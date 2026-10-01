import { useMemo } from 'react'
import { CloudIcon, DocumentIcon, FolderIcon, XMarkIcon } from '@heroicons/react/24/outline'
import { formatSize } from '../../utils/formatSize'

// What the list shows of one entry: Single Job's FileInfo, or the
// LocalFileInfoDTO GetLocalFilesInfo returns, whose error means the entry
// cannot be read.
export interface SelectedFileInfo {
  name: string
  isDir: boolean
  size: number
  fileCount: number
  error?: string
}

interface SelectedFilesListProps {
  paths: string[]
  fileInfo: Record<string, SelectedFileInfo>
  onRemove: (index: number) => void
  onClear: () => void
}

// Selected inputs always have a byte count, so nothing to show reads as empty.
function formatFileSize(bytes: number): string {
  return formatSize(bytes, { zero: '0 B', invalid: '0 B', trimTrailingZero: true })
}

// The list of selected inputs that Single Job and PUR's Common Input Files
// share: each file with its size, each folder with its file count, remove,
// Clear All and the total. An "id:" entry is a file already on Rescale, which
// has no local size.
export function SelectedFilesList({ paths, fileInfo, onRemove, onClear }: SelectedFilesListProps) {
  const { fileCount, folderCount, totalSize } = useMemo(() => {
    let files = 0
    let folders = 0
    let total = 0
    for (const path of paths) {
      const info = fileInfo[path]
      if (info?.isDir) {
        folders++
      } else {
        files++
      }
      total += info?.size || 0
    }
    return { fileCount: files, folderCount: folders, totalSize: total }
  }, [paths, fileInfo])

  if (paths.length === 0) return null

  return (
    <div className="p-3 bg-gray-50 dark:bg-gray-800 rounded border border-gray-200 dark:border-gray-700">
      {/* Header with counts */}
      <div className="flex items-center justify-between mb-2">
        <span className="text-sm font-medium text-gray-700 dark:text-gray-300">
          {fileCount > 0 && `${fileCount} file${fileCount !== 1 ? 's' : ''}`}
          {fileCount > 0 && folderCount > 0 && ', '}
          {folderCount > 0 && `${folderCount} folder${folderCount !== 1 ? 's' : ''}`}
        </span>
        <button
          onClick={() => onClear()}
          className="text-xs text-red-500 hover:text-red-600"
        >
          Clear All
        </button>
      </div>
      {/* File/folder list with sizes */}
      <div className="space-y-1 max-h-48 overflow-y-auto">
        {paths.map((filePath, i) => {
          const info = fileInfo[filePath]
          const isDir = info?.isDir || false
          const remote = filePath.startsWith('id:')
          const name = info?.name || filePath.split('/').pop() || filePath
          return (
            <div
              key={i}
              className="flex items-center justify-between group text-sm bg-white dark:bg-gray-700 px-2 py-1.5 rounded"
            >
              <div className="flex items-center gap-2 flex-1 min-w-0">
                {remote ? (
                  <CloudIcon className="w-4 h-4 text-blue-400 flex-shrink-0" />
                ) : isDir ? (
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
                  {remote ? 'on Rescale' : info ? (
                    info.error ? 'cannot be read' : isDir
                      ? `${info.fileCount} file${info.fileCount !== 1 ? 's' : ''}`
                      : formatFileSize(info.size)
                  ) : (
                    '...'
                  )}
                </span>
                <button
                  onClick={() => onRemove(i)}
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
  )
}
