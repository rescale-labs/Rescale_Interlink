import { create } from 'zustand'
import * as App from '../../wailsjs/go/wailsapp/App'
import { wailsapp } from '../../wailsjs/go/models'
import { EventsOn } from '../../wailsjs/runtime/runtime'
import { ProgressEventDTO, TransferEventDTO, EnumerationEventDTO, EVENT_NAMES } from '../types/events'

// Transfer task state
export type TransferState = 'queued' | 'initializing' | 'active' | 'completed' | 'failed' | 'cancelled' | 'paused'

// v4.7.1: Error classification for disk space and other error types
export type TransferErrorType = 'disk_space' | 'generic'

export function classifyError(error: string | undefined): TransferErrorType {
  if (!error) return 'generic'
  const lower = error.toLowerCase()
  if (
    lower.includes('insufficient disk space') ||
    lower.includes('no space left on device') ||
    lower.includes('disk full') ||
    lower.includes('out of disk space') ||
    lower.includes('not enough space') ||
    lower.includes('disk quota exceeded') ||
    lower.includes('enospc')
  ) return 'disk_space'
  return 'generic'
}

export function extractDiskSpaceInfo(error: string): { available: string; needed: string } | null {
  const availMatch = error.match(/have ([\d.]+\s*[KMGT]?B) available/i)
  const needMatch = error.match(/need ([\d.]+\s*[KMGT]?B)/i)
  if (!availMatch) return null
  return { available: availMatch[1], needed: needMatch ? needMatch[1] : 'unknown' }
}

// v4.0.8: Enumeration state for folder scan progress
export interface Enumeration {
  id: string
  folderName: string
  direction: 'upload' | 'download'
  foldersFound: number
  filesFound: number
  bytesFound: number
  isComplete: boolean
  error?: string
}

// Extended transfer task with UI state
export interface TransferTask extends wailsapp.TransferTaskDTO {
  // UI state
  displayProgress: number // Smoothed progress for display
  speedFormatted: string // Formatted speed string
  etaFormatted: string // Formatted ETA string
  errorType?: TransferErrorType // v4.7.1: Classified error type
}

// Transfer statistics
export interface TransferStats extends wailsapp.TransferStatsDTO {
  totalActive: number
}

interface TransferStore {
  // State
  tasks: TransferTask[]
  stats: TransferStats
  enumerations: Enumeration[] // v4.0.8: Active folder scans
  isLoading: boolean
  error: string | null
  isPolling: boolean
  lastUpdate: number

  // Actions
  fetchTasks: () => Promise<void>
  fetchStats: () => Promise<void>
  startPolling: (intervalMs?: number) => void
  stopPolling: () => void
  cancelTransfer: (taskId: string) => Promise<void>
  cancelAllTransfers: () => Promise<void>
  retryTransfer: (taskId: string) => Promise<string | null>
  clearCompletedTransfers: () => void
  handleProgressEvent: (event: ProgressEventDTO) => void
  handleTransferEvent: (event: TransferEventDTO) => void
  handleEnumerationEvent: (event: EnumerationEventDTO) => void // v4.0.8

  // v4.0.8: App-level event listeners (always active, unlike polling which is tab-specific)
  setupEventListeners: () => () => void

  // Internal
  _pollInterval: ReturnType<typeof setInterval> | null
  _unsubscribeProgress: (() => void) | null
  _unsubscribeTransfer: (() => void) | null
  _unsubscribeEnumeration: (() => void) | null // v4.0.8
  _appEventListenersSetup: boolean // v4.0.8: Track if app-level listeners are set up
}

// Format speed in bytes/sec to human readable
// v4.0.5: Added defensive handling for undefined/NaN values (issue #18)
function formatSpeed(bytesPerSec: number): string {
  // Handle undefined, NaN, or non-finite values
  if (typeof bytesPerSec !== 'number' || !Number.isFinite(bytesPerSec) || bytesPerSec <= 0) return ''
  const units = ['B/s', 'KB/s', 'MB/s', 'GB/s']
  const exp = Math.min(Math.floor(Math.log(bytesPerSec) / Math.log(1024)), units.length - 1)
  const speed = bytesPerSec / Math.pow(1024, exp)
  return `${speed.toFixed(1)} ${units[exp]}`
}

