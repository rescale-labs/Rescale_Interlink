import { describe, it, expect, vi, afterEach } from 'vitest'
import { render, screen, fireEvent, cleanup } from '@testing-library/react'
import { PURTab } from './PURTab'
import { useJobStore, useRunStore, DEFAULT_JOB_TEMPLATE } from '../../stores'
import type { JobRow } from '../../types/jobs'
import { computeStageStats } from '../../utils/stageStats'

// The shared setup mock stops at the bindings the other tabs call; the export
// path needs the two save bindings, so this file supplies the App module.
const app = vi.hoisted(() => ({
  SaveFile: vi.fn(),
  SaveJobsToCSV: vi.fn(),
}))

vi.mock('../../../wailsjs/go/wailsapp/App', () => app)

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
  useJobStore.setState({ workflowState: 'initial', scannedJobs: [], jobRows: [] })
  useRunStore.setState({ activeRun: null, purViewMode: 'auto' })
})

// The state the Export CSV button lives in: one validated job, ready to run.
const readyToRun = () => {
  useJobStore.setState({
    workflowState: 'jobsValidated',
    scannedJobs: [{ ...DEFAULT_JOB_TEMPLATE, jobName: 'Run_1', directory: '/scratch/Run_1' }],
    jobRows: [],
  })
}

describe('PURTab export to CSV', () => {
  it('shows the refusal when the jobs CSV cannot be written', async () => {
    // What the Go side refuses to write: a local input path the ";"-separated
    // column would reload as a different file.
    const refusal =
      'job "Run_1" has the local input file "/scratch/a;b.inp", which a jobs CSV cannot carry'
    app.SaveFile.mockResolvedValue('/tmp/jobs.csv')
    app.SaveJobsToCSV.mockRejectedValue(new Error(refusal))
    readyToRun()

    render(<PURTab />)
    fireEvent.click(screen.getByRole('button', { name: /export csv/i }))

    expect(await screen.findByText(refusal)).toBeInTheDocument()
  })

  it('shows nothing when the export succeeds', async () => {
    app.SaveFile.mockResolvedValue('/tmp/jobs.csv')
    app.SaveJobsToCSV.mockResolvedValue(undefined)
    readyToRun()

    render(<PURTab />)
    fireEvent.click(screen.getByRole('button', { name: /export csv/i }))

    await vi.waitFor(() => expect(app.SaveJobsToCSV).toHaveBeenCalled())
    expect(screen.queryByText(/cannot carry/)).toBeNull()
  })
})

// A run left holding creations the platform never confirmed. Tar and upload
// succeeded; the create call was answered ambiguously and its text sits in the
// row's error, where the results view used to read it as an ordinary failure.
const unconfirmedRow = (jobName: string, index = 1): JobRow => ({
  index,
  directory: `/scratch/${jobName}`,
  jobName,
  tarStatus: 'completed',
  uploadStatus: 'completed',
  uploadProgress: 100,
  createStatus: 'indeterminate',
  submitStatus: 'indeterminate',
  status: 'indeterminate',
  jobId: '',
  progress: 0,
  error: 'job may exist: the platform accepted the request but the answer was lost',
})

const doneRow = (jobName: string, index = 2): JobRow => ({
  ...unconfirmedRow(jobName, index),
  createStatus: 'completed',
  submitStatus: 'completed',
  status: 'completed',
  jobId: 'job-123',
  error: '',
})

describe('PURTab results for unconfirmed creations', () => {
  it('names the unconfirmed creations instead of reporting a clean completion', () => {
    useRunStore.setState({
      activeRun: {
        runId: 'run_1',
        runType: 'pur',
        startTime: Date.now(),
        status: 'unconfirmed',
        totalJobs: 2,
        completedJobs: 0,
        failedJobs: 0,
        unconfirmedJobs: 2,
        durationMs: 1000,
        jobRows: [unconfirmedRow('Run_1'), unconfirmedRow('Run_2', 2)],
        pipelineStageStats: computeStageStats([]),
        pipelineLogs: [],
      },
      purViewMode: 'auto',
    })
    useJobStore.setState({ workflowState: 'completed' })

    render(<PURTab />)

    expect(screen.getByText(/2 job\(s\) could not be confirmed as created/)).toBeInTheDocument()
    expect(screen.getByText(/--recreate-indeterminate/)).toBeInTheDocument()
    expect(screen.queryByText('Pipeline Complete!')).toBeNull()
  })

  it('does not count the ambiguity as a failure in the fallback tally', () => {
    useJobStore.setState({
      workflowState: 'completed',
      jobRows: [unconfirmedRow('Run_1'), doneRow('Run_2')],
    })

    render(<PURTab />)

    expect(screen.getByText(/1 job\(s\) could not be confirmed as created/)).toBeInTheDocument()
    expect(screen.getByText(/1 succeeded, 0 failed/)).toBeInTheDocument()
  })

  it('leaves an ordinary completed run alone', () => {
    useJobStore.setState({
      workflowState: 'completed',
      jobRows: [doneRow('Run_1')],
    })

    render(<PURTab />)

    expect(screen.getByText('Pipeline Complete!')).toBeInTheDocument()
    expect(screen.queryByText(/could not be confirmed as created/)).toBeNull()
  })
})
