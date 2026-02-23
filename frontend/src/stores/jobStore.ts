import { create } from 'zustand'
import * as App from '../../wailsjs/go/wailsapp/App'
import { wailsapp } from '../../wailsjs/go/models'

// v4.7.3: Shared domain types imported from types/jobs.ts (breaks import cycles).
// Re-exported here for backward compatibility with existing imports.
import type {
  WorkflowState,
  WorkflowPath,
  JobSpec,
  JobRow,
  RunStatus,
  PipelineLogEntry,
  PipelineStageStats,
} from '../types/jobs'

export type { WorkflowState, WorkflowPath, JobSpec, JobRow, RunStatus, PipelineLogEntry, PipelineStageStats }

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
  directory: '',
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
  orgCode: '',
  automations: [],
}

// v4.7.3: PipelineLogEntry and PipelineStageStats are now defined in types/jobs.ts
// and re-exported above for backward compatibility.

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

  // v4.7.3: Pipeline diagnostics, event subscriptions, and polling moved to runStore.ts.
  // pipelineLogs, pipelineStageStats, _eventUnsubs, startTime, isPolling, _pollInterval removed.

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

  // Actions - Execution (v4.7.3: polling/events moved to runStore)
  startBulkRun: () => Promise<string | null>
  cancelRun: () => Promise<void>

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
  // v4.7.3: runStatus and runId kept for pre-execution phases; during execution, use runStore.activeRun
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

  // v4.7.3: pipelineLogs, pipelineStageStats, _eventUnsubs, startTime, isPolling, _pollInterval
  // removed — all live in runStore.ts now.

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
    // v4.7.3: Polling lifecycle now owned by runStore; stopPolling removed from here.
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
  // v4.7.3: startBulkRun refactored — event subscriptions and polling moved to runStore.
  startBulkRun: async () => {
    const { scannedJobs, jobRows } = get()

    if (scannedJobs.length === 0) {
      set({ errorMessage: 'No jobs to run' })
      return null
    }

    try {
      set({ workflowState: 'executing' })

      const runId = await App.StartBulkRunWithOptions(
        scannedJobs as wailsapp.JobSpecDTO[],
        get().purRunOptions as wailsapp.PURRunOptionsDTO,
      )
      set({ runId })

      // Build initial jobRows (all pending) and register with runStore
      const initialJobRows: JobRow[] = jobRows.length > 0
        ? jobRows.map((r) => ({ ...r, tarStatus: r.tarStatus || 'pending', uploadStatus: r.uploadStatus || 'pending', submitStatus: r.submitStatus || 'pending', status: 'pending' }))
        : scannedJobs.map((job, i) => ({
            index: i,
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

      // Register with runStore and start polling there
      const { useRunStore } = await import('./runStore')
      const runStore = useRunStore.getState()
      runStore.registerRun(runId, 'pur', scannedJobs.length, initialJobRows)
      runStore.startPolling(3000)

      return runId
    } catch (error) {
      set({
        workflowState: 'error',
        errorMessage: error instanceof Error ? error.message : String(error),
      })
      return null
    }
  },

  // v4.7.3: cancelRun delegates to runStore
  cancelRun: async () => {
    const { useRunStore } = await import('./runStore')
    await useRunStore.getState().cancelRun()
    set({ workflowState: 'completed' })
  },

  // v4.7.3: refreshRunStatus, startPolling, stopPolling removed — now in runStore.
  // refreshJobsStats kept for pre-execution stat queries.

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
        orgCode: job.orgCode || '',
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
        orgCode: job.orgCode || '',
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
        orgCode: job.orgCode || '',
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
