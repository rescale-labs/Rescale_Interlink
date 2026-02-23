import { useEffect, useCallback, useState, useMemo } from 'react'
import {
  FolderPlusIcon,
  DocumentArrowUpIcon,
  ArrowPathIcon,
  PlayIcon,
  StopIcon,
  CheckCircleIcon,
  XCircleIcon,
  ExclamationTriangleIcon,
  ArrowLeftIcon,
  FolderOpenIcon,
  DocumentArrowDownIcon,
  CheckIcon,
  ChevronDownIcon,
  EyeIcon,
} from '@heroicons/react/24/outline'
import clsx from 'clsx'
import { useJobStore, useConfigStore, useRunStore } from '../../stores'
import type { WorkflowState } from '../../types/jobs'
import { wailsapp } from '../../../wailsjs/go/models'
import { TemplateBuilder, JobsTable, StatsBar, PipelineStageSummary, PipelineLogPanel, ErrorSummary } from '../widgets'
import { formatDuration } from '../../utils/formatDuration'
import * as App from '../../../wailsjs/go/wailsapp/App'
import * as Runtime from '../../../wailsjs/runtime/runtime'

// v4.0.0 G2: Improved workflow steps with user-friendly labels
const WORKFLOW_STEPS: { state: WorkflowState; label: string; shortLabel: string }[] = [
  { state: 'initial', label: 'Choose Source', shortLabel: 'Source' },
  { state: 'pathChosen', label: 'Configure', shortLabel: 'Config' },
  { state: 'templateReady', label: 'Scan to Create Jobs', shortLabel: 'Scan' },
  { state: 'directoriesScanned', label: 'Validate Jobs', shortLabel: 'Validate' },
  { state: 'jobsValidated', label: 'Ready to Run', shortLabel: 'Ready' },
  { state: 'executing', label: 'Running', shortLabel: 'Running' },
  { state: 'completed', label: 'Complete', shortLabel: 'Done' },
]

// v4.7.3: StatusBadge, StatsBar, JobsTable, PipelineStageSummary, PipelineLogPanel,
// ErrorSummary extracted to widgets/ for reuse. Imported above.

// v4.0.0 G2: Improved progress bar with numbered steps and checkmarks
function WorkflowProgressBar({ currentState }: { currentState: WorkflowState }) {
  const currentIndex = WORKFLOW_STEPS.findIndex((s) => s.state === currentState)

  return (
    <div className="flex items-center justify-center mb-6">
      {WORKFLOW_STEPS.map((step, index) => {
        const isCompleted = index < currentIndex
        const isCurrent = step.state === currentState
        const isFuture = index > currentIndex
        const stepNumber = index + 1

        return (
          <div key={step.state} className="flex items-center">
            <div className="flex flex-col items-center">
              {/* Step circle with number or checkmark */}
              <div
                className={clsx(
                  'w-8 h-8 rounded-full flex items-center justify-center text-sm font-semibold transition-colors',
                  isCompleted && 'bg-green-500 text-white',
                  isCurrent && 'bg-blue-500 text-white ring-4 ring-blue-100',
                  isFuture && 'bg-gray-200 text-gray-500'
                )}
              >
                {isCompleted ? (
                  <CheckIcon className="w-5 h-5" />
                ) : (
                  stepNumber
                )}
              </div>
              {/* Step label */}
              <span
                className={clsx(
                  'text-xs mt-1 whitespace-nowrap',
                  isCompleted && 'text-green-600 font-medium',
                  isCurrent && 'text-blue-600 font-semibold',
                  isFuture && 'text-gray-400'
                )}
              >
                {step.shortLabel}
              </span>
            </div>
            {/* Connector line */}
            {index < WORKFLOW_STEPS.length - 1 && (
              <div
                className={clsx(
                  'w-12 h-0.5 mx-2 mt-[-1rem] transition-colors',
                  index < currentIndex ? 'bg-green-500' : 'bg-gray-200'
                )}
              />
            )}
          </div>
        )
      })}
    </div>
  )
}

// v4.7.1: Shared Pipeline Settings component for workers + tar options
const COMPRESSION_OPTIONS = ['gzip', 'none'] as const

function PipelineSettings({ config, updateConfig, saveConfig }: {
  config: wailsapp.ConfigDTO | null
  updateConfig: (updates: Partial<wailsapp.ConfigDTO>) => void
  saveConfig: () => Promise<void>
}) {
  return (
    <div className="border-t pt-4 mt-4">
      <h4 className="text-sm font-medium text-gray-700 dark:text-gray-300 mb-3">Pipeline Settings</h4>
      {/* Worker Configuration */}
      <div className="mb-4">
        <label className="block text-xs font-medium text-gray-500 mb-2">Worker Configuration</label>
        <div className="grid grid-cols-3 gap-3">
          <div>
            <label className="block text-xs text-gray-500 mb-1">Tar Workers</label>
            <input
              type="number"
              min={1}
              max={16}
              className="w-full px-3 py-2 text-sm border border-gray-300 dark:border-gray-600 rounded bg-white dark:bg-gray-800 focus:outline-none focus:ring-2 focus:ring-blue-500"
              value={config?.tarWorkers || 4}
              onChange={(e) => updateConfig({ tarWorkers: parseInt(e.target.value) || 4 })}
              onBlur={() => saveConfig()}
            />
          </div>
          <div>
            <label className="block text-xs text-gray-500 mb-1">Upload Workers</label>
            <input
              type="number"
              min={1}
              max={16}
              className="w-full px-3 py-2 text-sm border border-gray-300 dark:border-gray-600 rounded bg-white dark:bg-gray-800 focus:outline-none focus:ring-2 focus:ring-blue-500"
              value={config?.uploadWorkers || 4}
              onChange={(e) => updateConfig({ uploadWorkers: parseInt(e.target.value) || 4 })}
              onBlur={() => saveConfig()}
            />
          </div>
          <div>
            <label className="block text-xs text-gray-500 mb-1">Job Workers</label>
            <input
              type="number"
              min={1}
              max={16}
              className="w-full px-3 py-2 text-sm border border-gray-300 dark:border-gray-600 rounded bg-white dark:bg-gray-800 focus:outline-none focus:ring-2 focus:ring-blue-500"
              value={config?.jobWorkers || 4}
              onChange={(e) => updateConfig({ jobWorkers: parseInt(e.target.value) || 4 })}
              onBlur={() => saveConfig()}
            />
          </div>
        </div>
      </div>
      {/* Tar Options */}
      <div>
        <label className="block text-xs font-medium text-gray-500 mb-2">Tar Options</label>
        <div className="space-y-3">
          <div>
            <label className="block text-xs text-gray-500 mb-1">Exclude Patterns</label>
            <input
              type="text"
              className="w-full px-3 py-2 text-sm border border-gray-300 dark:border-gray-600 rounded bg-white dark:bg-gray-800 focus:outline-none focus:ring-2 focus:ring-blue-500"
              placeholder="*.tmp,*.log,*.bak"
              value={config?.excludePatterns || ''}
              onChange={(e) => updateConfig({ excludePatterns: e.target.value })}
              onBlur={() => saveConfig()}
            />
          </div>
          <div>
            <label className="block text-xs text-gray-500 mb-1">Include Patterns</label>
            <input
              type="text"
              className="w-full px-3 py-2 text-sm border border-gray-300 dark:border-gray-600 rounded bg-white dark:bg-gray-800 focus:outline-none focus:ring-2 focus:ring-blue-500"
              placeholder="*.dat,*.csv,*.inp"
              value={config?.includePatterns || ''}
              onChange={(e) => updateConfig({ includePatterns: e.target.value })}
              onBlur={() => saveConfig()}
            />
          </div>
          <div>
            <label className="block text-xs text-gray-500 mb-1">Compression</label>
            <select
              className="w-full px-3 py-2 text-sm border border-gray-300 dark:border-gray-600 rounded bg-white dark:bg-gray-800 focus:outline-none focus:ring-2 focus:ring-blue-500"
              value={config?.tarCompression || 'gzip'}
              onChange={(e) => {
                updateConfig({ tarCompression: e.target.value })
                saveConfig()
              }}
            >
              {COMPRESSION_OPTIONS.map((opt) => (
                <option key={opt} value={opt}>{opt}</option>
              ))}
            </select>
          </div>
          <div className="flex items-center">
            <input
              type="checkbox"
              id="pipelineFlattenTar"
              checked={config?.flattenTar || false}
              onChange={(e) => {
                updateConfig({ flattenTar: e.target.checked })
                saveConfig()
              }}
              className="h-4 w-4 rounded border border-gray-300 text-blue-500 focus:ring-blue-500 focus:ring-2 bg-white cursor-pointer"
            />
            <label htmlFor="pipelineFlattenTar" className="ml-2 text-sm text-gray-700 dark:text-gray-300 cursor-pointer">
              Flatten directory structure in tar
            </label>
          </div>
        </div>
        <p className="mt-1 text-xs text-gray-400">
          Patterns support wildcards (*). Use comma-separated list.
        </p>
      </div>
    </div>
  )
}

