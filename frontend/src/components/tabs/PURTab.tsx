import { useEffect, useCallback, useState } from 'react'
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
} from '@heroicons/react/24/outline'
import clsx from 'clsx'
import { useJobStore, WorkflowState, JobRow } from '../../stores'
import { TemplateBuilder } from '../widgets'
import * as App from '../../../wailsjs/go/wailsapp/App'
import * as Runtime from '../../../wailsjs/runtime/runtime'

// v4.0.0 G2: Improved workflow steps with user-friendly labels
const WORKFLOW_STEPS: { state: WorkflowState; label: string; shortLabel: string }[] = [
  { state: 'initial', label: 'Choose Source', shortLabel: 'Source' },
  { state: 'pathChosen', label: 'Configure', shortLabel: 'Config' },
  { state: 'templateReady', label: 'Scan Directories', shortLabel: 'Scan' },
  { state: 'directoriesScanned', label: 'Validate Jobs', shortLabel: 'Validate' },
  { state: 'jobsValidated', label: 'Ready to Run', shortLabel: 'Ready' },
  { state: 'executing', label: 'Running', shortLabel: 'Running' },
  { state: 'completed', label: 'Complete', shortLabel: 'Done' },
]

// Status badge component
function StatusBadge({ status }: { status: string }) {
  const styles: Record<string, string> = {
    pending: 'bg-gray-200 text-gray-700',
    running: 'bg-blue-200 text-blue-700',
    success: 'bg-green-200 text-green-700',
    completed: 'bg-green-200 text-green-700',
    failed: 'bg-red-200 text-red-700',
    skipped: 'bg-gray-100 text-gray-500',
  }

  return (
    <span
      className={clsx(
        'px-2 py-0.5 text-xs rounded-full font-medium',
        styles[status] || 'bg-gray-200 text-gray-700'
      )}
    >
      {status}
    </span>
  )
}

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

// Stats bar component
function StatsBar({ jobs }: { jobs: JobRow[] }) {
  const stats = jobs.reduce(
    (acc, job) => {
      acc.total++
      if (job.submitStatus === 'completed' || job.submitStatus === 'success') {
        acc.completed++
      } else if (job.submitStatus === 'failed') {
        acc.failed++
      } else if (
        job.tarStatus === 'running' ||
        job.uploadStatus === 'running' ||
        job.createStatus === 'running' ||
        job.submitStatus === 'running'
      ) {
        acc.inProgress++
      } else {
        acc.pending++
      }
      return acc
    },
    { total: 0, completed: 0, inProgress: 0, pending: 0, failed: 0 }
  )

  return (
    <div className="flex items-center gap-4 mb-4 p-3 bg-gray-50 dark:bg-gray-800 rounded-lg text-sm">
      <span className="font-medium">Jobs:</span>
      <span>Total: <span className="font-semibold">{stats.total}</span></span>
      <span className="text-green-600">Completed: <span className="font-semibold">{stats.completed}</span></span>
      <span className="text-blue-600">In Progress: <span className="font-semibold">{stats.inProgress}</span></span>
      <span className="text-gray-500">Pending: <span className="font-semibold">{stats.pending}</span></span>
      {stats.failed > 0 && (
        <span className="text-red-600">Failed: <span className="font-semibold">{stats.failed}</span></span>
      )}
    </div>
  )
}

