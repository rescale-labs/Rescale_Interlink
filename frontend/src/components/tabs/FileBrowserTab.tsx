import { useCallback, useState, useMemo } from 'react'
import {
  ArrowUpTrayIcon,
  ArrowDownTrayIcon,
  TrashIcon,
  ExclamationTriangleIcon,
} from '@heroicons/react/24/outline'
import { LocalBrowser, RemoteBrowser } from '../widgets'
import { useFileBrowserStore } from '../../stores'
import * as App from '../../../wailsjs/go/wailsapp/App'
import { wailsapp } from '../../../wailsjs/go/models'
import { useTabNavigation } from '../../App'

interface ConfirmDialogProps {
  isOpen: boolean
  title: string
  message: string
  confirmText: string
  isDanger?: boolean
  warning?: string
  onConfirm: () => void
  onCancel: () => void
}

function ConfirmDialog({
  isOpen,
  title,
  message,
  confirmText,
  isDanger = false,
  warning,
  onConfirm,
  onCancel,
}: ConfirmDialogProps) {
  if (!isOpen) return null

  return (
    <div className="fixed inset-0 bg-black/50 flex items-center justify-center z-50">
      <div className="bg-white dark:bg-gray-800 rounded-lg shadow-lg p-4 w-96 max-w-[90vw]">
        <h3 className="text-lg font-medium mb-2">{title}</h3>
        <p className="text-sm text-gray-600 dark:text-gray-400 mb-4 whitespace-pre-line">
          {message}
        </p>
        {warning && (
          <div className="flex items-start gap-2 p-2 mb-4 bg-yellow-50 dark:bg-yellow-900/20 border border-yellow-200 dark:border-yellow-800 rounded text-sm text-yellow-700 dark:text-yellow-400">
            <ExclamationTriangleIcon className="w-5 h-5 flex-shrink-0 mt-0.5" />
            <span>{warning}</span>
          </div>
        )}
        <div className="flex justify-end gap-2">
          <button
            onClick={onCancel}
            className="px-4 py-2 text-sm text-gray-600 dark:text-gray-400 hover:bg-gray-100 dark:hover:bg-gray-700 rounded"
          >
            Cancel
          </button>
          <button
            onClick={onConfirm}
            className={`px-4 py-2 text-sm text-white rounded ${
              isDanger
                ? 'bg-red-500 hover:bg-red-600'
                : 'bg-blue-500 hover:bg-blue-600'
            }`}
          >
            {confirmText}
          </button>
        </div>
      </div>
    </div>
  )
}

// v4.0.8: Error dialog for critical errors that need user attention
interface ErrorDialogProps {
  isOpen: boolean
  title: string
  message: string
  onClose: () => void
}

function ErrorDialog({ isOpen, title, message, onClose }: ErrorDialogProps) {
  if (!isOpen) return null

  return (
    <div className="fixed inset-0 bg-black/50 flex items-center justify-center z-50">
      <div className="bg-white dark:bg-gray-800 rounded-lg shadow-lg p-4 w-[450px] max-w-[90vw]">
        <div className="flex items-start gap-3 mb-4">
          <div className="p-2 bg-red-100 dark:bg-red-900/30 rounded-full">
            <ExclamationTriangleIcon className="w-6 h-6 text-red-600 dark:text-red-400" />
          </div>
          <div>
            <h3 className="text-lg font-medium text-red-600 dark:text-red-400">{title}</h3>
            <p className="text-sm text-gray-600 dark:text-gray-400 mt-2 whitespace-pre-line">
              {message}
            </p>
          </div>
        </div>
        <div className="flex justify-end">
          <button
            onClick={onClose}
            className="px-4 py-2 text-sm text-white bg-red-500 hover:bg-red-600 rounded"
          >
            OK
          </button>
        </div>
      </div>
    </div>
  )
}