// Format ETA in milliseconds to human readable
// v4.0.5: Added defensive handling for undefined/NaN values (issue #18)
function formatETA(etaMs: number): string {
  // Handle undefined, NaN, or non-finite values
  if (typeof etaMs !== 'number' || !Number.isFinite(etaMs) || etaMs <= 0) return ''
  const seconds = Math.floor(etaMs / 1000)
  if (seconds < 60) return `${seconds}s`
  const minutes = Math.floor(seconds / 60)
  const remainingSeconds = seconds % 60
  if (minutes < 60) return `${minutes}m ${remainingSeconds}s`
  const hours = Math.floor(minutes / 60)
  const remainingMinutes = minutes % 60
  return `${hours}h ${remainingMinutes}m`
}

// Convert DTO to enhanced TransferTask
function enhanceTask(dto: wailsapp.TransferTaskDTO): TransferTask {
  return {
    ...dto,
    displayProgress: dto.progress,
    speedFormatted: formatSpeed(dto.speed),
    etaFormatted: dto.speed > 0 && dto.size > 0
      ? formatETA(((dto.size * (1 - dto.progress)) / dto.speed) * 1000)
      : '',
    errorType: classifyError(dto.error),
  }
}

const initialStats: TransferStats = {
  queued: 0,
  initializing: 0,
  active: 0,
  paused: 0,
  completed: 0,
  failed: 0,
  cancelled: 0,
  total: 0,
  totalActive: 0,
}

