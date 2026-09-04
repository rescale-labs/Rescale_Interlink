// Shared domain types extracted from jobStore.ts to break import cycles.
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

// Workflow path enum. 'createSweep' is the dedicated DOE parameter-sweep entry:
// like 'createNew' it builds a base job, but the base job is expanded into one
// case per design point rather than scanned from directories.
export type WorkflowPath = 'unknown' | 'loadCSV' | 'createNew' | 'createSweep'

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

  // One user-defined license feature: the job checks out licensesPerJob seats of
  // licenseFeatureName from the customer's own license server. Only meaningful
  // together — an empty name with a count, or the reverse, is rejected on save.
  licenseFeatureName: string
  licensesPerJob: number

  tags: string[]
  projectId: string
  orgCode: string
  automations: string[]

  // Already-uploaded file IDs to attach instead of tarring directory. Set by
  // DOE sweeps over a shared input deck and by single-job remote-file mode.
  inputFiles?: string[]

  // Local paths forming this job's archive: the tarball holds exactly these
  // files and the rest of the directory is not walked. Set by file-scan mode,
  // where each job owns one file set.
  localInputFiles?: string[]

  tarSubpath?: string
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
