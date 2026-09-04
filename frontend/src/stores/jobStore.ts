import { create } from 'zustand'
import * as App from '../../wailsjs/go/wailsapp/App'
import { wailsapp } from '../../wailsjs/go/models'

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

// Project from API. remainingAmounts are the platform's own budget lines
// ("(no budget)", "All: My budget ($100.00 available)") — display strings.
export interface Project {
  id: string
  name: string
  isDefault: boolean
  remainingAmounts: string[]
}

// Secondary pattern for file scanning mode
export interface SecondaryPattern {
  pattern: string   // Glob pattern, may include subpath (e.g., "*.mesh", "../meshes/*.cfg")
  required: boolean // If true, skip job when file missing; if false, warn and continue
}

// PUR run options (beyond job list)
export interface PURRunOptions {
  commonInputFiles: string   // Comma-separated paths and/or id:fileId, shared by all jobs
  decompressCommon: boolean
  rmTarOnSuccess: boolean
  uploadFolder: string       // Remote folder path for this batch's uploads, created if missing
  uploadFolderParent: string // Folder ID uploadFolder resolves beneath (empty = My Library)
  fileTags: string[]         // Tags applied to every file this batch uploads
}

// One swept parameter in a DOE. A non-empty values list makes the parameter
// categorical and the numeric range is then ignored.
export interface DOEParameter {
  name: string
  min: number
  max: number
  levels: number
  values: string[]
  format: string
}

// A DOE sampling method, described by the backend so this list cannot drift
// from what generation accepts.
export interface DOEMethod {
  method: string
  label: string
  description: string
  usesSamples: boolean
  usesLevels: boolean
  usesCenterPoints: boolean
  usesCases: boolean
  minParameters: number
  maxParameters: number
}

// DOE sweep configuration
export interface DOEOptions {
  method: string
  parameters: DOEParameter[]
  samples: number
  seed: number
  centerPoints: number
  maxCases: number
  jobNameTemplate: string
  tagTemplates: string[]
  baseFileIds: string // Comma-separated IDs of an already-uploaded shared deck

  // casesCSV holds the explicit method's cases as pasted text: a header row
  // naming the parameters, then one row per case.
  casesCSV: string
}

// One generated case
export interface DOECase {
  index: number
  values: Record<string, string>
  jobName: string
  command: string
  tags: string[]
}

// One validation finding. Errors block generation; warnings do not.
export interface DOEProblem {
  code: string
  param: string
  message: string
}

// Result of previewing or generating a sweep. caseCount is the whole design;
// cases may be truncated for preview.
export interface DOEResult {
  ok: boolean
  caseCount: number
  truncated: boolean
  cases: DOECase[]
  errors: DOEProblem[]
  warnings: DOEProblem[]
}

// How many cases the live preview asks for. The whole sweep is generated only
// when the user commits to it.
export const DOE_PREVIEW_LIMIT = 25

// Scan options
export interface ScanOptions {
  rootDir: string
  pattern: string
  validationPattern: string
  runSubpath: string
  recursive: boolean
  includeHidden: boolean

  scanMode: 'folders' | 'files' | 'doe'
  primaryPattern: string           // For file mode: e.g., "*.inp", "inputs/*.inp"
  secondaryPatterns: SecondaryPattern[]

  // Subdirectory within each Run_* to tar
  tarSubpath: string

  // Vary command across runs (iterate numeric patterns)
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
  licenseFeatureName: '',
  licensesPerJob: 0,
  tags: [],
  projectId: '',
  orgCode: '',
  automations: [],
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
  projects: Project[]
  isLoadingCoreTypes: boolean
  isLoadingAnalysisCodes: boolean
  isLoadingAutomations: boolean
  isLoadingProjects: boolean
  coreTypesError: string | null
  analysisCodesError: string | null
  automationsError: string | null
  projectsError: string | null
  // Whether a scan has been attempted this session. An empty result is a valid
  // answer, so emptiness cannot be the signal to fetch — an account with no
  // projects (or no coretypes) would put the fetch-on-open effects into a loop.
  coreTypesLoaded: boolean
  projectsLoaded: boolean

