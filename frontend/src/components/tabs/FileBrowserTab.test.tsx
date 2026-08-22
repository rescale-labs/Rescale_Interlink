import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { FileBrowserTab } from './FileBrowserTab'
import * as App from '../../../wailsjs/go/wailsapp/App'
import { wailsapp } from '../../../wailsjs/go/models'
import { RemoteBrowserState, useFileBrowserStore } from '../../stores'

// The tab reads the active tab name from App's navigation context; mock the
// hook so the tab can be mounted on its own.
vi.mock('../../App', () => ({
  useTabNavigation: () => ({ activeTabName: 'File Browser', switchToTab: vi.fn() }),
}))

const fileItem = (id: string, name: string) => ({
  id, name, isFolder: false, size: 12, modTime: '', path: id,
}) as unknown as wailsapp.FileItemDTO

const contents = (folderId: string) => ({
  folderId, folderPath: folderId, items: [], hasMore: false, nextCursor: '', warning: '',
}) as unknown as wailsapp.FolderContentsDTO

const LOCAL_FILE = fileItem('/home/user/a.txt', 'a.txt')

// The remote pane resolves its root folder ids on mount when they are missing,
// and the local pane loads the home directory when it has no path. Both are
// pre-seeded so each test starts from the view it is about to exercise, with
// one file already selected for upload.
function seedStore(remote: Partial<RemoteBrowserState>) {
  useFileBrowserStore.setState(state => ({
    local: {
      ...state.local,
      currentPath: '/home/user',
      items: [LOCAL_FILE],
      isLoading: false,
      error: null,
      warning: null,
      selection: { selectedIds: new Set([LOCAL_FILE.id]), lastSelectedId: LOCAL_FILE.id },
    },
    remote: {
      ...state.remote,
      mode: 'library',
      currentFolderId: '',
      items: [],
      isLoading: false,
      error: null,
      breadcrumb: [],
      myLibraryId: 'lib-folder-123',
      myJobsId: 'jobs-folder-456',
      selection: { selectedIds: new Set(), lastSelectedId: null },
      pageCache: new Map(),
      ...remote,
    },
  }))
}

// The Upload button is the only control in the local pane's header, and its
// label is the assertion target, so it cannot be queried by its own text.
function uploadButton(): HTMLButtonElement {
  const header = screen.getByText('Local Files').parentElement as HTMLElement
  return within(header).getByRole('button') as HTMLButtonElement
}

const IN_JOB_OUTPUT: Partial<RemoteBrowserState> = {
  mode: 'jobs',
  currentFolderId: 'output-folder-9',
  breadcrumb: [
    { id: 'jobs-folder-456', name: 'My Jobs' },
    { id: 'job-1', name: 'job-1' },
    { id: 'output-folder-9', name: 'Output' },
  ],
}

beforeEach(() => {
  vi.clearAllMocks()
})

afterEach(() => {
  cleanup()
})

describe('FileBrowserTab upload gating', () => {
  it('refuses to upload from a job output folder', () => {
    seedStore(IN_JOB_OUTPUT)
    render(<FileBrowserTab />)

    expect(uploadButton()).toBeDisabled()
    expect(uploadButton()).toHaveTextContent('N/A in Jobs view')
  })

  it('stays disabled while the load triggered by a mode switch is in flight', async () => {
    seedStore(IN_JOB_OUTPUT)
    let release: (v: wailsapp.FolderContentsDTO) => void = () => {}
    vi.mocked(App.ListRemoteFolderPage).mockImplementation(
      () => new Promise<wailsapp.FolderContentsDTO>(r => { release = r })
    )

    render(<FileBrowserTab />)
    fireEvent.click(screen.getByRole('button', { name: 'My Library' }))

    await waitFor(() => expect(uploadButton()).toHaveTextContent('Loading folder...'))
    expect(uploadButton()).toBeDisabled()

    await act(async () => { release(contents('lib-folder-123')) })

    await waitFor(() => expect(uploadButton()).toBeEnabled())
    expect(uploadButton()).toHaveTextContent('Upload 1')
  })

  it('stays disabled when the folder load fails', async () => {
    seedStore(IN_JOB_OUTPUT)
    vi.mocked(App.ListRemoteFolderPage).mockRejectedValue(new Error('network unreachable'))

    render(<FileBrowserTab />)
    fireEvent.click(screen.getByRole('button', { name: 'My Library' }))

    await waitFor(() => expect(uploadButton()).toHaveTextContent('Folder unavailable'))
    expect(uploadButton()).toBeDisabled()
  })
})

describe('FileBrowserTab upload destination', () => {
  it('refuses mid-switch, then uploads to the folder the new view resolved', async () => {
    seedStore(IN_JOB_OUTPUT)
    let release: (v: wailsapp.FolderContentsDTO) => void = () => {}
    vi.mocked(App.ListRemoteFolderPage).mockImplementation(
      () => new Promise<wailsapp.FolderContentsDTO>(r => { release = r })
    )

    render(<FileBrowserTab />)
    fireEvent.click(screen.getByRole('button', { name: 'My Library' }))

    // FR-1: the view has left the job's Output folder but nothing has resolved
    // the new destination yet. Uploading here used to open a dialog naming
    // "My Library" and register the transfer against the Output folder, which
    // the API rejects as immutable once every byte has already moved.
    await waitFor(() => expect(uploadButton()).toHaveTextContent('Loading folder...'))
    fireEvent.click(uploadButton())
    expect(screen.queryByText('Confirm Upload')).toBeNull()
    expect(App.StartTransfers).not.toHaveBeenCalled()

    await act(async () => { release(contents('lib-folder-123')) })
    await waitFor(() => expect(uploadButton()).toBeEnabled())

    fireEvent.click(uploadButton())

    // The dialog must name the folder the transfer will actually target.
    await screen.findByText('Upload 1 item(s) to: My Library')

    await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Upload' })) })

    await waitFor(() => expect(App.StartTransfers).toHaveBeenCalled())
    expect(App.ValidateRemoteFolder).toHaveBeenCalledWith('lib-folder-123')
    expect(App.StartTransfers).toHaveBeenCalledWith([
      expect.objectContaining({ type: 'upload', source: LOCAL_FILE.id, dest: 'lib-folder-123' }),
    ])
  })

  it('names the browsed subfolder in the dialog and uploads there', async () => {
    seedStore({
      currentFolderId: 'sub-1',
      breadcrumb: [{ id: 'lib-folder-123', name: 'My Library' }, { id: 'sub-1', name: 'Sub' }],
    })

    render(<FileBrowserTab />)
    expect(uploadButton()).toBeEnabled()

    fireEvent.click(uploadButton())
    await screen.findByText('Upload 1 item(s) to: My Library > Sub')

    await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Upload' })) })

    await waitFor(() => expect(App.StartTransfers).toHaveBeenCalled())
    expect(App.StartTransfers).toHaveBeenCalledWith([
      expect.objectContaining({ dest: 'sub-1' }),
    ])
  })

  it('uploads from Legacy Files to the library root and says so', async () => {
    seedStore({
      mode: 'legacy',
      currentFolderId: '',
      breadcrumb: [{ id: '', name: 'Legacy Files' }],
    })

    render(<FileBrowserTab />)
    expect(uploadButton()).toBeEnabled()

    fireEvent.click(uploadButton())
    await screen.findByText('Upload 1 item(s) to: My Library')

    await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Upload' })) })

    await waitFor(() => expect(App.StartTransfers).toHaveBeenCalled())
    expect(App.StartTransfers).toHaveBeenCalledWith([
      expect.objectContaining({ dest: 'lib-folder-123' }),
    ])
  })
})
