// Shared domain types extracted from jobStore.ts to break import cycles.
// Both runStore.ts and jobStore.ts import from here. jobStore.ts re-exports for backward compat.

import type { wailsapp } from '../../wailsjs/go/models'

// Workflow state enum (matches Go JobsWorkflow)
export type WorkflowState =
  | 'initial'
  | 'pathChosen'
  | 'templateReady'
  | 'directoriesScanned'
  | 'jobsValidated'
  | 'executing'
  | 'completed'
  | 'error'

// Workflow path enum. 'createSweep' is the dedicated DOE parameter-sweep entry:
// like 'createNew' it builds a base job, but the base job is expanded into one
// case per design point rather than scanned from directories.
export type WorkflowPath = 'unknown' | 'loadCSV' | 'createNew' | 'createSweep'

// Job spec from Go. Derived from the generated binding rather than restated:
// a hand-written copy silently drops any field the Go DTO gains, which is how
// tarSubpath and inputFiles went missing from the CSV round-trip.
export type JobSpec = wailsapp.JobSpecDTO

// Job row for the jobs table
export interface JobRow {
  index: number
  directory: string
  jobName: string
  tarStatus: string
  uploadStatus: string
  uploadProgress: number
  createStatus: string
  submitStatus: string
  status: string
  jobId: string
  progress: number
  error: string
}

// Run status
export interface RunStatus {
  state: 'idle' | 'running' | 'completed' | 'failed' | 'cancelled'
  totalJobs: number
  successJobs: number
  failedJobs: number
  durationMs: number
  error?: string
}

// Pipeline log entry for in-tab display
export interface PipelineLogEntry {
  timestamp: number
  level: string
  message: string
  jobName?: string
  stage?: string
}

// Per-stage stats for pipeline summary
export interface PipelineStageStats {
  tar: { completed: number; total: number; failed: number }
  upload: { completed: number; total: number; failed: number }
  create: { completed: number; total: number; failed: number }
  submit: { completed: number; total: number; failed: number }
}
