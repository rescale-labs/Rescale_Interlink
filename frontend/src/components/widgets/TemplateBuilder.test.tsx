import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent, cleanup, waitFor } from '@testing-library/react'
import { TemplateBuilder } from './TemplateBuilder'
import { DEFAULT_JOB_TEMPLATE, useJobStore } from '../../stores'
import type { JobSpec, Project } from '../../stores'
import * as App from '../../../wailsjs/go/wailsapp/App'
import type { wailsapp } from '../../../wailsjs/go/models'
import { afterEach, beforeEach } from 'vitest'

beforeEach(() => {
  // Only the picker tests care about the scan-on-open effects, so the rest start
  // from "already scanned" to keep a resolving promise out of every render.
  useJobStore.setState({ coreTypesLoaded: true, projectsLoaded: true })
})

afterEach(() => {
  cleanup()
  // Store state is module-level, so a seeded ladder or project list would
  // otherwise leak into the next test.
  useJobStore.setState({
    coreTypes: [],
    coreTypesLoaded: false,
    projects: [],
    projectsError: null,
    projectsLoaded: false,
    isLoadingProjects: false,
  })
  vi.clearAllMocks()
})

function renderOpen(initial?: JobSpec) {
  const onClose = vi.fn()
  const onSave = vi.fn()
  const utils = render(
    <TemplateBuilder
      isOpen={true}
      initialTemplate={initial}
      onClose={onClose}
      onSave={onSave}
    />
  )
  return { ...utils, onClose, onSave }
}

// Fields are reached through their visible label, which is what the user reads,
// rather than a test id. levels is how far up from the label the control's
// container sits: one for a plain field, two where a scan button shares the
// label's row.
function fieldFor<T extends HTMLElement>(label: string, selector: string, levels = 1): T {
  let scope = screen.getByText(label).parentElement
  for (let i = 1; i < levels; i++) scope = scope?.parentElement ?? null
  const field = scope?.querySelector(selector)
  if (!field) throw new Error(`${label}: no ${selector} found`)
  return field as T
}

const getLicenseTypeSelect = () => fieldFor<HTMLSelectElement>('License Type', 'select')
const getLicenseValueInput = () => fieldFor<HTMLInputElement>('License Value', 'input')

describe('TemplateBuilder license UX', () => {
  it('auto-switches CUSTOM + RLM_LICENSE=value to the RLM preset and shows the switch hint', () => {
    renderOpen()

    // Pick CUSTOM to enable the value input.
    const typeSel = getLicenseTypeSelect()
    fireEvent.change(typeSel, { target: { value: 'CUSTOM' } })

    const input = getLicenseValueInput()
    fireEvent.change(input, { target: { value: 'RLM_LICENSE=123@test.com' } })

    expect(getLicenseTypeSelect().value).toBe('RLM_LICENSE')
    expect(getLicenseValueInput().value).toBe('123@test.com')
    expect(screen.getByText(/Switched to RLM_LICENSE preset/i)).toBeInTheDocument()
  })

  it('leaves CUSTOM as-is for non-preset keys and shows no hint', () => {
    renderOpen()
    fireEvent.change(getLicenseTypeSelect(), { target: { value: 'CUSTOM' } })
    fireEvent.change(getLicenseValueInput(), { target: { value: 'WEIRD_VAR=foo' } })

    expect(getLicenseTypeSelect().value).toBe('CUSTOM')
    expect(getLicenseValueInput().value).toBe('WEIRD_VAR=foo')
    expect(screen.queryByText(/Switched to /i)).not.toBeInTheDocument()
    expect(screen.queryByText(/loaded as the/i)).not.toBeInTheDocument()
  })

  it('clears the auto-switch hint on explicit dropdown change', () => {
    renderOpen()
    fireEvent.change(getLicenseTypeSelect(), { target: { value: 'CUSTOM' } })
    fireEvent.change(getLicenseValueInput(), { target: { value: 'RLM_LICENSE=123' } })
    expect(screen.getByText(/Switched to RLM_LICENSE preset/i)).toBeInTheDocument()

    fireEvent.change(getLicenseTypeSelect(), { target: { value: 'ANSYS_LICENSE_FILE' } })

    expect(screen.queryByText(/Switched to /i)).not.toBeInTheDocument()
  })

  it('shows the load-time hint when an existing template carries {RLM_LICENSE:"..."}', () => {
    const initial: JobSpec = {
      ...DEFAULT_JOB_TEMPLATE,
      licenseSettings: JSON.stringify({ RLM_LICENSE: '123@test.com' }),
    }
    renderOpen(initial)

    expect(getLicenseTypeSelect().value).toBe('RLM_LICENSE')
    expect(getLicenseValueInput().value).toBe('123@test.com')
    expect(screen.getByText(/This template was saved with RLM_LICENSE=…/i)).toBeInTheDocument()
  })

  it('clears the load-time hint when the user explicitly changes dropdown', () => {
    const initial: JobSpec = {
      ...DEFAULT_JOB_TEMPLATE,
      licenseSettings: JSON.stringify({ RLM_LICENSE: '123@test.com' }),
    }
    renderOpen(initial)
    expect(screen.getByText(/This template was saved with RLM_LICENSE=…/i)).toBeInTheDocument()

    fireEvent.change(getLicenseTypeSelect(), { target: { value: 'ANSYS_LICENSE_FILE' } })
    expect(screen.queryByText(/This template was saved with/i)).not.toBeInTheDocument()
  })
})

