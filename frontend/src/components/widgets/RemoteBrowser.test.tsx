import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react'
import { RemoteBrowser } from './RemoteBrowser'
import * as App from '../../../wailsjs/go/wailsapp/App'
import { wailsapp } from '../../../wailsjs/go/models'
import { RemoteBrowserState, useFileBrowserStore } from '../../stores'

const ROOT = { id: 'lib-folder-123', name: 'My Library' }

function seed(patch: Partial<RemoteBrowserState>) {
  useFileBrowserStore.setState(state => ({ remote: { ...state.remote, ...patch } }))
}

beforeEach(() => {
  vi.useFakeTimers()
  vi.clearAllMocks()
  // Root ids are pre-seeded so the browser does not resolve them on mount.
  seed({
    mode: 'library',
    currentFolderId: ROOT.id,
    breadcrumb: [ROOT],
    items: [],
    isLoading: false,
    error: null,
    myLibraryId: ROOT.id,
    myJobsId: 'jobs-folder-456',
    librarySearchQuery: '',
    pageCache: new Map(),
  })
})

afterEach(() => {
  cleanup()
  vi.useRealTimers()
})

describe('RemoteBrowser search box', () => {
  it('drops a search typed just before a view switch', async () => {
    render(<RemoteBrowser />)

    fireEvent.change(screen.getByPlaceholderText('Search for files within "My Library"'), {
      target: { value: 'few-file1' },
    })
    // Inside the search box's debounce, so the search has not been sent yet.
    act(() => { vi.advanceTimersByTime(300) })
    fireEvent.click(screen.getByRole('button', { name: 'My Jobs' }))
    await act(async () => { vi.advanceTimersByTime(1000) })

    expect(App.SearchRemoteFolderContents).not.toHaveBeenCalled()
    expect(App.ListRemoteFolderPage).toHaveBeenLastCalledWith('jobs-folder-456', '', 25)
    expect(screen.getByPlaceholderText('Search for files within "My Jobs"')).toHaveValue('')
  })
})

describe('RemoteBrowser empty state', () => {
  it.each<[string, Partial<RemoteBrowserState>]>([
    ['at the library root', {}],
    ['in a subfolder', { currentFolderId: 'sub-1', breadcrumb: [ROOT, { id: 'sub-1', name: 'Sub' }] }],
    ['in My Jobs', { mode: 'jobs', currentFolderId: 'jobs-folder-456', breadcrumb: [{ id: 'jobs-folder-456', name: 'My Jobs' }] }],
  ])('says no files match a search that found nothing %s', (_, where) => {
    seed({ ...where, librarySearchQuery: 'zzz' })
    render(<RemoteBrowser />)

    expect(screen.getByText('No files match your search.')).toBeInTheDocument()
    expect(screen.queryByText('Your library is empty')).toBeNull()
    expect(screen.queryByText('This folder is empty')).toBeNull()
  })

  it('shows the error of a listing that failed, not an empty library', async () => {
    const warning = 'Rate limit exceeded - please wait a moment and try again'
    vi.mocked(App.ListRemoteFolderPage).mockResolvedValueOnce(
      { folderId: ROOT.id, folderPath: '', items: [], hasMore: false, nextCursor: '', warning } as unknown as wailsapp.FolderContentsDTO
    )
    render(<RemoteBrowser />)

    await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Refresh' })) })

    expect(screen.getByText(warning)).toBeInTheDocument()
    expect(screen.queryByText('Your library is empty')).toBeNull()
  })
})

describe('RemoteBrowser New Folder dialog', () => {
  const nameField = () => screen.queryByPlaceholderText('Folder name')
  const create = async (name: string) => {
    fireEvent.change(nameField()!, { target: { value: name } })
    await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Create' })) })
  }

  it('keeps the dialog and the typed name when a folder is refused, and shows why as worded', async () => {
    vi.mocked(App.CreateRemoteFolder).mockRejectedValueOnce('A folder named "runs" already exists')
    render(<RemoteBrowser />)

    fireEvent.click(screen.getByRole('button', { name: 'Create new folder' }))
    await create('runs')

    expect(screen.getByText('A folder named "runs" already exists')).toBeInTheDocument()
    expect(nameField()).toHaveValue('runs')

    // A reopened dialog starts clean.
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    fireEvent.click(screen.getByRole('button', { name: 'Create new folder' }))
    expect(screen.queryByText('A folder named "runs" already exists')).toBeNull()

    await create('runs-2')

    expect(App.CreateRemoteFolder).toHaveBeenLastCalledWith('runs-2', ROOT.id)
    expect(nameField()).toBeNull()
  })
})
