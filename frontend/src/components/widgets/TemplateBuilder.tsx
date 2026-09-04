import { useState, useEffect, useCallback, useMemo } from 'react'
import {
  XMarkIcon,
  MagnifyingGlassIcon,
  ExclamationTriangleIcon,
  TrashIcon,
  ChevronDownIcon,
  ChevronUpIcon,
  BookmarkIcon,
  FolderIcon,
  PlusIcon,
  MinusIcon,
} from '@heroicons/react/24/outline'
import clsx from 'clsx'
import { useJobStore, JobSpec, DEFAULT_JOB_TEMPLATE, AnalysisCode, Project } from '../../stores'
import * as App from '../../../wailsjs/go/wailsapp/App'

interface TemplateInfo {
  name: string
  path: string
  software: string
  hardware: string
  modTime: string
  job?: JobSpec
}

interface TemplateBuilderProps {
  isOpen: boolean
  initialTemplate?: JobSpec
  onClose: () => void
  onSave: (template: JobSpec) => void
}

// Node size assumed while coretype metadata has not loaded. Only a fallback:
// once coreTypes is populated the real per-node maximum is used everywhere.
const DEFAULT_NODE_CORES = 64

// Submit mode options
const SUBMIT_MODES = [
  { value: 'create_and_submit', label: 'Create and Submit' },
  { value: 'create_only', label: 'Create Only (Do Not Submit)' },
  { value: 'draft', label: 'Save as Draft' },
]

// Common license types
const LICENSE_TYPES = [
  { key: '', displayName: 'No License', placeholder: '' },
  { key: 'ANSYS_LICENSE_FILE', displayName: 'ANSYS License', placeholder: 'port@license-server' },
  { key: 'ABAQUS_LICENSE_FILE', displayName: 'Abaqus License', placeholder: 'port@license-server' },
  { key: 'LSTC_LICENSE_SERVER', displayName: 'LS-DYNA License', placeholder: 'port@license-server' },
  { key: 'CDLMD_LICENSE_FILE', displayName: 'STAR-CCM+ License', placeholder: 'port@license-server' },
  { key: 'LM_LICENSE_FILE', displayName: 'Generic FlexLM', placeholder: 'port@license-server' },
  { key: 'RLM_LICENSE', displayName: 'RLM License', placeholder: 'port@license-server' },
  { key: 'CUSTOM', displayName: 'Custom', placeholder: 'LICENSE_VAR=value' },
]

const PRESET_LICENSE_KEYS = new Set(
  LICENSE_TYPES.map((lt) => lt.key).filter((k) => k && k !== 'CUSTOM')
)

function parseCustomLicenseEntry(input: string): { key: string; value: string } | null {
  const eq = input.indexOf('=')
  if (eq <= 0) return null
  const key = input.slice(0, eq).trim()
  const value = input.slice(eq + 1).trim()
  if (!key || !value) return null
  if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(key)) return null
  return { key, value }
}

// Searchable select component
interface SearchableSelectProps {
  options: string[]
  value: string
  onChange: (value: string) => void
  placeholder?: string
  disabled?: boolean
  className?: string
}

function SearchableSelect({
  options,
  value,
  onChange,
  placeholder = 'Search...',
  disabled = false,
  className = '',
}: SearchableSelectProps) {
  const [isOpen, setIsOpen] = useState(false)
  const [search, setSearch] = useState('')

  const filteredOptions = useMemo(() => {
    if (!search) return options
    const searchLower = search.toLowerCase()
    return options.filter((opt) => opt.toLowerCase().includes(searchLower))
  }, [options, search])

  const handleSelect = (option: string) => {
    onChange(option)
    setSearch('')
    setIsOpen(false)
  }

  return (
    <div className={clsx('relative', className)}>
      <div className="relative">
        <input
          type="text"
          value={isOpen ? search : value}
          onChange={(e) => {
            setSearch(e.target.value)
            setIsOpen(true)
          }}
          onFocus={() => setIsOpen(true)}
          onBlur={() => setTimeout(() => setIsOpen(false), 200)}
          placeholder={placeholder}
          disabled={disabled}
          className="w-full px-3 py-2 text-sm border border-gray-300 dark:border-gray-600 rounded bg-white dark:bg-gray-800 focus:outline-none focus:ring-2 focus:ring-blue-500 disabled:bg-gray-100 dark:disabled:bg-gray-700"
        />
        <MagnifyingGlassIcon className="absolute right-3 top-2.5 w-4 h-4 text-gray-400" />
      </div>
      {isOpen && filteredOptions.length > 0 && (
        <div className="absolute z-10 w-full mt-1 bg-white dark:bg-gray-800 border border-gray-300 dark:border-gray-600 rounded shadow-lg max-h-48 overflow-auto">
          {filteredOptions.slice(0, 50).map((option) => (
            <button
              key={option}
              type="button"
              onClick={() => handleSelect(option)}
              className="w-full px-3 py-2 text-sm text-left hover:bg-gray-100 dark:hover:bg-gray-700"
            >
              {option}
            </button>
          ))}
          {filteredOptions.length > 50 && (
            <div className="px-3 py-2 text-xs text-gray-500">
              Showing 50 of {filteredOptions.length} results...
            </div>
          )}
        </div>
      )}
    </div>
  )
}

