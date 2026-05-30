import { useRateLimitStore } from '../stores/rateLimitStore';

// App-level strip shown while the Rescale API is actively rate-limiting us.
// Surfaced globally (not per-tab) so a user watching a slow operation — e.g. a
// trash purge stuck behind active transfers — understands why it's waiting.
export default function RateLimitBanner() {
  const active = useRateLimitStore((s) => s.active);
  const message = useRateLimitStore((s) => s.message);

  if (!active) return null;

  return (
    <div
      role="status"
      className="flex items-center gap-2 px-4 py-1.5 bg-amber-100 text-amber-800 border-b border-amber-200 text-sm"
    >
      <svg className="h-4 w-4 flex-shrink-0 animate-pulse" fill="currentColor" viewBox="0 0 20 20" aria-hidden="true">
        <path
          fillRule="evenodd"
          d="M10 18a8 8 0 100-16 8 8 0 000 16zm1-12a1 1 0 10-2 0v4a1 1 0 00.293.707l2.828 2.829a1 1 0 101.415-1.415L11 9.586V6z"
          clipRule="evenodd"
        />
      </svg>
      <span>
        {message || 'Rescale is rate-limiting requests — operations may be slower than usual while active transfers complete.'}
      </span>
    </div>
  );
}