const getFeatureNameInput = () => fieldFor<HTMLInputElement>('License Feature Name', 'input')
const getLicensesPerJobInput = () => fieldFor<HTMLInputElement>('Licenses Per Job', 'input')

function saveTemplate() {
  fireEvent.click(screen.getByText('Use Template'))
}

// The default template is deliberately incomplete, so saving it would fail on
// unrelated required fields before the license rules are reached.
function validTemplate(): JobSpec {
  return {
    ...DEFAULT_JOB_TEMPLATE,
    jobName: 'Run_1',
    analysisCode: 'user_included',
    coreType: 'emerald',
    command: './run.sh',
    coresPerSlot: 4,
    walltimeHours: 1,
  }
}

describe('TemplateBuilder license feature set', () => {
  // A template with no feature set must stay that way, or every job would carry
  // a license the user never asked for.
  it('saves nothing when both fields are left alone', () => {
    const { onSave } = renderOpen(validTemplate())
    saveTemplate()

    expect(onSave).toHaveBeenCalledTimes(1)
    expect(onSave.mock.calls[0][0]).toMatchObject({
      licenseFeatureName: '',
      licensesPerJob: 0,
    })
  })

  it('carries the feature name and count through save', () => {
    const { onSave } = renderOpen(validTemplate())

    fireEvent.change(getFeatureNameInput(), { target: { value: '  ansys_hpc  ' } })
    fireEvent.change(getLicensesPerJobInput(), { target: { value: '8' } })
    saveTemplate()

    expect(onSave).toHaveBeenCalledTimes(1)
    // Trimmed, since a stray space would reach the license server verbatim.
    expect(onSave.mock.calls[0][0]).toMatchObject({
      licenseFeatureName: 'ansys_hpc',
      licensesPerJob: 8,
    })
  })

  it('refuses a feature name with no count', () => {
    const { onSave } = renderOpen(validTemplate())

    fireEvent.change(getFeatureNameInput(), { target: { value: 'ansys_hpc' } })
    saveTemplate()

    expect(onSave).not.toHaveBeenCalled()
    expect(screen.getByText(/Licenses per job must be 1 or more/)).toBeInTheDocument()
  })

  it('keeps the count disabled until a feature name is given', () => {
    renderOpen(validTemplate())

    expect(getLicensesPerJobInput()).toBeDisabled()
    // Unset shows as blank: a literal 0 in a box the platform would reject
    // reads as a real value.
    expect(getLicensesPerJobInput().value).toBe('')
    fireEvent.change(getFeatureNameInput(), { target: { value: 'ansys_hpc' } })
    expect(getLicensesPerJobInput()).not.toBeDisabled()
  })

  // The count box is disabled whenever the name is empty, so a count left over
  // from a name the user cleared would be uneditable — it goes with the name.
  it('drops the count when the feature name is cleared', () => {
    const { onSave } = renderOpen(validTemplate())

    fireEvent.change(getFeatureNameInput(), { target: { value: 'ansys_hpc' } })
    fireEvent.change(getLicensesPerJobInput(), { target: { value: '4' } })
    fireEvent.change(getFeatureNameInput(), { target: { value: '' } })
    saveTemplate()

    expect(onSave).toHaveBeenCalledTimes(1)
    expect(onSave.mock.calls[0][0]).toMatchObject({ licenseFeatureName: '', licensesPerJob: 0 })
  })
})

