// v4.7.3: SingleJob state store — extracts local state from SingleJobTab.tsx
// so it persists across tab navigation.

import { create } from 'zustand'
import * as App from '../../wailsjs/go/wailsapp/App'
import { wailsapp } from '../../wailsjs/go/models'
import type { JobSpec, JobRow } from '../types/jobs'
import { DEFAULT_JOB_TEMPLATE } from './jobStore'

// Single job workflow states
export type SingleJobState =
  | 'initial'
  | 'jobConfigured'
  | 'inputsReady'
  | 'executing'
  | 'completed'
  | 'failed'

// Input mode for single job
export type InputMode = 'directory' | 'localFiles' | 'remoteFiles'

// v4.0.0 G1: File info for displaying sizes
export interface FileInfo {
  path: string
  name: string
  isDir: boolean
  size: number
  fileCount: number
}

interface SingleJobStore {
  state: SingleJobState
  job: JobSpec
  inputMode: InputMode
  directory: string
  localFiles: string[]
  remoteFileIds: string[]
  error: string | null
  submittedJobId: string | null
  fileInfoMap: Record<string, FileInfo>

  // UI state
  showTemplateBuilder: boolean
  showLoadMenu: boolean
  showSaveMenu: boolean
  showRemoteFilePicker: boolean

  // Actions
  setState: (s: SingleJobState) => void
  setJob: (j: JobSpec) => void
  setInputMode: (m: InputMode) => void
  setDirectory: (d: string) => void
  addLocalFiles: (files: string[]) => void
  removeLocalFile: (idx: number) => void
  clearLocalFiles: () => void
  setRemoteFileIds: (ids: string[]) => void
  setError: (e: string | null) => void
  setSubmittedJobId: (id: string | null) => void
  setFileInfoMap: (updater: (prev: Record<string, FileInfo>) => Record<string, FileInfo>) => void
  setShowTemplateBuilder: (v: boolean) => void
  setShowLoadMenu: (v: boolean) => void
  setShowSaveMenu: (v: boolean) => void
  setShowRemoteFilePicker: (v: boolean) => void

  isInputsValid: () => boolean

  submitJob: () => Promise<string | null>
  queueJob: () => void
  reset: () => void
}

export const useSingleJobStore = create<SingleJobStore>((set, get) => ({
  state: 'initial',
  job: { ...DEFAULT_JOB_TEMPLATE },
  inputMode: 'directory',
  directory: '',
  localFiles: [],
  remoteFileIds: [],
  error: null,
  submittedJobId: null,
  fileInfoMap: {},

  showTemplateBuilder: false,
  showLoadMenu: false,
  showSaveMenu: false,
  showRemoteFilePicker: false,

  setState: (s) => set({ state: s }),
  setJob: (j) => set({ job: j }),
  setInputMode: (m) => set({
    inputMode: m,
    directory: '',
    localFiles: [],
    remoteFileIds: [],
    fileInfoMap: {},
  }),
  setDirectory: (d) => set({ directory: d }),
  addLocalFiles: (files) => set((prev) => {
    const existing = new Set(prev.localFiles)
    const newFiles = files.filter((f) => !existing.has(f))
    return { localFiles: [...prev.localFiles, ...newFiles] }
  }),
  removeLocalFile: (idx) => set((prev) => {
    const pathToRemove = prev.localFiles[idx]
    const updated = prev.localFiles.filter((_, i) => i !== idx)
    if (pathToRemove) {
      const { [pathToRemove]: _, ...rest } = prev.fileInfoMap
      return { localFiles: updated, fileInfoMap: rest }
    }
    return { localFiles: updated }
  }),
  clearLocalFiles: () => set({ localFiles: [], fileInfoMap: {} }),
  setRemoteFileIds: (ids) => set({ remoteFileIds: ids }),
  setError: (e) => set({ error: e }),
  setSubmittedJobId: (id) => set({ submittedJobId: id }),
  setFileInfoMap: (updater) => set((prev) => ({
    fileInfoMap: updater(prev.fileInfoMap),
  })),
  setShowTemplateBuilder: (v) => set({ showTemplateBuilder: v }),
  setShowLoadMenu: (v) => set({ showLoadMenu: v }),
  setShowSaveMenu: (v) => set({ showSaveMenu: v }),
  setShowRemoteFilePicker: (v) => set({ showRemoteFilePicker: v }),

  isInputsValid: () => {
    const { inputMode, directory, localFiles, remoteFileIds } = get()
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
  },

  submitJob: async () => {
    const { job, inputMode, directory, localFiles, remoteFileIds, isInputsValid } = get()
    if (!isInputsValid()) return null

    set({ error: null, state: 'executing' })

    try {
      const input = {
        job: job as unknown as wailsapp.JobSpecDTO,
        inputMode,
        directory,
        localFiles,
        remoteFileIds,
      } as wailsapp.SingleJobInputDTO

      const runId = await App.StartSingleJob(input)

      if (runId) {
        // Build initial single-element jobRows
        const initialJobRows: JobRow[] = [{
          index: 0,
          directory: inputMode === 'directory' ? directory : '',
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
        }]

        // Register with runStore (imported dynamically to avoid circular deps)
        const { useRunStore } = await import('./runStore')
        const runStore = useRunStore.getState()
        runStore.registerRun(runId, 'single', 1, initialJobRows)
        runStore.startPolling(1000)

        return runId
      }
      return null
    } catch (err) {
      set({
        error: err instanceof Error ? err.message : String(err),
        state: 'failed',
      })
      return null
    }
  },

  queueJob: () => {
    const { job, inputMode, directory, localFiles, remoteFileIds } = get()

    // Deep-copy to create an immutable snapshot (prevents mutation from subsequent UI edits)
    const inputSnapshot = structuredClone({
      job: job as unknown as wailsapp.JobSpecDTO,
      inputMode,
      directory,
      localFiles,
      remoteFileIds,
    }) as wailsapp.SingleJobInputDTO

    // Dynamically import to avoid circular deps
    import('./runStore').then(({ useRunStore }) => {
      const runStore = useRunStore.getState()
      runStore.setQueuedJob({
        runType: 'single',
        input: inputSnapshot,
        queuedAt: Date.now(),
      })
      runStore.setQueueStatus('queued')
    })
  },

  reset: () => {
    set({
      state: 'initial',
      job: { ...DEFAULT_JOB_TEMPLATE },
      inputMode: 'directory',
      directory: '',
      localFiles: [],
      remoteFileIds: [],
      error: null,
      submittedJobId: null,
      fileInfoMap: {},
      showTemplateBuilder: false,
      showLoadMenu: false,
      showSaveMenu: false,
      showRemoteFilePicker: false,
    })
  },
}))
