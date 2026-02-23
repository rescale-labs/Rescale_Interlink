// v4.7.3: Extracted from PURTab.tsx for reuse across run monitoring views.
import clsx from 'clsx'

const STYLES: Record<string, string> = {
  pending: 'bg-gray-200 text-gray-700',
  running: 'bg-blue-200 text-blue-700',
  in_progress: 'bg-blue-200 text-blue-700',
  success: 'bg-green-200 text-green-700',
  completed: 'bg-green-200 text-green-700',
  failed: 'bg-red-200 text-red-700',
  skipped: 'bg-gray-100 text-gray-500',
}

export function StatusBadge({ status }: { status: string }) {
  return (
    <span
      className={clsx(
        'px-2 py-0.5 text-xs rounded-full font-medium',
        STYLES[status] || 'bg-gray-200 text-gray-700'
      )}
    >
      {status}
    </span>
  )
}
