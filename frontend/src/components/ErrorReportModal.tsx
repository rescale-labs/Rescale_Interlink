// v4.8.7: Error report modal for safe error reporting (Plan 3, 6A-6E).
// Appears when a reportable error event is received. Offers "Copy to Clipboard"
// and "Save Report" options with an optional user note.

import { useState } from 'react'
import { ExclamationTriangleIcon, XMarkIcon, ChevronDownIcon, ChevronUpIcon } from '@heroicons/react/24/outline'
import { ClipboardSetText } from '../../wailsjs/runtime/runtime'
import * as App from '../../wailsjs/go/wailsapp/App'
import { useErrorReportStore } from '../stores/errorReportStore'
import { useConfigStore } from '../stores/configStore'

// Friendly category labels
const CATEGORY_LABELS: Record<string, string> = {
  transfer: 'File Transfer Error',
  job_create: 'Job Creation Error',
  job_submit: 'Job Submission Error',
  pur_pipeline: 'Pipeline Error',
  auth: 'Authentication Error',
}

// Friendly class labels
const CLASS_LABELS: Record<string, string> = {
  network: 'Network connectivity issue',
  auth: 'Authentication / authorization problem',
  disk_space: 'Insufficient disk space',
  client_error: 'Client-side error (bad request)',
  server_error: 'Server-side error',
  internal: 'Internal error',
  timeout: 'Operation timed out',
}

