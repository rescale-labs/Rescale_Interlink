import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { wailsapp } from '../../wailsjs/go/models'
import { useTransferStore, classifyError, BATCH_PAGE_SIZE, type Enumeration } from './transferStore'

// The shared setup mock doesn't carry the batch/daemon bindings this store uses,
// so this file declares its own; it overrides the setup mock for this module.
vi.mock('../../wailsjs/go/wailsapp/App', () => ({
  GetTransferBatches: vi.fn(() => Promise.resolve([])),
  GetUngroupedTransferTasks: vi.fn(() => Promise.resolve([])),
  GetTransferTasks: vi.fn(() => Promise.resolve([])),
  GetBatchTasks: vi.fn(() => Promise.resolve([])),
  GetDaemonTransferSnapshot: vi.fn(() => Promise.resolve(null)),
  GetTransferStats: vi.fn(() => Promise.resolve({
    queued: 0, initializing: 0, active: 0, paused: 0,
    completed: 0, failed: 0, cancelled: 0, total: 0,
  })),
  ClearCompletedTransfers: vi.fn(() => Promise.resolve()),
}))

import * as App from '../../wailsjs/go/wailsapp/App'

function taskDTO(overrides: Record<string, unknown> = {}): wailsapp.TransferTaskDTO {
  return {
    id: 'task-1',
    type: 'upload',
    state: 'active',
    name: 'file.dat',
    source: '/src/file.dat',
    dest: 'folder-1',
    size: 1024,
    progress: 0,
    speed: 0,
    error: '',
    sourceLabel: '',
    batchID: '',
    batchLabel: '',
    createdAt: '',
    startedAt: '',
    completedAt: '',
    ...overrides,
  } as unknown as wailsapp.TransferTaskDTO
}

function batchDTO(overrides: Record<string, unknown> = {}): wailsapp.TransferBatchDTO {
  return {
    batchID: 'batch-1',
    batchLabel: 'Upload',
    direction: 'upload',
    sourceLabel: '',
    total: 1,
    queued: 0,
    active: 1,
    completed: 0,
    failed: 0,
    cancelled: 0,
    totalBytes: 1024,
    progress: 0,
    speed: 0,
    totalKnown: true,
    ...overrides,
  } as unknown as wailsapp.TransferBatchDTO
}

function enumeration(overrides: Partial<Enumeration> = {}): Enumeration {
  return {
    id: 'enum-1',
    folderName: 'data',
    direction: 'upload',
    foldersFound: 0,
    filesFound: 0,
    bytesFound: 0,
    isComplete: true,
    lastEventAt: Date.now(),
    completedAt: Date.now(),
    ...overrides,
  }
}

// Drain the microtask queue so an in-flight tick finishes and arms its next
// timer. advanceTimersByTimeAsync moves the clock before those microtasks run,
// so a timer armed afterwards would sit 500ms in the (already advanced) future
// and never fire.
async function settle() {
  for (let i = 0; i < 20; i++) await Promise.resolve()
}

// Run n further poll ticks, letting each settle before the clock moves again.
async function advanceTicks(n: number, intervalMs = 500) {
  await settle()
  for (let i = 0; i < n; i++) {
    await vi.advanceTimersByTimeAsync(intervalMs)
    await settle()
  }
}

function reset() {
  useTransferStore.getState().stopPolling()
  useTransferStore.setState({
    tasks: [],
    batches: [],
    enumerations: [],
    expandedBatches: new Set<string>(),
    batchTasks: new Map(),
    batchEpochs: new Map(),
    batchStatusFilter: new Map(),
    error: null,
  })
}

// clearAllMocks keeps implementations, so a test that installs a pending or
// custom resolution would leak it into the next one. Re-establish the defaults.
beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(App.GetTransferBatches).mockResolvedValue([])
  vi.mocked(App.GetUngroupedTransferTasks).mockResolvedValue([])
  vi.mocked(App.GetTransferTasks).mockResolvedValue([])
  vi.mocked(App.GetBatchTasks).mockResolvedValue([])
  vi.mocked(App.GetDaemonTransferSnapshot).mockResolvedValue(
    null as unknown as wailsapp.DaemonTransferSnapshotDTO
  )
  vi.mocked(App.GetTransferStats).mockResolvedValue({
    queued: 0, initializing: 0, active: 0, paused: 0,
    completed: 0, failed: 0, cancelled: 0, total: 0,
  } as unknown as wailsapp.TransferStatsDTO)
  reset()
})

afterEach(() => {
  reset()
  vi.useRealTimers()
})

