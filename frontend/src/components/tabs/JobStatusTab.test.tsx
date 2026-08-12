import { describe, it, expect, vi, afterEach } from 'vitest'
import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/react'
import { JobStatusTab } from './JobStatusTab'
import { ListJobStatuses, ListJobStatusesPage } from '../../../wailsjs/go/wailsapp/App'
import { wailsapp } from '../../../wailsjs/go/models'

// The tab reads the active tab name from App's navigation context; mock the hook
// so tests can drive tab switches without mounting the whole App.
const nav = { activeTabName: 'Job Status', switchToTab: vi.fn() }
vi.mock('../../App', () => ({
  useTabNavigation: () => nav,
}))

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
  nav.activeTabName = 'Job Status'
})

const job = (id: string) => ({
  id,
  name: `job-${id}`,
  status: 'Completed',
  reason: '',
  createdAt: '2026-01-01',
})

const page = (ids: string[], hasMore: boolean) =>
  wailsapp.JobStatusListDTO.createFrom({
    jobs: ids.map(job),
    hasMore,
    fetchErrors: 0,
  })

describe('JobStatusTab', () => {
  it('fetches on activation and pages with Load next', async () => {
    vi.mocked(ListJobStatuses).mockResolvedValue(page(['a'], true))
    vi.mocked(ListJobStatusesPage).mockResolvedValue(page(['b'], false))

    render(<JobStatusTab />)
    await screen.findByText('job-a')

    fireEvent.click(screen.getByRole('button', { name: /load next/i }))
    await screen.findByText('job-b')
    expect(ListJobStatusesPage).toHaveBeenCalledWith(1)
    expect(screen.queryByRole('button', { name: /load next/i })).toBeNull()
  })

  it('disables Refresh while a Load next request is in flight', async () => {
    vi.mocked(ListJobStatuses).mockResolvedValue(page(['a'], true))
    vi.mocked(ListJobStatusesPage).mockImplementation(() => new Promise(() => {}))

    render(<JobStatusTab />)
    await screen.findByText('job-a')

    fireEvent.click(screen.getByRole('button', { name: /load next/i }))
    await waitFor(() =>
      expect(screen.getByTitle('Refresh job list')).toBeDisabled()
    )
  })

  it('recovers Load next when a tab-switch refresh supersedes an in-flight page load', async () => {
    // Regression: a refresh triggered mid-load bumps the fetch generation; the
    // stale load's cleanup used to skip resetting isLoadingMore, leaving the
    // Load next button disabled forever.
    vi.mocked(ListJobStatuses).mockResolvedValue(page(['a'], true))
    let resolveStalePage: (v: wailsapp.JobStatusListDTO) => void = () => {}
    vi.mocked(ListJobStatusesPage).mockImplementation(
      () => new Promise(res => { resolveStalePage = res })
    )

    const { rerender } = render(<JobStatusTab />)
    await screen.findByText('job-a')

    fireEvent.click(screen.getByRole('button', { name: /load next/i }))
    await waitFor(() =>
      expect(screen.getByRole('button', { name: /loading/i })).toBeDisabled()
    )

    // Leave the tab and come back: the activation effect refetches, superseding
    // the in-flight page load.
    nav.activeTabName = 'Transfers'
    rerender(<JobStatusTab />)
    nav.activeTabName = 'Job Status'
    rerender(<JobStatusTab />)
    await waitFor(() => expect(ListJobStatuses).toHaveBeenCalledTimes(2))

    // The stale page resolves after being superseded.
    resolveStalePage(page(['stale'], true))

    // Its jobs must not append, and Load next must be usable again.
    await waitFor(() =>
      expect(screen.getByRole('button', { name: /load next/i })).toBeEnabled()
    )
    expect(screen.queryByText('job-stale')).toBeNull()
  })
})