export default function ErrorReportModal() {
  const { pendingError, isModalOpen, isSaving, lastResult, dismiss, setIsSaving, setLastResult } = useErrorReportStore()
  const [userNote, setUserNote] = useState('')
  const [showDetails, setShowDetails] = useState(false)

  if (!isModalOpen || !pendingError) return null

  const categoryLabel = CATEGORY_LABELS[pendingError.category] || pendingError.category
  const classLabel = CLASS_LABELS[pendingError.errorClass] || pendingError.errorClass

  const buildReportJSON = async (): Promise<string | null> => {
    try {
      const { config, workspaceName, workspaceId } = useConfigStore.getState()
      const request = {
        errorID: pendingError.errorID,
        category: pendingError.category,
        severity: pendingError.severity,
        operation: pendingError.operation,
        backend: pendingError.backend,
        errorMessage: pendingError.errorMessage,
        errorClass: pendingError.errorClass,
        timeline: pendingError.timeline || [],
        userNote: userNote.trim(),
        workspaceName: workspaceName || '',
        workspaceID: workspaceId || '',
        platformURL: config?.apiBaseUrl || '',
      }
      return await App.BuildErrorReport(JSON.stringify(request))
    } catch (err) {
      console.error('Failed to build error report:', err)
      setLastResult('Failed to build report')
      return null
    }
  }

  const handleCopy = async () => {
    setIsSaving(true)
    setLastResult(null)
    const reportJSON = await buildReportJSON()
    if (reportJSON) {
      try {
        await ClipboardSetText(reportJSON)
        setLastResult('Copied to clipboard!')
      } catch (err) {
        console.error('Failed to copy to clipboard:', err)
        setLastResult('Copy failed')
      }
    }
    setIsSaving(false)
  }

  const handleSave = async () => {
    setIsSaving(true)
    setLastResult(null)
    const reportJSON = await buildReportJSON()
    if (reportJSON) {
      try {
        const savedPath = await App.SaveErrorReport(reportJSON)
        if (savedPath) {
          setLastResult(`Saved to ${savedPath}`)
        } else {
          // User cancelled save dialog
          setLastResult(null)
        }
      } catch (err) {
        console.error('Failed to save report:', err)
        setLastResult('Save failed')
      }
    }
    setIsSaving(false)
  }

  const handleDismiss = () => {
    setUserNote('')
    setShowDetails(false)
    dismiss()
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40">
      <div className="bg-white rounded-lg shadow-xl max-w-lg w-full mx-4 overflow-hidden">
        {/* Header */}
        <div className="flex items-start gap-3 p-5 pb-3">
          <div className="flex-shrink-0 w-10 h-10 rounded-full bg-red-100 flex items-center justify-center">
            <ExclamationTriangleIcon className="w-6 h-6 text-red-600" />
          </div>
          <div className="flex-1 min-w-0">
            <h3 className="text-lg font-semibold text-gray-900">{categoryLabel}</h3>
            <p className="text-sm text-gray-600 mt-0.5">
              {pendingError.errorMessage}
            </p>
          </div>
          <button
            onClick={handleDismiss}
            className="flex-shrink-0 text-gray-400 hover:text-gray-600 transition-colors"
          >
            <XMarkIcon className="w-5 h-5" />
          </button>
        </div>

        {/* Context line */}
        <div className="px-5 pb-3">
          <p className="text-xs text-gray-500">
            Operation: <span className="font-medium text-gray-700">{pendingError.operation}</span>
            {pendingError.backend && (
              <> &middot; Backend: <span className="font-medium text-gray-700">{pendingError.backend}</span></>
            )}
          </p>
        </div>

        {/* Expandable technical details */}
        <div className="px-5 pb-3">
          <button
            onClick={() => setShowDetails(!showDetails)}
            className="text-xs text-gray-500 hover:text-gray-700 flex items-center gap-1 transition-colors"
          >
            {showDetails ? <ChevronUpIcon className="w-3 h-3" /> : <ChevronDownIcon className="w-3 h-3" />}
            Technical details
          </button>
          {showDetails && (
            <div className="mt-2 p-3 bg-gray-50 rounded text-xs text-gray-600 space-y-1">
              <p>Error class: <span className="font-medium">{classLabel}</span></p>
              <p>Severity: <span className="font-medium">{pendingError.severity}</span></p>
              <p>Error ID: <span className="font-mono text-gray-500">{pendingError.errorID}</span></p>
              {pendingError.timeline && pendingError.timeline.length > 0 && (
                <p>Timeline entries: <span className="font-medium">{pendingError.timeline.length}</span></p>
              )}
            </div>
          )}
        </div>

        {/* User note */}
        <div className="px-5 pb-4">
          <label className="block text-xs text-gray-500 mb-1">
            What were you trying to do? (optional)
          </label>
          <textarea
            value={userNote}
            onChange={(e) => setUserNote(e.target.value)}
            placeholder="e.g., I was uploading a folder with 500 files..."
            className="w-full p-2 border border-gray-300 rounded text-sm resize-none focus:outline-none focus:ring-1 focus:ring-blue-500 focus:border-blue-500"
            rows={2}
          />
        </div>

        {/* Call to action */}
        <div className="px-5 pb-3">
          <p className="text-sm text-gray-700">
            Please copy or save this error report and send it to your Rescale contact or{' '}
            <span className="font-medium">support@rescale.com</span> for faster diagnosis.
          </p>
        </div>

        {/* Result feedback */}
        {lastResult && (
          <div className="px-5 pb-3">
            <p className={`text-xs ${lastResult.startsWith('Failed') || lastResult.startsWith('Copy failed') || lastResult.startsWith('Save failed')
              ? 'text-red-600'
              : 'text-green-600'
            }`}>
              {lastResult}
            </p>
          </div>
        )}

        {/* Actions */}
        <div className="flex items-center justify-between px-5 py-4 bg-gray-50 border-t border-gray-200">
          <button
            onClick={handleDismiss}
            className="text-sm text-gray-500 hover:text-gray-700 transition-colors"
          >
            Dismiss
          </button>
          <div className="flex items-center gap-2">
            <button
              onClick={handleCopy}
              disabled={isSaving}
              className="px-3 py-1.5 text-sm font-medium text-gray-700 bg-white border border-gray-300 rounded hover:bg-gray-50 disabled:opacity-50 transition-colors"
            >
              Copy to Clipboard
            </button>
            <button
              onClick={handleSave}
              disabled={isSaving}
              className="px-3 py-1.5 text-sm font-medium text-white bg-rescale-blue rounded hover:bg-blue-700 disabled:opacity-50 transition-colors"
            >
              Save Report
            </button>
          </div>
        </div>

        {/* Privacy note */}
        <div className="px-5 py-2 bg-gray-50 border-t border-gray-100">
          <p className="text-[10px] text-gray-400 text-center">
            Reports contain only technical diagnostics. No API keys, passwords, or file contents are included.
          </p>
        </div>
      </div>
    </div>
  )
}
