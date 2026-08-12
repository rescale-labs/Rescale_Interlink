import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { RemoteFilePicker } from './RemoteFilePicker'
import * as App from '../../../wailsjs/go/wailsapp/App'
import { wailsapp } from '../../../wailsjs/go/models'
import { useConfigStore } from '../../stores'

// FileList virtualizes its rows, and jsdom gives every element zero height, so
// no row ever renders here. These tests assert on what is observable instead:
// the binding calls the picker makes, the breadcrumb, and FileList's
// empty-state message (which renders only while the item list is empty).
const fileItem = (name: string) => ({
  id: name, name, isFolder: false, size: 1, modTime: '', path: name,
}) as unknown as wailsapp.FileItemDTO

const contents = (names: string[] = []) => ({
  folderId: 'folder', folderPath: 'path', items: names.map(fileItem),
  hasMore: false, nextCursor: '',
}) as unknown as wailsapp.FolderContentsDTO

function setAPIKey(apiKey: string) {
  useConfigStore.setState({ config: { apiKey } as unknown as wailsapp.ConfigDTO })
}

function renderPicker() {
  const onSelect = vi.fn()
  const onClose = vi.fn()
  const view = render(<RemoteFilePicker isOpen onClose={onClose} onSelect={onSelect} />)
  return { ...view, onSelect, onClose }
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(App.GetMyLibraryFolderID).mockResolvedValue('lib-a')
  vi.mocked(App.GetMyJobsFolderID).mockResolvedValue('jobs-a')
  vi.mocked(App.ListRemoteFolder).mockResolvedValue(contents())
  vi.mocked(App.ListRemoteLegacy).mockResolvedValue(contents())
  setAPIKey('key-a')
})

afterEach(() => {
  cleanup()
  useConfigStore.setState({ config: null })
})

describe('RemoteFilePicker workspace invalidation', () => {
  it('re-resolves the root folders when the API key changes', async () => {
    renderPicker()
    await waitFor(() => expect(App.ListRemoteFolder).toHaveBeenCalledWith('lib-a'))
    expect(App.GetMyLibraryFolderID).toHaveBeenCalledTimes(1)

    // A different key is a different workspace: the cached root IDs and listing
    // belong to an account that is no longer active.
    vi.mocked(App.GetMyLibraryFolderID).mockResolvedValue('lib-b')
    vi.mocked(App.GetMyJobsFolderID).mockResolvedValue('jobs-b')
    await act(async () => { setAPIKey('key-b') })

    await waitFor(() => expect(App.GetMyLibraryFolderID).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(App.ListRemoteFolder).toHaveBeenLastCalledWith('lib-b'))
  })

  it('drops the previous workspace listing on the key change', async () => {
    vi.mocked(App.ListRemoteFolder).mockResolvedValue(contents(['old-workspace.dat']))
    renderPicker()
    await waitFor(() => expect(App.ListRemoteFolder).toHaveBeenCalledWith('lib-a'))
    // FileList shows its loading/empty message only while the item list is
    // empty, so its absence here means the old workspace's rows are loaded.
    await waitFor(() => expect(screen.queryByText('Loading...')).toBeNull())
    expect(screen.queryByText('Your library is empty')).toBeNull()

    // The new workspace's listing never resolves, so the only thing that can
    // clear the old items is the invalidation itself.
    vi.mocked(App.GetMyLibraryFolderID).mockResolvedValue('lib-b')
    vi.mocked(App.ListRemoteFolder).mockImplementation(() => new Promise(() => {}))
    await act(async () => { setAPIKey('key-b') })

    await screen.findByText('Loading...')
  })

  it('leaves the cache alone when the key is unchanged', async () => {
    const { rerender } = renderPicker()
    await waitFor(() => expect(App.ListRemoteFolder).toHaveBeenCalledTimes(1))

    await act(async () => { setAPIKey('key-a') })
    rerender(<RemoteFilePicker isOpen onClose={() => {}} onSelect={() => {}} />)

    expect(App.GetMyLibraryFolderID).toHaveBeenCalledTimes(1)
    expect(App.ListRemoteFolder).toHaveBeenCalledTimes(1)
  })
})

describe('RemoteFilePicker stale responses', () => {
  it('discards a listing that resolves after the user navigated elsewhere', async () => {
    renderPicker()
    await waitFor(() => expect(App.ListRemoteFolder).toHaveBeenCalledWith('lib-a'))
    await screen.findByText('Your library is empty')

    // My Jobs listing hangs, and would arrive carrying items.
    let releaseJobs: () => void = () => {}
    vi.mocked(App.ListRemoteFolder).mockImplementation((folderId: string) => {
      if (folderId === 'jobs-a') {
        return new Promise(resolve => {
          releaseJobs = () => resolve(contents(['from-my-jobs.dat']))
        })
      }
      return Promise.resolve(contents())
    })

    fireEvent.click(screen.getByRole('button', { name: 'My Jobs' }))
    await waitFor(() => expect(App.ListRemoteFolder).toHaveBeenCalledWith('jobs-a'))

    // Back to My Library before My Jobs answers.
    fireEvent.click(screen.getByRole('button', { name: 'My Library' }))
    await waitFor(() => expect(App.ListRemoteFolder).toHaveBeenLastCalledWith('lib-a'))
    await screen.findByText('Your library is empty')

    // The superseded response now arrives.
    await act(async () => { releaseJobs() })

    // It must not repopulate the list, add its breadcrumb, or become the folder
    // a refresh would re-request.
    expect(screen.getByText('Your library is empty')).toBeTruthy()
    const crumbs = screen.getAllByRole('button').map(b => b.textContent)
    expect(crumbs.filter(c => c === 'My Jobs')).toHaveLength(1) // the mode toggle only

    vi.mocked(App.ListRemoteFolder).mockResolvedValue(contents())
    fireEvent.click(screen.getByTitle('Refresh'))
    await waitFor(() => expect(App.ListRemoteFolder).toHaveBeenLastCalledWith('lib-a'))
  })

  it('a superseded listing does not clear the spinner the newest request owns', async () => {
    renderPicker()
    await screen.findByText('Your library is empty')

    // Both navigations hang, so the only thing that can end the loading state is
    // a response, and only the newest one is entitled to.
    let releaseJobs: () => void = () => {}
    let releaseLibrary: () => void = () => {}
    vi.mocked(App.ListRemoteFolder).mockImplementation((folderId: string) => {
      if (folderId === 'jobs-a') {
        return new Promise(resolve => { releaseJobs = () => resolve(contents()) })
      }
      return new Promise(resolve => { releaseLibrary = () => resolve(contents()) })
    })

    fireEvent.click(screen.getByRole('button', { name: 'My Jobs' }))
    await waitFor(() => expect(screen.getByTitle('Refresh')).toBeDisabled())

    fireEvent.click(screen.getByRole('button', { name: 'My Library' }))
    await waitFor(() => expect(App.ListRemoteFolder).toHaveBeenLastCalledWith('lib-a'))

    // My Jobs answers late while the library request that superseded it is still
    // running: the spinner belongs to the library request.
    await act(async () => { releaseJobs() })
    expect(screen.getByTitle('Refresh')).toBeDisabled()

    await act(async () => { releaseLibrary() })
    await waitFor(() => expect(screen.getByTitle('Refresh')).toBeEnabled())
  })
})
