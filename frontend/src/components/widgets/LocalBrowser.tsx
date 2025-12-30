import { useEffect, useCallback, useState, useRef } from 'react'
import {
  ArrowLeftIcon,
  HomeIcon,
  ArrowPathIcon,
  EyeSlashIcon,
  EyeIcon,
} from '@heroicons/react/24/outline'
import { useFileBrowserStore } from '../../stores'
import { FileList } from './FileList'

export function LocalBrowser() {
  const {
    local: { currentPath, items, isLoading, error, showHidden, history, selection },
    navigateLocalTo,
    goLocalBack,
    goLocalHome,
    refreshLocal,
    toggleShowHidden,
    setLocalSelection,
  } = useFileBrowserStore()

  const [pathInput, setPathInput] = useState(currentPath)
  const pathInputRef = useRef<HTMLInputElement>(null)

  // Initialize with home directory
  useEffect(() => {
    if (!currentPath) {
      goLocalHome()
    }
  }, [currentPath, goLocalHome])

  // Sync path input with current path
  useEffect(() => {
    setPathInput(currentPath)
  }, [currentPath])

  // Handle path input submission
  const handlePathSubmit = useCallback((e: React.FormEvent) => {
    e.preventDefault()
    if (pathInput && pathInput !== currentPath) {
      navigateLocalTo(pathInput)
    }
  }, [pathInput, currentPath, navigateLocalTo])

  // Handle folder open from FileList
  const handleFolderOpen = useCallback((item: { id: string; name: string; isFolder: boolean }) => {
    if (item.isFolder && item.id) {
      navigateLocalTo(item.id)
    }
  }, [navigateLocalTo])

  // Handle selection change
  const handleSelectionChange = useCallback((ids: Set<string>, lastId: string | null) => {
    setLocalSelection(ids, lastId)
  }, [setLocalSelection])

  return (
    <div className="flex flex-col h-full">
      {/* Navigation bar */}
      <div className="flex items-center gap-2 p-2 border-b border-gray-200 dark:border-gray-700 bg-gray-50 dark:bg-gray-800">
        {/* Back button */}
        <button
          onClick={goLocalBack}
          disabled={history.length === 0}
          className="p-1.5 rounded hover:bg-gray-200 dark:hover:bg-gray-700 disabled:opacity-50 disabled:cursor-not-allowed"
          title="Go back"
        >
          <ArrowLeftIcon className="w-4 h-4" />
        </button>

        {/* Home button */}
        <button
          onClick={goLocalHome}
          className="p-1.5 rounded hover:bg-gray-200 dark:hover:bg-gray-700"
          title="Go to home directory"
        >
          <HomeIcon className="w-4 h-4" />
        </button>

        {/* Path input */}
        <form onSubmit={handlePathSubmit} className="flex-1">
          <input
            ref={pathInputRef}
            type="text"
            value={pathInput}
            onChange={(e) => setPathInput(e.target.value)}
            onBlur={() => setPathInput(currentPath)}
            className="w-full px-2 py-1 text-sm border border-gray-300 dark:border-gray-600 rounded bg-white dark:bg-gray-900 focus:outline-none focus:ring-1 focus:ring-blue-500"
            placeholder="Enter path..."
          />
        </form>

        {/* Hidden files toggle */}
        <button
          onClick={toggleShowHidden}
          className={`p-1.5 rounded hover:bg-gray-200 dark:hover:bg-gray-700 ${
            showHidden ? 'text-blue-500' : ''
          }`}
          title={showHidden ? 'Hide hidden files' : 'Show hidden files'}
        >
          {showHidden ? (
            <EyeIcon className="w-4 h-4" />
          ) : (
            <EyeSlashIcon className="w-4 h-4" />
          )}
        </button>

        {/* Refresh button */}
        <button
          onClick={refreshLocal}
          disabled={isLoading}
          className="p-1.5 rounded hover:bg-gray-200 dark:hover:bg-gray-700 disabled:opacity-50"
          title="Refresh"
        >
          <ArrowPathIcon className={`w-4 h-4 ${isLoading ? 'animate-spin' : ''}`} />
        </button>
      </div>

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
          emptyMessage="This folder is empty"
          isLocal={true}
        />
      </div>
    </div>
  )
}
