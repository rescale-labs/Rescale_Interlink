// Run session persistence and monitoring types.

import type { JobRow, PipelineStageStats, PipelineLogEntry } from './jobs'
import type { wailsapp } from '../../wailsjs/go/models'

export type RunType = 'pur' | 'single'
// 'unconfirmed': the run is over and nothing failed, but the platform never
// confirmed one or more job creations. It is terminal, and it is not a success.
export type RunState = 'active' | 'completed' | 'failed' | 'cancelled' | 'interrupted' | 'unconfirmed'

export function isTerminalRunState(status: RunState): boolean {
  return status === 'completed' || status === 'failed' || status === 'cancelled' || status === 'unconfirmed'
}

export interface ActiveRun {
  runId: string
  runType: RunType
  startTime: number          // Date.now()
  status: RunState
  totalJobs: number
  completedJobs: number
  failedJobs: number
  unconfirmedJobs: number    // Creations the platform never confirmed
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
  unconfirmedJobs: number
  durationMs: number
  error?: string
  jobRows: JobRow[]          // Snapshot at completion
  finalStatus: 'completed' | 'failed' | 'cancelled' | 'interrupted' | 'unconfirmed'
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