// A 64-core node sold in halves and quarters — the uneven ladder.
const EMERALD = {
  code: 'emerald',
  name: 'Emerald',
  displayOrder: 0,
  isActive: true,
  cores: [4, 8, 16, 32, 64],
}

const getCoresInput = () => fieldFor<HTMLInputElement>('Cores', 'input[type="number"]')

function coresValue(): number {
  return Number(getCoresInput().value)
}

// A coreType the seeded list does not describe leaves the ladder empty, which is
// the coretype-metadata-not-loaded case.
function renderWithLadder(coresPerSlot: number, coreType = 'emerald') {
  useJobStore.setState({ coreTypes: [EMERALD] })
  return renderOpen({ ...DEFAULT_JOB_TEMPLATE, coreType, coresPerSlot })
}

describe('TemplateBuilder cores stepper', () => {
  // Every walk of the stepper: the value hand-typed into the box, whether the
  // minus control is disabled there, the controls clicked in order and the value
  // shown after each click. atFloor is where nothing smaller is valid.
  it.each([
    // Slices within a node, up and back down.
    { coreType: 'emerald', start: 4, atFloor: true, clicks: ['up', 'up', 'down'], expected: [8, 16, 8] },
    // Whole nodes at and above a full node, then back to where the slices resume.
    { coreType: 'emerald', start: 64, atFloor: false, clicks: ['up', 'up', 'down', 'down', 'down'], expected: [128, 192, 128, 64, 32] },
    // 100 is neither a slice nor a whole number of nodes; validate() would reject
    // it on save, so a step resolves it onto the ladder rather than adding to it.
    { coreType: 'emerald', start: 100, atFloor: false, clicks: ['down'], expected: [64] },
    { coreType: 'emerald', start: 100, atFloor: false, clicks: ['up'], expected: [128] },
    // No ladder to read, so the stepper counts in nodes and refuses to guess at
    // fractions of one.
    { coreType: 'unknown_coretype', start: 64, atFloor: true, clicks: ['up', 'down'], expected: [128, 64] },
  ])('walks $coreType from $start via $clicks', ({ coreType, start, atFloor, clicks, expected }) => {
    renderWithLadder(0, coreType)

    // The box takes a hand-typed value as given — the ladder is enforced on save
    // — so a walk can start on it or off it.
    fireEvent.change(getCoresInput(), { target: { value: String(start) } })
    expect(coresValue()).toBe(start)
    expect(screen.getByLabelText('Fewer cores')).toHaveProperty('disabled', atFloor)

    clicks.forEach((direction, i) => {
      fireEvent.click(screen.getByLabelText(direction === 'up' ? 'More cores' : 'Fewer cores'))
      expect(coresValue()).toBe(expected[i])
    })
  })

  // At 0 the stepper substitutes the node size, so the tooltip and the step
  // attribute have to describe the value a click actually produces rather than
  // the one the raw 0 suggests — a step of 128 counts from a base of 0.
  it('agrees with the click when the stored value is zero', () => {
    renderWithLadder(0)

    const up = screen.getByLabelText('More cores')
    expect(up).toHaveAttribute('title', 'Up to 128 cores')
    expect(getCoresInput().step).toBe('64')
    fireEvent.click(up)
    expect(coresValue()).toBe(128)
  })

  // With no coretype metadata loaded, the stored value is not a per-node
  // maximum, so stating the hint in terms of it contradicts the control: the
  // live report was a hint of "Multiples of 4" over a stepper that went 4 → 64.
  it('states the assumed node size while coretype metadata has not loaded', () => {
    renderWithLadder(4, 'unknown_coretype')

    expect(screen.getByText('Multiples of 64')).toBeInTheDocument()
    expect(screen.getByLabelText('More cores')).toHaveAttribute('title', 'Up to 64 cores')

    // An empty or negative box falls back to the same assumed node size.
    fireEvent.change(getCoresInput(), { target: { value: '0' } })
    expect(coresValue()).toBe(64)
  })

  it('takes min and step from the ladder rather than counting by one', () => {
    renderWithLadder(8)

    const input = getCoresInput()
    expect(input.min).toBe('4')
    // One step up from 8 is the next slice, 16.
    expect(input.step).toBe('8')
  })

  it('walks the ladder from the keyboard too', () => {
    renderWithLadder(16)

    fireEvent.keyDown(getCoresInput(), { key: 'ArrowUp' })
    expect(coresValue()).toBe(32)
    fireEvent.keyDown(getCoresInput(), { key: 'ArrowDown' })
    expect(coresValue()).toBe(16)
  })
})

