import { describe, it, expect, afterEach, vi } from 'vitest'
import { useJobStore } from './jobStore'
import { useConfigStore } from './configStore'
import { wailsapp } from '../../wailsjs/go/models'

// The shared setup mock stops at the bindings the other stores call; the
// config-file path needs three more, so this file supplies the App module.
const app = vi.hoisted(() => ({
  GetConfig: vi.fn(),
  GetCredentialSource: vi.fn(),
  LoadConfigFromPath: vi.fn(),
}))

vi.mock('../../wailsjs/go/wailsapp/App', () => app)

afterEach(() => {
  useJobStore.setState({
    coreTypes: [],
    projects: [],
    analysisCodes: [],
    automations: [],
    coreTypesLoaded: false,
    projectsLoaded: false,
    coreTypesError: null,
    projectsError: null,
  })
  useConfigStore.setState({ config: null })
})

describe('jobStore.resetAccountCatalogs', () => {
  it('drops the scanned catalogs, their flags and their errors', () => {
    useJobStore.setState({
      coreTypes: [{ code: 'emerald', name: 'Emerald', displayOrder: 0, isActive: true, cores: [64] }],
      projects: [{ id: 'pOLD', name: 'Old account project', isDefault: true, remainingAmounts: [] }],
      analysisCodes: [{ code: 'abaqus', name: 'Abaqus', description: '', vendorName: '', versions: [] }],
      automations: [{ id: 'a1', name: 'Post', description: '', executeOn: 'complete', scriptName: 'post.sh' }],
      coreTypesLoaded: true,
      projectsLoaded: true,
      coreTypesError: 'status 403: forbidden',
      projectsError: 'status 403: forbidden',
    })

    useJobStore.getState().resetAccountCatalogs()

    expect(useJobStore.getState()).toMatchObject({
      coreTypes: [],
      projects: [],
      analysisCodes: [],
      automations: [],
      // Cleared with the lists: a flag left set would keep the pickers from
      // re-scanning, which is what left the previous account's projects on
      // screen after a key change.
      coreTypesLoaded: false,
      projectsLoaded: false,
      coreTypesError: null,
      projectsError: null,
    })
  })
})

describe('configStore.loadConfigFromFile', () => {
  it('drops the account catalogs only when the imported file carries a different key', async () => {
    const fileHoldingKey = (apiKey: string) => {
      app.LoadConfigFromPath.mockResolvedValue(undefined)
      app.GetConfig.mockResolvedValue(new wailsapp.ConfigDTO({ apiKey }))
      app.GetCredentialSource.mockResolvedValue(new wailsapp.CredentialSourceDTO({}))
    }

    // Signed in to account A, with its project list already scanned.
    useConfigStore.setState({ config: new wailsapp.ConfigDTO({ apiKey: 'key-A' }) })
    useJobStore.setState({
      projects: [{ id: 'pA', name: 'Account A project', isDefault: true, remainingAmounts: [] }],
      projectsLoaded: true,
    })

    // Re-importing the same account's config is not a key change, so the
    // catalog stands: dropping it would cost the picker a needless re-scan.
    fileHoldingKey('key-A')
    await useConfigStore.getState().loadConfigFromFile('/cfg/account-a.json')
    expect(useJobStore.getState().projects).toHaveLength(1)
    expect(useJobStore.getState().projectsLoaded).toBe(true)

    // A file for another account: the picker must stop offering account A's
    // projects, which is the stale list this import used to leave on screen.
    fileHoldingKey('key-B')
    await useConfigStore.getState().loadConfigFromFile('/cfg/account-b.json')
    expect(useJobStore.getState().projects).toEqual([])
    expect(useJobStore.getState().projectsLoaded).toBe(false)
  })
})
