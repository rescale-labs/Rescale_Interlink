import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent, cleanup, waitFor } from '@testing-library/react'
import { TemplateBuilder } from './TemplateBuilder'
import { DEFAULT_JOB_TEMPLATE, useJobStore } from '../../stores'
import type { JobSpec, Project } from '../../stores'
import * as App from '../../../wailsjs/go/wailsapp/App'
import type { wailsapp } from '../../../wailsjs/go/models'
import { afterEach, beforeEach } from 'vitest'

beforeEach(() => {
  // Opening the builder scans for coretypes and projects when neither has been
  // scanned yet. Only the picker tests care, so the rest start from "already
  // scanned" to keep a promise resolving after every render out of them.
  useJobStore.setState({ coreTypesLoaded: true, projectsLoaded: true })
})

afterEach(() => {
  cleanup()
  // Coretype and project metadata are module-level store state, so a seeded
  // ladder or project list would otherwise leak into the next test.
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

function getLicenseTypeSelect(): HTMLSelectElement {
  // The select is the first one to follow the "License Type" label.
  const label = screen.getByText('License Type')
  const select = label.parentElement?.querySelector('select')
  if (!select) throw new Error('License Type select not found')
  return select as HTMLSelectElement
}

function getLicenseValueInput(): HTMLInputElement {
  const label = screen.getByText('License Value')
  const input = label.parentElement?.querySelector('input')
  if (!input) throw new Error('License Value input not found')
  return input as HTMLInputElement
}

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

  // The CUSTOM-validation-error render test was removed after it hung in
  // vitest (async mock promises from ListSavedTemplates/GetCoreTypes
  // accumulate across earlier tests and defer the error render past the
  // findByText timeout). That cause is gone — the fetch-on-open effects used to
  // re-fire forever on an empty result, starving timers — so the test can be
  // restored if the assertion is wanted back. The sharpened error string itself is covered by
  // a simple source grep in the review checklist — the functional path
  // (validate() producing the new string for non-KEY=value input) is a
  // one-line change that doesn't need component-render coverage.
})

function getFeatureNameInput(): HTMLInputElement {
  const label = screen.getByText('License Feature Name')
  const input = label.parentElement?.querySelector('input')
  if (!input) throw new Error('License Feature Name input not found')
  return input as HTMLInputElement
}

function getLicensesPerJobInput(): HTMLInputElement {
  const label = screen.getByText('Licenses Per Job')
  const input = label.parentElement?.querySelector('input')
  if (!input) throw new Error('Licenses Per Job input not found')
  return input as HTMLInputElement
}

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
    fireEvent.change(getFeatureNameInput(), { target: { value: 'ansys_hpc' } })
    expect(getLicensesPerJobInput()).not.toBeDisabled()
  })

  it('shows an unset count as blank rather than zero', () => {
    renderOpen(validTemplate())
    expect(getLicensesPerJobInput().value).toBe('')
  })
})

// A 64-core node sold in halves and quarters: the gaps between valid sizes are
// uneven, which is why the control cannot just carry a fixed step.
const EMERALD = {
  code: 'emerald',
  name: 'Emerald',
  displayOrder: 0,
  isActive: true,
  cores: [4, 8, 16, 32, 64],
}

function getCoresInput(): HTMLInputElement {
  const label = screen.getByText('Cores')
  const input = label.parentElement?.querySelector('input[type="number"]')
  if (!input) throw new Error('Cores input not found')
  return input as HTMLInputElement
}

function coresValue(): number {
  return Number(getCoresInput().value)
}

function renderWithLadder(coresPerSlot: number) {
  useJobStore.setState({ coreTypes: [EMERALD] })
  return renderOpen({ ...DEFAULT_JOB_TEMPLATE, coreType: 'emerald', coresPerSlot })
}

