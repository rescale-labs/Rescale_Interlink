import '@testing-library/jest-dom'
import { vi } from 'vitest'

// Mock Wails runtime
vi.mock('../../wailsjs/runtime/runtime', () => ({
  EventsOn: vi.fn(() => vi.fn()),
  EventsOff: vi.fn(),
  ClipboardGetText: vi.fn(() => Promise.resolve('')),
}))

// Mock Wails Go bindings
vi.mock('../../wailsjs/go/wailsapp/App', () => ({
  // Config bindings
  GetConfig: vi.fn(() => Promise.resolve({
    apiBaseUrl: 'https://platform.rescale.com',
    apiKey: '',
    proxyMode: 'no-proxy',
    tarWorkers: 4,
    uploadWorkers: 4,
    jobWorkers: 4,
  })),
  GetAppInfo: vi.fn(() => Promise.resolve({
    version: '4.0.0-dev',
    fipsEnabled: true,
    fipsStatus: 'FIPS 140-3',
  })),
  UpdateConfig: vi.fn(() => Promise.resolve()),
  SaveConfig: vi.fn(() => Promise.resolve()),
  TestConnection: vi.fn(() => Promise.resolve()),
  SelectFile: vi.fn(() => Promise.resolve('')),
  SelectDirectory: vi.fn(() => Promise.resolve('')),

  // File browser bindings
  ListLocalDirectory: vi.fn(() => Promise.resolve({
    folderId: '/home/user',
    folderPath: '/home/user',
    items: [],
    hasMore: false,
    nextCursor: '',
  })),
  GetHomeDirectory: vi.fn(() => Promise.resolve('/home/user')),
  ListRemoteFolder: vi.fn(() => Promise.resolve({
    folderId: 'folder-123',
    folderPath: 'My Library',
    items: [],
    hasMore: false,
    nextCursor: '',
  })),
  ListRemoteLegacy: vi.fn(() => Promise.resolve({
    folderId: '',
    folderPath: 'Legacy Files',
    items: [],
    hasMore: false,
    nextCursor: '',
  })),
  GetMyLibraryFolderID: vi.fn(() => Promise.resolve('lib-folder-123')),
  GetMyJobsFolderID: vi.fn(() => Promise.resolve('jobs-folder-456')),
  CreateRemoteFolder: vi.fn(() => Promise.resolve('new-folder-789')),
  DeleteRemoteItems: vi.fn(() => Promise.resolve({ deleted: 1, failed: 0, error: '' })),

  // Transfer bindings
  StartTransfers: vi.fn(() => Promise.resolve()),
  CancelTransfer: vi.fn(() => Promise.resolve()),
  CancelAllTransfers: vi.fn(() => Promise.resolve()),
  RetryTransfer: vi.fn(() => Promise.resolve('new-task-123')),
  GetTransferStats: vi.fn(() => Promise.resolve({
    queued: 0,
    initializing: 0,
    active: 0,
    paused: 0,
    completed: 0,
    failed: 0,
    cancelled: 0,
    total: 0,
  })),
  GetTransferTasks: vi.fn(() => Promise.resolve([])),
  ClearCompletedTransfers: vi.fn(() => Promise.resolve()),
}))
