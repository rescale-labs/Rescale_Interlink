import { useState, useCallback, useEffect, useMemo, useRef } from 'react'
import {
  FolderIcon,
  DocumentIcon,
  CloudIcon,
  PlayIcon,
  StopIcon,
  CheckCircleIcon,
  XCircleIcon,
  ArrowPathIcon,
  FolderOpenIcon,
  DocumentArrowDownIcon,
  DocumentArrowUpIcon,
  ChevronDownIcon,
  XMarkIcon,
  PlusIcon,
  FolderPlusIcon,
} from '@heroicons/react/24/outline'
import clsx from 'clsx'
import { useJobStore, JobSpec, DEFAULT_JOB_TEMPLATE } from '../../stores'
import { TemplateBuilder, RemoteFilePicker } from '../widgets'
import * as App from '../../../wailsjs/go/wailsapp/App'
import { wailsapp } from '../../../wailsjs/go/models'

// v4.0.0 G1: File info for displaying sizes
interface FileInfo {
  path: string
  name: string
  isDir: boolean
  size: number
  fileCount: number
}

// Helper to format file sizes nicely
function formatFileSize(bytes: number): string {
  if (bytes === 0) return '0 B'
  const k = 1024
  const sizes = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.floor(Math.log(bytes) / Math.log(k))
  return parseFloat((bytes / Math.pow(k, i)).toFixed(1)) + ' ' + sizes[i]
}

// Single job workflow states
type SingleJobState =
  | 'initial'
  | 'jobConfigured'
  | 'inputsReady'
  | 'executing'
  | 'completed'
  | 'failed'

// Input mode for single job
type InputMode = 'directory' | 'localFiles' | 'remoteFiles'

// Progress steps
const STEPS = [
  { key: 'configure', label: 'Configure' },
  { key: 'inputs', label: 'Select Inputs' },
  { key: 'submit', label: 'Submit' },
  { key: 'complete', label: 'Complete' },
]

