import { useEffect, useCallback, useState } from 'react'
import {
  ArrowLeftIcon,
  ArrowPathIcon,
  FolderPlusIcon,
  ChevronRightIcon,
  TrashIcon,
  MagnifyingGlassIcon,
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
      // Server-side pagination state
      currentPage,
      itemsPerPage,
      knownTotalPages,
      // Legacy Files filters
      legacyOwnerFilter,
      legacySearchQuery,
      legacySortField,
      legacySortDirection,
      librarySearchQuery,
    },
    initRemote,
    setRemoteMode,
    navigateRemoteTo,
    navigateRemoteToBreadcrumb,
    goRemoteBack,
    refreshRemote,
    setRemoteSelection,
    createRemoteFolder,
    // Server-side pagination actions
    setRemoteItemsPerPage,
    goToNextRemotePage,
    goToPreviousRemotePage,
    // Legacy Files filter actions
    setLegacyOwnerFilter,
    setLegacySearchQuery,
    setLegacySort,
    setLibrarySearchQuery,
  } = useFileBrowserStore()

  const isTrash = mode === 'trash'
  const isLegacy = mode === 'legacy'

  const [showNewFolderDialog, setShowNewFolderDialog] = useState(false)
  const [newFolderName, setNewFolderName] = useState('')
  const [isCreatingFolder, setIsCreatingFolder] = useState(false)

  // Local state for search input (debounced before applying to store)
  const [searchInputValue, setSearchInputValue] = useState(legacySearchQuery)
  const [librarySearchInputValue, setLibrarySearchInputValue] = useState(librarySearchQuery)

  // Initialize remote browser
  useEffect(() => {
    if (!myLibraryId && !myJobsId) {
      initRemote()
    }
  }, [myLibraryId, myJobsId, initRemote])

  // Debounce search input (500ms delay)
  useEffect(() => {
    const timer = setTimeout(() => {
      if (searchInputValue !== legacySearchQuery) {
        setLegacySearchQuery(searchInputValue)
      }
    }, 500)
    return () => clearTimeout(timer)
  }, [searchInputValue, legacySearchQuery, setLegacySearchQuery])

  // Sync input value when store changes externally (e.g., mode change resets it)
  useEffect(() => {
    setSearchInputValue(legacySearchQuery)
  }, [legacySearchQuery])

  // Debounce library search input (500ms delay)
  useEffect(() => {
    const timer = setTimeout(() => {
      if (librarySearchInputValue !== librarySearchQuery) {
        setLibrarySearchQuery(librarySearchInputValue)
      }
    }, 500)
    return () => clearTimeout(timer)
  }, [librarySearchInputValue, librarySearchQuery, setLibrarySearchQuery])

  // Sync library search input when store changes externally (e.g., mode change resets it)
  useEffect(() => {
    setLibrarySearchInputValue(librarySearchQuery)
  }, [librarySearchQuery])

  // Handle mode change.
  // Clear the local input mirrors first: a debounce timer already in flight
  // compares its captured input value against the store and would otherwise
  // re-apply the outgoing mode's search term after the switch.
  const handleModeChange = useCallback((newMode: BrowseMode) => {
    if (newMode !== mode) {
      setSearchInputValue('')
      setLibrarySearchInputValue('')
      setRemoteMode(newMode)
    }
  }, [mode, setRemoteMode])

  // Handle folder open from FileList.
  // In trash mode, folder-like entries are non-navigable; they represent
  // whole trashed job outputs and are treated as opaque items.
  const handleFolderOpen = useCallback((item: { id: string; name: string; isFolder: boolean }) => {
    if (isTrash) return
    if (item.isFolder && item.id) {
      navigateRemoteTo(item.id, item.name)
    }
  }, [isTrash, navigateRemoteTo])

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
      <div className="flex items-center gap-2 p-2 border-b border-gray-200 dark:border-gray-700 bg-gray-50 dark:bg-gray-800 min-w-0">
        {/* Back button */}
        <button
          onClick={goRemoteBack}
          disabled={!canGoBack}
          className="p-1.5 rounded hover:bg-gray-200 dark:hover:bg-gray-700 disabled:opacity-50 disabled:cursor-not-allowed flex-shrink-0"
          title="Go up"
        >
          <ArrowLeftIcon className="w-4 h-4" />
        </button>

        {/* Mode toggle */}
        <div className="flex items-center bg-white dark:bg-gray-900 border border-gray-300 dark:border-gray-600 rounded overflow-hidden min-w-0">
          {(['library', 'jobs', 'legacy', 'trash'] as BrowseMode[]).map((m) => (
            <button
              key={m}
              onClick={() => handleModeChange(m)}
              className={`px-3 py-1 text-xs font-medium transition-colors flex items-center gap-1 min-w-0 whitespace-nowrap ${
                mode === m
                  ? 'bg-blue-500 text-white'
                  : 'text-gray-600 dark:text-gray-400 hover:bg-gray-100 dark:hover:bg-gray-800'
              }`}
              title={m === 'library' ? 'My Library' : m === 'jobs' ? 'My Jobs' : m === 'legacy' ? 'Legacy' : 'Trash'}
            >
              {m === 'trash' && <TrashIcon className="w-3.5 h-3.5 flex-shrink-0" />}
              <span className="truncate">
                {m === 'library' ? 'My Library' : m === 'jobs' ? 'My Jobs' : m === 'legacy' ? 'Legacy' : 'Trash'}
              </span>
            </button>
          ))}
        </div>

        {/* Spacer */}
        <div className="flex-1 min-w-0" />

        {/* New folder button (only in My Library) */}
        {canCreateFolder && !isTrash && (
          <button
            onClick={() => setShowNewFolderDialog(true)}
            className="p-1.5 rounded hover:bg-gray-200 dark:hover:bg-gray-700 flex-shrink-0"
            title="Create new folder"
          >
            <FolderPlusIcon className="w-4 h-4" />
          </button>
        )}

        {/* Refresh button */}
        <button
          onClick={refreshRemote}
          disabled={isLoading}
          className="p-1.5 rounded hover:bg-gray-200 dark:hover:bg-gray-700 disabled:opacity-50 flex-shrink-0"
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

      {/* My Library / My Jobs Search Filter */}
      {(mode === 'library' || mode === 'jobs') && (
        <div className="flex items-center gap-2 px-2 py-2 border-b border-gray-200 dark:border-gray-700 bg-gray-50 dark:bg-gray-800">
          <MagnifyingGlassIcon className="w-4 h-4 text-gray-400 flex-shrink-0" />
          <input
            type="text"
            placeholder={`Search for files within "${mode === 'library' ? 'My Library' : 'My Jobs'}"`}
            value={librarySearchInputValue}
            onChange={(e) => setLibrarySearchInputValue(e.target.value)}
            className="flex-1 px-2 py-1 text-sm border border-gray-300 dark:border-gray-600 rounded bg-white dark:bg-gray-900 text-gray-900 dark:text-gray-100 placeholder-gray-400 focus:outline-none focus:ring-1 focus:ring-blue-500 min-w-0"
          />
          {librarySearchInputValue && (
            <button
              onClick={() => setLibrarySearchInputValue('')}
              className="text-xs text-gray-500 hover:text-gray-700 dark:hover:text-gray-300 flex-shrink-0"
              title="Clear search"
            >
              ✕
            </button>
          )}
        </div>
      )}

      {/* Legacy Files Filters */}
      {isLegacy && (
        <div className="flex items-center gap-2 px-2 py-2 border-b border-gray-200 dark:border-gray-700 bg-gray-50 dark:bg-gray-800">
          {/* Owner Filter Dropdown */}
          <select
            value={legacyOwnerFilter}
            onChange={(e) => setLegacyOwnerFilter(e.target.value)}
            className="px-2 py-1 text-sm border border-gray-300 dark:border-gray-600 rounded bg-white dark:bg-gray-900 text-gray-900 dark:text-gray-100 focus:outline-none focus:ring-1 focus:ring-blue-500"
          >
            <option value="0">Any owner</option>
            <option value="1">My files</option>
            <option value="2">Shared with me</option>
          </select>

          {/* Search Input */}
          <div className="flex-1 flex items-center gap-1 min-w-0">
            <MagnifyingGlassIcon className="w-4 h-4 text-gray-400 flex-shrink-0" />
            <input
              type="text"
              placeholder="Filter by name"
              value={searchInputValue}
              onChange={(e) => setSearchInputValue(e.target.value)}
              className="flex-1 px-2 py-1 text-sm border border-gray-300 dark:border-gray-600 rounded bg-white dark:bg-gray-900 text-gray-900 dark:text-gray-100 placeholder-gray-400 focus:outline-none focus:ring-1 focus:ring-blue-500 min-w-0"
            />
            {searchInputValue && (
              <button
                onClick={() => setSearchInputValue('')}
                className="text-xs text-gray-500 hover:text-gray-700 dark:hover:text-gray-300 flex-shrink-0"
                title="Clear search"
              >
                ✕
              </button>
            )}
          </div>
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
          mode={mode}
          showFileId
          emptyMessage={
            mode === 'library'
              ? 'Your library is empty'
              : mode === 'jobs'
              ? 'No job files found'
              : mode === 'trash'
              ? 'Trash is empty'
              : 'No files found'
          }
          loadingMessage={
            mode === 'legacy'
              ? 'Loading legacy files (this may take a moment)...'
              : 'Loading...'
          }
          // Server-side pagination
          useServerPagination={true}
          serverCurrentPage={currentPage}
          serverKnownTotalPages={knownTotalPages}
          serverHasMore={hasMore}
          serverItemsPerPage={itemsPerPage}
          onServerNextPage={goToNextRemotePage}
          onServerPrevPage={goToPreviousRemotePage}
          onServerItemsPerPageChange={setRemoteItemsPerPage}
          // Controlled sorting for legacy mode
          sortField={mode === 'legacy' ? (legacySortField as any) : undefined}
          sortDirection={mode === 'legacy' ? (legacySortDirection as any) : undefined}
          onSortChange={mode === 'legacy' ? setLegacySort : undefined}
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
