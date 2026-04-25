import { describe, it, expect } from 'vitest'
import { mergePolledJobRow } from './runStore'
import type { JobRow } from '../types/jobs'

function baseRow(overrides: Partial<JobRow> = {}): JobRow {
  return {
    index: 1,
    directory: '',
    jobName: 'job1',
    tarStatus: 'pending',
    uploadStatus: 'pending',
    uploadProgress: 0,
    createStatus: 'pending',
    submitStatus: 'pending',
    status: 'running',
    jobId: '',
    progress: 0,
    error: '',
    ...overrides,
  }
}

describe('mergePolledJobRow', () => {
  it('does not downgrade an in-progress upload back to pending when progress is visible', () => {
    // Simulate an event-updated row: upload is active, percentage showing.
    const existing = baseRow({ uploadStatus: 'in_progress', uploadProgress: 30 })
    // Polled row: backend state manager still shows "pending" because
    // UploadStatus is only persisted on terminal success/failure.
    const polled = baseRow({ uploadStatus: 'pending', uploadProgress: 0 })

    const merged = mergePolledJobRow(existing, polled)

    expect(merged.uploadStatus).toBe('in_progress')
    expect(merged.uploadProgress).toBeGreaterThan(0)
  })

  it('accepts a polled terminal success update over an in-progress row', () => {
    const existing = baseRow({ uploadStatus: 'in_progress', uploadProgress: 90 })
    const polled = baseRow({ uploadStatus: 'success', uploadProgress: 1.0 })

    const merged = mergePolledJobRow(existing, polled)

    expect(merged.uploadStatus).toBe('success')
    expect(merged.uploadProgress).toBe(100) // polled fraction 1.0 × 100
  })

  it('accepts a polled terminal failed update over an in-progress row', () => {
    const existing = baseRow({ uploadStatus: 'in_progress', uploadProgress: 50 })
    const polled = baseRow({ uploadStatus: 'failed', error: 'boom' })

    const merged = mergePolledJobRow(existing, polled)

    expect(merged.uploadStatus).toBe('failed')
    expect(merged.error).toBe('boom')
  })

  it('returns polled row unchanged if no existing row', () => {
    const polled = baseRow({ uploadStatus: 'in_progress', uploadProgress: 42 })
    expect(mergePolledJobRow(undefined, polled)).toEqual(polled)
  })

  it('does not block pending-over-pending (no spurious guard)', () => {
    const existing = baseRow({ uploadStatus: 'pending', uploadProgress: 0 })
    const polled = baseRow({ uploadStatus: 'pending', uploadProgress: 0 })
    const merged = mergePolledJobRow(existing, polled)
    expect(merged.uploadStatus).toBe('pending')
  })
})
