import { useEffect, useRef, useMemo, useState } from 'react';
import { useLogStore } from '../../stores';
import { useRunStore } from '../../stores/runStore';
import type { LogLevel } from '../../types';
import type { JobRow } from '../../types/jobs';
import { GetDaemonStatus, GetDaemonLogs, GetRunHistory, GetHistoricalJobRows } from '../../../wailsjs/go/wailsapp/App';
import { JobsTable } from '../widgets';
import { formatDurationMs } from '../../utils/formatDuration';
import {
  MagnifyingGlassIcon,
  ArrowDownTrayIcon,
  TrashIcon,
  ChevronDownIcon,
  ChevronRightIcon,
} from '@heroicons/react/24/outline';
import clsx from 'clsx';
import { useVirtualizer } from '@tanstack/react-virtual';

const LOG_LEVELS: Array<LogLevel | null> = [null, 'DEBUG', 'INFO', 'WARN', 'ERROR'];
const LEVEL_LABELS: Record<string, string> = {
  '': 'All Levels',
  'DEBUG': 'DEBUG',
  'INFO': 'INFO',
  'WARN': 'WARN',
  'ERROR': 'ERROR',
};

const LEVEL_COLORS: Record<LogLevel, string> = {
  DEBUG: 'text-gray-500',
  INFO: 'text-blue-600',
  WARN: 'text-yellow-600',
  ERROR: 'text-red-600',
};

function formatUptime(ms: number): string {
  const seconds = Math.floor(ms / 1000);
  const minutes = Math.floor(seconds / 60);
  const hours = Math.floor(minutes / 60);

  if (hours >= 1) {
    return `${hours.toFixed(1)}h`;
  } else if (minutes >= 1) {
    return `${minutes.toFixed(1)}m`;
  } else {
    return `${seconds}s`;
  }
}

// LogEntry interface for local state (matching logStore's LogEntry)
interface LogEntry {
  id: number;
  timestamp: Date;
  level: LogLevel;
  message: string;
  stage: string;
  jobName: string;
  error?: string;
  formattedText: string;
  lowerText: string;
}

