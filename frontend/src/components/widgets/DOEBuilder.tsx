// DOE sweep builder: turns one base job into a parameter sweep.
//
// The sweep's values are rendered into the job's command line rather than passed
// as environment variables, so each case's configuration is visible on its
// Rescale job page. Generation happens in Go and is pure, which is what lets the
// preview below refresh as the design is edited.
import { useEffect, useMemo, useState } from 'react'
import { XCircleIcon, ExclamationTriangleIcon } from '@heroicons/react/24/outline'
import { useJobStore } from '../../stores'
import type { DOEParameter } from '../../stores/jobStore'

const INPUT_CLASS =
  'w-full px-2 py-1.5 text-sm border border-gray-300 dark:border-gray-600 rounded bg-white dark:bg-gray-800 focus:outline-none focus:ring-2 focus:ring-blue-500'

// How long to wait after an edit before refreshing the preview.
const PREVIEW_DEBOUNCE_MS = 250

// The per-parameter fields held as numbers, which therefore need draft text
// while they are being typed.
type NumericField = 'min' | 'max' | 'levels'

// Fewest grid points a swept parameter can have: a range needs both ends.
const MIN_LEVELS = 2

function newParameter(): DOEParameter {
  return { name: '', min: 0, max: 1, levels: 3, values: [], format: '' }
}

