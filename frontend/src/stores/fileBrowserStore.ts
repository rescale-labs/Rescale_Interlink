import { create } from 'zustand'
import * as App from '../../wailsjs/go/wailsapp/App'
import { EventsOn, EventsOff } from '../../wailsjs/runtime/runtime'
import { wailsapp } from '../../wailsjs/go/models'
import { EVENT_NAMES } from '../types/events'

// Browse mode for remote browser (matching Go BrowseMode)
export type BrowseMode = 'library' | 'jobs' | 'legacy'

const PAGE_CACHE_TTL = 5 * 60 * 1000  // 5 minutes
const MAX_CACHED_PAGES = 10           // Limit memory usage

// Cached page entry for fast back/forward navigation without re-fetching
export interface CachedPage {
  items: wailsapp.FileItemDTO[]
  hasMore: boolean
  nextCursor: string
  timestamp: number
}

// Selection state for file list
export interface SelectionState {
  selectedIds: Set<string>
  lastSelectedId: string | null
}

// Breadcrumb entry for navigation path
export interface BreadcrumbEntry {
  id: string
  name: string
}

// Local browser state
interface LocalBrowserState {
  currentPath: string
  items: wailsapp.FileItemDTO[]
  isLoading: boolean
  error: string | null
  warning: string | null
  showHidden: boolean
  history: string[]
  // Bumped on every new local-nav operation to discard stale in-flight
  // responses. Mirrors remote pane's navGeneration so rapid navigation
  // doesn't show spurious "Operation cancelled" errors from superseded
  // backend calls.
  navGeneration: number
  selection: SelectionState
}

// Cancellation sentinel emitted by ListLocalDirectoryEx when a prior read
// is cancelled by a newer one. Kept in lockstep with the Go constant in
// internal/wailsapp/file_bindings.go (context.Canceled branch). Do not
// surface as a user-facing error — the cancelled call is by definition
// the one the user no longer cares about.
const LOCAL_CANCELLED_WARNING = 'Operation cancelled'

// Remote browser state
interface RemoteBrowserState {
  mode: BrowseMode
  currentFolderId: string
  items: wailsapp.FileItemDTO[]  // Current page's items only (not cumulative)
  isLoading: boolean
  error: string | null
  breadcrumb: BreadcrumbEntry[]
  hasMore: boolean
  nextCursor: string
  myLibraryId: string | null
  myJobsId: string | null
  navGeneration: number             // Bumped on every view-changing operation to discard stale in-flight responses
  selection: SelectionState
  currentPage: number            // 0-indexed page number
  itemsPerPage: number           // User's selected page size (sent to server)
  pageCursors: string[]          // Cursor for each page: [page0='', page1='...', page2='...']
  knownTotalPages: number        // Discovered page count (increments as user navigates forward)
  pageCache: Map<number, CachedPage>  // Cache by page number for fast back/forward
}

interface FileBrowserStore {
  // Local browser state
  local: LocalBrowserState

  // Remote browser state
  remote: RemoteBrowserState

  // Local browser actions
  loadLocalDirectory: (path?: string) => Promise<void>
  navigateLocalTo: (path: string) => void
  goLocalBack: () => void
  goLocalHome: () => void
  refreshLocal: () => void
  toggleShowHidden: () => void
  setLocalSelection: (ids: Set<string>, lastId?: string | null) => void
  clearLocalSelection: () => void

  // Remote browser actions
  initRemote: () => Promise<void>
  setRemoteMode: (mode: BrowseMode) => void
  loadRemoteFolder: (folderId?: string, folderName?: string) => Promise<void>
  loadRemoteLegacy: () => Promise<void>
  navigateRemoteTo: (folderId: string, folderName: string) => void
  navigateRemoteToBreadcrumb: (index: number) => void
  goRemoteBack: () => void
  refreshRemote: () => void
  setRemoteSelection: (ids: Set<string>, lastId?: string | null) => void
  clearRemoteSelection: () => void
  createRemoteFolder: (name: string) => Promise<string | null>
  deleteRemoteItems: (items: wailsapp.FileItemDTO[]) => Promise<{ deleted: number; failed: number }>
  setRemoteItemsPerPage: (size: number) => void       // Change page size, reload page 0
  goToNextRemotePage: () => Promise<void>             // Navigate to next page (replaces items)
  goToPreviousRemotePage: () => Promise<void>         // Navigate to previous page (from cache)