  // PUR run options
  purRunOptions: PURRunOptions

  // Scan state
  scanOptions: ScanOptions
  isScanning: boolean
  scanError: string | null
  // Files the scan declined to turn into jobs, and non-fatal notes about the
  // scan. Surfaced because a silent skip looks like a file that simply was not
  // there.
  scanSkippedFiles: string[]
  scanWarnings: string[]

  // DOE state
  doeOptions: DOEOptions
  doeMethods: DOEMethod[]
  doePreview: DOEResult | null
  isGeneratingDOE: boolean
  doeError: string | null

  // Workflow memory
  memory: WorkflowMemory

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

  // Actions - DOE
  setDOEOptions: (opts: Partial<DOEOptions>) => void
  fetchDOEMethods: () => Promise<void>
  previewDOE: () => Promise<void>
  generateDOE: () => Promise<void>

  // Actions - Validation
  validateJobs: () => Promise<string[]>
  updateJobRow: (index: number, updates: Partial<JobRow>) => void

  // Actions - Execution
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
  fetchProjects: () => Promise<void>

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

// buildDOEOptionsDTO converts the store's sweep configuration into the shape the
// Go binding expects. Empty optional fields are sent as-is; the backend fills in
// its own defaults for them.
function buildDOEOptionsDTO(opts: DOEOptions, template: JobSpec): wailsapp.DOEOptionsDTO {
  return {
    template: template as wailsapp.JobSpecDTO,
    parameters: opts.parameters.map((p) => ({
      name: p.name.trim(),
      min: p.min,
      max: p.max,
      levels: p.levels,
      // An empty list means "not categorical", so it must be omitted rather
      // than sent as [], which would mean "categorical with no values".
      values: p.values.length > 0 ? p.values : undefined,
      format: p.format,
    })),
    method: opts.method,
    samples: opts.samples,
    seed: opts.seed,
    centerPoints: opts.centerPoints,
    cases: parseCasesCSV(opts.casesCSV),
    baseFileIds: splitList(opts.baseFileIds),
    jobNameTemplate: opts.jobNameTemplate,
    tagTemplates: opts.tagTemplates.filter(Boolean),
    maxCases: opts.maxCases,
  } as wailsapp.DOEOptionsDTO
}

// parseCasesCSV reads pasted cases: a header row naming the parameters, then one
// row per case. Rows whose column count does not match the header are dropped,
// which the backend then reports as a case missing a parameter value.
//
// Quoting is not handled because a parameter value cannot contain a quote or a
// comma anyway — the backend rejects values that would restructure the command.
export function parseCasesCSV(text: string): Array<Record<string, string>> {
  const rows = text
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter(Boolean)

  if (rows.length < 2) return []

  const names = rows[0].split(',').map((name) => name.trim())

  return rows.slice(1).flatMap((row) => {
    const values = row.split(',').map((value) => value.trim())
    if (values.length !== names.length) return []

    const entry: Record<string, string> = {}
    names.forEach((name, i) => {
      entry[name] = values[i]
    })
    return [entry]
  })
}

// toDOEResult normalizes a result DTO so the UI never has to guard for null
// arrays.
function toDOEResult(result: wailsapp.DOEResultDTO): DOEResult {
  return {
    ok: result.ok,
    caseCount: result.caseCount,
    truncated: result.truncated,
    // Each case is mapped field by field rather than cast: the Go DTO marks the
    // per-case tags omitempty and a nil values map marshals to null, so a case
    // with no tags arrives without the field at all. DOECase declares both as
    // present, so casting would hand the UI a `tags` that is undefined and
    // crash the first `.length` read on it.
    cases: (result.cases || []).map((c) => ({
      index: c.index,
      values: c.values || {},
      jobName: c.jobName,
      command: c.command,
      tags: c.tags || [],
    })),
    errors: (result.errors || []) as DOEProblem[],
    warnings: (result.warnings || []) as DOEProblem[],
  }
}

// splitList parses a comma-separated field into trimmed, non-empty entries.
function splitList(value: string): string[] {
  return value
    .split(',')
    .map((entry) => entry.trim())
    .filter(Boolean)
}

// toJobRows builds the pending table rows for a freshly created job list.
function toJobRows(jobs: JobSpec[]): JobRow[] {
  return jobs.map((job, index) => ({
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
}

export const useJobStore = create<JobStore>((set, get) => ({
  // Initial state
  workflowState: 'initial',
  workflowPath: 'unknown',
  errorMessage: '',

  template: { ...DEFAULT_JOB_TEMPLATE },
  scannedJobs: [],
  jobRows: [],
  // Pre-execution only; during execution, use runStore.activeRun
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
  projects: [],
  isLoadingCoreTypes: false,
  isLoadingAnalysisCodes: false,
  isLoadingAutomations: false,
  isLoadingProjects: false,
  coreTypesError: null,
  analysisCodesError: null,
  automationsError: null,
  projectsError: null,
  coreTypesLoaded: false,
  projectsLoaded: false,

  purRunOptions: {
    commonInputFiles: '',
    decompressCommon: false,
    rmTarOnSuccess: false,
    uploadFolder: '',
    uploadFolderParent: '',
    fileTags: [],
  },

  scanOptions: {
    rootDir: '',
    pattern: 'Run_*',
    validationPattern: '',
    runSubpath: '',
    recursive: false,
    includeHidden: false,
    scanMode: 'folders' as const,
    primaryPattern: '*.inp',
    secondaryPatterns: [],
    tarSubpath: '',
    iteratePatterns: false,
  },
  isScanning: false,
  scanError: null,
  scanSkippedFiles: [],
  scanWarnings: [],

  doeOptions: {
    method: 'full-factorial',
    parameters: [],
    samples: 20,
    seed: 0,
    centerPoints: 1,
    maxCases: 0,
    jobNameTemplate: '',
    tagTemplates: [],
    baseFileIds: '',
    casesCSV: '',
  },
  doeMethods: [],
  doePreview: null,
  isGeneratingDOE: false,
  doeError: null,

  memory: {
    lastTemplate: { ...DEFAULT_JOB_TEMPLATE },
    lastScanDir: '',
    lastPattern: 'Run_*',
    lastCoreType: '',
    lastAnalysisCode: '',
    lastProjectId: '',
  },

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
    if (
      workflowState !== 'pathChosen' ||
      (workflowPath !== 'createNew' && workflowPath !== 'createSweep')
    )
      return

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
      scanSkippedFiles: [],
      scanWarnings: [],
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

    set({ isScanning: true, scanError: null, scanSkippedFiles: [], scanWarnings: [] })

    try {
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
          scanMode: scanOptions.scanMode,
          primaryPattern: scanOptions.primaryPattern,
          secondaryPatterns: secondaryPatternsDTO,
          tarSubpath: scanOptions.tarSubpath,
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
        // Both are omitempty on the Go side, so absent means none.
        scanSkippedFiles: result.skippedFiles || [],
        scanWarnings: result.warnings || [],
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

  // DOE Actions
  setDOEOptions: (opts) => {
    set((state) => ({
      doeOptions: { ...state.doeOptions, ...opts },
      // Any edit invalidates the preview, so it is dropped rather than left
      // showing a design the current options no longer describe.
      doePreview: null,
    }))
  },

  fetchDOEMethods: async () => {
    try {
      const methods = await App.GetDOEMethods()
      set({ doeMethods: (methods || []) as DOEMethod[] })
    } catch (error) {
      console.error('Failed to fetch DOE methods:', error)
    }
  },

  previewDOE: async () => {
    const { doeOptions, template } = get()

    if (doeOptions.parameters.length === 0) {
      set({ doePreview: null, doeError: null })
      return
    }

    try {
      const result = await App.PreviewDOECases(
        buildDOEOptionsDTO(doeOptions, template),
        DOE_PREVIEW_LIMIT,
      )
      set({ doePreview: toDOEResult(result), doeError: null })
    } catch (error) {
      set({
        doePreview: null,
        doeError: error instanceof Error ? error.message : String(error),
      })
    }
  },

  generateDOE: async () => {
    const { doeOptions, template } = get()

    set({ isGeneratingDOE: true, doeError: null })

    try {
      const result = await App.GenerateDOE(buildDOEOptionsDTO(doeOptions, template))

      set({ doePreview: toDOEResult(result) })

      if (!result.ok) {
        set({
          isGeneratingDOE: false,
          doeError: (result.errors || [])
            .map((p) => p.message)
            .join('; ') || 'Sweep validation failed',
        })
        return
      }

      // A sweep is just a job list, so it joins the normal PUR flow here and
      // inherits validation, tar/upload, submission and resume unchanged.
      const jobs = (result.jobs || []) as JobSpec[]

      set({
        scannedJobs: jobs,
        jobRows: toJobRows(jobs),
        workflowState: 'directoriesScanned',
        isGeneratingDOE: false,
      })
    } catch (error) {
      set({
        isGeneratingDOE: false,
        doeError: error instanceof Error ? error.message : String(error),
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

  cancelRun: async () => {
    const { useRunStore } = await import('./runStore')
    await useRunStore.getState().cancelRun()
    set({ workflowState: 'completed' })
  },

  // Pre-execution stat queries only; polling lives in runStore.

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
        licenseFeatureName: job.licenseFeatureName || '',
        licensesPerJob: job.licensesPerJob || 0,
        tags: job.tags || [],
        projectId: job.projectId,
        orgCode: job.orgCode || '',
        automations: job.automations || [],
        // The CSV carries both, and dropping them here would silently undo a
        // file-scan run saved to CSV and loaded back.
        localInputFiles: job.localInputFiles || [],
        tarSubpath: job.tarSubpath || '',
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
        licenseFeatureName: job.licenseFeatureName || '',
        licensesPerJob: job.licensesPerJob || 0,
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
        licenseFeatureName: job.licenseFeatureName || '',
        licensesPerJob: job.licensesPerJob || 0,
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
  fetchCoreTypes: async () => {
    set({ isLoadingCoreTypes: true, coreTypesError: null })
    try {
      const result = await App.GetCoreTypes()
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
      // Marked on any outcome, so an account that legitimately returns no
      // coretypes does not restart the fetch-on-open effect forever.
      set({ isLoadingCoreTypes: false, coreTypesLoaded: true })
    }
  },

  fetchAnalysisCodes: async (search = '') => {
    set({ isLoadingAnalysisCodes: true, analysisCodesError: null })
    try {
      const result = await App.GetAnalysisCodes(search)
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

  fetchAutomations: async () => {
    set({ isLoadingAutomations: true, automationsError: null })
    try {
      const result = await App.GetAutomations()
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

  fetchProjects: async () => {
    set({ isLoadingProjects: true, projectsError: null })
    try {
      const result = await App.GetProjects()
      if (result.error) {
        console.error('Failed to fetch projects:', result.error)
        set({ projectsError: result.error })
        return
      }
      const mapped: Project[] = (result.projects || []).map((p) => ({
        id: p.id,
        name: p.name,
        isDefault: p.isDefault,
        remainingAmounts: p.remainingAmounts || [],
      }))
      set({ projects: mapped, projectsError: null })
    } catch (error) {
      const errMsg = error instanceof Error ? error.message : String(error)
      console.error('Failed to fetch projects:', errMsg)
      set({ projectsError: errMsg })
    } finally {
      // Marked on any outcome, including failure: the button is how a retry
      // happens, not another pass of the effect.
      set({ isLoadingProjects: false, projectsLoaded: true })
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
          // Layered over the defaults, not used raw: a template stored by an
          // older build is missing any field added since, and the builder reads
          // those fields directly (a missing string would break .trim()).
          template: { ...DEFAULT_JOB_TEMPLATE, ...(memory.lastTemplate || {}) },
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
