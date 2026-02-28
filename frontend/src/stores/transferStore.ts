import { create } from 'zustand'
import * as App from '../../wailsjs/go/wailsapp/App'
import { wailsapp } from '../../wailsjs/go/models'
import { EventsOn } from '../../wailsjs/runtime/runtime'
import { ProgressEventDTO, TransferEventDTO, EnumerationEventDTO, BatchProgressEventDTO, EVENT_NAMES } from '../types/events'

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
// v4.7.7: Added statusMessage, completedAt, lastEventAt for seamless batch transition
export interface Enumeration {
  id: string
  folderName: string
  direction: 'upload' | 'download'
  foldersFound: number
  filesFound: number
  bytesFound: number
  isComplete: boolean
  error?: string
  statusMessage?: string    // v4.7.7: Human-readable status (e.g. "Creating folders... (3 of 47)")
  completedAt?: number      // v4.7.7: Timestamp when isComplete was set
  lastEventAt: number       // v4.7.7: Timestamp of last event received (for staleness-based fallback)
}

// v4.7.7: Transfer batch for grouped display
export interface TransferBatch {
  batchID: string
  batchLabel: string
  direction: string
  sourceLabel: string
  total: number
  queued: number
  active: number
  completed: number
  failed: number
  cancelled: number
  totalBytes: number
  progress: number
  speed: number
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
  batches: TransferBatch[] // v4.7.7: Batch aggregates
  expandedBatches: Set<string> // v4.7.7: Which batches are expanded
  batchTasks: Map<string, TransferTask[]> // v4.7.7: Lazily loaded expanded tasks
  batchEpochs: Map<string, number> // v4.7.7: Epoch counter per batch for stale-response protection
  isLoading: boolean
  error: string | null
  isPolling: boolean
  lastUpdate: number

  // Actions
  fetchTasks: () => Promise<void>
  fetchStats: () => Promise<void>
  fetchBatches: () => Promise<void>
  fetchUngroupedTasks: () => Promise<void>
  fetchBatchTasks: (batchID: string, offset: number, limit: number) => Promise<void>
  startPolling: (intervalMs?: number) => void
  stopPolling: () => void
  cancelTransfer: (taskId: string) => Promise<void>
  cancelAllTransfers: () => Promise<void>
  cancelBatch: (batchID: string) => Promise<void>
  retryTransfer: (taskId: string) => Promise<string | null>
  retryFailedInBatch: (batchID: string) => Promise<void>
  clearCompletedTransfers: () => void
  toggleBatchExpanded: (batchID: string) => void
  handleProgressEvent: (event: ProgressEventDTO) => void
  handleTransferEvent: (event: TransferEventDTO) => void
  handleEnumerationEvent: (event: EnumerationEventDTO) => void // v4.0.8
  handleBatchProgressEvent: (event: BatchProgressEventDTO) => void // v4.7.7

  // v4.0.8: App-level event listeners (always active, unlike polling which is tab-specific)
  setupEventListeners: () => () => void

  // Internal
  _pollInterval: ReturnType<typeof setInterval> | null
  _unsubscribeProgress: (() => void) | null
  _unsubscribeTransfer: (() => void) | null
  _unsubscribeEnumeration: (() => void) | null // v4.0.8
  _unsubscribeBatchProgress: (() => void) | null // v4.7.7
  _appEventListenersSetup: boolean // v4.0.8: Track if app-level listeners are set up
}

// Format speed in bytes/sec to human readable
// v4.0.5: Added defensive handling for undefined/NaN values (issue #18)
export function formatSpeed(bytesPerSec: number): string {
  // Handle undefined, NaN, or non-finite values
  if (typeof bytesPerSec !== 'number' || !Number.isFinite(bytesPerSec) || bytesPerSec <= 0) return ''
  const units = ['B/s', 'KB/s', 'MB/s', 'GB/s']
  const exp = Math.min(Math.floor(Math.log(bytesPerSec) / Math.log(1024)), units.length - 1)
  const speed = bytesPerSec / Math.pow(1024, exp)
  return `${speed.toFixed(1)} ${units[exp]}`
}

