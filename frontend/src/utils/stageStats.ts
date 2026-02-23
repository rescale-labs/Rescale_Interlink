// v4.7.3: Extracted from jobStore.ts (was duplicated at lines 666-681 and 786-799).
// Computes per-stage completion stats from job rows.

import type { JobRow, PipelineStageStats } from '../types/jobs'

export function computeStageStats(jobRows: JobRow[]): PipelineStageStats {
  const stats: PipelineStageStats = {
    tar: { completed: 0, total: jobRows.length, failed: 0 },
    upload: { completed: 0, total: jobRows.length, failed: 0 },
    create: { completed: 0, total: jobRows.length, failed: 0 },
    submit: { completed: 0, total: jobRows.length, failed: 0 },
  }
  for (const r of jobRows) {
    if (r.tarStatus === 'completed' || r.tarStatus === 'success' || r.tarStatus === 'skipped') stats.tar.completed++
    if (r.tarStatus === 'failed') stats.tar.failed++
    if (r.uploadStatus === 'completed' || r.uploadStatus === 'success' || r.uploadStatus === 'skipped') stats.upload.completed++
    if (r.uploadStatus === 'failed') stats.upload.failed++
    if (r.createStatus === 'completed' || r.createStatus === 'success') stats.create.completed++
    if (r.createStatus === 'failed') stats.create.failed++
    if (r.submitStatus === 'completed' || r.submitStatus === 'success' || r.submitStatus === 'skipped') stats.submit.completed++
    if (r.submitStatus === 'failed') stats.submit.failed++
  }
  return stats
}
