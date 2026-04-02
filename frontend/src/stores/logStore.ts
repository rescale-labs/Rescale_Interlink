import { create } from 'zustand';
import { EventsOn } from '../../wailsjs/runtime/runtime';
import type { LogEventDTO, LogLevel, ProgressEventDTO, StateChangeEventDTO } from '../types';

// Two-tier ring buffer: WARN/ERROR entries are never evicted by DEBUG/INFO volume.
// Total capacity remains 10,000 entries (same memory footprint).
const MAX_DEBUG_INFO = 8000;   // DEBUG + INFO entries
const MAX_WARN_ERROR = 2000;   // WARN + ERROR entries (protected tier)

// Level severity for ">=" filtering (higher = more severe)
const LEVEL_SEVERITY: Record<string, number> = {
  DEBUG: 0,
  INFO: 1,
  WARN: 2,
  ERROR: 3,
};

interface LogEntry {
  id: number;
  timestamp: Date;
  level: LogLevel;
  message: string;
  stage: string;
  jobName: string;
  error?: string;
  // Cached for performance (matching Fyne optimization)
  formattedText: string;
  lowerText: string;
}

interface LogStats {
  total: number;
  errors: number;
  warnings: number;
  uptime: number; // milliseconds
}

// Merge two sorted-by-id arrays into one. O(n) merge.
function mergeSortedLogs(a: LogEntry[], b: LogEntry[]): LogEntry[] {
  if (a.length === 0) return b;
  if (b.length === 0) return a;
  const result: LogEntry[] = new Array(a.length + b.length);
  let i = 0, j = 0, k = 0;
  while (i < a.length && j < b.length) {
    if (a[i].id <= b[j].id) {
      result[k++] = a[i++];
    } else {
      result[k++] = b[j++];
    }
  }
  while (i < a.length) result[k++] = a[i++];
  while (j < b.length) result[k++] = b[j++];
  return result;
}

interface LogState {
  // Two-tier storage
  debugInfoLogs: LogEntry[];
  warnErrorLogs: LogEntry[];
  logVersion: number; // Increments on every addLog — lightweight change counter for useMemo
  errorCount: number;
  warningCount: number;
  stats: LogStats;
  startTime: Date;

  // Filters
  levelFilter: LogLevel | null;
  searchTerm: string;
  autoScroll: boolean;

  // Overall progress (from progress events)
  overallProgress: number;
  overallMessage: string;

  // Actions
  addLog: (event: LogEventDTO) => void;
  setLevelFilter: (level: LogLevel | null) => void;
  setSearchTerm: (term: string) => void;
  setAutoScroll: (enabled: boolean) => void;
  clearLogs: () => void;
  getFilteredLogs: () => LogEntry[];
  updateProgress: (event: ProgressEventDTO) => void;
  updateFromStateChange: (event: StateChangeEventDTO) => void;
  exportLogs: () => string;

  // Event listeners
  setupEventListeners: () => () => void;
}

let nextLogId = 0;

function formatLogEntry(entry: LogEventDTO): string {
  const timestamp = new Date(entry.timestamp).toLocaleTimeString();
  const parts = [timestamp, entry.level];

  if (entry.stage) {
    parts.push(`[${entry.stage}]`);
  }

  if (entry.jobName) {
    parts.push(`[${entry.jobName}]`);
  }

  parts.push(entry.message);

  return parts.join(' ');
}

// Check if a level is WARN or ERROR (routes to protected tier)
function isWarnOrError(level: string): boolean {
  return level === 'WARN' || level === 'ERROR';
}

