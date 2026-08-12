import { describe, it, expect, beforeEach } from 'vitest'
import { useErrorReportStore } from './errorReportStore'
import type { ReportableErrorEventDTO } from '../types/events'

function event(overrides: Partial<ReportableErrorEventDTO> = {}): ReportableErrorEventDTO {
  return {
    timestamp: '2026-01-01T00:00:00Z',
    errorID: 'err-1',
    category: 'transfer',
    severity: 'error',
    operation: 'folder_download',
    backend: 's3',
    errorMessage: 'something went wrong',
    errorClass: 'server_error',
    timeline: [],
    ...overrides,
  }
}

describe('errorReportStore.showError', () => {
  beforeEach(() => {
    useErrorReportStore.setState({
      pendingError: null,
      isModalOpen: false,
      isSaving: false,
      lastResult: null,
      _shownErrorIDs: new Map<string, number>(),
    })
  })

  it('opens the modal for a genuinely reportable error', () => {
    useErrorReportStore.getState().showError(event())
    expect(useErrorReportStore.getState().isModalOpen).toBe(true)
  })

  // Defense in depth: the backend suppresses these, but if any path publishes
  // one anyway the modal must not interrupt the user for something they can fix.
  it.each(['disk_space', 'auth', 'client_error', 'network', 'timeout', 'local_fs'])(
    'drops %s events',
    (errorClass) => {
      useErrorReportStore.getState().showError(event({ errorClass }))
      expect(useErrorReportStore.getState().isModalOpen).toBe(false)
      expect(useErrorReportStore.getState().pendingError).toBeNull()
    }
  )

  it('does not reopen for the same errorID within the cooldown', () => {
    const store = useErrorReportStore.getState()
    store.showError(event({ errorID: 'dup-1' }))
    expect(useErrorReportStore.getState().isModalOpen).toBe(true)

    useErrorReportStore.getState().dismiss()
    expect(useErrorReportStore.getState().isModalOpen).toBe(false)

    // A replayed or duplicated event for the same error must not reopen it.
    useErrorReportStore.getState().showError(event({ errorID: 'dup-1' }))
    expect(useErrorReportStore.getState().isModalOpen).toBe(false)
  })

  it('still opens for a different errorID after dismissal', () => {
    useErrorReportStore.getState().showError(event({ errorID: 'first' }))
    useErrorReportStore.getState().dismiss()
    useErrorReportStore.getState().showError(event({ errorID: 'second' }))
    expect(useErrorReportStore.getState().isModalOpen).toBe(true)
    expect(useErrorReportStore.getState().pendingError?.errorID).toBe('second')
  })

  it('drops events while the modal is already open', () => {
    useErrorReportStore.getState().showError(event({ errorID: 'a' }))
    useErrorReportStore.getState().showError(event({ errorID: 'b' }))
    expect(useErrorReportStore.getState().pendingError?.errorID).toBe('a')
  })
})
