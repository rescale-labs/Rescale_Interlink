import { useEffect, useCallback, useMemo } from 'react'
import {
  ArrowUpIcon,
  ArrowDownIcon,
  XMarkIcon,
  ArrowPathIcon,
  TrashIcon,
  CheckCircleIcon,
  ExclamationCircleIcon,
  ClockIcon,
} from '@heroicons/react/24/outline'
import clsx from 'clsx'
import { useTransferStore, TransferTask } from '../../stores'

// Format file size
// v4.0.5: Added defensive handling for undefined/NaN values (issue #18)
function formatSize(bytes: number): string {
  // Handle undefined, NaN, or non-finite values
  if (typeof bytes !== 'number' || !Number.isFinite(bytes) || bytes <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const exp = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1)
  const size = bytes / Math.pow(1024, exp)
  return `${size.toFixed(exp > 0 ? 1 : 0)} ${units[exp]}`
}

// Get status icon and color
function getStatusInfo(state: string): { icon: typeof CheckCircleIcon; color: string; label: string } {
  switch (state) {
    case 'queued':
      return { icon: ClockIcon, color: 'text-gray-500', label: 'Queued' }
    case 'initializing':
      return { icon: ArrowPathIcon, color: 'text-blue-500', label: 'Initializing...' }
    case 'active':
      return { icon: ArrowPathIcon, color: 'text-blue-500', label: 'Transferring' }
    case 'completed':
      return { icon: CheckCircleIcon, color: 'text-green-500', label: 'Complete' }
    case 'failed':
      return { icon: ExclamationCircleIcon, color: 'text-red-500', label: 'Failed' }
    case 'cancelled':
      return { icon: XMarkIcon, color: 'text-gray-500', label: 'Cancelled' }
    case 'paused':
      return { icon: ClockIcon, color: 'text-yellow-500', label: 'Paused' }
    default:
      return { icon: ClockIcon, color: 'text-gray-500', label: state }
  }
}

interface TransferRowProps {
  task: TransferTask
  onCancel: (taskId: string) => void
  onRetry: (taskId: string) => void
}

function TransferRow({ task, onCancel, onRetry }: TransferRowProps) {
  const statusInfo = getStatusInfo(task.state)
  const StatusIcon = statusInfo.icon
  const isActive = ['queued', 'initializing', 'active', 'paused'].includes(task.state)
  const canRetry = ['failed', 'cancelled'].includes(task.state)

  // Truncate name if too long
  const displayName = task.name.length > 30 ? task.name.slice(0, 27) + '...' : task.name

  return (
    <div className="flex items-center gap-3 px-4 py-3 border-b border-gray-200 dark:border-gray-700 hover:bg-gray-50 dark:hover:bg-gray-800/50">
      {/* Direction icon */}
      <div className="flex-shrink-0 w-6">
        {task.type === 'upload' ? (
          <ArrowUpIcon className="w-5 h-5 text-blue-500" />
        ) : (
          <ArrowDownIcon className="w-5 h-5 text-green-500" />
        )}
      </div>

      {/* Name and size */}
      <div className="flex-shrink-0 w-48">
        <div className="text-sm font-medium truncate" title={task.name}>
          {displayName}
        </div>
        <div className="text-xs text-gray-500">
          {formatSize(task.size)}
        </div>
      </div>

      {/* Progress bar */}
      <div className="flex-1 min-w-0">
        <div className="relative h-2 bg-gray-200 dark:bg-gray-700 rounded-full overflow-hidden">
          <div
            className={clsx(
              'absolute top-0 left-0 h-full rounded-full transition-all duration-300',
              task.state === 'completed' ? 'bg-green-500' :
              task.state === 'failed' ? 'bg-red-500' :
              task.state === 'cancelled' ? 'bg-gray-400' :
              'bg-blue-500'
            )}
            style={{ width: `${task.displayProgress * 100}%` }}
          />
        </div>
        <div className="flex justify-between mt-1 text-xs text-gray-500">
          <span>{Math.round(task.displayProgress * 100)}%</span>
          {task.speedFormatted && <span>{task.speedFormatted}</span>}
          {task.etaFormatted && <span>ETA: {task.etaFormatted}</span>}
        </div>
      </div>

      {/* Status */}
      <div className={clsx('flex items-center gap-1 flex-shrink-0 w-28 text-sm', statusInfo.color)}>
        <StatusIcon className={clsx('w-4 h-4', task.state === 'active' && 'animate-spin')} />
        <span className="truncate">{task.error || statusInfo.label}</span>
      </div>

      {/* Action button */}
      <div className="flex-shrink-0 w-20">
        {isActive && (
          <button
            onClick={() => onCancel(task.id)}
            className="flex items-center gap-1 px-2 py-1 text-xs text-red-600 hover:bg-red-100 dark:hover:bg-red-900/30 rounded"
          >
            <XMarkIcon className="w-4 h-4" />
            Cancel
          </button>
        )}
        {canRetry && (
          <button
            onClick={() => onRetry(task.id)}
            className="flex items-center gap-1 px-2 py-1 text-xs text-blue-600 hover:bg-blue-100 dark:hover:bg-blue-900/30 rounded"
          >
            <ArrowPathIcon className="w-4 h-4" />
            Retry
          </button>
        )}
      </div>
    </div>
  )
}