describe('transferStore polling', () => {
  it('does not start another tick while the previous one is still in flight', async () => {
    vi.useFakeTimers()
    let releaseBatches: (v: wailsapp.TransferBatchDTO[]) => void = () => {}
    vi.mocked(App.GetTransferBatches).mockImplementation(
      () => new Promise(resolve => { releaseBatches = resolve })
    )

    useTransferStore.getState().startPolling(500)

    // The first tick runs immediately.
    expect(App.GetTransferBatches).toHaveBeenCalledTimes(1)
    expect(App.GetTransferStats).toHaveBeenCalledTimes(1)
    expect(App.GetUngroupedTransferTasks).toHaveBeenCalledTimes(1)

    // Four intervals elapse while that tick is still pending. Nothing may fire:
    // the next tick is armed only once the current one settles.
    await vi.advanceTimersByTimeAsync(2000)
    expect(App.GetTransferBatches).toHaveBeenCalledTimes(1)
    expect(App.GetTransferStats).toHaveBeenCalledTimes(1)

    releaseBatches([])
    await vi.advanceTimersByTimeAsync(0)
    // Settling alone doesn't fire the next tick; it waits out the interval.
    expect(App.GetTransferBatches).toHaveBeenCalledTimes(1)

    await vi.advanceTimersByTimeAsync(500)
    expect(App.GetTransferBatches).toHaveBeenCalledTimes(2)
  })

  it('stops re-arming after stopPolling', async () => {
    vi.useFakeTimers()
    useTransferStore.getState().startPolling(500)
    await advanceTicks(1)
    expect(App.GetTransferStats).toHaveBeenCalledTimes(2)

    useTransferStore.getState().stopPolling()
    await advanceTicks(10)
    expect(App.GetTransferStats).toHaveBeenCalledTimes(2)
    expect(useTransferStore.getState().isPolling).toBe(false)
  })

  it('startPolling is a no-op while already polling', async () => {
    vi.useFakeTimers()
    useTransferStore.getState().startPolling(500)
    useTransferStore.getState().startPolling(500)
    expect(App.GetTransferStats).toHaveBeenCalledTimes(1)
    await advanceTicks(1)
    expect(App.GetTransferStats).toHaveBeenCalledTimes(2)
  })

  it('refreshes expanded batch pages every fourth tick, not every tick', async () => {
    vi.useFakeTimers()
    useTransferStore.setState({ expandedBatches: new Set(['batch-1']) })

    useTransferStore.getState().startPolling(500)
    await advanceTicks(0)
    // Tick 1: batch counters refresh, the page does not.
    expect(App.GetTransferBatches).toHaveBeenCalledTimes(1)
    expect(App.GetBatchTasks).not.toHaveBeenCalled()

    // Ticks 2, 3 — still no page refresh.
    await advanceTicks(2)
    expect(App.GetTransferBatches).toHaveBeenCalledTimes(3)
    expect(App.GetBatchTasks).not.toHaveBeenCalled()

    // Tick 4 refreshes the page.
    await advanceTicks(1)
    expect(App.GetTransferBatches).toHaveBeenCalledTimes(4)
    expect(App.GetBatchTasks).toHaveBeenCalledTimes(1)
    expect(App.GetBatchTasks).toHaveBeenCalledWith('batch-1', 0, BATCH_PAGE_SIZE, '')
  })

  it('expanding a batch fetches its first page immediately', () => {
    useTransferStore.getState().toggleBatchExpanded('batch-1')
    expect(App.GetBatchTasks).toHaveBeenCalledWith('batch-1', 0, BATCH_PAGE_SIZE, '')
  })
})

