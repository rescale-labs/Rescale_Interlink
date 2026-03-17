// v4.7.3: Extracted from PURTab.tsx for reuse in RunMonitorView, CompletedResultsView.
import type { PipelineStageStats } from '../../types/jobs'

export function PipelineStageSummary({ stats, total }: { stats: PipelineStageStats; total: number }) {
  const stages = [
    { label: 'Tar', data: stats.tar },
    { label: 'Upload', data: stats.upload },
    { label: 'Create', data: stats.create },
    { label: 'Submit', data: stats.submit },
  ]

  return (
    <div className="flex items-center gap-6 mb-3 p-2 bg-blue-50 dark:bg-blue-900/20 rounded text-xs">
      {stages.map((s) => (
        <span key={s.label}>
          <span className="font-medium">{s.label}:</span>{' '}
          <span className="text-green-600">{s.data.completed}</span>
          {s.data.failed > 0 && <span className="text-red-600">/{s.data.failed}err</span>}
          /{total}
        </span>
      ))}
    </div>
  )
}
