import { describe, it, expect, afterEach, vi } from 'vitest'
import { isUnconfirmedRow, mergePolledJobRow, useRunStore } from './runStore'
import type { JobRow } from '../types/jobs'

// The shared setup mock hands out a fresh unsubscribe per EventsOn call and
// drops the handler; these tests have to call the handlers themselves.
const runtime = vi.hoisted(() => ({
  handlers: new Map<string, (data: unknown) => void>(),
  EventsOn: vi.fn(),
  EventsOff: vi.fn(),
  ClipboardGetText: vi.fn(() => Promise.resolve('')),
  BrowserOpenURL: vi.fn(),
}))
runtime.EventsOn.mockImplementation((name: string, cb: (data: unknown) => void) => {
  runtime.handlers.set(name, cb)
  return vi.fn()
})

vi.mock('../../wailsjs/runtime/runtime', () => runtime)

const app = vi.hoisted(() => ({
  GetRunStatus: vi.fn(),
  GetJobRows: vi.fn(),
  ResetRun: vi.fn(() => Promise.resolve()),
  CancelRun: vi.fn(() => Promise.resolve()),
}))

vi.mock('../../wailsjs/go/wailsapp/App', () => app)

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

// A job the platform may or may not hold: tar and upload succeeded, the create
// call was answered ambiguously, and the ambiguity text sits in the row's error.
function unconfirmedRow(jobName: string): JobRow {
  return baseRow({
    jobName,
    tarStatus: 'completed',
    uploadStatus: 'completed',
    createStatus: 'indeterminate',
    submitStatus: 'indeterminate',
    error: 'job may exist: the platform accepted the request but the answer was lost',
  })
}

// What a poll taken while the create call was still outstanding leaves behind:
// the state file said 'creating', and the answer arrived after the snapshot.
function staleCreatingRow(jobName: string, overrides: Partial<JobRow> = {}): JobRow {
  return baseRow({
    jobName,
    tarStatus: 'completed',
    uploadStatus: 'completed',
    createStatus: 'creating',
    submitStatus: 'creating',
    ...overrides,
  })
}

function doneRow(jobName: string): JobRow {
  return baseRow({
    jobName,
    tarStatus: 'completed',
    uploadStatus: 'completed',
    createStatus: 'completed',
    submitStatus: 'completed',
    status: 'completed',
  })
}

function completeEvent(overrides: Record<string, unknown> = {}) {
  return {
    timestamp: '',
    totalJobs: 2,
    successJobs: 0,
    failedJobs: 0,
    unconfirmedJobs: 0,
    durationMs: 1000,
    ...overrides,
  }
}

afterEach(() => {
  useRunStore.getState().stopPolling()
  useRunStore.setState({
    activeRun: null,
    completedRuns: [],
    queuedJob: null,
    queueStatus: null,
    _eventListenersSetup: false,
    _pollInterval: null,
  })
  runtime.handlers.clear()
  vi.clearAllMocks()
})

describe('isUnconfirmedRow', () => {
  it('is true for a create the platform never answered', () => {
    expect(isUnconfirmedRow(unconfirmedRow('job_1'))).toBe(true)
  })

  it('is false for a row that carries a job id', () => {
    expect(isUnconfirmedRow(staleCreatingRow('job_1', { jobId: 'job-abc' }))).toBe(false)
  })

  it('is false once the create call has been answered either way', () => {
    expect(isUnconfirmedRow(staleCreatingRow('job_1', { createStatus: 'completed' }))).toBe(false)
    expect(isUnconfirmedRow(staleCreatingRow('job_2', { createStatus: 'failed' }))).toBe(false)
  })
})