export function DOEBuilder() {
  const {
    template,
    doeOptions,
    doeMethods,
    doePreview,
    doeError,
    setDOEOptions,
    fetchDOEMethods,
    previewDOE,
  } = useJobStore()

  useEffect(() => {
    if (doeMethods.length === 0) {
      void fetchDOEMethods()
    }
  }, [doeMethods.length, fetchDOEMethods])

  // Refresh the preview after edits settle, so typing a parameter name does not
  // fire a generation per keystroke. A sweep with no named parameter can only
  // fail validation, so hold off until there is something worth generating
  // rather than greeting the user with an error panel.
  const hasNamedParameter = doeOptions.parameters.some((p) => p.name.trim() !== '')

  useEffect(() => {
    if (!hasNamedParameter) return
    const timer = setTimeout(() => {
      void previewDOE()
    }, PREVIEW_DEBOUNCE_MS)
    return () => clearTimeout(timer)
  }, [hasNamedParameter, doeOptions, template.command, template.jobName, previewDOE])

  const method = useMemo(
    () => doeMethods.find((m) => m.method === doeOptions.method),
    [doeMethods, doeOptions.method]
  )

  const updateParameter = (index: number, updates: Partial<DOEParameter>) => {
    const parameters = [...doeOptions.parameters]
    parameters[index] = { ...parameters[index], ...updates }
    setDOEOptions({ parameters })
  }

  // In-progress text for the numeric fields, keyed by row and field.
  //
  // A number field has to display what the user is still typing, and "-", "" and
  // "1e" are all unparseable on the way to a valid number. Storing only the
  // parsed number and echoing it back would erase the keystroke — which is what
  // made negative values impossible to type by hand. The store still only ever
  // holds numbers; this draft is what the input shows until it loses focus.
  const [numberDrafts, setNumberDrafts] = useState<Record<string, string>>({})

  const draftKey = (index: number, field: NumericField) => `${index}:${field}`

  const numberFieldValue = (index: number, field: NumericField, committed: number) => {
    const draft = numberDrafts[draftKey(index, field)]
    return draft !== undefined ? draft : String(committed)
  }

  // A bound is any real number, negative included. Unparseable input commits 0
  // rather than NaN so the design stays generatable while the field is mid-edit.
  const handleBoundChange = (index: number, field: 'min' | 'max', raw: string) => {
    setNumberDrafts((prev) => ({ ...prev, [draftKey(index, field)]: raw }))
    const parsed = parseFloat(raw)
    updateParameter(index, {
      [field]: Number.isFinite(parsed) ? parsed : 0,
    } as Partial<DOEParameter>)
  }

  // Levels counts the grid points along a parameter, so unlike a bound it is an
  // integer of at least MIN_LEVELS. Non-digits are dropped from the draft rather
  // than parsed away, so a sign or decimal point cannot be typed at all, and the
  // committed value is never below the minimum.
  const handleLevelsChange = (index: number, raw: string) => {
    const digits = raw.replace(/\D/g, '')
    setNumberDrafts((prev) => ({ ...prev, [draftKey(index, 'levels')]: digits }))
    const parsed = parseInt(digits, 10)
    updateParameter(index, {
      levels: Number.isFinite(parsed) && parsed >= MIN_LEVELS ? parsed : MIN_LEVELS,
    })
  }

  // Dropping the draft on blur normalizes the display to the stored number, so a
  // field left as "-" settles to 0 instead of lingering as invalid-looking text.
  const handleNumberBlur = (index: number, field: NumericField) => {
    setNumberDrafts((prev) => {
      const next = { ...prev }
      delete next[draftKey(index, field)]
      return next
    })
  }

  // Drafts are keyed by row index, so adding or removing a parameter would leave
  // them pointing at the wrong row. Clearing is correct: every field falls back
  // to its stored value.
  const addParameter = () => {
    setNumberDrafts({})
    setDOEOptions({ parameters: [...doeOptions.parameters, newParameter()] })
  }

  const removeParameter = (index: number) => {
    setNumberDrafts({})
    setDOEOptions({ parameters: doeOptions.parameters.filter((_, i) => i !== index) })
  }

  return (
    <div className="space-y-6">
      {/* Method */}
      <div>
        <label className="block text-sm font-medium mb-1">Sampling Method</label>
        <select
          value={doeOptions.method}
          onChange={(e) => setDOEOptions({ method: e.target.value })}
          className={INPUT_CLASS}
        >
          {doeMethods.map((m) => (
            <option key={m.method} value={m.method}>
              {m.label}
            </option>
          ))}
        </select>
        {method && <p className="mt-1 text-xs text-gray-500">{method.description}</p>}
      </div>

      {/* Parameters */}
      <div>
        <div className="flex items-center justify-between mb-2">
          <label className="block text-sm font-medium">Swept Parameters</label>
          <button
            onClick={addParameter}
            className="text-sm text-blue-500 hover:text-blue-600"
          >
            + Add Parameter
          </button>
        </div>

        {doeOptions.parameters.length === 0 ? (
          <p className="text-xs text-gray-500">
            Add a parameter for each <code>{'{{name}}'}</code> token in the command.
          </p>
        ) : (
          <div className="space-y-2">
            {doeOptions.parameters.map((param, index) => {
              const isCategorical = param.values.length > 0
              return (
                <div
                  key={index}
                  className="grid grid-cols-12 gap-2 items-start p-2 border border-gray-200 dark:border-gray-700 rounded"
                >
                  <div className="col-span-3">
                    <input
                      type="text"
                      value={param.name}
                      onChange={(e) => updateParameter(index, { name: e.target.value })}
                      placeholder="alpha"
                      className={INPUT_CLASS}
                    />
                    <p className="mt-1 text-xs text-gray-400 truncate">
                      {param.name ? `{{${param.name}}}` : 'token name'}
                    </p>
                  </div>

                  <div className="col-span-2">
                    <select
                      value={isCategorical ? 'list' : 'range'}
                      onChange={(e) =>
                        updateParameter(index, {
                          // Switching type is expressed by whether values is set:
                          // a non-empty list makes the parameter categorical.
                          values: e.target.value === 'list' ? [''] : [],
                        })
                      }
                      className={INPUT_CLASS}
                    >
                      <option value="range">Range</option>
                      <option value="list">Values</option>
                    </select>
                  </div>

                  {isCategorical ? (
                    <div className="col-span-6">
                      <input
                        type="text"
                        value={param.values.join(', ')}
                        onChange={(e) =>
                          updateParameter(index, {
                            values: e.target.value.split(',').map((v) => v.trim()),
                          })
                        }
                        placeholder="kepsilon, komega, les"
                        className={INPUT_CLASS}
                      />
                      <p className="mt-1 text-xs text-gray-400">
                        Comma-separated; used verbatim
                      </p>
                    </div>
                  ) : (
                    <>
                      <div className="col-span-2">
                        <input
                          type="number"
                          value={numberFieldValue(index, 'min', param.min)}
                          onChange={(e) => handleBoundChange(index, 'min', e.target.value)}
                          onBlur={() => handleNumberBlur(index, 'min')}
                          className={INPUT_CLASS}
                        />
                        <p className="mt-1 text-xs text-gray-400">min</p>
                      </div>
                      <div className="col-span-2">
                        <input
                          type="number"
                          value={numberFieldValue(index, 'max', param.max)}
                          onChange={(e) => handleBoundChange(index, 'max', e.target.value)}
                          onBlur={() => handleNumberBlur(index, 'max')}
                          className={INPUT_CLASS}
                        />
                        <p className="mt-1 text-xs text-gray-400">max</p>
                      </div>
                      <div className="col-span-2">
                        {method?.usesLevels ? (
                          <>
                            <input
                              type="number"
                              min={MIN_LEVELS}
                              step={1}
                              value={numberFieldValue(index, 'levels', param.levels)}
                              onChange={(e) => handleLevelsChange(index, e.target.value)}
                              onBlur={() => handleNumberBlur(index, 'levels')}
                              className={INPUT_CLASS}
                            />
                            <p className="mt-1 text-xs text-gray-400">levels</p>
                          </>
                        ) : (
                          <>
                            <input
                              type="text"
                              value={param.format}
                              onChange={(e) => updateParameter(index, { format: e.target.value })}
                              placeholder="%g"
                              className={INPUT_CLASS}
                            />
                            <p className="mt-1 text-xs text-gray-400">format</p>
                          </>
                        )}
                      </div>
                    </>
                  )}

                  <div className="col-span-1 flex justify-end">
                    <button
                      onClick={() => removeParameter(index)}
                      className="p-1 text-red-500 hover:bg-red-50 dark:hover:bg-red-900/20 rounded"
                      title="Remove parameter"
                    >
                      <XCircleIcon className="w-5 h-5" />
                    </button>
                  </div>
                </div>
              )
            })}
          </div>
        )}
      </div>

      {/* Method-specific settings */}
      {(method?.usesSamples || method?.usesCenterPoints) && (
        <div className="grid grid-cols-3 gap-4">
          {method.usesSamples && (
            <>
              <div>
                <label className="block text-sm font-medium mb-1">Samples</label>
                <input
                  type="number"
                  min={1}
                  value={doeOptions.samples}
                  onChange={(e) => setDOEOptions({ samples: parseInt(e.target.value) || 1 })}
                  className={INPUT_CLASS}
                />
                <p className="mt-1 text-xs text-gray-500">One job per sample</p>
              </div>
              <div>
                <label className="block text-sm font-medium mb-1">Seed</label>
                <input
                  type="number"
                  min={0}
                  value={doeOptions.seed}
                  // Seed is a uint64 in Go, so a typed "-5" would fail to
                  // unmarshal and reject the whole call with a raw parse error.
                  onChange={(e) => setDOEOptions({ seed: Math.max(0, parseInt(e.target.value) || 0) })}
                  className={INPUT_CLASS}
                />
                <p className="mt-1 text-xs text-gray-500">
                  The same seed always yields the same sweep
                </p>
              </div>
            </>
          )}
          {method.usesCenterPoints && (
            <div>
              <label className="block text-sm font-medium mb-1">Center Points</label>
              <input
                type="number"
                min={1}
                value={doeOptions.centerPoints}
                onChange={(e) => setDOEOptions({ centerPoints: parseInt(e.target.value) || 1 })}
                className={INPUT_CLASS}
              />
              <p className="mt-1 text-xs text-gray-500">
                Repeats of the center case, for an error estimate
              </p>
            </div>
          )}
        </div>
      )}

      {method?.usesCases && (
        <div>
          <label className="block text-sm font-medium mb-1">Cases</label>
          <textarea
            rows={5}
            value={doeOptions.casesCSV}
            onChange={(e) => setDOEOptions({ casesCSV: e.target.value })}
            placeholder={'alpha,beta\n10,15\n20,25'}
            className={`${INPUT_CLASS} font-mono`}
          />
          <p className="mt-1 text-xs text-gray-500">
            One column per parameter. The header must name the parameters above.
          </p>
        </div>
      )}

      {/* Naming and tagging */}
      <div className="grid grid-cols-2 gap-4">
        <div>
          <label className="block text-sm font-medium mb-1">Job Name Template</label>
          <input
            type="text"
            value={doeOptions.jobNameTemplate}
            onChange={(e) => setDOEOptions({ jobNameTemplate: e.target.value })}
            placeholder="{{__base}}_{{__index}}"
            className={INPUT_CLASS}
          />
          <p className="mt-1 text-xs text-gray-500">
            May use parameter tokens plus <code>{'{{__base}}'}</code> and{' '}
            <code>{'{{__index}}'}</code>
          </p>
        </div>
        <div>
          <label className="block text-sm font-medium mb-1">Job Tags per Case</label>
          <input
            type="text"
            value={doeOptions.tagTemplates.join(', ')}
            onChange={(e) =>
              setDOEOptions({
                tagTemplates: e.target.value
                  .split(',')
                  .map((t) => t.trim())
                  .filter(Boolean),
              })
            }
            placeholder="alpha={{alpha}}, sweep-{{__index}}"
            className={INPUT_CLASS}
          />
          <p className="mt-1 text-xs text-gray-500">
            Rendered per case and applied to that case&apos;s Rescale job
          </p>
        </div>
      </div>

      {/* Shared inputs */}
      <div>
        <label className="block text-sm font-medium mb-1">
          Shared Input File IDs (optional)
        </label>
        <input
          type="text"
          value={doeOptions.baseFileIds}
          onChange={(e) => setDOEOptions({ baseFileIds: e.target.value })}
          placeholder="Comma-separated Rescale file IDs"
          className={INPUT_CLASS}
        />
        <p className="mt-1 text-xs text-gray-500">
          Every case in a sweep uses the same input deck. Give the IDs of an
          already-uploaded deck and each case references those files directly, so the
          deck transfers once for the whole sweep instead of once per case. Leave empty
          and the cases carry no input files of their own — supply the deck once as a
          Common Input File below.
        </p>
      </div>

      <div>
        <label className="block text-sm font-medium mb-1">Max Cases</label>
        <input
          type="number"
          min={0}
          value={doeOptions.maxCases}
          onChange={(e) => setDOEOptions({ maxCases: parseInt(e.target.value) || 0 })}
          className="w-40 px-2 py-1.5 text-sm border border-gray-300 dark:border-gray-600 rounded bg-white dark:bg-gray-800 focus:outline-none focus:ring-2 focus:ring-blue-500"
        />
        <p className="mt-1 text-xs text-gray-500">
          0 uses the default cap. A full factorial grows as the product of its level
          counts, so the cap is what stops a handful of parameters asking for thousands
          of jobs.
        </p>
      </div>

      {/* Problems */}
      {doeError && (
        <div className="p-3 bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800 rounded text-sm text-red-700 dark:text-red-400">
          {doeError}
        </div>
      )}

      {doePreview && doePreview.errors.length > 0 && (
        <div className="p-3 bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800 rounded">
          <div className="flex items-start gap-2 text-red-700 dark:text-red-400">
            <ExclamationTriangleIcon className="w-5 h-5 flex-shrink-0 mt-0.5" />
            <div className="space-y-1">
              <div className="font-medium">This sweep cannot be generated:</div>
              {doePreview.errors.map((problem, i) => (
                <div key={i} className="text-sm">
                  {problem.message}
                </div>
              ))}
            </div>
          </div>
        </div>
      )}

      {doePreview && doePreview.warnings.length > 0 && (
        <div className="p-3 bg-yellow-50 dark:bg-yellow-900/20 border border-yellow-200 dark:border-yellow-800 rounded text-sm text-yellow-800 dark:text-yellow-400 space-y-1">
          {doePreview.warnings.map((problem, i) => (
            <div key={i}>{problem.message}</div>
          ))}
        </div>
      )}

      {/* Preview */}
      {doePreview && doePreview.ok && doePreview.cases.length > 0 && (
        <div>
          <h4 className="text-sm font-medium mb-2">
            {doePreview.caseCount} case{doePreview.caseCount !== 1 ? 's' : ''}
            {doePreview.truncated && ` (showing the first ${doePreview.cases.length})`}
          </h4>

          <div className="overflow-x-auto border border-gray-200 dark:border-gray-700 rounded">
            <table className="min-w-full text-sm">
              <thead className="bg-gray-50 dark:bg-gray-800 text-xs text-gray-500">
                <tr>
                  <th className="px-3 py-2 text-left">#</th>
                  <th className="px-3 py-2 text-left">Job Name</th>
                  {doeOptions.parameters.map((p, i) => (
                    <th key={i} className="px-3 py-2 text-left">
                      {p.name || `param ${i + 1}`}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {doePreview.cases.map((c) => (
                  <tr key={c.index} className="border-t border-gray-100 dark:border-gray-700">
                    <td className="px-3 py-1.5 text-gray-500">{c.index}</td>
                    <td className="px-3 py-1.5 font-mono text-xs">{c.jobName}</td>
                    {doeOptions.parameters.map((p, i) => (
                      <td key={i} className="px-3 py-1.5 font-mono text-xs">
                        {c.values[p.name] ?? ''}
                      </td>
                    ))}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>

          <div className="mt-3 p-3 bg-gray-50 dark:bg-gray-800 rounded">
            <div className="text-xs font-medium text-gray-500 mb-1">
              Case {doePreview.cases[0].index} command
            </div>
            <code className="block text-xs break-all">{doePreview.cases[0].command}</code>
            {doePreview.cases[0].tags.length > 0 && (
              <div className="mt-2 flex flex-wrap gap-1">
                {doePreview.cases[0].tags.map((tag, i) => (
                  <span
                    key={i}
                    className="px-2 py-0.5 text-xs bg-blue-100 dark:bg-blue-900/40 text-blue-700 dark:text-blue-300 rounded"
                  >
                    {tag}
                  </span>
                ))}
              </div>
            )}
          </div>
        </div>
      )}
    </div>
  )
}
