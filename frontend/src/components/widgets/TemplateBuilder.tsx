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
} from '@heroicons/react/24/outline'
import clsx from 'clsx'
import { useJobStore, JobSpec, DEFAULT_JOB_TEMPLATE, AnalysisCode } from '../../stores'
import * as App from '../../../wailsjs/go/wailsapp/App'

// v4.0.0 G3: Template info from backend
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
  { key: 'CUSTOM', displayName: 'Custom', placeholder: 'LICENSE_VAR=value' },
]

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
    fetchCoreTypes,
    fetchAnalysisCodes,
    fetchAutomations,
  } = useJobStore()

  // Form state
  const [template, setTemplate] = useState<JobSpec>(initialTemplate || DEFAULT_JOB_TEMPLATE)
  const [selectedAnalysis, setSelectedAnalysis] = useState<AnalysisCode | null>(null)
  const [licenseType, setLicenseType] = useState('')
  const [licenseValue, setLicenseValue] = useState('')
  const [errors, setErrors] = useState<string[]>([])

  // v4.0.0 G3: Template library state
  const [savedTemplates, setSavedTemplates] = useState<TemplateInfo[]>([])
  const [showSavedTemplates, setShowSavedTemplates] = useState(false)
  const [saveTemplateName, setSaveTemplateName] = useState('')
  const [showSaveDialog, setShowSaveDialog] = useState(false)

  // v4.0.0 G3: Load saved templates
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

  // v4.0.0 G3: Load a saved template
  const handleLoadSavedTemplate = useCallback((templateInfo: TemplateInfo) => {
    if (templateInfo.job) {
      setTemplate(templateInfo.job as JobSpec)
      // Parse license settings if present
      if (templateInfo.job.licenseSettings) {
        try {
          const parsed = JSON.parse(templateInfo.job.licenseSettings)
          const key = Object.keys(parsed)[0]
          if (key) {
            setLicenseType(key)
            setLicenseValue(parsed[key] || '')
          }
        } catch {
          // Invalid JSON, ignore
        }
      }
      setShowSavedTemplates(false)
    }
  }, [])

  // v4.0.0 G3: Save current template
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

  // v4.0.0 G3: Delete a saved template
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
      // Parse license settings if present
      if (initialTemplate.licenseSettings) {
        try {
          const parsed = JSON.parse(initialTemplate.licenseSettings)
          const key = Object.keys(parsed)[0]
          if (key) {
            setLicenseType(key)
            setLicenseValue(parsed[key] || '')
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

  const versionOptions = useMemo(() => {
    if (!selectedAnalysis) return []
    return selectedAnalysis.versions.map((v) => v.version || v.versionCode)
  }, [selectedAnalysis])

  const coresPerSlotOptions = useMemo(() => {
    const ct = coreTypes.find((c) => c.code === template.coreType)
    if (ct && ct.cores.length > 0) {
      return ct.cores.map(String)
    }
    return ['1', '2', '4', '8', '16', '32', '64', '128']
  }, [coreTypes, template.coreType])

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
        analysisVersion: analysis?.versions[0]?.version || '',
      }))
    },
    [analysisCodes]
  )

  // Handle core type change
  const handleCoreTypeChange = useCallback(
    (coreType: string) => {
      const ct = coreTypes.find((c) => c.code === coreType)
      const defaultCores = ct?.cores[0] || 4

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

  // Validate template
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
      errs.push('Cores per slot must be positive')
    }
    if (template.walltimeHours <= 0) {
      errs.push('Walltime must be positive')
    }

    return errs
  }, [template])

  // Handle save
  const handleSave = useCallback(() => {
    const validationErrors = validate()
    if (validationErrors.length > 0) {
      setErrors(validationErrors)
      return
    }

    // Build license JSON if set
    let licenseSettings = ''
    if (licenseType && licenseValue) {
      licenseSettings = JSON.stringify({ [licenseType]: licenseValue })
    }

    const finalTemplate = {
      ...template,
      licenseSettings,
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

        {/* v4.0.0 G3: Saved Templates Section */}
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
          <section>
            <h3 className="text-sm font-semibold text-gray-700 dark:text-gray-300 mb-3 pb-1 border-b border-gray-200 dark:border-gray-700">
              Software Configuration
            </h3>
            <div className="grid grid-cols-2 gap-4">
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
              </div>
              <div>
                <label className="block text-sm font-medium mb-1">Version</label>
                <SearchableSelect
                  options={versionOptions}
                  value={template.analysisVersion}
                  onChange={(v) => updateField('analysisVersion', v)}
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
                    {isLoadingCoreTypes ? 'Scanning...' : 'Scan Hardware'}
                  </button>
                </div>
                <SearchableSelect
                  options={coreTypeOptions}
                  value={template.coreType}
                  onChange={handleCoreTypeChange}
                  placeholder={coreTypes.length === 0 ? 'Click "Scan Hardware" to load' : 'Search hardware...'}
                  disabled={isLoadingCoreTypes || coreTypes.length === 0}
                />
              </div>
              <div>
                <label className="block text-sm font-medium mb-1">Cores Per Slot</label>
                <select
                  value={template.coresPerSlot}
                  onChange={(e) => updateField('coresPerSlot', Number(e.target.value))}
                  className="w-full px-3 py-2 text-sm border border-gray-300 dark:border-gray-600 rounded bg-white dark:bg-gray-800 focus:outline-none focus:ring-2 focus:ring-blue-500"
                >
                  {coresPerSlotOptions.map((c) => (
                    <option key={c} value={c}>
                      {c}
                    </option>
                  ))}
                </select>
              </div>
              <div>
                <label className="block text-sm font-medium mb-1">Walltime (hours)</label>
                <input
                  type="number"
                  step="0.1"
                  min="0.1"
                  value={template.walltimeHours}
                  onChange={(e) => updateField('walltimeHours', Number(e.target.value))}
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
                <label className="block text-sm font-medium mb-1">Project ID</label>
                <input
                  type="text"
                  value={template.projectId}
                  onChange={(e) => updateField('projectId', e.target.value)}
                  placeholder="project-id (optional)"
                  className="w-full px-3 py-2 text-sm border border-gray-300 dark:border-gray-600 rounded bg-white dark:bg-gray-800 focus:outline-none focus:ring-2 focus:ring-blue-500"
                />
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
                  onChange={(e) => setLicenseType(e.target.value)}
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
                  onChange={(e) => setLicenseValue(e.target.value)}
                  placeholder={
                    LICENSE_TYPES.find((lt) => lt.key === licenseType)?.placeholder ||
                    'port@license-server'
                  }
                  disabled={!licenseType}
                  className="w-full px-3 py-2 text-sm border border-gray-300 dark:border-gray-600 rounded bg-white dark:bg-gray-800 focus:outline-none focus:ring-2 focus:ring-blue-500 disabled:bg-gray-100 dark:disabled:bg-gray-700"
                />
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
                  <span className="text-sm">Low Priority (use preemptible instances)</span>
                </label>
              </div>
            </div>
          </section>
        </div>

        {/* Footer */}
        <div className="flex items-center justify-between px-6 py-4 border-t border-gray-200 dark:border-gray-700">
          {/* v4.0.0 G3: Save as Template button */}
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
