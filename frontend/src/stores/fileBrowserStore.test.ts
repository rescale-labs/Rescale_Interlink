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

function resetRemote() {
  useFileBrowserStore.setState({
    remote: {
      mode: 'library',
      currentFolderId: '',
      items: [],
      isLoading: false,
      error: null,
      breadcrumb: [],
      hasMore: false,
      nextCursor: '',
      myLibraryId: 'lib-folder-123',
      myJobsId: 'jobs-folder-456',
      navGeneration: 0,
      selection: { selectedIds: new Set(), lastSelectedId: null },
      currentPage: 0,
      itemsPerPage: 25,
      pageCursors: [''],
      knownTotalPages: 1,
      pageCache: new Map(),
      legacyOwnerFilter: '0',
      legacySearchQuery: '',
      legacySortField: 'created',
      legacySortDirection: 'desc',
      librarySearchQuery: '',
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

describe('remote trash browser', () => {
  beforeEach(() => {
    resetRemote()
    vi.clearAllMocks()
  })

  it('loads trash through the trash endpoint and sets trash breadcrumb', async () => {
    vi.mocked(App.ListRemoteTrash).mockResolvedValueOnce(
      mockContents({
        folderId: 'trash',
        folderPath: 'Trash',
        items: [mockFileItem({ id: 'file-1', name: 'result.dat', symlinkId: 'filesymlink-1' })],
      })
    )

    await useFileBrowserStore.getState().loadRemoteTrash()

    const s = useFileBrowserStore.getState().remote
    expect(App.ListRemoteTrash).toHaveBeenCalledWith('', 25)
    expect(s.currentFolderId).toBe('trash')
    expect(s.breadcrumb).toEqual([{ id: 'trash', name: 'Trash' }])
    expect(s.items[0].symlinkId).toBe('filesymlink-1')
  })

  it('routes trash breadcrumb clicks back through the trash endpoint', () => {
    useFileBrowserStore.setState((state) => ({
      remote: {
        ...state.remote,
        mode: 'trash',
        currentFolderId: 'trash',
        breadcrumb: [{ id: 'trash', name: 'Trash' }],
      },
    }))
    vi.mocked(App.ListRemoteTrash).mockResolvedValueOnce(mockContents({ folderId: 'trash', folderPath: 'Trash' }))

    useFileBrowserStore.getState().navigateRemoteToBreadcrumb(0)

    expect(App.ListRemoteTrash).toHaveBeenCalledWith('', 25)
    expect(App.ListRemoteFolderPage).not.toHaveBeenCalled()
  })

  it('recovering trash items refreshes trash and clears selection', async () => {
    const item = mockFileItem({ id: 'file-1', name: 'result.dat', symlinkId: 'filesymlink-1' })
    useFileBrowserStore.setState((state) => ({
      remote: {
        ...state.remote,
        mode: 'trash',
        currentFolderId: 'trash',
        items: [item],
        selection: { selectedIds: new Set(['file-1']), lastSelectedId: 'file-1' },
      },
    }))
    vi.mocked(App.RecoverTrashItems).mockResolvedValueOnce({ deleted: 1, failed: 0, error: '' })
    vi.mocked(App.ListRemoteTrash).mockResolvedValueOnce(mockContents({ folderId: 'trash', folderPath: 'Trash' }))

    const result = await useFileBrowserStore.getState().recoverTrashItems([item])

    expect(App.RecoverTrashItems).toHaveBeenCalledWith([item])
    expect(result).toEqual({ recovered: 1, failed: 0, error: '' })
    expect(App.ListRemoteTrash).toHaveBeenCalledWith('', 25)
    expect(useFileBrowserStore.getState().remote.selection.selectedIds.size).toBe(0)
  })
})

// The filter setters are synchronous and fire their reload without awaiting it,
// so tests that assert post-reload state need one macrotask turn.
const flush = () => new Promise<void>((r) => setTimeout(r, 0))

function setRemote(patch: Partial<ReturnType<typeof useFileBrowserStore.getState>['remote']>) {
  useFileBrowserStore.setState((state) => ({ remote: { ...state.remote, ...patch } }))
}

describe('My Library search', () => {
  beforeEach(() => {
    resetRemote()
    vi.clearAllMocks()
  })

  it('routes a non-empty query through the search binding, not the listing binding', async () => {
    setRemote({ currentFolderId: 'lib-folder-123', librarySearchQuery: 'mesh' })
    vi.mocked(App.SearchRemoteFolderContents).mockResolvedValueOnce(
      mockContents({ folderId: 'lib-folder-123', items: [mockFileItem({ id: 'f-1', name: 'mesh.stl' })] })
    )

    await useFileBrowserStore.getState().loadRemoteFolder()

    expect(App.SearchRemoteFolderContents).toHaveBeenCalledWith('lib-folder-123', 'mesh', '', 25)
    expect(App.ListRemoteFolderPage).not.toHaveBeenCalled()
    expect(useFileBrowserStore.getState().remote.items).toHaveLength(1)
  })

  it('falls back to the listing binding when the query is empty', async () => {
    setRemote({ currentFolderId: 'lib-folder-123', librarySearchQuery: '' })
    vi.mocked(App.ListRemoteFolderPage).mockResolvedValueOnce(mockContents({ folderId: 'lib-folder-123' }))

    await useFileBrowserStore.getState().loadRemoteFolder()

    expect(App.ListRemoteFolderPage).toHaveBeenCalledWith('lib-folder-123', '', 25)
    expect(App.SearchRemoteFolderContents).not.toHaveBeenCalled()
  })

  it('changing the query resets pagination state', async () => {
    setRemote({
      currentFolderId: 'lib-folder-123',
      currentPage: 2,
      pageCursors: ['', 'c1', 'c2'],
      knownTotalPages: 3,
      pageCache: new Map([[0, { items: [], hasMore: true, nextCursor: 'c1', timestamp: Date.now() }]]),
    })
    vi.mocked(App.SearchRemoteFolderContents).mockResolvedValueOnce(mockContents({ folderId: 'lib-folder-123' }))

    useFileBrowserStore.getState().setLibrarySearchQuery('mesh')
    await flush()

    const s = useFileBrowserStore.getState().remote
    expect(s.librarySearchQuery).toBe('mesh')
    expect(s.currentPage).toBe(0)
    expect(s.pageCursors).toEqual([''])
    expect(s.knownTotalPages).toBe(1)
    expect(s.pageCache.has(2)).toBe(false)
    expect(App.SearchRemoteFolderContents).toHaveBeenCalledWith('lib-folder-123', 'mesh', '', 25)
  })

  it('page two of a search reuses page one\'s cursor', async () => {
    setRemote({ currentFolderId: 'lib-folder-123', librarySearchQuery: 'mesh' })
    vi.mocked(App.SearchRemoteFolderContents).mockResolvedValueOnce(
      mockContents({ folderId: 'lib-folder-123', hasMore: true, nextCursor: 'search-cursor-2' })
    )

    await useFileBrowserStore.getState().loadRemoteFolder()
    expect(App.SearchRemoteFolderContents).toHaveBeenNthCalledWith(1, 'lib-folder-123', 'mesh', '', 25)

    vi.mocked(App.SearchRemoteFolderContents).mockResolvedValueOnce(mockContents({ folderId: 'lib-folder-123' }))
    await useFileBrowserStore.getState().goToNextRemotePage()

    expect(App.SearchRemoteFolderContents).toHaveBeenNthCalledWith(2, 'lib-folder-123', 'mesh', 'search-cursor-2', 25)
    expect(useFileBrowserStore.getState().remote.currentPage).toBe(1)
  })

  it('discards a search response superseded by a newer navigation', async () => {
    setRemote({ currentFolderId: 'lib-folder-123', librarySearchQuery: 'mesh' })

    let resolveSearch: (v: wailsapp.FolderContentsDTO) => void = () => {}
    vi.mocked(App.SearchRemoteFolderContents).mockImplementationOnce(
      () => new Promise<wailsapp.FolderContentsDTO>((r) => {
        resolveSearch = r
      })
    )
    vi.mocked(App.SearchRemoteFolderContents).mockResolvedValueOnce(
      mockContents({ folderId: 'sub-folder', items: [mockFileItem({ id: 'f-new', name: 'newer.txt' })] })
    )

    const stalePromise = useFileBrowserStore.getState().loadRemoteFolder()
    useFileBrowserStore.getState().navigateRemoteTo('sub-folder', 'Sub')
    await flush()

    resolveSearch(
      mockContents({ folderId: 'lib-folder-123', items: [mockFileItem({ id: 'f-stale', name: 'stale.txt' })] })
    )
    await stalePromise

    const s = useFileBrowserStore.getState().remote
    expect(s.items.map((i) => i.id)).toEqual(['f-new'])
  })

  it('reports a search failure instead of rendering an empty library', async () => {
    setRemote({ currentFolderId: 'lib-folder-123', librarySearchQuery: 'mesh' })
    vi.mocked(App.SearchRemoteFolderContents).mockResolvedValueOnce(
      mockContents({ folderId: 'lib-folder-123', items: [], warning: 'Server error - please try again later' })
    )

    await useFileBrowserStore.getState().loadRemoteFolder()

    const s = useFileBrowserStore.getState().remote
    expect(s.error).toBe('Server error - please try again later')
    expect(s.items).toHaveLength(0)
  })
})

describe('Legacy Files filters', () => {
  beforeEach(() => {
    resetRemote()
    setRemote({ mode: 'legacy' })
    vi.clearAllMocks()
  })

  it('forwards an explicit owner filter', async () => {
    vi.mocked(App.ListRemoteLegacyWithFilters).mockResolvedValueOnce(mockContents({ folderPath: 'Legacy Files' }))

    useFileBrowserStore.getState().setLegacyOwnerFilter('1')
    await flush()

    expect(App.ListRemoteLegacyWithFilters).toHaveBeenCalledWith('', 25, '1', '', 'created', 'desc')
    expect(useFileBrowserStore.getState().remote.legacyOwnerFilter).toBe('1')
  })

  it('forwards the "any owner" default as "0", which the binding drops', async () => {
    vi.mocked(App.ListRemoteLegacyWithFilters).mockResolvedValueOnce(mockContents({ folderPath: 'Legacy Files' }))

    await useFileBrowserStore.getState().loadRemoteLegacy()

    expect(useFileBrowserStore.getState().remote.legacyOwnerFilter).toBe('0')
    expect(App.ListRemoteLegacyWithFilters).toHaveBeenCalledWith('', 25, '0', '', 'created', 'desc')
  })

  it('changing the owner filter resets pagination', async () => {
    setRemote({
      currentPage: 2,
      pageCursors: ['', 'c1', 'c2'],
      knownTotalPages: 3,
      pageCache: new Map([[0, { items: [], hasMore: true, nextCursor: 'c1', timestamp: Date.now() }]]),
    })
    vi.mocked(App.ListRemoteLegacyWithFilters).mockResolvedValueOnce(mockContents({ folderPath: 'Legacy Files' }))

    useFileBrowserStore.getState().setLegacyOwnerFilter('2')
    await flush()

    const s = useFileBrowserStore.getState().remote
    expect(s.currentPage).toBe(0)
    expect(s.pageCursors).toEqual([''])
    expect(s.knownTotalPages).toBe(1)
    expect(s.pageCache.has(2)).toBe(false)
    expect(App.ListRemoteLegacyWithFilters).toHaveBeenCalledWith('', 25, '2', '', 'created', 'desc')
  })

  it('setLegacySort stores both field and direction', async () => {
    vi.mocked(App.ListRemoteLegacyWithFilters).mockResolvedValueOnce(mockContents({ folderPath: 'Legacy Files' }))

    useFileBrowserStore.getState().setLegacySort('name', 'asc')
    await flush()

    const s = useFileBrowserStore.getState().remote
    expect(s.legacySortField).toBe('name')
    expect(s.legacySortDirection).toBe('asc')
  })

  it('changing the sort resets pagination and re-requests page 0', async () => {
    setRemote({
      currentPage: 3,
      pageCursors: ['', 'c1', 'c2', 'c3'],
      knownTotalPages: 4,
      pageCache: new Map([[3, { items: [], hasMore: false, nextCursor: '', timestamp: Date.now() }]]),
    })
    vi.mocked(App.ListRemoteLegacyWithFilters).mockResolvedValueOnce(mockContents({ folderPath: 'Legacy Files' }))

    useFileBrowserStore.getState().setLegacySort('size', 'asc')
    await flush()

    const s = useFileBrowserStore.getState().remote
    expect(s.currentPage).toBe(0)
    expect(s.pageCursors).toEqual([''])
    expect(s.knownTotalPages).toBe(1)
    expect(s.pageCache.has(3)).toBe(false)
    expect(App.ListRemoteLegacyWithFilters).toHaveBeenCalledWith('', 25, '0', '', 'size', 'asc')
  })

  it.each([
    ['name', 'asc'],
    ['name', 'desc'],
    ['size', 'asc'],
    ['created', 'desc'],
  ])('forwards sort field %s / direction %s', async (field, direction) => {
    vi.mocked(App.ListRemoteLegacyWithFilters).mockResolvedValueOnce(mockContents({ folderPath: 'Legacy Files' }))

    useFileBrowserStore.getState().setLegacySort(field, direction)
    await flush()

    expect(App.ListRemoteLegacyWithFilters).toHaveBeenCalledWith('', 25, '0', '', field, direction)
  })

  it('next page passes the stored cursor and preserves the active filters', async () => {
    setRemote({
      legacyOwnerFilter: '1',
      legacySearchQuery: 'abc',
      legacySortField: 'name',
      legacySortDirection: 'asc',
      hasMore: true,
      pageCursors: ['', 'legacy-cursor-2'],
    })
    vi.mocked(App.ListRemoteLegacyWithFilters).mockResolvedValueOnce(mockContents({ folderPath: 'Legacy Files' }))

    await useFileBrowserStore.getState().goToNextRemotePage()

    expect(App.ListRemoteLegacyWithFilters).toHaveBeenCalledWith('legacy-cursor-2', 25, '1', 'abc', 'name', 'asc')
    expect(useFileBrowserStore.getState().remote.currentPage).toBe(1)
  })

  it('reports a listing failure instead of rendering an empty list', async () => {
    vi.mocked(App.ListRemoteLegacyWithFilters).mockResolvedValueOnce(
      mockContents({ folderPath: 'Legacy Files', items: [], warning: 'Rate limit exceeded - please wait a moment and try again' })
    )

    await useFileBrowserStore.getState().loadRemoteLegacy()

    const s = useFileBrowserStore.getState().remote
    expect(s.error).toBe('Rate limit exceeded - please wait a moment and try again')
    expect(s.items).toHaveLength(0)
  })
})

describe('filter setters are scoped to their own mode', () => {
  beforeEach(() => {
    resetRemote()
    vi.clearAllMocks()
  })

  it('ignores legacy setters while My Library is showing', () => {
    setRemote({ mode: 'library' })

    useFileBrowserStore.getState().setLegacySearchQuery('mesh')
    useFileBrowserStore.getState().setLegacyOwnerFilter('1')
    useFileBrowserStore.getState().setLegacySort('name', 'asc')

    const s = useFileBrowserStore.getState().remote
    expect(s.legacySearchQuery).toBe('')
    expect(s.legacyOwnerFilter).toBe('0')
    expect(s.legacySortField).toBe('created')
    expect(s.legacySortDirection).toBe('desc')
    expect(App.ListRemoteLegacyWithFilters).not.toHaveBeenCalled()
  })

  it('ignores the library search setter while Legacy Files is showing', () => {
    setRemote({ mode: 'legacy' })

    useFileBrowserStore.getState().setLibrarySearchQuery('mesh')

    expect(useFileBrowserStore.getState().remote.librarySearchQuery).toBe('')
    expect(App.SearchRemoteFolderContents).not.toHaveBeenCalled()
    expect(App.ListRemoteFolderPage).not.toHaveBeenCalled()
  })

  it('switching modes clears the legacy filters and the library search', async () => {
    setRemote({
      mode: 'legacy',
      legacyOwnerFilter: '1',
      legacySearchQuery: 'abc',
      legacySortField: 'name',
      legacySortDirection: 'asc',
      librarySearchQuery: 'mesh',
    })
    vi.mocked(App.ListRemoteFolderPage).mockResolvedValueOnce(mockContents({ folderId: 'lib-folder-123' }))

    useFileBrowserStore.getState().setRemoteMode('library')
    await flush()

    const s = useFileBrowserStore.getState().remote
    expect(s.legacyOwnerFilter).toBe('0')
    expect(s.legacySearchQuery).toBe('')
    expect(s.legacySortField).toBe('created')
    expect(s.legacySortDirection).toBe('desc')
    expect(s.librarySearchQuery).toBe('')
  })
})
