// Zustand store for safe error reporting.
// Manages the error report modal state and event subscription.

import { create } from 'zustand'
import { EventsOn } from '../../wailsjs/runtime/runtime'
import { EVENT_NAMES, type ReportableErrorEventDTO } from '../types/events'

// Error classes the backend already treats as user-fixable, kept here as
// defense in depth: if any backend path publishes one of these anyway, the
// modal must not interrupt the user for something they can read and fix
// themselves (a full disk, a protected folder, expired credentials, no network).
const SUPPRESSED_ERROR_CLASSES = new Set([
  'disk_space',
  'auth',
  'client_error',
  'network',
  'timeout',
  'local_fs',
])

// Minimum gap before the same errorID may open the modal again, so a duplicated
// or replayed event cannot reopen it repeatedly.
const REOPEN_COOLDOWN_MS = 60_000

// Bounds the seen-IDs map in a long session.
const MAX_TRACKED_ERROR_IDS = 200

interface ErrorReportStore {
  pendingError: ReportableErrorEventDTO | null
  isModalOpen: boolean
  isSaving: boolean
  lastResult: string | null // "Copied!" or "Saved to <path>"

  // Actions
  showError: (error: ReportableErrorEventDTO) => void
  dismiss: () => void
  setIsSaving: (saving: boolean) => void
  setLastResult: (result: string | null) => void

  // Event listener lifecycle
  setupEventListeners: () => () => void
  _eventListenersSetup: boolean
  _shownErrorIDs: Map<string, number>
}

export const useErrorReportStore = create<ErrorReportStore>((set, get) => ({
  pendingError: null,
  isModalOpen: false,
  isSaving: false,
  lastResult: null,
  _eventListenersSetup: false,
  _shownErrorIDs: new Map<string, number>(),

  showError: (error) => {
    // Duplicate suppression: if the modal is already open, drop subsequent events
    if (get().isModalOpen) {
      return
    }
    if (error?.errorClass && SUPPRESSED_ERROR_CLASSES.has(error.errorClass)) {
      return
    }

    const shown = get()._shownErrorIDs
    const now = Date.now()
    if (error?.errorID) {
      const last = shown.get(error.errorID)
      if (last !== undefined && now - last < REOPEN_COOLDOWN_MS) {
        return
      }
      shown.set(error.errorID, now)
      if (shown.size > MAX_TRACKED_ERROR_IDS) {
        // Drop the oldest entries; Map preserves insertion order.
        const excess = shown.size - MAX_TRACKED_ERROR_IDS
        let dropped = 0
        for (const key of shown.keys()) {
          if (dropped >= excess) break
          shown.delete(key)
          dropped++
        }
      }
    }

    set({ pendingError: error, isModalOpen: true, lastResult: null })
  },

  dismiss: () => {
    set({ pendingError: null, isModalOpen: false, isSaving: false, lastResult: null })
  },

  setIsSaving: (saving) => set({ isSaving: saving }),

  setLastResult: (result) => set({ lastResult: result }),

  setupEventListeners: () => {
    if (get()._eventListenersSetup) {
      return () => {}
    }
    set({ _eventListenersSetup: true })

    const cancelReportableError = EventsOn(EVENT_NAMES.REPORTABLE_ERROR, (data: ReportableErrorEventDTO) => {
      get().showError(data)
    })

    return () => {
      cancelReportableError()
      set({ _eventListenersSetup: false })
    }
  },
}))
