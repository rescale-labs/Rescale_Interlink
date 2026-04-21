import { beforeEach, describe, expect, it, vi } from 'vitest'
import * as App from '../../wailsjs/go/wailsapp/App'
import { wailsapp } from '../../wailsjs/go/models'
import { useFileBrowserStore } from './fileBrowserStore'

// Build a FolderContentsDTO-shaped object and cast through unknown to
// satisfy the generated TS types without running `new FolderContentsDTO(...)`
// (which has convertValues constructor coupling we don't need in tests).
function mockContents(overrides: Partial<wailsapp.FolderContentsDTO> = {}): wailsapp.FolderContentsDTO {
  return {
    folderId: '',
    folderPath: '',
    items: [],
    hasMore: false,
    nextCursor: '',
    warning: '',
    isSlowPath: false,
    ...overrides,
  } as unknown as wailsapp.FolderContentsDTO
}

function mockFileItem(overrides: Partial<wailsapp.FileItemDTO> = {}): wailsapp.FileItemDTO {
  return {
    id: '',
    name: '',
    isFolder: false,
    size: 0,
    modTime: '',
    path: '',
    ...overrides,
  } as unknown as wailsapp.FileItemDTO
}

function resetLocal() {
  useFileBrowserStore.setState({
    local: {
      currentPath: '',
      items: [],
      isLoading: false,
      error: null,
      warning: null,
      showHidden: false,
      history: [],
      navGeneration: 0,
      selection: { selectedIds: new Set(), lastSelectedId: null },
    },
  })
}

describe('loadLocalDirectory', () => {
  beforeEach(() => {
    resetLocal()
    vi.clearAllMocks()
  })

  it('happy path: sets items, clears error/warning', async () => {
    vi.mocked(App.ListLocalDirectoryEx).mockResolvedValueOnce(
      mockContents({
        folderId: '/home/user',
        folderPath: '/home/user',
        items: [mockFileItem({ id: '/home/user/a.txt', name: 'a.txt', size: 10, path: '/home/user/a.txt' })],
      })
    )

    await useFileBrowserStore.getState().loadLocalDirectory('/home/user')

    const s = useFileBrowserStore.getState().local
    expect(s.items).toHaveLength(1)
    expect(s.error).toBeNull()
    expect(s.warning).toBeNull()
    expect(s.currentPath).toBe('/home/user')
  })

  it('hard error (warning + !isSlowPath): sets error, clears warning, empties items', async () => {
    vi.mocked(App.ListLocalDirectoryEx).mockResolvedValueOnce(
      mockContents({
        folderId: '/bad',
        folderPath: '/bad',
        items: [],
        warning: 'open /bad: permission denied',
        isSlowPath: false,
      })
    )

    await useFileBrowserStore.getState().loadLocalDirectory('/bad')

    const s = useFileBrowserStore.getState().local
    expect(s.error).toBe('open /bad: permission denied')
    expect(s.warning).toBeNull()
    expect(s.items).toHaveLength(0)
  })

  it('slow path (warning + isSlowPath): sets warning, keeps items, clears error', async () => {
    vi.mocked(App.ListLocalDirectoryEx).mockResolvedValueOnce(
      mockContents({
        folderId: '/slow',
        folderPath: '/slow',
        items: [mockFileItem({ id: '/slow/x', name: 'x', path: '/slow/x' })],
        warning: 'Directory listing took 6.2s',
        isSlowPath: true,
      })
    )

    await useFileBrowserStore.getState().loadLocalDirectory('/slow')

    const s = useFileBrowserStore.getState().local
    expect(s.warning).toBe('Directory listing took 6.2s')
    expect(s.error).toBeNull()
    expect(s.items).toHaveLength(1)
  })

  it('cancellation warning is dropped silently (no error, no warning, no state change)', async () => {
    vi.mocked(App.ListLocalDirectoryEx).mockResolvedValueOnce(
      mockContents({
        folderId: '/cancelled',
        folderPath: '/cancelled',
        items: [],
        warning: 'Operation cancelled',
        isSlowPath: false,
      })
    )

    await useFileBrowserStore.getState().loadLocalDirectory('/cancelled')

    const s = useFileBrowserStore.getState().local
    expect(s.error).toBeNull()
    expect(s.warning).toBeNull()
    // currentPath must NOT be set to the cancelled path — a newer call owns it.
    expect(s.currentPath).toBe('')
  })

  it('stale response (superseded by newer call) is discarded', async () => {
    let resolveFirst: (v: wailsapp.FolderContentsDTO) => void = () => {}
    vi.mocked(App.ListLocalDirectoryEx).mockImplementationOnce(
      () => new Promise<wailsapp.FolderContentsDTO>((r) => {
        resolveFirst = r
      })
    )
    vi.mocked(App.ListLocalDirectoryEx).mockResolvedValueOnce(
      mockContents({
        folderId: '/second',
        folderPath: '/second',
        items: [mockFileItem({ id: '/second/y', name: 'y', path: '/second/y' })],
      })
    )

    const firstPromise = useFileBrowserStore.getState().loadLocalDirectory('/first')
    await useFileBrowserStore.getState().loadLocalDirectory('/second')
    resolveFirst(
      mockContents({
        folderId: '/first',
        folderPath: '/first',
        items: [mockFileItem({ id: '/first/z', name: 'z', path: '/first/z' })],
      })
    )
    await firstPromise

    const s = useFileBrowserStore.getState().local
    // Second call's result must win, not the late-arriving first.
    expect(s.currentPath).toBe('/second')
    expect(s.items).toHaveLength(1)
    expect(s.items[0].name).toBe('y')
  })

  it('passes showHidden to ListLocalDirectoryEx (Go-side enforcement)', async () => {
    useFileBrowserStore.setState((state) => ({
      local: { ...state.local, showHidden: true },
    }))
    vi.mocked(App.ListLocalDirectoryEx).mockResolvedValueOnce(
      mockContents({ folderId: '/h', folderPath: '/h' })
    )

    await useFileBrowserStore.getState().loadLocalDirectory('/h')

    expect(App.ListLocalDirectoryEx).toHaveBeenCalledWith('/h', true)
  })
})