export const useLogStore = create<LogState>((set, get) => ({
  // Initial state — two-tier ring buffer
  debugInfoLogs: [],
  warnErrorLogs: [],
  logVersion: 0,
  errorCount: 0,
  warningCount: 0,
  stats: { total: 0, errors: 0, warnings: 0, uptime: 0 },
  startTime: new Date(),
  levelFilter: 'INFO' as LogLevel,
  searchTerm: '',
  autoScroll: true,
  overallProgress: 0,
  overallMessage: 'Ready',

  addLog: (event: LogEventDTO) => {
    const formattedText = formatLogEntry(event);
    const entry: LogEntry = {
      id: nextLogId++,
      timestamp: new Date(event.timestamp),
      level: event.level,
      message: event.message,
      stage: event.stage,
      jobName: event.jobName,
      error: event.error,
      formattedText,
      lowerText: formattedText.toLowerCase(),
    };

    set((state) => {
      let newDebugInfo = state.debugInfoLogs;
      let newWarnError = state.warnErrorLogs;
      let newErrorCount = state.errorCount;
      let newWarningCount = state.warningCount;

      if (isWarnOrError(entry.level)) {
        // Route to protected WARN/ERROR tier
        newWarnError = [...newWarnError, entry];
        if (entry.level === 'ERROR') newErrorCount++;
        if (entry.level === 'WARN') newWarningCount++;

        // Trim WARN/ERROR tier if over capacity
        if (newWarnError.length > MAX_WARN_ERROR) {
          const trimmed = newWarnError.slice(newWarnError.length - MAX_WARN_ERROR);
          // Recompute counts after trim (rare — only when 2,000 WARN/ERROR entries overflow)
          newErrorCount = 0;
          newWarningCount = 0;
          for (const log of trimmed) {
            if (log.level === 'ERROR') newErrorCount++;
            if (log.level === 'WARN') newWarningCount++;
          }
          newWarnError = trimmed;
        }
      } else {
        // Route to DEBUG/INFO tier
        newDebugInfo = [...newDebugInfo, entry];
        if (newDebugInfo.length > MAX_DEBUG_INFO) {
          newDebugInfo = newDebugInfo.slice(newDebugInfo.length - MAX_DEBUG_INFO);
        }
      }

      const total = newDebugInfo.length + newWarnError.length;
      return {
        debugInfoLogs: newDebugInfo,
        warnErrorLogs: newWarnError,
        logVersion: state.logVersion + 1,
        errorCount: newErrorCount,
        warningCount: newWarningCount,
        stats: {
          total,
          errors: newErrorCount,
          warnings: newWarningCount,
          uptime: Date.now() - state.startTime.getTime(),
        },
      };
    });
  },

  setLevelFilter: (level) => {
    set({ levelFilter: level });
  },

  setSearchTerm: (term) => {
    set({ searchTerm: term });
  },

  setAutoScroll: (enabled) => {
    set({ autoScroll: enabled });
  },

  clearLogs: () => {
    nextLogId = 0;
    set({
      debugInfoLogs: [],
      warnErrorLogs: [],
      logVersion: 0,
      errorCount: 0,
      warningCount: 0,
      stats: { total: 0, errors: 0, warnings: 0, uptime: 0 },
      startTime: new Date(),
      overallProgress: 0,
      overallMessage: 'Ready',
    });
  },

  getFilteredLogs: () => {
    const { debugInfoLogs, warnErrorLogs, levelFilter, searchTerm } = get();
    const lowerSearch = searchTerm.toLowerCase();

    // Optimization: when filter >= WARN, skip DEBUG/INFO tier entirely
    const minSeverity = levelFilter ? (LEVEL_SEVERITY[levelFilter] ?? 0) : 0;
    const source = minSeverity >= 2
      ? warnErrorLogs
      : mergeSortedLogs(debugInfoLogs, warnErrorLogs);

    return source.filter((log) => {
      // Level filter uses ">=" semantics (e.g. INFO shows INFO+WARN+ERROR)
      if (levelFilter) {
        const logSeverity = LEVEL_SEVERITY[log.level] ?? 0;
        if (logSeverity < minSeverity) return false;
      }

      // Search filter - use cached lowercase text
      if (searchTerm && !log.lowerText.includes(lowerSearch)) {
        return false;
      }

      return true;
    });
  },

  updateProgress: (event: ProgressEventDTO) => {
    if (event.stage === 'overall') {
      set({
        overallProgress: event.progress,
        overallMessage: event.message,
      });
    }
  },

  updateFromStateChange: (event: StateChangeEventDTO) => {
    // Update status message based on state changes
    const { stage, newStatus, jobName } = event;
    let message = 'Ready';

    if (newStatus === 'running' || newStatus === 'in_progress') {
      // Format stage for display
      const stageDisplay = stage ? stage.charAt(0).toUpperCase() + stage.slice(1) : '';
      message = jobName ? `${stageDisplay}: ${jobName}` : stageDisplay || 'Processing...';
    } else if (newStatus === 'completed') {
      message = jobName ? `Completed: ${jobName}` : 'Completed';
    } else if (newStatus === 'failed' || newStatus === 'error') {
      message = jobName ? `Failed: ${jobName}` : 'Failed';
    }

    set({ overallMessage: message });
  },

  exportLogs: () => {
    const { debugInfoLogs, warnErrorLogs } = get();
    const allLogs = mergeSortedLogs(debugInfoLogs, warnErrorLogs);
    const lines = [
      'Rescale Interlink Activity Log Export',
      `Exported: ${new Date().toISOString()}`,
      `Total Entries: ${allLogs.length}`,
      '='.repeat(80),
      '',
    ];

    for (const log of allLogs) {
      lines.push(log.formattedText);
    }

    return lines.join('\n');
  },

  setupEventListeners: () => {
    const { addLog, updateProgress, updateFromStateChange } = get();

    const handleLog = (event: LogEventDTO) => {
      addLog(event);
    };

    const handleProgress = (event: ProgressEventDTO) => {
      updateProgress(event);
    };

    // Handle state change events to update status bar
    const handleStateChange = (event: StateChangeEventDTO) => {
      updateFromStateChange(event);
    };

    // Handle error events as log entries
    const handleError = (event: { timestamp: string; jobName: string; stage: string; message: string }) => {
      addLog({
        timestamp: event.timestamp,
        level: 'ERROR',
        message: event.message,
        stage: event.stage,
        jobName: event.jobName,
      });
    };

    // Handle transfer events with errors
    const handleTransfer = (event: { timestamp: string; taskId: string; taskType: string; name: string; size: number; progress: number; speed: number; error?: string }) => {
      if (event.error) {
        addLog({
          timestamp: event.timestamp,
          level: 'ERROR',
          message: `${event.taskType === 'upload' ? 'Upload' : 'Download'} failed: ${event.error}`,
          stage: 'transfer',
          jobName: event.name,
        });
      }
    };

    // Use unsub callbacks, NEVER EventsOff (would remove other stores' listeners)
    const unsubLog = EventsOn('interlink:log', handleLog);
    const unsubProgress = EventsOn('interlink:progress', handleProgress);
    const unsubError = EventsOn('interlink:error', handleError);
    const unsubTransfer = EventsOn('interlink:transfer', handleTransfer);
    const unsubStateChange = EventsOn('interlink:state_change', handleStateChange);

    return () => {
      unsubLog();
      unsubProgress();
      unsubError();
      unsubTransfer();
      unsubStateChange();
    };
  },
}));