export function TransfersTab() {
  const {
    tasks,
    stats,
    startPolling,
    stopPolling,
    cancelTransfer,
    cancelAllTransfers,
    retryTransfer,
    clearCompletedTransfers,
  } = useTransferStore()

  // Start polling when tab is mounted
  useEffect(() => {
    startPolling()
    return () => stopPolling()
  }, [startPolling, stopPolling])

  // Handle cancel
  const handleCancel = useCallback((taskId: string) => {
    cancelTransfer(taskId)
  }, [cancelTransfer])

  // Handle retry
  const handleRetry = useCallback((taskId: string) => {
    retryTransfer(taskId)
  }, [retryTransfer])

  // Handle cancel all
  const handleCancelAll = useCallback(() => {
    if (stats.totalActive > 0) {
      cancelAllTransfers()
    }
  }, [stats.totalActive, cancelAllTransfers])

  // Handle clear completed
  const handleClearCompleted = useCallback(() => {
    clearCompletedTransfers()
  }, [clearCompletedTransfers])

  // Count completed/failed/cancelled for clear button
  const finishedCount = useMemo(() => {
    return stats.completed + stats.failed + stats.cancelled
  }, [stats])

  // Empty state
  const isEmpty = tasks.length === 0

  return (
    <div className="flex flex-col h-full">
      {/* Header with stats and actions */}
      <div className="flex items-center justify-between px-4 py-3 border-b border-gray-200 dark:border-gray-700 bg-gray-50 dark:bg-gray-800">
        <div className="text-sm">
          <span className="font-medium">
            {stats.total === 0 ? 'No transfers' : `${stats.total} transfer${stats.total !== 1 ? 's' : ''}`}
          </span>
          {stats.totalActive > 0 && (
            <span className="ml-2 text-blue-600">
              ({stats.totalActive} active)
            </span>
          )}
        </div>

        <div className="flex items-center gap-2">
          {stats.totalActive > 0 && (
            <button
              onClick={handleCancelAll}
              className="flex items-center gap-1 px-3 py-1.5 text-sm text-red-600 border border-red-300 dark:border-red-700 rounded hover:bg-red-50 dark:hover:bg-red-900/20"
            >
              <XMarkIcon className="w-4 h-4" />
              Cancel All
            </button>
          )}
          {finishedCount > 0 && (
            <button
              onClick={handleClearCompleted}
              className="flex items-center gap-1 px-3 py-1.5 text-sm text-gray-600 border border-gray-300 dark:border-gray-600 rounded hover:bg-gray-100 dark:hover:bg-gray-700"
            >
              <TrashIcon className="w-4 h-4" />
              Clear Completed
            </button>
          )}
        </div>
      </div>

      {/* Transfer list */}
      <div className="flex-1 overflow-auto">
        {isEmpty ? (
          <div className="flex flex-col items-center justify-center h-full text-gray-500">
            <ArrowPathIcon className="w-12 h-12 mb-4 opacity-50" />
            <p className="text-lg font-medium">No transfers in queue</p>
            <p className="text-sm mt-1">
              Use the File Browser to upload or download files.
            </p>
            <p className="text-sm">Transfers will appear here.</p>
          </div>
        ) : (
          <div>
            {tasks.map((task) => (
              <TransferRow
                key={task.id}
                task={task}
                onCancel={handleCancel}
                onRetry={handleRetry}
              />
            ))}
          </div>
        )}
      </div>

      {/* Footer with summary */}
      {!isEmpty && (
        <div className="flex items-center gap-4 px-4 py-2 text-xs text-gray-500 border-t border-gray-200 dark:border-gray-700 bg-gray-50 dark:bg-gray-800">
          {stats.queued > 0 && <span>{stats.queued} queued</span>}
          {stats.active > 0 && <span className="text-blue-600">{stats.active} active</span>}
          {stats.completed > 0 && <span className="text-green-600">{stats.completed} completed</span>}
          {stats.failed > 0 && <span className="text-red-600">{stats.failed} failed</span>}
          {stats.cancelled > 0 && <span>{stats.cancelled} cancelled</span>}
        </div>
      )}
    </div>
  )
}