const NO_BUDGET: Project = {
  id: 'pCTMk',
  name: 'Zebra project',
  isDefault: true,
  remainingAmounts: ['(no budget)'],
}

const WITH_BUDGET: Project = {
  id: 'BNTMk',
  name: 'Alpha project',
  isDefault: false,
  remainingAmounts: ['All: My budget ($100.00 available)'],
}

// Two levels up: the Project label shares its row with the Scan Projects button.
const getProjectSelect = () => fieldFor<HTMLSelectElement>('Project', 'select', 2)

function projectOptionText(): string[] {
  return Array.from(getProjectSelect().options).map((o) => o.textContent?.trim() ?? '')
}

// projectsLoaded marks the scan as already done, so the effect leaves the
// seeded list alone.
function seedProjects(projects: Project[]) {
  useJobStore.setState({ projects, projectsLoaded: true })
}

describe('TemplateBuilder project picker', () => {
  it('lists the account projects default-first with their budget lines', () => {
    seedProjects([WITH_BUDGET, NO_BUDGET])
    renderOpen()

    // Zebra sorts after Alpha by name but is the default, so it leads. Only the
    // real budget line reaches the label — "(no budget)" adds nothing.
    expect(projectOptionText()).toEqual([
      'No project',
      'Zebra project (default)',
      'Alpha project — All: My budget ($100.00 available)',
    ])
  })

  it('keeps a stored project ID the account does not list', () => {
    seedProjects([NO_BUDGET])
    renderOpen({ ...DEFAULT_JOB_TEMPLATE, projectId: 'pGONE' })

    expect(getProjectSelect().value).toBe('pGONE')
    expect(projectOptionText()).toContain("pGONE (not in this account's projects)")
  })

  it('rescans on demand from the Scan Projects button', async () => {
    seedProjects([NO_BUDGET])
    renderOpen()
    expect(App.GetProjects).not.toHaveBeenCalled()

    vi.mocked(App.GetProjects).mockResolvedValueOnce({
      projects: [NO_BUDGET, WITH_BUDGET],
    } as unknown as wailsapp.ProjectsResultDTO)

    fireEvent.click(screen.getByText('Scan Projects'))

    expect(App.GetProjects).toHaveBeenCalledTimes(1)
    await waitFor(() => {
      expect(projectOptionText()).toHaveLength(3)
    })
  })

  it('scans once on open and does not re-ask an account with no projects', async () => {
    // The mock resolves to an empty list, which is a real answer.
    useJobStore.setState({ projectsLoaded: false })
    renderOpen()
    await waitFor(() => {
      expect(App.GetProjects).toHaveBeenCalledTimes(1)
    })
    await new Promise((resolve) => setTimeout(resolve, 50))
    expect(App.GetProjects).toHaveBeenCalledTimes(1)
  })

  // The id the dialog opened with must survive "No project" — see
  // unlistedProjectId for why it cannot be typed back.
  it('keeps offering the opened id after "No project" is selected', () => {
    seedProjects([NO_BUDGET])
    renderOpen({ ...DEFAULT_JOB_TEMPLATE, projectId: 'pGONE' })

    fireEvent.change(getProjectSelect(), { target: { value: '' } })

    expect(getProjectSelect().value).toBe('')
    expect(projectOptionText()).toContain("pGONE (not in this account's projects)")
  })

  // The same trap sprung from inside the dialog, by a template that brings its
  // own id in.
  it('keeps a project id loaded from a saved template after "No project" is selected', async () => {
    seedProjects([NO_BUDGET])
    vi.mocked(App.ListSavedTemplates).mockResolvedValueOnce([
      {
        name: 'Other account template',
        path: '/tmp/other.json',
        description: '',
        software: '',
        hardware: '',
        modTime: '',
        job: { ...DEFAULT_JOB_TEMPLATE, projectId: 'pGONE' },
      },
    ] as unknown as wailsapp.TemplateInfoDTO[])
    renderOpen()

    fireEvent.click(await screen.findByText(/Saved Templates \(1\)/))
    fireEvent.click(screen.getByText('Other account template'))

    expect(getProjectSelect().value).toBe('pGONE')
    expect(projectOptionText()).toContain("pGONE (not in this account's projects)")

    fireEvent.change(getProjectSelect(), { target: { value: '' } })

    expect(getProjectSelect().value).toBe('')
    expect(projectOptionText()).toContain("pGONE (not in this account's projects)")
  })

  // A template written by an older build can carry an org code the picker knows
  // nothing about. Left in place it would address the pipeline at another
  // account's organization, the assignment would be refused, and the job would
  // run unassigned — with the picker showing the project the user chose.
  it('clears a stored org code when a project is picked', () => {
    seedProjects([NO_BUDGET])
    const { onSave } = renderOpen({ ...validTemplate(), orgCode: 'old-org' })

    fireEvent.change(getProjectSelect(), { target: { value: NO_BUDGET.id } })
    saveTemplate()

    expect(onSave).toHaveBeenCalledTimes(1)
    expect(onSave.mock.calls[0][0]).toMatchObject({ projectId: NO_BUDGET.id, orgCode: '' })
  })

  it('does not retry after a failed scan', async () => {
    vi.mocked(App.GetProjects).mockResolvedValueOnce(
      { projects: null, error: 'status 403: forbidden' } as unknown as wailsapp.ProjectsResultDTO)
    useJobStore.setState({ projectsLoaded: false })
    renderOpen()

    await waitFor(() => expect(App.GetProjects).toHaveBeenCalledTimes(1))
    await waitFor(() =>
      expect(screen.getByText(/Could not load projects: status 403: forbidden/)).toBeInTheDocument())
    expect(screen.getByText(/use "Scan Projects" to retry/)).toBeInTheDocument()
    // A failed scan still counts as done; the button is the retry.
    await new Promise((r) => setTimeout(r, 50))
    expect(App.GetProjects).toHaveBeenCalledTimes(1)
  })
})

describe('TemplateBuilder scan headers', () => {
  it('asks for the software catalog with an empty search, not the click event', () => {
    renderOpen()
    fireEvent.click(screen.getByRole('button', { name: 'Scan Software' }))
    expect(App.GetAnalysisCodes).toHaveBeenCalledWith('')
  })
})
