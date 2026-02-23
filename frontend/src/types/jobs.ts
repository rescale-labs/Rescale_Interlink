// v4.7.3: Shared domain types extracted from jobStore.ts to break import cycles.
// Both runStore.ts and jobStore.ts import from here. jobStore.ts re-exports for backward compat.

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

// Workflow path enum
export type WorkflowPath = 'unknown' | 'loadCSV' | 'createNew'

// Job spec from Go
export interface JobSpec {
  directory: string
  jobName: string
  analysisCode: string
  analysisVersion: string
  command: string
  coreType: string
  coresPerSlot: number
  walltimeHours: number
  slots: number
  licenseSettings: string
  extraInputFileIds: string
  noDecompress: boolean
  submitMode: string
  isLowPriority: boolean
  onDemandLicenseSeller: string
  tags: string[]
  projectId: string
  orgCode: string
  automations: string[]
}

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

// v4.6.0: Pipeline log entry for in-tab display
export interface PipelineLogEntry {
  timestamp: number
  level: string
  message: string
  jobName?: string
  stage?: string
}

// v4.6.0: Per-stage stats for pipeline summary
export interface PipelineStageStats {
  tar: { completed: number; total: number; failed: number }
  upload: { completed: number; total: number; failed: number }
  create: { completed: number; total: number; failed: number }
  submit: { completed: number; total: number; failed: number }
}
