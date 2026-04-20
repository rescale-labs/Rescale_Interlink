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

// Format file size (issue #18)
function formatSize(bytes: number): string {
  // Defensive: handle undefined/NaN values
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

function FolderCheckRow({ folderName }: { folderName: string }) {
  return (
    <div className="flex items-center gap-3 px-4 py-3 border-b border-gray-200 dark:border-gray-700 bg-blue-50 dark:bg-blue-900/20">
      <div className="flex-shrink-0 w-6">
        <ArrowUpIcon className="w-5 h-5 text-blue-500" />
      </div>
      <div className="flex items-center gap-2 flex-shrink-0 w-48">
        <FolderOpenIcon className="w-5 h-5 text-yellow-500" />
        <div>
          <div className="text-sm font-medium truncate" title={folderName}>
            {folderName.length > 25 ? folderName.slice(0, 22) + '...' : folderName}
          </div>
          <div className="text-xs text-gray-500">Preparing upload...</div>
        </div>
      </div>
      <div className="flex-1 min-w-0 flex items-center gap-3">
        <div className="flex items-center gap-2">
          <div className="w-4 h-4 border-2 border-blue-500 border-t-transparent rounded-full animate-spin" />
          <span className="text-sm text-blue-600">Checking destination for conflicts...</span>
        </div>
      </div>
      <div className="flex-shrink-0 w-40" />
      <div className="flex-shrink-0 w-20" />
    </div>
  )
}

interface EnumerationRowProps {
  enumeration: Enumeration
}

function EnumerationRow({ enumeration }: EnumerationRowProps) {
  const isComplete = enumeration.isComplete
  const hasError = !!enumeration.error
  const statusMessage = enumeration.statusMessage
  // Use structured phase instead of fragile substring matching on statusMessage
  const isCreatingFolders = enumeration.phase === 'creating_folders'
  const isUpload = enumeration.direction === 'upload'

  const subtitle = isCreatingFolders
    ? 'Creating remote folders...'
    : isUpload ? 'Preparing upload...' : 'Scanning remote folder...'

  const spinnerText = isUpload ? 'Preparing...' : 'Scanning...'

  return (
    <div className="flex items-center gap-3 px-4 py-3 border-b border-gray-200 dark:border-gray-700 bg-blue-50 dark:bg-blue-900/20">
      {/* Direction icon */}
      <div className="flex-shrink-0 w-6">
        {isUpload ? (
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
            {subtitle}
          </div>
        </div>
      </div>

      {/* Scanning/progress indicator */}
      <div className="flex-1 min-w-0 flex items-center gap-3">
        {!isComplete && !hasError && !statusMessage && !isCreatingFolders && (
          <div className="flex items-center gap-2">
            <div className="w-4 h-4 border-2 border-blue-500 border-t-transparent rounded-full animate-spin" />
            <span className="text-sm text-blue-600">{spinnerText}</span>
          </div>
        )}
        {!isComplete && !hasError && isCreatingFolders && (
          <div className="flex items-center gap-2">
            <FolderOpenIcon className="w-4 h-4 text-blue-500" />
            <span className="text-sm text-blue-600">
              {enumeration.foldersTotal && enumeration.foldersTotal > 0
                ? `Creating remote folders... (${enumeration.foldersCreated ?? 0} of ${enumeration.foldersTotal})`
                : 'Creating remote folders...'}
            </span>
            {enumeration.foldersTotal && enumeration.foldersTotal > 0 && (
              <div className="w-24 h-1.5 bg-gray-200 dark:bg-gray-700 rounded-full overflow-hidden">
                <div
                  className="h-full bg-blue-500 rounded-full transition-all duration-300"
                  style={{ width: `${Math.min(100, ((enumeration.foldersCreated ?? 0) / enumeration.foldersTotal) * 100)}%` }}
                />
              </div>
            )}
          </div>
        )}
        {!isComplete && !hasError && statusMessage && !isCreatingFolders && (
          <div className="flex items-center gap-2">
            <div className="w-4 h-4 border-2 border-blue-500 border-t-transparent rounded-full animate-spin" />
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

interface BatchRowProps {
  batch: TransferBatch
  isExpanded: boolean
  expandedTasks: TransferTask[]
  statusFilter: string
  onToggle: () => void
  onCancel: () => void
  onRetryFailed: () => void
  onLoadMore: () => void
  onCancelTask: (taskId: string) => void
  onRetryTask: (taskId: string) => void
  onFilterChange: (filter: string) => void
}

const BatchRow = memo(function BatchRow({
  batch, isExpanded, expandedTasks, statusFilter, onToggle, onCancel, onRetryFailed, onLoadMore, onCancelTask, onRetryTask, onFilterChange
}: BatchRowProps) {
  const isActive = batch.queued > 0 || batch.active > 0 || !batch.totalKnown
  const isAllComplete = batch.totalKnown && batch.total > 0 && batch.completed === batch.total
  const hasFailed = batch.failed > 0
  const hasCancelled = batch.cancelled > 0
  const isPartial = !isActive && !isAllComplete && batch.completed > 0 && (hasFailed || hasCancelled)

  // Use backend-computed ETA — smoothed and handles discovered-during-scan bytes correctly
  const etaFormatted = batch.etaSeconds > 0 ? formatETA(batch.etaSeconds * 1000) : ''
  const speedFormatted = formatSpeed(batch.speed)

  const [elapsedTick, setElapsedTick] = useState(0)
  useEffect(() => {
    if (!isActive) return
    const id = setInterval(() => setElapsedTick(t => t + 1), 1000)
    return () => clearInterval(id)
  }, [isActive])

  const elapsedFormatted = useMemo(() => {
    void elapsedTick // trigger re-computation on tick
    if (!batch.startedAtUnix || batch.startedAtUnix <= 0) return ''
    const elapsed = Math.floor(Date.now() / 1000) - batch.startedAtUnix
    if (elapsed < 10) return '' // hide for first 10s
    const minutes = Math.floor(elapsed / 60)
    const seconds = elapsed % 60
    if (minutes > 0) return `${minutes}m ${seconds}s`
    return `${seconds}s`
  }, [elapsedTick, batch.startedAtUnix])

  // Status color for progress bar
  const barColor = isAllComplete ? 'bg-green-500' :
    isPartial ? 'bg-yellow-500' :
    hasFailed && !isActive ? 'bg-red-500' :
    hasCancelled && !isActive ? 'bg-gray-400' :
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
              {batch.sourceLabel === 'Daemon' && (
                <span
                  className="text-[10px] px-1 py-0.5 bg-purple-100 dark:bg-purple-900/30 text-purple-600 dark:text-purple-400 rounded flex-shrink-0"
                  title="Auto-download (daemon)"
                >
                  Daemon
                </span>
              )}
            </div>
            <div className="text-xs text-gray-500">
              {(() => {
                if (batch.totalKnown) {
                  // Use discoveredTotal as progress denominator when it's the better estimate.
                  // batch.total counts registered tasks (grows during streaming). discoveredTotal is the
                  // true file count from the scan. For progress text, always use the higher value.
                  const progressDenom = Math.max(batch.discoveredTotal, batch.total)
                  return `Completed ${formatNumber(batch.completed)} of ${formatNumber(progressDenom)} files`
                }
                // Use discoveredTotal as approximate denominator during scan
                const scanCount = Math.max(batch.discoveredTotal, batch.total)
                if (scanCount > 0) {
                  return `Completed ${formatNumber(batch.completed)} of ~${formatNumber(scanCount)} files (discovering...)`
                }
                return batch.direction === 'upload' ? 'Preparing upload...' : 'Scanning...'
              })()}
              {(() => {
                if (!batch.totalKnown && batch.totalBytes > 0) {
                  const scanBytes = Math.max(batch.discoveredBytes || 0, batch.totalBytes)
                  return ` — ~${formatSize(scanBytes)}`
                }
                if (batch.totalBytes > 0) {
                  return ` — ${formatSize(batch.totalBytes)}`
                }
                return ''
              })()}
            </div>
          </div>
        </div>

        {/* Progress bar */}
        <div className="flex-1 min-w-0">
          <div className="relative h-2 bg-gray-200 dark:bg-gray-700 rounded-full overflow-hidden">
            {batch.totalKnown ? (
              <div
                className={clsx('absolute top-0 left-0 h-full rounded-full transition-all duration-300', barColor)}
                style={{ width: `${batch.progress * 100}%` }}
              />
            ) : (
              <div className="absolute top-0 left-0 h-full w-full rounded-full bg-blue-400 animate-pulse opacity-60" />
            )}
          </div>
          {/* 3-column grid prevents layout jump when speed/ETA appear */}
          <div className="grid grid-cols-3 mt-1 text-xs text-gray-500">
            <span>
              {(() => {
                if (batch.totalKnown) {
                  // Use discoveredTotal as progress denominator (same logic as subtitle above)
                  const progressDenom = Math.max(batch.discoveredTotal, batch.total)
                  return `${Math.round(batch.progress * 100)}% — Completed ${formatNumber(batch.completed)} of ${formatNumber(progressDenom)} files`
                }
                // Use discoveredTotal during scan phase
                const scanCount = Math.max(batch.discoveredTotal, batch.total)
                if (batch.active > 0 && scanCount > 0) {
                  return `Completed ${formatNumber(batch.completed)} of ~${formatNumber(scanCount)} files`
                }
                if (scanCount > 0) {
                  return `~${formatNumber(scanCount)} files found`
                }
                return batch.direction === 'upload' ? 'Preparing...' : 'Scanning...'
              })()}
            </span>
            <span className="text-center">
              {speedFormatted || (batch.filesPerSec > 0 ? `${batch.filesPerSec.toFixed(1)} files/s` : '')}
            </span>
            <span className="text-right">
              {(() => {
                const parts: string[] = []
                if (isActive && elapsedFormatted) {
                  parts.push(elapsedFormatted)
                }
                if (batch.totalKnown && etaFormatted) {
                  parts.push(`ETA ${etaFormatted}`)
                }
                return parts.join(' | ')
              })()}
            </span>
          </div>
        </div>

        {/* Status */}
        <div className="flex items-center gap-1 flex-shrink-0 w-32 text-sm">
          {isActive && (
            <span className="text-blue-500 flex items-center gap-1">
              <ArrowPathIcon className="w-4 h-4 animate-spin" />
              {batch.active} {batch.direction === 'download' ? 'downloading' : 'uploading'}{batch.queued > 0 ? `, ${batch.queued.toLocaleString()} queued` : ''}
            </span>
          )}
          {isAllComplete && (
            <span className="text-green-500 flex items-center gap-1">
              <CheckCircleIcon className="w-4 h-4" />
              Complete
            </span>
          )}
          {isPartial && (
            <span className="text-yellow-600 dark:text-yellow-400 flex items-center gap-1">
              <ExclamationTriangleIcon className="w-4 h-4" />
              {batch.completed} done{hasFailed ? `, ${batch.failed} failed` : ''}{hasCancelled ? `, ${batch.cancelled} cancelled` : ''}
            </span>
          )}
          {hasFailed && !isActive && !isAllComplete && !isPartial && (
            <span className="text-red-500 flex items-center gap-1">
              <ExclamationCircleIcon className="w-4 h-4" />
              {batch.failed} failed
            </span>
          )}
          {hasCancelled && !isActive && !isAllComplete && !isPartial && !hasFailed && (
            <span className="text-gray-500 flex items-center gap-1">
              <XMarkIcon className="w-4 h-4" />
              Cancelled
            </span>
          )}
        </div>

        {/* Action buttons */}
        <div className="flex-shrink-0 w-20" onClick={e => e.stopPropagation()}>
          {isActive && (
            <button
              onClick={onCancel}
              className="flex items-center gap-1 px-2 py-1 text-xs text-red-600 hover:bg-red-100 dark:hover:bg-red-900/30 rounded"
            >
              <XMarkIcon className="w-4 h-4" />
              Cancel
            </button>
          )}
          {hasFailed && !isActive && (
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

      {/* Expanded: show filter chips + paginated task rows */}
      {isExpanded && (
        <div className="bg-gray-50/50 dark:bg-gray-800/20">
          {batch.total > 0 && (
            <div className="flex items-center gap-1.5 px-4 py-2 border-b border-gray-100 dark:border-gray-700/50" onClick={e => e.stopPropagation()}>
              <span className="text-xs text-gray-500 mr-1">Filter:</span>
              {/* All */}
              <button
                onClick={() => onFilterChange('')}
                className={clsx(
                  'px-2 py-0.5 text-xs rounded-full border transition-colors',
                  !statusFilter
                    ? 'bg-blue-100 dark:bg-blue-900/40 text-blue-700 dark:text-blue-300 border-blue-300 dark:border-blue-700'
                    : 'text-gray-600 dark:text-gray-400 border-gray-300 dark:border-gray-600 hover:bg-gray-100 dark:hover:bg-gray-700'
                )}
              >
                All ({formatNumber(batch.total)})
              </button>
              {batch.active > 0 && (
                <button
                  onClick={() => onFilterChange('inprogress')}
                  className={clsx(
                    'px-2 py-0.5 text-xs rounded-full border transition-colors',
                    statusFilter === 'inprogress'
                      ? 'bg-blue-100 dark:bg-blue-900/40 text-blue-700 dark:text-blue-300 border-blue-300 dark:border-blue-700'
                      : 'text-gray-600 dark:text-gray-400 border-gray-300 dark:border-gray-600 hover:bg-gray-100 dark:hover:bg-gray-700'
                  )}
                >
                  In Progress ({formatNumber(batch.active)})
                </button>
              )}
              {batch.queued > 0 && (
                <button
                  onClick={() => onFilterChange('queued')}
                  className={clsx(
                    'px-2 py-0.5 text-xs rounded-full border transition-colors',
                    statusFilter === 'queued'
                      ? 'bg-blue-100 dark:bg-blue-900/40 text-blue-700 dark:text-blue-300 border-blue-300 dark:border-blue-700'
                      : 'text-gray-600 dark:text-gray-400 border-gray-300 dark:border-gray-600 hover:bg-gray-100 dark:hover:bg-gray-700'
                  )}
                >
                  Queued ({formatNumber(batch.queued)})
                </button>
              )}
              {/* Succeeded */}
              {batch.completed > 0 && (
                <button
                  onClick={() => onFilterChange('completed')}
                  className={clsx(
                    'px-2 py-0.5 text-xs rounded-full border transition-colors',
                    statusFilter === 'completed'
                      ? 'bg-green-100 dark:bg-green-900/40 text-green-700 dark:text-green-300 border-green-300 dark:border-green-700'
                      : 'text-gray-600 dark:text-gray-400 border-gray-300 dark:border-gray-600 hover:bg-gray-100 dark:hover:bg-gray-700'
                  )}
                >
                  Succeeded ({formatNumber(batch.completed)})
                </button>
              )}
              {/* Failed */}
              {batch.failed > 0 && (
                <button
                  onClick={() => onFilterChange('failed')}
                  className={clsx(
                    'px-2 py-0.5 text-xs rounded-full border transition-colors',
                    statusFilter === 'failed'
                      ? 'bg-red-100 dark:bg-red-900/40 text-red-700 dark:text-red-300 border-red-300 dark:border-red-700'
                      : 'text-gray-600 dark:text-gray-400 border-gray-300 dark:border-gray-600 hover:bg-gray-100 dark:hover:bg-gray-700'
                  )}
                >
                  Failed ({formatNumber(batch.failed)})
                  {/* Red badge when failed > 0 and not actively filtered */}
                  {statusFilter !== 'failed' && (
                    <span className="ml-1 inline-block w-1.5 h-1.5 rounded-full bg-red-500" />
                  )}
                </button>
              )}
              {/* Cancelled */}
              {batch.cancelled > 0 && (
                <button
                  onClick={() => onFilterChange('cancelled')}
                  className={clsx(
                    'px-2 py-0.5 text-xs rounded-full border transition-colors',
                    statusFilter === 'cancelled'
                      ? 'bg-gray-200 dark:bg-gray-700 text-gray-700 dark:text-gray-300 border-gray-400 dark:border-gray-600'
                      : 'text-gray-600 dark:text-gray-400 border-gray-300 dark:border-gray-600 hover:bg-gray-100 dark:hover:bg-gray-700'
                  )}
                >
                  Cancelled ({formatNumber(batch.cancelled)})
                </button>
              )}
            </div>
          )}
          {expandedTasks.map(task => (
            <TransferRow
              key={task.id}
              task={task}
              onCancel={onCancelTask}
              onRetry={onRetryTask}
              indent
            />
          ))}
          {(() => {
            // "Show more" count uses the filtered total when a status filter is active
            const filteredTotal = statusFilter === 'completed' ? batch.completed
              : statusFilter === 'failed' ? batch.failed
              : statusFilter === 'cancelled' ? batch.cancelled
              : statusFilter === 'inprogress' ? batch.active
              : statusFilter === 'queued' ? batch.queued
              : statusFilter === 'active' ? (batch.active + batch.queued)
              : batch.total
            const remaining = filteredTotal - expandedTasks.length
            if (remaining <= 0) return null
            return (
              <div className="px-4 py-2 text-center">
                <button
                  onClick={(e) => { e.stopPropagation(); onLoadMore() }}
                  className="text-xs text-blue-600 hover:text-blue-800 dark:text-blue-400"
                >
                  Show more ({formatNumber(remaining)} remaining)
                </button>
              </div>
            )
          })()}
        </div>
      )}
    </>
  )
})

function getShortErrorLabel(task: TransferTask): string {
  if (!task.error) return ''
  if (task.errorType === 'disk_space') return 'No disk space'
  return task.error
}

// Plan 3: DaemonBatchRow removed. Daemon-initiated transfers now render in
// the unified batch list with a Daemon badge (see BatchRow).

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
  indent?: boolean
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
      indent && 'pl-12'
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

      {/* Action buttons */}
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
})

export function TransfersTab() {
  const {
    tasks,
    stats,
    enumerations,
    batches,
    expandedBatches,
    batchTasks,
    batchStatusFilter,
    folderCheckStatus,
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
    setBatchStatusFilter,
  } = useTransferStore()

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

  // Scan tasks for disk space errors; update incident state
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

  const isEmpty = tasks.length === 0 && enumerations.length === 0 && batches.length === 0 && !folderCheckStatus

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
            {folderCheckStatus && <FolderCheckRow folderName={folderCheckStatus.folderName} />}

            {/* For uploads, only show enumeration row during creating_folders phase (or completed/error).
                Once scanning starts the BatchRow takes over. Filtering by "batch exists" left a
                1-frame flash because the batch doesn't exist on the first render after scan starts. */}
            {enumerations
              .filter(e => {
                if (e.isComplete) return true
                if (batches.some(b => b.batchID === e.id)) return false
                if (e.direction === 'upload' && e.phase !== 'creating_folders') return false
                return true
              })
              .map((enumeration) => (
              <EnumerationRow
                key={enumeration.id}
                enumeration={enumeration}
              />
            ))}

            {batches.map((batch) => (
              <BatchRow
                key={batch.batchID}
                batch={batch}
                isExpanded={expandedBatches.has(batch.batchID)}
                expandedTasks={batchTasks.get(batch.batchID) || []}
                statusFilter={batchStatusFilter.get(batch.batchID) || ''}
                onToggle={() => toggleBatchExpanded(batch.batchID)}
                onCancel={() => cancelBatch(batch.batchID)}
                onRetryFailed={() => retryFailedInBatch(batch.batchID)}
                onLoadMore={() => {
                  const current = batchTasks.get(batch.batchID) || []
                  fetchBatchTasks(batch.batchID, current.length, 50)
                }}
                onCancelTask={handleCancel}
                onRetryTask={handleRetry}
                onFilterChange={(filter) => setBatchStatusFilter(batch.batchID, filter)}
              />
            ))}

            {/* Plan 3: daemon batches are merged into `batches` above and
                rendered via BatchRow with a Daemon badge. */}

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
