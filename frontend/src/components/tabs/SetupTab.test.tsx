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
  ValidateAutoDownloadPreFlight: vi.fn(() => Promise.resolve({ apiKeyOk: true, folderOk: true })),
  ValidateAutoDownloadSetup: vi.fn(() => Promise.resolve({ hasAutoDownloadField: true, errors: [] })),
  UpdateConfig: vi.fn(() => Promise.resolve()),
  SaveConfig: vi.fn(() => Promise.resolve()),
  SetFlattenJobDownload: vi.fn(() => Promise.resolve()),
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

  // One panel while it runs: its state, each control once, named as in the
  // tray, the PID, and the error once.
  it('shows one set of controls while auto-download runs', async () => {
    app.GetDaemonStatus.mockResolvedValue({
      running: true, ipcConnected: true, userState: 'running', pid: 4242, error: 'FAKE scan failed', lastErrorTime: '',
    })
    await openAdvanced()

    fireEvent.click(await screen.findByRole('button', { name: 'Stop Auto-Download' }))
    await vi.waitFor(() => expect(app.StopDaemon).toHaveBeenCalledTimes(1))
    for (const name of ['Pause Auto-Download', 'Scan Now', 'Stop Auto-Download']) {
      expect(screen.getAllByRole('button', { name })).toHaveLength(1)
    }
    expect(screen.getAllByText('Auto-Download Control')).toHaveLength(1)
    expect(screen.queryByText('My Downloads')).toBeNull()
    expect(screen.getByText('PID: 4242')).toBeInTheDocument()
    expect(screen.getAllByText(/FAKE scan failed/)).toHaveLength(1)
    expect(screen.queryByText('Service Control')).toBeNull()
  })

  // The daemon's facts show whenever it answers, whatever the user's state.
  it.each(['pending', 'error', 'paused'])('shows the daemon\'s facts while it answers, %s', async (userState) => {
    app.GetDaemonStatus.mockResolvedValue({
      running: true, ipcConnected: true, userState, uptime: 'FAKE-UPTIME', jobsDownloaded: 3, activeDownloads: 1,
    })
    await openAdvanced()

    expect(await screen.findByText('FAKE-UPTIME')).toBeInTheDocument()
    expect(screen.getByText('3 jobs downloaded')).toBeInTheDocument()
  })

  it('gives a slow start\'s advice once', async () => {
    app.GetDaemonStatus.mockResolvedValue({
      running: true, ipcConnected: false, userState: 'error', errorCode: 'transient_timeout',
      userStateDetail: 'Error: FAKE slow start. If this persists, click Retry, or Open Logs.',
    })
    await openAdvanced()

    expect(await screen.findAllByText(/If this persists/)).toHaveLength(1)
  })

  // A daemon that runs but does not answer is not one that needs starting.
  it('says a Retry found the running daemon not answering', async () => {
    app.GetDaemonStatus.mockResolvedValue({ running: true, ipcConnected: false, userState: 'pending', pid: 4242 })
    app.ReloadDaemonConfig.mockResolvedValueOnce({ error: 'daemon not running' })
    await openAdvanced()

    fireEvent.click(await screen.findByRole('button', { name: 'Retry' }))
    expect(await screen.findByText('Settings reloaded (auto-download does not answer)')).toBeInTheDocument()
  })

  it('offers Resume while paused, and no control without IPC', async () => {
    app.GetDaemonStatus.mockResolvedValue({ running: true, ipcConnected: true, userState: 'paused' })
    await openAdvanced()
    expect(await screen.findByRole('button', { name: 'Resume Auto-Download' })).toBeInTheDocument()
    cleanup()

    app.GetDaemonStatus.mockResolvedValue({ running: true, ipcConnected: false, userState: 'pending', pid: 4242 })
    await openAdvanced()
    expect(await screen.findByText('IPC unavailable - controls disabled')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Pause|Resume|Scan Now|Stop/ })).toBeNull()
  })

  it('says where the API key is set when auto-download cannot find it', async () => {
    app.GetDaemonStatus.mockResolvedValue({
      running: true, ipcConnected: true, userState: 'error', errorCode: 'no_api_key', error: 'FAKE no API key',
    })
    await openAdvanced()

    expect(await screen.findByText('Auto-download cannot find your API key')).toBeInTheDocument()
    expect(screen.getByText('Ensure API Configuration is saved and Test Connection succeeds.')).toBeInTheDocument()
    expect(screen.queryByText(/FAKE no API key/)).toBeNull()
  })

  // ReloadDaemonConfig answers, never throws: the status line says what it did.
  it('says enabled auto-download needs starting when none runs', async () => {
    app.GetDaemonConfig.mockResolvedValueOnce({ enabled: false, downloadFolder: 'FAKE-DIR' })
    app.ReloadDaemonConfig.mockResolvedValueOnce({ error: 'daemon not running' })
    await openAdvanced()

    fireEvent.click(await screen.findByLabelText('Enable Auto-Download'))
    expect(await screen.findByText('Auto-download enabled; auto-download needs starting')).toBeInTheDocument()
  })

  it('says what a Retry did', async () => {
    app.GetDaemonStatus.mockResolvedValue({ running: true, ipcConnected: true, userState: 'error', error: 'FAKE scan failed' })
    app.ReloadDaemonConfig.mockResolvedValueOnce({ deferred: true, activeDownloads: 2 })
    await openAdvanced()

    fireEvent.click(await screen.findByRole('button', { name: 'Retry' }))
    expect(await screen.findByText('Settings reloaded (will apply when 2 downloads finish)')).toBeInTheDocument()
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
})

describe('SetupTab File Browser settings', () => {
  // The option saves on its own: an API key typed but not saved is neither
  // applied nor saved, and stays in the field.
  it('saves only the Input/Output split option when it is ticked', async () => {
    await openAdvanced()
    const apiKey = await screen.findByPlaceholderText('API Key')
    fireEvent.change(apiKey, { target: { value: 'UNSAVED-KEY' } })
    const option = screen.getByLabelText('Download jobs without Input/Output split')
    fireEvent.click(option)

    await vi.waitFor(() => expect(app.SetFlattenJobDownload).toHaveBeenCalledWith(true))
    await vi.waitFor(() => expect(option).toBeChecked())
    expect(app.UpdateConfig).not.toHaveBeenCalled()
    expect(app.SaveConfig).not.toHaveBeenCalled()
    expect(apiKey).toHaveValue('UNSAVED-KEY')
  })
})
