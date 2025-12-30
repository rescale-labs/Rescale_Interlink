import { useState, useEffect, useCallback } from 'react'
import {
  ArrowLeftIcon,
  ArrowPathIcon,
  ChevronRightIcon,
  XMarkIcon,
} from '@heroicons/react/24/outline'
import clsx from 'clsx'
import { FileList } from './FileList'
import * as App from '../../../wailsjs/go/wailsapp/App'
import { wailsapp } from '../../../wailsjs/go/models'

// Browse mode for remote browser
type BrowseMode = 'library' | 'jobs' | 'legacy'

// Breadcrumb entry for navigation path
interface BreadcrumbEntry {
  id: string
  name: string
}

interface RemoteFilePickerProps {
  isOpen: boolean
  onClose: () => void
  onSelect: (fileIds: string[]) => void
  title?: string
}

/**
 * RemoteFilePicker is a self-contained dialog for browsing and selecting
 * remote files from Rescale. Unlike RemoteBrowser which uses the global
 * fileBrowserStore, this component maintains its own internal state to
 * avoid interfering with the main FileBrowserTab.
 */
export function RemoteFilePicker({
  isOpen,
  onClose,
  onSelect,
  title = 'Select Remote Files',
}: RemoteFilePickerProps) {
  // Internal state - completely independent from fileBrowserStore
  const [mode, setMode] = useState<BrowseMode>('library')
  const [items, setItems] = useState<wailsapp.FileItemDTO[]>([])
  const [isLoading, setIsLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [breadcrumb, setBreadcrumb] = useState<BreadcrumbEntry[]>([])
  // hasMore state removed - pagination not currently implemented in UI
  const [myLibraryId, setMyLibraryId] = useState<string | null>(null)
  const [myJobsId, setMyJobsId] = useState<string | null>(null)
  const [selectedIds, setSelectedIds] = useState<Set<string>>(new Set())
  const [currentFolderId, setCurrentFolderId] = useState('')

  // Initialize root folder IDs on first open
  useEffect(() => {
    if (isOpen && !myLibraryId && !myJobsId) {
      initRemote()
    }
  }, [isOpen, myLibraryId, myJobsId])

  // Reset selection when dialog opens
  useEffect(() => {
    if (isOpen) {
      setSelectedIds(new Set())
    }
  }, [isOpen])

  // Initialize remote browser - get root folder IDs
  const initRemote = useCallback(async () => {
    setIsLoading(true)
    setError(null)

    try {
      const [libId, jobsId] = await Promise.all([
        App.GetMyLibraryFolderID(),
        App.GetMyJobsFolderID(),
      ])

      setMyLibraryId(libId)
      setMyJobsId(jobsId)

      // Load initial folder based on mode
      if (mode === 'legacy') {
        await loadLegacy()
      } else {
        const folderId = mode === 'library' ? libId : jobsId
        const folderName = mode === 'library' ? 'My Library' : 'My Jobs'
        await loadFolder(folderId, folderName)
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
      setIsLoading(false)
    }
  }, [mode])

  // Load a specific folder
  const loadFolder = useCallback(async (folderId: string, folderName?: string) => {
    if (!folderId) return

    setIsLoading(true)
    setError(null)

    try {
      const contents = await App.ListRemoteFolder(folderId)

      // Update breadcrumb
      if (folderName !== undefined) {
        setBreadcrumb(prev => {
          const existingIndex = prev.findIndex(b => b.id === folderId)
          if (existingIndex >= 0) {
            return prev.slice(0, existingIndex + 1)
          }
          return [...prev, { id: folderId, name: folderName }]
        })
      }

      setCurrentFolderId(folderId)
      setItems(contents.items)
      setIsLoading(false)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
      setIsLoading(false)
    }
  }, [])

  // Load legacy files
  const loadLegacy = useCallback(async (cursor?: string) => {
    setIsLoading(true)
    setError(null)

    try {
      const contents = await App.ListRemoteLegacy(cursor ?? '')

      const existingItems = cursor ? items : []

      setCurrentFolderId('')
      setItems([...existingItems, ...contents.items])
      setBreadcrumb([{ id: '', name: 'Legacy Files' }])
      setIsLoading(false)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
      setIsLoading(false)
    }
  }, [items])

  // Handle mode change
  const handleModeChange = useCallback((newMode: BrowseMode) => {
    if (newMode === mode) return

    setMode(newMode)
    setItems([])
    setSelectedIds(new Set())
    setBreadcrumb([])

    if (newMode === 'legacy') {
      loadLegacy()
    } else {
      const folderId = newMode === 'library' ? myLibraryId : myJobsId
      const folderName = newMode === 'library' ? 'My Library' : 'My Jobs'
      if (folderId) {
        loadFolder(folderId, folderName)
      }
    }
  }, [mode, myLibraryId, myJobsId, loadFolder, loadLegacy])

  // Handle folder navigation
  const handleFolderOpen = useCallback((item: wailsapp.FileItemDTO) => {
    if (item.isFolder && item.id) {
      setSelectedIds(new Set())
      loadFolder(item.id, item.name)
    }
  }, [loadFolder])

  // Handle breadcrumb navigation
  const handleBreadcrumbClick = useCallback((index: number) => {
    if (index < 0 || index >= breadcrumb.length) return

    const target = breadcrumb[index]
    setBreadcrumb(prev => prev.slice(0, index + 1))
    setSelectedIds(new Set())
    loadFolder(target.id, undefined)
  }, [breadcrumb, loadFolder])

  // Handle back navigation
  const handleGoBack = useCallback(() => {
    if (breadcrumb.length <= 1) return
    handleBreadcrumbClick(breadcrumb.length - 2)
  }, [breadcrumb.length, handleBreadcrumbClick])

  // Handle refresh
  const handleRefresh = useCallback(() => {
    if (mode === 'legacy') {
      setItems([])
      loadLegacy()
    } else {
      loadFolder(currentFolderId, undefined)
    }
  }, [mode, currentFolderId, loadFolder, loadLegacy])

  // Handle selection change from FileList
  const handleSelectionChange = useCallback((ids: Set<string>, _lastId: string | null) => {
    setSelectedIds(ids)
  }, [])

  // Handle confirm selection
  const handleConfirm = useCallback(() => {
    // Filter to only files (not folders) and extract IDs
    const selectedFileIds = items
      .filter(item => selectedIds.has(item.id) && !item.isFolder)
      .map(item => item.id)

    if (selectedFileIds.length === 0) {
      setError('Please select at least one file (not a folder)')
      return
    }

    onSelect(selectedFileIds)
    onClose()
  }, [items, selectedIds, onSelect, onClose])

  // Count selected files (not folders)
  const selectedFileCount = items.filter(
    item => selectedIds.has(item.id) && !item.isFolder
  ).length

  const canGoBack = breadcrumb.length > 1

  if (!isOpen) return null

  return (
    <div className="fixed inset-0 bg-black/50 flex items-center justify-center z-50">
      <div className="bg-white dark:bg-gray-800 rounded-lg shadow-xl w-[800px] max-w-[90vw] h-[600px] max-h-[80vh] flex flex-col">
        {/* Header */}
        <div className="flex items-center justify-between px-4 py-3 border-b border-gray-200 dark:border-gray-700">
          <h2 className="text-lg font-semibold text-gray-900 dark:text-gray-100">
            {title}
          </h2>
          <button
            onClick={onClose}
            className="p-1 rounded hover:bg-gray-200 dark:hover:bg-gray-700"
          >
            <XMarkIcon className="w-5 h-5" />
          </button>
        </div>

        {/* Navigation bar */}
        <div className="flex items-center gap-2 p-2 border-b border-gray-200 dark:border-gray-700 bg-gray-50 dark:bg-gray-800">
          {/* Back button */}
          <button
            onClick={handleGoBack}
            disabled={!canGoBack}
            className="p-1.5 rounded hover:bg-gray-200 dark:hover:bg-gray-700 disabled:opacity-50 disabled:cursor-not-allowed"
            title="Go up"
          >
            <ArrowLeftIcon className="w-4 h-4" />
          </button>

          {/* Mode toggle */}
          <div className="flex items-center bg-white dark:bg-gray-900 border border-gray-300 dark:border-gray-600 rounded overflow-hidden">
            {(['library', 'jobs', 'legacy'] as BrowseMode[]).map((m) => (
              <button
                key={m}
                onClick={() => handleModeChange(m)}
                className={clsx(
                  'px-3 py-1 text-xs font-medium transition-colors',
                  mode === m
                    ? 'bg-blue-500 text-white'
                    : 'text-gray-600 dark:text-gray-400 hover:bg-gray-100 dark:hover:bg-gray-800'
                )}
              >
                {m === 'library' ? 'My Library' : m === 'jobs' ? 'My Jobs' : 'Legacy'}
              </button>
            ))}
          </div>

          <div className="flex-1" />

          {/* Refresh button */}
          <button
            onClick={handleRefresh}
            disabled={isLoading}
            className="p-1.5 rounded hover:bg-gray-200 dark:hover:bg-gray-700 disabled:opacity-50"
            title="Refresh"
          >
            <ArrowPathIcon className={clsx('w-4 h-4', isLoading && 'animate-spin')} />
          </button>
        </div>

        {/* Breadcrumb */}
        {breadcrumb.length > 0 && (
          <div className="flex items-center gap-1 px-2 py-1 text-sm border-b border-gray-200 dark:border-gray-700 overflow-x-auto">
            {breadcrumb.map((entry, index) => (
              <div key={entry.id || index} className="flex items-center">
                {index > 0 && (
                  <ChevronRightIcon className="w-4 h-4 text-gray-400 mx-1 flex-shrink-0" />
                )}
                <button
                  onClick={() => handleBreadcrumbClick(index)}
                  className={clsx(
                    'px-1 py-0.5 rounded hover:bg-gray-200 dark:hover:bg-gray-700 truncate max-w-[150px]',
                    index === breadcrumb.length - 1
                      ? 'text-gray-900 dark:text-gray-100 font-medium'
                      : 'text-gray-600 dark:text-gray-400'
                  )}
                  title={entry.name}
                >
                  {entry.name}
                </button>
              </div>
            ))}
          </div>
        )}

        {/* File list */}
        <div className="flex-1 overflow-hidden p-2">
          <FileList
            items={items}
            selectedIds={selectedIds}
            onSelectionChange={handleSelectionChange}
            onFolderOpen={handleFolderOpen}
            isLoading={isLoading}
            error={error}
            emptyMessage={
              mode === 'library'
                ? 'Your library is empty'
                : mode === 'jobs'
                ? 'No job files found'
                : 'No files found'
            }
            loadingMessage={
              mode === 'legacy'
                ? 'Loading legacy files (this may take a moment)...'
                : 'Loading...'
            }
          />
        </div>


        {/* Footer with selection info and buttons */}
        <div className="flex items-center justify-between px-4 py-3 border-t border-gray-200 dark:border-gray-700 bg-gray-50 dark:bg-gray-800">
          <div className="text-sm text-gray-600 dark:text-gray-400">
            {selectedFileCount > 0 ? (
              <span>{selectedFileCount} file{selectedFileCount !== 1 ? 's' : ''} selected</span>
            ) : (
              <span>Select files to use as job inputs</span>
            )}
          </div>
          <div className="flex gap-3">
            <button
              onClick={onClose}
              className="px-4 py-2 text-sm text-gray-700 dark:text-gray-300 hover:bg-gray-200 dark:hover:bg-gray-700 rounded"
            >
              Cancel
            </button>
            <button
              onClick={handleConfirm}
              disabled={selectedFileCount === 0}
              className={clsx(
                'px-4 py-2 text-sm rounded',
                selectedFileCount > 0
                  ? 'bg-blue-500 text-white hover:bg-blue-600'
                  : 'bg-gray-300 dark:bg-gray-600 text-gray-500 cursor-not-allowed'
              )}
            >
              Use Selected ({selectedFileCount})
            </button>
          </div>
        </div>
      </div>
    </div>
  )
}
