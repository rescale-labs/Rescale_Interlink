import { useEffect, useState, useCallback, useRef } from 'react';
import {
  ArrowPathIcon,
  MagnifyingGlassIcon,
  ExclamationCircleIcon,
} from '@heroicons/react/24/outline';
import clsx from 'clsx';
import { ListJobStatuses, ListJobStatusesPage } from '../../../wailsjs/go/wailsapp/App';
import { wailsapp } from '../../../wailsjs/go/models';
import { useTabNavigation } from '../../App';

type JobItem = wailsapp.JobStatusItemDTO;

const PAGE_SIZE = 50;

const STATUS_STYLES: Record<string, string> = {
  Completed:       'bg-green-100 text-green-800 dark:bg-green-900 dark:text-green-200',
  Running:         'bg-blue-100 text-blue-800 dark:bg-blue-900 dark:text-blue-200',
  Executing:       'bg-blue-100 text-blue-800 dark:bg-blue-900 dark:text-blue-200',
  Queued:          'bg-yellow-100 text-yellow-800 dark:bg-yellow-900 dark:text-yellow-200',
  Failed:          'bg-red-100 text-red-800 dark:bg-red-900 dark:text-red-200',
  Stopped:         'bg-gray-100 text-gray-700 dark:bg-gray-700 dark:text-gray-300',
  'Force Stopped': 'bg-gray-100 text-gray-700 dark:bg-gray-700 dark:text-gray-300',
  Terminated:      'bg-gray-100 text-gray-700 dark:bg-gray-700 dark:text-gray-300',
  'Not Submitted': 'bg-slate-100 text-slate-500 dark:bg-slate-700 dark:text-slate-400',
};