export function SingleJobTab() {
  const {
    loadMemory,
    loadJobFromJSON,
    saveJobToJSON,
    loadJobFromSGE,
    saveJobToSGE,
  } = useJobStore()

  // Local state for single job workflow
  const [state, setState] = useState<SingleJobState>('initial')
  const [job, setJob] = useState<JobSpec>({ ...DEFAULT_JOB_TEMPLATE })
  const [inputMode, setInputMode] = useState<InputMode>('directory')
  const [directory, setDirectory] = useState('')
  const [localFiles, setLocalFiles] = useState<string[]>([])
  const [remoteFileIds, setRemoteFileIds] = useState<string[]>([])
  const [showTemplateBuilder, setShowTemplateBuilder] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [submittedJobId, setSubmittedJobId] = useState<string | null>(null)
  const [showLoadMenu, setShowLoadMenu] = useState(false)
  const [showSaveMenu, setShowSaveMenu] = useState(false)
  const [showRemoteFilePicker, setShowRemoteFilePicker] = useState(false)
  // v4.0.0 G1: File info cache for displaying sizes
  const [fileInfoMap, setFileInfoMap] = useState<Record<string, FileInfo>>({})

  // v4.0.7 C1: Refs for polling state to avoid stale closure bugs
  const isPollingRef = useRef(false)
  const pollTimeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  // Load memory on mount
  useEffect(() => {
    loadMemory()
  }, [loadMemory])

  // v4.0.7 C1: Cleanup polling on unmount
  useEffect(() => {
    return () => {
      isPollingRef.current = false
      if (pollTimeoutRef.current) {
        clearTimeout(pollTimeoutRef.current)
        pollTimeoutRef.current = null
      }
    }
  }, [])

  // Get current step index
  const getCurrentStep = () => {
    switch (state) {
      case 'initial':
        return 0
      case 'jobConfigured':
        return 1
      case 'inputsReady':
        return 2
      case 'executing':
        return 2
      case 'completed':
      case 'failed':
        return 3
      default:
        return 0
    }
  }

  // Handle template save
  const handleTemplateSave = useCallback((template: JobSpec) => {
    setJob(template)
    setState('jobConfigured')
    setShowTemplateBuilder(false)
  }, [])

  // Load from CSV
  const handleLoadFromCSV = useCallback(async () => {
    setShowLoadMenu(false)
    try {
      const path = await App.SelectFile('Select Jobs CSV File')
      if (!path) return

      const jobs = await App.LoadJobsFromCSV(path)
      if (jobs && jobs.length > 0) {
        // Take the first job as the template
        const loadedJob = jobs[0]
        setJob({
          directory: loadedJob.directory,
          jobName: loadedJob.jobName,
          analysisCode: loadedJob.analysisCode,
          analysisVersion: loadedJob.analysisVersion,
          command: loadedJob.command,
          coreType: loadedJob.coreType,
          coresPerSlot: loadedJob.coresPerSlot,
          walltimeHours: loadedJob.walltimeHours,
          slots: loadedJob.slots,
          licenseSettings: loadedJob.licenseSettings,
          extraInputFileIds: loadedJob.extraInputFileIds,
          noDecompress: loadedJob.noDecompress,
          submitMode: loadedJob.submitMode,
          isLowPriority: loadedJob.isLowPriority,
          onDemandLicenseSeller: loadedJob.onDemandLicenseSeller,
          tags: loadedJob.tags || [],
          projectId: loadedJob.projectId,
          orgCode: loadedJob.orgCode || '',
          automations: loadedJob.automations || [],
        })
        setState('jobConfigured')
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    }
  }, [])

  // Load from JSON
  const handleLoadFromJSON = useCallback(async () => {
    setShowLoadMenu(false)
    try {
      const path = await App.SelectFile('Select Job JSON File')
      if (!path) return

      const loadedJob = await loadJobFromJSON(path)
      if (loadedJob) {
        setJob(loadedJob)
        setState('jobConfigured')
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    }
  }, [loadJobFromJSON])

  // Load from SGE script
  const handleLoadFromSGE = useCallback(async () => {
    setShowLoadMenu(false)
    try {
      const path = await App.SelectFile('Select SGE Script')
      if (!path) return

      const loadedJob = await loadJobFromSGE(path)
      if (loadedJob) {
        setJob(loadedJob)
        setState('jobConfigured')
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    }
  }, [loadJobFromSGE])

  // Save to JSON
  const handleSaveToJSON = useCallback(async () => {
    setShowSaveMenu(false)
    try {
      const path = await App.SaveFile('Save Job JSON')
      if (!path) return

      await saveJobToJSON(path, job)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    }
  }, [job, saveJobToJSON])

  // Save to SGE script
  const handleSaveToSGE = useCallback(async () => {
    setShowSaveMenu(false)
    try {
      const path = await App.SaveFile('Save SGE Script')
      if (!path) return

      await saveJobToSGE(path, job)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    }
  }, [job, saveJobToSGE])

  // Save to CSV
  const handleSaveToCSV = useCallback(async () => {
    setShowSaveMenu(false)
    try {
      const path = await App.SaveFile('Save Job CSV')
      if (!path) return

      // Convert job to the DTO format and save as an array
      await App.SaveJobsToCSV(path, [job as unknown as wailsapp.JobSpecDTO])
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    }
  }, [job])

  // Handle directory selection
  const handleSelectDirectory = useCallback(async () => {
    try {
      const dir = await App.SelectDirectory('Select Job Directory')
      if (dir) {
        setDirectory(dir)
        if (inputMode === 'directory' && dir) {
          setState('inputsReady')
        }
      }
    } catch (err) {
      console.error('Failed to select directory:', err)
    }
  }, [inputMode])

  // v4.0.0 G1: Fetch file info for given paths
  const fetchFileInfo = useCallback(async (paths: string[]) => {
    if (paths.length === 0) return
    try {
      const infos = await App.GetLocalFilesInfo(paths)
      setFileInfoMap((prev) => {
        const updated = { ...prev }
        for (const info of infos) {
          updated[info.path] = {
            path: info.path,
            name: info.name,
            isDir: info.isDir,
            size: info.size,
            fileCount: info.fileCount,
          }
        }
        return updated
      })
    } catch (err) {
      console.error('Failed to fetch file info:', err)
    }
  }, [])

  // Handle file selection - v4.0.0: Use multi-file selection + fetch info
  const handleSelectFiles = useCallback(async () => {
    try {
      const files = await App.SelectMultipleFiles('Select Input Files')
      if (files && files.length > 0) {
        // Add files that aren't already in the list
        const existing = new Set(localFiles)
        const newFiles = files.filter((f: string) => !existing.has(f))
        if (newFiles.length > 0) {
          setLocalFiles((prev) => [...prev, ...newFiles])
          // Fetch file info for new files
          fetchFileInfo(newFiles)
        }
      }
    } catch (err) {
      console.error('Failed to select files:', err)
    }
  }, [localFiles, fetchFileInfo])

  // v4.0.0 G1: Handle folder selection - adds folder as a single item
  const handleSelectFolder = useCallback(async () => {
    try {
      const dir = await App.SelectDirectory('Select Folder to Add')
      if (dir && !localFiles.includes(dir)) {
        setLocalFiles((prev) => [...prev, dir])
        fetchFileInfo([dir])
      }
    } catch (err) {
      console.error('Failed to select folder:', err)
    }
  }, [localFiles, fetchFileInfo])

  // Remove a single file from the list
  const handleRemoveFile = useCallback((index: number) => {
    // v4.0.7 L1: Get path before removal to clean up cache
    setLocalFiles((prev) => {
      const pathToRemove = prev[index]
      // Clean up file info cache entry
      if (pathToRemove) {
        setFileInfoMap((infoMap) => {
          const { [pathToRemove]: _, ...rest } = infoMap
          return rest
        })
      }
      return prev.filter((_, i) => i !== index)
    })
  }, [])

  // Handle input mode change
  const handleInputModeChange = useCallback((mode: InputMode) => {
    setInputMode(mode)
    setDirectory('')
    setLocalFiles([])
    setRemoteFileIds([])
    setFileInfoMap({}) // v4.0.0 G1: Clear file info cache
  }, [])

  // v4.0.0 G1: Calculate total size from file info
  const totalSize = useMemo(() => {
    return localFiles.reduce((sum, path) => {
      const info = fileInfoMap[path]
      return sum + (info?.size || 0)
    }, 0)
  }, [localFiles, fileInfoMap])

  // v4.0.0 G1: Count files and folders separately
  const { fileCount, folderCount } = useMemo(() => {
    let files = 0
    let folders = 0
    for (const path of localFiles) {
      const info = fileInfoMap[path]
      if (info?.isDir) {
        folders++
      } else {
        files++
      }
    }
    return { fileCount: files, folderCount: folders }
  }, [localFiles, fileInfoMap])

  // Handle remote file selection from picker
  const handleRemoteFilesSelected = useCallback((fileIds: string[]) => {
    setRemoteFileIds(fileIds)
    // Automatically transition to inputsReady if we have files
    if (fileIds.length > 0) {
      setState('inputsReady')
    }
  }, [])

  // Check if inputs are ready
  const isInputsValid = useCallback(() => {
    switch (inputMode) {
      case 'directory':
        return !!directory
      case 'localFiles':
        return localFiles.length > 0
      case 'remoteFiles':
        return remoteFileIds.length > 0
      default:
        return false
    }
  }, [inputMode, directory, localFiles, remoteFileIds])

  // Handle job submission
  const handleSubmit = useCallback(async () => {
    if (!isInputsValid()) return

    setError(null)
    setState('executing')

    try {
      // Create input object matching SingleJobInputDTO structure
      const input = {
        job: job as unknown as wailsapp.JobSpecDTO,
        inputMode,
        directory,
        localFiles,
        remoteFileIds,
      } as wailsapp.SingleJobInputDTO
      const runId = await App.StartSingleJob(input)

      if (runId) {
        // v4.0.7 C1: Start polling with proper ref-based tracking to avoid stale closure
        isPollingRef.current = true

        const pollStatus = async () => {
          // Check ref instead of state to avoid stale closure
          if (!isPollingRef.current) return

          try {
            const status = await App.GetRunStatus()
            if (status.state === 'completed') {
              isPollingRef.current = false
              // v4.0.7 H2: Get job ID BEFORE state transition to ensure it displays
              const rows = await App.GetJobRows()
              if (rows && rows.length > 0) {
                setSubmittedJobId(rows[0].jobId)
              }
              setState('completed')
            } else if (status.state === 'failed') {
              isPollingRef.current = false
              setError('Job submission failed')
              setState('failed')
            } else if (isPollingRef.current) {
              // Continue polling using ref check, not state
              pollTimeoutRef.current = setTimeout(pollStatus, 1000)
            }
          } catch (pollErr) {
            // v4.0.7: Handle polling errors gracefully
            isPollingRef.current = false
            setError(pollErr instanceof Error ? pollErr.message : String(pollErr))
            setState('failed')
          }
        }
        pollStatus()
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
      setState('failed')
    }
  }, [job, inputMode, directory, localFiles, remoteFileIds, isInputsValid])

  // Handle cancel
  const handleCancel = useCallback(async () => {
    // v4.0.7 C1: Stop polling immediately on cancel
    isPollingRef.current = false
    if (pollTimeoutRef.current) {
      clearTimeout(pollTimeoutRef.current)
      pollTimeoutRef.current = null
    }
    try {
      await App.CancelRun()
      setState('failed')
      setError('Job cancelled by user')
    } catch (err) {
      console.error('Failed to cancel job:', err)
    }
  }, [])

  // Handle start over
  const handleStartOver = useCallback(() => {
    // v4.0.7 C1: Stop any active polling
    isPollingRef.current = false
    if (pollTimeoutRef.current) {
      clearTimeout(pollTimeoutRef.current)
      pollTimeoutRef.current = null
    }
    setState('initial')
    setJob({ ...DEFAULT_JOB_TEMPLATE })
    setInputMode('directory')
    setDirectory('')
    setLocalFiles([])
    setRemoteFileIds([])
    setFileInfoMap({}) // v4.0.0 G1: Clear file info cache
    setError(null)
    setSubmittedJobId(null)
  }, [])

  // Render step indicator
  const renderStepIndicator = () => {
    const currentStep = getCurrentStep()

    return (
      <div className="flex items-center justify-center gap-2 mb-6">
        {STEPS.map((step, index) => {
          const isCompleted = index < currentStep
          const isCurrent = index === currentStep
          const isFuture = index > currentStep

          return (
            <div key={step.key} className="flex items-center">
              <div
                className={clsx(
                  'flex items-center gap-2 px-3 py-1.5 rounded-full text-sm transition-colors',
                  isCompleted && 'bg-green-100 text-green-700',
                  isCurrent && 'bg-blue-500 text-white font-semibold',
                  isFuture && 'bg-gray-100 text-gray-400'
                )}
              >
                <span className="w-5 h-5 flex items-center justify-center rounded-full border border-current text-xs">
                  {index + 1}
                </span>
                {step.label}
              </div>
              {index < STEPS.length - 1 && (
                <div
                  className={clsx(
                    'w-8 h-0.5 mx-1',
                    index < currentStep ? 'bg-green-300' : 'bg-gray-200'
                  )}
                />
              )}
            </div>
          )
        })}
      </div>
    )
  }

  // Render content based on state
  const renderContent = () => {
    // Initial state - configure job
    if (state === 'initial') {
      return (
        <div className="flex flex-col items-center justify-center h-full">
          <h3 className="text-lg font-semibold mb-4">Configure Your Job</h3>
          <p className="text-gray-600 dark:text-gray-400 mb-6 text-center max-w-md">
            Set up your job parameters including software, hardware, and command settings.
          </p>
          <div className="flex gap-4 mb-6">
            <button
              onClick={() => setShowTemplateBuilder(true)}
              className="flex items-center gap-2 px-6 py-3 bg-blue-500 text-white rounded-lg hover:bg-blue-600"
            >
              <PlayIcon className="w-5 h-5" />
              Configure Job
            </button>

            {/* Load From dropdown */}
            <div className="relative">
              <button
                onClick={() => setShowLoadMenu(!showLoadMenu)}
                className="flex items-center gap-2 px-4 py-3 border border-gray-300 dark:border-gray-600 rounded-lg hover:bg-gray-100 dark:hover:bg-gray-700"
              >
                <DocumentArrowUpIcon className="w-5 h-5" />
                Load From...
                <ChevronDownIcon className="w-4 h-4" />
              </button>
              {showLoadMenu && (
                <div className="absolute top-full left-0 mt-1 w-48 bg-white dark:bg-gray-800 border border-gray-200 dark:border-gray-700 rounded-lg shadow-lg z-10">
                  <button
                    onClick={handleLoadFromCSV}
                    className="w-full px-4 py-2 text-left hover:bg-gray-100 dark:hover:bg-gray-700 rounded-t-lg"
                  >
                    CSV File
                  </button>
                  <button
                    onClick={handleLoadFromJSON}
                    className="w-full px-4 py-2 text-left hover:bg-gray-100 dark:hover:bg-gray-700"
                  >
                    JSON File
                  </button>
                  <button
                    onClick={handleLoadFromSGE}
                    className="w-full px-4 py-2 text-left hover:bg-gray-100 dark:hover:bg-gray-700 rounded-b-lg"
                  >
                    SGE Script
                  </button>
                </div>
              )}
            </div>
          </div>
          {error && (
            <div className="mt-4 p-3 bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800 rounded text-red-700 dark:text-red-400 text-sm max-w-md">
              {error}
            </div>
          )}
        </div>
      )
    }

    // Job configured - select inputs
    if (state === 'jobConfigured') {
      return (
        <div className="p-6">
          <h3 className="text-lg font-semibold mb-4">Select Input Files</h3>

          {/* Job summary */}
          <div className="mb-6 p-4 bg-gray-50 dark:bg-gray-800 rounded-lg">
            <div className="flex items-start justify-between">
              <div>
                <h4 className="font-medium mb-2">Job Configuration</h4>
                <div className="grid grid-cols-2 gap-2 text-sm">
                  <div>
                    <span className="text-gray-500">Job Name:</span> {job.jobName}
                  </div>
                  <div>
                    <span className="text-gray-500">Software:</span> {job.analysisCode}
                  </div>
                  <div>
                    <span className="text-gray-500">Hardware:</span> {job.coreType}
                  </div>
                  <div>
                    <span className="text-gray-500">Cores:</span> {job.coresPerSlot}
                  </div>
                </div>
                <button
                  onClick={() => setShowTemplateBuilder(true)}
                  className="mt-3 text-sm text-blue-500 hover:text-blue-600"
                >
                  Edit Configuration
                </button>
              </div>

              {/* Save As dropdown */}
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
                      onClick={handleSaveToCSV}
                      className="w-full px-4 py-2 text-left text-sm hover:bg-gray-100 dark:hover:bg-gray-700 rounded-t-lg"
                    >
                      CSV File
                    </button>
                    <button
                      onClick={handleSaveToJSON}
                      className="w-full px-4 py-2 text-left text-sm hover:bg-gray-100 dark:hover:bg-gray-700"
                    >
                      JSON File
                    </button>
                    <button
                      onClick={handleSaveToSGE}
                      className="w-full px-4 py-2 text-left text-sm hover:bg-gray-100 dark:hover:bg-gray-700 rounded-b-lg"
                    >
                      SGE Script
                    </button>
                  </div>
                )}
              </div>
            </div>
          </div>

          {/* Input mode selection */}
          <div className="mb-6">
            <label className="block text-sm font-medium mb-2">Input Mode</label>
            <div className="flex gap-4">
              <button
                onClick={() => handleInputModeChange('directory')}
                className={clsx(
                  'flex flex-col items-center gap-2 p-4 border-2 rounded-lg transition-colors flex-1',
                  inputMode === 'directory'
                    ? 'border-blue-500 bg-blue-50 dark:bg-blue-900/20'
                    : 'border-gray-200 dark:border-gray-700 hover:border-gray-300'
                )}
              >
                <FolderIcon className="w-8 h-8 text-blue-500" />
                <span className="font-medium">Directory</span>
                <span className="text-xs text-gray-500">Tar and upload a folder</span>
              </button>
              <button
                onClick={() => handleInputModeChange('localFiles')}
                className={clsx(
                  'flex flex-col items-center gap-2 p-4 border-2 rounded-lg transition-colors flex-1',
                  inputMode === 'localFiles'
                    ? 'border-blue-500 bg-blue-50 dark:bg-blue-900/20'
                    : 'border-gray-200 dark:border-gray-700 hover:border-gray-300'
                )}
              >
                <DocumentIcon className="w-8 h-8 text-green-500" />
                <span className="font-medium">Local Files</span>
                <span className="text-xs text-gray-500">Upload individual files</span>
              </button>
              <button
                onClick={() => handleInputModeChange('remoteFiles')}
                className={clsx(
                  'flex flex-col items-center gap-2 p-4 border-2 rounded-lg transition-colors flex-1',
                  inputMode === 'remoteFiles'
                    ? 'border-blue-500 bg-blue-50 dark:bg-blue-900/20'
                    : 'border-gray-200 dark:border-gray-700 hover:border-gray-300'
                )}
              >
                <CloudIcon className="w-8 h-8 text-purple-500" />
                <span className="font-medium">Remote Files</span>
                <span className="text-xs text-gray-500">Use existing Rescale files</span>
              </button>
            </div>
          </div>

          {/* Input selection based on mode */}
          <div className="mb-6">
            {inputMode === 'directory' && (
              <div>
                <label className="block text-sm font-medium mb-2">Select Directory</label>
                <div className="flex gap-2">
                  <input
                    type="text"
                    value={directory}
                    onChange={(e) => setDirectory(e.target.value)}
                    placeholder="/path/to/job/directory"
                    className="flex-1 px-3 py-2 text-sm border border-gray-300 dark:border-gray-600 rounded bg-white dark:bg-gray-800 focus:outline-none focus:ring-2 focus:ring-blue-500"
                  />
                  <button
                    onClick={handleSelectDirectory}
                    className="px-4 py-2 border border-gray-300 dark:border-gray-600 rounded hover:bg-gray-100 dark:hover:bg-gray-700"
                  >
                    <FolderOpenIcon className="w-5 h-5" />
                  </button>
                </div>
                <p className="mt-1 text-xs text-gray-500">
                  This directory will be tar'd and uploaded as job input
                </p>
              </div>
            )}

            {inputMode === 'localFiles' && (
              <div>
                <label className="block text-sm font-medium mb-2">Input Files</label>
                {/* v4.0.0 G1: Add Files and Add Folder buttons side by side */}
                <div className="flex gap-2 mb-3">
                  <button
                    onClick={handleSelectFiles}
                    className="flex items-center gap-2 px-4 py-2 border border-gray-300 dark:border-gray-600 rounded hover:bg-gray-100 dark:hover:bg-gray-700"
                  >
                    <PlusIcon className="w-5 h-5" />
                    Add Files
                  </button>
                  <button
                    onClick={handleSelectFolder}
                    className="flex items-center gap-2 px-4 py-2 border border-gray-300 dark:border-gray-600 rounded hover:bg-gray-100 dark:hover:bg-gray-700"
                  >
                    <FolderPlusIcon className="w-5 h-5" />
                    Add Folder
                  </button>
                </div>
                {localFiles.length > 0 && (
                  <div className="p-3 bg-gray-50 dark:bg-gray-800 rounded border border-gray-200 dark:border-gray-700">
                    {/* Header with counts */}
                    <div className="flex items-center justify-between mb-2">
                      <span className="text-sm font-medium text-gray-700 dark:text-gray-300">
                        {fileCount > 0 && `${fileCount} file${fileCount !== 1 ? 's' : ''}`}
                        {fileCount > 0 && folderCount > 0 && ', '}
                        {folderCount > 0 && `${folderCount} folder${folderCount !== 1 ? 's' : ''}`}
                      </span>
                      <button
                        onClick={() => {
                          setLocalFiles([])
                          setFileInfoMap({})
                        }}
                        className="text-xs text-red-500 hover:text-red-600"
                      >
                        Clear All
                      </button>
                    </div>
                    {/* File/folder list with sizes */}
                    <div className="space-y-1 max-h-48 overflow-y-auto">
                      {localFiles.map((filePath, i) => {
                        const info = fileInfoMap[filePath]
                        const isDir = info?.isDir || false
                        const name = info?.name || filePath.split('/').pop() || filePath
                        return (
                          <div
                            key={i}
                            className="flex items-center justify-between group text-sm bg-white dark:bg-gray-700 px-2 py-1.5 rounded"
                          >
                            <div className="flex items-center gap-2 flex-1 min-w-0">
                              {isDir ? (
                                <FolderIcon className="w-4 h-4 text-amber-500 flex-shrink-0" />
                              ) : (
                                <DocumentIcon className="w-4 h-4 text-gray-400 flex-shrink-0" />
                              )}
                              <span className="truncate text-gray-600 dark:text-gray-300" title={filePath}>
                                {name}
                              </span>
                            </div>
                            <div className="flex items-center gap-2 flex-shrink-0">
                              <span className="text-xs text-gray-400">
                                {info ? (
                                  isDir
                                    ? `${info.fileCount} file${info.fileCount !== 1 ? 's' : ''}`
                                    : formatFileSize(info.size)
                                ) : (
                                  '...'
                                )}
                              </span>
                              <button
                                onClick={() => handleRemoveFile(i)}
                                className="opacity-0 group-hover:opacity-100 text-gray-400 hover:text-red-500 transition-opacity"
                                title="Remove"
                              >
                                <XMarkIcon className="w-4 h-4" />
                              </button>
                            </div>
                          </div>
                        )
                      })}
                    </div>
                    {/* Total size footer */}
                    {totalSize > 0 && (
                      <div className="mt-2 pt-2 border-t border-gray-200 dark:border-gray-600 text-right">
                        <span className="text-xs text-gray-500">
                          Total: <span className="font-medium">{formatFileSize(totalSize)}</span>
                        </span>
                      </div>
                    )}
                  </div>
                )}
                <p className="mt-2 text-xs text-gray-500">
                  Add files or folders to upload as job inputs
                </p>
              </div>
            )}

            {inputMode === 'remoteFiles' && (
              <div>
                <label className="block text-sm font-medium mb-2">Remote Files</label>
                <button
                  onClick={() => setShowRemoteFilePicker(true)}
                  className="flex items-center gap-2 px-4 py-2 border border-gray-300 dark:border-gray-600 rounded hover:bg-gray-100 dark:hover:bg-gray-700"
                >
                  <CloudIcon className="w-5 h-5" />
                  Browse Remote Files...
                </button>
                {remoteFileIds.length > 0 && (
                  <div className="mt-3 p-3 bg-gray-50 dark:bg-gray-800 rounded border border-gray-200 dark:border-gray-700">
                    <div className="flex items-center justify-between mb-2">
                      <span className="text-sm font-medium text-gray-700 dark:text-gray-300">
                        {remoteFileIds.length} file{remoteFileIds.length !== 1 ? 's' : ''} selected
                      </span>
                      <button
                        onClick={() => setRemoteFileIds([])}
                        className="text-xs text-red-500 hover:text-red-600"
                      >
                        Clear
                      </button>
                    </div>
                    <div className="text-xs text-gray-500 dark:text-gray-400 font-mono max-h-24 overflow-y-auto space-y-0.5">
                      {remoteFileIds.map((id, i) => (
                        <div key={i} className="truncate">{id}</div>
                      ))}
                    </div>
                  </div>
                )}
                <p className="mt-2 text-xs text-gray-500">
                  Browse and select files from your Rescale library
                </p>
              </div>
            )}
          </div>

          {/* Continue button */}
          <button
            onClick={() => {
              if (isInputsValid()) {
                setState('inputsReady')
              }
            }}
            disabled={!isInputsValid()}
            className={clsx(
              'flex items-center gap-2 px-4 py-2 rounded',
              isInputsValid()
                ? 'bg-blue-500 text-white hover:bg-blue-600'
                : 'bg-gray-300 dark:bg-gray-600 text-gray-500 cursor-not-allowed'
            )}
          >
            Continue
          </button>
        </div>
      )
    }

    // Inputs ready - confirm and submit
    if (state === 'inputsReady') {
      return (
        <div className="p-6">
          <h3 className="text-lg font-semibold mb-4">Review and Submit</h3>

          {/* Summary */}
          <div className="mb-6 space-y-4">
            <div className="p-4 bg-gray-50 dark:bg-gray-800 rounded-lg">
              <h4 className="font-medium mb-2">Job Configuration</h4>
              <div className="grid grid-cols-2 gap-2 text-sm">
                <div>
                  <span className="text-gray-500">Name:</span> {job.jobName}
                </div>
                <div>
                  <span className="text-gray-500">Software:</span> {job.analysisCode}{' '}
                  {job.analysisVersion}
                </div>
                <div>
                  <span className="text-gray-500">Hardware:</span> {job.coreType}
                </div>
                <div>
                  <span className="text-gray-500">Cores:</span> {job.coresPerSlot}
                </div>
                <div>
                  <span className="text-gray-500">Walltime:</span> {job.walltimeHours}h
                </div>
                <div>
                  <span className="text-gray-500">Submit Mode:</span> {job.submitMode}
                </div>
              </div>
            </div>

            <div className="p-4 bg-gray-50 dark:bg-gray-800 rounded-lg">
              <h4 className="font-medium mb-2">Input Files</h4>
              <div className="text-sm">
                {inputMode === 'directory' && (
                  <div>
                    <span className="text-gray-500">Directory:</span> {directory}
                  </div>
                )}
                {inputMode === 'localFiles' && (
                  <div>
                    <span className="text-gray-500">Files:</span> {localFiles.length} file(s)
                  </div>
                )}
                {inputMode === 'remoteFiles' && (
                  <div>
                    <span className="text-gray-500">Remote Files:</span> {remoteFileIds.length} file(s)
                  </div>
                )}
              </div>
            </div>
          </div>

          {/* Submit button */}
          <div className="flex gap-4">
            <button
              onClick={() => setState('jobConfigured')}
              className="px-4 py-2 border border-gray-300 dark:border-gray-600 rounded hover:bg-gray-100 dark:hover:bg-gray-700"
            >
              Back
            </button>
            <button
              onClick={handleSubmit}
              className="flex items-center gap-2 px-6 py-2 bg-green-500 text-white rounded hover:bg-green-600"
            >
              <PlayIcon className="w-5 h-5" />
              Submit Job
            </button>
          </div>
        </div>
      )
    }

    // Executing
    if (state === 'executing') {
      return (
        <div className="flex flex-col items-center justify-center h-full">
          <ArrowPathIcon className="w-16 h-16 text-blue-500 animate-spin mb-4" />
          <h3 className="text-lg font-semibold mb-2">Submitting Job...</h3>
          <p className="text-gray-600 dark:text-gray-400 mb-6">
            Please wait while your job is being processed
          </p>
          <button
            onClick={handleCancel}
            className="flex items-center gap-2 px-4 py-2 bg-red-500 text-white rounded hover:bg-red-600"
          >
            <StopIcon className="w-5 h-5" />
            Cancel
          </button>
        </div>
      )
    }

    // Completed
    if (state === 'completed') {
      return (
        <div className="flex flex-col items-center justify-center h-full">
          <CheckCircleIcon className="w-16 h-16 text-green-500 mb-4" />
          <h3 className="text-lg font-semibold mb-2">Job Submitted Successfully!</h3>
          {submittedJobId && (
            <p className="text-gray-600 dark:text-gray-400 mb-6">
              Job ID: <code className="bg-gray-100 dark:bg-gray-800 px-2 py-1 rounded">{submittedJobId}</code>
            </p>
          )}
          <button
            onClick={handleStartOver}
            className="flex items-center gap-2 px-4 py-2 bg-blue-500 text-white rounded hover:bg-blue-600"
          >
            <ArrowPathIcon className="w-5 h-5" />
            Submit Another Job
          </button>
        </div>
      )
    }

    // Failed
    if (state === 'failed') {
      return (
        <div className="flex flex-col items-center justify-center h-full">
          <XCircleIcon className="w-16 h-16 text-red-500 mb-4" />
          <h3 className="text-lg font-semibold text-red-600 mb-2">Job Submission Failed</h3>
          {error && (
            <div className="max-w-md mb-6 p-3 bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800 rounded text-red-700 dark:text-red-400 text-sm">
              {error}
            </div>
          )}
          <button
            onClick={handleStartOver}
            className="flex items-center gap-2 px-4 py-2 bg-blue-500 text-white rounded hover:bg-blue-600"
          >
            <ArrowPathIcon className="w-5 h-5" />
            Try Again
          </button>
        </div>
      )
    }

    return null
  }

  return (
    <div className="h-full flex flex-col">
      {/* Step indicator */}
      <div className="px-6 py-4 border-b border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800">
        {renderStepIndicator()}
      </div>

      {/* Content */}
      <div className="flex-1 overflow-auto">{renderContent()}</div>

      {/* Template builder dialog */}
      <TemplateBuilder
        isOpen={showTemplateBuilder}
        initialTemplate={job}
        onClose={() => setShowTemplateBuilder(false)}
        onSave={handleTemplateSave}
      />

      {/* Remote file picker dialog */}
      <RemoteFilePicker
        isOpen={showRemoteFilePicker}
        onClose={() => setShowRemoteFilePicker(false)}
        onSelect={handleRemoteFilesSelected}
        title="Select Remote Files for Job Input"
      />
    </div>
  )
}
