// v4.7.3: Run session persistence and monitoring types.

import type { JobRow, PipelineStageStats, PipelineLogEntry } from './jobs'
import type { wailsapp } from '../../wailsjs/go/models'

export type RunType = 'pur' | 'single'
export type RunState = 'active' | 'completed' | 'failed' | 'cancelled' | 'interrupted'

export interface ActiveRun {
  runId: string
  runType: RunType
  startTime: number          // Date.now()
  status: RunState
  totalJobs: number
  completedJobs: number
  failedJobs: number
  durationMs: number
  error?: string
  jobRows: JobRow[]
  pipelineStageStats: PipelineStageStats
  pipelineLogs: PipelineLogEntry[]
  singleJobId?: string       // For SingleJob: Rescale job ID once created
}

export interface CompletedRun {
  runId: string
  runType: RunType
  startTime: number
  endTime: number
  totalJobs: number
  completedJobs: number
  failedJobs: number
  durationMs: number
  error?: string
  jobRows: JobRow[]          // Snapshot at completion
  finalStatus: 'completed' | 'failed' | 'cancelled' | 'interrupted'
}

// Discriminated union for type-safe queue (C6). Inputs are deep-copied at queue time
// to prevent mutation from subsequent UI edits.
export type QueuedJob =
  | { runType: 'single'; input: wailsapp.SingleJobInputDTO; queuedAt: number }
  | { runType: 'pur'; input: { jobs: wailsapp.JobSpecDTO[]; opts: wailsapp.PURRunOptionsDTO }; queuedAt: number }

// Minimal info persisted to localStorage for restart recovery
export interface PersistedActiveRun {
  runId: string
  runType: RunType
  startTime: number
  totalJobs: number
}
