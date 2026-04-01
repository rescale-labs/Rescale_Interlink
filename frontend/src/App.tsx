import { useState, useEffect, useRef, createContext, useContext } from 'react'
import { Tab } from '@headlessui/react'
import {
  Cog6ToothIcon,
  Square3Stack3DIcon,
  FolderOpenIcon,
  ArrowsRightLeftIcon,
  DocumentTextIcon,
  PlayIcon,
} from '@heroicons/react/24/outline'
import clsx from 'clsx'

// All tab components
import {
  SetupTab,
  ActivityTab,
  FileBrowserTab,
  TransfersTab,
  SingleJobTab,
  PURTab,
} from './components/tabs'
import { ErrorBoundary } from './components/common'
import ErrorReportModal from './components/ErrorReportModal'
import * as App from '../wailsjs/go/wailsapp/App'
import { wailsapp } from '../wailsjs/go/models'
import { BrowserOpenURL } from '../wailsjs/runtime/runtime'
import { useConfigStore } from './stores/configStore'
import { useFileBrowserStore } from './stores/fileBrowserStore'
import { useLogStore } from './stores/logStore'
import { useTransferStore } from './stores/transferStore'
import { useRunStore } from './stores/runStore'
import { useErrorReportStore } from './stores/errorReportStore'

// Tab navigation context for switching tabs from other components
interface TabNavigationContextType {
  switchToTab: (tabName: string) => void;
}
const TabNavigationContext = createContext<TabNavigationContextType>({ switchToTab: () => {} });
export const useTabNavigation = () => useContext(TabNavigationContext);

const tabs = [
  { name: 'Setup', icon: Cog6ToothIcon, component: SetupTab },
  { name: 'Single Job', icon: PlayIcon, component: SingleJobTab },
  { name: 'PUR (Multiple Jobs)', icon: Square3Stack3DIcon, component: PURTab, title: 'PUR = Parallel Upload and Run' },
  { name: 'File Browser', icon: FolderOpenIcon, component: FileBrowserTab },
  { name: 'Transfers', icon: ArrowsRightLeftIcon, component: TransfersTab },
  { name: 'Activity Logs', icon: DocumentTextIcon, component: ActivityTab },
]

