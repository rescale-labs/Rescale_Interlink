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
  ReloadDaemonConfig: vi.fn(() => Promise.resolve({})),
  SaveDaemonConfig: vi.fn(() => Promise.resolve()),
  StartDaemon: vi.fn(() => Promise.resolve()),
  StopDaemon: vi.fn(() => Promise.resolve()),
}))

vi.mock('../../../wailsjs/go/wailsapp/App', () => app)

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
  app.GetDaemonStatus.mockResolvedValue({})
})

async function openAdvanced() {
  render(<SetupTab />)
  fireEvent.click(await screen.findByText('Advanced Settings'))
}

describe('SetupTab auto-download controls', () => {
  // Auto-download runs in the user's own session: the tab starts it there and
  // offers nothing to install, start or remove a Windows service.
  it('offers per-user auto-download and never a service', async () => {
    await openAdvanced()

    expect(await screen.findByText('Auto-Download Control')).toBeInTheDocument()
    fireEvent.click(await screen.findByRole('button', { name: 'Start Auto-Download' }))
    await vi.waitFor(() => expect(app.StartDaemon).toHaveBeenCalledTimes(1))
    expect(screen.queryByRole('button', { name: /Install|Service/ })).toBeNull()
  })

  it('names the per-user controls without the word service', async () => {
    app.GetDaemonStatus.mockResolvedValue({ running: true, ipcConnected: true, state: 'running' })
    await openAdvanced()

    fireEvent.click(await screen.findByRole('button', { name: 'Stop Auto-Download' }))
    await vi.waitFor(() => expect(app.StopDaemon).toHaveBeenCalledTimes(1))
    expect(screen.queryByText('Service Control')).toBeNull()
  })

  it('says where the API key is set when auto-download cannot find it', async () => {
    app.GetDaemonStatus.mockResolvedValue({ running: true, ipcConnected: true, userState: 'error', errorCode: 'no_api_key' })
    await openAdvanced()

    expect(await screen.findByText('Auto-download cannot find your API key')).toBeInTheDocument()
    expect(screen.getByText('Ensure API Configuration is saved and Test Connection succeeds.')).toBeInTheDocument()
  })

  // Workspace folders are opt-in, and flattening applies only to them: it is
  // off and disabled until they are on, and turning them off turns it off.
  it('saves the workspace folder settings, flattening only with folders on', async () => {
    await openAdvanced()
    const include = await screen.findByLabelText('Include jobs in workspace folders')
    const flatten = screen.getByLabelText('Flatten folder structure')
    expect(flatten).toBeDisabled()

    fireEvent.click(include)
    expect(flatten).toBeEnabled()
    fireEvent.click(flatten)
    await vi.waitFor(() => expect(app.SaveDaemonConfig).toHaveBeenLastCalledWith(
      expect.objectContaining({ includeWorkspaceFolders: true, flattenFolderStructure: true })), { timeout: 3000 })

    fireEvent.click(include)
    await vi.waitFor(() => expect(app.SaveDaemonConfig).toHaveBeenLastCalledWith(
      expect.objectContaining({ includeWorkspaceFolders: false, flattenFolderStructure: false })), { timeout: 3000 })
    expect(flatten).toBeDisabled()
  })

  it('says a job\'s own download path must be inside the Download Folder', async () => {
    await openAdvanced()
    expect(await screen.findByText(/"Auto Download Path" \(per-job download location, must be inside the Download Folder\)/)).toBeInTheDocument()
  })
})
