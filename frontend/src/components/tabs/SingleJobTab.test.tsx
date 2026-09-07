import { describe, it, expect, afterEach, vi } from 'vitest'
import { render, screen, fireEvent, cleanup } from '@testing-library/react'
import { SingleJobTab } from './SingleJobTab'
import { useRunStore } from '../../stores'
import { useSingleJobStore } from '../../stores/singleJobStore'
import type { JobRow } from '../../types/jobs'
import type { RunState } from '../../types/run'
import { computeStageStats } from '../../utils/stageStats'

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
  useRunStore.setState({ activeRun: null, purViewMode: 'auto' })
  useSingleJobStore.getState().reset()
})

const row = (overrides: Partial<JobRow> = {}): JobRow => ({
  index: 0,
  directory: '/scratch/Sim_A',
  jobName: 'Sim_A',
  tarStatus: 'completed',
  uploadStatus: 'completed',
  uploadProgress: 100,
  createStatus: 'completed',
  submitStatus: 'completed',
  status: 'completed',
  jobId: 'job-123',
  progress: 0,
  error: '',
  ...overrides,
})

// A single job run left in a terminal state, as the store finalizes it.
function singleRun(status: RunState, jobRows: JobRow[]) {
  useSingleJobStore.setState({ state: 'executing' })
  useRunStore.setState({
    activeRun: {
      runId: 'run_1',
      runType: 'single',
      startTime: Date.now(),
      status,
      totalJobs: 1,
      completedJobs: status === 'completed' ? 1 : 0,
      failedJobs: 0,
      unconfirmedJobs: status === 'unconfirmed' ? 1 : 0,
      durationMs: 1000,
      jobRows,
      pipelineStageStats: computeStageStats(jobRows),
      pipelineLogs: [],
    },
  })
}

const unconfirmedRow = row({
  createStatus: 'indeterminate',
  submitStatus: 'indeterminate',
  status: 'indeterminate',
  jobId: '',
  error: 'job may exist: the answer to the create request was lost',
})

describe('SingleJobTab with an unconfirmed creation', () => {
  it('stops waiting and names the job whose creation was never confirmed', () => {
    singleRun('unconfirmed', [unconfirmedRow])

    render(<SingleJobTab />)

    expect(screen.getByText(/Sim_A could not be confirmed as created/)).toBeInTheDocument()
    expect(screen.getByText(/[Cc]heck the platform/)).toBeInTheDocument()
    expect(screen.queryByText(/Please wait/)).toBeNull()
    expect(screen.queryByText('Job Submitted Successfully!')).toBeNull()
  })

  it('offers Submit Another Job and clears the run when it is taken', async () => {
    singleRun('unconfirmed', [unconfirmedRow])

    render(<SingleJobTab />)
    fireEvent.click(screen.getByRole('button', { name: /submit another job/i }))

    await vi.waitFor(() => expect(useRunStore.getState().activeRun).toBeNull())
    expect(useSingleJobStore.getState().state).toBe('initial')
  })

  it('takes the workflow to its final step and offers no cancellation', () => {
    singleRun('unconfirmed', [unconfirmedRow])

    render(<SingleJobTab />)

    // The run is over: the step indicator is on 'Complete' and the view that
    // reports the ambiguity is a terminal one.
    expect(useSingleJobStore.getState().state).toBe('completed')
    expect(screen.queryByRole('button', { name: /^cancel$/i })).toBeNull()
    expect(screen.getByRole('button', { name: /submit another job/i })).toBeInTheDocument()
  })

  it('sends a cancelled run to the failure view, not the unconfirmed one', () => {
    singleRun('cancelled', [row({ submitStatus: 'cancelled', status: 'cancelled', jobId: '' })])

    render(<SingleJobTab />)

    expect(useSingleJobStore.getState().state).toBe('failed')
    expect(screen.getByText('Job Submission Failed')).toBeInTheDocument()
    expect(screen.queryByText(/could not be confirmed as created/)).toBeNull()
  })

  it('sends a failed run to the failure view', () => {
    singleRun('failed', [row({ submitStatus: 'failed', status: 'failed', jobId: '', error: 'create rejected' })])

    render(<SingleJobTab />)

    expect(useSingleJobStore.getState().state).toBe('failed')
    expect(screen.getByText('Job Submission Failed')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /try again/i })).toBeInTheDocument()
    expect(screen.queryByText(/could not be confirmed as created/)).toBeNull()
  })

  it('leaves an ordinary completed single job alone', () => {
    singleRun('completed', [row()])

    render(<SingleJobTab />)

    expect(screen.getByText('Job Submitted Successfully!')).toBeInTheDocument()
    expect(screen.queryByText(/could not be confirmed as created/)).toBeNull()
  })

  it('still waits while the run is active', () => {
    singleRun('active', [row({ submitStatus: 'pending', status: 'running', jobId: '' })])

    render(<SingleJobTab />)

    expect(screen.getByText(/Please wait/)).toBeInTheDocument()
    expect(screen.queryByText(/could not be confirmed as created/)).toBeNull()
  })
})
