import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useVirtualizer } from '@tanstack/react-virtual'
import { FolderIcon, DocumentIcon, ArrowUpIcon, ArrowDownIcon } from '@heroicons/react/24/outline'
import clsx from 'clsx'
import { wailsapp } from '../../../wailsjs/go/models'

// Sort configuration
export type SortField = 'name' | 'size' | 'modTime'
export type SortDirection = 'asc' | 'desc'

// Pagination constants (matching Fyne GUI behavior)
const LOCAL_DEFAULT_PAGE_SIZE = 200
const LOCAL_MAX_PAGE_SIZE = 1000
const REMOTE_DEFAULT_PAGE_SIZE = 25
const REMOTE_MAX_PAGE_SIZE = 200
const MIN_PAGE_SIZE = 10

interface FileListProps {
  items: wailsapp.FileItemDTO[]
  selectedIds: Set<string>
  lastSelectedId?: string | null // v4.0.0: Track last selected for correct range selection
  onSelectionChange: (ids: Set<string>, lastId: string | null) => void
  onFolderOpen: (item: wailsapp.FileItemDTO) => void
  isLoading?: boolean
  error?: string | null
  emptyMessage?: string
  loadingMessage?: string // Custom loading message (e.g., for Legacy mode)
  showPath?: boolean // Show full path instead of just name
  isLocal?: boolean // v4.0.0: Local browser uses different pagination defaults
  // v4.0.3: Server-side pagination props (for remote browser)
  useServerPagination?: boolean  // When true, items are already one page from server
  serverCurrentPage?: number     // Current page (0-indexed)
  serverKnownTotalPages?: number // Known total pages
  serverHasMore?: boolean        // Server has more pages
  serverItemsPerPage?: number    // Current items per page setting
  onServerNextPage?: () => void  // Navigate to next page
  onServerPrevPage?: () => void  // Navigate to previous page
  onServerItemsPerPageChange?: (size: number) => void  // Change items per page
}

// Format file size for display
function formatSize(bytes: number): string {
  if (bytes < 0) return '?'
  if (bytes === 0) return '-'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const exp = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1)
  const size = bytes / Math.pow(1024, exp)
  return `${size.toFixed(exp > 0 ? 1 : 0)} ${units[exp]}`
}

// Format date for display
function formatDate(dateStr: string): string {
  if (!dateStr) return '-'
  try {
    const date = new Date(dateStr)
    return date.toLocaleDateString(undefined, {
      year: 'numeric',
      month: 'short',
      day: 'numeric',
      hour: '2-digit',
      minute: '2-digit',
    })
  } catch {
    return '-'
  }
}

// Natural sort comparator for filenames
function naturalCompare(a: string, b: string): number {
  const collator = new Intl.Collator(undefined, { numeric: true, sensitivity: 'base' })
  return collator.compare(a, b)
}