export function TemplateBuilder({ isOpen, initialTemplate, onClose, onSave }: TemplateBuilderProps) {
  const {
    coreTypes,
    analysisCodes,
    automations,
    isLoadingCoreTypes,
    isLoadingAnalysisCodes,
    isLoadingAutomations,
    projects,
    isLoadingProjects,
    coreTypesError,
    analysisCodesError,
    automationsError,
    projectsError,
    coreTypesLoaded,
    projectsLoaded,
    fetchCoreTypes,
    fetchAnalysisCodes,
    fetchAutomations,
    fetchProjects,
  } = useJobStore()

  // Form state
  const [template, setTemplate] = useState<JobSpec>(initialTemplate || DEFAULT_JOB_TEMPLATE)
  const [selectedAnalysis, setSelectedAnalysis] = useState<AnalysisCode | null>(null)
  const [licenseType, setLicenseType] = useState('')
  const [licenseValue, setLicenseValue] = useState('')
  // Shown when the user types a preset KEY=value into a CUSTOM license
  // field and we auto-switch them to the matching preset dropdown.
  const [licenseAutoSwitchHint, setLicenseAutoSwitchHint] = useState<string | null>(null)
  // Shown when a saved template's license was originally stored as a
  // preset key (e.g. {"RLM_LICENSE":"..."}) and we auto-classify it to the
  // preset on load. Explains to the user why the value appears without
  // the KEY= prefix they may remember typing.
  const [licenseLoadHint, setLicenseLoadHint] = useState<string | null>(null)
  const [errors, setErrors] = useState<string[]>([])

  // Type-time classification: if the user is in CUSTOM mode and types a
  // preset key with an = sign, auto-switch the dropdown to the preset and
  // strip the prefix from the value. Prevents the "license value changes
  // on reload" surprise for all future edits.
  const handleLicenseValueChange = useCallback((raw: string) => {
    if (licenseType === 'CUSTOM' && raw.includes('=')) {
      const parsed = parseCustomLicenseEntry(raw)
      if (parsed && PRESET_LICENSE_KEYS.has(parsed.key)) {
        setLicenseType(parsed.key)
        setLicenseValue(parsed.value)
        setLicenseAutoSwitchHint(
          `Switched to ${parsed.key} preset — the KEY= prefix isn't needed when a license type is selected.`
        )
        setLicenseLoadHint(null)
        return
      }
    }
    setLicenseValue(raw)
    setLicenseAutoSwitchHint(null)
  }, [licenseType])

  const [savedTemplates, setSavedTemplates] = useState<TemplateInfo[]>([])
  const [showSavedTemplates, setShowSavedTemplates] = useState(false)
  const [saveTemplateName, setSaveTemplateName] = useState('')
  const [showSaveDialog, setShowSaveDialog] = useState(false)

  const loadSavedTemplates = useCallback(async () => {
    try {
      const templates = await App.ListSavedTemplates()
      setSavedTemplates(templates as unknown as TemplateInfo[])
    } catch (err) {
      console.error('Failed to load saved templates:', err)
    }
  }, [])

  // Load saved templates when dialog opens
  useEffect(() => {
    if (isOpen) {
      loadSavedTemplates()
    }
  }, [isOpen, loadSavedTemplates])

  // Load coretype metadata on open so validation of saved templates works without a manual Scan.
  // Gated on the attempt, not on the list being empty: an empty answer would
  // otherwise be re-asked on every render. Rescanning is the button's job.
  useEffect(() => {
    if (isOpen && !coreTypesLoaded && !isLoadingCoreTypes) {
      fetchCoreTypes()
    }
  }, [isOpen, coreTypesLoaded, isLoadingCoreTypes, fetchCoreTypes])

  // Load the account's projects on open: the picker needs them to show anything
  // at all, and one small request is cheaper than making the user find an ID.
  // Gated on projectsLoaded rather than an empty list, since "no projects" is a
  // real answer that would otherwise be re-asked forever. Rescanning after that
  // is the Scan Projects button's job.
  useEffect(() => {
    if (isOpen && !projectsLoaded && !isLoadingProjects) {
      fetchProjects()
    }
  }, [isOpen, projectsLoaded, isLoadingProjects, fetchProjects])

  const handleLoadSavedTemplate = useCallback((templateInfo: TemplateInfo) => {
    if (templateInfo.job) {
      setTemplate(templateInfo.job as JobSpec)
      setLicenseAutoSwitchHint(null)
      setLicenseLoadHint(null)
      if (templateInfo.job.licenseSettings) {
        try {
          const parsed = JSON.parse(templateInfo.job.licenseSettings)
          const key = Object.keys(parsed)[0]
          if (key) {
            if (PRESET_LICENSE_KEYS.has(key)) {
              setLicenseType(key)
              setLicenseValue(parsed[key] || '')
              // Explain to the user why the value no longer shows the
              // KEY= prefix they may have originally typed.
              setLicenseLoadHint(
                `This template was saved with ${key}=… — loaded as the ${key} preset with the bare value.`
              )
            } else {
              setLicenseType('CUSTOM')
              setLicenseValue(`${key}=${parsed[key] || ''}`)
            }
          }
        } catch {
          // Invalid JSON, ignore
        }
      }
      setShowSavedTemplates(false)
    }
  }, [])

  const handleSaveTemplate = useCallback(async () => {
    if (!saveTemplateName.trim()) return
    try {
      await App.SaveTemplate(saveTemplateName, template as unknown as Parameters<typeof App.SaveTemplate>[1])
      setShowSaveDialog(false)
      setSaveTemplateName('')
      loadSavedTemplates()
    } catch (err) {
      console.error('Failed to save template:', err)
    }
  }, [saveTemplateName, template, loadSavedTemplates])

  const handleDeleteTemplate = useCallback(async (name: string) => {
    try {
      await App.DeleteTemplate(name)
      loadSavedTemplates()
    } catch (err) {
      console.error('Failed to delete template:', err)
    }
  }, [loadSavedTemplates])

  // Note: Software/hardware scanning is now user-initiated via Scan buttons
  // to give users control over when network calls happen

  // Initialize from template
  useEffect(() => {
    if (initialTemplate) {
      setTemplate(initialTemplate)
      setLicenseAutoSwitchHint(null)
      setLicenseLoadHint(null)
      // Parse license settings if present
      if (initialTemplate.licenseSettings) {
        try {
          const parsed = JSON.parse(initialTemplate.licenseSettings)
          const key = Object.keys(parsed)[0]
          if (key) {
            if (PRESET_LICENSE_KEYS.has(key)) {
              setLicenseType(key)
              setLicenseValue(parsed[key] || '')
              setLicenseLoadHint(
                `This template was saved with ${key}=… — loaded as the ${key} preset with the bare value.`
              )
            } else {
              setLicenseType('CUSTOM')
              setLicenseValue(`${key}=${parsed[key] || ''}`)
            }
          }
        } catch {
          // Invalid JSON, ignore
        }
      }
    }
  }, [initialTemplate])

  // Get options for dropdowns
  const analysisOptions = useMemo(() => {
    return analysisCodes.map((a) => `${a.name} (${a.code})`)
  }, [analysisCodes])

  const coreTypeOptions = useMemo(() => {
    return coreTypes.map((ct) => ct.code)
  }, [coreTypes])

  // Projects, default first and then by name, so the one most jobs belong to is
  // at the top of the list rather than wherever the API happened to return it.
  const projectOptions = useMemo(() => {
    return [...projects].sort((a, b) => {
      if (a.isDefault !== b.isDefault) return a.isDefault ? -1 : 1
      return a.name.localeCompare(b.name)
    })
  }, [projects])

  // A project ID the account's list does not contain — a template or CSV written
  // against another account, or a project since deleted. Kept as an option so
  // opening the builder does not silently unassign it.
  const unlistedProjectId = useMemo(() => {
    if (!template.projectId) return ''
    return projects.some((p) => p.id === template.projectId) ? '' : template.projectId
  }, [projects, template.projectId])

  // The budget line is what distinguishes two similarly named projects, so it
  // belongs in the option text. "(no budget)" adds nothing next to a name.
  const projectLabel = useCallback((project: Project) => {
    const budget = project.remainingAmounts.filter((a) => a && a !== '(no budget)')
    const parts = [project.name]
    if (project.isDefault) parts.push('(default)')
    if (budget.length > 0) parts.push(`— ${budget.join('; ')}`)
    return parts.join(' ')
  }, [])

  // Build version display→code mapping for the dropdown
  const versionMap = useMemo(() => {
    if (!selectedAnalysis) return new Map<string, string>()
    const map = new Map<string, string>()
    for (const v of selectedAnalysis.versions) {
      const display = v.version || v.versionCode
      const code = v.versionCode || v.version
      map.set(display, code)
    }
    return map
  }, [selectedAnalysis])

  const versionOptions = useMemo(() => {
    return Array.from(versionMap.keys())
  }, [versionMap])

  // The core counts this coretype sells within one node, ascending — the
  // platform's own list (4, 8, 16, 32, 64 for a 64-core node). Above a full node
  // only whole nodes are valid. Those are the two rules validate() enforces, and
  // the two the cores stepper walks, so both read them from here.
  const coreLadder = useMemo(() => {
    const ct = coreTypes.find((c) => c.code === template.coreType)
    if (!ct || ct.cores.length === 0) {
      return [] as number[]
    }
    return Array.from(new Set(ct.cores.filter((n) => n > 0))).sort((a, b) => a - b)
  }, [coreTypes, template.coreType])

  // Base unit for cores: max cores per node for selected hardware.
  // Users can enter multiples of this value (64, 128, 192, etc.)
  const coresBaseUnit = useMemo(() => {
    if (coreLadder.length > 0) {
      return coreLadder[coreLadder.length - 1]
    }
    // Metadata not loaded yet — fall back to the stored value so the hint and the
    // empty-field default are stated in terms of what the user already has.
    if (template.coresPerSlot > 0) {
      return template.coresPerSlot
    }
    return DEFAULT_NODE_CORES
  }, [coreLadder, template.coresPerSlot])

  // Node size the stepper counts in. Deliberately not coresBaseUnit: that one
  // falls back to the live value, and stepping by the number being stepped would
  // double it on every click.
  const nodeCores = coreLadder.length > 0 ? coreLadder[coreLadder.length - 1] : DEFAULT_NODE_CORES

  // Smallest value the coretype accepts, so the stepper cannot walk below it.
  const coresMin = coreLadder.length > 0 ? coreLadder[0] : 1

  // Handle analysis code change
  const handleAnalysisChange = useCallback(
    (displayName: string) => {
      // Extract code from "Name (code)" format
      const match = displayName.match(/\(([^)]+)\)$/)
      const code = match ? match[1] : displayName

      const analysis = analysisCodes.find((a) => a.code === code)
      setSelectedAnalysis(analysis || null)

      setTemplate((t) => ({
        ...t,
        analysisCode: code,
        analysisVersion: analysis?.versions[0]?.versionCode || analysis?.versions[0]?.version || '',
      }))
    },
    [analysisCodes]
  )

  // Handle core type change — defaults to max cores (full node)
  const handleCoreTypeChange = useCallback(
    (coreType: string) => {
      const ct = coreTypes.find((c) => c.code === coreType)
      // Default to max cores (base unit for multiples)
      const defaultCores = ct && ct.cores.length > 0 ? Math.max(...ct.cores) : DEFAULT_NODE_CORES

      setTemplate((t) => ({
        ...t,
        coreType,
        coresPerSlot: defaultCores,
      }))
    },
    [coreTypes]
  )

  // Update template field
  const updateField = useCallback(<K extends keyof JobSpec>(key: K, value: JobSpec[K]) => {
    setTemplate((t) => ({ ...t, [key]: value }))
  }, [])

  // Allow any positive value — validation happens on save
  const handleCoresChange = useCallback(
    (value: number) => {
      if (value <= 0) {
        updateField('coresPerSlot', coresBaseUnit)
        return
      }
      // Allow user to enter any value - validation will check if it's valid
      updateField('coresPerSlot', value)
    },
    [coresBaseUnit, updateField]
  )

  // The next valid value up: the next slice within a node while there is one,
  // then whole nodes. A function of the current value rather than a constant
  // because the ladder is uneven — 4, 8, 16, 32, 64 has no single step size.
  const nextCores = useCallback(
    (current: number) => {
      const nextSlice = coreLadder.find((c) => c > current)
      if (nextSlice !== undefined) {
        return nextSlice
      }
      return (Math.floor(current / nodeCores) + 1) * nodeCores
    },
    [coreLadder, nodeCores]
  )

  // The next valid value down, or the current one when nothing smaller is known
  // to be valid — which is what disables the minus control.
  const prevCores = useCallback(
    (current: number) => {
      if (current > nodeCores) {
        // Whole nodes on the way down, and Math.ceil so a hand-typed 100 lands on
        // 64 rather than on another value the platform would reject.
        return Math.max((Math.ceil(current / nodeCores) - 1) * nodeCores, nodeCores)
      }
      const smaller = coreLadder.filter((c) => c < current)
      if (smaller.length > 0) {
        return smaller[smaller.length - 1]
      }
      return current
    },
    [coreLadder, nodeCores]
  )

  const stepCores = useCallback(
    (direction: 1 | -1) => {
      const current = template.coresPerSlot > 0 ? template.coresPerSlot : coresBaseUnit
      updateField('coresPerSlot', direction > 0 ? nextCores(current) : prevCores(current))
    },
    [template.coresPerSlot, coresBaseUnit, nextCores, prevCores, updateField]
  )

  const coresStepUp = nextCores(template.coresPerSlot)
  const coresStepDown = prevCores(template.coresPerSlot)

  // Validate template — cores allow fractional nodes OR multi-node (multiples of max)
  const validate = useCallback((): string[] => {
    const errs: string[] = []

    if (!template.jobName.trim()) {
      errs.push('Job name is required')
    }
    if (!template.analysisCode.trim()) {
      errs.push('Analysis code is required')
    }
    if (!template.coreType.trim()) {
      errs.push('Core type is required')
    }
    if (!template.command.trim()) {
      errs.push('Command is required')
    }
    if (template.coresPerSlot <= 0) {
      errs.push('Cores must be positive')
    } else {
      // Only enforce the combinations check once coretype metadata is loaded.
      // Without metadata, trust the stored value — the platform API is the ultimate validator.
      // Read from the same ladder the stepper walks, so the control cannot offer a
      // value this then rejects.
      if (coreLadder.length > 0) {
        const isValidFractional = coreLadder.includes(template.coresPerSlot)
        const isValidMultiNode = template.coresPerSlot % nodeCores === 0
        if (!isValidFractional && !isValidMultiNode) {
          errs.push(
            `Cores ${template.coresPerSlot} not valid for coretype '${template.coreType}'. Valid values: ${coreLadder.join(', ')} or a multiple of ${nodeCores}.`
          )
        }
      }
    }
    if (template.walltimeHours <= 0) {
      errs.push('Walltime must be positive')
    }
    // A feature set needs both halves. Catching it here says which box is empty,
    // where BuildJobRequest can only refuse the job later on.
    if (template.licenseFeatureName.trim() && template.licensesPerJob <= 0) {
      errs.push('Licenses per job must be 1 or more when a license feature name is set')
    }
    if (!template.licenseFeatureName.trim() && template.licensesPerJob > 0) {
      errs.push('License feature name is required when licenses per job is set')
    }
    if (licenseType === 'CUSTOM' && licenseValue.trim()) {
      if (!parseCustomLicenseEntry(licenseValue)) {
        errs.push(
          'Custom license value must be in KEY=value form (e.g. FOO_LICENSE=port@server). ' +
          'If your key matches a listed preset (ANSYS, RLM, etc.), pick it from the dropdown instead.'
        )
      }
    }

    return errs
  }, [template, coreLadder, nodeCores, licenseType, licenseValue])

  // Handle save
  const handleSave = useCallback(() => {
    const validationErrors = validate()
    if (validationErrors.length > 0) {
      setErrors(validationErrors)
      return
    }

    let licenseSettings = ''
    if (licenseType && licenseValue) {
      if (licenseType === 'CUSTOM') {
        const parsed = parseCustomLicenseEntry(licenseValue)
        if (parsed) {
          licenseSettings = JSON.stringify({ [parsed.key]: parsed.value })
        }
      } else {
        licenseSettings = JSON.stringify({ [licenseType]: licenseValue })
      }
    }

    // Trailing spaces in a feature name would reach the license server verbatim,
    // and a count left behind by a name the user cleared must not be sent alone —
    // validate() only allows the empty-name case through with a zero count.
    const licenseFeatureName = template.licenseFeatureName.trim()

    const finalTemplate = {
      ...template,
      licenseSettings,
      licenseFeatureName,
      licensesPerJob: licenseFeatureName ? template.licensesPerJob : 0,
    }

    onSave(finalTemplate)
  }, [template, licenseType, licenseValue, validate, onSave])

  if (!isOpen) return null

  return (
    <div className="fixed inset-0 bg-black/50 flex items-center justify-center z-50">
      <div className="bg-white dark:bg-gray-800 rounded-lg shadow-xl w-[800px] max-w-[90vw] max-h-[90vh] flex flex-col">
        {/* Header */}
        <div className="flex items-center justify-between px-6 py-4 border-b border-gray-200 dark:border-gray-700">
          <h2 className="text-lg font-semibold">Configure Job Template</h2>
          <button
            onClick={onClose}
            className="p-1 hover:bg-gray-100 dark:hover:bg-gray-700 rounded"
          >
            <XMarkIcon className="w-5 h-5" />
          </button>
        </div>

        <div className="border-b border-gray-200 dark:border-gray-700">
          <button
            onClick={() => setShowSavedTemplates(!showSavedTemplates)}
            className="w-full flex items-center justify-between px-6 py-3 text-sm font-medium text-gray-700 dark:text-gray-300 hover:bg-gray-50 dark:hover:bg-gray-700/50"
          >
            <span className="flex items-center gap-2">
              <FolderIcon className="w-4 h-4" />
              Saved Templates ({savedTemplates.length})
            </span>
            {showSavedTemplates ? (
              <ChevronUpIcon className="w-4 h-4" />
            ) : (
              <ChevronDownIcon className="w-4 h-4" />
            )}
          </button>
          {showSavedTemplates && (
            <div className="px-6 pb-4">
              {savedTemplates.length === 0 ? (
                <p className="text-sm text-gray-500 italic">No saved templates yet</p>
              ) : (
                <div className="grid gap-2 max-h-48 overflow-y-auto">
                  {savedTemplates.map((t) => (
                    <div
                      key={t.name}
                      className="flex items-center justify-between p-2 bg-gray-50 dark:bg-gray-700/50 rounded hover:bg-gray-100 dark:hover:bg-gray-700"
                    >
                      <button
                        onClick={() => handleLoadSavedTemplate(t)}
                        className="flex-1 text-left"
                      >
                        <div className="font-medium text-sm">{t.name}</div>
                        <div className="text-xs text-gray-500">
                          {t.software && `${t.software} `}
                          {t.hardware && `• ${t.hardware}`}
                        </div>
                      </button>
                      <button
                        onClick={(e) => {
                          e.stopPropagation()
                          handleDeleteTemplate(t.name)
                        }}
                        className="p-1 text-gray-400 hover:text-red-500"
                        title="Delete template"
                      >
                        <TrashIcon className="w-4 h-4" />
                      </button>
                    </div>
                  ))}
                </div>
              )}
            </div>
          )}
        </div>

        {/* Errors */}
        {errors.length > 0 && (
          <div className="mx-6 mt-4 p-3 bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800 rounded">
            <div className="flex items-start gap-2 text-red-700 dark:text-red-400">
              <ExclamationTriangleIcon className="w-5 h-5 flex-shrink-0 mt-0.5" />
              <div>
                {errors.map((err, i) => (
                  <div key={i} className="text-sm">
                    {err}
                  </div>
                ))}
              </div>
            </div>
          </div>
        )}

        {/* Form */}
        <div className="flex-1 overflow-auto p-6 space-y-6">
          {/* Software Configuration */}
          <div>
            <label className="block text-sm font-medium mb-1">Job Name</label>
            <input
              type="text"
              value={template.jobName}
              onChange={(e) => updateField('jobName', e.target.value)}
              placeholder="Run_1"
              className="w-full px-3 py-2 text-sm border border-gray-300 dark:border-gray-600 rounded bg-white dark:bg-gray-800 focus:outline-none focus:ring-2 focus:ring-blue-500"
            />
          </div>
          <section>
            <h3 className="text-sm font-semibold text-gray-700 dark:text-gray-300 mb-3 pb-1 border-b border-gray-200 dark:border-gray-700">
              Software Configuration
            </h3>
            <div className="grid grid-cols-2 gap-4">
              <div>
                <div className="flex items-center justify-between mb-1">
                  <label className="block text-sm font-medium">Analysis Code</label>
                  <button
                    type="button"
                    onClick={() => fetchAnalysisCodes()}
                    disabled={isLoadingAnalysisCodes}
                    className="text-xs text-blue-600 hover:text-blue-800 disabled:text-gray-400 disabled:cursor-not-allowed"
                  >
                    {isLoadingAnalysisCodes ? 'Scanning...' : 'Scan Software'}
                  </button>
                </div>
                {isLoadingAnalysisCodes && analysisCodes.length === 0 && (
                  <p className="mb-1 text-xs text-gray-500 italic">
                    First scan may take up to several minutes...
                  </p>
                )}
                <SearchableSelect
                  options={analysisOptions}
                  value={
                    selectedAnalysis
                      ? `${selectedAnalysis.name} (${selectedAnalysis.code})`
                      : template.analysisCode
                  }
                  onChange={handleAnalysisChange}
                  placeholder={analysisCodes.length === 0 ? 'Click "Scan Software" to load' : 'Search software...'}
                  disabled={isLoadingAnalysisCodes || analysisCodes.length === 0}
                />
                {analysisCodesError && (
                  <p className="mt-1 text-xs text-red-500">{analysisCodesError}</p>
                )}
              </div>
              <div>
                <label className="block text-sm font-medium mb-1">Version</label>
                <SearchableSelect
                  options={versionOptions}
                  value={
                    selectedAnalysis?.versions.find(
                      (v) => v.versionCode === template.analysisVersion || v.version === template.analysisVersion
                    )?.version || template.analysisVersion
                  }
                  onChange={(v) => updateField('analysisVersion', versionMap.get(v) || v)}
                  placeholder="Select version..."
                  disabled={!selectedAnalysis}
                />
              </div>
              <div className="col-span-2">
                <label className="block text-sm font-medium mb-1">Command</label>
                <textarea
                  value={template.command}
                  onChange={(e) => updateField('command', e.target.value)}
                  placeholder="./run.sh"
                  rows={3}
                  className="w-full px-3 py-2 text-sm font-mono border border-gray-300 dark:border-gray-600 rounded bg-white dark:bg-gray-800 focus:outline-none focus:ring-2 focus:ring-blue-500 resize-none"
                />
              </div>
            </div>
          </section>

          {/* Hardware Configuration */}
          <section>
            <h3 className="text-sm font-semibold text-gray-700 dark:text-gray-300 mb-3 pb-1 border-b border-gray-200 dark:border-gray-700">
              Hardware Configuration
            </h3>
            <div className="grid grid-cols-3 gap-4">
              <div>
                <div className="flex items-center justify-between mb-1">
                  <label className="block text-sm font-medium">Core Type</label>
                  <button
                    type="button"
                    onClick={() => fetchCoreTypes()}
                    disabled={isLoadingCoreTypes}
                    className="text-xs text-blue-600 hover:text-blue-800 disabled:text-gray-400 disabled:cursor-not-allowed"
                  >
                    {isLoadingCoreTypes ? 'Scanning...' : 'Scan Coretypes'}
                  </button>
                </div>
                {isLoadingCoreTypes && coreTypes.length === 0 && (
                  <p className="mb-1 text-xs text-gray-500 italic">
                    First scan may take up to several minutes...
                  </p>
                )}
                <SearchableSelect
                  options={coreTypeOptions}
                  value={template.coreType}
                  onChange={handleCoreTypeChange}
                  placeholder={coreTypes.length === 0 ? 'Click "Scan Coretypes" to load' : 'Search coretypes...'}
                  disabled={isLoadingCoreTypes || coreTypes.length === 0}
                />
                {coreTypesError && (
                  <p className="mt-1 text-xs text-red-500">{coreTypesError}</p>
                )}
              </div>
              <div>
                <label className="block text-sm font-medium mb-1">Cores</label>
                <div className="relative">
                  <input
                    type="number"
                    min={coresMin}
                    step={Math.max(coresStepUp - template.coresPerSlot, 1)}
                    value={template.coresPerSlot}
                    onChange={(e) => handleCoresChange(Number(e.target.value))}
                    onBlur={(e) => handleCoresChange(Number(e.target.value))}
                    onKeyDown={(e) => {
                      // The native arrows count by a fixed step, which lands between
                      // valid values on an uneven ladder, so they are replaced rather
                      // than merely re-sized.
                      if (e.key === 'ArrowUp') {
                        e.preventDefault()
                        stepCores(1)
                      } else if (e.key === 'ArrowDown') {
                        e.preventDefault()
                        stepCores(-1)
                      }
                    }}
                    className="w-full pl-3 pr-16 py-2 text-sm border border-gray-300 dark:border-gray-600 rounded bg-white dark:bg-gray-800 focus:outline-none focus:ring-2 focus:ring-blue-500 [appearance:textfield] [&::-webkit-inner-spin-button]:appearance-none [&::-webkit-outer-spin-button]:appearance-none"
                  />
                  <div className="absolute inset-y-0 right-1 flex items-center gap-1">
                    <button
                      type="button"
                      onClick={() => stepCores(-1)}
                      disabled={coresStepDown >= template.coresPerSlot}
                      aria-label="Fewer cores"
                      title={
                        coresStepDown < template.coresPerSlot
                          ? `Down to ${coresStepDown} cores`
                          : `${template.coresPerSlot} is the smallest valid size`
                      }
                      className="flex items-center justify-center w-6 h-6 rounded border border-gray-300 dark:border-gray-600 text-gray-600 dark:text-gray-300 hover:bg-gray-100 dark:hover:bg-gray-700 disabled:opacity-40 disabled:hover:bg-transparent"
                    >
                      <MinusIcon className="w-3 h-3" />
                    </button>
                    <button
                      type="button"
                      onClick={() => stepCores(1)}
                      aria-label="More cores"
                      title={`Up to ${coresStepUp} cores`}
                      className="flex items-center justify-center w-6 h-6 rounded border border-gray-300 dark:border-gray-600 text-gray-600 dark:text-gray-300 hover:bg-gray-100 dark:hover:bg-gray-700"
                    >
                      <PlusIcon className="w-3 h-3" />
                    </button>
                  </div>
                </div>
                <p className="mt-1 text-xs text-gray-500">
                  {coreLadder.length > 0
                    ? `Valid: ${coreLadder.join(', ')} or multiples of ${coresBaseUnit}`
                    : `Multiples of ${coresBaseUnit}`}
                </p>
              </div>
              <div>
                <label className="block text-sm font-medium mb-1">Walltime (hours)</label>
                <input
                  type="number"
                  step="1"
                  min="1"
                  value={Math.round(template.walltimeHours)}
                  onChange={(e) => updateField('walltimeHours', Math.max(1, Math.round(Number(e.target.value))))}
                  className="w-full px-3 py-2 text-sm border border-gray-300 dark:border-gray-600 rounded bg-white dark:bg-gray-800 focus:outline-none focus:ring-2 focus:ring-blue-500"
                />
              </div>
            </div>
          </section>

          {/* Project & Tags */}
          <section>
            <h3 className="text-sm font-semibold text-gray-700 dark:text-gray-300 mb-3 pb-1 border-b border-gray-200 dark:border-gray-700">
              Project & Tags
            </h3>
            <div className="grid grid-cols-2 gap-4">
              <div>
                <div className="flex items-center justify-between mb-1">
                  <label className="block text-sm font-medium">Project</label>
                  <button
                    type="button"
                    onClick={() => fetchProjects()}
                    disabled={isLoadingProjects}
                    className="text-xs text-blue-600 hover:text-blue-800 disabled:text-gray-400 disabled:cursor-not-allowed"
                  >
                    {isLoadingProjects ? 'Scanning...' : 'Scan Projects'}
                  </button>
                </div>
                <select
                  value={template.projectId}
                  onChange={(e) => updateField('projectId', e.target.value)}
                  disabled={isLoadingProjects}
                  className="w-full px-3 py-2 text-sm border border-gray-300 dark:border-gray-600 rounded bg-white dark:bg-gray-800 focus:outline-none focus:ring-2 focus:ring-blue-500 disabled:bg-gray-100 dark:disabled:bg-gray-700"
                >
                  <option value="">
                    {isLoadingProjects ? 'Loading projects…' : 'No project'}
                  </option>
                  {projectOptions.map((project) => (
                    <option key={project.id} value={project.id}>
                      {projectLabel(project)}
                    </option>
                  ))}
                  {unlistedProjectId && (
                    <option value={unlistedProjectId}>
                      {unlistedProjectId} (not in this account's projects)
                    </option>
                  )}
                </select>
                {projectsError ? (
                  // Retrying is what the header button does, so the error only has
                  // to say what went wrong and point at it.
                  <p className="mt-1 text-xs text-red-500">
                    Could not load projects: {projectsError} — use &quot;Scan Projects&quot; to retry
                  </p>
                ) : (
                  <p className="mt-1 text-xs text-gray-500">
                    {projects.length === 0 && !isLoadingProjects
                      ? 'No projects available for this API key'
                      : 'Optional — jobs are billed to the selected project'}
                  </p>
                )}
              </div>
              <div>
                <label className="block text-sm font-medium mb-1">Tags</label>
                <input
                  type="text"
                  value={template.tags.join(', ')}
                  onChange={(e) =>
                    updateField(
                      'tags',
                      e.target.value
                        .split(',')
                        .map((t) => t.trim())
                        .filter(Boolean)
                    )
                  }
                  placeholder="tag1, tag2 (optional)"
                  className="w-full px-3 py-2 text-sm border border-gray-300 dark:border-gray-600 rounded bg-white dark:bg-gray-800 focus:outline-none focus:ring-2 focus:ring-blue-500"
                />
              </div>
            </div>
          </section>

          {/* Automations */}
          <section>
            <h3 className="text-sm font-semibold text-gray-700 dark:text-gray-300 mb-3 pb-1 border-b border-gray-200 dark:border-gray-700">
              Automations
            </h3>
            <div>
              <div className="flex items-center gap-2 mb-2">
                <button
                  type="button"
                  onClick={() => fetchAutomations()}
                  disabled={isLoadingAutomations}
                  className="px-3 py-1.5 text-xs bg-gray-100 hover:bg-gray-200 dark:bg-gray-700 dark:hover:bg-gray-600 rounded disabled:opacity-50"
                >
                  {isLoadingAutomations ? 'Loading...' : 'Load Automations'}
                </button>
                <span className="text-xs text-gray-500">
                  {automations.length > 0 && `${automations.length} available`}
                </span>
              </div>
              {automationsError && (
                <p className="mb-2 text-xs text-red-500">{automationsError}</p>
              )}
              {automations.length > 0 && (
                <div className="border border-gray-300 dark:border-gray-600 rounded max-h-32 overflow-y-auto">
                  {automations.map((auto) => (
                    <label
                      key={auto.id}
                      className="flex items-center gap-2 px-3 py-2 hover:bg-gray-50 dark:hover:bg-gray-800 cursor-pointer border-b border-gray-200 dark:border-gray-700 last:border-b-0"
                    >
                      <input
                        type="checkbox"
                        checked={template.automations.includes(auto.id)}
                        onChange={(e) => {
                          const newAutomations = e.target.checked
                            ? [...template.automations, auto.id]
                            : template.automations.filter((id) => id !== auto.id)
                          updateField('automations', newAutomations)
                        }}
                        className="rounded border-gray-300 text-blue-600 focus:ring-blue-500"
                      />
                      <div className="flex-1 min-w-0">
                        <div className="text-sm font-medium truncate">{auto.name}</div>
                        {auto.description && (
                          <div className="text-xs text-gray-500 truncate">{auto.description}</div>
                        )}
                      </div>
                    </label>
                  ))}
                </div>
              )}
              {template.automations.length > 0 && (
                <div className="mt-2 text-xs text-gray-600 dark:text-gray-400">
                  Selected: {template.automations.length} automation(s)
                </div>
              )}
            </div>
          </section>

          {/* License Configuration */}
          <section>
            <h3 className="text-sm font-semibold text-gray-700 dark:text-gray-300 mb-3 pb-1 border-b border-gray-200 dark:border-gray-700">
              License Settings
            </h3>
            <div className="grid grid-cols-2 gap-4">
              <div>
                <label className="block text-sm font-medium mb-1">License Type</label>
                <select
                  value={licenseType}
                  onChange={(e) => {
                    setLicenseType(e.target.value)
                    // User made an explicit choice — both hint states are stale.
                    setLicenseAutoSwitchHint(null)
                    setLicenseLoadHint(null)
                  }}
                  className="w-full px-3 py-2 text-sm border border-gray-300 dark:border-gray-600 rounded bg-white dark:bg-gray-800 focus:outline-none focus:ring-2 focus:ring-blue-500"
                >
                  {LICENSE_TYPES.map((lt) => (
                    <option key={lt.key} value={lt.key}>
                      {lt.displayName}
                    </option>
                  ))}
                </select>
              </div>
              <div>
                <label className="block text-sm font-medium mb-1">License Value</label>
                <input
                  type="text"
                  value={licenseValue}
                  onChange={(e) => handleLicenseValueChange(e.target.value)}
                  placeholder={
                    LICENSE_TYPES.find((lt) => lt.key === licenseType)?.placeholder ||
                    'port@license-server'
                  }
                  disabled={!licenseType}
                  className="w-full px-3 py-2 text-sm border border-gray-300 dark:border-gray-600 rounded bg-white dark:bg-gray-800 focus:outline-none focus:ring-2 focus:ring-blue-500 disabled:bg-gray-100 dark:disabled:bg-gray-700"
                />
                {licenseAutoSwitchHint && (
                  <p className="mt-1 text-xs text-blue-600 dark:text-blue-400">
                    {licenseAutoSwitchHint}
                  </p>
                )}
                {licenseLoadHint && !licenseAutoSwitchHint && (
                  <p className="mt-1 text-xs text-blue-600 dark:text-blue-400">
                    {licenseLoadHint}
                  </p>
                )}
              </div>
            </div>

            {/* Feature set — optional, and only sent when both fields are filled in. */}
            <div className="grid grid-cols-2 gap-4 mt-4">
              <div>
                <label className="block text-sm font-medium mb-1">License Feature Name</label>
                <input
                  type="text"
                  value={template.licenseFeatureName}
                  onChange={(e) => updateField('licenseFeatureName', e.target.value)}
                  placeholder="e.g. ansys_hpc"
                  className="w-full px-3 py-2 text-sm border border-gray-300 dark:border-gray-600 rounded bg-white dark:bg-gray-800 focus:outline-none focus:ring-2 focus:ring-blue-500"
                />
                <p className="mt-1 text-xs text-gray-500">
                  Optional — the feature this job checks out of your license server
                </p>
              </div>
              <div>
                <label className="block text-sm font-medium mb-1">Licenses Per Job</label>
                <input
                  type="number"
                  min={1}
                  step={1}
                  // 0 means "unset", and showing a literal 0 in a box the platform
                  // would reject reads as a real value.
                  value={template.licensesPerJob > 0 ? template.licensesPerJob : ''}
                  onChange={(e) => {
                    const count = Number(e.target.value)
                    updateField('licensesPerJob', Number.isFinite(count) ? Math.max(0, Math.floor(count)) : 0)
                  }}
                  disabled={!template.licenseFeatureName.trim()}
                  className="w-full px-3 py-2 text-sm border border-gray-300 dark:border-gray-600 rounded bg-white dark:bg-gray-800 focus:outline-none focus:ring-2 focus:ring-blue-500 disabled:bg-gray-100 dark:disabled:bg-gray-700"
                />
                <p className="mt-1 text-xs text-gray-500">
                  {template.licenseFeatureName.trim()
                    ? 'Seats checked out per job (1 or more)'
                    : 'Enter a feature name first'}
                </p>
              </div>
            </div>
          </section>

          {/* Submit Options */}
          <section>
            <h3 className="text-sm font-semibold text-gray-700 dark:text-gray-300 mb-3 pb-1 border-b border-gray-200 dark:border-gray-700">
              Submit Options
            </h3>
            <div className="grid grid-cols-2 gap-4">
              <div>
                <label className="block text-sm font-medium mb-1">Submit Mode</label>
                <select
                  value={template.submitMode}
                  onChange={(e) => updateField('submitMode', e.target.value)}
                  className="w-full px-3 py-2 text-sm border border-gray-300 dark:border-gray-600 rounded bg-white dark:bg-gray-800 focus:outline-none focus:ring-2 focus:ring-blue-500"
                >
                  {SUBMIT_MODES.map((mode) => (
                    <option key={mode.value} value={mode.value}>
                      {mode.label}
                    </option>
                  ))}
                </select>
              </div>
              <div className="flex items-end">
                <label className="flex items-center gap-2 cursor-pointer">
                  <input
                    type="checkbox"
                    checked={template.isLowPriority}
                    onChange={(e) => updateField('isLowPriority', e.target.checked)}
                    className="w-4 h-4 text-blue-500 border-gray-300 rounded focus:ring-blue-500"
                  />
                  <span className="text-sm">Use ODE (instead of default ODP - warning, ODE jobs may be interrupted)</span>
                </label>
              </div>
            </div>
          </section>
        </div>

        {/* Footer */}
        <div className="flex items-center justify-between px-6 py-4 border-t border-gray-200 dark:border-gray-700">
          <div className="flex items-center gap-2">
            {showSaveDialog ? (
              <div className="flex items-center gap-2">
                <input
                  type="text"
                  value={saveTemplateName}
                  onChange={(e) => setSaveTemplateName(e.target.value)}
                  placeholder="Template name..."
                  className="px-2 py-1 text-sm border border-gray-300 dark:border-gray-600 rounded bg-white dark:bg-gray-800 focus:outline-none focus:ring-2 focus:ring-blue-500"
                  autoFocus
                  onKeyDown={(e) => {
                    if (e.key === 'Enter') handleSaveTemplate()
                    if (e.key === 'Escape') {
                      setShowSaveDialog(false)
                      setSaveTemplateName('')
                    }
                  }}
                />
                <button
                  onClick={handleSaveTemplate}
                  disabled={!saveTemplateName.trim()}
                  className="px-2 py-1 text-xs text-white bg-green-500 hover:bg-green-600 disabled:bg-gray-400 rounded"
                >
                  Save
                </button>
                <button
                  onClick={() => {
                    setShowSaveDialog(false)
                    setSaveTemplateName('')
                  }}
                  className="px-2 py-1 text-xs text-gray-600 dark:text-gray-400 hover:bg-gray-100 dark:hover:bg-gray-700 rounded"
                >
                  Cancel
                </button>
              </div>
            ) : (
              <button
                onClick={() => setShowSaveDialog(true)}
                className="flex items-center gap-1 px-3 py-1.5 text-sm text-gray-600 dark:text-gray-400 hover:bg-gray-100 dark:hover:bg-gray-700 rounded"
              >
                <BookmarkIcon className="w-4 h-4" />
                Save as Template
              </button>
            )}
          </div>

          <div className="flex items-center gap-3">
            <button
              onClick={onClose}
              className="px-4 py-2 text-sm text-gray-600 dark:text-gray-400 hover:bg-gray-100 dark:hover:bg-gray-700 rounded"
            >
              Cancel
            </button>
            <button
              onClick={handleSave}
              className="px-4 py-2 text-sm text-white bg-blue-500 hover:bg-blue-600 rounded"
            >
              Use Template
            </button>
          </div>
        </div>
      </div>
    </div>
  )
}
