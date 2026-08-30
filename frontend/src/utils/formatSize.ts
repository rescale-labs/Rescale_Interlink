// Byte-size formatting shared by the transfer, file-browser and single-job
// views. Those three deliberately render an empty or unusable size differently
// — a transfer with nothing moved yet reads '0 B', a directory entry with no
// size reads '-', and an unreadable one reads '?' — so the policy is passed in
// per call site rather than picked here.

// Capped at TB on purpose: a size past 1024 TB reads as "1024.0 TB" rather
// than overflowing the unit list.
const UNITS = ['B', 'KB', 'MB', 'GB', 'TB']

export interface FormatSizeOptions {
  // Rendered for exactly 0 bytes.
  zero: string
  // Rendered for a non-number, a non-finite number, or a negative size.
  invalid: string
  // Render "2 KB" rather than "2.0 KB". Matches the single-job view, which has
  // always trimmed a trailing zero.
  trimTrailingZero?: boolean
}

export function formatSize(bytes: number, opts: FormatSizeOptions): string {
  if (typeof bytes !== 'number' || !Number.isFinite(bytes) || bytes < 0) return opts.invalid
  if (bytes === 0) return opts.zero

  const exp = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), UNITS.length - 1)
  const size = bytes / Math.pow(1024, exp)
  const value = opts.trimTrailingZero
    ? String(parseFloat(size.toFixed(1)))
    : size.toFixed(exp > 0 ? 1 : 0)

  return `${value} ${UNITS[exp]}`
}
