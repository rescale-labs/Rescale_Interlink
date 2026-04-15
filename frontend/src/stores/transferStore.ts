import { create } from 'zustand'
import * as App from '../../wailsjs/go/wailsapp/App'
import { wailsapp } from '../../wailsjs/go/models'
import { EventsOn } from '../../wailsjs/runtime/runtime'
import { ProgressEventDTO, TransferEventDTO, EnumerationEventDTO, BatchProgressEventDTO, EVENT_NAMES } from '../types/events'

// Transfer task state
export type TransferState = 'queued' | 'initializing' | 'active' | 'completed' | 'failed' | 'cancelled' | 'paused'

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

export interface Enumeration {
  id: string
  folderName: string
  direction: 'upload' | 'download'
  foldersFound: number
  filesFound: number
  bytesFound: number
  isComplete: boolean
  error?: string
  statusMessage?: string
  completedAt?: number      // Timestamp when isComplete was set
  lastEventAt: number       // Timestamp of last event received (for staleness-based fallback)
  phase?: string            // "scanning", "creating_folders", "complete", "error"
  foldersTotal?: number     // Total folders to create
  foldersCreated?: number   // Folders created so far
}

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
  totalKnown: boolean // True when scan complete, total is final
  filesPerSec: number // File completion rate (windowed)
  etaSeconds: number // Estimated time remaining (-1 = unknown)
  discoveredTotal: number // Files discovered by scan
  discoveredBytes: number // Bytes discovered by scan
  startedAtUnix: number // Batch start time (Unix seconds)
}

// Extended transfer task with UI state
export interface TransferTask extends wailsapp.TransferTaskDTO {
  // UI state
  displayProgress: number // Smoothed progress for display
  speedFormatted: string // Formatted speed string
  etaFormatted: string // Formatted ETA string
  errorType?: TransferErrorType
}

// Transfer statistics
export interface TransferStats extends wailsapp.TransferStatsDTO {
  totalActive: number
}

// Daemon auto-download batch (read-only, from IPC)
export interface DaemonBatchStatus {
  batchID: string
  batchLabel: string
  total: number
  completed: number
  failed: number
  active: number
  totalBytes: number
  bytesDone: number
  speed: number
  startedAt: number   // unix millis
  completedAt: number // zero if active
}

interface TransferStore {
  // State
  tasks: TransferTask[]
  stats: TransferStats
  enumerations: Enumeration[]
  batches: TransferBatch[]
  daemonBatches: DaemonBatchStatus[]
  expandedBatches: Set<string>
  batchTasks: Map<string, TransferTask[]>
  batchEpochs: Map<string, number> // Epoch counter per batch for stale-response protection
  batchStatusFilter: Map<string, string> // Per-batch status filter ("" = all, "active", "completed", "failed", "cancelled")
  folderCheckStatus: { folderName: string } | null
  isLoading: boolean
  error: string | null
  isPolling: boolean
  lastUpdate: number

  // Actions
  fetchTasks: () => Promise<void>
  fetchStats: () => Promise<void>
  fetchBatches: () => Promise<void>
  fetchDaemonBatches: () => Promise<void>
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
  setBatchStatusFilter: (batchID: string, filter: string) => void
  handleProgressEvent: (event: ProgressEventDTO) => void
  handleTransferEvent: (event: TransferEventDTO) => void
  handleEnumerationEvent: (event: EnumerationEventDTO) => void
  handleBatchProgressEvent: (event: BatchProgressEventDTO) => void
  setFolderCheckStatus: (status: { folderName: string } | null) => void

  // App-level event listeners (always active, unlike polling which is tab-specific)
  setupEventListeners: () => () => void

  // Internal
  _pollInterval: ReturnType<typeof setInterval> | null
  _unsubscribeProgress: (() => void) | null
  _unsubscribeTransfer: (() => void) | null
  _unsubscribeEnumeration: (() => void) | null
  _unsubscribeBatchProgress: (() => void) | null
  _appEventListenersSetup: boolean
}

// Format speed in bytes/sec to human readable
// Defensive: handle undefined/NaN values (issue #18)
export function formatSpeed(bytesPerSec: number): string {
  // Handle undefined, NaN, or non-finite values
  if (typeof bytesPerSec !== 'number' || !Number.isFinite(bytesPerSec) || bytesPerSec <= 0) return ''
  const units = ['B/s', 'KB/s', 'MB/s', 'GB/s']
  const exp = Math.min(Math.floor(Math.log(bytesPerSec) / Math.log(1024)), units.length - 1)
  const speed = bytesPerSec / Math.pow(1024, exp)
  return `${speed.toFixed(1)} ${units[exp]}`
}