export function FileBrowserTab() {
  const {
    local,
    remote,
    getLocalSelectedItems,
    getRemoteSelectedItems,
    clearLocalSelection,
    clearRemoteSelection,
    refreshLocal,
    refreshRemote,
    deleteRemoteItems,
  } = useFileBrowserStore()

  // Tab navigation for switching to Transfers after starting transfers
  const { switchToTab } = useTabNavigation()

  // Resizable pane state
  const [leftPaneWidth, setLeftPaneWidth] = useState(50) // Percentage
  const [isResizing, setIsResizing] = useState(false)

  // Transfer state
  const [isUploading, setIsUploading] = useState(false)
  const [isDownloading, setIsDownloading] = useState(false)
  const [isDeleting, setIsDeleting] = useState(false)

  // Confirmation dialogs with optional warning
  const [uploadConfirm, setUploadConfirm] = useState<{
    items: wailsapp.FileItemDTO[]
    destPath: string
    folderCount: number
  } | null>(null)
  const [downloadConfirm, setDownloadConfirm] = useState<{
    items: wailsapp.FileItemDTO[]
    destPath: string
    folderCount: number
  } | null>(null)
  const [deleteConfirm, setDeleteConfirm] = useState<wailsapp.FileItemDTO[] | null>(null)

  // v4.0.8: Error dialog for critical errors
  const [errorDialog, setErrorDialog] = useState<{ title: string; message: string } | null>(null)

  // v4.0.8: Merge confirmation dialog for existing folders
  const [mergeConfirm, setMergeConfirm] = useState<{
    existingFolders: string[]
    uploadData: {
      files: wailsapp.FileItemDTO[]
      folders: wailsapp.FileItemDTO[]
      destFolderId: string
    }
  } | null>(null)

  // Status message
  const [status, setStatus] = useState('Select files, then use Upload/Download')

  // Get selection counts
  const localSelectedCount = local.selection.selectedIds.size
  const remoteSelectedCount = remote.selection.selectedIds.size

  // Upload availability and reason
  // Jobs mode: Uploads disabled (job outputs are read-only)
  // Library mode: Uploads allowed
  // Legacy mode: Uploads allowed (files upload to user's library and appear in Legacy view)
  const uploadState = useMemo(() => {
    if (remote.mode === 'jobs') {
      return { allowed: false, reason: 'N/A in Jobs view', hasSelection: localSelectedCount > 0 }
    }
    if (localSelectedCount === 0) {
      return { allowed: false, reason: 'Select files', hasSelection: false }
    }
    return { allowed: true, reason: '', hasSelection: true }
  }, [remote.mode, localSelectedCount])

  // Handle resize
  const handleMouseDown = useCallback((e: React.MouseEvent) => {
    e.preventDefault()
    setIsResizing(true)

    const handleMouseMove = (e: MouseEvent) => {
      const container = document.getElementById('file-browser-container')
      if (!container) return
      const rect = container.getBoundingClientRect()
      const newWidth = ((e.clientX - rect.left) / rect.width) * 100
      setLeftPaneWidth(Math.max(20, Math.min(80, newWidth)))
    }

    const handleMouseUp = () => {
      setIsResizing(false)
      document.removeEventListener('mousemove', handleMouseMove)
      document.removeEventListener('mouseup', handleMouseUp)
    }

    document.addEventListener('mousemove', handleMouseMove)
    document.addEventListener('mouseup', handleMouseUp)
  }, [])

  // Handle upload button click
  const handleUpload = useCallback(() => {
    if (!uploadState.allowed) return

    const selectedItems = getLocalSelectedItems()
    if (selectedItems.length === 0) return

    // Count folders for confirmation dialog
    const folderCount = selectedItems.filter(item => item.isFolder).length

    // Get destination path from breadcrumb
    const destPath = remote.breadcrumb.map(b => b.name).join(' > ') || 'My Library'

    // v4.0.0: Support both files and folders
    setUploadConfirm({
      items: selectedItems, // Include all items (files AND folders)
      destPath,
      folderCount
    })
  }, [uploadState.allowed, getLocalSelectedItems, remote.breadcrumb])

  // v4.0.8: Helper function that performs the actual upload (called after merge confirmation if needed)
  const proceedWithUpload = useCallback(async (
    files: wailsapp.FileItemDTO[],
    folders: wailsapp.FileItemDTO[],
    destFolderId: string
  ) => {
    setIsUploading(true)
    const totalItems = files.length + folders.length
    setStatus(`Uploading ${totalItems} item(s)...`)

    try {
      // Switch to Transfers tab early for folder uploads
      if (folders.length > 0) {
        switchToTab('Transfers')
      }

      // Upload folders first using StartFolderUpload
      for (const folder of folders) {
        setStatus(`Uploading folder: ${folder.name}...`)
        console.log(`[FileBrowserTab] Starting folder upload: ${folder.name} (id: ${folder.id}) to ${destFolderId}`)
        const result = await App.StartFolderUpload(folder.id, destFolderId)
        console.log(`[FileBrowserTab] Folder upload result:`, result)
        if (result.error) {
          console.error(`Folder upload error for ${folder.name}:`, result.error)
          setErrorDialog({
            title: `Upload Failed: ${folder.name}`,
            message: result.error
          })
          setStatus(`Error uploading ${folder.name}`)
          return
        } else if (result.mergedInto) {
          console.log(`[FileBrowserTab] Merged into existing folder: ${result.mergedInto}`)
          setStatus(`Merged ${result.filesQueued} files into existing folder "${result.mergedInto}"`)
        } else {
          console.log(`[FileBrowserTab] Folder upload success: ${result.foldersCreated} folders, ${result.filesQueued} files queued`)
        }
      }

      // Upload individual files using transfer queue
      if (files.length > 0) {
        const requests = files.map(item => ({
          type: 'upload',
          source: item.id,
          dest: destFolderId,
          name: item.name,
          size: item.size ?? 0,
        }))
        await App.StartTransfers(requests)
      }

      clearLocalSelection()

      const statusParts = []
      if (folders.length > 0) statusParts.push(`${folders.length} folder(s)`)
      if (files.length > 0) statusParts.push(`${files.length} file(s)`)
      setStatus(`Upload started: ${statusParts.join(' and ')}.`)

      if (files.length > 0 || folders.length > 0) {
        switchToTab('Transfers')
      }

      setTimeout(() => refreshRemote(), 2000)
    } catch (error) {
      console.error('Upload failed:', error)
      setStatus(`Upload failed: ${error instanceof Error ? error.message : String(error)}`)
    } finally {
      setIsUploading(false)
    }
  }, [clearLocalSelection, switchToTab, refreshRemote])

  // Confirm upload - v4.0.8: Now checks for existing folders first
  const confirmUpload = useCallback(async () => {
    if (!uploadConfirm) return

    setUploadConfirm(null)

    // Separate files and folders
    const files = uploadConfirm.items.filter(item => !item.isFolder)
    const folders = uploadConfirm.items.filter(item => item.isFolder)

    // For Legacy mode, uploads go to My Library root folder
    const destFolderId = remote.mode === 'legacy'
      ? (remote.myLibraryId || remote.currentFolderId)
      : remote.currentFolderId

    // v4.0.8: Check if any folders already exist before uploading
    if (folders.length > 0) {
      setStatus('Checking for existing folders...')
      const existingFolders: string[] = []

      for (const folder of folders) {
        try {
          const check = await App.CheckFolderExistsForUpload(folder.name, destFolderId)
          if (check.error) {
            console.warn(`Error checking folder ${folder.name}:`, check.error)
          } else if (check.exists) {
            existingFolders.push(folder.name)
          }
        } catch (err) {
          console.warn(`Error checking folder ${folder.name}:`, err)
        }
      }

      // If any folders exist, show merge confirmation dialog
      if (existingFolders.length > 0) {
        setMergeConfirm({
          existingFolders,
          uploadData: { files, folders, destFolderId }
        })
        setStatus('Waiting for merge confirmation...')
        return
      }
    }

    // No existing folders - proceed directly
    await proceedWithUpload(files, folders, destFolderId)
  }, [uploadConfirm, remote.mode, remote.myLibraryId, remote.currentFolderId, proceedWithUpload])

  // v4.0.8: Confirm merge and proceed with upload
  const confirmMerge = useCallback(async () => {
    if (!mergeConfirm) return
    const { files, folders, destFolderId } = mergeConfirm.uploadData
    setMergeConfirm(null)
    await proceedWithUpload(files, folders, destFolderId)
  }, [mergeConfirm, proceedWithUpload])

  // v4.0.8: Cancel merge (cancel upload)
  const cancelMerge = useCallback(() => {
    setMergeConfirm(null)
    setStatus('Upload cancelled.')
  }, [])

  // Handle download button click
  const handleDownload = useCallback(() => {
    if (remoteSelectedCount === 0) return

    const selectedItems = getRemoteSelectedItems()
    if (selectedItems.length === 0) return

    // v4.0.0: Count folders for confirmation dialog info
    const folderCount = selectedItems.filter(item => item.isFolder).length

    const destPath = local.currentPath || 'Home'

    // v4.0.0: Support both files and folders
    setDownloadConfirm({
      items: selectedItems, // Include all selected items (files AND folders)
      destPath,
      folderCount
    })
  }, [remoteSelectedCount, getRemoteSelectedItems, local.currentPath])

  // Confirm download
  const confirmDownload = useCallback(async () => {
    if (!downloadConfirm) return

    setDownloadConfirm(null)
    setIsDownloading(true)

    // v4.0.0: Separate files and folders for different download paths
    const files = downloadConfirm.items.filter(item => !item.isFolder)
    const folders = downloadConfirm.items.filter(item => item.isFolder)

    const totalItems = files.length + folders.length
    setStatus(`Downloading ${totalItems} item(s)...`)

    try {
      // v4.0.5: Switch to Transfers tab early so users can see Activity log (issue #19)
      // This shows the scanning progress as log events in the Activity tab
      if (folders.length > 0) {
        switchToTab('Transfers')
      }

      // Download folders first using recursive folder download
      for (const folder of folders) {
        // v4.0.5: Show "Scanning" status since that's what happens first (issue #19)
        setStatus(`Scanning folder: ${folder.name}...`)
        const result = await App.StartFolderDownload(folder.id, folder.name, local.currentPath)
        if (result.error) {
          console.error(`Folder download error for ${folder.name}:`, result.error)
          // Continue with other items
        } else {
          setStatus(`Folder '${folder.name}': ${result.filesDownloaded} files queued for download`)
        }
      }

      // Download individual files using transfer queue
      if (files.length > 0) {
        const requests = files.map(item => ({
          type: 'download',
          source: item.id, // Remote file ID
          dest: local.currentPath.endsWith('/')
            ? local.currentPath + item.name
            : local.currentPath + '/' + item.name,
          name: item.name,
          size: item.size ?? 0,
        }))

        await App.StartTransfers(requests)
      }

      clearRemoteSelection()

      // Build status message
      const statusParts = []
      if (folders.length > 0) {
        statusParts.push(`${folders.length} folder(s)`)
      }
      if (files.length > 0) {
        statusParts.push(`${files.length} file(s)`)
      }
      setStatus(`Download started: ${statusParts.join(' and ')}.`)

      // Switch to Transfers tab to show progress
      if (files.length > 0 || folders.length > 0) {
        switchToTab('Transfers')
      }

      // Refresh local after a delay to show downloaded files
      setTimeout(() => {
        refreshLocal()
      }, 2000)
    } catch (error) {
      console.error('Download failed:', error)
      setStatus(`Download failed: ${error instanceof Error ? error.message : String(error)}`)
    } finally {
      setIsDownloading(false)
    }
  }, [downloadConfirm, local.currentPath, clearRemoteSelection, refreshLocal, switchToTab])

  // Handle delete button click
  const handleDelete = useCallback(() => {
    if (remoteSelectedCount === 0) return

    const selectedItems = getRemoteSelectedItems()
    if (selectedItems.length === 0) return

    setDeleteConfirm(selectedItems)
  }, [remoteSelectedCount, getRemoteSelectedItems])

  // Confirm delete
  const confirmDelete = useCallback(async () => {
    if (!deleteConfirm) return

    setDeleteConfirm(null)
    setIsDeleting(true)
    setStatus(`Deleting ${deleteConfirm.length} item(s)...`)

    try {
      const result = await deleteRemoteItems(deleteConfirm)

      if (result.failed > 0) {
        setStatus(`Deleted ${result.deleted} item(s), ${result.failed} failed`)
      } else {
        setStatus(`Deleted ${result.deleted} item(s)`)
      }
    } catch (error) {
      console.error('Delete failed:', error)
      setStatus(`Delete failed: ${error instanceof Error ? error.message : String(error)}`)
    } finally {
      setIsDeleting(false)
    }
  }, [deleteConfirm, deleteRemoteItems])

  // Upload button text
  const uploadButtonText = useMemo(() => {
    if (!uploadState.allowed && uploadState.reason) {
      return uploadState.reason
    }
    if (localSelectedCount > 0) {
      return `Upload ${localSelectedCount}`
    }
    return 'Upload'
  }, [uploadState, localSelectedCount])

  return (
    <div className="flex flex-col h-full">
      {/* Two-pane layout */}
      <div
        id="file-browser-container"
        className="flex-1 flex overflow-hidden"
        style={{ cursor: isResizing ? 'col-resize' : undefined }}
      >
        {/* Left pane - Local browser */}
        <div
          className="flex flex-col overflow-hidden border-r border-gray-200 dark:border-gray-700"
          style={{ width: `${leftPaneWidth}%` }}
        >
          {/* Header */}
          <div className="flex items-center justify-between px-3 py-2 bg-gray-100 dark:bg-gray-800 border-b border-gray-200 dark:border-gray-700">
            <span className="font-medium text-sm">Local Files</span>
            <button
              onClick={handleUpload}
              disabled={!uploadState.allowed || isUploading}
              title={uploadState.reason || 'Upload selected files to Rescale'}
              className={`flex items-center gap-1 px-3 py-1 text-sm rounded ${
                uploadState.allowed && !isUploading
                  ? 'bg-blue-500 text-white hover:bg-blue-600'
                  : 'bg-gray-300 dark:bg-gray-600 text-gray-500 dark:text-gray-400 cursor-not-allowed'
              }`}
            >
              <ArrowUpTrayIcon className="w-4 h-4" />
              {uploadButtonText}
              {uploadState.allowed && <span className="text-xs">&rarr;</span>}
            </button>
          </div>

          {/* Local browser */}
          <div className="flex-1 overflow-hidden">
            <LocalBrowser />
          </div>
        </div>

        {/* Resize handle */}
        <div
          className="w-1 bg-gray-200 dark:bg-gray-700 hover:bg-blue-400 cursor-col-resize flex-shrink-0"
          onMouseDown={handleMouseDown}
        />

        {/* Right pane - Remote browser */}
        <div
          className="flex flex-col overflow-hidden"
          style={{ width: `${100 - leftPaneWidth}%` }}
        >
          {/* Header */}
          <div className="flex items-center justify-between px-3 py-2 bg-gray-100 dark:bg-gray-800 border-b border-gray-200 dark:border-gray-700">
            <span className="font-medium text-sm">Rescale Files</span>
            <div className="flex items-center gap-2">
              <button
                onClick={handleDownload}
                disabled={remoteSelectedCount === 0 || isDownloading}
                title="Download selected files to local"
                className={`flex items-center gap-1 px-3 py-1 text-sm rounded ${
                  remoteSelectedCount > 0 && !isDownloading
                    ? 'bg-blue-500 text-white hover:bg-blue-600'
                    : 'bg-gray-300 dark:bg-gray-600 text-gray-500 dark:text-gray-400 cursor-not-allowed'
                }`}
              >
                <span className="text-xs">&larr;</span>
                <ArrowDownTrayIcon className="w-4 h-4" />
                {remoteSelectedCount > 0 ? `Download ${remoteSelectedCount}` : 'Download'}
              </button>
              <button
                onClick={handleDelete}
                disabled={remoteSelectedCount === 0 || isDeleting}
                title="Delete selected items from Rescale"
                className={`flex items-center gap-1 px-3 py-1 text-sm rounded ${
                  remoteSelectedCount > 0 && !isDeleting
                    ? 'bg-red-500 text-white hover:bg-red-600'
                    : 'bg-gray-300 dark:bg-gray-600 text-gray-500 dark:text-gray-400 cursor-not-allowed'
                }`}
              >
                <TrashIcon className="w-4 h-4" />
                Delete
              </button>
            </div>
          </div>

          {/* Remote browser */}
          <div className="flex-1 overflow-hidden">
            <RemoteBrowser />
          </div>
        </div>
      </div>

      {/* Status bar */}
      <div className="px-3 py-1 text-sm text-gray-600 dark:text-gray-400 border-t border-gray-200 dark:border-gray-700 bg-gray-50 dark:bg-gray-800">
        {status}
      </div>

      {/* Confirmation dialogs */}
      <ConfirmDialog
        isOpen={uploadConfirm !== null}
        title="Confirm Upload"
        message={`Upload ${uploadConfirm?.items.length ?? 0} item(s) to:\n${uploadConfirm?.destPath ?? ''}`}
        confirmText="Upload"
        warning={
          uploadConfirm && uploadConfirm.folderCount > 0
            ? `${uploadConfirm.folderCount} folder(s) will be uploaded recursively (merge mode: reuse existing folders).`
            : undefined
        }
        onConfirm={confirmUpload}
        onCancel={() => setUploadConfirm(null)}
      />

      <ConfirmDialog
        isOpen={downloadConfirm !== null}
        title="Confirm Download"
        message={`Download ${downloadConfirm?.items.length ?? 0} item(s) to:\n${downloadConfirm?.destPath ?? ''}`}
        confirmText="Download"
        warning={
          downloadConfirm && downloadConfirm.folderCount > 0
            ? `${downloadConfirm.folderCount} folder(s) will be downloaded recursively (merge mode: skip existing files).`
            : undefined
        }
        onConfirm={confirmDownload}
        onCancel={() => setDownloadConfirm(null)}
      />

      <ConfirmDialog
        isOpen={deleteConfirm !== null}
        title="Confirm Delete"
        message={`Delete ${deleteConfirm?.length ?? 0} item(s) from Rescale?\n\nThis cannot be undone.`}
        confirmText="Delete"
        isDanger
        onConfirm={confirmDelete}
        onCancel={() => setDeleteConfirm(null)}
      />

      {/* v4.0.8: Merge confirmation dialog for existing folders */}
      <ConfirmDialog
        isOpen={mergeConfirm !== null}
        title="Folder Already Exists"
        message={
          mergeConfirm
            ? `The following folder${mergeConfirm.existingFolders.length > 1 ? 's' : ''} already exist${mergeConfirm.existingFolders.length === 1 ? 's' : ''} on Rescale:\n\n${mergeConfirm.existingFolders.map(f => `• ${f}`).join('\n')}\n\nMerge files into the existing folder${mergeConfirm.existingFolders.length > 1 ? 's' : ''}?`
            : ''
        }
        confirmText="Merge"
        warning="Files with the same name will be uploaded as new versions."
        onConfirm={confirmMerge}
        onCancel={cancelMerge}
      />

      {/* v4.0.8: Error dialog for critical errors */}
      <ErrorDialog
        isOpen={errorDialog !== null}
        title={errorDialog?.title ?? 'Error'}
        message={errorDialog?.message ?? ''}
        onClose={() => setErrorDialog(null)}
      />
    </div>
  )
}
