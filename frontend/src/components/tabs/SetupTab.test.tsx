import { describe, it, expect, afterEach, vi } from 'vitest'
import { render, screen, fireEvent, cleanup } from '@testing-library/react'
import { SetupTab } from './SetupTab'

vi.mock('../../App', () => ({
  useTabNavigation: () => ({ activeTabName: 'Setup', switchToTab: () => {} }),
}))

// Only what the tab calls on mount and from its auto-download controls.
const app = vi.hoisted(() => ({
  GetConfig: vi.fn(() => Promise.resolve({ apiBaseUrl: 'https://platform.example.invalid', apiKey: '', proxyMode: 'no-proxy' })),
  GetCredentialSource: vi.fn(() => Promise.resolve({})),
  GetAppInfo: vi.fn(() => Promise.resolve({ version: 'dev' })),
  GetDefaultConfigPath: vi.fn(() => Promise.resolve('')),
  GetDaemonStatus: vi.fn(() => Promise.resolve({})),
  GetDaemonConfig: vi.fn(() => Promise.resolve({ enabled: true, downloadFolder: 'FAKE-DIR' })),
  GetDefaultDownloadFolder: vi.fn(() => Promise.resolve('')),
  GetFileLoggingSettings: vi.fn(() => Promise.resolve({ enabled: false, filePath: '' })),
  GetServiceStatus: vi.fn(),
  SaveDaemonConfig: vi.fn(() => Promise.resolve()),
  StartDaemon: vi.fn(() => Promise.resolve()),
  StopDaemon: vi.fn(() => Promise.resolve()),
  UninstallServiceElevated: vi.fn(() => Promise.resolve({ success: true })),
}))

vi.mock('../../../wailsjs/go/wailsapp/App', () => app)

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
  app.GetDaemonStatus.mockResolvedValue({})
})

const oldService = { installed: true, running: false, status: 'Stopped' }
const noService = { installed: false, running: false, status: 'Not Installed' }

async function openAdvanced() {
  render(<SetupTab />)
  fireEvent.click(await screen.findByText('Advanced Settings'))
}

describe('SetupTab auto-download controls', () => {
  // The Windows Service can no longer be installed or started. One installed
  // by an earlier version is shown with a way to remove it, and does not stop
  // the user starting auto-download in their own session.
  it.each([
    ['an old service installed', oldService],
    ['no service', noService],
  ])('with %s, offers per-user auto-download and never the service', async (_, status) => {
    app.GetServiceStatus.mockResolvedValue(status)
    await openAdvanced()

    expect(await screen.findByText('Auto-Download Control')).toBeInTheDocument()
    fireEvent.click(await screen.findByRole('button', { name: 'Start Auto-Download' }))
    await vi.waitFor(() => expect(app.StartDaemon).toHaveBeenCalledTimes(1))
    expect(screen.queryByRole('button', { name: /Install|Start Service/ })).toBeNull()
    expect(screen.queryByRole('button', { name: /Remove Old Service/ }) !== null).toBe(status.installed)
    expect(screen.queryByText(/removes itself the next time Windows starts it/) !== null).toBe(status.installed)
  })

  it('names the per-user controls without the word service', async () => {
    app.GetServiceStatus.mockResolvedValue(noService)
    app.GetDaemonStatus.mockResolvedValue({ running: true, ipcConnected: true, state: 'running' })
    await openAdvanced()

    fireEvent.click(await screen.findByRole('button', { name: 'Stop Auto-Download' }))
    await vi.waitFor(() => expect(app.StopDaemon).toHaveBeenCalledTimes(1))
    expect(screen.queryByText('Service Control')).toBeNull()
  })

  it('says where the API key is set when auto-download cannot find it', async () => {
    app.GetServiceStatus.mockResolvedValue(noService)
    app.GetDaemonStatus.mockResolvedValue({ running: true, ipcConnected: true, userState: 'error', errorCode: 'no_api_key' })
    await openAdvanced()

    expect(await screen.findByText('Auto-download cannot find your API key')).toBeInTheDocument()
    expect(screen.getByText('Ensure API Configuration is saved and Test Connection succeeds.')).toBeInTheDocument()
  })

  // A status error after the command ran ends the wait and is reported as a
  // failed check, not as the command failing.
  it('Remove Old Service stops waiting at the first status error and says so', async () => {
    app.GetServiceStatus
      .mockResolvedValueOnce(oldService)
      .mockRejectedValue(new Error('FAKE pipe closed'))

    await openAdvanced()
    fireEvent.click(await screen.findByRole('button', { name: 'Remove Old Service' }))
    fireEvent.click(screen.getByRole('button', { name: /Continue/ }))

    const message = 'Remove command completed, but the service status could not be checked: Error: FAKE pipe closed'
    expect(await screen.findByText(message, {}, { timeout: 3000 })).toBeInTheDocument()
    expect(app.UninstallServiceElevated).toHaveBeenCalledTimes(1)
    expect(screen.getByRole('button', { name: 'Remove Old Service' })).toBeEnabled()
    const calls = app.GetServiceStatus.mock.calls.length
    await new Promise((resolve) => setTimeout(resolve, 1200))
    expect(app.GetServiceStatus.mock.calls.length).toBe(calls)
  })
})
