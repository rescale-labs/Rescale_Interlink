import { describe, it, expect, afterEach, vi } from 'vitest'
import { render, screen, fireEvent, cleanup } from '@testing-library/react'
import { SetupTab } from './SetupTab'

vi.mock('../../App', () => ({
  useTabNavigation: () => ({ activeTabName: 'Setup', switchToTab: () => {} }),
}))

// Only what the tab calls on mount and on starting the service.
const app = vi.hoisted(() => ({
  GetConfig: vi.fn(() => Promise.resolve({ apiBaseUrl: 'https://platform.example.invalid', apiKey: '', proxyMode: 'no-proxy' })),
  GetCredentialSource: vi.fn(() => Promise.resolve({})),
  GetAppInfo: vi.fn(() => Promise.resolve({ version: 'dev' })),
  GetDefaultConfigPath: vi.fn(() => Promise.resolve('')),
  GetDaemonStatus: vi.fn(() => Promise.resolve({})),
  GetDaemonConfig: vi.fn(() => Promise.resolve({ downloadFolder: 'FAKE-DIR' })),
  GetDefaultDownloadFolder: vi.fn(() => Promise.resolve('')),
  GetFileLoggingSettings: vi.fn(() => Promise.resolve({ enabled: false, filePath: '' })),
  GetServiceStatus: vi.fn(),
  StartServiceElevated: vi.fn(() => Promise.resolve({ success: true })),
  StopServiceElevated: vi.fn(() => Promise.resolve({ success: true })),
  InstallAndStartServiceElevated: vi.fn(() => Promise.resolve({ success: true })),
}))

vi.mock('../../../wailsjs/go/wailsapp/App', () => app)

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

// A status error after the command ran ends the wait and is reported as a
// failed check, not as the command failing.
describe('SetupTab service control', () => {
  it.each([
    ['Start Service', { installed: true, running: false }, 'Start'],
    ['Install & Start Service', { installed: false, running: false }, 'Install and start'],
    ['Stop Service (Admin)', { installed: true, running: true }, 'Stop'],
  ])('%s stops waiting at the first status error and says so', async (button, initial, command) => {
    app.GetServiceStatus
      .mockResolvedValueOnce({ ...initial, status: 'FAKE' })
      .mockRejectedValue(new Error('FAKE pipe closed'))

    render(<SetupTab />)
    fireEvent.click(await screen.findByText('Advanced Settings'))
    fireEvent.click(await screen.findByRole('button', { name: button }))
    fireEvent.click(screen.getByRole('button', { name: /Continue/ }))

    const message = `${command} command completed, but the service status could not be checked: Error: FAKE pipe closed`
    expect(await screen.findByText(message, {}, { timeout: 3000 })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: button })).toBeEnabled()
    const calls = app.GetServiceStatus.mock.calls.length
    await new Promise((resolve) => setTimeout(resolve, 1200))
    expect(app.GetServiceStatus.mock.calls.length).toBe(calls)
  })
})
