import { create } from 'zustand'
import * as App from '../../wailsjs/go/wailsapp/App'
import { wailsapp } from '../../wailsjs/go/models'
import { EventsOn } from '../../wailsjs/runtime/runtime'
import { ProgressEventDTO, TransferEventDTO, EVENT_NAMES } from '../types/events'

// Transfer task state
export type TransferState = 'queued' | 'initializing' | 'active' | 'completed' | 'failed' | 'cancelled' | 'paused'

// Extended transfer task with UI state
export interface TransferTask extends wailsapp.TransferTaskDTO {
  // UI state
  displayProgress: number // Smoothed progress for display
  speedFormatted: string // Formatted speed string
  etaFormatted: string // Formatted ETA string
}

// Transfer statistics
export interface TransferStats extends wailsapp.TransferStatsDTO {
  totalActive: number
}

interface TransferStore {
  // State
  tasks: TransferTask[]
  stats: TransferStats
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

  // Internal
  _pollInterval: ReturnType<typeof setInterval> | null
  _unsubscribeProgress: (() => void) | null
  _unsubscribeTransfer: (() => void) | null
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
  isLoading: false,
  error: null,
  isPolling: false,
  lastUpdate: 0,
  _pollInterval: null,
  _unsubscribeProgress: null,
  _unsubscribeTransfer: null,

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

    // Subscribe to transfer events for real-time updates (file uploads/downloads)
    const unsubscribeTransfer = EventsOn(EVENT_NAMES.TRANSFER, (event: TransferEventDTO) => {
      get().handleTransferEvent(event)
    })

    // Initial fetch
    get().fetchTasks()
    get().fetchStats()

    set({
      isPolling: true,
      _pollInterval: pollInterval,
      _unsubscribeProgress: unsubscribeProgress,
      _unsubscribeTransfer: unsubscribeTransfer,
    })
  },

  stopPolling: () => {
    const { _pollInterval, _unsubscribeProgress, _unsubscribeTransfer } = get()

    if (_pollInterval) {
      clearInterval(_pollInterval)
    }

    if (_unsubscribeProgress) {
      _unsubscribeProgress()
    }

    if (_unsubscribeTransfer) {
      _unsubscribeTransfer()
    }

    set({
      isPolling: false,
      _pollInterval: null,
      _unsubscribeProgress: null,
      _unsubscribeTransfer: null,
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
      }

      updatedTasks[taskIndex] = task
      return { tasks: updatedTasks, lastUpdate: Date.now() }
    })
  },
}))
