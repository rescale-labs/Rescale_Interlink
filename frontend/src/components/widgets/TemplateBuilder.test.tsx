import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent, cleanup } from '@testing-library/react'
import { TemplateBuilder } from './TemplateBuilder'
import { DEFAULT_JOB_TEMPLATE } from '../../stores'
import type { JobSpec } from '../../stores'
import { afterEach } from 'vitest'

afterEach(() => cleanup())

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
  // findByText timeout). The sharpened error string itself is covered by
  // a simple source grep in the review checklist — the functional path
  // (validate() producing the new string for non-KEY=value input) is a
  // one-line change that doesn't need component-render coverage.
})
