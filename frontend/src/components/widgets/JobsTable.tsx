// v4.7.3: Extracted from PURTab.tsx for reuse in RunMonitorView, CompletedResultsView, ActivityTab.
import clsx from 'clsx'
import type { JobRow } from '../../types/jobs'
import { StatusBadge } from './StatusBadge'

export function JobsTable({ jobs }: { jobs: JobRow[] }) {
  if (jobs.length === 0) {
    return (
      <div className="text-center text-gray-500 py-8">
        No jobs to display
      </div>
    )
  }

  return (
    <div className="overflow-auto max-h-96">
      <table className="w-full text-sm">
        <thead className="bg-gray-50 dark:bg-gray-800 sticky top-0">
          <tr>
            <th className="px-4 py-2 text-left font-medium text-gray-700 dark:text-gray-300">
              #
            </th>
            <th className="px-4 py-2 text-left font-medium text-gray-700 dark:text-gray-300">
              Directory
            </th>
            <th className="px-4 py-2 text-left font-medium text-gray-700 dark:text-gray-300">
              Job Name
            </th>
            <th className="px-4 py-2 text-center font-medium text-gray-700 dark:text-gray-300">
              Tar
            </th>
            <th className="px-4 py-2 text-center font-medium text-gray-700 dark:text-gray-300">
              Upload
            </th>
            <th className="px-4 py-2 text-center font-medium text-gray-700 dark:text-gray-300">
              Create
            </th>
            <th className="px-4 py-2 text-center font-medium text-gray-700 dark:text-gray-300">
              Submit
            </th>
            <th className="px-4 py-2 text-left font-medium text-gray-700 dark:text-gray-300">
              Job ID
            </th>
            <th className="px-4 py-2 text-left font-medium text-gray-700 dark:text-gray-300">
              Error
            </th>
          </tr>
        </thead>
        <tbody className="divide-y divide-gray-200 dark:divide-gray-700">
          {jobs.map((job) => (
            <tr
              key={job.index}
              className={clsx(
                'hover:bg-gray-50 dark:hover:bg-gray-800/50',
                job.error && 'bg-red-50 dark:bg-red-900/10'
              )}
            >
              <td className="px-4 py-2 text-gray-600">{job.index + 1}</td>
              <td className="px-4 py-2 font-mono text-xs truncate max-w-48" title={job.directory}>
                {job.directory}
              </td>
              <td className="px-4 py-2">{job.jobName}</td>
              <td className="px-4 py-2 text-center">
                <StatusBadge status={job.tarStatus} />
              </td>
              <td className="px-4 py-2 text-center">
                {(job.uploadStatus === 'running' || job.uploadStatus === 'in_progress') && job.uploadProgress > 0 ? (
                  <span className="px-2 py-0.5 text-xs rounded-full font-medium bg-blue-200 text-blue-700">
                    {job.uploadProgress.toFixed(1)}%
                  </span>
                ) : (
                  <StatusBadge status={job.uploadStatus} />
                )}
              </td>
              <td className="px-4 py-2 text-center">
                <StatusBadge status={job.createStatus} />
              </td>
              <td className="px-4 py-2 text-center">
                <StatusBadge status={job.submitStatus} />
              </td>
              <td className="px-4 py-2 font-mono text-xs text-gray-500">
                {job.jobId || '-'}
              </td>
              <td className="px-4 py-2 text-xs max-w-48 truncate" title={job.error || ''}>
                {job.error ? (
                  <span className="text-red-600">{job.error}</span>
                ) : '-'}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