describe('transferStore fetch namespaces', () => {
  it('a local task fetch keeps daemon rows', async () => {
    useTransferStore.setState({
      tasks: [{
        ...taskDTO({ id: 'daemon-task', sourceLabel: 'Daemon' }),
        displayProgress: 0,
        speedFormatted: '',
        etaFormatted: '',
      }],
    })
    vi.mocked(App.GetUngroupedTransferTasks).mockResolvedValue([taskDTO({ id: 'local-task' })])

    await useTransferStore.getState().fetchUngroupedTasks()

    expect(useTransferStore.getState().tasks.map(t => t.id)).toEqual(['local-task', 'daemon-task'])
  })

  it('a batch fetch keeps daemon batches', async () => {
    useTransferStore.setState({
      batches: [{
        ...batchDTO({ batchID: 'daemon-batch', sourceLabel: 'Daemon' }),
        totalKnown: true, filesPerSec: 0, etaSeconds: -1,
        discoveredTotal: 0, discoveredBytes: 0, startedAtUnix: 0,
        skipped: 0, cancelRequested: false,
      }],
    })
    vi.mocked(App.GetTransferBatches).mockResolvedValue([batchDTO({ batchID: 'local-batch' })])

    await useTransferStore.getState().fetchBatches(false)

    expect(useTransferStore.getState().batches.map(b => b.batchID)).toEqual(['local-batch', 'daemon-batch'])
  })

  it('a daemon snapshot keeps local rows and batches', async () => {
    vi.mocked(App.GetUngroupedTransferTasks).mockResolvedValue([taskDTO({ id: 'local-task' })])
    vi.mocked(App.GetTransferBatches).mockResolvedValue([batchDTO({ batchID: 'local-batch' })])
    await useTransferStore.getState().fetchUngroupedTasks()
    await useTransferStore.getState().fetchBatches(false)

    vi.mocked(App.GetDaemonTransferSnapshot).mockResolvedValue({
      tasks: [{ id: 'daemon-task', sourceLabel: 'Daemon', batchId: 'daemon-batch' }],
      batches: [{ batchId: 'daemon-batch', sourceLabel: 'Daemon' }],
    } as unknown as wailsapp.DaemonTransferSnapshotDTO)

    await useTransferStore.getState().fetchDaemonSnapshot()

    expect(useTransferStore.getState().tasks.map(t => t.id)).toEqual(['local-task', 'daemon-task'])
    expect(useTransferStore.getState().batches.map(b => b.batchID)).toEqual(['local-batch', 'daemon-batch'])
  })

  it('a full poll tick leaves both namespaces populated', async () => {
    vi.mocked(App.GetUngroupedTransferTasks).mockResolvedValue([taskDTO({ id: 'local-task' })])
    vi.mocked(App.GetTransferBatches).mockResolvedValue([batchDTO({ batchID: 'local-batch' })])
    vi.mocked(App.GetDaemonTransferSnapshot).mockResolvedValue({
      tasks: [{ id: 'daemon-task', sourceLabel: 'Daemon', batchId: 'daemon-batch' }],
      batches: [{ batchId: 'daemon-batch', sourceLabel: 'Daemon' }],
    } as unknown as wailsapp.DaemonTransferSnapshotDTO)

    vi.useFakeTimers()
    useTransferStore.getState().startPolling(500)
    await advanceTicks(2)

    const { tasks, batches } = useTransferStore.getState()
    expect(tasks.map(t => t.id).sort()).toEqual(['daemon-task', 'local-task'])
    expect(batches.map(b => b.batchID).sort()).toEqual(['daemon-batch', 'local-batch'])
  })
})

describe('classifyError', () => {
  it('classifies quota exhaustion under both spellings', () => {
    // Linux
    expect(classifyError('write /vol/f: disk quota exceeded')).toBe('disk_space')
    // macOS strerror(EDQUOT)
    expect(classifyError('write /vol/f: disc quota exceeded')).toBe('disk_space')
  })

  it('leaves unrelated failures generic', () => {
    expect(classifyError('connection reset by peer')).toBe('generic')
    expect(classifyError(undefined)).toBe('generic')
  })
})

describe('transferStore enumeration reconciliation', () => {
  it('reaps a stale finished enumeration but keeps one that failed', async () => {
    const longAgo = Date.now() - 60000
    useTransferStore.setState({
      enumerations: [
        enumeration({ id: 'ok', completedAt: longAgo, lastEventAt: longAgo }),
        enumeration({ id: 'boom', completedAt: longAgo, lastEventAt: longAgo, error: 'permission denied' }),
      ],
    })

    await useTransferStore.getState().fetchBatches(false)

    expect(useTransferStore.getState().enumerations.map(e => e.id)).toEqual(['boom'])
  })

  it('Clear Completed drops finished enumerations, including failed ones', async () => {
    const longAgo = Date.now() - 60000
    useTransferStore.setState({
      enumerations: [
        enumeration({ id: 'boom', completedAt: longAgo, lastEventAt: longAgo, error: 'permission denied' }),
        enumeration({ id: 'scanning', isComplete: false, completedAt: undefined }),
      ],
    })

    useTransferStore.getState().clearCompletedTransfers()

    expect(App.ClearCompletedTransfers).toHaveBeenCalled()
    expect(useTransferStore.getState().enumerations.map(e => e.id)).toEqual(['scanning'])
  })
})
