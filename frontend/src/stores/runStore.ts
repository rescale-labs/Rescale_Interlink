// v4.7.3: Central run state manager — tracks active run, completed run history, and job queue.
// Event listeners are app-level (set up from App.tsx), same pattern as logStore/transferStore.

import { create } from 'zustand'
import * as App from '../../wailsjs/go/wailsapp/App'
import { EventsOn } from '../../wailsjs/runtime/runtime'
import type { JobRow, PipelineLogEntry } from '../types/jobs'
import type {
  RunType,
  RunState,
  ActiveRun,
  CompletedRun,
  QueuedJob,
  PersistedActiveRun,
} from '../types/run'
import { computeStageStats } from '../utils/stageStats'
import type { StateChangeEventDTO, LogEventDTO, CompleteEventDTO } from '../types/events'

const ACTIVE_RUN_KEY = 'rescale-int-active-run'
const MAX_COMPLETED_RUNS = 20
const MAX_PIPELINE_LOGS = 200

// Known pipeline stages for log filtering
const PIPELINE_STAGES = new Set(['tar', 'upload', 'create', 'submit', 'pipeline'])

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

interface RunStore {
  activeRun: ActiveRun | null
  completedRuns: CompletedRun[]
  queuedJob: QueuedJob | null
  queueStatus: string | null  // 'queued' | 'starting' | 'started' | 'failed:...' | null
  purViewMode: 'auto' | 'monitor' | 'configure'

  // Actions
  registerRun: (runId: string, runType: RunType, totalJobs: number, initialJobRows: JobRow[]) => void
  clearActiveRun: () => Promise<void>
  setQueuedJob: (job: QueuedJob | null) => void
  setQueueStatus: (status: string | null) => void
  setPurViewMode: (mode: 'auto' | 'monitor' | 'configure') => void
  cancelRun: () => Promise<void>

  // App-level event listeners (called from App.tsx, always active)
  setupEventListeners: () => () => void

  // Restart recovery
  recoverFromRestart: () => Promise<void>

  // Polling for reconciliation
  startPolling: (intervalMs?: number) => void
  stopPolling: () => void

  // Internal
  _pollInterval: ReturnType<typeof setInterval> | null
  _eventListenersSetup: boolean
}

