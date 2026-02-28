import { useEffect, useCallback, useMemo, useState, memo } from 'react'
import {
  ArrowUpIcon,
  ArrowDownIcon,
  XMarkIcon,
  ArrowPathIcon,
  TrashIcon,
  CheckCircleIcon,
  ExclamationCircleIcon,
  ExclamationTriangleIcon,
  ClockIcon,
  FolderOpenIcon,
  ChevronRightIcon,
  ChevronDownIcon,
} from '@heroicons/react/24/outline'
import clsx from 'clsx'
import { useTransferStore, TransferTask, TransferBatch, Enumeration, extractDiskSpaceInfo, formatSpeed, formatETA } from '../../stores'

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

// Format number with commas
function formatNumber(n: number): string {
  return n.toLocaleString()
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

// v4.0.8: Enumeration row for folder scan progress
// v4.7.7: Enhanced with statusMessage display for folder creation phase
interface EnumerationRowProps {
  enumeration: Enumeration
}

function EnumerationRow({ enumeration }: EnumerationRowProps) {
  const isComplete = enumeration.isComplete
  const hasError = !!enumeration.error
  const statusMessage = enumeration.statusMessage
  const isCreatingFolders = statusMessage?.includes('Creating folders')

  return (
    <div className="flex items-center gap-3 px-4 py-3 border-b border-gray-200 dark:border-gray-700 bg-blue-50 dark:bg-blue-900/20">
      {/* Direction icon */}
      <div className="flex-shrink-0 w-6">
        {enumeration.direction === 'upload' ? (
          <ArrowUpIcon className="w-5 h-5 text-blue-500" />
        ) : (
          <ArrowDownIcon className="w-5 h-5 text-green-500" />
        )}
      </div>

      {/* Folder icon and name */}
      <div className="flex items-center gap-2 flex-shrink-0 w-48">
        <FolderOpenIcon className={clsx('w-5 h-5', isCreatingFolders ? 'text-blue-500' : 'text-yellow-500')} />
        <div>
          <div className="text-sm font-medium truncate" title={enumeration.folderName}>
            {enumeration.folderName.length > 25
              ? enumeration.folderName.slice(0, 22) + '...'
              : enumeration.folderName}
          </div>
          <div className="text-xs text-gray-500">
            {isCreatingFolders ? 'Creating folders...' : 'Scanning folder...'}
          </div>
        </div>
      </div>

      {/* Scanning/progress indicator */}
      <div className="flex-1 min-w-0 flex items-center gap-3">
        {!isComplete && !hasError && !statusMessage && (
          <div className="flex items-center gap-2">
            <div className="w-4 h-4 border-2 border-blue-500 border-t-transparent rounded-full animate-spin" />
            <span className="text-sm text-blue-600">Scanning...</span>
          </div>
        )}
        {!isComplete && !hasError && statusMessage && (
          <div className="flex items-center gap-2">
            {isCreatingFolders ? (
              <FolderOpenIcon className="w-4 h-4 text-blue-500" />
            ) : (
              <div className="w-4 h-4 border-2 border-blue-500 border-t-transparent rounded-full animate-spin" />
            )}
            <span className="text-sm text-blue-600">{statusMessage}</span>
          </div>
        )}
        {isComplete && !hasError && (
          <div className="flex items-center gap-2">
            <CheckCircleIcon className="w-4 h-4 text-green-500" />
            <span className="text-sm text-green-600">Complete</span>
          </div>
        )}
        {hasError && (
          <div className="flex items-center gap-2">
            <ExclamationCircleIcon className="w-4 h-4 text-red-500" />
            <span className="text-sm text-red-600">{enumeration.error}</span>
          </div>
        )}
      </div>

      {/* Counts */}
      <div className="flex-shrink-0 w-40 text-right text-sm text-gray-600">
        <span className="font-medium">{enumeration.filesFound}</span> files,
        <span className="font-medium ml-1">{enumeration.foldersFound}</span> folders
        {enumeration.bytesFound > 0 && (
          <div className="text-xs text-gray-500">
            {formatSize(enumeration.bytesFound)}
          </div>
        )}
      </div>

      {/* Spacer for action button area */}
      <div className="flex-shrink-0 w-20" />
    </div>
  )
}

// v4.7.7: Batch row for grouped transfer display
interface BatchRowProps {
  batch: TransferBatch
  isExpanded: boolean
  expandedTasks: TransferTask[]
  onToggle: () => void
  onCancel: () => void
  onRetryFailed: () => void
  onLoadMore: () => void
  onCancelTask: (taskId: string) => void
  onRetryTask: (taskId: string) => void
}

const BatchRow = memo(function BatchRow({
  batch, isExpanded, expandedTasks, onToggle, onCancel, onRetryFailed, onLoadMore, onCancelTask, onRetryTask
}: BatchRowProps) {
  const isActive = batch.queued > 0 || batch.active > 0
  const isAllComplete = batch.completed === batch.total
  const hasFailed = batch.failed > 0
  const isFileBrowser = batch.sourceLabel === 'FileBrowser'

  // Compute ETA from speed and remaining bytes
  const remainingBytes = batch.totalBytes * (1 - batch.progress)
  const etaFormatted = batch.speed > 0 ? formatETA((remainingBytes / batch.speed) * 1000) : ''
  const speedFormatted = formatSpeed(batch.speed)

  // Status color for progress bar
  const barColor = isAllComplete ? 'bg-green-500' :
    hasFailed && !isActive ? 'bg-red-500' :
    'bg-blue-500'

  return (
    <>
      <div
        className={clsx(
          'flex items-center gap-3 px-4 py-3 border-b border-gray-200 dark:border-gray-700 cursor-pointer',
          'hover:bg-gray-50 dark:hover:bg-gray-800/50',
          isExpanded && 'bg-gray-50 dark:bg-gray-800/30'
        )}
        onClick={onToggle}
      >
        {/* Expand/collapse chevron */}
        <div className="flex-shrink-0 w-6">
          {isExpanded ? (
            <ChevronDownIcon className="w-4 h-4 text-gray-400" />
          ) : (
            <ChevronRightIcon className="w-4 h-4 text-gray-400" />
          )}
        </div>

        {/* Folder icon and batch label */}
        <div className="flex items-center gap-2 flex-shrink-0 w-48">
          <FolderOpenIcon className="w-5 h-5 text-yellow-500" />
          <div>
            <div className="flex items-center gap-1.5">
              <span className="text-sm font-medium truncate" title={batch.batchLabel}>
                {batch.batchLabel.length > 22 ? batch.batchLabel.slice(0, 19) + '...' : batch.batchLabel}
              </span>
              {batch.direction === 'upload' ? (
                <ArrowUpIcon className="w-3 h-3 text-blue-500 flex-shrink-0" />
              ) : (
                <ArrowDownIcon className="w-3 h-3 text-green-500 flex-shrink-0" />
              )}
            </div>
            <div className="text-xs text-gray-500">
              {formatNumber(batch.completed)} / {formatNumber(batch.total)} files
              {batch.totalBytes > 0 && ` — ${formatSize(batch.totalBytes)}`}
            </div>
          </div>
        </div>

        {/* Progress bar */}
        <div className="flex-1 min-w-0">
          <div className="relative h-2 bg-gray-200 dark:bg-gray-700 rounded-full overflow-hidden">
            <div
              className={clsx('absolute top-0 left-0 h-full rounded-full transition-all duration-300', barColor)}
              style={{ width: `${batch.progress * 100}%` }}
            />
          </div>
          <div className="flex justify-between mt-1 text-xs text-gray-500">
            <span>{Math.round(batch.progress * 100)}%</span>
            {speedFormatted && <span>{speedFormatted}</span>}
            {etaFormatted && <span>ETA: {etaFormatted}</span>}
          </div>
        </div>

        {/* Status */}
        <div className="flex items-center gap-1 flex-shrink-0 w-32 text-sm">
          {isActive && (
            <span className="text-blue-500 flex items-center gap-1">
              <ArrowPathIcon className="w-4 h-4 animate-spin" />
              {batch.active} active
            </span>
          )}
          {isAllComplete && (
            <span className="text-green-500 flex items-center gap-1">
              <CheckCircleIcon className="w-4 h-4" />
              Complete
            </span>
          )}
          {hasFailed && !isActive && !isAllComplete && (
            <span className="text-red-500 flex items-center gap-1">
              <ExclamationCircleIcon className="w-4 h-4" />
              {batch.failed} failed
            </span>
          )}
        </div>

        {/* Action buttons — only for FileBrowser source batches */}
        <div className="flex-shrink-0 w-20" onClick={e => e.stopPropagation()}>
          {isActive && isFileBrowser && (
            <button
              onClick={onCancel}
              className="flex items-center gap-1 px-2 py-1 text-xs text-red-600 hover:bg-red-100 dark:hover:bg-red-900/30 rounded"
            >
              <XMarkIcon className="w-4 h-4" />
              Cancel
            </button>
          )}
          {hasFailed && !isActive && isFileBrowser && (
            <button
              onClick={onRetryFailed}
              className="flex items-center gap-1 px-2 py-1 text-xs text-blue-600 hover:bg-blue-100 dark:hover:bg-blue-900/30 rounded"
            >
              <ArrowPathIcon className="w-4 h-4" />
              Retry
            </button>
          )}
        </div>
      </div>

      {/* Expanded: show paginated task rows */}
      {isExpanded && (
        <div className="bg-gray-50/50 dark:bg-gray-800/20">
          {expandedTasks.map(task => (
            <TransferRow
              key={task.id}
              task={task}
              onCancel={onCancelTask}
              onRetry={onRetryTask}
              indent
            />
          ))}
          {expandedTasks.length < batch.total && (
            <div className="px-4 py-2 text-center">
              <button
                onClick={(e) => { e.stopPropagation(); onLoadMore() }}
                className="text-xs text-blue-600 hover:text-blue-800 dark:text-blue-400"
              >
                Show more ({formatNumber(batch.total - expandedTasks.length)} remaining)
              </button>
            </div>
          )}
        </div>
      )}
    </>
  )
})

// v4.7.1: Short error label for status column
function getShortErrorLabel(task: TransferTask): string {
  if (!task.error) return ''
  if (task.errorType === 'disk_space') return 'No disk space'
  return task.error
}

// v4.7.1: Disk space error banner
function DiskSpaceBanner({ incident, onDismiss }: {
  incident: { count: number; available: string; needed: string }
  onDismiss: () => void
}) {
  return (
    <div className="flex items-center gap-3 px-4 py-3 bg-amber-50 dark:bg-amber-900/20 border-b border-amber-200 dark:border-amber-800">
      <ExclamationTriangleIcon className="w-5 h-5 text-amber-600 flex-shrink-0" />
      <div className="flex-1 text-sm text-amber-800 dark:text-amber-300">
        <span className="font-medium">
          {incident.count} download{incident.count !== 1 ? 's' : ''} failed: insufficient disk space.
        </span>
        {' '}
        <span>{incident.available} available (latest failure needs {incident.needed}).</span>
      </div>
      <button onClick={onDismiss}
        className="flex-shrink-0 text-amber-600 hover:text-amber-800 dark:hover:text-amber-200"
        title="Dismiss">
        <XMarkIcon className="w-4 h-4" />
      </button>
    </div>
  )
}

interface TransferRowProps {
  task: TransferTask
  onCancel: (taskId: string) => void
  onRetry: (taskId: string) => void
  indent?: boolean // v4.7.7: Indent for expanded batch children
}

const TransferRow = memo(function TransferRow({ task, onCancel, onRetry, indent }: TransferRowProps) {
  const statusInfo = getStatusInfo(task.state)
  const StatusIcon = statusInfo.icon
  const isActive = ['queued', 'initializing', 'active', 'paused'].includes(task.state)
  const canRetry = ['failed', 'cancelled'].includes(task.state)

  // Truncate name if too long
  const displayName = task.name.length > 30 ? task.name.slice(0, 27) + '...' : task.name

  return (
    <div className={clsx(
      'flex items-center gap-3 px-4 py-3 border-b border-gray-200 dark:border-gray-700 hover:bg-gray-50 dark:hover:bg-gray-800/50',
      indent && 'pl-12' // v4.7.7: Extra padding for batch children
    )}>
      {/* Direction icon */}
      <div className="flex-shrink-0 w-6">
        {task.type === 'upload' ? (
          <ArrowUpIcon className="w-5 h-5 text-blue-500" />
        ) : (
          <ArrowDownIcon className="w-5 h-5 text-green-500" />
        )}
      </div>

      {/* Name, source label, and size */}
      <div className="flex-shrink-0 w-48">
        <div className="flex items-center gap-1.5">
          <span className="text-sm font-medium truncate" title={task.name}>
            {displayName}
          </span>
          {/* v4.7.4: Source label badge */}
          {task.sourceLabel === 'PUR' && (
            <span className="text-[10px] font-medium px-1.5 py-0.5 rounded bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-400 flex-shrink-0">PUR</span>
          )}
          {task.sourceLabel === 'SingleJob' && (
            <span className="text-[10px] font-medium px-1.5 py-0.5 rounded bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-400 flex-shrink-0">Job</span>
          )}
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
      <div className={clsx('flex items-center gap-1 flex-shrink-0 w-32 text-sm', statusInfo.color)}>
        <StatusIcon className={clsx('w-4 h-4 flex-shrink-0', task.state === 'active' && 'animate-spin')} />
        <span className="truncate" title={task.error || statusInfo.label}>
          {task.error ? getShortErrorLabel(task) : statusInfo.label}
        </span>
      </div>

      {/* v4.7.4: Action buttons — only for FileBrowser tasks (pipeline manages its own retry/cancel) */}
      <div className="flex-shrink-0 w-20">
        {isActive && (!task.sourceLabel || task.sourceLabel === 'FileBrowser') && (
          <button
            onClick={() => onCancel(task.id)}
            className="flex items-center gap-1 px-2 py-1 text-xs text-red-600 hover:bg-red-100 dark:hover:bg-red-900/30 rounded"
          >
            <XMarkIcon className="w-4 h-4" />
            Cancel
          </button>
        )}
        {canRetry && (!task.sourceLabel || task.sourceLabel === 'FileBrowser') && (
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
})

export function TransfersTab() {
  const {
    tasks,
    stats,
    enumerations, // v4.0.8: folder scan progress
    batches, // v4.7.7: batch aggregates
    expandedBatches,
    batchTasks,
    startPolling,
    stopPolling,
    cancelTransfer,
    cancelAllTransfers,
    cancelBatch,
    retryTransfer,
    retryFailedInBatch,
    clearCompletedTransfers,
    toggleBatchExpanded,
    fetchBatchTasks,
  } = useTransferStore()

  // v4.7.1: State for disk space banner — independent of task list
  const [diskSpaceIncident, setDiskSpaceIncident] = useState<{
    count: number
    available: string
    needed: string
    fingerprint: string
  } | null>(null)
  const [diskSpaceBannerDismissed, setDiskSpaceBannerDismissed] = useState(false)
  const [lastFingerprint, setLastFingerprint] = useState('')

  // Start polling when tab is mounted
  useEffect(() => {
    startPolling()
    return () => stopPolling()
  }, [startPolling, stopPolling])

  // v4.7.1: Scan tasks for disk space errors; update incident state
  useEffect(() => {
    const failedDiskSpace = tasks.filter(
      t => t.state === 'failed' && t.errorType === 'disk_space' && t.type === 'download'
    )
    if (failedDiskSpace.length === 0) return // Don't clear incident — it persists after clear

    // Extract info from most recent error
    let info: { available: string; needed: string } | null = null
    for (let i = failedDiskSpace.length - 1; i >= 0; i--) {
      info = extractDiskSpaceInfo(failedDiskSpace[i].error || '')
      if (info) break
    }

    // Fingerprint: use task ID + completedAt, which changes on each failure attempt
    const sorted = [...failedDiskSpace].sort((a, b) =>
      (b.completedAt || '').localeCompare(a.completedAt || '')
    )
    const latest = sorted[0]
    const fingerprint = `${latest.id}:${latest.completedAt || Date.now()}`

    setDiskSpaceIncident({
      count: failedDiskSpace.length,
      available: info?.available || 'unknown',
      needed: info?.needed || 'unknown',
      fingerprint,
    })

    // Re-show banner when fingerprint changes (new failure, even after clear)
    if (fingerprint !== lastFingerprint) {
      setDiskSpaceBannerDismissed(false)
      setLastFingerprint(fingerprint)
    }
  }, [tasks, lastFingerprint])

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

  // Empty state - v4.0.8: also check enumerations, v4.7.7: check batches
  const isEmpty = tasks.length === 0 && enumerations.length === 0 && batches.length === 0

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
          {batches.length > 0 && (
            <span className="ml-2 text-gray-500">
              in {batches.length} batch{batches.length !== 1 ? 'es' : ''}
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

      {/* v4.7.1: Disk space error banner */}
      {diskSpaceIncident && !diskSpaceBannerDismissed && (
        <DiskSpaceBanner incident={diskSpaceIncident} onDismiss={() => setDiskSpaceBannerDismissed(true)} />
      )}

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
            {/* v4.0.8: Show enumeration rows at top */}
            {enumerations.map((enumeration) => (
              <EnumerationRow
                key={enumeration.id}
                enumeration={enumeration}
              />
            ))}

            {/* v4.7.7: Batch rows (collapsed by default) */}
            {batches.map((batch) => (
              <BatchRow
                key={batch.batchID}
                batch={batch}
                isExpanded={expandedBatches.has(batch.batchID)}
                expandedTasks={batchTasks.get(batch.batchID) || []}
                onToggle={() => toggleBatchExpanded(batch.batchID)}
                onCancel={() => cancelBatch(batch.batchID)}
                onRetryFailed={() => retryFailedInBatch(batch.batchID)}
                onLoadMore={() => {
                  const current = batchTasks.get(batch.batchID) || []
                  fetchBatchTasks(batch.batchID, current.length, 50)
                }}
                onCancelTask={handleCancel}
                onRetryTask={handleRetry}
              />
            ))}

            {/* Ungrouped transfer rows */}
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