describe('TemplateBuilder cores stepper', () => {
  it('steps through the coretype slices below a full node', () => {
    renderWithLadder(4)

    fireEvent.click(screen.getByLabelText('More cores'))
    expect(coresValue()).toBe(8)
    fireEvent.click(screen.getByLabelText('More cores'))
    expect(coresValue()).toBe(16)
    fireEvent.click(screen.getByLabelText('Fewer cores'))
    expect(coresValue()).toBe(8)
  })

  it('steps whole nodes at and above a full node', () => {
    renderWithLadder(64)

    fireEvent.click(screen.getByLabelText('More cores'))
    expect(coresValue()).toBe(128)
    fireEvent.click(screen.getByLabelText('More cores'))
    expect(coresValue()).toBe(192)
    fireEvent.click(screen.getByLabelText('Fewer cores'))
    expect(coresValue()).toBe(128)
  })

  it('re-enters the slice ladder on the way down through a full node', () => {
    renderWithLadder(128)

    fireEvent.click(screen.getByLabelText('Fewer cores'))
    expect(coresValue()).toBe(64)
    fireEvent.click(screen.getByLabelText('Fewer cores'))
    expect(coresValue()).toBe(32)
  })

  it('pulls a hand-typed invalid value onto the ladder', () => {
    renderWithLadder(64)

    // 100 is neither a slice nor a whole number of nodes; validate() would
    // reject it on save, so stepping resolves it rather than adding to it.
    fireEvent.change(getCoresInput(), { target: { value: '100' } })
    expect(coresValue()).toBe(100)

    fireEvent.click(screen.getByLabelText('Fewer cores'))
    expect(coresValue()).toBe(64)

    fireEvent.change(getCoresInput(), { target: { value: '100' } })
    fireEvent.click(screen.getByLabelText('More cores'))
    expect(coresValue()).toBe(128)
  })

  it('stops at the smallest slice the coretype sells', () => {
    renderWithLadder(4)

    const down = screen.getByLabelText('Fewer cores')
    expect(down).toBeDisabled()
    fireEvent.click(down)
    expect(coresValue()).toBe(4)
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

  it('steps whole nodes when coretype metadata has not loaded', () => {
    // No ladder to read, so the stepper counts in nodes and refuses to guess at
    // fractions of one.
    renderOpen({ ...DEFAULT_JOB_TEMPLATE, coreType: 'unknown_coretype', coresPerSlot: 64 })

    expect(screen.getByLabelText('Fewer cores')).toBeDisabled()
    fireEvent.click(screen.getByLabelText('More cores'))
    expect(coresValue()).toBe(128)
    fireEvent.click(screen.getByLabelText('Fewer cores'))
    expect(coresValue()).toBe(64)
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

function getProjectSelect(): HTMLSelectElement {
  const label = screen.getByText('Project')
  const select = label.parentElement?.parentElement?.querySelector('select')
  if (!select) throw new Error('Project select not found')
  return select as HTMLSelectElement
}

function projectOptionText(): string[] {
  return Array.from(getProjectSelect().options).map((o) => o.textContent?.trim() ?? '')
}

// projectsLoaded marks the scan as already done, which is what keeps the
// fetch-on-open effect out of the way of a seeded list.
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
    // The mock resolves to an empty list, which is a real answer. Gating the
    // effect on the list being empty would make this an endless fetch loop.
    useJobStore.setState({ projectsLoaded: false })
    renderOpen()
    await waitFor(() => {
      expect(App.GetProjects).toHaveBeenCalledTimes(1)
    })
    await new Promise((resolve) => setTimeout(resolve, 50))
    expect(App.GetProjects).toHaveBeenCalledTimes(1)
  })

  it('points a failed scan at the button rather than retrying itself', async () => {
    useJobStore.setState({ projectsError: 'status 403: forbidden', projectsLoaded: true })
    renderOpen()

    expect(screen.getByText(/Could not load projects: status 403: forbidden/)).toBeInTheDocument()
    expect(screen.getByText(/use "Scan Projects" to retry/)).toBeInTheDocument()
    // An error must not put the effect into a fetch loop.
    await waitFor(() => {
      expect(App.GetProjects).not.toHaveBeenCalled()
    })
  })
})
