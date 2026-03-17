import { useCallback, useEffect, useMemo } from 'react'
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
import { useJobStore, useConfigStore } from '../../stores'
import type { JobSpec } from '../../stores'
import { useSingleJobStore } from '../../stores/singleJobStore'
import { useRunStore } from '../../stores/runStore'
import { TemplateBuilder, RemoteFilePicker } from '../widgets'
import * as App from '../../../wailsjs/go/wailsapp/App'
import { wailsapp } from '../../../wailsjs/go/models'

// Helper to format file sizes nicely
function formatFileSize(bytes: number): string {
  if (bytes === 0) return '0 B'
  const k = 1024
  const sizes = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.floor(Math.log(bytes) / Math.log(k))
  return parseFloat((bytes / Math.pow(k, i)).toFixed(1)) + ' ' + sizes[i]
}

// Progress steps
const STEPS = [
  { key: 'configure', label: 'Configure' },
  { key: 'inputs', label: 'Select Inputs' },
  { key: 'submit', label: 'Submit' },
  { key: 'complete', label: 'Complete' },
]

// v4.7.1: Compression options for tar
const COMPRESSION_OPTIONS = ['gzip', 'none'] as const

export function SingleJobTab() {
  const {
    loadMemory,
    loadJobFromJSON,
    saveJobToJSON,
    loadJobFromSGE,
    saveJobToSGE,
  } = useJobStore()

  // v4.7.1: Config store for tar options
  const { config, updateConfig, saveConfig } = useConfigStore()

  // v4.7.3: Store-backed state (persists across tab navigation)
  const sjStore = useSingleJobStore()
  const {
    state,
    job,
    inputMode,
    directory,
    localFiles,
    remoteFileIds,
    error,
    fileInfoMap,
    showTemplateBuilder,
    showLoadMenu,
    showSaveMenu,
    showRemoteFilePicker,
  } = sjStore

  // v4.7.3: Run store for active run awareness and queue mechanism
  const activeRun = useRunStore((s) => s.activeRun)
  const queueStatus = useRunStore((s) => s.queueStatus)
  const cancelRun = useRunStore((s) => s.cancelRun)
  const clearActiveRun = useRunStore((s) => s.clearActiveRun)

  // Derive whether a run is currently active (for queue behavior)
  const isRunActive = activeRun?.status === 'active'

  // Load memory on mount
  useEffect(() => {
    loadMemory()
  }, [loadMemory])

  // v4.7.3 Fix: Sync singleJobStore.state when single-job run transitions to terminal state.
  // sjStore.state is set to 'executing' (singleJobStore.ts submitJob) but never transitions
  // to 'completed' on normal run completion — only on cancel. This bridges the gap.
  useEffect(() => {
    if (activeRun && activeRun.runType === 'single' &&
        (activeRun.status === 'completed' || activeRun.status === 'failed' || activeRun.status === 'cancelled') &&
        state === 'executing') {
      if (activeRun.status === 'completed') {
        sjStore.setState('completed')
      } else {
        sjStore.setState('failed')
      }
    }
  }, [activeRun?.status, state, sjStore])

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
    sjStore.setJob(template)
    sjStore.setState('jobConfigured')
    sjStore.setShowTemplateBuilder(false)
  }, [sjStore])

  // Load from CSV
  const handleLoadFromCSV = useCallback(async () => {
    sjStore.setShowLoadMenu(false)
    try {
      const path = await App.SelectFile('Select Jobs CSV File')
      if (!path) return

      const jobs = await App.LoadJobsFromCSV(path)
      if (jobs && jobs.length > 0) {
        // Take the first job as the template
        const loadedJob = jobs[0]
        sjStore.setJob({
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
        sjStore.setState('jobConfigured')
      }
    } catch (err) {
      sjStore.setError(err instanceof Error ? err.message : String(err))
    }
  }, [sjStore])

  // Load from JSON
  const handleLoadFromJSON = useCallback(async () => {
    sjStore.setShowLoadMenu(false)
    try {
      const path = await App.SelectFile('Select Job JSON File')
      if (!path) return

      const loadedJob = await loadJobFromJSON(path)
      if (loadedJob) {
        sjStore.setJob(loadedJob)
        sjStore.setState('jobConfigured')
      }
    } catch (err) {
      sjStore.setError(err instanceof Error ? err.message : String(err))
    }
  }, [sjStore, loadJobFromJSON])

  // Load from SGE script
  const handleLoadFromSGE = useCallback(async () => {
    sjStore.setShowLoadMenu(false)
    try {
      const path = await App.SelectFile('Select SGE Script')
      if (!path) return

      const loadedJob = await loadJobFromSGE(path)
      if (loadedJob) {
        sjStore.setJob(loadedJob)
        sjStore.setState('jobConfigured')
      }
    } catch (err) {
      sjStore.setError(err instanceof Error ? err.message : String(err))
    }
  }, [sjStore, loadJobFromSGE])

  // Save to JSON
  const handleSaveToJSON = useCallback(async () => {
    sjStore.setShowSaveMenu(false)
    try {
      const path = await App.SaveFile('Save Job JSON')
      if (!path) return

      await saveJobToJSON(path, job)
    } catch (err) {
      sjStore.setError(err instanceof Error ? err.message : String(err))
    }
  }, [sjStore, job, saveJobToJSON])

  // Save to SGE script
  const handleSaveToSGE = useCallback(async () => {
    sjStore.setShowSaveMenu(false)
    try {
      const path = await App.SaveFile('Save SGE Script')
      if (!path) return

      await saveJobToSGE(path, job)
    } catch (err) {
      sjStore.setError(err instanceof Error ? err.message : String(err))
    }
  }, [sjStore, job, saveJobToSGE])

  // Save to CSV
  const handleSaveToCSV = useCallback(async () => {
    sjStore.setShowSaveMenu(false)
    try {
      const path = await App.SaveFile('Save Job CSV')
      if (!path) return

      // Convert job to the DTO format and save as an array
      await App.SaveJobsToCSV(path, [job as unknown as wailsapp.JobSpecDTO])
    } catch (err) {
      sjStore.setError(err instanceof Error ? err.message : String(err))
    }
  }, [sjStore, job])

  // Handle directory selection
  const handleSelectDirectory = useCallback(async () => {
    try {
      const dir = await App.SelectDirectory('Select Job Directory')
      if (dir) {
        sjStore.setDirectory(dir)
        if (inputMode === 'directory' && dir) {
          sjStore.setState('inputsReady')
        }
      }
    } catch (err) {
      console.error('Failed to select directory:', err)
    }
  }, [sjStore, inputMode])

  // v4.0.0 G1: Fetch file info for given paths
  const fetchFileInfo = useCallback(async (paths: string[]) => {
    if (paths.length === 0) return
    try {
      const infos = await App.GetLocalFilesInfo(paths)
      sjStore.setFileInfoMap((prev) => {
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
  }, [sjStore])

  // Handle file selection - v4.0.0: Use multi-file selection + fetch info
  const handleSelectFiles = useCallback(async () => {
    try {
      const files = await App.SelectMultipleFiles('Select Input Files')
      if (files && files.length > 0) {
        // Add files that aren't already in the list
        const existing = new Set(localFiles)
        const newFiles = files.filter((f: string) => !existing.has(f))
        if (newFiles.length > 0) {
          sjStore.addLocalFiles(newFiles)
          // Fetch file info for new files
          fetchFileInfo(newFiles)
        }
      }
    } catch (err) {
      console.error('Failed to select files:', err)
    }
  }, [sjStore, localFiles, fetchFileInfo])

  // v4.0.0 G1: Handle folder selection - adds folder as a single item
  const handleSelectFolder = useCallback(async () => {
    try {
      const dir = await App.SelectDirectory('Select Folder to Add')
      if (dir && !localFiles.includes(dir)) {
        sjStore.addLocalFiles([dir])
        fetchFileInfo([dir])
      }
    } catch (err) {
      console.error('Failed to select folder:', err)
    }
  }, [sjStore, localFiles, fetchFileInfo])

  // Remove a single file from the list
  const handleRemoveFile = useCallback((index: number) => {
    sjStore.removeLocalFile(index)
  }, [sjStore])

  // Handle input mode change
  const handleInputModeChange = useCallback((mode: 'directory' | 'localFiles' | 'remoteFiles') => {
    sjStore.setInputMode(mode)
  }, [sjStore])

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
    sjStore.setRemoteFileIds(fileIds)
    // Automatically transition to inputsReady if we have files
    if (fileIds.length > 0) {
      sjStore.setState('inputsReady')
    }
  }, [sjStore])

  // v4.7.3: Handle job submission — delegates to store's submitJob() or queueJob()
  const handleSubmit = useCallback(async () => {
    if (!sjStore.isInputsValid()) return

    if (isRunActive) {
      // Queue instead of submit when a run is already active
      sjStore.queueJob()
      return
    }

    await sjStore.submitJob()
  }, [sjStore, isRunActive])

  // v4.7.3: Handle cancel — delegates to runStore.cancelRun()
  // Guard: only cancel if the active run is actually a single-job run that's still active.
  // After cancel, only force failed state if the run was actually cancelled (not if it already completed).
  const handleCancel = useCallback(async () => {
    const currentRun = useRunStore.getState().activeRun
    if (!currentRun || currentRun.runType !== 'single' || currentRun.status !== 'active') return

    try {
      await cancelRun()
      const updatedRun = useRunStore.getState().activeRun
      if (!updatedRun || updatedRun.status === 'cancelled') {
        sjStore.setState('failed')
        sjStore.setError('Job cancelled by user')
      }
      // If run already completed/failed, the useEffect sync (Fix 5a) handles the state transition
    } catch (err) {
      console.error('Failed to cancel job:', err)
    }
  }, [sjStore, cancelRun])

  // v4.7.3: Handle start over — clears activeRun (if it's a terminal single-job run) + resets store.
  // Guard: only clear activeRun if it's a completed/failed/cancelled single-job run — never kill an active PUR run.
  const handleStartOver = useCallback(async () => {
    if (activeRun && activeRun.runType === 'single' &&
        (activeRun.status === 'completed' || activeRun.status === 'failed' || activeRun.status === 'cancelled')) {
      await clearActiveRun()
    }
    sjStore.reset()
  }, [sjStore, activeRun, clearActiveRun])

  // v4.7.3: Derive the submitted job ID from runStore's activeRun
  const submittedJobId = activeRun?.runType === 'single' && activeRun.jobRows[0]?.jobId
    ? activeRun.jobRows[0].jobId
    : null

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
              onClick={() => sjStore.setShowTemplateBuilder(true)}
              className="flex items-center gap-2 px-6 py-3 bg-blue-500 text-white rounded-lg hover:bg-blue-600"
            >
              <PlayIcon className="w-5 h-5" />
              Configure Job
            </button>

            {/* Load From dropdown */}
            <div className="relative">
              <button
                onClick={() => sjStore.setShowLoadMenu(!showLoadMenu)}
                className="flex items-center gap-2 px-4 py-3 border border-gray-300 dark:border-gray-600 rounded-lg hover:bg-gray-100 dark:hover:bg-gray-700"
              >
                <DocumentArrowUpIcon className="w-5 h-5" />
                Load Existing Job Settings
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
                  onClick={() => sjStore.setShowTemplateBuilder(true)}
                  className="mt-3 text-sm text-blue-500 hover:text-blue-600"
                >
                  Edit Configuration
                </button>
              </div>

              {/* Save As dropdown */}
              <div className="relative">
                <button
                  onClick={() => sjStore.setShowSaveMenu(!showSaveMenu)}
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
                <span className="font-medium">Archive Directory</span>
                <span className="text-xs text-gray-500">Tar and upload a folder as a single archive</span>
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
                <span className="font-medium">Select Files</span>
                <span className="text-xs text-gray-500">Upload files and folder contents individually</span>
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
                <span className="font-medium">Rescale Library</span>
                <span className="text-xs text-gray-500">Use files already in your Rescale library</span>
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
                    onChange={(e) => sjStore.setDirectory(e.target.value)}
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
                  This directory will be archived (tar.gz), uploaded, and automatically decompressed on the Rescale cluster.
                </p>

                {/* v4.7.1: Tar Options for directory mode */}
                <div className="mt-4 p-3 bg-gray-50 dark:bg-gray-800 rounded border border-gray-200 dark:border-gray-700">
                  <h5 className="text-xs font-medium text-gray-500 mb-2">Tar Options</h5>
                  <div className="space-y-3">
                    <div className="grid grid-cols-2 gap-3">
                      <div>
                        <label className="block text-xs text-gray-500 mb-1">Exclude Patterns</label>
                        <input
                          type="text"
                          className="w-full px-3 py-1.5 text-sm border border-gray-300 dark:border-gray-600 rounded bg-white dark:bg-gray-800 focus:outline-none focus:ring-2 focus:ring-blue-500"
                          placeholder="*.tmp,*.log"
                          value={config?.excludePatterns || ''}
                          onChange={(e) => updateConfig({ excludePatterns: e.target.value })}
                          onBlur={() => saveConfig()}
                        />
                      </div>
                      <div>
                        <label className="block text-xs text-gray-500 mb-1">Include Patterns</label>
                        <input
                          type="text"
                          className="w-full px-3 py-1.5 text-sm border border-gray-300 dark:border-gray-600 rounded bg-white dark:bg-gray-800 focus:outline-none focus:ring-2 focus:ring-blue-500"
                          placeholder="*.dat,*.csv"
                          value={config?.includePatterns || ''}
                          onChange={(e) => updateConfig({ includePatterns: e.target.value })}
                          onBlur={() => saveConfig()}
                        />
                      </div>
                    </div>
                    <div className="flex items-center gap-4">
                      <div className="flex-1">
                        <label className="block text-xs text-gray-500 mb-1">Compression</label>
                        <select
                          className="w-full px-3 py-1.5 text-sm border border-gray-300 dark:border-gray-600 rounded bg-white dark:bg-gray-800 focus:outline-none focus:ring-2 focus:ring-blue-500"
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
                      <label className="flex items-center gap-2 mt-4 cursor-pointer">
                        <input
                          type="checkbox"
                          checked={config?.flattenTar || false}
                          onChange={(e) => {
                            updateConfig({ flattenTar: e.target.checked })
                            saveConfig()
                          }}
                          className="h-4 w-4 rounded border border-gray-300 text-blue-500 focus:ring-blue-500 focus:ring-2 bg-white cursor-pointer"
                        />
                        <span className="text-xs text-gray-600 dark:text-gray-300">Flatten</span>
                      </label>
                    </div>
                    <p className="text-xs text-gray-400">
                      Patterns support wildcards (*). Use comma-separated list.
                    </p>
                  </div>
                </div>
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
                        onClick={() => sjStore.clearLocalFiles()}
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
                  Files and folder contents are uploaded individually as job inputs (no archiving).
                </p>
              </div>
            )}

            {inputMode === 'remoteFiles' && (
              <div>
                <label className="block text-sm font-medium mb-2">Rescale Library</label>
                <button
                  onClick={() => sjStore.setShowRemoteFilePicker(true)}
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
                        onClick={() => sjStore.setRemoteFileIds([])}
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
                  Browse and select files already uploaded to your Rescale account.
                </p>
              </div>
            )}
          </div>

          {/* Continue button */}
          <button
            onClick={() => {
              if (sjStore.isInputsValid()) {
                sjStore.setState('inputsReady')
              }
            }}
            disabled={!sjStore.isInputsValid()}
            className={clsx(
              'flex items-center gap-2 px-4 py-2 rounded',
              sjStore.isInputsValid()
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

          {/* v4.7.3: Queue status inline banner */}
          {queueStatus && (
            <div className={clsx(
              'mb-4 p-3 rounded text-sm',
              queueStatus === 'queued' && 'bg-yellow-50 dark:bg-yellow-900/20 border border-yellow-200 dark:border-yellow-800 text-yellow-700 dark:text-yellow-400',
              queueStatus === 'starting' && 'bg-blue-50 dark:bg-blue-900/20 border border-blue-200 dark:border-blue-800 text-blue-700 dark:text-blue-400',
              queueStatus === 'started' && 'bg-green-50 dark:bg-green-900/20 border border-green-200 dark:border-green-800 text-green-700 dark:text-green-400',
              queueStatus.startsWith('failed') && 'bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800 text-red-700 dark:text-red-400',
            )}>
              {queueStatus === 'queued' && 'Job queued. It will start automatically when the current run completes.'}
              {queueStatus === 'starting' && 'Starting queued job...'}
              {queueStatus === 'started' && 'Queued job started successfully.'}
              {queueStatus.startsWith('failed') && `Queue failed: ${queueStatus.replace('failed: ', '')}`}
            </div>
          )}

          {/* Submit button */}
          <div className="flex gap-4">
            <button
              onClick={() => sjStore.setState('jobConfigured')}
              className="px-4 py-2 border border-gray-300 dark:border-gray-600 rounded hover:bg-gray-100 dark:hover:bg-gray-700"
            >
              Back
            </button>
            <button
              onClick={handleSubmit}
              className={clsx(
                'flex items-center gap-2 px-6 py-2 text-white rounded',
                isRunActive
                  ? 'bg-yellow-500 hover:bg-yellow-600'
                  : 'bg-green-500 hover:bg-green-600'
              )}
            >
              <PlayIcon className="w-5 h-5" />
              {isRunActive ? 'Queue Job' : 'Submit Job'}
            </button>
          </div>
        </div>
      )
    }

    // Executing — status-aware: adapts header and buttons when run transitions to terminal state
    if (state === 'executing') {
      const isTerminal = activeRun && activeRun.runType === 'single' &&
        (activeRun.status === 'completed' || activeRun.status === 'failed' || activeRun.status === 'cancelled')

      return (
        <div className="flex flex-col items-center justify-center h-full">
          {!isTerminal && (
            <ArrowPathIcon className="w-16 h-16 text-blue-500 animate-spin mb-4" />
          )}
          {isTerminal && activeRun.status === 'completed' && (
            <CheckCircleIcon className="w-16 h-16 text-green-500 mb-4" />
          )}
          {isTerminal && (activeRun.status === 'failed' || activeRun.status === 'cancelled') && (
            <XCircleIcon className="w-16 h-16 text-red-500 mb-4" />
          )}
          <h3 className="text-lg font-semibold mb-2">
            {!activeRun || activeRun.status === 'active' ? 'Submitting Job...'
              : activeRun.status === 'completed' ? 'Job Complete'
              : activeRun.status === 'failed' ? 'Job Failed'
              : activeRun.status === 'cancelled' ? 'Job Cancelled'
              : 'Job Status'}
          </h3>
          <p className="text-gray-600 dark:text-gray-400 mb-6">
            {!isTerminal
              ? 'Please wait while your job is being processed'
              : activeRun.status === 'completed' ? 'Your job has been submitted successfully'
              : activeRun.status === 'cancelled' ? 'Job was cancelled by user'
              : 'Job submission encountered an error'}
          </p>
          {activeRun?.runType === 'single' && activeRun?.status === 'active' && (
            <button
              onClick={handleCancel}
              className="flex items-center gap-2 px-4 py-2 bg-red-500 text-white rounded hover:bg-red-600"
            >
              <StopIcon className="w-5 h-5" />
              Cancel
            </button>
          )}
          {isTerminal && (
            <button
              onClick={handleStartOver}
              className="flex items-center gap-2 px-4 py-2 bg-blue-500 text-white rounded hover:bg-blue-600"
            >
              <ArrowPathIcon className="w-5 h-5" />
              Submit Another Job
            </button>
          )}
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
        onClose={() => sjStore.setShowTemplateBuilder(false)}
        onSave={handleTemplateSave}
      />

      {/* Remote file picker dialog */}
      <RemoteFilePicker
        isOpen={showRemoteFilePicker}
        onClose={() => sjStore.setShowRemoteFilePicker(false)}
        onSelect={handleRemoteFilesSelected}
        title="Select Remote Files for Job Input"
      />
    </div>
  )
}
