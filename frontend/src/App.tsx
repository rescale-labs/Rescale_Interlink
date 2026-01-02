import { useState, useEffect, createContext, useContext } from 'react'
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
import * as App from '../wailsjs/go/wailsapp/App'
import { wailsapp } from '../wailsjs/go/models'
import { useConfigStore } from './stores/configStore'
import { useLogStore } from './stores/logStore'
import { useTransferStore } from './stores/transferStore'

// Tab navigation context for switching tabs from other components
interface TabNavigationContextType {
  switchToTab: (tabName: string) => void;
}
const TabNavigationContext = createContext<TabNavigationContextType>({ switchToTab: () => {} });
export const useTabNavigation = () => useContext(TabNavigationContext);

const tabs = [
  { name: 'Setup', icon: Cog6ToothIcon, component: SetupTab },
  { name: 'Single Job', icon: PlayIcon, component: SingleJobTab },
  { name: 'PUR (Multiple Jobs)', icon: Square3Stack3DIcon, component: PURTab },
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
  const { stats: transferStats } = useTransferStore()

  // v4.0.0: Set up log event listeners at app level so they're always active.
  // Previously these were set up in ActivityTab, which meant events were missed
  // when the user was on other tabs (like File Browser during downloads).
  const { setupEventListeners } = useLogStore()
  useEffect(() => {
    const cleanup = setupEventListeners()
    return cleanup
  }, [setupEventListeners])

  // v4.0.4: Set up config event listeners and fetch config on mount
  useEffect(() => {
    const cleanup = setupConfigEventListeners()
    fetchConfig()
    return cleanup
  }, [setupConfigEventListeners, fetchConfig])

  // v4.0.4: Auto-trigger workspace fetch when API key is present but workspace is null.
  // This fixes the bug where env var API key users never see workspace info because
  // TestConnection() was only called when clicking the "Test Connection" button.
  useEffect(() => {
    // Only trigger if:
    // 1. Config is loaded and has an API key
    // 2. Workspace is not yet fetched
    // 3. Not currently testing connection
    if (config?.apiKey && !workspaceId && connectionStatus !== 'testing') {
      testConnection()
    }
  }, [config?.apiKey, workspaceId, connectionStatus, testConnection])

  useEffect(() => {
    // Fetch app info from Go backend
    App.GetAppInfo().then(setAppInfo).catch(console.error)
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
          {/* Version and FIPS status (right) */}
          <div className="flex items-center space-x-4 text-sm text-gray-500">
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
        </header>

        {/* Main Content with Tabs */}
        <Tab.Group as="div" className="flex-1 flex overflow-hidden" selectedIndex={selectedTabIndex} onChange={setSelectedTabIndex}>
        {/* Sidebar with tabs */}
        <Tab.List className="w-48 bg-white border-r border-gray-200 py-4 flex flex-col">
          {tabs.map((tab) => (
            <Tab
              key={tab.name}
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

        {/* Tab panels - wrapped in ErrorBoundary to catch rendering errors (v4.0.4) */}
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
          {(transferStats.active > 0 || transferStats.queued > 0) && (
            <span className="text-blue-600">
              {transferStats.active > 0 && `${transferStats.active} active`}
              {transferStats.active > 0 && transferStats.queued > 0 && ', '}
              {transferStats.queued > 0 && `${transferStats.queued} queued`}
            </span>
          )}
        </footer>
      </div>
    </TabNavigationContext.Provider>
  )
}

export default AppComponent
