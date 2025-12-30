import { useEffect, useRef, useMemo } from 'react';
import { useLogStore } from '../../stores';
import type { LogLevel } from '../../types';
import {
  MagnifyingGlassIcon,
  ArrowDownTrayIcon,
  TrashIcon,
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

  const logContainerRef = useRef<HTMLDivElement>(null);
  const filteredLogs = useMemo(() => getFilteredLogs(), [logs, levelFilter, searchTerm]);

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
      </div>

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
