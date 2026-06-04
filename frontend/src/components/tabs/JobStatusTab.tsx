import { useEffect, useState, useCallback } from 'react';
import {
  ArrowPathIcon,
  MagnifyingGlassIcon,
  ExclamationCircleIcon,
} from '@heroicons/react/24/outline';
import clsx from 'clsx';
import { ListJobStatuses } from '../../../wailsjs/go/wailsapp/App';
import { wailsapp } from '../../../wailsjs/go/models';

type JobItem = wailsapp.JobStatusItemDTO;

const STATUS_STYLES: Record<string, string> = {
  Completed:       'bg-green-100 text-green-800',
  Running:         'bg-blue-100 text-blue-800',
  Executing:       'bg-blue-100 text-blue-800',
  Queued:          'bg-yellow-100 text-yellow-800',
  Failed:          'bg-red-100 text-red-800',
  Stopped:         'bg-gray-100 text-gray-700',
  'Force Stopped': 'bg-gray-100 text-gray-700',
  Terminated:      'bg-gray-100 text-gray-700',
  'Not Submitted': 'bg-slate-100 text-slate-500',
};

function statusStyle(status: string): string {
  return STATUS_STYLES[status] ?? 'bg-gray-100 text-gray-600';
}

function formatDate(iso: string): string {
  if (!iso) return '—';
  try {
    return new Date(iso).toLocaleString();
  } catch {
    return iso;
  }
}

export function JobStatusTab() {
  const [jobs, setJobs] = useState<JobItem[]>([]);
  const [isLoading, setIsLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [filter, setFilter] = useState('');
  const [lastRefreshed, setLastRefreshed] = useState<Date | null>(null);

  const fetchJobs = useCallback(async () => {
    setIsLoading(true);
    setError(null);
    try {
      const result = await ListJobStatuses();
      if (result.error) {
        setError(result.error);
        setJobs([]);
      } else {
        setJobs(result.jobs ?? []);
        setLastRefreshed(new Date());
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setIsLoading(false);
    }
  }, []);

  useEffect(() => {
    fetchJobs();
  }, [fetchJobs]);

  const filtered = jobs.filter(j => {
    if (!filter) return true;
    const q = filter.toLowerCase();
    return (
      j.id.toLowerCase().includes(q) ||
      j.name.toLowerCase().includes(q) ||
      j.status.toLowerCase().includes(q) ||
      (j.reason ?? '').toLowerCase().includes(q)
    );
  });

  return (
    <div className="tab-panel flex flex-col h-full">
      {/* Header */}
      <div className="flex items-center gap-3 mb-4 flex-shrink-0">
        <div className="relative flex-1 max-w-sm">
          <MagnifyingGlassIcon className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-gray-400 pointer-events-none" />
          <input
            type="text"
            placeholder="Filter by id, name or status…"
            value={filter}
            onChange={e => setFilter(e.target.value)}
            className="w-full pl-9 pr-3 py-2 text-sm border border-gray-300 rounded-md focus:outline-none focus:ring-2 focus:ring-rescale-blue focus:border-transparent"
          />
        </div>
        <button
          onClick={fetchJobs}
          disabled={isLoading}
          className="btn-secondary flex items-center gap-2 flex-shrink-0"
          title="Refresh job list"
        >
          <ArrowPathIcon className={clsx('w-4 h-4', isLoading && 'animate-spin')} />
          Refresh
        </button>
        {lastRefreshed && (
          <span className="text-xs text-gray-400 flex-shrink-0">
            Updated {lastRefreshed.toLocaleTimeString()}
          </span>
        )}
      </div>

      {/* Error */}
      {error && (
        <div className="flex items-center gap-2 text-sm text-red-700 bg-red-50 border border-red-200 rounded-md px-3 py-2 mb-4 flex-shrink-0">
          <ExclamationCircleIcon className="w-4 h-4 flex-shrink-0" />
          {error}
        </div>
      )}

      {/* Loading state */}
      {isLoading && jobs.length === 0 && (
        <div className="flex flex-col items-center justify-center flex-1 text-gray-400">
          <ArrowPathIcon className="w-8 h-8 animate-spin mb-2" />
          <span className="text-sm">Loading jobs…</span>
        </div>
      )}

      {/* Table */}
      {!isLoading || jobs.length > 0 ? (
        <div className="flex-1 overflow-auto border border-gray-200 rounded-lg">
          <table className="min-w-full text-sm">
            <thead className="bg-gray-50 sticky top-0 z-10">
              <tr>
                <th className="px-4 py-3 text-left font-semibold text-gray-600 whitespace-nowrap">Job ID</th>
                <th className="px-4 py-3 text-left font-semibold text-gray-600">Name</th>
                <th className="px-4 py-3 text-left font-semibold text-gray-600 whitespace-nowrap">Status</th>
                <th className="px-4 py-3 text-left font-semibold text-gray-600">Reason</th>
                <th className="px-4 py-3 text-left font-semibold text-gray-600 whitespace-nowrap">Created</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-100 bg-white">
              {filtered.length === 0 && !isLoading && (
                <tr>
                  <td colSpan={5} className="px-4 py-8 text-center text-gray-400">
                    {filter ? 'No jobs match your filter.' : 'No jobs found.'}
                  </td>
                </tr>
              )}
              {filtered.map(job => (
                <tr key={job.id} className="hover:bg-gray-50 transition-colors">
                  <td className="px-4 py-3 font-mono text-xs text-gray-600 whitespace-nowrap">{job.id}</td>
                  <td className="px-4 py-3 text-gray-900">{job.name}</td>
                  <td className="px-4 py-3 whitespace-nowrap">
                    <span className={clsx('inline-flex items-center px-2 py-0.5 rounded-full text-xs font-medium', statusStyle(job.status))}>
                      {job.status}
                    </span>
                  </td>
                  <td className="px-4 py-3 text-gray-600 text-sm">{job.reason || '—'}</td>
                  <td className="px-4 py-3 text-gray-500 whitespace-nowrap">{formatDate(job.createdAt)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : null}

      {/* Footer count */}
      {jobs.length > 0 && (
        <div className="mt-2 text-xs text-gray-400 flex-shrink-0">
          {filtered.length} of {jobs.length} job{jobs.length !== 1 ? 's' : ''}
        </div>
      )}
    </div>
  );
}
