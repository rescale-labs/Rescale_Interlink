// v4.7.3: Extracted from PURTab.tsx for reuse in RunMonitorView, CompletedResultsView.
import type { JobRow } from '../../types/jobs'

export function StatsBar({ jobs }: { jobs: JobRow[] }) {
  const stats = jobs.reduce(
    (acc, job) => {
      acc.total++
      if (job.submitStatus === 'completed' || job.submitStatus === 'success' || job.submitStatus === 'skipped') {
        acc.completed++
      } else if (job.submitStatus === 'failed' || job.tarStatus === 'failed' || job.uploadStatus === 'failed') {
        acc.failed++
      } else if (
        job.tarStatus === 'running' || job.tarStatus === 'in_progress' ||
        job.uploadStatus === 'running' || job.uploadStatus === 'in_progress' ||
        job.createStatus === 'running' || job.createStatus === 'in_progress' ||
        job.submitStatus === 'running' || job.submitStatus === 'in_progress'
      ) {
        acc.inProgress++
      } else {
        acc.pending++
      }
      return acc
    },
    { total: 0, completed: 0, inProgress: 0, pending: 0, failed: 0 }
  )

  return (
    <div className="flex items-center gap-4 mb-4 p-3 bg-gray-50 dark:bg-gray-800 rounded-lg text-sm">
      <span className="font-medium">Jobs:</span>
      <span>Total: <span className="font-semibold">{stats.total}</span></span>
      <span className="text-green-600">Completed: <span className="font-semibold">{stats.completed}</span></span>
      <span className="text-blue-600">In Progress: <span className="font-semibold">{stats.inProgress}</span></span>
      <span className="text-gray-500">Pending: <span className="font-semibold">{stats.pending}</span></span>
      {stats.failed > 0 && (
        <span className="text-red-600">Failed: <span className="font-semibold">{stats.failed}</span></span>
      )}
    </div>
  )
}