export function FileList({
  items,
  selectedIds,
  lastSelectedId,
  onSelectionChange,
  onFolderOpen,
  isLoading = false,
  error = null,
  emptyMessage = 'No files or folders',
  loadingMessage = 'Loading...',
  showPath = false,
  isLocal = false,
  // v4.0.3: Server-side pagination props
  useServerPagination = false,
  serverCurrentPage = 0,
  serverKnownTotalPages = 1,
  serverHasMore = false,
  serverItemsPerPage = REMOTE_DEFAULT_PAGE_SIZE,
  onServerNextPage,
  onServerPrevPage,
  onServerItemsPerPageChange,
}: FileListProps) {
  const parentRef = useRef<HTMLDivElement>(null)

  // v4.0.0: Use refs to prevent stale closure issues with selection state.
  // When user clicks quickly, callbacks may have stale selectedIds from previous renders.
  // By using refs, we always get the latest values regardless of when the callback was created.
  const selectedIdsRef = useRef(selectedIds)
  const lastSelectedIdRef = useRef(lastSelectedId)
  useEffect(() => {
    selectedIdsRef.current = selectedIds
  }, [selectedIds])
  useEffect(() => {
    lastSelectedIdRef.current = lastSelectedId
  }, [lastSelectedId])
  const [sortField, setSortField] = useState<SortField>('name')
  const [sortDirection, setSortDirection] = useState<SortDirection>('asc')

  // v4.0.0: Pagination state (matching Fyne GUI behavior)
  // v4.0.3: For server pagination, use server state; for local, use local state
  const maxPageSize = isLocal ? LOCAL_MAX_PAGE_SIZE : REMOTE_MAX_PAGE_SIZE

  // v4.0.0: Use functional initializer to capture isLocal at mount time
  // v4.0.3: For server pagination, initialize from server state
  const [localItemsPerPage, setLocalItemsPerPage] = useState(() =>
    useServerPagination ? serverItemsPerPage : (isLocal ? LOCAL_DEFAULT_PAGE_SIZE : REMOTE_DEFAULT_PAGE_SIZE)
  )
  const [localCurrentPage, setLocalCurrentPage] = useState(0)
  // Local state for page size input - allows free typing before applying
  const [pageSizeInput, setPageSizeInput] = useState(() =>
    String(useServerPagination ? serverItemsPerPage : (isLocal ? LOCAL_DEFAULT_PAGE_SIZE : REMOTE_DEFAULT_PAGE_SIZE))
  )

  // v4.0.3: Effective values depend on pagination mode
  const currentPage = useServerPagination ? serverCurrentPage : localCurrentPage
  const itemsPerPage = useServerPagination ? serverItemsPerPage : localItemsPerPage

  // Reset pagination when isLocal changes (e.g., switching between browsers)
  // v4.0.0: This effect ensures state updates if isLocal changes after mount
  useEffect(() => {
    if (!useServerPagination) {
      const newDefault = isLocal ? LOCAL_DEFAULT_PAGE_SIZE : REMOTE_DEFAULT_PAGE_SIZE
      setLocalItemsPerPage(newDefault)
      setPageSizeInput(String(newDefault))
      setLocalCurrentPage(0)
    }
  }, [isLocal, useServerPagination])

  // v4.0.3: Sync pageSizeInput when server items per page changes
  useEffect(() => {
    if (useServerPagination) {
      setPageSizeInput(String(serverItemsPerPage))
    }
  }, [useServerPagination, serverItemsPerPage])

  // Keep pageSizeInput in sync when itemsPerPage changes programmatically
  useEffect(() => {
    if (!useServerPagination) {
      setPageSizeInput(String(localItemsPerPage))
    }
  }, [localItemsPerPage, useServerPagination])

  // Sort items: folders first, then by sort field
  const sortedItems = useMemo(() => {
    const sorted = [...items].sort((a, b) => {
      // Folders always first
      if (a.isFolder !== b.isFolder) {
        return a.isFolder ? -1 : 1
      }

      // Then sort by field
      let cmp = 0
      switch (sortField) {
        case 'name':
          cmp = naturalCompare(a.name, b.name)
          break
        case 'size':
          cmp = (a.size ?? 0) - (b.size ?? 0)
          break
        case 'modTime':
          cmp = (a.modTime ?? '').localeCompare(b.modTime ?? '')
          break
      }

      return sortDirection === 'asc' ? cmp : -cmp
    })
    return sorted
  }, [items, sortField, sortDirection])

  // v4.0.0: Paginated items (matching Fyne GUI behavior)
  // v4.0.3: For server pagination, items are already one page; for local, slice
  const totalPages = useServerPagination
    ? serverKnownTotalPages
    : Math.max(1, Math.ceil(sortedItems.length / itemsPerPage))

  const paginatedItems = useMemo(() => {
    if (useServerPagination) {
      // v4.0.3: Server already sent one page worth of items
      return sortedItems
    }
    // Local pagination: slice the items
    const startIdx = currentPage * itemsPerPage
    const endIdx = Math.min(startIdx + itemsPerPage, sortedItems.length)
    return sortedItems.slice(startIdx, endIdx)
  }, [sortedItems, currentPage, itemsPerPage, useServerPagination])

  // Reset to first page when items change (e.g., folder navigation)
  // v4.0.3: Only for local pagination
  useEffect(() => {
    if (!useServerPagination) {
      setLocalCurrentPage(0)
    }
  }, [items, useServerPagination])

  // Ensure currentPage is valid when totalPages changes
  // v4.0.3: Only for local pagination
  useEffect(() => {
    if (!useServerPagination && localCurrentPage >= totalPages) {
      setLocalCurrentPage(Math.max(0, totalPages - 1))
    }
  }, [totalPages, localCurrentPage, useServerPagination])

  // Virtual scrolling (now uses paginated items)
  const rowVirtualizer = useVirtualizer({
    count: paginatedItems.length,
    getScrollElement: () => parentRef.current,
    estimateSize: () => 32, // Row height
    overscan: 5,
  })

  // Handle sort header click
  const handleSort = useCallback((field: SortField) => {
    if (sortField === field) {
      setSortDirection(prev => prev === 'asc' ? 'desc' : 'asc')
    } else {
      setSortField(field)
      setSortDirection('asc')
    }
  }, [sortField])

  // Handle row click with selection logic
  // v4.0.0: Use refs to prevent stale closure issues - always get latest selection state
  const handleRowClick = useCallback((e: React.MouseEvent, item: wailsapp.FileItemDTO, index: number) => {
    // Read from refs to get latest values (avoids stale closure issues on quick clicks)
    const currentSelectedIds = selectedIdsRef.current
    const currentLastSelectedId = lastSelectedIdRef.current
    const newSelection = new Set(currentSelectedIds)
    let lastId: string | null = item.id

    if (e.metaKey || e.ctrlKey) {
      // Toggle selection (Cmd/Ctrl+click)
      if (newSelection.has(item.id)) {
        newSelection.delete(item.id)
        lastId = null
      } else {
        newSelection.add(item.id)
      }
    } else if (e.shiftKey && currentSelectedIds.size > 0) {
      // v4.0.0: Range selection - use lastSelectedId from ref, not first in array
      // This fixes the random de-selection bug when selecting multiple items
      const lastSelected = currentLastSelectedId
        ? paginatedItems.find(i => i.id === currentLastSelectedId)
        : paginatedItems.find(i => currentSelectedIds.has(i.id)) // Fallback for backward compatibility
      if (lastSelected) {
        const lastIdx = paginatedItems.findIndex(i => i.id === lastSelected.id)
        const start = Math.min(lastIdx, index)
        const end = Math.max(lastIdx, index)
        for (let i = start; i <= end; i++) {
          newSelection.add(paginatedItems[i].id)
        }
      }
    } else {
      // v4.0.0: Plain click toggles the item (like clicking its checkbox)
      // Previously this cleared all selections, which was frustrating when
      // accidentally clicking rows between checkbox clicks.
      if (newSelection.has(item.id)) {
        newSelection.delete(item.id)
        lastId = null
      } else {
        newSelection.add(item.id)
      }
    }

    onSelectionChange(newSelection, lastId)
  }, [paginatedItems, onSelectionChange]) // v4.0.0: Removed selectedIds/lastSelectedId - using refs now

  // Handle checkbox click - separate from row click for multi-select
  // v4.0.0: Use ref to prevent stale closure issues
  const handleCheckboxChange = useCallback((item: wailsapp.FileItemDTO, checked: boolean) => {
    const currentSelectedIds = selectedIdsRef.current
    const newSelection = new Set(currentSelectedIds)
    if (checked) {
      newSelection.add(item.id)
    } else {
      newSelection.delete(item.id)
    }
    onSelectionChange(newSelection, checked ? item.id : null)
  }, [onSelectionChange]) // v4.0.0: Removed selectedIds - using ref now

  // Handle double click to open folder
  const handleRowDoubleClick = useCallback((item: wailsapp.FileItemDTO) => {
    if (item.isFolder) {
      onFolderOpen(item)
    }
  }, [onFolderOpen])

  // Apply page size from input (on blur or Enter)
  // v4.0.3: For server pagination, call the callback; for local, update local state
  const applyPageSize = useCallback(() => {
    const parsed = parseInt(pageSizeInput, 10)
    let clampedSize: number
    if (isNaN(parsed) || parsed < MIN_PAGE_SIZE) {
      clampedSize = MIN_PAGE_SIZE
    } else if (parsed > maxPageSize) {
      clampedSize = maxPageSize
    } else {
      clampedSize = parsed
    }
    setPageSizeInput(String(clampedSize))

    if (useServerPagination && onServerItemsPerPageChange) {
      // v4.0.3: Server pagination - notify parent
      onServerItemsPerPageChange(clampedSize)
    } else {
      // Local pagination
      setLocalItemsPerPage(clampedSize)
      setLocalCurrentPage(0)
    }
  }, [pageSizeInput, maxPageSize, useServerPagination, onServerItemsPerPageChange])

  // Handle page size input key press
  const handlePageSizeKeyDown = useCallback((e: React.KeyboardEvent) => {
    if (e.key === 'Enter') {
      applyPageSize()
      ;(e.target as HTMLInputElement).blur()
    }
  }, [applyPageSize])

  // Handle page navigation
  // v4.0.3: For server pagination, call callbacks; for local, update local state
  const goToPrevPage = useCallback(() => {
    if (useServerPagination && onServerPrevPage) {
      onServerPrevPage()
    } else {
      setLocalCurrentPage(p => Math.max(0, p - 1))
    }
  }, [useServerPagination, onServerPrevPage])

  const goToNextPage = useCallback(() => {
    if (useServerPagination && onServerNextPage) {
      onServerNextPage()
    } else {
      setLocalCurrentPage(p => Math.min(totalPages - 1, p + 1))
    }
  }, [useServerPagination, onServerNextPage, totalPages])

  // Sort indicator
  const SortIndicator = ({ field }: { field: SortField }) => {
    if (sortField !== field) return null
    return sortDirection === 'asc'
      ? <ArrowUpIcon className="w-3 h-3 inline ml-1" />
      : <ArrowDownIcon className="w-3 h-3 inline ml-1" />
  }

  // Loading state
  if (isLoading && items.length === 0) {
    return (
      <div className="flex items-center justify-center h-full text-gray-500">
        <div className="animate-pulse">{loadingMessage}</div>
      </div>
    )
  }

  // Error state
  if (error) {
    return (
      <div className="flex items-center justify-center h-full text-red-500 p-4 text-center">
        {error}
      </div>
    )
  }

  // Empty state
  if (sortedItems.length === 0) {
    return (
      <div className="flex items-center justify-center h-full text-gray-500">
        {emptyMessage}
      </div>
    )
  }

  return (
    <div className="flex flex-col h-full border border-gray-200 dark:border-gray-700 rounded">
      {/* Header */}
      <div className="flex items-center bg-gray-50 dark:bg-gray-800 border-b border-gray-200 dark:border-gray-700 text-sm font-medium text-gray-600 dark:text-gray-300 px-2 py-1 flex-shrink-0">
        {/* Checkbox column header */}
        <span className="w-8 flex-shrink-0" />
        <button
          className="flex-1 text-left hover:text-gray-900 dark:hover:text-white cursor-pointer"
          onClick={() => handleSort('name')}
        >
          Name <SortIndicator field="name" />
        </button>
        <button
          className="w-24 text-right hover:text-gray-900 dark:hover:text-white cursor-pointer"
          onClick={() => handleSort('size')}
        >
          Size <SortIndicator field="size" />
        </button>
        <button
          className="w-48 text-right hover:text-gray-900 dark:hover:text-white cursor-pointer"
          onClick={() => handleSort('modTime')}
        >
          Modified <SortIndicator field="modTime" />
        </button>
      </div>

      {/* Virtual scrolling list */}
      <div ref={parentRef} className="flex-1 overflow-auto">
        <div
          style={{
            height: `${rowVirtualizer.getTotalSize()}px`,
            width: '100%',
            position: 'relative',
          }}
        >
          {rowVirtualizer.getVirtualItems().map((virtualRow) => {
            const item = paginatedItems[virtualRow.index]
            const isSelected = selectedIds.has(item.id)

            return (
              <div
                key={item.id}
                className={clsx(
                  'absolute top-0 left-0 w-full flex items-center px-2 py-1 cursor-pointer text-sm',
                  'hover:bg-blue-50 dark:hover:bg-blue-900/30',
                  isSelected && 'bg-blue-100 dark:bg-blue-800/50'
                )}
                style={{
                  height: `${virtualRow.size}px`,
                  transform: `translateY(${virtualRow.start}px)`,
                }}
                onClick={(e) => handleRowClick(e, item, virtualRow.index)}
                onDoubleClick={() => handleRowDoubleClick(item)}
              >
                {/* Checkbox */}
                <span className="w-8 flex-shrink-0 flex items-center justify-center">
                  <input
                    type="checkbox"
                    checked={isSelected}
                    onChange={(e) => {
                      e.stopPropagation()
                      handleCheckboxChange(item, e.target.checked)
                    }}
                    onClick={(e) => e.stopPropagation()}
                    className="h-4 w-4 rounded border border-gray-300 text-rescale-blue focus:ring-rescale-blue focus:ring-2 bg-white cursor-pointer"
                  />
                </span>

                {/* Icon */}
                <span className="w-5 h-5 mr-2 flex-shrink-0">
                  {item.isFolder ? (
                    <FolderIcon className="w-5 h-5 text-yellow-500" />
                  ) : (
                    <DocumentIcon className="w-5 h-5 text-gray-400" />
                  )}
                </span>

                {/* Name */}
                <span className="flex-1 truncate text-gray-900 dark:text-gray-100">
                  {showPath ? item.path || item.name : item.name}
                </span>

                {/* Size */}
                <span className="w-24 text-right text-gray-500 dark:text-gray-400 flex-shrink-0">
                  {item.isFolder ? '-' : formatSize(item.size ?? 0)}
                </span>

                {/* Modified - v4.0.0: Increased width to fit AM/PM */}
                <span className="w-48 text-right text-gray-500 dark:text-gray-400 flex-shrink-0 whitespace-nowrap">
                  {formatDate(item.modTime ?? '')}
                </span>
              </div>
            )
          })}
        </div>
      </div>

      {/* Footer with pagination and selection count - v4.0.0: Added pagination controls */}
      {/* v4.0.3: Updated for server-side pagination with "Page X of Y+" indicator */}
      <div className="flex items-center justify-between bg-gray-50 dark:bg-gray-800 border-t border-gray-200 dark:border-gray-700 px-2 py-1 text-xs text-gray-500 dark:text-gray-400 flex-shrink-0">
        <span>
          {sortedItems.length} item{sortedItems.length !== 1 ? 's' : ''}
        </span>

        {/* Pagination controls */}
        <div className="flex items-center gap-2">
          <button
            type="button"
            className="px-2 py-0.5 hover:bg-gray-200 dark:hover:bg-gray-700 rounded disabled:opacity-50 disabled:cursor-not-allowed"
            onClick={goToPrevPage}
            disabled={currentPage === 0 || isLoading}
            title="Previous page"
          >
            ◀
          </button>
          {/* v4.0.3: Show "Page X of Y+" where + indicates more pages may exist */}
          <span className="min-w-[5rem] text-center">
            Page {currentPage + 1} of {totalPages}{useServerPagination && serverHasMore ? '+' : ''}
          </span>
          <button
            type="button"
            className="px-2 py-0.5 hover:bg-gray-200 dark:hover:bg-gray-700 rounded disabled:opacity-50 disabled:cursor-not-allowed"
            onClick={goToNextPage}
            disabled={
              isLoading ||
              (useServerPagination
                ? !serverHasMore  // v4.0.3: Server pagination - disabled when no more pages
                : currentPage >= totalPages - 1)  // Local pagination
            }
            title="Next page"
          >
            ▶
          </button>
          <input
            type="text"
            inputMode="numeric"
            pattern="[0-9]*"
            value={pageSizeInput}
            onChange={(e) => setPageSizeInput(e.target.value)}
            onBlur={applyPageSize}
            onKeyDown={handlePageSizeKeyDown}
            className="w-14 text-center bg-white dark:bg-gray-700 border border-gray-300 dark:border-gray-600 rounded px-1"
            title={`Items per page (${MIN_PAGE_SIZE}-${maxPageSize})`}
          />
        </div>

        <div className="flex items-center gap-2">
          {selectedIds.size > 0 && (
            <span>{selectedIds.size} selected</span>
          )}
          {isLoading && <span className="animate-pulse">Loading...</span>}
        </div>
      </div>
    </div>
  )
}