// Format ETA in milliseconds to human readable
// v4.0.5: Added defensive handling for undefined/NaN values (issue #18)
export function formatETA(etaMs: number): string {
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
  batches: [], // v4.7.7
  expandedBatches: new Set<string>(), // v4.7.7
  batchTasks: new Map<string, TransferTask[]>(), // v4.7.7
  batchEpochs: new Map<string, number>(), // v4.7.7: epoch counter per batch for stale-response protection
  isLoading: false,
  error: null,
  isPolling: false,
  lastUpdate: 0,
  _pollInterval: null,
  _unsubscribeProgress: null,
  _unsubscribeTransfer: null,
  _unsubscribeEnumeration: null,
  _unsubscribeBatchProgress: null,
  _appEventListenersSetup: false,

  // v4.7.7: Fetch only ungrouped tasks (no batchID) — lightweight for large batches
  fetchUngroupedTasks: async () => {
    try {
      const tasks = await App.GetUngroupedTransferTasks()
      set({
        tasks: (tasks || []).map(enhanceTask),
        lastUpdate: Date.now(),
        error: null,
      })
    } catch (error) {
      set({ error: error instanceof Error ? error.message : String(error) })
    }
  },

  // v4.7.7: Fetch batch aggregates + reconcile enumerations for seamless transition
  fetchBatches: async () => {
    try {
      const batches = await App.GetTransferBatches()
      set({ batches: batches || [] })

      // Refresh expanded batch tasks
      const expanded = get().expandedBatches
      for (const batchID of expanded) {
        get().fetchBatchTasks(batchID, 0, 50)
      }

      // v4.7.7: Enumeration-to-batch reconciliation (4-layer removal)
      const currentBatches = batches || []
      const batchIDs = new Set(currentBatches.map(b => b.batchID))
      const now = Date.now()
      const enumerations = get().enumerations
      const toRemove: string[] = []

      for (const e of enumerations) {
        const hasMatchingBatch = batchIDs.has(e.id)

        if (e.isComplete && e.completedAt && hasMatchingBatch) {
          // Layer 1: isComplete + matching batch → remove after 500ms
          if (now - e.completedAt >= 500) {
            toRemove.push(e.id)
          }
        } else if (!e.isComplete && hasMatchingBatch) {
          // Layer 2: !isComplete but matching batch found → immediate removal
          // (handles dropped EventEnumerationCompleted)
          toRemove.push(e.id)
        } else if (!e.isComplete && !hasMatchingBatch && (now - e.lastEventAt > 30000)) {
          // Layer 3: !isComplete, no matching batch, stale for 30s → remove
          // Uses lastEventAt (staleness) not createdAt (absolute age) to avoid
          // prematurely removing long-running folder creation progress
          toRemove.push(e.id)
        } else if (e.isComplete && e.completedAt && !hasMatchingBatch && (now - e.completedAt > 10000)) {
          // Completed but no batch appeared after 10s (error path, empty folder, zero files)
          toRemove.push(e.id)
        }
      }

      if (toRemove.length > 0) {
        const removeSet = new Set(toRemove)
        set(s => ({
          enumerations: s.enumerations.filter(e => !removeSet.has(e.id))
        }))
      }
    } catch (error) {
      console.error('Failed to fetch batches:', error)
    }
  },

  // v4.7.7: Fetch paginated tasks for an expanded batch
  // v4.7.7: Merge + dedupe + epoch guard to preserve "Show more" pagination across polling
  fetchBatchTasks: async (batchID: string, offset: number, limit: number) => {
    try {
      // Capture epoch before the async call
      const epochBefore = get().batchEpochs.get(batchID) ?? 0
      const tasks = await App.GetBatchTasks(batchID, offset, limit)
      const enhanced = (tasks || []).map(enhanceTask)

      // After await: check if batch was invalidated while request was in flight
      const epochAfter = get().batchEpochs.get(batchID) ?? 0
      if (epochAfter !== epochBefore) return // Stale response — drop it

      set(state => {
        const newMap = new Map(state.batchTasks)
        const existing = newMap.get(batchID) || []

        if (offset === 0) {
          if (enhanced.length < existing.length) {
            // Poll returned fewer items than user has loaded via "Show more".
            // Check prefix alignment to detect composition changes.
            const prefixAligned = enhanced.length > 0 && enhanced.every(
              (t, i) => i < existing.length && existing[i].id === t.id
            )
            if (prefixAligned) {
              // IDs match — merge fresh first page + keep tail, dedupe.
              const freshIds = new Set(enhanced.map(t => t.id))
              const tail = existing.slice(enhanced.length).filter(t => !freshIds.has(t.id))
              newMap.set(batchID, [...enhanced, ...tail])
            } else {
              // Composition changed — replace entirely.
              newMap.set(batchID, enhanced)
            }
          } else {
            // Normal case: fresh data is same size or larger — replace.
            newMap.set(batchID, enhanced)
          }
        } else {
          // Append path ("Show more") — dedupe to handle rapid clicks.
          const existingIds = new Set(existing.map(t => t.id))
          const deduped = enhanced.filter(t => !existingIds.has(t.id))
          newMap.set(batchID, [...existing, ...deduped])
        }

        return { batchTasks: newMap }
      })
    } catch (error) {
      console.error('Failed to fetch batch tasks:', error)
    }
  },

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

    // v4.7.7: Poll batches + ungrouped tasks instead of all tasks
    const pollInterval = setInterval(() => {
      get().fetchBatches()
      get().fetchUngroupedTasks()
      get().fetchStats()
    }, intervalMs)

    // Subscribe to progress events for real-time updates (legacy PUR jobs)
    const unsubscribeProgress = EventsOn(EVENT_NAMES.PROGRESS, (event: ProgressEventDTO) => {
      get().handleProgressEvent(event)
    })

    // v4.0.8: Transfer and enumeration events are now subscribed at app level
    // via setupEventListeners() so they persist when navigating away

    // Initial fetch
    get().fetchBatches()
    get().fetchUngroupedTasks()
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
      get().fetchUngroupedTasks()
    } catch (error) {
      console.error('Failed to cancel transfer:', error)
    }
  },

  // v4.7.4: Only cancel FileBrowser-owned transfers (pipeline manages its own retry/cancel)
  cancelAllTransfers: async () => {
    try {
      const activeTasks = get().tasks.filter(
        t => ['queued', 'initializing', 'active', 'paused'].includes(t.state) &&
             (!t.sourceLabel || t.sourceLabel === 'FileBrowser')
      )
      for (const task of activeTasks) {
        await App.CancelTransfer(task.id)
      }
      get().fetchUngroupedTasks()
    } catch (error) {
      console.error('Failed to cancel transfers:', error)
    }
  },

  // v4.7.7: Cancel all tasks in a batch
  cancelBatch: async (batchID: string) => {
    try {
      await App.CancelBatch(batchID)
      set(state => {
        const newMap = new Map(state.batchTasks)
        newMap.delete(batchID)
        const newEpochs = new Map(state.batchEpochs)
        newEpochs.set(batchID, (newEpochs.get(batchID) ?? 0) + 1)
        return { batchTasks: newMap, batchEpochs: newEpochs }
      })
      get().fetchBatches()
    } catch (error) {
      console.error('Failed to cancel batch:', error)
    }
  },

  retryTransfer: async (taskId: string) => {
    try {
      const newTaskId = await App.RetryTransfer(taskId)
      get().fetchUngroupedTasks()
      return newTaskId
    } catch (error) {
      console.error('Failed to retry transfer:', error)
      return null
    }
  },

  // v4.7.7: Retry all failed tasks in a batch
  retryFailedInBatch: async (batchID: string) => {
    try {
      await App.RetryFailedInBatch(batchID)
      set(state => {
        const newMap = new Map(state.batchTasks)
        newMap.delete(batchID)
        const newEpochs = new Map(state.batchEpochs)
        newEpochs.set(batchID, (newEpochs.get(batchID) ?? 0) + 1)
        return { batchTasks: newMap, batchEpochs: newEpochs }
      })
      get().fetchBatches()
    } catch (error) {
      console.error('Failed to retry failed in batch:', error)
    }
  },

  clearCompletedTransfers: () => {
    App.ClearCompletedTransfers()
    // Invalidate all expanded batch caches — composition changed
    set(state => {
      const newEpochs = new Map(state.batchEpochs)
      for (const batchID of state.expandedBatches) {
        newEpochs.set(batchID, (newEpochs.get(batchID) ?? 0) + 1)
      }
      return { batchTasks: new Map(), batchEpochs: newEpochs }
    })
    get().fetchBatches()
    get().fetchUngroupedTasks()
  },

  // v4.7.7: Toggle batch expanded/collapsed state
  toggleBatchExpanded: (batchID: string) => {
    const expanded = new Set(get().expandedBatches)
    if (expanded.has(batchID)) {
      expanded.delete(batchID)
      // Clear cached tasks to free memory; bump epoch to discard any in-flight stale responses
      const newMap = new Map(get().batchTasks)
      newMap.delete(batchID)
      const newEpochs = new Map(get().batchEpochs)
      newEpochs.set(batchID, (newEpochs.get(batchID) ?? 0) + 1)
      set({ expandedBatches: expanded, batchTasks: newMap, batchEpochs: newEpochs })
    } else {
      expanded.add(batchID)
      set({ expandedBatches: expanded })
      // Fetch first page of tasks
      get().fetchBatchTasks(batchID, 0, 50)
    }
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

  // v4.7.7: Handle batch progress events for real-time aggregate updates
  handleBatchProgressEvent: (event: BatchProgressEventDTO) => {
    set(state => {
      const batchIndex = state.batches.findIndex(b => b.batchID === event.batchID)
      if (batchIndex === -1) return state

      const updatedBatches = [...state.batches]
      updatedBatches[batchIndex] = {
        ...updatedBatches[batchIndex],
        completed: event.completed,
        failed: event.failed,
        progress: event.progress,
        speed: event.speed,
      }
      return { batches: updatedBatches, lastUpdate: Date.now() }
    })
  },

  // v4.0.8: Handle enumeration events for folder scan progress
  // v4.7.7: No longer removes on completion — removal is handled by fetchBatches reconciliation
  handleEnumerationEvent: (event: EnumerationEventDTO) => {
    set(state => {
      const existingIndex = state.enumerations.findIndex(e => e.id === event.id)
      const now = Date.now()

      if (event.isComplete) {
        // Mark as complete but do NOT start a removal timer — fetchBatches handles removal
        if (existingIndex !== -1) {
          const updated = [...state.enumerations]
          updated[existingIndex] = {
            ...updated[existingIndex],
            foldersFound: event.foldersFound,
            filesFound: event.filesFound,
            bytesFound: event.bytesFound,
            isComplete: true,
            completedAt: now,
            lastEventAt: now,
            error: event.error,
            statusMessage: event.statusMessage,
          }
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
            statusMessage: event.statusMessage,
            lastEventAt: now,
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
          statusMessage: event.statusMessage,
          lastEventAt: now,
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

    // v4.7.7: Subscribe to batch progress events
    const unsubscribeBatchProgress = EventsOn(EVENT_NAMES.BATCH_PROGRESS, (event: BatchProgressEventDTO) => {
      get().handleBatchProgressEvent(event)
    })

    set({ _appEventListenersSetup: true })

    // Return cleanup function
    return () => {
      unsubscribeTransfer()
      unsubscribeEnumeration()
      unsubscribeBatchProgress()
      set({ _appEventListenersSetup: false })
    }
  },
}))
