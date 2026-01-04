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
} as const;

export type LogLevel = 'DEBUG' | 'INFO' | 'WARN' | 'ERROR';