export function PURTab() {
  const {
    workflowState,
    workflowPath,
    errorMessage,
    template,
    scannedJobs,
    jobRows,
    runStatus,
    scanOptions,
    isScanning,
    scanError,
    setWorkflowPath,
    setTemplate,
    goBack,
    reset,
    clearError,
    canGoBack,
    setScanOptions,
    scanDirectory,
    validateJobs,
    startBulkRun,
    cancelRun,
    loadMemory,
    loadJobsFromCSV,
    saveJobsToCSV,
    loadJobFromSGE,
    saveJobToJSON,
    saveJobToSGE,
    purRunOptions,
    setPURRunOptions,
  } = useJobStore()

  // v4.7.3: Run store for run session persistence
  const {
    activeRun,
    queuedJob,
    queueStatus,
    purViewMode,
    setPurViewMode,
    clearActiveRun,
    setQueuedJob,
    cancelRun: cancelActiveRun,
  } = useRunStore()

  // v4.7.1: Config store for Pipeline Settings
  const { config, updateConfig, saveConfig } = useConfigStore()

  // Template builder dialog state
  const [showTemplateBuilder, setShowTemplateBuilder] = useState(false)
  const [validationErrors, setValidationErrors] = useState<string[]>([])
  const [isValidating, setIsValidating] = useState(false)
  const [showLoadMenu, setShowLoadMenu] = useState(false)
  const [showSaveMenu, setShowSaveMenu] = useState(false)
  const [loadSaveError, setLoadSaveError] = useState<string | null>(null)
  const [monitorBannerCollapsed, setMonitorBannerCollapsed] = useState(false)

  // v4.7.3: Compute effective view mode
  const effectiveView = useMemo(() => {
    if (purViewMode === 'monitor' || purViewMode === 'configure') return purViewMode

    // Auto mode
    if (activeRun && activeRun.runType === 'pur' && activeRun.status === 'active') {
      // If user was mid-configuration, show configure with banner
      if (['pathChosen', 'templateReady', 'directoriesScanned', 'jobsValidated'].includes(workflowState)) {
        return 'configure' as const
      }
      return 'choice' as const
    }

    if (activeRun && activeRun.runType === 'pur' &&
        (activeRun.status === 'completed' || activeRun.status === 'failed' || activeRun.status === 'cancelled')) {
      return 'results' as const
    }

    return 'configure' as const
  }, [purViewMode, activeRun, workflowState])

  // Load workflow memory on mount
  useEffect(() => {
    loadMemory()
  }, [loadMemory])

  // v4.7.3 Fix: Sync jobStore.workflowState when run transitions to terminal state.
  // workflowState is set to 'executing' (jobStore.ts startBulkRun) but never transitions
  // to 'completed' on normal pipeline completion — only on cancel. This bridges the gap.
  useEffect(() => {
    if (activeRun && activeRun.runType === 'pur' &&
        (activeRun.status === 'completed' || activeRun.status === 'failed' || activeRun.status === 'cancelled') &&
        workflowState === 'executing') {
      useJobStore.setState({ workflowState: 'completed' })
    }
  }, [activeRun?.status, workflowState])

  // v4.7.1: Initialize scan options from persisted config
  useEffect(() => {
    if (config) {
      setScanOptions({
        runSubpath: scanOptions.runSubpath || config.runSubpath || '',
        validationPattern: scanOptions.validationPattern || config.validationPattern || '',
      })
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [config?.runSubpath, config?.validationPattern])

  // Local state for CSV loading
  const [isLoadingCSV, setIsLoadingCSV] = useState(false)
  const [csvLoadError, setCsvLoadError] = useState<string | null>(null)

  // Handle path selection - CSV loading
  const handleLoadCSV = useCallback(async () => {
    try {
      // Open file dialog to select CSV file
      const path = await App.SelectFile('Select Jobs CSV File')
      if (!path) return // User cancelled

      // Validate extension
      if (!path.toLowerCase().endsWith('.csv')) {
        setCsvLoadError('Please select a CSV file')
        return
      }

      setIsLoadingCSV(true)
      setCsvLoadError(null)

      await loadJobsFromCSV(path)
    } catch (error) {
      setCsvLoadError(error instanceof Error ? error.message : String(error))
    } finally {
      setIsLoadingCSV(false)
    }
  }, [loadJobsFromCSV])

  const handleCreateNew = useCallback(() => {
    setWorkflowPath('createNew')
  }, [setWorkflowPath])

  // v4.7.2: Map a loaded job DTO to template format
  const mapDTOToTemplate = useCallback((loaded: wailsapp.JobSpecDTO) => {
    setTemplate({
      directory: loaded.directory || '',
      jobName: loaded.jobName || '',
      analysisCode: loaded.analysisCode || '',
      analysisVersion: loaded.analysisVersion || '',
      command: loaded.command || '',
      coreType: loaded.coreType || '',
      coresPerSlot: loaded.coresPerSlot || 4,
      walltimeHours: loaded.walltimeHours || 1,
      slots: loaded.slots || 1,
      licenseSettings: loaded.licenseSettings || '',
      extraInputFileIds: loaded.extraInputFileIds || '',
      noDecompress: loaded.noDecompress || false,
      submitMode: loaded.submitMode || 'create_and_submit',
      isLowPriority: loaded.isLowPriority || false,
      onDemandLicenseSeller: loaded.onDemandLicenseSeller || '',
      tags: loaded.tags || [],
      projectId: loaded.projectId || '',
      orgCode: loaded.orgCode || '',
      automations: loaded.automations || [],
    })
  }, [setTemplate])

  // v4.7.2: Load template from CSV file
  const handleLoadTemplateFromCSV = useCallback(async () => {
    setShowLoadMenu(false)
    try {
      const path = await App.SelectFile('Select Jobs CSV File')
      if (!path) return
      setLoadSaveError(null)

      const jobs = await App.LoadJobsFromCSV(path)
      if (jobs && jobs.length > 0) {
        mapDTOToTemplate(jobs[0])
      }
    } catch (error) {
      setLoadSaveError(error instanceof Error ? error.message : String(error))
    }
  }, [mapDTOToTemplate])

  // v4.7.2: Load template from JSON file
  const handleLoadTemplateFromJSON = useCallback(async () => {
    setShowLoadMenu(false)
    try {
      const path = await App.SelectFile('Select Template JSON File')
      if (!path) return
      setLoadSaveError(null)

      const jobs = await App.LoadJobsFromJSON(path)
      if (jobs && jobs.length > 0) {
        mapDTOToTemplate(jobs[0])
      }
    } catch (error) {
      setLoadSaveError(error instanceof Error ? error.message : String(error))
    }
  }, [mapDTOToTemplate])

  // v4.7.2: Load template from SGE script
  const handleLoadTemplateFromSGE = useCallback(async () => {
    setShowLoadMenu(false)
    try {
      const path = await App.SelectFile('Select SGE Script')
      if (!path) return
      setLoadSaveError(null)

      const loaded = await loadJobFromSGE(path)
      if (loaded) {
        setTemplate(loaded)
      }
    } catch (error) {
      setLoadSaveError(error instanceof Error ? error.message : String(error))
    }
  }, [loadJobFromSGE, setTemplate])

  // v4.7.2: Save template to CSV
  const handleSaveTemplateToCSV = useCallback(async () => {
    setShowSaveMenu(false)
    try {
      const path = await App.SaveFile('Save Template CSV')
      if (!path) return
      setLoadSaveError(null)

      await App.SaveJobsToCSV(path, [template as unknown as wailsapp.JobSpecDTO])
    } catch (error) {
      setLoadSaveError(error instanceof Error ? error.message : String(error))
    }
  }, [template])

  // v4.7.2: Save template to JSON
  const handleSaveTemplateToJSON = useCallback(async () => {
    setShowSaveMenu(false)
    try {
      const path = await App.SaveFile('Save Template JSON')
      if (!path) return
      setLoadSaveError(null)

      await saveJobToJSON(path, template)
    } catch (error) {
      setLoadSaveError(error instanceof Error ? error.message : String(error))
    }
  }, [template, saveJobToJSON])

  // v4.7.2: Save template to SGE script
  const handleSaveTemplateToSGE = useCallback(async () => {
    setShowSaveMenu(false)
    try {
      const path = await App.SaveFile('Save Template SGE Script')
      if (!path) return
      setLoadSaveError(null)

      await saveJobToSGE(path, template)
    } catch (error) {
      setLoadSaveError(error instanceof Error ? error.message : String(error))
    }
  }, [template, saveJobToSGE])

  // Handle template save from builder
  const handleTemplateSave = useCallback(
    (newTemplate: typeof template) => {
      setTemplate(newTemplate)
      setShowTemplateBuilder(false)
    },
    [setTemplate]
  )

  // Handle directory selection
  const handleSelectDirectory = useCallback(async () => {
    try {
      const dir = await App.SelectDirectory('Select Root Directory')
      if (dir) {
        setScanOptions({ rootDir: dir })
      }
    } catch (error) {
      console.error('Failed to select directory:', error)
    }
  }, [setScanOptions])

  // Handle scan
  const handleScan = useCallback(async () => {
    await scanDirectory()
  }, [scanDirectory])

  // Handle validation
  const handleValidate = useCallback(async () => {
    setIsValidating(true)
    try {
      const errors = await validateJobs()
      setValidationErrors(errors)
    } finally {
      setIsValidating(false)
    }
  }, [validateJobs])

  // Handle run
  const handleRun = useCallback(async () => {
    await startBulkRun()
  }, [startBulkRun])

  // Handle export to CSV
  const handleExportCSV = useCallback(async () => {
    try {
      const path = await App.SaveFile('Save Jobs CSV')
      if (!path) return // User cancelled

      await saveJobsToCSV(path)
      Runtime.EventsEmit('notification', {
        type: 'success',
        message: 'Jobs exported successfully',
      })
    } catch (error) {
      console.error('Failed to export CSV:', error)
    }
  }, [saveJobsToCSV])

  // v4.7.3: Handle cancel delegates to runStore
  const handleCancel = useCallback(async () => {
    await cancelActiveRun()
    // Also transition jobStore workflow state
    cancelRun()
  }, [cancelActiveRun, cancelRun])

  // v4.7.3: Handle start over clears both stores
  const handleStartOver = useCallback(async () => {
    await clearActiveRun()
    reset()
    setValidationErrors([])
    setPurViewMode('auto')
  }, [clearActiveRun, reset, setPurViewMode])

  // v4.7.3: Handle queue - deep-copies current config and stores in runStore
  const handleQueueRun = useCallback(() => {
    const jobsSnapshot = structuredClone(scannedJobs) as wailsapp.JobSpecDTO[]
    const optsSnapshot = structuredClone(purRunOptions) as wailsapp.PURRunOptionsDTO
    setQueuedJob({
      runType: 'pur',
      input: { jobs: jobsSnapshot, opts: optsSnapshot },
      queuedAt: Date.now(),
    })
    useRunStore.setState({ queueStatus: 'queued' })
  }, [scannedJobs, purRunOptions, setQueuedJob])

  // Render content based on workflow state
  const renderContent = () => {
    // Error state
    if (workflowState === 'error') {
      return (
        <div className="flex flex-col items-center justify-center h-full text-center">
          <XCircleIcon className="w-16 h-16 text-red-500 mb-4" />
          <h3 className="text-lg font-semibold text-red-600 mb-2">Error</h3>
          <p className="text-gray-600 dark:text-gray-400 mb-6 max-w-md">
            {errorMessage || 'An unexpected error occurred'}
          </p>
          <button
            onClick={() => {
              clearError()
              handleStartOver()
            }}
            className="px-4 py-2 bg-blue-500 text-white rounded hover:bg-blue-600"
          >
            Start Over
          </button>
        </div>
      )
    }

    // Initial state - path selection
    if (workflowState === 'initial') {
      return (
        <div className="flex flex-col items-center justify-center h-full">
          <h3 className="text-lg font-semibold mb-6">
            How would you like to configure jobs?
          </h3>
          {csvLoadError && (
            <div className="mb-4 p-3 bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800 rounded text-red-700 dark:text-red-400 text-sm max-w-md">
              {csvLoadError}
            </div>
          )}
          <div className="flex gap-6">
            <button
              onClick={handleLoadCSV}
              disabled={isLoadingCSV}
              className={clsx(
                'flex flex-col items-center gap-3 p-6 border-2 rounded-lg transition-colors w-56',
                isLoadingCSV
                  ? 'border-gray-300 bg-gray-100 cursor-wait'
                  : 'border-gray-200 dark:border-gray-700 hover:border-blue-500 hover:bg-blue-50 dark:hover:bg-blue-900/20'
              )}
            >
              {isLoadingCSV ? (
                <ArrowPathIcon className="w-12 h-12 text-blue-500 animate-spin" />
              ) : (
                <DocumentArrowUpIcon className="w-12 h-12 text-blue-500" />
              )}
              <span className="font-medium">
                {isLoadingCSV ? 'Loading...' : 'Load Jobs File'}
              </span>
              <span className="text-sm text-gray-500">Load from existing CSV</span>
            </button>
            <button
              onClick={handleCreateNew}
              disabled={isLoadingCSV}
              className="flex flex-col items-center gap-3 p-6 border-2 border-gray-200 dark:border-gray-700 rounded-lg hover:border-blue-500 hover:bg-blue-50 dark:hover:bg-blue-900/20 transition-colors w-56"
            >
              <FolderPlusIcon className="w-12 h-12 text-green-500" />
              <span className="font-medium">Create Jobs by Scanning</span>
              <span className="text-sm text-gray-500">Scan directories for jobs</span>
            </button>
          </div>
        </div>
      )
    }

    // Path chosen - show appropriate next step
    if (workflowState === 'pathChosen') {
      if (workflowPath === 'loadCSV') {
        return (
          <div className="flex flex-col items-center justify-center h-full">
            <h3 className="text-lg font-semibold mb-4">Load Jobs CSV</h3>
            <p className="text-gray-600 mb-6">
              Select a CSV file containing job configurations
            </p>
            <button
              onClick={handleLoadCSV}
              disabled={isLoadingCSV}
              className="flex items-center gap-2 px-4 py-2 bg-blue-500 text-white rounded hover:bg-blue-600 disabled:opacity-50 disabled:cursor-not-allowed"
            >
              {isLoadingCSV ? (
                <ArrowPathIcon className="w-5 h-5 animate-spin" />
              ) : (
                <DocumentArrowUpIcon className="w-5 h-5" />
              )}
              {isLoadingCSV ? 'Loading...' : 'Select CSV File'}
            </button>
            {csvLoadError && (
              <p className="mt-4 text-red-500 text-sm">{csvLoadError}</p>
            )}
          </div>
        )
      }

      // Create new path - template selection
      return (
        <div className="flex flex-col items-center justify-center h-full">
          <h3 className="text-lg font-semibold mb-4">Configure Base Job Settings</h3>
          <p className="text-gray-600 mb-6">
            Create or load base job settings to apply to all jobs
          </p>
          <div className="flex gap-4">
            <button
              onClick={() => setShowTemplateBuilder(true)}
              className="flex items-center gap-2 px-4 py-2 bg-blue-500 text-white rounded hover:bg-blue-600"
            >
              <FolderPlusIcon className="w-5 h-5" />
              Configure New Base Job Settings
            </button>
            {/* v4.7.2: Load dropdown with CSV, JSON, SGE */}
            <div className="relative">
              <button
                onClick={() => setShowLoadMenu(!showLoadMenu)}
                className="flex items-center gap-2 px-4 py-2 border border-gray-300 dark:border-gray-600 rounded hover:bg-gray-100 dark:hover:bg-gray-700"
              >
                <DocumentArrowUpIcon className="w-5 h-5" />
                Load Existing Base Job Settings
                <ChevronDownIcon className="w-4 h-4" />
              </button>
              {showLoadMenu && (
                <div className="absolute top-full left-0 mt-1 w-48 bg-white dark:bg-gray-800 border border-gray-200 dark:border-gray-700 rounded-lg shadow-lg z-10">
                  <button
                    onClick={handleLoadTemplateFromCSV}
                    className="w-full px-4 py-2 text-left hover:bg-gray-100 dark:hover:bg-gray-700 rounded-t-lg"
                  >
                    CSV File
                  </button>
                  <button
                    onClick={handleLoadTemplateFromJSON}
                    className="w-full px-4 py-2 text-left hover:bg-gray-100 dark:hover:bg-gray-700"
                  >
                    JSON File
                  </button>
                  <button
                    onClick={handleLoadTemplateFromSGE}
                    className="w-full px-4 py-2 text-left hover:bg-gray-100 dark:hover:bg-gray-700 rounded-b-lg"
                  >
                    SGE Script
                  </button>
                </div>
              )}
            </div>
          </div>
          {loadSaveError && (
            <div className="mt-4 p-3 bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800 rounded text-red-700 dark:text-red-400 text-sm max-w-md">
              {loadSaveError}
            </div>
          )}
        </div>
      )
    }

    // Template ready - directory scan configuration
    if (workflowState === 'templateReady') {
      return (
        <div className="p-6">
          <div className="flex items-center justify-between mb-4">
            <h3 className="text-lg font-semibold">Scan to Create Jobs</h3>
            {/* v4.7.2: Save As dropdown for template */}
            <div className="relative">
              <button
                onClick={() => setShowSaveMenu(!showSaveMenu)}
                className="flex items-center gap-2 px-3 py-1.5 text-sm border border-gray-300 dark:border-gray-600 rounded hover:bg-gray-100 dark:hover:bg-gray-700"
              >
                <DocumentArrowDownIcon className="w-4 h-4" />
                Save As...
                <ChevronDownIcon className="w-3 h-3" />
              </button>
              {showSaveMenu && (
                <div className="absolute top-full right-0 mt-1 w-40 bg-white dark:bg-gray-800 border border-gray-200 dark:border-gray-700 rounded-lg shadow-lg z-10">
                  <button
                    onClick={handleSaveTemplateToCSV}
                    className="w-full px-4 py-2 text-left text-sm hover:bg-gray-100 dark:hover:bg-gray-700 rounded-t-lg"
                  >
                    CSV File
                  </button>
                  <button
                    onClick={handleSaveTemplateToJSON}
                    className="w-full px-4 py-2 text-left text-sm hover:bg-gray-100 dark:hover:bg-gray-700"
                  >
                    JSON File
                  </button>
                  <button
                    onClick={handleSaveTemplateToSGE}
                    className="w-full px-4 py-2 text-left text-sm hover:bg-gray-100 dark:hover:bg-gray-700 rounded-b-lg"
                  >
                    SGE Script
                  </button>
                </div>
              )}
            </div>
          </div>
          {loadSaveError && (
            <div className="mb-4 p-3 bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800 rounded text-red-700 dark:text-red-400 text-sm">
              {loadSaveError}
            </div>
          )}

          {/* v4.0.8: Scan Mode Toggle */}
          <div className="mb-4">
            <label className="block text-sm font-medium mb-2">Scan Mode</label>
            <div className="flex gap-4">
              <label className="flex items-center gap-2 cursor-pointer">
                <input
                  type="radio"
                  name="scanMode"
                  checked={scanOptions.scanMode === 'folders'}
                  onChange={() => setScanOptions({ scanMode: 'folders' })}
                  className="w-4 h-4 text-blue-500"
                />
                <span className="text-sm">Folders (each folder = 1 job)</span>
              </label>
              <label className="flex items-center gap-2 cursor-pointer">
                <input
                  type="radio"
                  name="scanMode"
                  checked={scanOptions.scanMode === 'files'}
                  onChange={() => setScanOptions({ scanMode: 'files' })}
                  className="w-4 h-4 text-blue-500"
                />
                <span className="text-sm">Files (each file = 1 job)</span>
              </label>
            </div>
          </div>

          <div className="grid grid-cols-2 gap-6 mb-6">
            <div>
              <label className="block text-sm font-medium mb-1">Root Directory</label>
              <div className="flex gap-2">
                <input
                  type="text"
                  value={scanOptions.rootDir}
                  onChange={(e) => setScanOptions({ rootDir: e.target.value })}
                  placeholder="/path/to/scan"
                  className="flex-1 px-3 py-2 text-sm border border-gray-300 dark:border-gray-600 rounded bg-white dark:bg-gray-800 focus:outline-none focus:ring-2 focus:ring-blue-500"
                />
                <button
                  onClick={handleSelectDirectory}
                  className="px-3 py-2 border border-gray-300 dark:border-gray-600 rounded hover:bg-gray-100 dark:hover:bg-gray-700"
                >
                  <FolderOpenIcon className="w-5 h-5" />
                </button>
              </div>
            </div>

            {/* Folder mode: Pattern field */}
            {scanOptions.scanMode === 'folders' && (
              <div>
                <label className="block text-sm font-medium mb-1">Folder Pattern</label>
                <input
                  type="text"
                  value={scanOptions.pattern}
                  onChange={(e) => setScanOptions({ pattern: e.target.value })}
                  placeholder="Run_*"
                  className="w-full px-3 py-2 text-sm border border-gray-300 dark:border-gray-600 rounded bg-white dark:bg-gray-800 focus:outline-none focus:ring-2 focus:ring-blue-500"
                />
              </div>
            )}

            {/* File mode: Primary Pattern field */}
            {scanOptions.scanMode === 'files' && (
              <div>
                <label className="block text-sm font-medium mb-1">Primary File Pattern</label>
                <input
                  type="text"
                  value={scanOptions.primaryPattern}
                  onChange={(e) => setScanOptions({ primaryPattern: e.target.value })}
                  placeholder="*.inp"
                  className="w-full px-3 py-2 text-sm border border-gray-300 dark:border-gray-600 rounded bg-white dark:bg-gray-800 focus:outline-none focus:ring-2 focus:ring-blue-500"
                />
                <p className="mt-1 text-xs text-gray-500">Each matching file creates one job</p>
              </div>
            )}

            {/* Folder mode only: Validation Pattern, Scan Prefix, and Tar Subpath */}
            {scanOptions.scanMode === 'folders' && (
              <>
                <div>
                  <label className="block text-sm font-medium mb-1">
                    Validation Pattern (optional)
                  </label>
                  <input
                    type="text"
                    value={scanOptions.validationPattern}
                    onChange={(e) => {
                      setScanOptions({ validationPattern: e.target.value })
                      updateConfig({ validationPattern: e.target.value })
                    }}
                    onBlur={() => saveConfig()}
                    placeholder="*.fnc"
                    className="w-full px-3 py-2 text-sm border border-gray-300 dark:border-gray-600 rounded bg-white dark:bg-gray-800 focus:outline-none focus:ring-2 focus:ring-blue-500"
                  />
                </div>
                {/* v4.6.0: Clarified label from "Run Subpath" to "Scan Prefix" */}
                <div>
                  <label className="block text-sm font-medium mb-1">
                    Scan Prefix (optional)
                  </label>
                  <input
                    type="text"
                    value={scanOptions.runSubpath}
                    onChange={(e) => {
                      setScanOptions({ runSubpath: e.target.value })
                      updateConfig({ runSubpath: e.target.value })
                    }}
                    onBlur={() => saveConfig()}
                    placeholder="Simcodes/Powerflow"
                    className="w-full px-3 py-2 text-sm border border-gray-300 dark:border-gray-600 rounded bg-white dark:bg-gray-800 focus:outline-none focus:ring-2 focus:ring-blue-500"
                  />
                  <p className="mt-1 text-xs text-gray-500">Navigate into a subpath before scanning for Run_* directories</p>
                </div>
                {/* v4.6.0: New TarSubpath field */}
                <div>
                  <label className="block text-sm font-medium mb-1">
                    Tar Subpath (optional)
                  </label>
                  <input
                    type="text"
                    value={scanOptions.tarSubpath || ''}
                    onChange={(e) => setScanOptions({ tarSubpath: e.target.value })}
                    placeholder="output/results"
                    className="w-full px-3 py-2 text-sm border border-gray-300 dark:border-gray-600 rounded bg-white dark:bg-gray-800 focus:outline-none focus:ring-2 focus:ring-blue-500"
                  />
                  <p className="mt-1 text-xs text-gray-500">Only tar this subdirectory within each matched Run_*</p>
                </div>
              </>
            )}
          </div>

          {/* v4.0.8: Secondary Patterns (File mode only) */}
          {scanOptions.scanMode === 'files' && (
            <div className="mb-6">
              <label className="block text-sm font-medium mb-2">
                Secondary File Patterns (attached to each job)
              </label>
              <div className="space-y-2">
                {scanOptions.secondaryPatterns.map((sp, index) => (
                  <div key={index} className="flex items-center gap-2">
                    <input
                      type="text"
                      value={sp.pattern}
                      onChange={(e) => {
                        const updated = [...scanOptions.secondaryPatterns]
                        updated[index] = { ...updated[index], pattern: e.target.value }
                        setScanOptions({ secondaryPatterns: updated })
                      }}
                      placeholder="*.mesh or ../meshes/*.cfg"
                      className="flex-1 px-3 py-2 text-sm border border-gray-300 dark:border-gray-600 rounded bg-white dark:bg-gray-800 focus:outline-none focus:ring-2 focus:ring-blue-500"
                    />
                    <select
                      value={sp.required ? 'required' : 'optional'}
                      onChange={(e) => {
                        const updated = [...scanOptions.secondaryPatterns]
                        updated[index] = { ...updated[index], required: e.target.value === 'required' }
                        setScanOptions({ secondaryPatterns: updated })
                      }}
                      className="px-3 py-2 text-sm border border-gray-300 dark:border-gray-600 rounded bg-white dark:bg-gray-800"
                    >
                      <option value="required">Required</option>
                      <option value="optional">Optional</option>
                    </select>
                    <button
                      onClick={() => {
                        const updated = scanOptions.secondaryPatterns.filter((_, i) => i !== index)
                        setScanOptions({ secondaryPatterns: updated })
                      }}
                      className="px-2 py-2 text-red-500 hover:bg-red-50 dark:hover:bg-red-900/20 rounded"
                    >
                      <XCircleIcon className="w-5 h-5" />
                    </button>
                  </div>
                ))}
                <button
                  onClick={() => {
                    setScanOptions({
                      secondaryPatterns: [...scanOptions.secondaryPatterns, { pattern: '', required: true }],
                    })
                  }}
                  className="text-sm text-blue-500 hover:text-blue-600"
                >
                  + Add Secondary Pattern
                </button>
              </div>
              <p className="mt-2 text-xs text-gray-500">
                Use * to match primary file&apos;s base name (e.g., *.mesh matches case1.mesh for case1.inp)
              </p>
            </div>
          )}

          <div className="flex items-center gap-4 mb-6">
            <label className="flex items-center gap-2 cursor-pointer">
              <input
                type="checkbox"
                checked={scanOptions.recursive}
                onChange={(e) => setScanOptions({ recursive: e.target.checked })}
                className="w-4 h-4 text-blue-500 border-gray-300 rounded focus:ring-blue-500"
              />
              <span className="text-sm">Recursive scan</span>
            </label>
            {scanOptions.scanMode === 'folders' && (
              <label className="flex items-center gap-2 cursor-pointer">
                <input
                  type="checkbox"
                  checked={scanOptions.includeHidden}
                  onChange={(e) => setScanOptions({ includeHidden: e.target.checked })}
                  className="w-4 h-4 text-blue-500 border-gray-300 rounded focus:ring-blue-500"
                />
                <span className="text-sm">Include hidden directories</span>
              </label>
            )}
          </div>

          {/* v4.6.1: Command Pattern Iteration - only in folder mode */}
          {scanOptions.scanMode === 'folders' && (
            <div className="mb-4">
              <label className="flex items-center gap-2 text-sm">
                <input
                  type="checkbox"
                  checked={scanOptions.iteratePatterns}
                  onChange={(e) =>
                    setScanOptions({ iteratePatterns: e.target.checked })
                  }
                  className="w-4 h-4 text-blue-500 border-gray-300 rounded focus:ring-blue-500"
                />
                <span className="font-medium text-gray-700">
                  Vary command across runs
                </span>
              </label>
              <p className="text-xs text-gray-400 ml-6 mt-1">
                When enabled, numeric patterns in the command (e.g., Run_1, data_001.csv)
                are automatically updated to match each directory&apos;s number.
              </p>
              {/* Pattern preview hint */}
              {scanOptions.iteratePatterns && template.command && template.command !== '# Enter your command here' && (
                <div className="mt-2 ml-6 p-3 bg-blue-50 rounded-md">
                  <h5 className="text-xs font-medium text-blue-700 mb-1">
                    Command Pattern Preview
                  </h5>
                  <p className="text-xs text-blue-600">
                    Numeric patterns in the command will be iterated to match directory numbers.
                  </p>
                  <code className="text-xs block mt-1 text-blue-800 bg-blue-100 p-1 rounded">
                    {template.command.substring(0, 80)}{template.command.length > 80 ? '...' : ''}
                  </code>
                </div>
              )}
            </div>
          )}

          {/* Extra Input Files Section */}
          <div className="border-t pt-4 mt-4 mb-6">
            <h4 className="text-sm font-medium text-gray-700 mb-2">
              Extra Input Files (shared across all jobs)
            </h4>
            <div className="space-y-2">
              <div className="flex gap-2">
                <input
                  type="text"
                  className="flex-1 px-3 py-2 border border-gray-300 dark:border-gray-600 rounded-md text-sm bg-white dark:bg-gray-800 focus:outline-none focus:ring-2 focus:ring-blue-500"
                  placeholder="Comma-separated local paths or id:fileId references"
                  value={purRunOptions.extraInputFiles}
                  onChange={(e) => setPURRunOptions({ extraInputFiles: e.target.value })}
                />
                <button
                  type="button"
                  className="px-3 py-2 border border-gray-300 dark:border-gray-600 rounded-md text-sm hover:bg-gray-50 dark:hover:bg-gray-700"
                  onClick={async () => {
                    try {
                      const path = await App.SelectFile('Select extra input file')
                      if (path) {
                        const current = purRunOptions.extraInputFiles
                        setPURRunOptions({
                          extraInputFiles: current ? `${current},${path}` : path,
                        })
                      }
                    } catch {
                      // User cancelled
                    }
                  }}
                >
                  Browse...
                </button>
              </div>
              <label className="flex items-center gap-2 text-sm text-gray-600">
                <input
                  type="checkbox"
                  checked={purRunOptions.decompressExtras}
                  onChange={(e) => setPURRunOptions({ decompressExtras: e.target.checked })}
                  className="w-4 h-4 text-blue-500 border-gray-300 rounded focus:ring-blue-500"
                />
                Decompress extra files on cluster
              </label>
              <p className="text-xs text-gray-400">
                These files are uploaded once and attached to every job in the batch.
                Use &quot;id:fileId&quot; for already-uploaded files.
              </p>
            </div>
          </div>

          {/* v4.7.1: Pipeline Settings — workers + tar options */}
          <PipelineSettings config={config} updateConfig={updateConfig} saveConfig={saveConfig} />

          {scanError && (
            <div className="mb-4 mt-4 p-3 bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800 rounded text-red-700 dark:text-red-400 text-sm">
              {scanError}
            </div>
          )}

          <button
            onClick={handleScan}
            disabled={!scanOptions.rootDir || isScanning}
            className={clsx(
              'flex items-center gap-2 px-4 py-2 rounded',
              scanOptions.rootDir && !isScanning
                ? 'bg-blue-500 text-white hover:bg-blue-600'
                : 'bg-gray-300 dark:bg-gray-600 text-gray-500 cursor-not-allowed'
            )}
          >
            {isScanning ? (
              <>
                <ArrowPathIcon className="w-5 h-5 animate-spin" />
                Scanning...
              </>
            ) : (
              <>
                <PlayIcon className="w-5 h-5" />
                Scan to Create Jobs
              </>
            )}
          </button>
        </div>
      )
    }

    // Directories scanned - show jobs table and validation
    if (workflowState === 'directoriesScanned') {
      return (
        <div className="p-6">
          <div className="flex items-center justify-between mb-4">
            <h3 className="text-lg font-semibold">
              Found {scannedJobs.length} job{scannedJobs.length !== 1 ? 's' : ''}
            </h3>
            <button
              onClick={handleValidate}
              disabled={isValidating || scannedJobs.length === 0}
              className={clsx(
                'flex items-center gap-2 px-4 py-2 rounded',
                !isValidating && scannedJobs.length > 0
                  ? 'bg-blue-500 text-white hover:bg-blue-600'
                  : 'bg-gray-300 dark:bg-gray-600 text-gray-500 cursor-not-allowed'
              )}
            >
              {isValidating ? (
                <>
                  <ArrowPathIcon className="w-5 h-5 animate-spin" />
                  Validating...
                </>
              ) : (
                <>
                  <CheckCircleIcon className="w-5 h-5" />
                  Validate Jobs
                </>
              )}
            </button>
          </div>

          {validationErrors.length > 0 && (
            <div className="mb-4 p-3 bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800 rounded">
              <div className="flex items-start gap-2 text-red-700 dark:text-red-400">
                <ExclamationTriangleIcon className="w-5 h-5 flex-shrink-0 mt-0.5" />
                <div>
                  <div className="font-medium mb-1">Validation Errors:</div>
                  {validationErrors.map((err, i) => (
                    <div key={i} className="text-sm">
                      {err}
                    </div>
                  ))}
                </div>
              </div>
            </div>
          )}

          <JobsTable jobs={jobRows} />
        </div>
      )
    }

    // Jobs validated - ready to run
    if (workflowState === 'jobsValidated') {
      return (
        <div className="p-6">
          <div className="flex items-center justify-between mb-4">
            <h3 className="text-lg font-semibold">
              {scannedJobs.length} job{scannedJobs.length !== 1 ? 's' : ''} validated
            </h3>
            <div className="flex items-center gap-2">
              <button
                onClick={handleExportCSV}
                className="flex items-center gap-2 px-4 py-2 border border-gray-300 dark:border-gray-600 rounded hover:bg-gray-100 dark:hover:bg-gray-700"
              >
                <DocumentArrowDownIcon className="w-5 h-5" />
                Export CSV
              </button>
              {/* v4.7.3: Queue button when a run is active */}
              {activeRun?.status === 'active' ? (
                <button
                  onClick={handleQueueRun}
                  disabled={!!queuedJob}
                  className={clsx(
                    'flex items-center gap-2 px-4 py-2 rounded',
                    queuedJob
                      ? 'bg-gray-400 text-white cursor-not-allowed'
                      : 'bg-yellow-500 text-white hover:bg-yellow-600'
                  )}
                >
                  <PlayIcon className="w-5 h-5" />
                  {queuedJob ? 'Run Queued' : 'Queue Run'}
                </button>
              ) : (
                <button
                  onClick={handleRun}
                  className="flex items-center gap-2 px-4 py-2 bg-green-500 text-white rounded hover:bg-green-600"
                >
                  <PlayIcon className="w-5 h-5" />
                  Start Pipeline
                </button>
              )}
            </div>
          </div>

          {/* v4.7.3: Queue status banner */}
          {queueStatus && (
            <div className={clsx(
              'mb-3 p-2 text-sm rounded',
              queueStatus === 'queued' && 'bg-yellow-50 text-yellow-700 border border-yellow-200',
              queueStatus === 'starting' && 'bg-blue-50 text-blue-700 border border-blue-200',
              queueStatus === 'started' && 'bg-green-50 text-green-700 border border-green-200',
              queueStatus.startsWith('failed') && 'bg-red-50 text-red-700 border border-red-200',
            )}>
              {queueStatus === 'queued' && 'Run queued — will start automatically when current run completes'}
              {queueStatus === 'starting' && 'Starting queued run...'}
              {queueStatus === 'started' && 'Queued run started successfully'}
              {queueStatus.startsWith('failed') && `Queue failed: ${queueStatus.replace('failed: ', '')}`}
            </div>
          )}

          <JobsTable jobs={jobRows} />

          {/* v4.7.1: Pipeline Settings — also visible in CSV-loaded workflow */}
          <PipelineSettings config={config} updateConfig={updateConfig} saveConfig={saveConfig} />
        </div>
      )
    }

    // v4.7.3: Executing — reads from runStore.activeRun for live data
    if (workflowState === 'executing') {
      // If runStore has an active PUR run, use its data; otherwise fall back
      const runData = activeRun && activeRun.runType === 'pur' ? activeRun : null
      const displayRows = runData ? runData.jobRows : jobRows
      const totalJobs = runData ? runData.totalJobs : (jobRows.length || runStatus.totalJobs || 1)
      const completedJobs = runData ? runData.completedJobs : displayRows.filter((j) =>
        j.submitStatus === 'completed' || j.submitStatus === 'success' || j.submitStatus === 'skipped'
      ).length
      const failedJobs = runData ? runData.failedJobs : displayRows.filter((j) =>
        j.submitStatus === 'failed' || j.tarStatus === 'failed' || j.uploadStatus === 'failed'
      ).length
      const doneJobs = completedJobs + failedJobs
      const elapsed = runData ? Math.round(runData.durationMs / 1000) : 0
      const elapsedStr = formatDuration(elapsed)
      const stageStats = runData ? runData.pipelineStageStats : null
      const logs = runData ? runData.pipelineLogs : []

      return (
        <div className="p-6">
          <div className="flex items-center justify-between mb-4">
            <div>
              <h3 className="text-lg font-semibold">
                {!activeRun || activeRun.status === 'active' ? 'Pipeline Running'
                  : activeRun.status === 'completed' ? 'Pipeline Complete'
                  : activeRun.status === 'failed' ? 'Pipeline Failed'
                  : activeRun.status === 'cancelled' ? 'Pipeline Cancelled'
                  : 'Pipeline Status'}
              </h3>
              <p className="text-sm text-gray-500">
                {doneJobs} of {totalJobs} complete
                {failedJobs > 0 && ` (${failedJobs} failed)`}
                {' '}&middot; Elapsed: {elapsedStr}
              </p>
            </div>
            <div className="flex items-center gap-2">
              <button
                onClick={() => {
                  reset()
                  setPurViewMode('configure')
                }}
                className="flex items-center gap-2 px-3 py-2 border border-gray-300 dark:border-gray-600 rounded hover:bg-gray-100 dark:hover:bg-gray-700 text-sm"
              >
                Prepare New Run
              </button>
              {activeRun?.runType === 'pur' && activeRun?.status === 'active' && (
                <button
                  onClick={handleCancel}
                  className="flex items-center gap-2 px-4 py-2 bg-red-500 text-white rounded hover:bg-red-600"
                >
                  <StopIcon className="w-5 h-5" />
                  Cancel Pipeline
                </button>
              )}
              {activeRun && activeRun.runType === 'pur' &&
                (activeRun.status === 'completed' || activeRun.status === 'failed' || activeRun.status === 'cancelled') && (
                <button
                  onClick={() => setPurViewMode('auto')}
                  className="flex items-center gap-2 px-3 py-2 bg-blue-500 text-white rounded hover:bg-blue-600 text-sm"
                >
                  View Results
                </button>
              )}
            </div>
          </div>

          {/* Progress bar */}
          <div className="mb-4">
            <div className="flex justify-between text-sm mb-1">
              <span>Progress</span>
              <span>
                {Math.round((doneJobs / totalJobs) * 100) || 0}%
              </span>
            </div>
            <div className="h-2 bg-gray-200 dark:bg-gray-700 rounded-full overflow-hidden">
              <div
                className="h-full transition-all duration-500"
                style={{
                  width: `${(doneJobs / totalJobs) * 100 || 0}%`,
                  background: failedJobs > 0
                    ? `linear-gradient(to right, #3b82f6 ${(completedJobs / totalJobs) * 100}%, #ef4444 ${(completedJobs / totalJobs) * 100}%)`
                    : '#3b82f6',
                }}
              />
            </div>
          </div>

          {stageStats && <PipelineStageSummary stats={stageStats} total={totalJobs} />}

          <StatsBar jobs={displayRows} />
          <JobsTable jobs={displayRows} />

          <PipelineLogPanel logs={logs} />
        </div>
      )
    }

    // v4.7.3: Completed — reads from runStore.activeRun for final snapshot
    if (workflowState === 'completed') {
      const runData = activeRun && activeRun.runType === 'pur' ? activeRun : null
      const displayRows = runData ? runData.jobRows : jobRows
      const completedCount = runData ? runData.completedJobs : displayRows.filter((j) =>
        j.submitStatus === 'completed' || j.submitStatus === 'success' || j.submitStatus === 'skipped'
      ).length
      const failedCount = runData ? runData.failedJobs : displayRows.filter((j) =>
        j.submitStatus === 'failed' || j.tarStatus === 'failed' || j.uploadStatus === 'failed' || j.error
      ).length
      const allSuccess = failedCount === 0
      const wasCancelled = runData?.status === 'cancelled' || runStatus.state === 'cancelled'
      const logs = runData ? runData.pipelineLogs : []

      return (
        <div className="p-6">
          <div className="flex flex-col items-center justify-center mb-6">
            {wasCancelled ? (
              <StopIcon className="w-16 h-16 text-gray-500 mb-4" />
            ) : allSuccess ? (
              <CheckCircleIcon className="w-16 h-16 text-green-500 mb-4" />
            ) : (
              <ExclamationTriangleIcon className="w-16 h-16 text-yellow-500 mb-4" />
            )}
            <h3 className="text-lg font-semibold mb-2">
              {wasCancelled
                ? 'Pipeline Cancelled'
                : allSuccess
                  ? 'Pipeline Complete!'
                  : 'Pipeline Complete with Errors'}
            </h3>
            <p className="text-sm text-gray-500">
              {completedCount} succeeded, {failedCount} failed
              {wasCancelled && `, ${displayRows.length - completedCount - failedCount} cancelled`}
            </p>
          </div>

          <ErrorSummary jobs={displayRows} />
          <JobsTable jobs={displayRows} />

          {logs.length > 0 && (
            <PipelineLogPanel logs={logs} maxHeight={300} />
          )}

          <div className="flex justify-center mt-6">
            <button
              onClick={handleStartOver}
              className="flex items-center gap-2 px-4 py-2 bg-blue-500 text-white rounded hover:bg-blue-600"
            >
              <ArrowPathIcon className="w-5 h-5" />
              Start New Pipeline
            </button>
          </div>
        </div>
      )
    }

    return null
  }

  // v4.7.3: ChoiceScreen — shown when returning to PUR during active run
  const renderChoiceScreen = () => {
    if (!activeRun) return null
    const doneJobs = activeRun.completedJobs + activeRun.failedJobs
    return (
      <div className="flex flex-col items-center justify-center h-full p-8">
        <ArrowPathIcon className="w-16 h-16 text-blue-500 animate-spin mb-4" />
        <h3 className="text-lg font-semibold mb-2">PUR Pipeline In Progress</h3>
        <p className="text-sm text-gray-500 mb-6">
          {doneJobs} of {activeRun.totalJobs} jobs complete
          {activeRun.failedJobs > 0 && ` (${activeRun.failedJobs} failed)`}
        </p>
        <div className="flex gap-4">
          <button
            onClick={() => setPurViewMode('monitor')}
            className="flex items-center gap-2 px-6 py-3 bg-blue-500 text-white rounded-lg hover:bg-blue-600"
          >
            <EyeIcon className="w-5 h-5" />
            Monitor Active Run
          </button>
          <button
            onClick={() => {
              reset()
              setPurViewMode('configure')
            }}
            className="flex items-center gap-2 px-6 py-3 border border-gray-300 dark:border-gray-600 rounded-lg hover:bg-gray-100 dark:hover:bg-gray-700"
          >
            <PlayIcon className="w-5 h-5" />
            Prepare New Run
          </button>
        </div>
      </div>
    )
  }

  // v4.7.3: RunMonitorView — read-only dashboard from runStore.activeRun
  const renderMonitorView = () => {
    if (!activeRun) return null
    const doneJobs = activeRun.completedJobs + activeRun.failedJobs
    const elapsed = Math.round(activeRun.durationMs / 1000)
    const elapsedStr = formatDuration(elapsed)
    const totalJobs = activeRun.totalJobs || 1

    return (
      <div className="p-6">
        <div className="flex items-center justify-between mb-4">
          <div>
            <h3 className="text-lg font-semibold">
              {activeRun.status === 'active' ? 'Pipeline Running'
                : activeRun.status === 'completed' ? 'Pipeline Complete'
                : activeRun.status === 'failed' ? 'Pipeline Failed'
                : activeRun.status === 'cancelled' ? 'Pipeline Cancelled'
                : 'Pipeline Status'}
            </h3>
            <p className="text-sm text-gray-500">
              {doneJobs} of {totalJobs} complete
              {activeRun.failedJobs > 0 && ` (${activeRun.failedJobs} failed)`}
              {' '}&middot; Elapsed: {elapsedStr}
            </p>
          </div>
          <div className="flex items-center gap-2">
            <button
              onClick={() => {
                reset()
                setPurViewMode('configure')
              }}
              className="flex items-center gap-2 px-3 py-2 border border-gray-300 dark:border-gray-600 rounded hover:bg-gray-100 dark:hover:bg-gray-700 text-sm"
            >
              Prepare New Run
            </button>
            {activeRun.status === 'active' && (
              <button
                onClick={handleCancel}
                className="flex items-center gap-2 px-4 py-2 bg-red-500 text-white rounded hover:bg-red-600"
              >
                <StopIcon className="w-5 h-5" />
                Cancel Pipeline
              </button>
            )}
            {(activeRun.status === 'completed' || activeRun.status === 'failed' || activeRun.status === 'cancelled') && (
              <button
                onClick={() => setPurViewMode('auto')}
                className="flex items-center gap-2 px-3 py-2 bg-blue-500 text-white rounded hover:bg-blue-600 text-sm"
              >
                View Results
              </button>
            )}
          </div>
        </div>

        {/* Progress bar */}
        <div className="mb-4">
          <div className="flex justify-between text-sm mb-1">
            <span>Progress</span>
            <span>{Math.round((doneJobs / totalJobs) * 100) || 0}%</span>
          </div>
          <div className="h-2 bg-gray-200 dark:bg-gray-700 rounded-full overflow-hidden">
            <div
              className="h-full transition-all duration-500"
              style={{
                width: `${(doneJobs / totalJobs) * 100 || 0}%`,
                background: activeRun.failedJobs > 0
                  ? `linear-gradient(to right, #3b82f6 ${(activeRun.completedJobs / totalJobs) * 100}%, #ef4444 ${(activeRun.completedJobs / totalJobs) * 100}%)`
                  : '#3b82f6',
              }}
            />
          </div>
        </div>

        <PipelineStageSummary stats={activeRun.pipelineStageStats} total={totalJobs} />
        <StatsBar jobs={activeRun.jobRows} />
        <JobsTable jobs={activeRun.jobRows} />
        <PipelineLogPanel logs={activeRun.pipelineLogs} />
      </div>
    )
  }

  // v4.7.3: CompletedResultsView — shown for completed/failed/cancelled runs
  const renderResultsView = () => {
    if (!activeRun) return null
    const allSuccess = activeRun.failedJobs === 0
    const wasCancelled = activeRun.status === 'cancelled'

    return (
      <div className="p-6">
        <div className="flex flex-col items-center justify-center mb-6">
          {wasCancelled ? (
            <StopIcon className="w-16 h-16 text-gray-500 mb-4" />
          ) : allSuccess ? (
            <CheckCircleIcon className="w-16 h-16 text-green-500 mb-4" />
          ) : (
            <ExclamationTriangleIcon className="w-16 h-16 text-yellow-500 mb-4" />
          )}
          <h3 className="text-lg font-semibold mb-2">
            {wasCancelled
              ? 'Pipeline Cancelled'
              : allSuccess
                ? 'Pipeline Complete!'
                : 'Pipeline Complete with Errors'}
          </h3>
          <p className="text-sm text-gray-500">
            {activeRun.completedJobs} succeeded, {activeRun.failedJobs} failed
          </p>
        </div>

        <ErrorSummary jobs={activeRun.jobRows} />
        <JobsTable jobs={activeRun.jobRows} />

        {activeRun.pipelineLogs.length > 0 && (
          <PipelineLogPanel logs={activeRun.pipelineLogs} maxHeight={300} />
        )}

        <div className="flex justify-center mt-6">
          <button
            onClick={handleStartOver}
            className="flex items-center gap-2 px-4 py-2 bg-blue-500 text-white rounded hover:bg-blue-600"
          >
            <ArrowPathIcon className="w-5 h-5" />
            Start New Pipeline
          </button>
        </div>
      </div>
    )
  }

  // v4.7.3: RunMonitoringBanner — thin bar during configure mode with active run
  const renderMonitoringBanner = () => {
    if (!activeRun || activeRun.status !== 'active' || activeRun.runType !== 'pur') return null
    if (monitorBannerCollapsed) {
      return (
        <button
          onClick={() => setMonitorBannerCollapsed(false)}
          className="px-4 py-1 bg-blue-50 dark:bg-blue-900/20 border-b border-blue-200 text-xs text-blue-600 hover:bg-blue-100 flex items-center gap-1"
        >
          <EyeIcon className="w-3 h-3" />
          PUR running ({activeRun.completedJobs + activeRun.failedJobs}/{activeRun.totalJobs})
          <span className="text-blue-400">— click to expand</span>
        </button>
      )
    }
    const doneJobs = activeRun.completedJobs + activeRun.failedJobs
    return (
      <div className="px-4 py-2 bg-blue-50 dark:bg-blue-900/20 border-b border-blue-200 flex items-center justify-between text-sm">
        <div className="flex items-center gap-3">
          <ArrowPathIcon className="w-4 h-4 text-blue-500 animate-spin" />
          <span className="text-blue-700 font-medium">
            PUR running: {doneJobs}/{activeRun.totalJobs} complete
            {activeRun.failedJobs > 0 && ` (${activeRun.failedJobs} failed)`}
          </span>
          <div className="w-24 h-1.5 bg-blue-200 rounded-full overflow-hidden">
            <div
              className="h-full bg-blue-500 transition-all"
              style={{ width: `${(doneJobs / (activeRun.totalJobs || 1)) * 100}%` }}
            />
          </div>
        </div>
        <div className="flex items-center gap-2">
          <button
            onClick={() => setPurViewMode('monitor')}
            className="text-xs text-blue-600 hover:text-blue-800 font-medium"
          >
            Monitor
          </button>
          <button
            onClick={() => setMonitorBannerCollapsed(true)}
            className="text-xs text-blue-400 hover:text-blue-600"
          >
            Hide
          </button>
        </div>
      </div>
    )
  }

  // v4.7.3: View mode rendering
  if (effectiveView === 'choice') {
    return (
      <div className="h-full flex flex-col">
        {renderChoiceScreen()}
      </div>
    )
  }

  if (effectiveView === 'monitor') {
    return (
      <div className="h-full flex flex-col">
        <div className="px-6 py-4 border-b border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800">
          <p className="text-xs text-gray-500 dark:text-gray-400 uppercase tracking-wide">
            Run Monitor
          </p>
        </div>
        <div className="flex-1 overflow-auto">{renderMonitorView()}</div>
      </div>
    )
  }

  if (effectiveView === 'results') {
    return (
      <div className="h-full flex flex-col">
        <div className="px-6 py-4 border-b border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800">
          <p className="text-xs text-gray-500 dark:text-gray-400 uppercase tracking-wide">
            Run Results
          </p>
        </div>
        <div className="flex-1 overflow-auto">{renderResultsView()}</div>
      </div>
    )
  }

  // effectiveView === 'configure' — normal workflow with optional monitoring banner
  return (
    <div className="h-full flex flex-col">
      {/* v4.7.3: Monitoring banner when configuring during active run */}
      {renderMonitoringBanner()}

      {/* Progress bar */}
      <div className="px-6 py-4 border-b border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800">
        <div className="flex items-center justify-between mb-2">
          <p className="text-xs text-gray-500 dark:text-gray-400 uppercase tracking-wide">
            Parallel Upload and Run
          </p>
        </div>
        <WorkflowProgressBar currentState={workflowState} />

        {/* Back button */}
        {canGoBack() && workflowState !== 'executing' && (
          <button
            onClick={goBack}
            className="flex items-center gap-1 text-sm text-gray-600 dark:text-gray-400 hover:text-gray-900 dark:hover:text-gray-200"
          >
            <ArrowLeftIcon className="w-4 h-4" />
            Back
          </button>
        )}
      </div>

      {/* Content */}
      <div className="flex-1 overflow-auto">{renderContent()}</div>

      {/* Template builder dialog */}
      <TemplateBuilder
        isOpen={showTemplateBuilder}
        initialTemplate={template}
        onClose={() => setShowTemplateBuilder(false)}
        onSave={handleTemplateSave}
      />
    </div>
  )
}
