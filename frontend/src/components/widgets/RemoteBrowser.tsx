import { useEffect, useCallback, useState } from 'react'
import {
  ArrowLeftIcon,
  ArrowPathIcon,
  FolderPlusIcon,
  ChevronRightIcon,
} from '@heroicons/react/24/outline'
import { useFileBrowserStore, BrowseMode } from '../../stores'
import { FileList } from './FileList'

export function RemoteBrowser() {
  const {
    remote: {
      mode,
      items,
      isLoading,
      error,
      breadcrumb,
      selection,
      myLibraryId,
      myJobsId,
      hasMore,
      // v4.0.3: Server-side pagination state
      currentPage,
      itemsPerPage,
      knownTotalPages,
    },
    initRemote,
    setRemoteMode,
    navigateRemoteTo,
    navigateRemoteToBreadcrumb,
    goRemoteBack,
    refreshRemote,
    setRemoteSelection,
    createRemoteFolder,
    // v4.0.3: Server-side pagination actions
    setRemoteItemsPerPage,
    goToNextRemotePage,
    goToPreviousRemotePage,
  } = useFileBrowserStore()

  const [showNewFolderDialog, setShowNewFolderDialog] = useState(false)
  const [newFolderName, setNewFolderName] = useState('')
  const [isCreatingFolder, setIsCreatingFolder] = useState(false)

  // Initialize remote browser
  useEffect(() => {
    if (!myLibraryId && !myJobsId) {
      initRemote()
    }
  }, [myLibraryId, myJobsId, initRemote])

  // Handle mode change
  const handleModeChange = useCallback((newMode: BrowseMode) => {
    if (newMode !== mode) {
      setRemoteMode(newMode)
    }
  }, [mode, setRemoteMode])

  // Handle folder open from FileList
  const handleFolderOpen = useCallback((item: { id: string; name: string; isFolder: boolean }) => {
    if (item.isFolder && item.id) {
      navigateRemoteTo(item.id, item.name)
    }
  }, [navigateRemoteTo])

  // Handle selection change
  const handleSelectionChange = useCallback((ids: Set<string>, lastId: string | null) => {
    setRemoteSelection(ids, lastId)
  }, [setRemoteSelection])

  // Handle create folder
  const handleCreateFolder = useCallback(async () => {
    if (!newFolderName.trim()) return

    setIsCreatingFolder(true)
    try {
      await createRemoteFolder(newFolderName.trim())
      setShowNewFolderDialog(false)
      setNewFolderName('')
    } finally {
      setIsCreatingFolder(false)
    }
  }, [newFolderName, createRemoteFolder])

  // Check if we can go back
  const canGoBack = breadcrumb.length > 1

  // Check if new folder is allowed (only in My Library mode, not at root)
  const canCreateFolder = mode === 'library' && breadcrumb.length > 0

  return (
    <div className="flex flex-col h-full">
      {/* Navigation bar */}
      <div className="flex items-center gap-2 p-2 border-b border-gray-200 dark:border-gray-700 bg-gray-50 dark:bg-gray-800">
        {/* Back button */}
        <button
          onClick={goRemoteBack}
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
              className={`px-3 py-1 text-xs font-medium transition-colors ${
                mode === m
                  ? 'bg-blue-500 text-white'
                  : 'text-gray-600 dark:text-gray-400 hover:bg-gray-100 dark:hover:bg-gray-800'
              }`}
            >
              {m === 'library' ? 'My Library' : m === 'jobs' ? 'My Jobs' : 'Legacy'}
            </button>
          ))}
        </div>

        {/* Spacer */}
        <div className="flex-1" />

        {/* New folder button (only in My Library) */}
        {canCreateFolder && (
          <button
            onClick={() => setShowNewFolderDialog(true)}
            className="p-1.5 rounded hover:bg-gray-200 dark:hover:bg-gray-700"
            title="Create new folder"
          >
            <FolderPlusIcon className="w-4 h-4" />
          </button>
        )}

        {/* Refresh button */}
        <button
          onClick={refreshRemote}
          disabled={isLoading}
          className="p-1.5 rounded hover:bg-gray-200 dark:hover:bg-gray-700 disabled:opacity-50"
          title="Refresh"
        >
          <ArrowPathIcon className={`w-4 h-4 ${isLoading ? 'animate-spin' : ''}`} />
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
                onClick={() => navigateRemoteToBreadcrumb(index)}
                className={`px-1 py-0.5 rounded hover:bg-gray-200 dark:hover:bg-gray-700 truncate max-w-[150px] ${
                  index === breadcrumb.length - 1
                    ? 'text-gray-900 dark:text-gray-100 font-medium'
                    : 'text-gray-600 dark:text-gray-400'
                }`}
                title={entry.name}
              >
                {entry.name}
              </button>
            </div>
          ))}
        </div>
      )}

      {/* File list */}
      <div className="flex-1 overflow-hidden">
        <FileList
          items={items}
          selectedIds={selection.selectedIds}
          lastSelectedId={selection.lastSelectedId}
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
          // v4.0.3: Server-side pagination
          useServerPagination={true}
          serverCurrentPage={currentPage}
          serverKnownTotalPages={knownTotalPages}
          serverHasMore={hasMore}
          serverItemsPerPage={itemsPerPage}
          onServerNextPage={goToNextRemotePage}
          onServerPrevPage={goToPreviousRemotePage}
          onServerItemsPerPageChange={setRemoteItemsPerPage}
        />
      </div>


      {/* New folder dialog */}
      {showNewFolderDialog && (
        <div className="fixed inset-0 bg-black/50 flex items-center justify-center z-50">
          <div className="bg-white dark:bg-gray-800 rounded-lg shadow-lg p-4 w-80">
            <h3 className="text-lg font-medium mb-4">Create New Folder</h3>
            <input
              type="text"
              value={newFolderName}
              onChange={(e) => setNewFolderName(e.target.value)}
              placeholder="Folder name"
              className="w-full px-3 py-2 border border-gray-300 dark:border-gray-600 rounded mb-4 bg-white dark:bg-gray-900 focus:outline-none focus:ring-1 focus:ring-blue-500"
              autoFocus
              onKeyDown={(e) => {
                if (e.key === 'Enter') handleCreateFolder()
                if (e.key === 'Escape') setShowNewFolderDialog(false)
              }}
            />
            <div className="flex justify-end gap-2">
              <button
                onClick={() => {
                  setShowNewFolderDialog(false)
                  setNewFolderName('')
                }}
                className="px-4 py-2 text-sm text-gray-600 dark:text-gray-400 hover:bg-gray-100 dark:hover:bg-gray-700 rounded"
              >
                Cancel
              </button>
              <button
                onClick={handleCreateFolder}
                disabled={!newFolderName.trim() || isCreatingFolder}
                className="px-4 py-2 text-sm bg-blue-500 text-white rounded hover:bg-blue-600 disabled:opacity-50 disabled:cursor-not-allowed"
              >
                {isCreatingFolder ? 'Creating...' : 'Create'}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
