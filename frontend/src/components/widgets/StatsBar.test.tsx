import { it, expect } from 'vitest'
import { render } from '@testing-library/react'
import { StatsBar } from './StatsBar'
import type { JobRow } from '../../types/jobs'

// A create call answered with a rejection is a failure at once, as the run
// header counts it, even while the row's submit status is a poll's stale
// 'creating'.
it('StatsBar counts a job whose creation was rejected as failed', () => {
  const rejected: JobRow = {
    index: 1, directory: '/scratch/job1', jobName: 'job1', tarStatus: 'completed', uploadStatus: 'completed',
    uploadProgress: 100, createStatus: 'failed', submitStatus: 'creating', status: 'creating', jobId: '',
    progress: 0, error: 'FAKE create rejected',
  }
  const { container } = render(<StatsBar jobs={[rejected]} />)
  expect(container.textContent).toContain('Pending: 0')
  expect(container.textContent).toContain('Failed: 1')
})
