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
  SelectFile: vi.fn(),
  LoadJobFromSGE: vi.fn(),
  ScanDirectory: vi.fn(),
  SelectMultipleFiles: vi.fn(),
  SelectDirectory: vi.fn(),
  GetLocalFilesInfo: vi.fn(),
}))

vi.mock('../../../wailsjs/go/wailsapp/App', () => app)

// The real picker's rows never render in jsdom (FileList virtualizes them), so
// a stand-in answers with two library files.
vi.mock('../widgets/RemoteFilePicker', async () => {
  const { createElement } = await import('react')
  return {
    RemoteFilePicker: ({ isOpen, onSelect }: { isOpen: boolean; onSelect: (ids: string[]) => void }) =>
      isOpen ? createElement('button', { onClick: () => onSelect(['FAKE-1', 'FAKE-2']) }, 'Pick library files') : null,
  }
})

const { scanOptions: initialScanOptions, purRunOptions: initialPURRunOptions } = useJobStore.getState()

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
  useJobStore.setState({
    workflowState: 'initial', scannedJobs: [], jobRows: [],
    scanOptions: initialScanOptions, purRunOptions: initialPURRunOptions,
  })
  useRunStore.setState({ activeRun: null, purViewMode: 'auto' })
})

// The scan step of a new batch, scanning for files under a chosen root.
const filesScanStep = () => {
  useJobStore.setState({
    workflowState: 'templateReady',
    workflowPath: 'createNew',
    scanOptions: { ...initialScanOptions, scanMode: 'files', rootDir: '/scratch/cases' },
  })
}

describe('PURTab file scan', () => {
  it('offers Recursive scan in Files mode and sends it with the scan', async () => {
    app.ScanDirectory.mockResolvedValue({ jobs: [], totalCount: 0, matchCount: 0 })
    filesScanStep()

    render(<PURTab />)
    fireEvent.click(screen.getByRole('checkbox', { name: 'Recursive scan' }))
    fireEvent.click(screen.getByRole('button', { name: /Scan to Create Jobs/ }))

    await vi.waitFor(() => expect(app.ScanDirectory).toHaveBeenCalledWith(
      expect.objectContaining({ scanMode: 'files', recursive: true }), expect.anything()))
    // "**" never enters a hidden folder, so that option stays with Folders mode.
    expect(screen.queryByRole('checkbox', { name: 'Include hidden directories' })).toBeNull()
  })
})

