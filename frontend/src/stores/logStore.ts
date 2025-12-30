import { create } from 'zustand';
import { EventsOn, EventsOff } from '../../wailsjs/runtime/runtime';
import type { LogEventDTO, LogLevel, ProgressEventDTO, StateChangeEventDTO } from '../types';

// Maximum number of logs to keep in memory
const MAX_LOGS = 10000;

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

interface LogState {
  // Data
  logs: LogEntry[];
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

export const useLogStore = create<LogState>((set, get) => ({
  // Initial state
  logs: [],
  stats: { total: 0, errors: 0, warnings: 0, uptime: 0 },
  startTime: new Date(),
  levelFilter: null,
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
      // Add new log and trim if needed
      let newLogs = [...state.logs, entry];
      if (newLogs.length > MAX_LOGS) {
        newLogs = newLogs.slice(newLogs.length - MAX_LOGS);
      }

      // Update stats
      const stats = {
        total: newLogs.length,
        errors: newLogs.filter(l => l.level === 'ERROR').length,
        warnings: newLogs.filter(l => l.level === 'WARN').length,
        uptime: Date.now() - state.startTime.getTime(),
      };

      return { logs: newLogs, stats };
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
      logs: [],
      stats: { total: 0, errors: 0, warnings: 0, uptime: 0 },
      startTime: new Date(),
      overallProgress: 0,
      overallMessage: 'Ready',
    });
  },

  getFilteredLogs: () => {
    const { logs, levelFilter, searchTerm } = get();
    const lowerSearch = searchTerm.toLowerCase();

    return logs.filter((log) => {
      // Level filter
      if (levelFilter && log.level !== levelFilter) {
        return false;
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
    const { logs } = get();
    const lines = [
      'Rescale Interlink Activity Log Export',
      `Exported: ${new Date().toISOString()}`,
      `Total Entries: ${logs.length}`,
      '='.repeat(80),
      '',
    ];

    for (const log of logs) {
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

    // v4.0.0: Handle state change events to update status bar
    const handleStateChange = (event: StateChangeEventDTO) => {
      updateFromStateChange(event);
    };

    // v4.0.0: Handle error events - these were previously ignored
    const handleError = (event: { timestamp: string; jobName: string; stage: string; message: string }) => {
      addLog({
        timestamp: event.timestamp,
        level: 'ERROR',
        message: event.message,
        stage: event.stage,
        jobName: event.jobName,
      });
    };

    // v4.0.0: Handle transfer events with errors
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

    EventsOn('interlink:log', handleLog);
    EventsOn('interlink:progress', handleProgress);
    EventsOn('interlink:error', handleError);
    EventsOn('interlink:transfer', handleTransfer);
    EventsOn('interlink:state_change', handleStateChange);

    return () => {
      EventsOff('interlink:log');
      EventsOff('interlink:progress');
      EventsOff('interlink:error');
      EventsOff('interlink:transfer');
      EventsOff('interlink:state_change');
    };
  },
}));
