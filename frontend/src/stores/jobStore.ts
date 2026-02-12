import { create } from 'zustand'
import * as App from '../../wailsjs/go/wailsapp/App'
import { wailsapp } from '../../wailsjs/go/models'
import { EventsOn } from '../../wailsjs/runtime/runtime'

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

// Core type from API
export interface CoreType {
  code: string
  name: string
  displayOrder: number
  isActive: boolean
  cores: number[]
}

// Analysis code from API
export interface AnalysisCode {
  code: string
  name: string
  description: string
  vendorName: string
  versions: AnalysisVersion[]
}

export interface AnalysisVersion {
  id: string
  version: string
  versionCode: string
  allowedCoreTypes: string[]
}

// Automation from API
export interface Automation {
  id: string
  name: string
  description: string
  executeOn: string
  scriptName: string
}

// Secondary pattern for file scanning mode (v4.0.8)
export interface SecondaryPattern {
  pattern: string   // Glob pattern, may include subpath (e.g., "*.mesh", "../meshes/*.cfg")
  required: boolean // If true, skip job when file missing; if false, warn and continue
}

// PUR run options (beyond job list)
export interface PURRunOptions {
  extraInputFiles: string   // Comma-separated paths and/or id:fileId
  decompressExtras: boolean
}

// Scan options
export interface ScanOptions {
  rootDir: string
  pattern: string
  validationPattern: string
  runSubpath: string
  recursive: boolean
  includeHidden: boolean

  // v4.0.8: File scanning mode fields
  scanMode: 'folders' | 'files'
  primaryPattern: string           // For file mode: e.g., "*.inp", "inputs/*.inp"
  secondaryPatterns: SecondaryPattern[]

  // v4.6.0: Subdirectory within each Run_* to tar
  tarSubpath: string

  // v4.6.1: Vary command across runs (iterate numeric patterns)
  iteratePatterns: boolean
}