export const useTransferStore = create<TransferStore>((set, get) => ({
  tasks: [],
  stats: initialStats,
  enumerations: [], // v4.0.8: Active folder scans
  isLoading: false,
  error: null,
  isPolling: false,
  lastUpdate: 0,
  _pollInterval: null,
  _unsubscribeProgress: null,
  _unsubscribeTransfer: null,
  _unsubscribeEnumeration: null,
  _appEventListenersSetup: false,

  fetchTasks: async () => {
    try {
      const tasks = await App.GetTransferTasks()
      set({
        tasks: (tasks || []).map(enhanceTask),
        lastUpdate: Date.now(),
        error: null,
      })
    } catch (error) {
      set({ error: error instanceof Error ? error.message : String(error) })
    }
  },

  fetchStats: async () => {
    try {
      const stats = await App.GetTransferStats()
      set({
        stats: {
          ...stats,
          totalActive: stats.queued + stats.initializing + stats.active + stats.paused,
        },
      })
    } catch (error) {
      // Silent fail for stats
      console.error('Failed to fetch transfer stats:', error)
    }
  },

  startPolling: (intervalMs = 500) => {
    const state = get()

    // Already polling
    if (state.isPolling) return

    // Start polling for task list
    const pollInterval = setInterval(() => {
      get().fetchTasks()
      get().fetchStats()
    }, intervalMs)

    // Subscribe to progress events for real-time updates (legacy PUR jobs)
    const unsubscribeProgress = EventsOn(EVENT_NAMES.PROGRESS, (event: ProgressEventDTO) => {
      get().handleProgressEvent(event)
    })

    // v4.0.8: Transfer and enumeration events are now subscribed at app level
    // via setupEventListeners() so they persist when navigating away

    // Initial fetch
    get().fetchTasks()
    get().fetchStats()

    set({
      isPolling: true,
      _pollInterval: pollInterval,
      _unsubscribeProgress: unsubscribeProgress,
    })
  },

  stopPolling: () => {
    const { _pollInterval, _unsubscribeProgress } = get()

    if (_pollInterval) {
      clearInterval(_pollInterval)
    }

    // v4.0.8: Only unsubscribe from progress events (legacy PUR)
    // Transfer and enumeration events are now handled at app level
    if (_unsubscribeProgress) {
      _unsubscribeProgress()
    }

    set({
      isPolling: false,
      _pollInterval: null,
      _unsubscribeProgress: null,
      // v4.0.8: Don't clear enumerations - they persist while scanning
    })
  },

  cancelTransfer: async (taskId: string) => {
    try {
      await App.CancelTransfer(taskId)
      // Refresh tasks
      get().fetchTasks()
    } catch (error) {
      console.error('Failed to cancel transfer:', error)
    }
  },

  cancelAllTransfers: async () => {
    try {
      await App.CancelAllTransfers()
      // Refresh tasks
      get().fetchTasks()
    } catch (error) {
      console.error('Failed to cancel all transfers:', error)
    }
  },

  retryTransfer: async (taskId: string) => {
    try {
      const newTaskId = await App.RetryTransfer(taskId)
      // Refresh tasks
      get().fetchTasks()
      return newTaskId
    } catch (error) {
      console.error('Failed to retry transfer:', error)
      return null
    }
  },

  clearCompletedTransfers: () => {
    App.ClearCompletedTransfers()
    // Refresh tasks
    get().fetchTasks()
  },

  handleProgressEvent: (event: ProgressEventDTO) => {
    // Update matching task with real-time progress (legacy: match by name)
    set(state => {
      const taskIndex = state.tasks.findIndex(t => t.name === event.jobName)
      if (taskIndex === -1) return state

      const updatedTasks = [...state.tasks]
      const task = { ...updatedTasks[taskIndex] }

      // Update progress from event
      task.progress = event.progress
      task.displayProgress = event.progress
      task.speed = event.rateBytes
      task.speedFormatted = formatSpeed(event.rateBytes)
      task.etaFormatted = formatETA(event.etaMs)

      updatedTasks[taskIndex] = task
      return { tasks: updatedTasks, lastUpdate: Date.now() }
    })
  },

  handleTransferEvent: (event: TransferEventDTO) => {
    // Update matching task with real-time progress (match by taskId)
    set(state => {
      const taskIndex = state.tasks.findIndex(t => t.id === event.taskId)
      if (taskIndex === -1) return state

      const updatedTasks = [...state.tasks]
      const task = { ...updatedTasks[taskIndex] }

      // Update progress from transfer event
      task.progress = event.progress
      task.displayProgress = event.progress
      task.speed = event.speed
      task.speedFormatted = formatSpeed(event.speed)
      // Calculate ETA: remaining bytes / speed
      if (event.speed > 0 && event.size > 0) {
        const remainingBytes = event.size * (1 - event.progress)
        task.etaFormatted = formatETA((remainingBytes / event.speed) * 1000)
      } else {
        task.etaFormatted = ''
      }

      // Update error if present
      if (event.error) {
        task.error = event.error
        task.errorType = classifyError(event.error)
      }

      updatedTasks[taskIndex] = task
      return { tasks: updatedTasks, lastUpdate: Date.now() }
    })
  },

  // v4.0.8: Handle enumeration events for folder scan progress
  handleEnumerationEvent: (event: EnumerationEventDTO) => {
    set(state => {
      const existingIndex = state.enumerations.findIndex(e => e.id === event.id)

      if (event.isComplete) {
        // Remove completed enumeration after a short delay (let user see final count)
        if (existingIndex !== -1) {
          const updated = [...state.enumerations]
          updated[existingIndex] = {
            ...updated[existingIndex],
            foldersFound: event.foldersFound,
            filesFound: event.filesFound,
            bytesFound: event.bytesFound,
            isComplete: true,
            error: event.error,
          }
          // Remove after 2 seconds
          setTimeout(() => {
            set(s => ({
              enumerations: s.enumerations.filter(e => e.id !== event.id)
            }))
          }, 2000)
          return { enumerations: updated }
        }
        return state
      }

      if (existingIndex === -1) {
        // New enumeration - add it
        return {
          enumerations: [...state.enumerations, {
            id: event.id,
            folderName: event.folderName,
            direction: event.direction,
            foldersFound: event.foldersFound,
            filesFound: event.filesFound,
            bytesFound: event.bytesFound,
            isComplete: event.isComplete,
            error: event.error,
          }]
        }
      } else {
        // Update existing enumeration
        const updated = [...state.enumerations]
        updated[existingIndex] = {
          ...updated[existingIndex],
          foldersFound: event.foldersFound,
          filesFound: event.filesFound,
          bytesFound: event.bytesFound,
        }
        return { enumerations: updated }
      }
    })
  },

  // v4.0.8: Set up event listeners at app level (always active)
  // This ensures enumeration and transfer events are captured even when not on Transfers tab
  setupEventListeners: () => {
    const state = get()
    if (state._appEventListenersSetup) {
      // Already set up, return no-op cleanup
      return () => {}
    }

    // Subscribe to transfer events
    const unsubscribeTransfer = EventsOn(EVENT_NAMES.TRANSFER, (event: TransferEventDTO) => {
      get().handleTransferEvent(event)
    })

    // Subscribe to enumeration events
    const unsubscribeEnumeration = EventsOn(EVENT_NAMES.ENUMERATION, (event: EnumerationEventDTO) => {
      get().handleEnumerationEvent(event)
    })

    set({ _appEventListenersSetup: true })

    // Return cleanup function
    return () => {
      unsubscribeTransfer()
      unsubscribeEnumeration()
      set({ _appEventListenersSetup: false })
    }
  },
}))