  // Event listeners
  setupEventListeners: () => () => void

  // Common actions
  getLocalSelectedItems: () => wailsapp.FileItemDTO[]
  getRemoteSelectedItems: () => wailsapp.FileItemDTO[]
}

const initialLocalState: LocalBrowserState = {
  currentPath: '',
  items: [],
  isLoading: false,
  error: null,
  warning: null,
  showHidden: false,
  history: [],
  navGeneration: 0,
  selection: { selectedIds: new Set(), lastSelectedId: null },
}

const initialRemoteState: RemoteBrowserState = {
  mode: 'library',
  currentFolderId: '',
  items: [],
  isLoading: false,
  error: null,
  breadcrumb: [],
  hasMore: false,
  nextCursor: '',
  myLibraryId: null,
  myJobsId: null,
  navGeneration: 0,
  selection: { selectedIds: new Set(), lastSelectedId: null },
  currentPage: 0,
  itemsPerPage: 25,            // Default page size
  pageCursors: [''],           // First page has empty cursor
  knownTotalPages: 1,          // At least one page
  pageCache: new Map(),
}

export const useFileBrowserStore = create<FileBrowserStore>((set, get) => ({
  local: initialLocalState,
  remote: initialRemoteState,

  // ===== LOCAL BROWSER ACTIONS =====

  loadLocalDirectory: async (path?: string) => {
    const targetPath = path ?? get().local.currentPath
    const showHidden = get().local.showHidden
    // Bump generation; a stale response from a superseded call will see
    // this advance and bail out instead of writing to the store.
    const myGen = get().local.navGeneration + 1
    set(state => ({
      local: {
        ...state.local,
        isLoading: true,
        error: null,
        warning: null,
        navGeneration: myGen,
      },
    }))

    const isStale = () => get().local.navGeneration !== myGen

    try {
      const contents = await App.ListLocalDirectoryEx(targetPath, showHidden)
      if (isStale()) return

      const warning = (contents as { warning?: string }).warning ?? ''
      const isSlowPath = (contents as { isSlowPath?: boolean }).isSlowPath ?? false

      // Backend cancelled us because a newer call started; the newer call
      // will surface the real result. Drop silently.
      if (warning === LOCAL_CANCELLED_WARNING) return

      if (warning && !isSlowPath) {
        // Hard error: path not found, permission denied, etc. Go has already
        // returned empty items and a humanized message.
        set(state => ({
          local: {
            ...state.local,
            currentPath: contents.folderPath,
            items: [],
            isLoading: false,
            error: warning,
            warning: null,
          },
        }))
        return
      }

      set(state => ({
        local: {
          ...state.local,
          currentPath: contents.folderPath,
          items: contents.items,
          isLoading: false,
          error: null,
          warning: warning && isSlowPath ? warning : null,
        },
      }))
    } catch (error) {
      if (isStale()) return
      set(state => ({
        local: {
          ...state.local,
          isLoading: false,
          error: error instanceof Error ? error.message : String(error),
          warning: null,
        },
      }))
    }
  },

  navigateLocalTo: (path: string) => {
    const { currentPath } = get().local
    // Save current path to history before navigating
    if (currentPath && currentPath !== path) {
      set(state => ({
        local: {
          ...state.local,
          history: [...state.local.history.slice(-49), currentPath],
          selection: { selectedIds: new Set(), lastSelectedId: null },
        }
      }))
    }
    get().loadLocalDirectory(path)
  },

  goLocalBack: () => {
    const { history } = get().local
    if (history.length === 0) return

    const prevPath = history[history.length - 1]
    set(state => ({
      local: {
        ...state.local,
        history: state.local.history.slice(0, -1),
        selection: { selectedIds: new Set(), lastSelectedId: null },
      }
    }))
    get().loadLocalDirectory(prevPath)
  },

  goLocalHome: async () => {
    try {
      const home = await App.GetHomeDirectory()
      get().navigateLocalTo(home)
    } catch (error) {
      console.error('Failed to get home directory:', error)
    }
  },

  refreshLocal: () => {
    get().loadLocalDirectory()
  },

  toggleShowHidden: () => {
    set(state => ({
      local: { ...state.local, showHidden: !state.local.showHidden }
    }))
    get().loadLocalDirectory()
  },

  setLocalSelection: (ids: Set<string>, lastId?: string | null) => {
    set(state => ({
      local: {
        ...state.local,
        selection: {
          selectedIds: ids,
          lastSelectedId: lastId ?? state.local.selection.lastSelectedId
        }
      }
    }))
  },

  clearLocalSelection: () => {
    set(state => ({
      local: {
        ...state.local,
        selection: { selectedIds: new Set(), lastSelectedId: null }
      }
    }))
  },

  // ===== REMOTE BROWSER ACTIONS =====

  initRemote: async () => {
    set(state => ({
      remote: { ...state.remote, isLoading: true, error: null }
    }))

    try {
      // Get root folder IDs
      const [myLibraryId, myJobsId] = await Promise.all([
        App.GetMyLibraryFolderID(),
        App.GetMyJobsFolderID(),
      ])

      set(state => ({
        remote: {
          ...state.remote,
          myLibraryId,
          myJobsId,
        }
      }))

      // Load the appropriate root based on current mode
      const mode = get().remote.mode
      if (mode === 'legacy') {
        await get().loadRemoteLegacy()
      } else {
        const folderId = mode === 'library' ? myLibraryId : myJobsId
        const folderName = mode === 'library' ? 'My Library' : 'My Jobs'
        await get().loadRemoteFolder(folderId, folderName)
      }
    } catch (error) {
      set(state => ({
        remote: {
          ...state.remote,
          isLoading: false,
          error: error instanceof Error ? error.message : String(error),
        }
      }))
    }
  },

  setRemoteMode: (mode: BrowseMode) => {
    const { myLibraryId, myJobsId } = get().remote

    // Stale response guard: bump navGeneration so in-flight responses from the old mode are discarded
    set(state => ({
      remote: {
        ...state.remote,
        mode,
        items: [],
        selection: { selectedIds: new Set(), lastSelectedId: null },
        breadcrumb: [],
        hasMore: false,
        nextCursor: '',
        currentPage: 0,
        pageCursors: [''],
        knownTotalPages: 1,
        pageCache: new Map(),
        navGeneration: state.remote.navGeneration + 1,
      }
    }))

    if (mode === 'legacy') {
      get().loadRemoteLegacy()
    } else {
      const folderId = mode === 'library' ? myLibraryId : myJobsId
      const folderName = mode === 'library' ? 'My Library' : 'My Jobs'
      if (folderId) {
        get().loadRemoteFolder(folderId, folderName)
      }
    }
  },

  // If folderName is provided, this is a new navigation (resets pagination).
  // Otherwise, reloads the current page. Items are REPLACED, not appended.
  loadRemoteFolder: async (folderId?: string, folderName?: string) => {
    const state = get().remote
    const targetId = folderId ?? state.currentFolderId
    if (!targetId) return

    const isNewNavigation = folderName !== undefined
    const currentPage = isNewNavigation ? 0 : state.currentPage
    const itemsPerPage = state.itemsPerPage
    const pageCursors = isNewNavigation ? [''] : state.pageCursors
    const cursor = pageCursors[currentPage] ?? ''

    // Use cached page for back/forward navigation to avoid redundant API calls
    const cachedPage = state.pageCache.get(currentPage)
    if (!isNewNavigation && cachedPage && (Date.now() - cachedPage.timestamp) < PAGE_CACHE_TTL) {
      set(state => ({
        remote: {
          ...state.remote,
          items: cachedPage.items,
          hasMore: cachedPage.hasMore,
          nextCursor: cachedPage.nextCursor,
          isLoading: false,
          error: null,
        }
      }))
      return
    }

    // Stale response guard: capture generation before async call
    const myGen = get().remote.navGeneration

    set(state => ({
      remote: { ...state.remote, isLoading: true, error: null }
    }))

    try {
      const contents = await App.ListRemoteFolderPage(targetId, cursor, itemsPerPage)

      // Stale response guard: discard if navigation changed during async call
      if (get().remote.navGeneration !== myGen) return

      // Update breadcrumb only on new navigation
      let breadcrumb = state.breadcrumb
      if (isNewNavigation) {
        const existingIndex = breadcrumb.findIndex(b => b.id === targetId)
        if (existingIndex >= 0) {
          breadcrumb = breadcrumb.slice(0, existingIndex + 1)
        } else {
          breadcrumb = [...breadcrumb, { id: targetId, name: folderName! }]
        }
      }

      const newPageCursors = [...pageCursors]
      if (contents.hasMore && contents.nextCursor) {
        newPageCursors[currentPage + 1] = contents.nextCursor
      }

      const newCache = new Map(state.pageCache)
      newCache.set(currentPage, {
        items: contents.items,
        hasMore: contents.hasMore,
        nextCursor: contents.nextCursor ?? '',
        timestamp: Date.now(),
      })
      // Limit cache size
      if (newCache.size > MAX_CACHED_PAGES) {
        const oldestKey = newCache.keys().next().value
        if (oldestKey !== undefined) {
          newCache.delete(oldestKey)
        }
      }

      let knownTotalPages = isNewNavigation ? 1 : state.knownTotalPages
      if (contents.hasMore && currentPage + 1 >= knownTotalPages) {
        knownTotalPages = currentPage + 2  // We know at least one more page exists
      } else if (!contents.hasMore) {
        knownTotalPages = currentPage + 1  // This is the last page
      }

      set(state => ({
        remote: {
          ...state.remote,
          currentFolderId: targetId,
          items: contents.items,  // Replace (not append) — each page is a standalone view
          isLoading: false,
          error: null,
          hasMore: contents.hasMore,
          nextCursor: contents.nextCursor ?? '',
          breadcrumb,
          currentPage,
          pageCursors: newPageCursors,
          knownTotalPages,
          pageCache: isNewNavigation ? new Map() : newCache,  // Clear cache on new navigation
        }
      }))

      if (isNewNavigation) {
        const freshCache = new Map<number, CachedPage>()
        freshCache.set(0, {
          items: contents.items,
          hasMore: contents.hasMore,
          nextCursor: contents.nextCursor ?? '',
          timestamp: Date.now(),
        })
        set(state => ({
          remote: { ...state.remote, pageCache: freshCache }
        }))
      }
    } catch (error) {
      // Stale response guard: discard if navigation changed during async call
      if (get().remote.navGeneration !== myGen) return
      set(state => ({
        remote: {
          ...state.remote,
          isLoading: false,
          error: error instanceof Error ? error.message : String(error),
        }
      }))
    }
  },

  loadRemoteLegacy: async () => {
    const state = get().remote
    const currentPage = state.currentPage
    const itemsPerPage = state.itemsPerPage
    const cursor = state.pageCursors[currentPage] ?? ''

    const cachedPage = state.pageCache.get(currentPage)
    if (cachedPage && (Date.now() - cachedPage.timestamp) < PAGE_CACHE_TTL) {
      set(state => ({
        remote: {
          ...state.remote,
          items: cachedPage.items,
          hasMore: cachedPage.hasMore,
          nextCursor: cachedPage.nextCursor,
          isLoading: false,
          error: null,
        }
      }))
      return
    }

    // Stale response guard: capture generation before async call
    const myGen = get().remote.navGeneration

    set(state => ({
      remote: { ...state.remote, isLoading: true, error: null }
    }))

    try {
      const contents = await App.ListRemoteLegacy(cursor, itemsPerPage)

      // Stale response guard: discard if navigation changed during async call
      if (get().remote.navGeneration !== myGen) return

      const newPageCursors = [...state.pageCursors]
      if (contents.hasMore && contents.nextCursor) {
        newPageCursors[currentPage + 1] = contents.nextCursor
      }

      const newCache = new Map(state.pageCache)
      newCache.set(currentPage, {
        items: contents.items,
        hasMore: contents.hasMore,
        nextCursor: contents.nextCursor ?? '',
        timestamp: Date.now(),
      })
      if (newCache.size > MAX_CACHED_PAGES) {
        const oldestKey = newCache.keys().next().value
        if (oldestKey !== undefined) {
          newCache.delete(oldestKey)
        }
      }

      let knownTotalPages = state.knownTotalPages
      if (contents.hasMore && currentPage + 1 >= knownTotalPages) {
        knownTotalPages = currentPage + 2
      } else if (!contents.hasMore) {
        knownTotalPages = currentPage + 1
      }

      set(state => ({
        remote: {
          ...state.remote,
          currentFolderId: '',
          items: contents.items,  // Replace (not append) — each page is a standalone view
          isLoading: false,
          error: null,
          hasMore: contents.hasMore,
          nextCursor: contents.nextCursor ?? '',
          breadcrumb: [{ id: '', name: 'Legacy Files' }],
          pageCursors: newPageCursors,
          knownTotalPages,
          pageCache: newCache,
        }
      }))
    } catch (error) {
      // Stale response guard: discard if navigation changed during async call
      if (get().remote.navGeneration !== myGen) return
      set(state => ({
        remote: {
          ...state.remote,
          isLoading: false,
          error: error instanceof Error ? error.message : String(error),
        }
      }))
    }
  },
  setRemoteItemsPerPage: (size: number) => {
    // Clamp to reasonable range
    const clampedSize = Math.max(10, Math.min(200, size))

    // Reset to page 0 with new page size, clear cache
    set(state => ({
      remote: {
        ...state.remote,
        itemsPerPage: clampedSize,
        currentPage: 0,
        pageCursors: [''],
        knownTotalPages: 1,
        pageCache: new Map(),
      }
    }))

    // Reload page 0 with new page size
    const { mode } = get().remote
    if (mode === 'legacy') {
      get().loadRemoteLegacy()
    } else {
      get().loadRemoteFolder()
    }
  },

  goToNextRemotePage: async () => {
    const { hasMore, currentPage, pageCursors, mode } = get().remote
    if (!hasMore) return

    // We need the next cursor to exist
    const nextCursor = pageCursors[currentPage + 1]
    if (!nextCursor) return

    // Update currentPage first
    set(state => ({
      remote: { ...state.remote, currentPage: currentPage + 1 }
    }))

    // Load the next page
    if (mode === 'legacy') {
      await get().loadRemoteLegacy()
    } else {
      await get().loadRemoteFolder()
    }
  },

  goToPreviousRemotePage: async () => {
    const { currentPage, mode } = get().remote
    if (currentPage <= 0) return

    // Update currentPage first
    set(state => ({
      remote: { ...state.remote, currentPage: currentPage - 1 }
    }))

    // Load the previous page (will use cache if available)
    if (mode === 'legacy') {
      await get().loadRemoteLegacy()
    } else {
      await get().loadRemoteFolder()
    }
  },

  navigateRemoteTo: (folderId: string, folderName: string) => {
    // Stale response guard: bump navGeneration so in-flight responses from the old folder are discarded
    set(state => ({
      remote: {
        ...state.remote,
        selection: { selectedIds: new Set(), lastSelectedId: null },
        currentPage: 0,
        pageCursors: [''],
        knownTotalPages: 1,
        pageCache: new Map(),
        navGeneration: state.remote.navGeneration + 1,
      }
    }))
    get().loadRemoteFolder(folderId, folderName)
  },

  navigateRemoteToBreadcrumb: (index: number) => {
    const { breadcrumb } = get().remote
    if (index < 0 || index >= breadcrumb.length) return

    const target = breadcrumb[index]
    // Stale response guard: bump navGeneration so in-flight responses from the old location are discarded
    set(state => ({
      remote: {
        ...state.remote,
        breadcrumb: breadcrumb.slice(0, index + 1),
        selection: { selectedIds: new Set(), lastSelectedId: null },
        currentPage: 0,
        pageCursors: [''],
        knownTotalPages: 1,
        pageCache: new Map(),
        navGeneration: state.remote.navGeneration + 1,
      }
    }))
    get().loadRemoteFolder(target.id, target.name)
  },

  goRemoteBack: () => {
    const { breadcrumb } = get().remote
    if (breadcrumb.length <= 1) return // Can't go above root

    // Navigate to parent folder
    const parentIndex = breadcrumb.length - 2
    get().navigateRemoteToBreadcrumb(parentIndex)
  },

  refreshRemote: () => {
    const { mode, currentPage } = get().remote

    // Evict only the current page from cache to force a fresh fetch
    set(state => {
      const newCache = new Map(state.remote.pageCache)
      newCache.delete(currentPage)
      return {
        remote: {
          ...state.remote,
          pageCache: newCache,
        }
      }
    })

    if (mode === 'legacy') {
      get().loadRemoteLegacy()
    } else {
      get().loadRemoteFolder()
    }
  },

  setRemoteSelection: (ids: Set<string>, lastId?: string | null) => {
    set(state => ({
      remote: {
        ...state.remote,
        selection: {
          selectedIds: ids,
          lastSelectedId: lastId ?? state.remote.selection.lastSelectedId
        }
      }
    }))
  },

  clearRemoteSelection: () => {
    set(state => ({
      remote: {
        ...state.remote,
        selection: { selectedIds: new Set(), lastSelectedId: null }
      }
    }))
  },

  createRemoteFolder: async (name: string) => {
    const { currentFolderId, mode } = get().remote
    if (mode === 'legacy' || !currentFolderId) {
      return null
    }

    try {
      const folderId = await App.CreateRemoteFolder(name, currentFolderId)
      // Refresh to show new folder
      get().refreshRemote()
      return folderId
    } catch (error) {
      console.error('Failed to create folder:', error)
      return null
    }
  },

  deleteRemoteItems: async (items: wailsapp.FileItemDTO[]) => {
    try {
      const result = await App.DeleteRemoteItems(items)
      if (result.deleted > 0) {
        // Refresh to show updated list
        get().refreshRemote()
        get().clearRemoteSelection()
      }
      return { deleted: result.deleted, failed: result.failed }
    } catch (error) {
      console.error('Failed to delete items:', error)
      return { deleted: 0, failed: items.length }
    }
  },

  // ===== EVENT LISTENERS =====

  // Reset remote browser state when API config changes (e.g., different API key = different account)
  setupEventListeners: () => {
    const handleConfigChanged = () => {
      console.log('[FileBrowser] Config changed, invalidating remote cache')
      set(state => ({
        remote: {
          ...initialRemoteState,
          navGeneration: state.remote.navGeneration + 1,
        }
      }))
      get().initRemote()
    }
    EventsOn(EVENT_NAMES.CONFIG_CHANGED, handleConfigChanged)
    return () => { EventsOff(EVENT_NAMES.CONFIG_CHANGED) }
  },

  // ===== COMMON ACTIONS =====

  getLocalSelectedItems: () => {
    const { items, selection } = get().local
    return items.filter(item => selection.selectedIds.has(item.id))
  },

  getRemoteSelectedItems: () => {
    const { items, selection } = get().remote
    return items.filter(item => selection.selectedIds.has(item.id))
  },
}))
