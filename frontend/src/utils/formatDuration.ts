// v4.7.3: Extracted from PURTab.tsx for reuse in RunMonitorView, CompletedResultsView, ActivityTab.

/**
 * Formats a duration in seconds into a human-readable string.
 * Examples: "45s", "3m 12s", "1h 23m"
 */
export function formatDuration(seconds: number): string {
  if (seconds > 3600) {
    return `${Math.floor(seconds / 3600)}h ${Math.floor((seconds % 3600) / 60)}m`
  }
  if (seconds > 60) {
    return `${Math.floor(seconds / 60)}m ${seconds % 60}s`
  }
  return `${seconds}s`
}

/**
 * Formats a duration in milliseconds into a human-readable string.
 */
export function formatDurationMs(ms: number): string {
  return formatDuration(Math.round(ms / 1000))
}