// Common Input Files offer what Single Job's inputs do: Add Files, Add Folder,
// the Rescale Library and the list, all editing the one comma-separated value
// the run takes.
describe('PURTab common input files', () => {
  it('adds files, a folder and library files to the value, and lists them', async () => {
    app.SelectMultipleFiles.mockResolvedValue(['/scratch/deck.inp', '/scratch/mats.dat'])
    app.SelectDirectory.mockResolvedValue('/scratch/lib')
    app.GetLocalFilesInfo.mockImplementation(async (paths: string[]) => paths.map((path) => ({
      path, name: path.split('/').pop(), isDir: path === '/scratch/lib', size: 1024, fileCount: 2, modTime: '',
    })))
    filesScanStep()

    render(<PURTab />)
    fireEvent.click(screen.getByRole('button', { name: 'Add Files' }))
    await screen.findByTitle('/scratch/mats.dat')
    fireEvent.click(screen.getByRole('button', { name: 'Add Folder' }))
    await screen.findByText('2 files') // the folder's own count
    fireEvent.click(screen.getByRole('button', { name: 'Browse Rescale Library' }))
    fireEvent.click(screen.getByRole('button', { name: 'Pick library files' }))

    expect(useJobStore.getState().purRunOptions.commonInputFiles)
      .toBe('/scratch/deck.inp,/scratch/mats.dat,/scratch/lib,id:FAKE-1,id:FAKE-2')
    expect(screen.getByText('4 files, 1 folder')).toBeInTheDocument()
    expect(screen.getAllByText('on Rescale')).toHaveLength(2)
    expect(app.GetLocalFilesInfo).not.toHaveBeenCalledWith(expect.arrayContaining(['id:FAKE-1']))
  })

  // The list's sizes are fetched once per entry, so a failed lookup has to be
  // tried again, and a removed entry looked up afresh when it comes back.
  it('retries a failed lookup and looks a re-added entry up again', async () => {
    const info = (size: number, error?: string) =>
      [{ path: '/scratch/deck.inp', name: 'deck.inp', isDir: false, size, fileCount: 0, modTime: '', error }]
    app.SelectMultipleFiles.mockResolvedValue(['/scratch/deck.inp'])
    app.GetLocalFilesInfo.mockResolvedValueOnce(info(0, 'FAKE: no such file'))
      .mockResolvedValueOnce(info(1024)).mockResolvedValueOnce(info(2048))
    filesScanStep()

    render(<PURTab />)
    fireEvent.click(screen.getByRole('button', { name: 'Add Files' }))
    await screen.findByText('cannot be read')
    fireEvent.blur(screen.getByPlaceholderText(/Comma-separated local files/))
    expect(await screen.findAllByText('1 KB')).toHaveLength(2) // the row and the total
    fireEvent.click(screen.getByTitle('Remove'))
    fireEvent.click(screen.getByRole('button', { name: 'Add Files' }))
    expect(await screen.findAllByText('2 KB')).toHaveLength(2)
    expect(app.GetLocalFilesInfo).toHaveBeenCalledTimes(3)
  })

  it('removes an entry and clears the list', () => {
    filesScanStep()
    useJobStore.setState({ purRunOptions: { ...initialPURRunOptions, commonInputFiles: '/scratch/a.dat, id:FAKE-1,/scratch/b.dat' } })

    render(<PURTab />)
    fireEvent.click(screen.getAllByTitle('Remove')[1])
    expect(useJobStore.getState().purRunOptions.commonInputFiles).toBe('/scratch/a.dat,/scratch/b.dat')
    fireEvent.click(screen.getByRole('button', { name: 'Clear All' }))
    expect(useJobStore.getState().purRunOptions.commonInputFiles).toBe('')
  })
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

describe('PURTab loading base job settings', () => {
  it('says why an SGE script could not be loaded', async () => {
    app.SelectFile.mockResolvedValue('/fake/job.sh')
    app.LoadJobFromSGE.mockRejectedValue(new Error('FAKE: not an SGE script'))
    useJobStore.setState({ workflowState: 'pathChosen', workflowPath: 'createNew' })

    render(<PURTab />)
    fireEvent.click(screen.getByRole('button', { name: /Load Existing Base Job Settings/ }))
    fireEvent.click(screen.getByRole('button', { name: 'SGE Script' }))

    expect(await screen.findByText('FAKE: not an SGE script')).toBeInTheDocument()
  })

  // An SGE script is a template like a CSV or JSON one: what it sets is kept,
  // and what it leaves empty takes the template's default.
  it('keeps an SGE script\'s settings and defaults what it leaves empty', async () => {
    app.SelectFile.mockResolvedValue('/fake/job.sh')
    app.LoadJobFromSGE.mockResolvedValue({
      ...DEFAULT_JOB_TEMPLATE, jobName: 'Sim_SGE', coreType: 'FAKE-CORE', coresPerSlot: 8, command: './run.sh', submitMode: '',
    })
    useJobStore.setState({ workflowState: 'pathChosen', workflowPath: 'createNew' })

    render(<PURTab />)
    fireEvent.click(screen.getByRole('button', { name: /Load Existing Base Job Settings/ }))
    fireEvent.click(screen.getByRole('button', { name: 'SGE Script' }))

    await vi.waitFor(() => expect(useJobStore.getState().template.jobName).toBe('Sim_SGE'))
    expect(useJobStore.getState().template).toMatchObject({
      coreType: 'FAKE-CORE', coresPerSlot: 8, command: './run.sh', submitMode: DEFAULT_JOB_TEMPLATE.submitMode,
    })
    useJobStore.setState({ template: DEFAULT_JOB_TEMPLATE })
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

// An ordinary failure: the create call came back with a refusal.
const failedRow = (jobName: string, index = 3): JobRow => ({
  ...unconfirmedRow(jobName, index),
  createStatus: 'failed',
  submitStatus: 'failed',
  status: 'failed',
  error: 'create rejected: invalid analysis code',
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

  it('does not claim the platform took the request, and says the flag is batch-wide', () => {
    useJobStore.setState({
      workflowState: 'completed',
      jobRows: [unconfirmedRow('Run_1'), unconfirmedRow('Run_2', 2)],
    })

    render(<PURTab />)

    // The notice's own opening claim; a row's error text is the backend's.
    expect(screen.queryByText(/^The platform accepted the request/)).toBeNull()
    expect(screen.getByText(/may or may not have reached the platform/)).toBeInTheDocument()
    expect(screen.getByText(/creates every unconfirmed job in the batch/)).toBeInTheDocument()
  })

  it('leaves the unconfirmed creations out of the failure panel', () => {
    useJobStore.setState({
      workflowState: 'completed',
      jobRows: [unconfirmedRow('Run_1'), unconfirmedRow('Run_2', 2)],
    })

    render(<PURTab />)

    expect(screen.queryByRole('button', { name: /jobs? failed/i })).toBeNull()
  })

  // Control for the assertion above: the same query finds the panel when the
  // run really did fail, so its absence there is evidence and not a blind spot.
  it('still shows the failure panel for an ordinary failure', () => {
    useJobStore.setState({
      workflowState: 'completed',
      jobRows: [failedRow('Run_1'), doneRow('Run_2')],
    })

    render(<PURTab />)

    expect(screen.getByRole('button', { name: /jobs? failed/i })).toHaveTextContent('1 job failed')
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

  // A job that failed and then completed keeps its old error in the row: it is
  // done, not failed, in the tally and the failure panel, as in the run view.
  it('counts a completed job with an old error as done', () => {
    useJobStore.setState({
      workflowState: 'completed',
      jobRows: [{ ...doneRow('Run_1'), error: 'FAKE earlier upload failure' }],
    })

    render(<PURTab />)

    expect(screen.getByText('Pipeline Complete!')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /jobs? failed/i })).toBeNull()
  })
})