describe('runStore finalization: completion event', () => {
  it('records a batch whose creations were never confirmed as unconfirmed, not completed', () => {
    const store = useRunStore.getState()
    store.setupEventListeners()
    store.registerRun('run_1', 'pur', 2, [unconfirmedRow('job_1'), unconfirmedRow('job_2')])

    runtime.handlers.get('interlink:complete')!(
      completeEvent({ totalJobs: 2, unconfirmedJobs: 2 })
    )

    const { activeRun, completedRuns } = useRunStore.getState()
    expect(completedRuns[0].finalStatus).toBe('unconfirmed')
    expect(completedRuns[0].unconfirmedJobs).toBe(2)
    expect(completedRuns[0].failedJobs).toBe(0)
    expect(activeRun?.status).toBe('unconfirmed')
  })

  it('leaves an ordinary run completed', () => {
    const store = useRunStore.getState()
    store.setupEventListeners()
    store.registerRun('run_2', 'pur', 2, [doneRow('job_1'), doneRow('job_2')])

    runtime.handlers.get('interlink:complete')!(
      completeEvent({ totalJobs: 2, successJobs: 2 })
    )

    const { completedRuns } = useRunStore.getState()
    expect(completedRuns[0].finalStatus).toBe('completed')
    expect(completedRuns[0].unconfirmedJobs).toBe(0)
  })

  it('records a create-only run the backend counted as a success as completed', () => {
    const store = useRunStore.getState()
    store.setupEventListeners()
    // Create succeeded and submission was skipped without a submit-stage event,
    // so the row's submit status is still the poll's stale 'creating'.
    store.registerRun('run_7', 'pur', 1, [
      staleCreatingRow('job_1', { createStatus: 'completed', jobId: 'job-abc' }),
    ])

    runtime.handlers.get('interlink:complete')!(
      completeEvent({ totalJobs: 1, successJobs: 1, failedJobs: 0, unconfirmedJobs: 0 })
    )

    const { activeRun, completedRuns } = useRunStore.getState()
    expect(completedRuns[0].finalStatus).toBe('completed')
    expect(completedRuns[0].completedJobs).toBe(1)
    expect(completedRuns[0].unconfirmedJobs).toBe(0)
    expect(activeRun?.status).toBe('completed')
  })

  it('counts a definite create rejection as the failure the backend reported', () => {
    const store = useRunStore.getState()
    store.setupEventListeners()
    store.registerRun('run_8', 'pur', 1, [
      staleCreatingRow('job_1', { createStatus: 'failed', error: 'create rejected' }),
    ])

    runtime.handlers.get('interlink:complete')!(
      completeEvent({ totalJobs: 1, successJobs: 0, failedJobs: 1, unconfirmedJobs: 0 })
    )

    const [run] = useRunStore.getState().completedRuns
    expect(run.finalStatus).toBe('failed')
    expect(run.failedJobs).toBe(1)
    expect(run.unconfirmedJobs).toBe(0)
  })

  it('counts the rows when the backend does not report the unconfirmed field', () => {
    const store = useRunStore.getState()
    store.setupEventListeners()
    store.registerRun('run_9', 'pur', 2, [unconfirmedRow('job_1'), unconfirmedRow('job_2')])

    // An older backend: the event carries no unconfirmed count at all.
    runtime.handlers.get('interlink:complete')!({
      timestamp: '', totalJobs: 2, successJobs: 0, failedJobs: 0, durationMs: 1000,
    })

    const [run] = useRunStore.getState().completedRuns
    expect(run.finalStatus).toBe('unconfirmed')
    expect(run.unconfirmedJobs).toBe(2)
  })

  it('keeps reporting a failed run as failed', () => {
    const store = useRunStore.getState()
    store.setupEventListeners()
    store.registerRun('run_3', 'pur', 2, [
      unconfirmedRow('job_1'),
      baseRow({ jobName: 'job_2', submitStatus: 'failed', error: 'create rejected' }),
    ])

    runtime.handlers.get('interlink:complete')!(
      completeEvent({ totalJobs: 2, failedJobs: 1, unconfirmedJobs: 1 })
    )

    expect(useRunStore.getState().completedRuns[0].finalStatus).toBe('failed')
  })
})

