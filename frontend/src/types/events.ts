// Event DTOs - match event_bridge.go DTOs for type safety

export interface ProgressEventDTO {
  timestamp: string;
  jobName: string;
  stage: string;
  progress: number;
  bytesCurrent: number;
  bytesTotal: number;
  message: string;
  rateBytes: number;
  etaMs: number;
}

export interface LogEventDTO {
  timestamp: string;
  level: 'DEBUG' | 'INFO' | 'WARN' | 'ERROR';
  message: string;
  stage: string;
  jobName: string;
  error?: string;
}

export interface StateChangeEventDTO {
  timestamp: string;
  jobName: string;
  oldStatus: string;
  newStatus: string;
  stage: string;
  jobId?: string;
  errorMessage?: string;
  uploadProgress: number;
}

export interface ErrorEventDTO {
  timestamp: string;
  jobName: string;
  stage: string;
  message: string;
}

export interface CompleteEventDTO {
  timestamp: string;
  totalJobs: number;
  successJobs: number;
  failedJobs: number;
  durationMs: number;
}

export interface TransferEventDTO {
  timestamp: string;
  taskId: string;
  taskType: string; // "upload" or "download"
  name: string;     // Display name (filename)
  size: number;     // File size in bytes
  progress: number; // 0.0 to 1.0
  speed: number;    // bytes/sec
  error?: string;
}

export interface EnumerationEventDTO {
  timestamp: string;
  id: string;
  folderName: string;
  direction: 'upload' | 'download';
  foldersFound: number;
  filesFound: number;
  bytesFound: number;
  isComplete: boolean;
  error?: string;
  statusMessage?: string;
  phase?: string; // "scanning", "creating_folders", "complete", "error"
  foldersTotal?: number;
  foldersCreated?: number;
}

export interface ScanProgressEventDTO {
  timestamp: string;
  scanType: 'software' | 'hardware';
  page: number;
  itemsFound: number;
  isComplete: boolean;
  isCached: boolean;
  error?: string;
}

export interface ConfigChangedEventDTO {
  timestamp: string;
  source: string;
  email: string;
}

export interface SanitizedTimelineEntry {
  timestamp: string;
  type: string;
  summary: string;
}

export interface ReportableErrorEventDTO {
  timestamp: string;
  errorID: string;
  category: string;     // "transfer", "job_create", "pur_pipeline", "auth"
  severity: string;     // "critical", "error"
  operation: string;    // "folder_upload", "file_download", etc.
  backend: string;      // "s3", "azure", ""
  errorMessage: string; // Redacted
  errorClass: string;   // "network", "auth", "disk_space", "client_error", "server_error", "internal", "timeout"
  timeline: SanitizedTimelineEntry[];
}

export interface BatchProgressEventDTO {
  timestamp: string;
  batchID: string;
  label: string;
  direction: string;
  total: number;
  active: number;
  queued: number;
  completed: number;
  failed: number;
  progress: number;
  speed: number;
  totalKnown: boolean; // true when scan complete; total is final
  filesPerSec: number; // file completion rate (windowed)
  etaSeconds: number; // estimated time remaining (-1 = unknown)
  discoveredTotal: number;
  discoveredBytes: number;
}

export interface ConnectionResultDTO {
  success: boolean;
  email?: string;
  fullName?: string;
  workspaceId?: string;
  workspaceName?: string;
  error?: string;
}

// Event names as constants
export const EVENT_NAMES = {
  PROGRESS: 'interlink:progress',
  LOG: 'interlink:log',
  STATE_CHANGE: 'interlink:state_change',
  ERROR: 'interlink:error',
  COMPLETE: 'interlink:complete',
  CONNECTION_RESULT: 'interlink:connection_result',
  TRANSFER: 'interlink:transfer',
  ENUMERATION: 'interlink:enumeration',
  SCAN_PROGRESS: 'interlink:scan_progress',
  BATCH_PROGRESS: 'interlink:batch_progress',
  CONFIG_CHANGED: 'interlink:config_changed',
  REPORTABLE_ERROR: 'interlink:reportable_error',
} as const;

export type LogLevel = 'DEBUG' | 'INFO' | 'WARN' | 'ERROR';
