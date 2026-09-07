import { describe, it, expect, afterEach } from 'vitest'
import { render, screen, fireEvent, cleanup } from '@testing-library/react'
import { ErrorSummary } from './ErrorSummary'
import type { JobRow } from '../../types/jobs'

afterEach(cleanup)

const row = (jobName: string, index: number, overrides: Partial<JobRow> = {}): JobRow => ({
  index,
  directory: `/scratch/${jobName}`,
  jobName,
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

// The ambiguity text sits in the same column a failure's message does.
const unconfirmed = (jobName: string, index: number) => row(jobName, index, {
  createStatus: 'indeterminate',
  submitStatus: 'indeterminate',
  status: 'indeterminate',
  jobId: '',
  error: 'job may exist: the answer to the create request was lost',
})

const failed = (jobName: string, index: number) => row(jobName, index, {
  createStatus: 'failed',
  submitStatus: 'failed',
  status: 'failed',
  jobId: '',
  error: 'create rejected: invalid core type',
})

describe('ErrorSummary', () => {
  it('does not count an unconfirmed creation as a failure', () => {
    render(<ErrorSummary jobs={[unconfirmed('Run_1', 1), failed('Run_2', 2)]} />)

    expect(screen.getByRole('button', { name: /1 job failed/ })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button'))
    expect(screen.getByText(/create rejected/)).toBeInTheDocument()
    expect(screen.queryByText(/job may exist/)).toBeNull()
  })

  it('renders no failure panel when every row carrying text is unconfirmed', () => {
    const { container } = render(
      <ErrorSummary jobs={[unconfirmed('Run_1', 1), unconfirmed('Run_2', 2)]} />
    )

    expect(container).toBeEmptyDOMElement()
  })

  it('still counts ordinary failures', () => {
    render(<ErrorSummary jobs={[failed('Run_1', 1), failed('Run_2', 2), row('Run_3', 3)]} />)

    expect(screen.getByRole('button', { name: /2 jobs failed/ })).toBeInTheDocument()
  })
})