// Default job template
export const DEFAULT_JOB_TEMPLATE: JobSpec = {
  directory: './Run_${index}',
  jobName: 'Run_1',
  analysisCode: '',
  analysisVersion: '',
  command: '# Enter your command here',
  coreType: '',
  coresPerSlot: 4,
  walltimeHours: 1.0,
  slots: 1,
  licenseSettings: '',
  extraInputFileIds: '',
  noDecompress: false,
  submitMode: 'create_and_submit',
  isLowPriority: false,
  onDemandLicenseSeller: '',
  tags: [],
  projectId: '',
  automations: [],
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

// Workflow memory - persisted values between sessions
export interface WorkflowMemory {
  lastTemplate: JobSpec
  lastScanDir: string
  lastPattern: string
  lastCoreType: string
  lastAnalysisCode: string
  lastProjectId: string
}

// Job stats from backend
export interface JobsStats {
  total: number
  completed: number
  inProgress: number
  pending: number
  failed: number
}

interface JobStore {
  // Workflow state
  workflowState: WorkflowState
  workflowPath: WorkflowPath
  errorMessage: string

  // Template and jobs
  template: JobSpec
  scannedJobs: JobSpec[]
  jobRows: JobRow[]
  runStatus: RunStatus
  runId: string | null
  jobsStats: JobsStats

  // API cache
  coreTypes: CoreType[]
  analysisCodes: AnalysisCode[]
  automations: Automation[]
  isLoadingCoreTypes: boolean
  isLoadingAnalysisCodes: boolean
  isLoadingAutomations: boolean
  // v4.0.6: Error states for API calls
  coreTypesError: string | null
  analysisCodesError: string | null
  automationsError: string | null

  // PUR run options
  purRunOptions: PURRunOptions

  // Scan state
  scanOptions: ScanOptions
  isScanning: boolean
  scanError: string | null

  // Workflow memory
  memory: WorkflowMemory

  // v4.6.0: Pipeline diagnostics
  pipelineLogs: PipelineLogEntry[]
  pipelineStageStats: PipelineStageStats
  _eventUnsubs: (() => void)[]
  startTime: number | null

  // Polling
  isPolling: boolean
  _pollInterval: ReturnType<typeof setInterval> | null

  // Actions - State Machine
  setWorkflowPath: (path: WorkflowPath) => void
  setTemplate: (template: JobSpec) => void
  goBack: () => void
  reset: () => void
  setError: (message: string) => void
  clearError: () => void
  canGoBack: () => boolean

  // Actions - PUR Run Options
  setPURRunOptions: (opts: Partial<PURRunOptions>) => void

  // Actions - Scanning
  setScanOptions: (opts: Partial<ScanOptions>) => void
  scanDirectory: () => Promise<void>

  // Actions - Validation
  validateJobs: () => Promise<string[]>
  updateJobRow: (index: number, updates: Partial<JobRow>) => void

  // Actions - Execution
  startBulkRun: () => Promise<string | null>
  cancelRun: () => Promise<void>
  refreshRunStatus: () => Promise<void>
  refreshJobsStats: () => Promise<void>
  startPolling: (intervalMs?: number) => void
  stopPolling: () => void

  // Actions - File Operations
  loadJobsFromCSV: (path: string) => Promise<void>
  saveJobsToCSV: (path: string) => Promise<void>
  loadJobFromJSON: (path: string) => Promise<JobSpec | null>
  saveJobToJSON: (path: string, job: JobSpec) => Promise<void>
  loadJobFromSGE: (path: string) => Promise<JobSpec | null>
  saveJobToSGE: (path: string, job: JobSpec) => Promise<void>

  // Actions - API Cache
  fetchCoreTypes: () => Promise<void>
  fetchAnalysisCodes: (search?: string) => Promise<void>
  fetchAutomations: () => Promise<void>

  // Actions - Memory
  saveMemory: () => void
  loadMemory: () => void
}

// State transition rules (kept for reference but not currently used for validation)
// const STATE_TRANSITIONS: Record<WorkflowState, WorkflowState[]> = {
//   initial: ['pathChosen', 'error'],
//   pathChosen: ['initial', 'templateReady', 'jobsValidated', 'error'],
//   templateReady: ['pathChosen', 'directoriesScanned', 'error'],
//   directoriesScanned: ['templateReady', 'jobsValidated', 'error'],
//   jobsValidated: ['directoriesScanned', 'pathChosen', 'executing', 'error'],
//   executing: ['completed', 'error'],
//   completed: ['initial', 'error'],
//   error: ['initial'],
// }

// Back navigation targets
const BACK_TARGETS: Partial<Record<WorkflowState, WorkflowState>> = {
  pathChosen: 'initial',
  templateReady: 'pathChosen',
  directoriesScanned: 'templateReady',
  jobsValidated: 'directoriesScanned',
}

const MEMORY_KEY = 'rescale-int-job-memory'

export const useJobStore = create<JobStore>((set, get) => ({
  // Initial state
  workflowState: 'initial',
  workflowPath: 'unknown',
  errorMessage: '',

  template: { ...DEFAULT_JOB_TEMPLATE },
  scannedJobs: [],
  jobRows: [],
  runStatus: {
    state: 'idle',
    totalJobs: 0,
    successJobs: 0,
    failedJobs: 0,
    durationMs: 0,
  },
  runId: null,
  jobsStats: {
    total: 0,
    completed: 0,
    inProgress: 0,
    pending: 0,
    failed: 0,
  },

  coreTypes: [],
  analysisCodes: [],
  automations: [],
  isLoadingCoreTypes: false,
  isLoadingAnalysisCodes: false,
  isLoadingAutomations: false,
  // v4.0.6: Error states
  coreTypesError: null,
  analysisCodesError: null,
  automationsError: null,

  purRunOptions: {
    extraInputFiles: '',
    decompressExtras: false,
  },

  scanOptions: {
    rootDir: '',
    pattern: 'Run_*',
    validationPattern: '',
    runSubpath: '',
    recursive: false,
    includeHidden: false,
    // v4.0.8: File scanning mode defaults
    scanMode: 'folders' as const,
    primaryPattern: '*.inp',
    secondaryPatterns: [],
    // v4.6.0
    tarSubpath: '',
    // v4.6.1
    iteratePatterns: false,
  },
  isScanning: false,
  scanError: null,

  memory: {
    lastTemplate: { ...DEFAULT_JOB_TEMPLATE },
    lastScanDir: '',
    lastPattern: 'Run_*',
    lastCoreType: '',
    lastAnalysisCode: '',
    lastProjectId: '',
  },

  // v4.6.0: Pipeline diagnostics
  pipelineLogs: [],
  pipelineStageStats: {
    tar: { completed: 0, total: 0, failed: 0 },
    upload: { completed: 0, total: 0, failed: 0 },
    create: { completed: 0, total: 0, failed: 0 },
    submit: { completed: 0, total: 0, failed: 0 },
  },
  _eventUnsubs: [],
  startTime: null,

  isPolling: false,
  _pollInterval: null,

  // State Machine Actions
  setWorkflowPath: (path) => {
    const { workflowState } = get()
    if (workflowState !== 'initial') return

    set({
      workflowPath: path,
      workflowState: 'pathChosen',
    })
  },

  setTemplate: (template) => {
    const { workflowState, workflowPath } = get()
    if (workflowState !== 'pathChosen' || workflowPath !== 'createNew') return

    set({
      template,
      workflowState: 'templateReady',
    })

    // Update memory
    get().saveMemory()
  },

  goBack: () => {
    const { workflowState, workflowPath } = get()
    const target = BACK_TARGETS[workflowState]
    if (!target) return

    // Special handling for jobsValidated state
    if (workflowState === 'jobsValidated' && workflowPath === 'loadCSV') {
      set({
        workflowState: 'pathChosen',
        scannedJobs: [],
        jobRows: [],
      })
      return
    }

    set({ workflowState: target })

    // Clear state as we go back
    if (target === 'initial') {
      set({ workflowPath: 'unknown' })
    }
    if (target === 'pathChosen') {
      set({ template: { ...DEFAULT_JOB_TEMPLATE } })
    }
    if (target === 'templateReady') {
      set({ scannedJobs: [], jobRows: [] })
    }
  },

  reset: () => {
    const { stopPolling, _pollInterval } = get()
    if (_pollInterval) {
      stopPolling()
    }

    set({
      workflowState: 'initial',
      workflowPath: 'unknown',
      errorMessage: '',
      template: { ...DEFAULT_JOB_TEMPLATE },
      scannedJobs: [],
      jobRows: [],
      runStatus: {
        state: 'idle',
        totalJobs: 0,
        successJobs: 0,
        failedJobs: 0,
        durationMs: 0,
      },
      runId: null,
      scanError: null,
      pipelineLogs: [],
      startTime: null,
    })
  },

  setError: (message) => {
    set({
      workflowState: 'error',
      errorMessage: message,
    })
  },

  clearError: () => {
    set({
      workflowState: 'initial',
      errorMessage: '',
    })
  },

  canGoBack: () => {
    const { workflowState } = get()
    return workflowState in BACK_TARGETS
  },

  // PUR Run Options Actions
  setPURRunOptions: (opts) => {
    set((state) => ({
      purRunOptions: { ...state.purRunOptions, ...opts },
    }))
  },

  // Scan Actions
  setScanOptions: (opts) => {
    set((state) => ({
      scanOptions: { ...state.scanOptions, ...opts },
    }))
  },

  scanDirectory: async () => {
    const { scanOptions, template } = get()

    if (!scanOptions.rootDir) {
      set({ scanError: 'Root directory is required' })
      return
    }

    set({ isScanning: true, scanError: null })

    try {
      // v4.0.8: Convert secondary patterns to DTO format
      const secondaryPatternsDTO = scanOptions.secondaryPatterns.map((sp) => ({
        pattern: sp.pattern,
        required: sp.required,
      }))

      const result = await App.ScanDirectory(
        {
          rootDir: scanOptions.rootDir,
          pattern: scanOptions.pattern,
          validationPattern: scanOptions.validationPattern,
          runSubpath: scanOptions.runSubpath,
          recursive: scanOptions.recursive,
          includeHidden: scanOptions.includeHidden,
          // v4.0.8: File scanning mode fields
          scanMode: scanOptions.scanMode,
          primaryPattern: scanOptions.primaryPattern,
          secondaryPatterns: secondaryPatternsDTO,
          // v4.6.0
          tarSubpath: scanOptions.tarSubpath,
          // v4.6.1
          iteratePatterns: scanOptions.iteratePatterns,
        } as wailsapp.ScanOptionsDTO,
        template as wailsapp.JobSpecDTO
      )

      if (result.error) {
        set({ scanError: result.error, isScanning: false })
        return
      }

      const jobs = (result.jobs || []) as JobSpec[]
      const jobRows: JobRow[] = jobs.map((job, index) => ({
        index,
        directory: job.directory,
        jobName: job.jobName,
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

      set({
        scannedJobs: jobs,
        jobRows,
        workflowState: 'directoriesScanned',
        isScanning: false,
      })

      // Update memory
      get().saveMemory()
    } catch (error) {
      set({
        scanError: error instanceof Error ? error.message : String(error),
        isScanning: false,
      })
    }
  },

  // Validation Actions
  validateJobs: async () => {
    const { scannedJobs } = get()
    const errors: string[] = []

    for (const job of scannedJobs) {
      try {
        const jobErrors = await App.ValidateJobSpec(job as wailsapp.JobSpecDTO)
        if (jobErrors && jobErrors.length > 0) {
          errors.push(`${job.jobName}: ${jobErrors.join(', ')}`)
        }
      } catch (error) {
        errors.push(`${job.jobName}: Validation failed`)
      }
    }

    if (errors.length === 0) {
      set({ workflowState: 'jobsValidated' })
    }

    return errors
  },

  updateJobRow: (index, updates) => {
    set((state) => {
      const jobRows = [...state.jobRows]
      if (index >= 0 && index < jobRows.length) {
        jobRows[index] = { ...jobRows[index], ...updates }
      }
      return { jobRows }
    })
  },

  // Execution Actions
  startBulkRun: async () => {
    const { scannedJobs } = get()

    if (scannedJobs.length === 0) {
      set({ errorMessage: 'No jobs to run' })
      return null
    }

    try {
      set({
        workflowState: 'executing',
        pipelineLogs: [],
        startTime: Date.now(),
      })

      const runId = await App.StartBulkRunWithOptions(
        scannedJobs as wailsapp.JobSpecDTO[],
        get().purRunOptions as wailsapp.PURRunOptionsDTO,
      )
      set({ runId })

      // v4.6.0: Subscribe to real-time Wails events for live updates
      const unsubs: (() => void)[] = []

      // State change events: update individual job rows by jobName/stage
      unsubs.push(EventsOn('interlink:state_change', (data: {
        jobName: string; stage: string; newStatus: string;
        jobId: string; errorMessage: string; uploadProgress: number
      }) => {
        set((state) => {
          const jobRows = [...state.jobRows]
          const idx = jobRows.findIndex((r) => r.jobName === data.jobName)
          if (idx === -1) return state

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

          // Recompute stage stats from job rows
          const stageStats: PipelineStageStats = {
            tar: { completed: 0, total: jobRows.length, failed: 0 },
            upload: { completed: 0, total: jobRows.length, failed: 0 },
            create: { completed: 0, total: jobRows.length, failed: 0 },
            submit: { completed: 0, total: jobRows.length, failed: 0 },
          }
          for (const r of jobRows) {
            if (r.tarStatus === 'completed' || r.tarStatus === 'success' || r.tarStatus === 'skipped') stageStats.tar.completed++
            if (r.tarStatus === 'failed') stageStats.tar.failed++
            if (r.uploadStatus === 'completed' || r.uploadStatus === 'success' || r.uploadStatus === 'skipped') stageStats.upload.completed++
            if (r.uploadStatus === 'failed') stageStats.upload.failed++
            if (r.createStatus === 'completed' || r.createStatus === 'success') stageStats.create.completed++
            if (r.createStatus === 'failed') stageStats.create.failed++
            if (r.submitStatus === 'completed' || r.submitStatus === 'success' || r.submitStatus === 'skipped') stageStats.submit.completed++
            if (r.submitStatus === 'failed') stageStats.submit.failed++
          }

          return { jobRows, pipelineStageStats: stageStats }
        })
      }))

      // Log events: collect pipeline log messages for in-tab display
      unsubs.push(EventsOn('interlink:log', (data: {
        level: string; message: string; category: string; detail: string
      }) => {
        set((state) => {
          const entry: PipelineLogEntry = {
            timestamp: Date.now(),
            level: data.level,
            message: data.message,
            jobName: data.detail || undefined,
            stage: data.category || undefined,
          }
          // Keep last 200 log entries
          const logs = [...state.pipelineLogs, entry].slice(-200)
          return { pipelineLogs: logs }
        })
      }))

      // Complete event: transition to completed state
      unsubs.push(EventsOn('interlink:complete', () => {
        get().stopPolling()
        set({ workflowState: 'completed' })
      }))

      set({ _eventUnsubs: unsubs })

      // Start polling for reconciliation (reduced interval since events are primary)
      get().startPolling(3000)

      return runId
    } catch (error) {
      set({
        workflowState: 'error',
        errorMessage: error instanceof Error ? error.message : String(error),
      })
      return null
    }
  },

  cancelRun: async () => {
    try {
      await App.CancelRun()
      get().stopPolling()
      set({
        runStatus: { ...get().runStatus, state: 'cancelled' },
        workflowState: 'completed',
      })
    } catch (error) {
      // v4.6.0: Handle "no run in progress" gracefully (race between cancel and completion)
      const errMsg = error instanceof Error ? error.message : String(error)
      if (errMsg.includes('no run') || errMsg.includes('not in progress')) {
        get().stopPolling()
        set({ workflowState: 'completed' })
      } else {
        console.error('Failed to cancel run:', error)
      }
    }
  },

  refreshRunStatus: async () => {
    try {
      const [status, rows] = await Promise.all([
        App.GetRunStatus(),
        App.GetJobRows(),
      ])

      set((prev) => {
        const polledRows = (rows || []) as JobRow[]

        // Guard: if poll returns empty during active run, preserve existing rows
        if (polledRows.length === 0 && prev.jobRows.length > 0 &&
            prev.workflowState === 'executing') {
          return { runStatus: status as RunStatus }
        }

        const mergedRows = polledRows.map((polled) => {
          const existing = prev.jobRows.find((r) => r.jobName === polled.jobName)
          if (!existing) return polled
          return {
            ...existing,
            // State manager is authoritative for persisted fields (non-empty wins)
            tarStatus: polled.tarStatus || existing.tarStatus,
            uploadStatus: polled.uploadStatus || existing.uploadStatus,
            submitStatus: polled.submitStatus || existing.submitStatus,
            jobId: polled.jobId || existing.jobId,
            error: polled.error || existing.error,
            // Preserve event-sourced transient fields (createStatus not in state mgr)
            createStatus: existing.createStatus || polled.createStatus,
            // Normalize: polled progress is 0.0-1.0, events store as 0-100
            uploadProgress: polled.uploadProgress > 0
              ? polled.uploadProgress * 100
              : existing.uploadProgress,
            // Preserve event-derived overall status
            status: existing.status === 'failed' ? 'failed'
              : (polled.submitStatus === 'success' ? 'completed' : existing.status),
          }
        })

        // Recompute stage stats
        const stageStats: PipelineStageStats = {
          tar: { completed: 0, total: mergedRows.length, failed: 0 },
          upload: { completed: 0, total: mergedRows.length, failed: 0 },
          create: { completed: 0, total: mergedRows.length, failed: 0 },
          submit: { completed: 0, total: mergedRows.length, failed: 0 },
        }
        for (const r of mergedRows) {
          if (r.tarStatus === 'completed' || r.tarStatus === 'success' || r.tarStatus === 'skipped') stageStats.tar.completed++
          if (r.tarStatus === 'failed') stageStats.tar.failed++
          if (r.uploadStatus === 'completed' || r.uploadStatus === 'success' || r.uploadStatus === 'skipped') stageStats.upload.completed++
          if (r.uploadStatus === 'failed') stageStats.upload.failed++
          if (r.createStatus === 'completed' || r.createStatus === 'success') stageStats.create.completed++
          if (r.createStatus === 'failed') stageStats.create.failed++
          if (r.submitStatus === 'completed' || r.submitStatus === 'success' || r.submitStatus === 'skipped') stageStats.submit.completed++
          if (r.submitStatus === 'failed') stageStats.submit.failed++
        }

        return {
          runStatus: status as RunStatus,
          jobRows: mergedRows,
          pipelineStageStats: stageStats,
        }
      })

      const currentStatus = get().runStatus
      if (currentStatus.state === 'completed' || currentStatus.state === 'failed') {
        get().stopPolling()
        set({ workflowState: 'completed' })
      }
    } catch (error) {
      console.error('Failed to refresh run status:', error)
    }
  },

  startPolling: (intervalMs = 1000) => {
    const { isPolling } = get()
    if (isPolling) return

    const pollInterval = setInterval(() => {
      get().refreshRunStatus()
    }, intervalMs)

    // Initial fetch
    get().refreshRunStatus()

    set({
      isPolling: true,
      _pollInterval: pollInterval,
    })
  },

  stopPolling: () => {
    const { _pollInterval, _eventUnsubs } = get()
    if (_pollInterval) {
      clearInterval(_pollInterval)
    }
    // v4.6.0: Clean up event subscriptions
    for (const unsub of _eventUnsubs) {
      try { unsub() } catch { /* ignore */ }
    }
    set({
      isPolling: false,
      _pollInterval: null,
      _eventUnsubs: [],
    })
  },

  refreshJobsStats: async () => {
    try {
      const stats = await App.GetJobsStats()
      set({
        jobsStats: {
          total: stats.total,
          completed: stats.completed,
          inProgress: stats.inProgress,
          pending: stats.pending,
          failed: stats.failed,
        },
      })
    } catch (error) {
      console.error('Failed to refresh jobs stats:', error)
    }
  },

  // File Operations Actions
  loadJobsFromCSV: async (path: string) => {
    try {
      const jobs = await App.LoadJobsFromCSV(path)
      if (!jobs || jobs.length === 0) {
        throw new Error('No jobs found in CSV file')
      }

      // Map DTOs to local types
      const mappedJobs: JobSpec[] = jobs.map((job) => ({
        directory: job.directory,
        jobName: job.jobName,
        analysisCode: job.analysisCode,
        analysisVersion: job.analysisVersion,
        command: job.command,
        coreType: job.coreType,
        coresPerSlot: job.coresPerSlot,
        walltimeHours: job.walltimeHours,
        slots: job.slots,
        licenseSettings: job.licenseSettings,
        extraInputFileIds: job.extraInputFileIds,
        noDecompress: job.noDecompress,
        submitMode: job.submitMode,
        isLowPriority: job.isLowPriority,
        onDemandLicenseSeller: job.onDemandLicenseSeller,
        tags: job.tags || [],
        projectId: job.projectId,
        automations: job.automations || [],
      }))

      // Create job rows from the loaded jobs
      const jobRows: JobRow[] = mappedJobs.map((job, index) => ({
        index,
        directory: job.directory,
        jobName: job.jobName,
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

      set({
        scannedJobs: mappedJobs,
        jobRows,
        workflowPath: 'loadCSV',
        workflowState: 'jobsValidated',
      })
    } catch (error) {
      throw error
    }
  },

  saveJobsToCSV: async (path: string) => {
    const { scannedJobs } = get()
    if (scannedJobs.length === 0) {
      throw new Error('No jobs to save')
    }

    await App.SaveJobsToCSV(path, scannedJobs as wailsapp.JobSpecDTO[])
  },

  loadJobFromJSON: async (path: string) => {
    try {
      const job = await App.LoadJobFromJSON(path)
      return {
        directory: job.directory,
        jobName: job.jobName,
        analysisCode: job.analysisCode,
        analysisVersion: job.analysisVersion,
        command: job.command,
        coreType: job.coreType,
        coresPerSlot: job.coresPerSlot,
        walltimeHours: job.walltimeHours,
        slots: job.slots,
        licenseSettings: job.licenseSettings,
        extraInputFileIds: job.extraInputFileIds,
        noDecompress: job.noDecompress,
        submitMode: job.submitMode,
        isLowPriority: job.isLowPriority,
        onDemandLicenseSeller: job.onDemandLicenseSeller,
        tags: job.tags || [],
        projectId: job.projectId,
        automations: job.automations || [],
      } as JobSpec
    } catch (error) {
      console.error('Failed to load job from JSON:', error)
      return null
    }
  },

  saveJobToJSON: async (path: string, job: JobSpec) => {
    await App.SaveJobToJSON(path, job as wailsapp.JobSpecDTO)
  },

  loadJobFromSGE: async (path: string) => {
    try {
      const job = await App.LoadJobFromSGE(path)
      return {
        directory: job.directory,
        jobName: job.jobName,
        analysisCode: job.analysisCode,
        analysisVersion: job.analysisVersion,
        command: job.command,
        coreType: job.coreType,
        coresPerSlot: job.coresPerSlot,
        walltimeHours: job.walltimeHours,
        slots: job.slots,
        licenseSettings: job.licenseSettings,
        extraInputFileIds: job.extraInputFileIds,
        noDecompress: job.noDecompress,
        submitMode: job.submitMode,
        isLowPriority: job.isLowPriority,
        onDemandLicenseSeller: job.onDemandLicenseSeller,
        tags: job.tags || [],
        projectId: job.projectId,
        automations: job.automations || [],
      } as JobSpec
    } catch (error) {
      console.error('Failed to load job from SGE:', error)
      return null
    }
  },

  saveJobToSGE: async (path: string, job: JobSpec) => {
    await App.SaveJobToSGE(path, job as wailsapp.JobSpecDTO)
  },

  // API Cache Actions
  // v4.0.6: Updated to handle new result DTOs with error propagation
  fetchCoreTypes: async () => {
    set({ isLoadingCoreTypes: true, coreTypesError: null })
    try {
      const result = await App.GetCoreTypes()
      // v4.0.6: Check for error in result DTO
      if (result.error) {
        console.error('Failed to fetch core types:', result.error)
        set({ coreTypesError: result.error })
        return
      }
      // Map DTO to our local type
      const mapped: CoreType[] = (result.coreTypes || []).map((ct) => ({
        code: ct.code,
        name: ct.name,
        displayOrder: ct.displayOrder,
        isActive: ct.isActive,
        cores: ct.cores || [],
      }))
      set({ coreTypes: mapped, coreTypesError: null })
    } catch (error) {
      const errMsg = error instanceof Error ? error.message : String(error)
      console.error('Failed to fetch core types:', errMsg)
      set({ coreTypesError: errMsg })
    } finally {
      set({ isLoadingCoreTypes: false })
    }
  },

  // v4.0.6: Updated to handle new result DTOs with error propagation
  fetchAnalysisCodes: async (search = '') => {
    set({ isLoadingAnalysisCodes: true, analysisCodesError: null })
    try {
      const result = await App.GetAnalysisCodes(search)
      // v4.0.6: Check for error in result DTO
      if (result.error) {
        console.error('Failed to fetch analysis codes:', result.error)
        set({ analysisCodesError: result.error })
        return
      }
      // Map DTO to our local type
      const mapped: AnalysisCode[] = (result.codes || []).map((ac) => ({
        code: ac.code,
        name: ac.name,
        description: ac.description || '',
        vendorName: ac.vendorName || '',
        versions: (ac.versions || []).map((v) => ({
          id: v.id,
          version: v.version,
          versionCode: v.versionCode,
          allowedCoreTypes: v.allowedCoreTypes || [],
        })),
      }))
      set({ analysisCodes: mapped, analysisCodesError: null })
    } catch (error) {
      const errMsg = error instanceof Error ? error.message : String(error)
      console.error('Failed to fetch analysis codes:', errMsg)
      set({ analysisCodesError: errMsg })
    } finally {
      set({ isLoadingAnalysisCodes: false })
    }
  },

  // v4.0.6: Updated to handle new result DTOs with error propagation
  fetchAutomations: async () => {
    set({ isLoadingAutomations: true, automationsError: null })
    try {
      const result = await App.GetAutomations()
      // v4.0.6: Check for error in result DTO
      if (result.error) {
        console.error('Failed to fetch automations:', result.error)
        set({ automationsError: result.error })
        return
      }
      // Map DTO to our local type
      const mapped: Automation[] = (result.automations || []).map((a) => ({
        id: a.id,
        name: a.name,
        description: a.description || '',
        executeOn: a.executeOn,
        scriptName: a.scriptName,
      }))
      set({ automations: mapped, automationsError: null })
    } catch (error) {
      const errMsg = error instanceof Error ? error.message : String(error)
      console.error('Failed to fetch automations:', errMsg)
      set({ automationsError: errMsg })
    } finally {
      set({ isLoadingAutomations: false })
    }
  },

  // Memory Actions
  saveMemory: () => {
    const { template, scanOptions } = get()
    const memory: WorkflowMemory = {
      lastTemplate: template,
      lastScanDir: scanOptions.rootDir,
      lastPattern: scanOptions.pattern,
      lastCoreType: template.coreType,
      lastAnalysisCode: template.analysisCode,
      lastProjectId: template.projectId,
    }
    try {
      localStorage.setItem(MEMORY_KEY, JSON.stringify(memory))
    } catch (error) {
      console.error('Failed to save workflow memory:', error)
    }
    set({ memory })
  },

  loadMemory: () => {
    try {
      const saved = localStorage.getItem(MEMORY_KEY)
      if (saved) {
        const memory = JSON.parse(saved) as WorkflowMemory
        set({
          memory,
          template: memory.lastTemplate || { ...DEFAULT_JOB_TEMPLATE },
          scanOptions: {
            ...get().scanOptions,
            rootDir: memory.lastScanDir || '',
            pattern: memory.lastPattern || 'Run_*',
          },
        })
      }
    } catch (error) {
      console.error('Failed to load workflow memory:', error)
    }
  },
}))