function AppComponent() {
  const [appInfo, setAppInfo] = useState<wailsapp.AppInfoDTO | null>(null)
  const [selectedTabIndex, setSelectedTabIndex] = useState(0)
  const {
    workspaceName,
    workspaceId,
    connectionStatus,
    config,
    testConnection,
    setupEventListeners: setupConfigEventListeners,
    fetchConfig,
  } = useConfigStore()
  const { overallMessage, overallProgress } = useLogStore()
  const { stats: transferStats, setupEventListeners: setupTransferEventListeners } = useTransferStore()
  const { activeRun, setupEventListeners: setupRunEventListeners, recoverFromRestart } = useRunStore()
  const { setupEventListeners: setupFileBrowserEventListeners } = useFileBrowserStore()
  const { setupEventListeners: setupErrorReportEventListeners } = useErrorReportStore()

  // App-level listener — persists across tab navigation
  // (events would be missed if set up inside ActivityTab only)
  const { setupEventListeners } = useLogStore()
  useEffect(() => {
    const cleanup = setupEventListeners()
    return cleanup
  }, [setupEventListeners])

  // App-level listener — persists across tab navigation
  // (enumeration events would be lost if set up inside TransfersTab only)
  useEffect(() => {
    const cleanup = setupTransferEventListeners()
    return cleanup
  }, [setupTransferEventListeners])

  // App-level stats polling so footer updates on all tabs.
  // The Transfers tab's own 500ms polling also calls fetchStats(), giving faster
  // updates when on that tab. Both are idempotent and harmless to overlap.
  useEffect(() => {
    const interval = setInterval(() => {
      useTransferStore.getState().fetchStats()
    }, 2000)
    return () => clearInterval(interval)
  }, [])

  // App-level listener — persists across tab navigation
  // (tracks active runs, state changes, completion, and queued job auto-start)
  useEffect(() => {
    const cleanup = setupRunEventListeners()
    return cleanup
  }, [setupRunEventListeners])

  // App-level listener — persists across tab navigation
  useEffect(() => {
    const cleanup = setupFileBrowserEventListeners()
    return cleanup
  }, [setupFileBrowserEventListeners])

  // App-level listener — persists across tab navigation
  useEffect(() => {
    const cleanup = setupErrorReportEventListeners()
    return cleanup
  }, [setupErrorReportEventListeners])

  // Recover active run state after app restart — checks localStorage for
  // persisted run info and loads historical state from disk.
  useEffect(() => {
    recoverFromRestart()
  }, [recoverFromRestart])

  // Set up config event listeners and fetch config on mount
  useEffect(() => {
    const cleanup = setupConfigEventListeners()
    fetchConfig()
    return cleanup
  }, [setupConfigEventListeners, fetchConfig])

  // Guard: skip if already testing/failed/connected to prevent re-trigger loop.
  // Auto-trigger workspace fetch when API key is present but workspace is null,
  // so env-var API key users see workspace info without clicking "Test Connection".
  useEffect(() => {
    // Only trigger if:
    // 1. Config is loaded and has an API key
    // 2. Workspace is not yet fetched
    // 3. Connection status is 'unknown' (never tested yet)
    // CRITICAL: Do NOT re-trigger if status is 'testing', 'failed', or 'connected'
    if (config?.apiKey && !workspaceId && connectionStatus === 'unknown') {
      testConnection()
    }
  }, [config?.apiKey, workspaceId, connectionStatus, testConnection])

  useEffect(() => {
    // Fetch app info from Go backend
    App.GetAppInfo().then(setAppInfo).catch(console.error)
  }, [])

  // Check for updates on startup (non-blocking, 2s delay)
  const updateCheckRan = useRef(false)
  useEffect(() => {
    if (updateCheckRan.current) return
    updateCheckRan.current = true
    const timer = setTimeout(async () => {
      try {
        await App.CheckForUpdates()
        const refreshedInfo = await App.GetAppInfo()
        setAppInfo(refreshedInfo)
      } catch (err) {
        console.error('Failed to check for updates:', err)
      }
    }, 2000)
    return () => clearTimeout(timer)
  }, [])

  // Tab navigation function
  const switchToTab = (tabName: string) => {
    const index = tabs.findIndex(t => t.name === tabName)
    if (index !== -1) {
      setSelectedTabIndex(index)
    }
  }

  return (
    <TabNavigationContext.Provider value={{ switchToTab }}>
      <ErrorReportModal />
      <div className="h-screen flex flex-col bg-slate-50">
        {/* Header */}
        <header className="bg-white border-b border-gray-200 px-4 py-3 flex items-center justify-between">
          <div className="flex items-center space-x-4">
            <img
              src="/rescale-logo.png"
              alt="Rescale"
              className="h-8 object-contain"
            />
            <img
              src="/interlink-logo.png"
              alt="Interlink"
              className="h-8 object-contain"
            />
          </div>
          {/* Workspace info (center) */}
          <div className="flex-1 flex justify-center">
            {connectionStatus === 'connected' && workspaceName && (
              <span className="text-sm text-gray-600">
                <span className="text-gray-500">Workspace: </span>
                <span className="font-medium">{workspaceName}</span>
                {workspaceId && (
                  <span className="text-gray-400 ml-1">({workspaceId})</span>
                )}
              </span>
            )}
          </div>
          {/* Version, FIPS status, and update notification (right) */}
          <div className="flex flex-col items-end text-sm text-gray-500">
            <div className="flex items-center space-x-4">
              {appInfo && (
                <>
                  <span>{appInfo.version}</span>
                  {appInfo.fipsEnabled && appInfo.fipsStatus && (
                    <span className="px-2 py-0.5 bg-green-100 text-green-700 rounded text-xs font-medium">
                      {appInfo.fipsStatus}
                    </span>
                  )}
                </>
              )}
            </div>
            {/* Update notification */}
            {appInfo?.versionCheck?.hasUpdate && appInfo.versionCheck.releaseUrl && (
              <button
                onClick={() => BrowserOpenURL(appInfo.versionCheck!.releaseUrl!)}
                className="text-xs px-2 py-0.5 bg-yellow-100 text-yellow-700 hover:bg-yellow-200 rounded font-medium cursor-pointer transition-colors"
                title={`Update available: ${appInfo.versionCheck.latestVersion}`}
              >
                Update available: {appInfo.versionCheck.latestVersion} &rarr;
              </button>
            )}
          </div>
        </header>

        {/* Main Content with Tabs */}
        <Tab.Group as="div" className="flex-1 flex overflow-hidden" selectedIndex={selectedTabIndex} onChange={setSelectedTabIndex}>
        {/* Sidebar with tabs */}
        <Tab.List className="w-48 bg-white border-r border-gray-200 py-4 flex flex-col">
          {tabs.map((tab) => (
            <Tab
              key={tab.name}
              title={(tab as any).title}
              className={({ selected }) =>
                clsx(
                  'flex items-center px-4 py-2.5 text-sm font-medium text-left transition-colors',
                  'focus:outline-none focus:bg-slate-100',
                  selected
                    ? 'text-rescale-blue bg-blue-50 border-r-2 border-rescale-blue'
                    : 'text-gray-600 hover:text-gray-900 hover:bg-slate-50'
                )
              }
            >
              <tab.icon className="w-5 h-5 mr-3" />
              {tab.name}
            </Tab>
          ))}
        </Tab.List>

        {/* Tab panels — wrapped in ErrorBoundary to catch rendering errors */}
        <Tab.Panels className="flex-1 overflow-hidden">
          {tabs.map((tab) => (
            <Tab.Panel key={tab.name} className="h-full">
              <ErrorBoundary>
                <tab.component />
              </ErrorBoundary>
            </Tab.Panel>
          ))}
        </Tab.Panels>
      </Tab.Group>

        {/* Status bar */}
        <footer className="bg-white border-t border-gray-200 px-4 py-2 text-xs text-gray-500 flex items-center justify-between">
          <div className="flex items-center space-x-4">
            <span>{overallMessage}</span>
            {overallProgress > 0 && overallProgress < 100 && (
              <span className="text-blue-600 font-medium">{overallProgress.toFixed(0)}%</span>
            )}
          </div>
          <div className="flex items-center space-x-4">
            {/* Active run indicator */}
            {activeRun && activeRun.status === 'active' && (
              <span className="text-blue-600 font-medium">
                {activeRun.runType === 'pur' ? 'PUR' : 'Job'} running:{' '}
                {activeRun.completedJobs + activeRun.failedJobs}/{activeRun.totalJobs}
              </span>
            )}
            {(transferStats.active > 0 || transferStats.queued > 0) && (
              <span className="text-blue-600">
                {transferStats.active > 0 && `${transferStats.active} active`}
                {transferStats.active > 0 && transferStats.queued > 0 && ', '}
                {transferStats.queued > 0 && `${transferStats.queued} queued`}
              </span>
            )}
          </div>
        </footer>
      </div>
    </TabNavigationContext.Provider>
  )
}

export default AppComponent
