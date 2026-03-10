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

// v4.0.8: Enumeration event for folder scan progress
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
  statusMessage?: string; // v4.7.7: Human-readable status
  phase?: string; // v4.8.5: "scanning", "creating_folders", "complete", "error"
  foldersTotal?: number; // v4.8.5: total folders to create
  foldersCreated?: number; // v4.8.5: folders created so far
}

// v4.0.8: Scan progress event for software/hardware catalog scanning
export interface ScanProgressEventDTO {
  timestamp: string;
  scanType: 'software' | 'hardware';
  page: number;
  itemsFound: number;
  isComplete: boolean;
  isCached: boolean;
  error?: string;
}

// v4.8.7: Config changed event for credential invalidation
export interface ConfigChangedEventDTO {
  timestamp: string;
  source: string;
  email: string;
}

// v4.8.7: Sanitized timeline entry for error reports
export interface SanitizedTimelineEntry {
  timestamp: string;
  type: string;
  summary: string;
}

// v4.8.7: Reportable error event for safe error reporting (Plan 3, 6A-6E)
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

// v4.7.7: Batch progress event for grouped transfer display
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
  totalKnown: boolean; // v4.8.0: True when scan complete, total is final
  filesPerSec: number; // v4.8.5: file completion rate (windowed)
  etaSeconds: number; // v4.8.5: estimated time remaining (-1 = unknown)
  discoveredTotal: number; // v4.8.5: files discovered by scan
  discoveredBytes: number; // v4.8.5: bytes discovered by scan
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
  ENUMERATION: 'interlink:enumeration', // v4.0.8: folder scan progress
  SCAN_PROGRESS: 'interlink:scan_progress', // v4.0.8: software/hardware catalog scan
  BATCH_PROGRESS: 'interlink:batch_progress', // v4.7.7: batch progress for grouped transfers
  CONFIG_CHANGED: 'interlink:config_changed', // v4.8.7: credential/config changes
  REPORTABLE_ERROR: 'interlink:reportable_error', // v4.8.7: safe error reporting
} as const;

export type LogLevel = 'DEBUG' | 'INFO' | 'WARN' | 'ERROR';
