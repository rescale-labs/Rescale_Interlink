import { describe, it, expect, vi, afterEach } from 'vitest'
import { render, screen, fireEvent, cleanup } from '@testing-library/react'
import { PURTab } from './PURTab'
import { useJobStore, DEFAULT_JOB_TEMPLATE } from '../../stores'

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
