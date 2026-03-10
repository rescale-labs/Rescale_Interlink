// v4.8.7: Zustand store for safe error reporting (Plan 3, 6A-6E).
// Manages the error report modal state and event subscription.

import { create } from 'zustand'
import { EventsOn } from '../../wailsjs/runtime/runtime'
import { EVENT_NAMES, type ReportableErrorEventDTO } from '../types/events'

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
}

export const useErrorReportStore = create<ErrorReportStore>((set, get) => ({
  pendingError: null,
  isModalOpen: false,
  isSaving: false,
  lastResult: null,
  _eventListenersSetup: false,

  showError: (error) => {
    // Duplicate suppression: if the modal is already open, drop subsequent events
    if (get().isModalOpen) {
      return
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