export const useRunStore = create<RunStore>((set, get) => ({
  activeRun: null,
  completedRuns: [],
  queuedJob: null,
  queueStatus: null,
  purViewMode: 'auto',

  _pollInterval: null,
  _eventListenersSetup: false,

  registerRun: (runId, runType, totalJobs, initialJobRows) => {
    const activeRun: ActiveRun = {
      runId,
      runType,
      startTime: Date.now(),
      status: 'active',
      totalJobs,
      completedJobs: 0,
      failedJobs: 0,
      durationMs: 0,
      jobRows: initialJobRows,
      pipelineStageStats: computeStageStats(initialJobRows),
      pipelineLogs: [],
    }
    set({ activeRun, purViewMode: 'auto' })

    // Persist to localStorage for restart recovery
    try {
      const persisted: PersistedActiveRun = {
        runId,
        runType,
        startTime: activeRun.startTime,
        totalJobs,
      }
      localStorage.setItem(ACTIVE_RUN_KEY, JSON.stringify(persisted))
    } catch { /* ignore localStorage errors */ }
  },

  clearActiveRun: async () => {
    const { stopPolling } = get()
    stopPolling()

    // Call backend ResetRun to free engine state
    try {
      await App.ResetRun()
    } catch { /* ignore — may already be idle */ }

    set({ activeRun: null, purViewMode: 'auto', queueStatus: null })
    try {
      localStorage.removeItem(ACTIVE_RUN_KEY)
    } catch { /* ignore */ }
  },

  setQueuedJob: (job) => set({ queuedJob: job }),
  setQueueStatus: (status) => set({ queueStatus: status }),

  setPurViewMode: (mode) => set({ purViewMode: mode }),

  cancelRun: async () => {
    const { activeRun, stopPolling } = get()
    if (!activeRun || activeRun.status !== 'active') return

    // C1: Set cancelled immediately (frontend-optimistic)
    set((prev) => ({
      activeRun: prev.activeRun ? { ...prev.activeRun, status: 'cancelled' as RunState } : null,
    }))

    try {
      await App.CancelRun()
    } catch (err) {
      // Handle "no run in progress" gracefully (race with completion)
      const errMsg = err instanceof Error ? err.message : String(err)
      if (!errMsg.includes('no run') && !errMsg.includes('not in progress')) {
        console.error('Failed to cancel run:', err)
      }
    }

    // C1: Wait up to 5s for interlink:complete event to reconcile
    await sleep(5000)

    // If still in cancelled state (complete event didn't override), force-finalize
    const current = get().activeRun
    if (current && current.status === 'cancelled') {
      stopPolling()
      const completedCount = current.jobRows.filter((j) =>
        j.submitStatus === 'completed' || j.submitStatus === 'success' || j.submitStatus === 'skipped'
      ).length
      const failedCount = current.jobRows.filter((j) =>
        j.submitStatus === 'failed' || j.tarStatus === 'failed' || j.uploadStatus === 'failed'
      ).length

      const completedRun: CompletedRun = {
        runId: current.runId,
        runType: current.runType,
        startTime: current.startTime,
        endTime: Date.now(),
        totalJobs: current.totalJobs,
        completedJobs: completedCount,
        failedJobs: failedCount,
        durationMs: Date.now() - current.startTime,
        jobRows: [...current.jobRows],
        finalStatus: 'cancelled',
      }

      set((prev) => ({
        activeRun: { ...current, status: 'cancelled' as RunState, completedJobs: completedCount, failedJobs: failedCount },
        completedRuns: [completedRun, ...prev.completedRuns].slice(0, MAX_COMPLETED_RUNS),
      }))

      try { localStorage.removeItem(ACTIVE_RUN_KEY) } catch { /* ignore */ }
    }
  },

  setupEventListeners: () => {
    if (get()._eventListenersSetup) {
      return () => {} // Already set up
    }

    // C9: Use unsub callbacks, NEVER EventsOff (would remove logStore's listeners)
    const unsubStateChange = EventsOn('interlink:state_change', (data: StateChangeEventDTO) => {
      const { activeRun } = get()
      if (!activeRun || activeRun.status !== 'active') return

      set((prev) => {
        if (!prev.activeRun) return prev
        const jobRows = [...prev.activeRun.jobRows]
        const idx = jobRows.findIndex((r) => r.jobName === data.jobName)
        if (idx === -1) return prev

        const row = { ...jobRows[idx] }
        if (data.stage === 'tar') row.tarStatus = data.newStatus
        else if (data.stage === 'upload') {
          row.uploadStatus = data.newStatus
          if (data.uploadProgress > 0) row.uploadProgress = data.uploadProgress * 100
        }
        else if (data.stage === 'create') row.createStatus = data.newStatus
        else if (data.stage === 'submit') row.submitStatus = data.newStatus

        if (data.jobId) row.jobId = data.jobId
        if (data.errorMessage) row.error = data.errorMessage
        if (data.newStatus === 'failed') row.status = 'failed'
        else if (data.newStatus === 'completed' && data.stage === 'submit') row.status = 'completed'

        jobRows[idx] = row

        const stageStats = computeStageStats(jobRows)

        const completedJobs = jobRows.filter((j) =>
          j.submitStatus === 'completed' || j.submitStatus === 'success' || j.submitStatus === 'skipped'
        ).length
        const failedJobs = jobRows.filter((j) =>
          j.submitStatus === 'failed' || j.tarStatus === 'failed' || j.uploadStatus === 'failed'
        ).length

        return {
          activeRun: {
            ...prev.activeRun,
            jobRows,
            pipelineStageStats: stageStats,
            completedJobs,
            failedJobs,
            durationMs: Date.now() - prev.activeRun.startTime,
          },
        }
      })
    })

    // C5: Use correct field names from LogEventDTO (jobName, stage — NOT detail, category)
    const unsubLog = EventsOn('interlink:log', (data: LogEventDTO) => {
      const { activeRun } = get()
      if (!activeRun || activeRun.status !== 'active') return

      // Log filtering: only capture pipeline-relevant entries
      if (!data.jobName && !PIPELINE_STAGES.has(data.stage)) return

      set((prev) => {
        if (!prev.activeRun) return prev
        const entry: PipelineLogEntry = {
          timestamp: Date.now(),
          level: data.level,
          message: data.message,
          jobName: data.jobName || undefined,
          stage: data.stage || undefined,
        }
        const logs = [...prev.activeRun.pipelineLogs, entry].slice(-MAX_PIPELINE_LOGS)
        return {
          activeRun: { ...prev.activeRun, pipelineLogs: logs },
        }
      })
    })

    const unsubComplete = EventsOn('interlink:complete', (data: CompleteEventDTO) => {
      const { activeRun, queuedJob, stopPolling } = get()
      if (!activeRun) return

      stopPolling()

      const completedCount = activeRun.jobRows.filter((j) =>
        j.submitStatus === 'completed' || j.submitStatus === 'success' || j.submitStatus === 'skipped'
      ).length
      const failedCount = activeRun.jobRows.filter((j) =>
        j.submitStatus === 'failed' || j.tarStatus === 'failed' || j.uploadStatus === 'failed'
      ).length

      // Determine final status — respect if already set to 'cancelled' (C1)
      let finalStatus: CompletedRun['finalStatus']
      if (activeRun.status === 'cancelled') {
        finalStatus = 'cancelled'
      } else if (failedCount > 0 || (data && data.failedJobs > 0)) {
        finalStatus = 'failed'
      } else {
        finalStatus = 'completed'
      }

      const completedRun: CompletedRun = {
        runId: activeRun.runId,
        runType: activeRun.runType,
        startTime: activeRun.startTime,
        endTime: Date.now(),
        totalJobs: activeRun.totalJobs,
        completedJobs: completedCount,
        failedJobs: failedCount,
        durationMs: Date.now() - activeRun.startTime,
        jobRows: [...activeRun.jobRows],
        finalStatus,
      }

      set((prev) => ({
        activeRun: prev.activeRun ? {
          ...prev.activeRun,
          status: finalStatus as RunState,
          completedJobs: completedCount,
          failedJobs: failedCount,
          durationMs: Date.now() - prev.activeRun.startTime,
        } : null,
        completedRuns: [completedRun, ...prev.completedRuns].slice(0, MAX_COMPLETED_RUNS),
      }))

      try { localStorage.removeItem(ACTIVE_RUN_KEY) } catch { /* ignore */ }

      // C2: Auto-start queued job with retry-with-backoff
      if (queuedJob) {
        startQueuedJobWithRetry()
      }
    })

    set({ _eventListenersSetup: true })

    return () => {
      // C9: Only remove OUR listeners via unsub callbacks
      unsubStateChange()
      unsubLog()
      unsubComplete()
      set({ _eventListenersSetup: false })
    }
  },

  recoverFromRestart: async () => {
    // C3: Read localStorage for persisted active run
    let persisted: PersistedActiveRun | null = null
    try {
      const raw = localStorage.getItem(ACTIVE_RUN_KEY)
      if (raw) persisted = JSON.parse(raw)
    } catch { /* ignore */ }

    if (!persisted) return

    try {
      // Check if the engine is still running (edge case: app reconnected, not truly restarted)
      const status = await App.GetRunStatus()
      if (status.state === 'running') {
        // Re-establish monitoring — build activeRun from current state
        const rows = await App.GetJobRows()
        const jobRows: JobRow[] = (rows || []).map((r, i) => ({
          index: r.index ?? i,
          directory: r.directory || '',
          jobName: r.jobName || '',
          tarStatus: r.tarStatus || 'pending',
          uploadStatus: r.uploadStatus || 'pending',
          uploadProgress: (r.uploadProgress || 0) * 100,
          createStatus: r.createStatus || '',
          submitStatus: r.submitStatus || 'pending',
          status: r.submitStatus || 'pending',
          jobId: r.jobId || '',
          progress: 0,
          error: r.error || '',
        }))

        const activeRun: ActiveRun = {
          runId: persisted.runId,
          runType: persisted.runType,
          startTime: persisted.startTime,
          status: 'active',
          totalJobs: persisted.totalJobs,
          completedJobs: 0,
          failedJobs: 0,
          durationMs: Date.now() - persisted.startTime,
          jobRows,
          pipelineStageStats: computeStageStats(jobRows),
          pipelineLogs: [],
        }
        set({ activeRun })
        get().startPolling(3000)
        return
      }

      // Engine is idle — try loading historical state from disk
      try {
        const historicalRows = await App.GetHistoricalJobRows(persisted.runId)
        if (historicalRows && historicalRows.length > 0) {
          const jobRows: JobRow[] = historicalRows.map((r, i) => ({
            index: r.index ?? i,
            directory: r.directory || '',
            jobName: r.jobName || '',
            tarStatus: r.tarStatus || '',
            uploadStatus: r.uploadStatus || '',
            uploadProgress: 0,
            createStatus: r.createStatus || '',
            submitStatus: r.submitStatus || '',
            status: r.submitStatus || '',
            jobId: r.jobId || '',
            progress: 0,
            error: r.error || '',
          }))

          // Classify final status from rows
          const completed = jobRows.filter((j) =>
            j.submitStatus === 'success' || j.submitStatus === 'completed' || j.submitStatus === 'skipped'
          ).length
          const failed = jobRows.filter((j) =>
            j.tarStatus === 'failed' || j.uploadStatus === 'failed' || j.submitStatus === 'failed'
          ).length
          const pending = jobRows.length - completed - failed

          let finalStatus: CompletedRun['finalStatus']
          if (pending > 0) {
            finalStatus = 'interrupted' // Run was cut short by app close/crash
          } else if (failed > 0) {
            finalStatus = 'failed'
          } else {
            finalStatus = 'completed'
          }

          const completedRun: CompletedRun = {
            runId: persisted.runId,
            runType: persisted.runType,
            startTime: persisted.startTime,
            endTime: Date.now(),
            totalJobs: persisted.totalJobs,
            completedJobs: completed,
            failedJobs: failed,
            durationMs: Date.now() - persisted.startTime,
            jobRows,
            finalStatus,
          }

          set((prev) => ({
            completedRuns: [completedRun, ...prev.completedRuns].slice(0, MAX_COMPLETED_RUNS),
          }))
        }
      } catch {
        // State file doesn't exist or can't be loaded — clean up silently
      }
    } catch {
      // Engine not available — clean up
    }

    try { localStorage.removeItem(ACTIVE_RUN_KEY) } catch { /* ignore */ }
  },

  startPolling: (intervalMs = 3000) => {
    const { _pollInterval } = get()
    if (_pollInterval) return // Already polling

    const refreshRunStatus = async () => {
      const { activeRun } = get()
      if (!activeRun || activeRun.status !== 'active') {
        get().stopPolling()
        return
      }

      try {
        const [status, rows] = await Promise.all([
          App.GetRunStatus(),
          App.GetJobRows(),
        ])

        set((prev) => {
          if (!prev.activeRun) return prev
          const polledRows = (rows || []) as JobRow[]

          // Guard: empty poll during active run preserves existing rows
          if (polledRows.length === 0 && prev.activeRun.jobRows.length > 0) {
            return prev
          }

          const mergedRows = polledRows.map((polled) => {
            const existing = prev.activeRun!.jobRows.find((r) => r.jobName === polled.jobName)
            if (!existing) return polled
            return {
              ...existing,
              tarStatus: polled.tarStatus || existing.tarStatus,
              uploadStatus: polled.uploadStatus || existing.uploadStatus,
              submitStatus: polled.submitStatus || existing.submitStatus,
              jobId: polled.jobId || existing.jobId,
              error: polled.error || existing.error,
              createStatus: existing.createStatus || polled.createStatus,
              uploadProgress: polled.uploadProgress > 0
                ? polled.uploadProgress * 100
                : existing.uploadProgress,
              status: existing.status === 'failed' ? 'failed'
                : (polled.submitStatus === 'success' ? 'completed' : existing.status),
            }
          })

          const stageStats = computeStageStats(mergedRows)
          const completedJobs = mergedRows.filter((j) =>
            j.submitStatus === 'completed' || j.submitStatus === 'success' || j.submitStatus === 'skipped'
          ).length
          const failedJobs = mergedRows.filter((j) =>
            j.submitStatus === 'failed' || j.tarStatus === 'failed' || j.uploadStatus === 'failed'
          ).length

          return {
            activeRun: {
              ...prev.activeRun,
              jobRows: mergedRows,
              pipelineStageStats: stageStats,
              completedJobs,
              failedJobs,
              durationMs: Date.now() - prev.activeRun.startTime,
            },
          }
        })

        // Check for terminal state — finalize if complete event hasn't already
        if (status.state === 'completed' || status.state === 'failed' || status.state === 'idle') {
          const currentRun = get().activeRun
          if (currentRun && currentRun.status === 'active') {
            // Poll detected completion — finalize the run
            const { stopPolling } = get()
            stopPolling()

            const jobRows = currentRun.jobRows
            const completedCount = jobRows.filter((j) =>
              j.submitStatus === 'completed' || j.submitStatus === 'success' || j.submitStatus === 'skipped'
            ).length
            const failedCount = jobRows.filter((j) =>
              j.submitStatus === 'failed' || j.tarStatus === 'failed' || j.uploadStatus === 'failed'
            ).length

            let finalStatus: CompletedRun['finalStatus']
            if (failedCount > 0) {
              finalStatus = 'failed'
            } else {
              finalStatus = 'completed'
            }

            const completedRun: CompletedRun = {
              runId: currentRun.runId,
              runType: currentRun.runType,
              startTime: currentRun.startTime,
              endTime: Date.now(),
              totalJobs: currentRun.totalJobs,
              completedJobs: completedCount,
              failedJobs: failedCount,
              durationMs: Date.now() - currentRun.startTime,
              jobRows: [...jobRows],
              finalStatus,
            }

            set((prev) => ({
              activeRun: prev.activeRun ? {
                ...prev.activeRun,
                status: finalStatus as RunState,
                completedJobs: completedCount,
                failedJobs: failedCount,
                durationMs: Date.now() - prev.activeRun.startTime,
              } : null,
              completedRuns: [completedRun, ...prev.completedRuns].slice(0, MAX_COMPLETED_RUNS),
            }))

            try { localStorage.removeItem(ACTIVE_RUN_KEY) } catch { /* ignore */ }
          }
        }
      } catch (err) {
        console.error('runStore: poll failed:', err)
      }
    }

    // Initial fetch
    refreshRunStatus()

    const interval = setInterval(refreshRunStatus, intervalMs)
    set({ _pollInterval: interval })
  },

  stopPolling: () => {
    const { _pollInterval } = get()
    if (_pollInterval) {
      clearInterval(_pollInterval)
      set({ _pollInterval: null })
    }
  },
}))