export function ActivityTab() {
  const {
    logs,
    stats,
    levelFilter,
    searchTerm,
    autoScroll,
    // v4.0.0: overallProgress/overallMessage hidden - was confusing users
    // overallProgress,
    // overallMessage,
    setLevelFilter,
    setSearchTerm,
    setAutoScroll,
    clearLogs,
    getFilteredLogs,
    exportLogs,
    // v4.0.0: setupEventListeners moved to App.tsx for global event listening
  } = useLogStore();

  // v4.3.2: Daemon log state for IPC-based log streaming
  const [daemonLogs, setDaemonLogs] = useState<LogEntry[]>([]);
  const [daemonRunning, setDaemonRunning] = useState(false);

  // Run history state
  const { completedRuns } = useRunStore();
  const [runHistoryExpanded, setRunHistoryExpanded] = useState(false);
  const [expandedRunId, setExpandedRunId] = useState<string | null>(null);
  const [historicalRuns, setHistoricalRuns] = useState<any[]>([]);
  const [historicalRows, setHistoricalRows] = useState<Record<string, JobRow[]>>({});
  const [loadingHistory, setLoadingHistory] = useState(false);

  const logContainerRef = useRef<HTMLDivElement>(null);

  // v4.3.2: Merge GUI logs and daemon logs with filtering
  const filteredLogs = useMemo(() => {
    const guiFiltered = getFilteredLogs();
    const lowerSearch = searchTerm.toLowerCase();

    // Filter daemon logs with same criteria
    const daemonFiltered = daemonLogs.filter((log) => {
      if (levelFilter && log.level !== levelFilter) return false;
      if (searchTerm && !log.lowerText.includes(lowerSearch)) return false;
      return true;
    });

    // Merge and sort by timestamp
    return [...guiFiltered, ...daemonFiltered].sort(
      (a, b) => a.timestamp.getTime() - b.timestamp.getTime()
    );
  }, [logs, daemonLogs, levelFilter, searchTerm, getFilteredLogs]);

  // Virtual scrolling for performance with large log counts
  const rowVirtualizer = useVirtualizer({
    count: filteredLogs.length,
    getScrollElement: () => logContainerRef.current,
    estimateSize: () => 24, // Estimated row height
    overscan: 20,
  });

  // v4.0.0: Event listeners are now set up at the app level (App.tsx)
  // so they're active even when this tab isn't visible.
  // No need to set them up here anymore.

  // Auto-scroll to bottom when new logs arrive
  useEffect(() => {
    if (autoScroll && logContainerRef.current && filteredLogs.length > 0) {
      logContainerRef.current.scrollTop = logContainerRef.current.scrollHeight;
    }
  }, [filteredLogs.length, autoScroll]);

  // Update uptime periodically
  useEffect(() => {
    const interval = setInterval(() => {
      // Force re-render to update uptime display
      useLogStore.setState((state) => ({
        stats: {
          ...state.stats,
          uptime: Date.now() - state.startTime.getTime(),
        },
      }));
    }, 1000);

    return () => clearInterval(interval);
  }, []);

  // v4.3.2: Poll daemon logs when daemon is running
  useEffect(() => {
    const pollDaemonLogs = async () => {
      try {
        const status = await GetDaemonStatus();
        const isConnected = status.running && status.ipcConnected;
        setDaemonRunning(isConnected);

        if (isConnected) {
          const daemonLogEntries = await GetDaemonLogs(100);
          if (daemonLogEntries && daemonLogEntries.length > 0) {
            const entries: LogEntry[] = daemonLogEntries.map((log, i) => ({
              id: -1000000 - i, // Negative IDs to avoid collision with GUI logs
              timestamp: new Date(log.timestamp),
              level: log.level.toUpperCase() as LogLevel,
              message: log.message,
              stage: `Daemon/${log.stage}`,
              jobName: '',
              formattedText: `[${log.timestamp}] ${log.level} [Daemon/${log.stage}] ${log.message}`,
              lowerText: `${log.level} daemon ${log.stage} ${log.message}`.toLowerCase(),
            }));
            setDaemonLogs(entries);
          }
        } else {
          setDaemonLogs([]);
        }
      } catch (err) {
        console.error('Failed to poll daemon logs:', err);
      }
    };

    pollDaemonLogs();
    const interval = setInterval(pollDaemonLogs, 5000);
    return () => clearInterval(interval);
  }, []);

  // Run history helpers
  const loadHistoricalRuns = async () => {
    setLoadingHistory(true);
    try {
      const history = await GetRunHistory();
      setHistoricalRuns(history || []);
    } catch (err) {
      console.error('Failed to load run history:', err);
    }
    setLoadingHistory(false);
  };

  const loadHistoricalJobRows = async (runId: string) => {
    try {
      const rows = await GetHistoricalJobRows(runId);
      const mapped: JobRow[] = (rows || []).map((r, i) => ({
        index: r.index ?? i,
        directory: r.directory || '',
        jobName: r.jobName || '',
        tarStatus: r.tarStatus || '',
        uploadStatus: r.uploadStatus || '',
        uploadProgress: 0,
        createStatus: r.createStatus || '',
        submitStatus: r.submitStatus || '',
        status: r.submitStatus || '',
        jobId: r.jobId || '',
        progress: 0,
        error: r.error || '',
      }));
      setHistoricalRows((prev) => ({ ...prev, [runId]: mapped }));
    } catch (err) {
      console.error('Failed to load job rows:', err);
    }
  };

  const handleRunEntryClick = (runId: string, jobRows?: JobRow[]) => {
    if (expandedRunId === runId) {
      setExpandedRunId(null);
      return;
    }
    setExpandedRunId(runId);
    // For historical runs without loaded rows, load them
    if (!jobRows && !historicalRows[runId]) {
      loadHistoricalJobRows(runId);
    }
  };

  const STATUS_BG: Record<string, string> = {
    completed: 'bg-green-100 text-green-800',
    failed: 'bg-red-100 text-red-800',
    cancelled: 'bg-yellow-100 text-yellow-800',
    interrupted: 'bg-orange-100 text-orange-800',
  };

  const hasRunHistory = completedRuns.length > 0 || historicalRuns.length > 0;

  const handleExport = () => {
    const content = exportLogs();
    // Create a blob and download
    const blob = new Blob([content], { type: 'text/plain' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = `interlink-activity-${new Date().toISOString().slice(0, 10)}.log`;
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
    URL.revokeObjectURL(url);
  };

  const handleClear = () => {
    if (confirm(`This will permanently delete all ${stats.total} log entries.\n\nAre you sure?`)) {
      clearLogs();
    }
  };

  return (
    <div className="tab-panel flex flex-col h-full">
      {/* Filter Bar */}
      <div className="flex items-center gap-4 pb-4 border-b border-gray-200">
        {/* Level Filter */}
        <div className="flex items-center gap-2">
          <label className="text-sm font-medium text-gray-700">Level:</label>
          <select
            className="input py-1 w-32"
            value={levelFilter || ''}
            onChange={(e) => setLevelFilter(e.target.value as LogLevel || null)}
          >
            {LOG_LEVELS.map((level) => (
              <option key={level || 'all'} value={level || ''}>
                {LEVEL_LABELS[level || '']}
              </option>
            ))}
          </select>
        </div>

        {/* Search */}
        <div className="flex-1 flex items-center gap-2">
          <label className="text-sm font-medium text-gray-700">Search:</label>
          <div className="relative flex-1 max-w-md">
            <MagnifyingGlassIcon className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-gray-400" />
            <input
              type="text"
              className="input pl-9 py-1"
              placeholder="Search logs..."
              value={searchTerm}
              onChange={(e) => setSearchTerm(e.target.value)}
            />
          </div>
        </div>

        {/* Controls */}
        <div className="flex items-center gap-2">
          <label className="flex items-center gap-2 text-sm text-gray-700 cursor-pointer">
            <input
              type="checkbox"
              checked={autoScroll}
              onChange={(e) => setAutoScroll(e.target.checked)}
              className="h-4 w-4 rounded border border-gray-300 text-rescale-blue focus:ring-rescale-blue focus:ring-2 bg-white cursor-pointer"
            />
            Auto-scroll
          </label>
          <button onClick={handleClear} className="btn-danger py-1 px-3">
            <TrashIcon className="w-4 h-4 mr-1" />
            Clear Logs
          </button>
          <button onClick={handleExport} className="btn-secondary py-1 px-3">
            <ArrowDownTrayIcon className="w-4 h-4 mr-1" />
            Export Logs
          </button>
        </div>
      </div>

      {/* Stats Bar */}
      <div className="flex items-center gap-8 py-3 border-b border-gray-200">
        <div className="flex flex-col">
          <span className="text-xs font-medium text-gray-500 uppercase">Total Logs</span>
          <span className="text-lg font-semibold text-gray-900">{stats.total}</span>
        </div>
        <div className="flex flex-col">
          <span className="text-xs font-medium text-gray-500 uppercase">Errors</span>
          <span className={clsx(
            'text-lg font-semibold',
            stats.errors > 0 ? 'text-red-600' : 'text-gray-900'
          )}>
            {stats.errors}
          </span>
        </div>
        <div className="flex flex-col">
          <span className="text-xs font-medium text-gray-500 uppercase">Warnings</span>
          <span className={clsx(
            'text-lg font-semibold',
            stats.warnings > 0 ? 'text-yellow-600' : 'text-gray-900'
          )}>
            {stats.warnings}
          </span>
        </div>
        <div className="flex flex-col">
          <span className="text-xs font-medium text-gray-500 uppercase">Uptime</span>
          <span className="text-lg font-semibold text-gray-900">
            {formatUptime(stats.uptime)}
          </span>
        </div>
        {/* v4.3.2: Daemon connection status indicator */}
        {daemonRunning && (
          <div className="flex flex-col">
            <span className="text-xs font-medium text-gray-500 uppercase">Daemon</span>
            <span className="text-lg font-semibold text-green-600">Connected</span>
          </div>
        )}
      </div>

      {/* Run History Section */}
      {hasRunHistory && (
        <div className="border border-gray-200 rounded-lg overflow-hidden mt-4">
          <button
            onClick={() => setRunHistoryExpanded(!runHistoryExpanded)}
            className="w-full flex items-center justify-between px-4 py-2 bg-gray-50 hover:bg-gray-100 transition-colors"
          >
            <span className="text-sm font-semibold text-gray-900">
              Run History
              <span className="ml-2 text-xs font-normal text-gray-500">
                ({completedRuns.length} session{historicalRuns.length > 0 ? `, ${historicalRuns.length} on disk` : ''})
              </span>
            </span>
            {runHistoryExpanded ? (
              <ChevronDownIcon className="w-4 h-4 text-gray-500" />
            ) : (
              <ChevronRightIcon className="w-4 h-4 text-gray-500" />
            )}
          </button>

          {runHistoryExpanded && (
            <div className="p-3 space-y-2 max-h-96 overflow-auto">
              {/* Session Runs */}
              {completedRuns.length > 0 && (
                <div>
                  <h4 className="text-xs font-medium text-gray-500 uppercase mb-1">Session Runs</h4>
                  <div className="space-y-1">
                    {completedRuns.map((run) => (
                      <div key={run.runId}>
                        <button
                          onClick={() => handleRunEntryClick(run.runId, run.jobRows)}
                          className={clsx(
                            'w-full flex items-center justify-between px-3 py-2 rounded text-sm',
                            'hover:bg-gray-100 transition-colors text-left',
                            expandedRunId === run.runId && 'bg-gray-100'
                          )}
                        >
                          <div className="flex items-center gap-3">
                            <span className={clsx(
                              'px-1.5 py-0.5 text-xs rounded font-medium',
                              STATUS_BG[run.finalStatus] || 'bg-gray-100 text-gray-800'
                            )}>
                              {run.finalStatus}
                            </span>
                            <span className="text-xs font-medium text-gray-600 uppercase">
                              {run.runType === 'pur' ? 'PUR' : 'Single'}
                            </span>
                            <span className="text-xs text-gray-500">
                              {new Date(run.startTime).toLocaleString()}
                            </span>
                          </div>
                          <div className="flex items-center gap-3 text-xs text-gray-500">
                            <span>{formatDurationMs(run.durationMs)}</span>
                            <span className="text-green-600">{run.completedJobs} ok</span>
                            {run.failedJobs > 0 && (
                              <span className="text-red-600">{run.failedJobs} failed</span>
                            )}
                            <span>{run.totalJobs} total</span>
                          </div>
                        </button>
                        {expandedRunId === run.runId && run.jobRows && (
                          <div className="ml-4 mt-1 mb-2 border border-gray-200 rounded">
                            <JobsTable jobs={run.jobRows} />
                          </div>
                        )}
                      </div>
                    ))}
                  </div>
                </div>
              )}

              {/* Historical Runs (from disk) */}
              <div className="border-t border-gray-200 pt-2">
                <div className="flex items-center justify-between mb-1">
                  <h4 className="text-xs font-medium text-gray-500 uppercase">Historical Runs (Disk)</h4>
                  <button
                    onClick={loadHistoricalRuns}
                    disabled={loadingHistory}
                    className={clsx(
                      'text-xs px-2 py-1 rounded border border-gray-300',
                      'hover:bg-gray-100 transition-colors',
                      loadingHistory && 'opacity-50 cursor-not-allowed'
                    )}
                  >
                    {loadingHistory ? 'Loading...' : 'Load from disk'}
                  </button>
                </div>
                {historicalRuns.length > 0 ? (
                  <div className="space-y-1">
                    {historicalRuns.map((entry) => {
                      // Skip entries that are already in session completedRuns
                      if (completedRuns.some((r) => r.runId === entry.runId)) return null;
                      const loadedRows = historicalRows[entry.runId];
                      return (
                        <div key={entry.runId}>
                          <button
                            onClick={() => handleRunEntryClick(entry.runId)}
                            className={clsx(
                              'w-full flex items-center justify-between px-3 py-2 rounded text-sm',
                              'hover:bg-gray-100 transition-colors text-left',
                              expandedRunId === entry.runId && 'bg-gray-100'
                            )}
                          >
                            <div className="flex items-center gap-3">
                              <span className="px-1.5 py-0.5 text-xs rounded font-medium bg-gray-100 text-gray-700">
                                disk
                              </span>
                              <span className="text-xs font-medium text-gray-600 uppercase">
                                {entry.runType === 'pur' ? 'PUR' : 'Single'}
                              </span>
                              <span className="text-xs text-gray-500">
                                {new Date(entry.modTime).toLocaleString()}
                              </span>
                            </div>
                            <div className="flex items-center gap-3 text-xs text-gray-500">
                              <span>{entry.jobCount} job{entry.jobCount !== 1 ? 's' : ''}</span>
                            </div>
                          </button>
                          {expandedRunId === entry.runId && (
                            <div className="ml-4 mt-1 mb-2 border border-gray-200 rounded">
                              {loadedRows ? (
                                <JobsTable jobs={loadedRows} />
                              ) : (
                                <div className="text-center text-gray-500 text-sm py-4">
                                  Loading job rows...
                                </div>
                              )}
                            </div>
                          )}
                        </div>
                      );
                    })}
                  </div>
                ) : (
                  <p className="text-xs text-gray-400 italic">
                    Click "Load from disk" to view historical runs.
                  </p>
                )}
              </div>
            </div>
          )}
        </div>
      )}

      {/* Log List - Virtualized */}
      <div
        ref={logContainerRef}
        className="flex-1 overflow-auto font-mono text-sm bg-slate-50 border border-gray-200 rounded mt-4"
      >
        {filteredLogs.length === 0 ? (
          <div className="flex items-center justify-center h-32 text-gray-500">
            {logs.length === 0 ? 'Activity logs will appear here...' : 'No logs match the current filters'}
          </div>
        ) : (
          <div
            style={{
              height: `${rowVirtualizer.getTotalSize()}px`,
              width: '100%',
              position: 'relative',
            }}
          >
            {rowVirtualizer.getVirtualItems().map((virtualRow) => {
              const log = filteredLogs[virtualRow.index];
              return (
                <div
                  key={log.id}
                  style={{
                    position: 'absolute',
                    top: 0,
                    left: 0,
                    width: '100%',
                    height: `${virtualRow.size}px`,
                    transform: `translateY(${virtualRow.start}px)`,
                  }}
                  className={clsx(
                    'px-3 py-1 border-b border-gray-100 hover:bg-slate-100 whitespace-nowrap',
                    LEVEL_COLORS[log.level]
                  )}
                >
                  <span className="text-gray-400 mr-2">
                    {log.timestamp.toLocaleTimeString()}
                  </span>
                  <span className={clsx('font-medium mr-2', LEVEL_COLORS[log.level])}>
                    {log.level}
                  </span>
                  {log.stage && (
                    <span className="text-gray-500 mr-2">[{log.stage}]</span>
                  )}
                  {log.jobName && (
                    <span className="text-gray-500 mr-2">[{log.jobName}]</span>
                  )}
                  <span className="text-gray-800">{log.message}</span>
                </div>
              );
            })}
          </div>
        )}
      </div>

      {/* v4.0.0: Overall Progress indicator hidden - was confusing users.
          TODO: Re-enable when tied to batch operations or remove entirely. */}
    </div>
  );
}

export default ActivityTab;
