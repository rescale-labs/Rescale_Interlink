import { create } from 'zustand'
import * as App from '../../wailsjs/go/wailsapp/App'
import { wailsapp } from '../../wailsjs/go/models'

// Browse mode for remote browser (matching Go BrowseMode)
export type BrowseMode = 'library' | 'jobs' | 'legacy'

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
  showHidden: boolean
  history: string[]
  selection: SelectionState
}

// Remote browser state
interface RemoteBrowserState {
  mode: BrowseMode
  currentFolderId: string
  items: wailsapp.FileItemDTO[]
  isLoading: boolean
  error: string | null
  breadcrumb: BreadcrumbEntry[]
  hasMore: boolean
  nextCursor: string
  myLibraryId: string | null
  myJobsId: string | null
  selection: SelectionState
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
  loadRemoteFolder: (folderId?: string, folderName?: string, cursor?: string) => Promise<void>
  loadRemoteLegacy: (cursor?: string) => Promise<void>
  loadNextRemotePage: () => Promise<void>  // v4.0.2: Load next page using cursor
  navigateRemoteTo: (folderId: string, folderName: string) => void
  navigateRemoteToBreadcrumb: (index: number) => void
  goRemoteBack: () => void
  refreshRemote: () => void
  setRemoteSelection: (ids: Set<string>, lastId?: string | null) => void
  clearRemoteSelection: () => void
  createRemoteFolder: (name: string) => Promise<string | null>
  deleteRemoteItems: (items: wailsapp.FileItemDTO[]) => Promise<{ deleted: number; failed: number }>

  // Common actions
  getLocalSelectedItems: () => wailsapp.FileItemDTO[]
  getRemoteSelectedItems: () => wailsapp.FileItemDTO[]
}

const initialLocalState: LocalBrowserState = {
  currentPath: '',
  items: [],
  isLoading: false,
  error: null,
  showHidden: false,
  history: [],
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
  selection: { selectedIds: new Set(), lastSelectedId: null },
}

export const useFileBrowserStore = create<FileBrowserStore>((set, get) => ({
  local: initialLocalState,
  remote: initialRemoteState,

  // ===== LOCAL BROWSER ACTIONS =====

  loadLocalDirectory: async (path?: string) => {
    const targetPath = path ?? get().local.currentPath
    set(state => ({
      local: { ...state.local, isLoading: true, error: null }
    }))

    try {
      const contents = await App.ListLocalDirectory(targetPath)

      // Filter hidden files if needed
      const showHidden = get().local.showHidden
      const filteredItems = showHidden
        ? contents.items
        : contents.items.filter(item => !item.name.startsWith('.'))

      set(state => ({
        local: {
          ...state.local,
          currentPath: contents.folderPath,
          items: filteredItems,
          isLoading: false,
          error: null,
        }
      }))
    } catch (error) {
      set(state => ({
        local: {
          ...state.local,
          isLoading: false,
          error: error instanceof Error ? error.message : String(error),
        }
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

    set(state => ({
      remote: {
        ...state.remote,
        mode,
        items: [],
        selection: { selectedIds: new Set(), lastSelectedId: null },
        breadcrumb: [],
        hasMore: false,
        nextCursor: '',
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

  loadRemoteFolder: async (folderId?: string, folderName?: string, cursor?: string) => {
    const targetId = folderId ?? get().remote.currentFolderId
    if (!targetId) return

    set(state => ({
      remote: { ...state.remote, isLoading: true, error: null }
    }))

    try {
      // v4.0.2: Use paginated API - pass cursor for next page
      const contents = await App.ListRemoteFolderPage(targetId, cursor ?? '')

      // Get existing items if appending (pagination)
      const existingItems = cursor ? get().remote.items : []

      // Update breadcrumb if folder name provided (new navigation, not pagination)
      let breadcrumb = get().remote.breadcrumb
      if (folderName !== undefined && !cursor) {
        // Check if navigating to a new folder or within existing breadcrumb
        const existingIndex = breadcrumb.findIndex(b => b.id === targetId)
        if (existingIndex >= 0) {
          // Navigating to existing breadcrumb - truncate
          breadcrumb = breadcrumb.slice(0, existingIndex + 1)
        } else {
          // New folder - append
          breadcrumb = [...breadcrumb, { id: targetId, name: folderName }]
        }
      }

      set(state => ({
        remote: {
          ...state.remote,
          currentFolderId: targetId,
          items: [...existingItems, ...contents.items],  // Append for pagination
          isLoading: false,
          error: null,
          hasMore: contents.hasMore,
          nextCursor: contents.nextCursor ?? '',
          breadcrumb,
        }
      }))
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

  loadRemoteLegacy: async (cursor?: string) => {
    set(state => ({
      remote: { ...state.remote, isLoading: true, error: null }
    }))

    try {
      const contents = await App.ListRemoteLegacy(cursor ?? '')

      const existingItems = cursor ? get().remote.items : []

      set(state => ({
        remote: {
          ...state.remote,
          currentFolderId: '',
          items: [...existingItems, ...contents.items],
          isLoading: false,
          error: null,
          hasMore: contents.hasMore,
          nextCursor: contents.nextCursor ?? '',
          breadcrumb: [{ id: '', name: 'Legacy Files' }],
        }
      }))
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

  // v4.0.2: Load next page of remote files using stored cursor
  loadNextRemotePage: async () => {
    const { mode, nextCursor, currentFolderId, hasMore } = get().remote
    if (!hasMore || !nextCursor) return

    if (mode === 'legacy') {
      await get().loadRemoteLegacy(nextCursor)
    } else {
      await get().loadRemoteFolder(currentFolderId, undefined, nextCursor)
    }
  },

  navigateRemoteTo: (folderId: string, folderName: string) => {
    set(state => ({
      remote: {
        ...state.remote,
        selection: { selectedIds: new Set(), lastSelectedId: null },
      }
    }))
    get().loadRemoteFolder(folderId, folderName)
  },

  navigateRemoteToBreadcrumb: (index: number) => {
    const { breadcrumb } = get().remote
    if (index < 0 || index >= breadcrumb.length) return

    const target = breadcrumb[index]
    set(state => ({
      remote: {
        ...state.remote,
        breadcrumb: breadcrumb.slice(0, index + 1),
        selection: { selectedIds: new Set(), lastSelectedId: null },
      }
    }))
    get().loadRemoteFolder(target.id, undefined)
  },

  goRemoteBack: () => {
    const { breadcrumb } = get().remote
    if (breadcrumb.length <= 1) return // Can't go above root

    // Navigate to parent folder
    const parentIndex = breadcrumb.length - 2
    get().navigateRemoteToBreadcrumb(parentIndex)
  },

  refreshRemote: () => {
    const { mode } = get().remote
    if (mode === 'legacy') {
      set(state => ({
        remote: { ...state.remote, items: [] }
      }))
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