// C2: Queued job auto-start with retry/backoff (module-level helper)
async function startQueuedJobWithRetry(maxAttempts = 5) {
  const store = useRunStore.getState()
  const queuedJob = store.queuedJob
  if (!queuedJob) return

  useRunStore.setState({ queueStatus: 'starting' })

  for (let attempt = 0; attempt < maxAttempts; attempt++) {
    await sleep(500 * (attempt + 1)) // 500ms, 1s, 1.5s, 2s, 2.5s

    try {
      const status = await App.GetRunStatus()
      if (status.state === 'running') continue // EndRun hasn't cleared yet

      // Safe to start
      if (queuedJob.runType === 'single') {
        const runId = await App.StartSingleJob(queuedJob.input)
        if (runId) {
          const jobRows: JobRow[] = [{
            index: 0,
            directory: '',
            jobName: queuedJob.input.job?.jobName || 'Job',
            tarStatus: 'pending',
            uploadStatus: 'pending',
            uploadProgress: 0,
            createStatus: 'pending',
            submitStatus: 'pending',
            status: 'pending',
            jobId: '',
            progress: 0,
            error: '',
          }]
          useRunStore.getState().registerRun(runId, 'single', 1, jobRows)
          useRunStore.getState().startPolling(1000)
        }
      } else {
        const runId = await App.StartBulkRunWithOptions(
          queuedJob.input.jobs,
          queuedJob.input.opts,
        )
        if (runId) {
          const jobRows: JobRow[] = queuedJob.input.jobs.map((job, i) => ({
            index: i,
            directory: job.directory || '',
            jobName: job.jobName || `Job_${i + 1}`,
            tarStatus: 'pending',
            uploadStatus: 'pending',
            uploadProgress: 0,
            createStatus: 'pending',
            submitStatus: 'pending',
            status: 'pending',
            jobId: '',
            progress: 0,
            error: '',
          }))
          useRunStore.getState().registerRun(runId, 'pur', queuedJob.input.jobs.length, jobRows)
          useRunStore.getState().startPolling(3000)
        }
      }

      useRunStore.setState({ queuedJob: null, queueStatus: 'started' })
      // Clear 'started' status after a few seconds
      setTimeout(() => {
        useRunStore.setState((prev) =>
          prev.queueStatus === 'started' ? { queueStatus: null } : prev
        )
      }, 3000)
      return
    } catch (err) {
      const errMsg = String(err)
      if (errMsg.includes('already in progress')) continue
      useRunStore.setState({
        queueStatus: `failed: ${err instanceof Error ? err.message : errMsg}`,
        queuedJob: null,
      })
      return
    }
  }

  // All attempts failed
  useRunStore.setState({
    queueStatus: 'failed: run still active after retries',
    queuedJob: null,
  })
}