describe('runStore finalization: polling fallback', () => {
  it('records an unconfirmed run when the completion event never arrives', async () => {
    const rows = [unconfirmedRow('job_1'), unconfirmedRow('job_2')]
    app.GetJobRows.mockResolvedValue(rows)
    app.GetRunStatus.mockResolvedValue({
      state: 'unconfirmed',
      totalJobs: 2,
      successJobs: 0,
      failedJobs: 0,
      unconfirmedJobs: 2,
      durationMs: 1000,
    })

    useRunStore.getState().registerRun('run_4', 'pur', 2, rows)
    useRunStore.getState().startPolling(60_000)

    await vi.waitFor(() => expect(useRunStore.getState().completedRuns).toHaveLength(1))
    const [run] = useRunStore.getState().completedRuns
    expect(run.finalStatus).toBe('unconfirmed')
    expect(run.unconfirmedJobs).toBe(2)
    expect(run.failedJobs).toBe(0)
  })

  it('does not call an idle poll over unconfirmed rows a completion', async () => {
    const rows = [unconfirmedRow('job_1'), doneRow('job_2')]
    app.GetJobRows.mockResolvedValue(rows)
    app.GetRunStatus.mockResolvedValue({
      state: 'idle',
      totalJobs: 2,
      successJobs: 1,
      failedJobs: 0,
      unconfirmedJobs: 1,
      durationMs: 1000,
    })

    useRunStore.getState().registerRun('run_6', 'pur', 2, rows)
    useRunStore.getState().startPolling(60_000)

    await vi.waitFor(() => expect(useRunStore.getState().completedRuns).toHaveLength(1))
    expect(useRunStore.getState().completedRuns[0].finalStatus).toBe('unconfirmed')
  })

  it('respects a backend failure count over a stale creating row', async () => {
    const rows = [staleCreatingRow('job_1', { error: 'create rejected' })]
    app.GetJobRows.mockResolvedValue(rows)
    app.GetRunStatus.mockResolvedValue({
      state: 'failed',
      totalJobs: 1,
      successJobs: 0,
      failedJobs: 1,
      unconfirmedJobs: 0,
      durationMs: 1000,
    })

    useRunStore.getState().registerRun('run_7p', 'pur', 1, rows)
    useRunStore.getState().startPolling(60_000)

    await vi.waitFor(() => expect(useRunStore.getState().completedRuns).toHaveLength(1))
    const [run] = useRunStore.getState().completedRuns
    expect(run.finalStatus).toBe('failed')
    expect(run.failedJobs).toBe(1)
    expect(run.unconfirmedJobs).toBe(0)
  })

  it('counts the rows when the engine reports on no run at all', async () => {
    const rows = [unconfirmedRow('job_1')]
    app.GetJobRows.mockResolvedValue(rows)
    // What GetRunStatus returns with no engine or nothing loaded: zeros that
    // report on nothing, not a run whose counts are all zero.
    app.GetRunStatus.mockResolvedValue({
      state: 'idle',
      totalJobs: 0,
      successJobs: 0,
      failedJobs: 0,
      unconfirmedJobs: 0,
      durationMs: 0,
    })

    useRunStore.getState().registerRun('run_9p', 'pur', 1, rows)
    useRunStore.getState().startPolling(60_000)

    await vi.waitFor(() => expect(useRunStore.getState().completedRuns).toHaveLength(1))
    expect(useRunStore.getState().completedRuns[0].finalStatus).toBe('unconfirmed')
  })

  it('counts the rows when the poll does not report the unconfirmed field', async () => {
    const rows = [unconfirmedRow('job_1')]
    app.GetJobRows.mockResolvedValue(rows)
    app.GetRunStatus.mockResolvedValue({
      state: 'idle',
      totalJobs: 1,
      successJobs: 0,
      failedJobs: 0,
      durationMs: 1000,
    })

    useRunStore.getState().registerRun('run_8p', 'pur', 1, rows)
    useRunStore.getState().startPolling(60_000)

    await vi.waitFor(() => expect(useRunStore.getState().completedRuns).toHaveLength(1))
    const [run] = useRunStore.getState().completedRuns
    expect(run.finalStatus).toBe('unconfirmed')
    expect(run.unconfirmedJobs).toBe(1)
  })

  it('leaves an ordinary idle run completed', async () => {
    const rows = [doneRow('job_1'), doneRow('job_2')]
    app.GetJobRows.mockResolvedValue(rows)
    app.GetRunStatus.mockResolvedValue({
      state: 'completed',
      totalJobs: 2,
      successJobs: 2,
      failedJobs: 0,
      unconfirmedJobs: 0,
      durationMs: 1000,
    })

    useRunStore.getState().registerRun('run_5', 'pur', 2, rows)
    useRunStore.getState().startPolling(60_000)

    await vi.waitFor(() => expect(useRunStore.getState().completedRuns).toHaveLength(1))
    expect(useRunStore.getState().completedRuns[0].finalStatus).toBe('completed')
  })
})
