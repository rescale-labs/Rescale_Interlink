// v4.7.3: Extracted from PURTab.tsx for reuse in RunMonitorView, CompletedResultsView.
import { useState } from 'react'
import clsx from 'clsx'
import type { PipelineLogEntry } from '../../types/jobs'

export function PipelineLogPanel({ logs, maxHeight = 200 }: { logs: PipelineLogEntry[]; maxHeight?: number }) {
  const [expanded, setExpanded] = useState(false)

  if (logs.length === 0) return null

  const displayLogs = expanded ? logs : logs.slice(-10)

  return (
    <div className="mt-4">
      <button
        onClick={() => setExpanded(!expanded)}
        className="flex items-center gap-1 text-xs text-gray-500 hover:text-gray-700 mb-1"
      >
        {expanded ? 'Hide' : 'Show'} Pipeline Log ({logs.length} entries)
      </button>
      {expanded && (
        <div
          className="bg-gray-900 text-gray-200 rounded p-2 font-mono text-xs overflow-auto"
          style={{ maxHeight }}
        >
          {displayLogs.map((log, i) => (
            <div key={i} className={clsx(
              'py-0.5',
              log.level === 'error' && 'text-red-400',
              log.level === 'warn' && 'text-yellow-400',
            )}>
              <span className="text-gray-500">{new Date(log.timestamp).toLocaleTimeString()}</span>
              {' '}
              {log.jobName && <span className="text-blue-400">[{log.jobName}]</span>}
              {' '}
              {log.message}
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