function statusStyle(status: string): string {
  return STATUS_STYLES[status] ?? 'bg-gray-100 text-gray-600 dark:bg-gray-700 dark:text-gray-300';
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
  const { activeTabName } = useTabNavigation();
  const [jobs, setJobs] = useState<JobItem[]>([]);
  const [isLoading, setIsLoading] = useState(false);
  const [isLoadingMore, setIsLoadingMore] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [fetchWarning, setFetchWarning] = useState<string | null>(null);
  const [filter, setFilter] = useState('');
  const [lastRefreshed, setLastRefreshed] = useState<Date | null>(null);
  const [hasMore, setHasMore] = useState(false);
  // Incremented each time a new fetch starts; lets in-flight callbacks detect staleness.
  const fetchGenRef = useRef(0);
  // Each loading flag is owned by the generation that most recently set it.
  // A stale finally must clear its own flag (or the flag wedges on permanently),
  // but must not clear a flag a newer request has since claimed.
  const loadingOwnerRef = useRef(0);
  const loadingMoreOwnerRef = useRef(0);

  const fetchJobs = useCallback(async () => {
    const gen = ++fetchGenRef.current;
    loadingOwnerRef.current = gen;
    setIsLoading(true);
    setError(null);
    setFetchWarning(null);
    try {
      const result = await ListJobStatuses();
      if (gen !== fetchGenRef.current) return;
      if (result.error) {
        setError(result.error);
        setJobs([]);
        setHasMore(false);
      } else {
        setJobs(result.jobs ?? []);
        setHasMore(result.hasMore ?? false);
        setLastRefreshed(new Date());
        if (result.fetchErrors && result.fetchErrors > 0) {
          setFetchWarning(
            `${result.fetchErrors} job${result.fetchErrors !== 1 ? 's' : ''} couldn't be fetched — reason may be missing.`
          );
        }
      }
    } catch (err) {
      if (gen !== fetchGenRef.current) return;
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      if (gen === loadingOwnerRef.current) setIsLoading(false);
    }
  }, []);

  const loadMore = useCallback(async () => {
    const gen = ++fetchGenRef.current;
    loadingMoreOwnerRef.current = gen;
    setIsLoadingMore(true);
    setFetchWarning(null);
    try {
      const result = await ListJobStatusesPage(jobs.length);
      if (gen !== fetchGenRef.current) return;
      if (result.error) {
        setError(result.error);
      } else {
        setJobs(prev => [...prev, ...(result.jobs ?? [])]);
        setHasMore(result.hasMore ?? false);
        if (result.fetchErrors && result.fetchErrors > 0) {
          setFetchWarning(
            `${result.fetchErrors} job${result.fetchErrors !== 1 ? 's' : ''} couldn't be fetched — reason may be missing.`
          );
        }
      }
    } catch (err) {
      if (gen !== fetchGenRef.current) return;
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      if (gen === loadingMoreOwnerRef.current) setIsLoadingMore(false);
    }
  }, [jobs.length]);

  // Only run the initial fetch when the tab is active. With unmount={false} in
  // App.tsx the component stays mounted, so mount alone is not a reliable trigger.
  useEffect(() => {
    if (activeTabName !== 'Job Status') return;
    fetchJobs();
  }, [activeTabName, fetchJobs]);

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
            className="w-full pl-9 pr-3 py-2 text-sm border border-gray-300 dark:border-gray-600 rounded-md bg-white dark:bg-gray-800 text-gray-900 dark:text-gray-100 placeholder-gray-400 dark:placeholder-gray-500 focus:outline-none focus:ring-2 focus:ring-rescale-blue focus:border-transparent"
          />
        </div>
        <button
          onClick={fetchJobs}
          disabled={isLoading || isLoadingMore}
          className="btn-secondary flex items-center gap-2 flex-shrink-0"
          title="Refresh job list"
        >
          <ArrowPathIcon className={clsx('w-4 h-4', isLoading && 'animate-spin')} />
          Refresh
        </button>
        {lastRefreshed && (
          <span className="text-xs text-gray-400 dark:text-gray-500 flex-shrink-0">
            Updated {lastRefreshed.toLocaleTimeString()}
          </span>
        )}
      </div>

      {/* Error */}
      {error && (
        <div className="flex items-center gap-2 text-sm text-red-700 dark:text-red-400 bg-red-50 dark:bg-red-900/30 border border-red-200 dark:border-red-800 rounded-md px-3 py-2 mb-4 flex-shrink-0">
          <ExclamationCircleIcon className="w-4 h-4 flex-shrink-0" />
          {error}
        </div>
      )}

      {/* Partial fetch warning */}
      {fetchWarning && !error && (
        <div className="flex items-center gap-2 text-sm text-yellow-700 dark:text-yellow-400 bg-yellow-50 dark:bg-yellow-900/30 border border-yellow-200 dark:border-yellow-800 rounded-md px-3 py-2 mb-4 flex-shrink-0">
          <ExclamationCircleIcon className="w-4 h-4 flex-shrink-0" />
          {fetchWarning}
        </div>
      )}

      {/* Loading state */}
      {isLoading && jobs.length === 0 && (
        <div className="flex flex-col items-center justify-center flex-1 text-gray-400 dark:text-gray-500">
          <ArrowPathIcon className="w-8 h-8 animate-spin mb-2" />
          <span className="text-sm">Loading jobs…</span>
        </div>
      )}

      {/* Table */}
      {!isLoading || jobs.length > 0 ? (
        <div className="flex-1 overflow-auto border border-gray-200 dark:border-gray-700 rounded-lg">
          <table className="min-w-full text-sm">
            <thead className="bg-gray-50 dark:bg-gray-800 sticky top-0 z-10">
              <tr>
                <th className="px-4 py-3 text-left font-semibold text-gray-600 dark:text-gray-300 whitespace-nowrap">Job ID</th>
                <th className="px-4 py-3 text-left font-semibold text-gray-600 dark:text-gray-300">Name</th>
                <th className="px-4 py-3 text-left font-semibold text-gray-600 dark:text-gray-300 whitespace-nowrap">Status</th>
                <th className="px-4 py-3 text-left font-semibold text-gray-600 dark:text-gray-300">Reason</th>
                <th className="px-4 py-3 text-left font-semibold text-gray-600 dark:text-gray-300 whitespace-nowrap">Created</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-100 dark:divide-gray-700 bg-white dark:bg-gray-900">
              {filtered.length === 0 && !isLoading && (
                <tr>
                  <td colSpan={5} className="px-4 py-8 text-center text-gray-400 dark:text-gray-500">
                    {filter ? 'No jobs match your filter.' : 'No jobs found.'}
                  </td>
                </tr>
              )}
              {filtered.map(job => (
                <tr key={job.id} className="hover:bg-gray-50 dark:hover:bg-gray-800 transition-colors">
                  <td className="px-4 py-3 font-mono text-xs text-gray-600 dark:text-gray-400 whitespace-nowrap">{job.id}</td>
                  <td className="px-4 py-3 text-gray-900 dark:text-gray-100">{job.name}</td>
                  <td className="px-4 py-3 whitespace-nowrap">
                    <span className={clsx('inline-flex items-center px-2 py-0.5 rounded-full text-xs font-medium', statusStyle(job.status))}>
                      {job.status}
                    </span>
                  </td>
                  <td className="px-4 py-3 text-gray-600 dark:text-gray-400 text-sm">{job.reason || '—'}</td>
                  <td className="px-4 py-3 text-gray-500 dark:text-gray-400 whitespace-nowrap">{formatDate(job.createdAt)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : null}

      {/* Footer: count + Load More */}
      <div className="mt-2 flex items-center justify-between flex-shrink-0">
        {jobs.length > 0 && (
          <span className="text-xs text-gray-400 dark:text-gray-500">
            {filter
              ? `${filtered.length} of ${jobs.length} job${jobs.length !== 1 ? 's' : ''} loaded`
              : `${jobs.length} most recent job${jobs.length !== 1 ? 's' : ''} loaded`}
            {hasMore && !filter && ' — more available'}
          </span>
        )}
        {hasMore && !filter && (
          <button
            onClick={loadMore}
            disabled={isLoadingMore || isLoading}
            className="btn-secondary flex items-center gap-2 text-xs"
          >
            {isLoadingMore
              ? <><ArrowPathIcon className="w-3 h-3 animate-spin" /> Loading…</>
              : `Load next ${PAGE_SIZE}`}
          </button>
        )}
      </div>
    </div>
  );
}