// Format ETA in milliseconds to human readable
// Defensive: handle undefined/NaN values (issue #18)
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
  enumerations: [],
  batches: [],
  daemonBatches: [],
  expandedBatches: new Set<string>(),
  batchTasks: new Map<string, TransferTask[]>(),
  batchEpochs: new Map<string, number>(), // Epoch counter per batch for stale-response protection
  batchStatusFilter: new Map<string, string>(),
  folderCheckStatus: null,
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

  // Fetch only ungrouped tasks (no batchID) -- lightweight for large batches
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

  fetchBatches: async () => {
    try {
      const raw = await App.GetTransferBatches()
      // Map DTO to TransferBatch (totalKnown defaults true for non-streaming batches)
      const batches: TransferBatch[] = (raw || []).map((b) => ({
        ...b,
        totalKnown: b.totalKnown ?? true,
        filesPerSec: (b as TransferBatch).filesPerSec ?? 0,
        etaSeconds: (b as TransferBatch).etaSeconds ?? -1,
        discoveredTotal: (b as TransferBatch).discoveredTotal ?? 0,
        discoveredBytes: (b as TransferBatch).discoveredBytes ?? 0,
        startedAtUnix: (b as TransferBatch).startedAtUnix ?? 0,
      }))
      set({ batches })

      // Refresh expanded batch tasks
      const expanded = get().expandedBatches
      for (const batchID of expanded) {
        get().fetchBatchTasks(batchID, 0, 50)
      }

      // Enumeration-to-batch reconciliation (4-layer removal)
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

  fetchDaemonBatches: async () => {
    try {
      const batches = await App.GetDaemonTransfers()
      set({ daemonBatches: batches || [] })
    } catch {
      // Silent fail — daemon may not be running or may not support this
    }
  },

  // Merge + dedupe + epoch guard to preserve "Show more" pagination across polling
  fetchBatchTasks: async (batchID: string, offset: number, limit: number) => {
    try {
      const stateFilter = get().batchStatusFilter.get(batchID) || ''
      // Capture epoch before the async call
      const epochBefore = get().batchEpochs.get(batchID) ?? 0
      const tasks = await App.GetBatchTasks(batchID, offset, limit, stateFilter)
      const enhanced = (tasks || []).map(enhanceTask)

      // After await: check if batch was invalidated while request was in flight
      const epochAfter = get().batchEpochs.get(batchID) ?? 0
      // Stale response guard: discard if epoch changed during async call
      if (epochAfter !== epochBefore) return

      set(state => {
        const newMap = new Map(state.batchTasks)
        const existing = newMap.get(batchID) || []

        if (offset === 0) {
          // When a status filter is active, always replace on offset-0 refresh.
          // Tasks may have left the filter between polls (e.g., a retried task moves
          // from "failed" to "active") so stale entries must not survive.
          if (stateFilter) {
            newMap.set(batchID, enhanced)
          } else if (enhanced.length < existing.length) {
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

    const pollInterval = setInterval(() => {
      get().fetchBatches()
      get().fetchUngroupedTasks()
      get().fetchStats()
      get().fetchDaemonBatches()
    }, intervalMs)

    // Subscribe to progress events for real-time updates (legacy PUR jobs)
    const unsubscribeProgress = EventsOn(EVENT_NAMES.PROGRESS, (event: ProgressEventDTO) => {
      get().handleProgressEvent(event)
    })

    // Transfer and enumeration events are subscribed at app level
    // via setupEventListeners() so they persist when navigating away

    // Initial fetch
    get().fetchBatches()
    get().fetchUngroupedTasks()
    get().fetchStats()
    get().fetchDaemonBatches()

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

    // Only unsubscribe from progress events (legacy PUR)
    // Transfer and enumeration events are handled at app level
    if (_unsubscribeProgress) {
      _unsubscribeProgress()
    }

    set({
      isPolling: false,
      _pollInterval: null,
      _unsubscribeProgress: null,
      // Don't clear enumerations - they persist while scanning
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

  cancelAllTransfers: async () => {
    try {
      // Cancel all active batches
      const activeBatches = get().batches.filter(
        b => b.queued > 0 || b.active > 0 || !b.totalKnown
      )
      for (const batch of activeBatches) {
        await App.CancelBatch(batch.batchID)
      }

      // Cancel remaining ungrouped active tasks
      const activeTasks = get().tasks.filter(
        t => ['queued', 'initializing', 'active', 'paused'].includes(t.state)
      )
      for (const task of activeTasks) {
        await App.CancelTransfer(task.id)
      }

      get().fetchBatches()
      get().fetchUngroupedTasks()
    } catch (error) {
      console.error('Failed to cancel transfers:', error)
    }
  },

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

  toggleBatchExpanded: (batchID: string) => {
    const expanded = new Set(get().expandedBatches)
    if (expanded.has(batchID)) {
      expanded.delete(batchID)
      // Clear cached tasks to free memory; bump epoch to discard any in-flight stale responses
      const newMap = new Map(get().batchTasks)
      newMap.delete(batchID)
      const newEpochs = new Map(get().batchEpochs)
      newEpochs.set(batchID, (newEpochs.get(batchID) ?? 0) + 1)
      const newFilters = new Map(get().batchStatusFilter)
      newFilters.delete(batchID)
      set({ expandedBatches: expanded, batchTasks: newMap, batchEpochs: newEpochs, batchStatusFilter: newFilters })
    } else {
      expanded.add(batchID)
      set({ expandedBatches: expanded })
      // Fetch first page of tasks
      get().fetchBatchTasks(batchID, 0, 50)
    }
  },

  setFolderCheckStatus: (status) => set({ folderCheckStatus: status }),

  // Clears cached tasks, bumps epoch, and re-fetches page 0 with the new filter.
  setBatchStatusFilter: (batchID: string, filter: string) => {
    const newFilters = new Map(get().batchStatusFilter)
    if (filter) {
      newFilters.set(batchID, filter)
    } else {
      newFilters.delete(batchID)
    }
    // Clear cached tasks and bump epoch to invalidate in-flight responses
    const newMap = new Map(get().batchTasks)
    newMap.delete(batchID)
    const newEpochs = new Map(get().batchEpochs)
    newEpochs.set(batchID, (newEpochs.get(batchID) ?? 0) + 1)
    set({ batchStatusFilter: newFilters, batchTasks: newMap, batchEpochs: newEpochs })
    // Re-fetch with new filter
    get().fetchBatchTasks(batchID, 0, 50)
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

  handleBatchProgressEvent: (event: BatchProgressEventDTO) => {
    set(state => {
      const batchIndex = state.batches.findIndex(b => b.batchID === event.batchID)
      if (batchIndex === -1) {
        // Upsert -- create batch from PreRegisterBatch's immediate event
        // Clear pre-check row -- batch row is now visible as replacement
        const newBatch: TransferBatch = {
          batchID: event.batchID,
          batchLabel: event.label,
          direction: event.direction,
          sourceLabel: '',
          total: event.total,
          queued: event.queued,
          active: event.active,
          completed: event.completed,
          failed: event.failed,
          cancelled: 0,
          totalBytes: 0,
          progress: event.progress,
          speed: event.speed,
          totalKnown: event.totalKnown,
          filesPerSec: event.filesPerSec ?? 0,
          etaSeconds: event.etaSeconds ?? -1,
          discoveredTotal: event.discoveredTotal ?? 0,
          discoveredBytes: event.discoveredBytes ?? 0,
          startedAtUnix: 0, // Will be populated on next fetchBatches
        }
        return { batches: [...state.batches, newBatch], lastUpdate: Date.now(), folderCheckStatus: null }
      }

      const updatedBatches = [...state.batches]
      updatedBatches[batchIndex] = {
        ...updatedBatches[batchIndex],
        total: event.total,         // Evolving total during streaming scan
        active: event.active,
        queued: event.queued,
        completed: event.completed,
        failed: event.failed,
        progress: event.progress,
        speed: event.speed,
        totalKnown: event.totalKnown,
        filesPerSec: event.filesPerSec ?? 0,
        etaSeconds: event.etaSeconds ?? -1,
        discoveredTotal: event.discoveredTotal ?? 0,
        discoveredBytes: event.discoveredBytes ?? 0,
      }
      return { batches: updatedBatches, lastUpdate: Date.now() }
    })
  },

  // Does not remove on completion -- removal is handled by fetchBatches reconciliation.
  // Ignores non-complete events for enumerations that already have a matching batch
  // (prevents phantom "Scanning" row flash during active downloads).
  handleEnumerationEvent: (event: EnumerationEventDTO) => {
    set(state => {
      // Defense-in-depth: if a batch already exists for this enumeration,
      // ignore non-complete progress events (batch row handles progress display)
      if (!event.isComplete) {
        const hasBatch = state.batches.some(b => b.batchID === event.id)
        if (hasBatch) return state
      }

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
            phase: event.phase,
            foldersTotal: event.foldersTotal,
            foldersCreated: event.foldersCreated,
          }
          return { enumerations: updated, folderCheckStatus: null }
        }
        return { folderCheckStatus: null }
      }

      // Clear pre-check row when a visible replacement appears.
      // Upload enumerations in creating_folders phase are shown by TransfersTab filter,
      // so they provide visual continuity from the pre-check row.
      const clearPreCheck = event.phase === 'creating_folders'

      if (existingIndex === -1) {
        // New enumeration - add it
        return {
          ...(clearPreCheck ? { folderCheckStatus: null } : {}),
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
            phase: event.phase,
            foldersTotal: event.foldersTotal,
            foldersCreated: event.foldersCreated,
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
          phase: event.phase,
          foldersTotal: event.foldersTotal,
          foldersCreated: event.foldersCreated,
        }
        return {
          ...(clearPreCheck ? { folderCheckStatus: null } : {}),
          enumerations: updated,
        }
      }
    })
  },

  // Set up event listeners at app level (always active).
  // This ensures enumeration and transfer events are captured even when not on Transfers tab.
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