// Jobs table component
function JobsTable({ jobs }: { jobs: JobRow[] }) {
  if (jobs.length === 0) {
    return (
      <div className="text-center text-gray-500 py-8">
        No jobs to display
      </div>
    )
  }

  return (
    <div className="overflow-auto max-h-96">
      <table className="w-full text-sm">
        <thead className="bg-gray-50 dark:bg-gray-800 sticky top-0">
          <tr>
            <th className="px-4 py-2 text-left font-medium text-gray-700 dark:text-gray-300">
              #
            </th>
            <th className="px-4 py-2 text-left font-medium text-gray-700 dark:text-gray-300">
              Directory
            </th>
            <th className="px-4 py-2 text-left font-medium text-gray-700 dark:text-gray-300">
              Job Name
            </th>
            <th className="px-4 py-2 text-center font-medium text-gray-700 dark:text-gray-300">
              Tar
            </th>
            <th className="px-4 py-2 text-center font-medium text-gray-700 dark:text-gray-300">
              Upload
            </th>
            {/* v4.0.7 L2: Removed unused "Create" column - PUR workflow is Tar→Upload→Submit */}
            <th className="px-4 py-2 text-center font-medium text-gray-700 dark:text-gray-300">
              Submit
            </th>
            <th className="px-4 py-2 text-left font-medium text-gray-700 dark:text-gray-300">
              Job ID
            </th>
          </tr>
        </thead>
        <tbody className="divide-y divide-gray-200 dark:divide-gray-700">
          {jobs.map((job) => (
            <tr
              key={job.index}
              className="hover:bg-gray-50 dark:hover:bg-gray-800/50"
            >
              <td className="px-4 py-2 text-gray-600">{job.index + 1}</td>
              <td className="px-4 py-2 font-mono text-xs truncate max-w-48" title={job.directory}>
                {job.directory}
              </td>
              <td className="px-4 py-2">{job.jobName}</td>
              <td className="px-4 py-2 text-center">
                <StatusBadge status={job.tarStatus} />
              </td>
              <td className="px-4 py-2 text-center">
                {job.uploadStatus === 'running' && job.uploadProgress > 0 ? (
                  <span className="px-2 py-0.5 text-xs rounded-full font-medium bg-blue-200 text-blue-700">
                    {job.uploadProgress.toFixed(1)}%
                  </span>
                ) : (
                  <StatusBadge status={job.uploadStatus} />
                )}
              </td>
              {/* v4.0.7 L2: Removed unused createStatus column */}
              <td className="px-4 py-2 text-center">
                <StatusBadge status={job.submitStatus} />
              </td>
              <td className="px-4 py-2 font-mono text-xs text-gray-500">
                {job.jobId || '-'}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
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
  } = useJobStore()

  // Template builder dialog state
  const [showTemplateBuilder, setShowTemplateBuilder] = useState(false)
  const [validationErrors, setValidationErrors] = useState<string[]>([])
  const [isValidating, setIsValidating] = useState(false)

  // Load workflow memory on mount
  useEffect(() => {
    loadMemory()
  }, [loadMemory])

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

  // Handle loading a template from JSON file
  const handleLoadTemplate = useCallback(async () => {
    try {
      const path = await App.SelectFile('Select Template JSON File')
      if (!path) return // User cancelled

      // Load the template from the JSON file
      const jobs = await App.LoadJobsFromJSON(path)
      if (jobs && jobs.length > 0) {
        // Use the first job as the template
        const loadedTemplate = jobs[0]
        setTemplate({
          directory: loadedTemplate.directory || '',
          jobName: loadedTemplate.jobName || '',
          analysisCode: loadedTemplate.analysisCode || '',
          analysisVersion: loadedTemplate.analysisVersion || '',
          command: loadedTemplate.command || '',
          coreType: loadedTemplate.coreType || '',
          coresPerSlot: loadedTemplate.coresPerSlot || 4,
          walltimeHours: loadedTemplate.walltimeHours || 1,
          slots: loadedTemplate.slots || 1,
          licenseSettings: loadedTemplate.licenseSettings || '',
          extraInputFileIds: loadedTemplate.extraInputFileIds || '',
          noDecompress: loadedTemplate.noDecompress || false,
          submitMode: loadedTemplate.submitMode || 'create_and_submit',
          isLowPriority: loadedTemplate.isLowPriority || false,
          onDemandLicenseSeller: loadedTemplate.onDemandLicenseSeller || '',
          tags: loadedTemplate.tags || [],
          projectId: loadedTemplate.projectId || '',
          automations: loadedTemplate.automations || [],
        })
      }
    } catch (error) {
      console.error('Failed to load template:', error)
    }
  }, [setTemplate])

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

  // Handle cancel
  const handleCancel = useCallback(async () => {
    await cancelRun()
  }, [cancelRun])

  // Handle start over
  const handleStartOver = useCallback(() => {
    reset()
    setValidationErrors([])
  }, [reset])

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
          <h3 className="text-lg font-semibold mb-4">Configure Job Template</h3>
          <p className="text-gray-600 mb-6">
            Create or load a job template to use for scanning
          </p>
          <div className="flex gap-4">
            <button
              onClick={() => setShowTemplateBuilder(true)}
              className="flex items-center gap-2 px-4 py-2 bg-blue-500 text-white rounded hover:bg-blue-600"
            >
              <FolderPlusIcon className="w-5 h-5" />
              Create New Template
            </button>
            <button
              onClick={handleLoadTemplate}
              className="flex items-center gap-2 px-4 py-2 border border-gray-300 dark:border-gray-600 rounded hover:bg-gray-100 dark:hover:bg-gray-700"
            >
              <DocumentArrowUpIcon className="w-5 h-5" />
              Load Template
            </button>
          </div>
        </div>
      )
    }

    // Template ready - directory scan configuration
    if (workflowState === 'templateReady') {
      return (
        <div className="p-6">
          <h3 className="text-lg font-semibold mb-4">Scan for Job Directories</h3>
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
            <div>
              <label className="block text-sm font-medium mb-1">Pattern</label>
              <input
                type="text"
                value={scanOptions.pattern}
                onChange={(e) => setScanOptions({ pattern: e.target.value })}
                placeholder="Run_*"
                className="w-full px-3 py-2 text-sm border border-gray-300 dark:border-gray-600 rounded bg-white dark:bg-gray-800 focus:outline-none focus:ring-2 focus:ring-blue-500"
              />
            </div>
            <div>
              <label className="block text-sm font-medium mb-1">
                Validation Pattern (optional)
              </label>
              <input
                type="text"
                value={scanOptions.validationPattern}
                onChange={(e) => setScanOptions({ validationPattern: e.target.value })}
                placeholder="*.fnc"
                className="w-full px-3 py-2 text-sm border border-gray-300 dark:border-gray-600 rounded bg-white dark:bg-gray-800 focus:outline-none focus:ring-2 focus:ring-blue-500"
              />
            </div>
            <div>
              <label className="block text-sm font-medium mb-1">
                Run Subpath (optional)
              </label>
              <input
                type="text"
                value={scanOptions.runSubpath}
                onChange={(e) => setScanOptions({ runSubpath: e.target.value })}
                placeholder="Simcodes/Powerflow"
                className="w-full px-3 py-2 text-sm border border-gray-300 dark:border-gray-600 rounded bg-white dark:bg-gray-800 focus:outline-none focus:ring-2 focus:ring-blue-500"
              />
            </div>
          </div>

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
            <label className="flex items-center gap-2 cursor-pointer">
              <input
                type="checkbox"
                checked={scanOptions.includeHidden}
                onChange={(e) => setScanOptions({ includeHidden: e.target.checked })}
                className="w-4 h-4 text-blue-500 border-gray-300 rounded focus:ring-blue-500"
              />
              <span className="text-sm">Include hidden directories</span>
            </label>
          </div>

          {scanError && (
            <div className="mb-4 p-3 bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800 rounded text-red-700 dark:text-red-400 text-sm">
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
                Scan Directories
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
              <button
                onClick={handleRun}
                className="flex items-center gap-2 px-4 py-2 bg-green-500 text-white rounded hover:bg-green-600"
              >
                <PlayIcon className="w-5 h-5" />
                Start Pipeline
              </button>
            </div>
          </div>

          <JobsTable jobs={jobRows} />
        </div>
      )
    }

    // Executing - show progress
    if (workflowState === 'executing') {
      return (
        <div className="p-6">
          <div className="flex items-center justify-between mb-4">
            <div>
              <h3 className="text-lg font-semibold">Pipeline Running</h3>
              <p className="text-sm text-gray-500">
                {runStatus.successJobs} of {runStatus.totalJobs} complete
                {runStatus.failedJobs > 0 && ` (${runStatus.failedJobs} failed)`}
              </p>
            </div>
            <button
              onClick={handleCancel}
              className="flex items-center gap-2 px-4 py-2 bg-red-500 text-white rounded hover:bg-red-600"
            >
              <StopIcon className="w-5 h-5" />
              Cancel Pipeline
            </button>
          </div>

          {/* Progress bar */}
          <div className="mb-4">
            <div className="flex justify-between text-sm mb-1">
              <span>Progress</span>
              <span>
                {Math.round((runStatus.successJobs / runStatus.totalJobs) * 100) || 0}%
              </span>
            </div>
            <div className="h-2 bg-gray-200 dark:bg-gray-700 rounded-full overflow-hidden">
              <div
                className="h-full bg-blue-500 transition-all duration-500"
                style={{
                  width: `${(runStatus.successJobs / runStatus.totalJobs) * 100 || 0}%`,
                }}
              />
            </div>
          </div>

          <StatsBar jobs={jobRows} />
          <JobsTable jobs={jobRows} />
        </div>
      )
    }

    // Completed
    if (workflowState === 'completed') {
      const allSuccess = runStatus.failedJobs === 0

      return (
        <div className="p-6">
          <div className="flex flex-col items-center justify-center mb-6">
            {allSuccess ? (
              <CheckCircleIcon className="w-16 h-16 text-green-500 mb-4" />
            ) : (
              <ExclamationTriangleIcon className="w-16 h-16 text-yellow-500 mb-4" />
            )}
            <h3 className="text-lg font-semibold mb-2">
              {allSuccess ? 'Pipeline Complete!' : 'Pipeline Complete with Errors'}
            </h3>
            <p className="text-sm text-gray-500">
              {runStatus.successJobs} succeeded, {runStatus.failedJobs} failed
            </p>
          </div>

          <JobsTable jobs={jobRows} />

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

  return (
    <div className="h-full flex flex-col">
      {/* Progress bar */}
      <div className="px-6 py-4 border-b border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800">
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
