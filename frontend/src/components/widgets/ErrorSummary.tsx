// v4.7.3: Extracted from PURTab.tsx for reuse in CompletedResultsView.
import { useState } from 'react'
import { ExclamationTriangleIcon } from '@heroicons/react/24/outline'
import type { JobRow } from '../../types/jobs'

export function ErrorSummary({ jobs }: { jobs: JobRow[] }) {
  const failedJobs = jobs.filter((j) => j.error)
  const [expanded, setExpanded] = useState(false)

  if (failedJobs.length === 0) return null

  return (
    <div className="mt-4 p-3 bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800 rounded">
      <button
        onClick={() => setExpanded(!expanded)}
        className="flex items-center gap-2 text-sm font-medium text-red-700 dark:text-red-400"
      >
        <ExclamationTriangleIcon className="w-4 h-4" />
        {failedJobs.length} job{failedJobs.length !== 1 ? 's' : ''} failed
        <span className="text-xs text-red-500">({expanded ? 'hide' : 'show details'})</span>
      </button>
      {expanded && (
        <div className="mt-2 space-y-1 text-xs">
          {failedJobs.map((job) => (
            <div key={job.index} className="flex items-start gap-2">
              <span className="font-medium text-red-600 whitespace-nowrap">{job.jobName}:</span>
              <span className="text-red-500">{job.error}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
