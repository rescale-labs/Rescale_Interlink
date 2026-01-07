import { useEffect, useState } from 'react';
import { useConfigStore } from '../../stores';
import {
  CheckCircleIcon,
  XCircleIcon,
  ArrowPathIcon,
  ClipboardDocumentIcon,
  FolderOpenIcon,
  EyeIcon,
  EyeSlashIcon,
} from '@heroicons/react/24/outline';
import clsx from 'clsx';
import { ClipboardGetText, EventsOn, EventsOff } from '../../../wailsjs/runtime/runtime';
import {
  GetAutoDownloadConfig,
  SaveAutoDownloadConfig,
  GetAutoDownloadStatus,
  TestAutoDownloadConnection,
  SelectDirectory,
  SaveConfigAs,
  GetDefaultConfigPath,
  SaveFile,
} from '../../../wailsjs/go/wailsapp/App';
import { wailsapp } from '../../../wailsjs/go/models';

// Token source options matching Fyne
type TokenSource = 'environment' | 'file' | 'direct';

const PROXY_MODES = ['no-proxy', 'system', 'ntlm', 'basic'] as const;
const COMPRESSION_OPTIONS = ['gzip', 'none'] as const;

export function SetupTab() {
  const {
    config,
    connectionStatus,
    connectionEmail,
    connectionError,
    lastConnectionTest,
    isLoading,
    isSaving,
    fetchConfig,
    fetchAppInfo,
    updateConfig,
    saveConfig,
    testConnection,
    selectFile,
    setupEventListeners,
  } = useConfigStore();

  const [tokenSource, setTokenSource] = useState<TokenSource>('direct');
  const [validationEnabled, setValidationEnabled] = useState(false);
  const [statusMessage, setStatusMessage] = useState('Ready');
  const [showApiKey, setShowApiKey] = useState(false); // v4.0.1: API key visibility toggle
  const [defaultConfigPath, setDefaultConfigPath] = useState<string>(''); // v4.0.8: Show config location

  // Auto-download state
  // v4.0.8: API key removed - uses unified source from Setup tab config
  const [autoDownloadConfig, setAutoDownloadConfig] = useState<wailsapp.AutoDownloadConfigDTO>({
    enabled: false,
    correctnessTag: 'isCorrect:true',
    defaultDownloadFolder: '',
    scanIntervalMinutes: 10,
    lookbackDays: 7,
  });
  const [autoDownloadStatus, setAutoDownloadStatus] = useState<wailsapp.AutoDownloadStatusDTO | null>(null);
  const [autoDownloadTestStatus, setAutoDownloadTestStatus] = useState<'idle' | 'testing' | 'success' | 'failed'>('idle');
  const [autoDownloadTestResult, setAutoDownloadTestResult] = useState<{
    success: boolean;
    email?: string;
    folderOk?: boolean;
    folderError?: string;
    error?: string;
  } | null>(null);
  const [isAutoDownloadSaving, setIsAutoDownloadSaving] = useState(false);

  // Setup event listeners and fetch initial data
  useEffect(() => {
    const cleanup = setupEventListeners();
    fetchConfig();
    fetchAppInfo();
    // v4.0.8: Fetch default config path to show in UI
    GetDefaultConfigPath().then(setDefaultConfigPath).catch(console.error);
    return cleanup;
  }, []);

  // Fetch auto-download config and status on mount
  useEffect(() => {
    const fetchAutoDownload = async () => {
      try {
        const cfg = await GetAutoDownloadConfig();
        setAutoDownloadConfig(cfg);
        const status = await GetAutoDownloadStatus();
        setAutoDownloadStatus(status);
      } catch (err) {
        console.error('Failed to fetch auto-download config:', err);
      }
    };
    fetchAutoDownload();

    // Listen for test connection result
    const handleTestResult = (result: {
      success: boolean;
      email?: string;
      folderOk?: boolean;
      folderError?: string;
      error?: string;
    }) => {
      setAutoDownloadTestResult(result);
      setAutoDownloadTestStatus(result.success ? 'success' : 'failed');
    };

    EventsOn('interlink:autodownload_test_result', handleTestResult);

    return () => {
      EventsOff('interlink:autodownload_test_result');
    };
  }, []);

  // Initialize local state from config
  useEffect(() => {
    if (config) {
      // Check if we should enable validation based on pattern
      if (config.validationPattern && config.validationPattern !== '*.avg.fnc') {
        setValidationEnabled(true);
      }
    }
  }, [config]);

  // v4.0.3: Sync statusMessage with connection test results
  useEffect(() => {
    if (connectionStatus === 'connected' && connectionEmail) {
      setStatusMessage(`Connected as ${connectionEmail}`);
    } else if (connectionStatus === 'failed' && connectionError) {
      setStatusMessage(`Connection failed: ${connectionError}`);
    } else if (connectionStatus === 'testing') {
      setStatusMessage('Testing connection...');
    }
  }, [connectionStatus, connectionEmail, connectionError]);

  const handlePasteApiKey = async () => {
    try {
      const text = await ClipboardGetText();
      if (text) {
        updateConfig({ apiKey: text.trim() });
      }
    } catch (err) {
      console.error('Failed to paste from clipboard:', err);
    }
  };

  const handleSelectTokenFile = async () => {
    try {
      const path = await selectFile('Select Token File');
      if (path) {
        // Load token from file would be handled by backend
        setStatusMessage(`Token file selected: ${path}`);
      }
    } catch (err) {
      console.error('Failed to select file:', err);
    }
  };

  const handleTestConnection = async () => {
    setStatusMessage('Testing connection...');
    await testConnection();
  };

  const handleSaveConfig = async () => {
    try {
      setStatusMessage('Saving configuration...');
      await saveConfig();
      setStatusMessage(`Configuration saved to ${defaultConfigPath}`);
    } catch (err) {
      setStatusMessage(`Failed to save: ${err}`);
    }
  };

  // v4.0.8: Export config to custom location
  const handleExportConfig = async () => {
    try {
      const path = await SaveFile('Export Configuration');
      if (path) {
        await SaveConfigAs(path);
        setStatusMessage(`Configuration exported to ${path}`);
      }
    } catch (err) {
      setStatusMessage(`Failed to export: ${err}`);
    }
  };

  // Auto-download handlers
  const handleAutoDownloadConfigChange = (field: keyof wailsapp.AutoDownloadConfigDTO, value: string | number | boolean) => {
    setAutoDownloadConfig(prev => ({ ...prev, [field]: value }));
  };

  const handleSelectDownloadFolder = async () => {
    try {
      const path = await SelectDirectory('Select Download Folder');
      if (path) {
        handleAutoDownloadConfigChange('defaultDownloadFolder', path);
      }
    } catch (err) {
      console.error('Failed to select folder:', err);
    }
  };

  const handleSaveAutoDownloadConfig = async () => {
    try {
      setIsAutoDownloadSaving(true);
      setStatusMessage('Saving auto-download settings...');
      await SaveAutoDownloadConfig(autoDownloadConfig);
      const status = await GetAutoDownloadStatus();
      setAutoDownloadStatus(status);
      setStatusMessage('Auto-download settings saved successfully');
    } catch (err) {
      setStatusMessage(`Failed to save auto-download settings: ${err}`);
    } finally {
      setIsAutoDownloadSaving(false);
    }
  };

  const handleTestAutoDownloadConnection = async () => {
    setAutoDownloadTestStatus('testing');
    setAutoDownloadTestResult(null);
    await TestAutoDownloadConnection(autoDownloadConfig);
  };

  const getConnectionStatusIcon = () => {
    switch (connectionStatus) {
      case 'testing':
        return <ArrowPathIcon className="w-5 h-5 text-blue-500 animate-spin" />;
      case 'connected':
        return <CheckCircleIcon className="w-5 h-5 text-green-500" />;
      case 'failed':
        return <XCircleIcon className="w-5 h-5 text-red-500" />;
      default:
        return null;
    }
  };

  const getConnectionStatusText = () => {
    if (connectionStatus === 'testing') return 'Testing...';
    if (connectionStatus === 'connected') {
      let text = 'Connected';
      if (lastConnectionTest) {
        const elapsed = Date.now() - lastConnectionTest.getTime();
        if (elapsed < 60000) {
          text += ` (${Math.round(elapsed / 1000)}s ago)`;
        } else {
          text += ` (${Math.round(elapsed / 60000)}m ago)`;
        }
      }
      return text;
    }
    if (connectionStatus === 'failed') {
      return `Failed: ${connectionError || 'Unknown error'}`;
    }
    return 'Not connected';
  };

  if (isLoading && !config) {
    return (
      <div className="tab-panel flex items-center justify-center">
        <ArrowPathIcon className="w-8 h-8 text-gray-400 animate-spin" />
        <span className="ml-2 text-gray-500">Loading configuration...</span>
      </div>
    );
  }

  const proxyEnabled = config?.proxyMode !== 'no-proxy' && config?.proxyMode !== 'system';
  const basicAuthEnabled = config?.proxyMode === 'basic';

  return (
    <div className="tab-panel">
      {/* Action Buttons - pinned at top */}
      <div className="flex items-center gap-2 mb-4 pb-4 border-b border-gray-200">
        <button
          onClick={handleSaveConfig}
          disabled={isSaving}
          className="btn-primary"
          title={`Save to ${defaultConfigPath}`}
        >
          {isSaving ? 'Saving...' : 'Save Default Config'}
        </button>
        <button
          onClick={handleExportConfig}
          className="btn-secondary"
          title="Export configuration to a custom location"
        >
          Export Config
        </button>
        <div className="flex-1" />
        <span className="text-sm text-gray-500">{statusMessage}</span>
      </div>

      <div className="space-y-6 overflow-y-auto flex-1">
        {/* API Configuration Section */}
        <div className="card">
          <h3 className="text-base font-semibold text-gray-900 mb-4">API Configuration</h3>
          <div className="space-y-4">
            {/* Platform URL */}
            <div>
              <label className="label">Platform URL</label>
              <input
                type="text"
                className="input"
                placeholder="https://platform.rescale.com"
                value={config?.apiBaseUrl || ''}
                onChange={(e) => updateConfig({ apiBaseUrl: e.target.value })}
              />
            </div>

            {/* Token Source */}
            <div>
              <label className="label">Token Source</label>
              <div className="flex flex-col space-y-2">
                {[
                  { value: 'environment', label: 'Environment Variable (RESCALE_API_KEY)' },
                  { value: 'file', label: 'Token File' },
                  { value: 'direct', label: 'Direct Input' },
                ].map((option) => (
                  <label key={option.value} className="flex items-center">
                    <input
                      type="radio"
                      name="tokenSource"
                      value={option.value}
                      checked={tokenSource === option.value}
                      onChange={(e) => setTokenSource(e.target.value as TokenSource)}
                      className="h-4 w-4 text-rescale-blue focus:ring-rescale-blue"
                    />
                    <span className="ml-2 text-sm text-gray-700">{option.label}</span>
                  </label>
                ))}
              </div>
            </div>

            {/* Token File Button */}
            {tokenSource === 'file' && (
              <div>
                <button
                  onClick={handleSelectTokenFile}
                  className="btn-secondary flex items-center"
                >
                  <FolderOpenIcon className="w-4 h-4 mr-2" />
                  Select Token File...
                </button>
              </div>
            )}

            {/* API Key */}
            <div>
              <label className="label">API Key</label>
              <div className="flex gap-2">
                <input
                  type={showApiKey ? 'text' : 'password'}
                  className="input flex-1"
                  placeholder="API Key"
                  value={config?.apiKey || ''}
                  onChange={(e) => updateConfig({ apiKey: e.target.value })}
                  disabled={tokenSource !== 'direct'}
                />
                <button
                  onClick={() => setShowApiKey(!showApiKey)}
                  className="btn-secondary p-2"
                  title={showApiKey ? 'Hide API key' : 'Show API key'}
                >
                  {showApiKey ? (
                    <EyeSlashIcon className="w-5 h-5" />
                  ) : (
                    <EyeIcon className="w-5 h-5" />
                  )}
                </button>
                <button
                  onClick={handlePasteApiKey}
                  className="btn-secondary p-2"
                  title="Paste from clipboard"
                  disabled={tokenSource !== 'direct'}
                >
                  <ClipboardDocumentIcon className="w-5 h-5" />
                </button>
              </div>
            </div>

            {/* Test Connection */}
            <div className="flex items-center gap-4">
              <button
                onClick={handleTestConnection}
                disabled={connectionStatus === 'testing'}
                className="btn-primary"
              >
                {connectionStatus === 'testing' ? 'Testing...' : 'Test Connection'}
              </button>
              <div className="flex items-center gap-2">
                {getConnectionStatusIcon()}
                <span className={clsx(
                  'text-sm',
                  connectionStatus === 'connected' && 'text-green-600',
                  connectionStatus === 'failed' && 'text-red-600',
                  connectionStatus === 'testing' && 'text-blue-600',
                  connectionStatus === 'unknown' && 'text-gray-500',
                )}>
                  {getConnectionStatusText()}
                </span>
              </div>
            </div>

            {/* Active Source */}
            {connectionEmail && (
              <div className="text-sm text-gray-600">
                Active Source: {tokenSource} ({connectionEmail})
              </div>
            )}
          </div>
        </div>

        {/* Run Folders Subpath and Validation Configuration */}
        <div className="card">
          <h3 className="text-base font-semibold text-gray-900 mb-4">
            Run Folders Subpath and Validation Configuration
          </h3>
          <div className="space-y-4">
            <div>
              <label className="label">Run Subpath</label>
              <input
                type="text"
                className="input"
                placeholder="Optional subpath within each run directory"
                value={config?.runSubpath || ''}
                onChange={(e) => updateConfig({ runSubpath: e.target.value })}
              />
            </div>

            <div className="flex items-center">
              <input
                type="checkbox"
                id="validationEnabled"
                checked={validationEnabled}
                onChange={(e) => setValidationEnabled(e.target.checked)}
                className="h-4 w-4 rounded border border-gray-300 text-rescale-blue focus:ring-rescale-blue focus:ring-2 bg-white cursor-pointer"
              />
              <label htmlFor="validationEnabled" className="ml-2 text-sm text-gray-700 cursor-pointer">
                Enable validation
              </label>
            </div>

            <div>
              <label className="label">Validation Pattern</label>
              <input
                type="text"
                className="input"
                placeholder="e.g., *.avg.fnc or results.dat"
                value={config?.validationPattern || ''}
                onChange={(e) => updateConfig({ validationPattern: e.target.value })}
                disabled={!validationEnabled}
              />
              <p className="mt-1 text-xs text-gray-500">
                Validation checks that each run directory contains files matching the pattern.
              </p>
            </div>
          </div>
        </div>

        {/* Worker Configuration Section */}
        <div className="card">
          <h3 className="text-base font-semibold text-gray-900 mb-4">Worker Configuration</h3>
          <div className="grid grid-cols-3 gap-4">
            <div>
              <label className="label">Tar Workers</label>
              <input
                type="number"
                min="1"
                max="16"
                className="input"
                value={config?.tarWorkers || 4}
                onChange={(e) => updateConfig({ tarWorkers: parseInt(e.target.value) || 4 })}
              />
            </div>
            <div>
              <label className="label">Upload Workers</label>
              <input
                type="number"
                min="1"
                max="16"
                className="input"
                value={config?.uploadWorkers || 4}
                onChange={(e) => updateConfig({ uploadWorkers: parseInt(e.target.value) || 4 })}
              />
            </div>
            <div>
              <label className="label">Job Workers</label>
              <input
                type="number"
                min="1"
                max="16"
                className="input"
                value={config?.jobWorkers || 4}
                onChange={(e) => updateConfig({ jobWorkers: parseInt(e.target.value) || 4 })}
              />
            </div>
          </div>
        </div>

        {/* Tar Options Section */}
        <div className="card">
          <h3 className="text-base font-semibold text-gray-900 mb-4">Tar Options</h3>
          <div className="space-y-4">
            <div>
              <label className="label">Exclude Patterns</label>
              <input
                type="text"
                className="input"
                placeholder="*.tmp,*.log,*.bak"
                value={config?.excludePatterns || ''}
                onChange={(e) => updateConfig({ excludePatterns: e.target.value })}
              />
            </div>
            <div>
              <label className="label">Include Patterns</label>
              <input
                type="text"
                className="input"
                placeholder="*.dat,*.csv,*.inp"
                value={config?.includePatterns || ''}
                onChange={(e) => updateConfig({ includePatterns: e.target.value })}
              />
            </div>
            <div>
              <label className="label">Compression</label>
              <select
                className="input"
                value={config?.tarCompression || 'gzip'}
                onChange={(e) => updateConfig({ tarCompression: e.target.value })}
              >
                {COMPRESSION_OPTIONS.map((opt) => (
                  <option key={opt} value={opt}>{opt}</option>
                ))}
              </select>
            </div>
            <div className="flex items-center">
              <input
                type="checkbox"
                id="flattenTar"
                checked={config?.flattenTar || false}
                onChange={(e) => updateConfig({ flattenTar: e.target.checked })}
                className="h-4 w-4 rounded border border-gray-300 text-rescale-blue focus:ring-rescale-blue focus:ring-2 bg-white cursor-pointer"
              />
              <label htmlFor="flattenTar" className="ml-2 text-sm text-gray-700 cursor-pointer">
                Flatten directory structure in tar
              </label>
            </div>
            <p className="text-xs text-gray-500">
              Patterns support wildcards (*). Use comma-separated list.
              Exclude: skip these files when creating tar archives.
              Include: only include these files (leave empty to include all).
            </p>
          </div>
        </div>

        {/* Advanced Settings Section */}
        <div className="card">
          <h3 className="text-base font-semibold text-gray-900 mb-4">Advanced Settings</h3>
          <div className="space-y-4">
            <div className="flex items-center">
              <input
                type="checkbox"
                id="detailedLogging"
                checked={config?.detailedLogging || false}
                onChange={(e) => updateConfig({ detailedLogging: e.target.checked })}
                className="h-4 w-4 rounded border border-gray-300 text-rescale-blue focus:ring-rescale-blue focus:ring-2 bg-white cursor-pointer"
              />
              <label htmlFor="detailedLogging" className="ml-2 text-sm text-gray-700 cursor-pointer">
                Enable detailed logging
              </label>
            </div>
            <p className="text-xs text-gray-500">
              When enabled, detailed timing and performance metrics will appear in the Activity tab.
              Useful for diagnosing slow transfers or troubleshooting issues.
            </p>
          </div>
        </div>

        {/* Proxy Configuration Section */}
        <div className="card">
          <h3 className="text-base font-semibold text-gray-900 mb-4">Proxy Configuration</h3>
          <div className="space-y-4">
            <div>
              <label className="label">Proxy Mode</label>
              <select
                className="input"
                value={config?.proxyMode || 'no-proxy'}
                onChange={(e) => updateConfig({ proxyMode: e.target.value })}
              >
                {PROXY_MODES.map((mode) => (
                  <option key={mode} value={mode}>{mode}</option>
                ))}
              </select>
            </div>
            <div className="grid grid-cols-2 gap-4">
              <div>
                <label className="label">Proxy Host</label>
                <input
                  type="text"
                  className="input"
                  placeholder="proxy.company.com"
                  value={config?.proxyHost || ''}
                  onChange={(e) => updateConfig({ proxyHost: e.target.value })}
                  disabled={!proxyEnabled}
                />
              </div>
              <div>
                <label className="label">Proxy Port</label>
                <input
                  type="number"
                  className="input"
                  placeholder="8080"
                  value={config?.proxyPort || ''}
                  onChange={(e) => updateConfig({ proxyPort: parseInt(e.target.value) || 0 })}
                  disabled={!proxyEnabled}
                />
              </div>
            </div>
            <div className="grid grid-cols-2 gap-4">
              <div>
                <label className="label">Username</label>
                <input
                  type="text"
                  className="input"
                  placeholder="Username (for Basic auth)"
                  value={config?.proxyUser || ''}
                  onChange={(e) => updateConfig({ proxyUser: e.target.value })}
                  disabled={!basicAuthEnabled}
                />
              </div>
              <div>
                <label className="label">Password</label>
                <input
                  type="password"
                  className="input"
                  placeholder="Password (for Basic auth)"
                  value={config?.proxyPassword || ''}
                  onChange={(e) => updateConfig({ proxyPassword: e.target.value })}
                  disabled={!basicAuthEnabled}
                />
              </div>
            </div>
          </div>
        </div>

        {/* Auto-Download Settings Section (v4.0.0) */}
        <div className="card">
          <h3 className="text-base font-semibold text-gray-900 mb-4">Auto-Download Settings</h3>
          <div className="space-y-4">
            {/* Enable Auto-Download */}
            <div className="flex items-center">
              <input
                type="checkbox"
                id="autoDownloadEnabled"
                checked={autoDownloadConfig.enabled}
                onChange={(e) => handleAutoDownloadConfigChange('enabled', e.target.checked)}
                className="h-4 w-4 rounded border border-gray-300 text-rescale-blue focus:ring-rescale-blue focus:ring-2 bg-white cursor-pointer"
              />
              <label htmlFor="autoDownloadEnabled" className="ml-2 text-sm text-gray-700 cursor-pointer">
                Enable Auto-Download
              </label>
            </div>

            {/* Correctness Tag */}
            <div>
              <label className="label">Correctness Tag</label>
              <input
                type="text"
                className="input"
                placeholder="isCorrect:true"
                value={autoDownloadConfig.correctnessTag}
                onChange={(e) => handleAutoDownloadConfigChange('correctnessTag', e.target.value)}
                disabled={!autoDownloadConfig.enabled}
              />
              <p className="mt-1 text-xs text-gray-500">
                Jobs must have this tag to be eligible for auto-download.
              </p>
            </div>

            {/* Download Folder */}
            <div>
              <label className="label">Default Download Folder</label>
              <div className="flex gap-2">
                <input
                  type="text"
                  className="input flex-1"
                  placeholder="/path/to/downloads"
                  value={autoDownloadConfig.defaultDownloadFolder}
                  onChange={(e) => handleAutoDownloadConfigChange('defaultDownloadFolder', e.target.value)}
                  disabled={!autoDownloadConfig.enabled}
                />
                <button
                  onClick={handleSelectDownloadFolder}
                  disabled={!autoDownloadConfig.enabled}
                  className="btn-secondary p-2"
                  title="Browse for folder"
                >
                  <FolderOpenIcon className="w-5 h-5" />
                </button>
              </div>
              <p className="mt-1 text-xs text-gray-500">
                Base directory for downloaded job outputs. Can be overridden per-job.
              </p>
            </div>

            {/* Scan Interval and Lookback Days */}
            <div className="grid grid-cols-2 gap-4">
              <div>
                <label className="label">Scan Interval (minutes)</label>
                <input
                  type="number"
                  min="1"
                  max="1440"
                  className="input"
                  value={autoDownloadConfig.scanIntervalMinutes}
                  onChange={(e) => handleAutoDownloadConfigChange('scanIntervalMinutes', parseInt(e.target.value) || 10)}
                  disabled={!autoDownloadConfig.enabled}
                />
                <p className="mt-1 text-xs text-gray-500">1-1440 minutes</p>
              </div>
              <div>
                <label className="label">Lookback Days</label>
                <input
                  type="number"
                  min="1"
                  max="365"
                  className="input"
                  value={autoDownloadConfig.lookbackDays}
                  onChange={(e) => handleAutoDownloadConfigChange('lookbackDays', parseInt(e.target.value) || 7)}
                  disabled={!autoDownloadConfig.enabled}
                />
                <p className="mt-1 text-xs text-gray-500">1-365 days</p>
              </div>
            </div>

            {/* Status Display */}
            {autoDownloadStatus && (
              <div className={clsx(
                'p-3 rounded-md text-sm',
                autoDownloadStatus.isValid && autoDownloadStatus.enabled ? 'bg-green-50 text-green-700' :
                autoDownloadStatus.isValid && !autoDownloadStatus.enabled ? 'bg-gray-50 text-gray-700' :
                'bg-yellow-50 text-yellow-700'
              )}>
                <div className="flex items-center gap-2">
                  {autoDownloadStatus.enabled && autoDownloadStatus.isValid ? (
                    <CheckCircleIcon className="w-5 h-5 text-green-500" />
                  ) : autoDownloadStatus.enabled && !autoDownloadStatus.isValid ? (
                    <XCircleIcon className="w-5 h-5 text-yellow-500" />
                  ) : (
                    <span className="w-5 h-5 rounded-full bg-gray-300" />
                  )}
                  <span>{autoDownloadStatus.validationMsg || 'Status unknown'}</span>
                </div>
                {!autoDownloadStatus.configExists && (
                  <p className="mt-1 text-xs">No saved configuration found. Save settings to create one.</p>
                )}
              </div>
            )}

            {/* Test Result Display */}
            {autoDownloadTestResult && (
              <div className={clsx(
                'p-3 rounded-md text-sm',
                autoDownloadTestResult.success ? 'bg-green-50 text-green-700' : 'bg-red-50 text-red-700'
              )}>
                {autoDownloadTestResult.success ? (
                  <div>
                    <div className="flex items-center gap-2">
                      <CheckCircleIcon className="w-5 h-5 text-green-500" />
                      <span>Connection successful</span>
                    </div>
                    {autoDownloadTestResult.email && (
                      <p className="mt-1">User: {autoDownloadTestResult.email}</p>
                    )}
                    {autoDownloadTestResult.folderOk !== undefined && (
                      <p className="mt-1">
                        Folder access: {autoDownloadTestResult.folderOk ? 'OK' : autoDownloadTestResult.folderError}
                      </p>
                    )}
                  </div>
                ) : (
                  <div className="flex items-center gap-2">
                    <XCircleIcon className="w-5 h-5 text-red-500" />
                    <span>{autoDownloadTestResult.error || 'Connection failed'}</span>
                  </div>
                )}
              </div>
            )}

            {/* Action Buttons */}
            <div className="flex items-center gap-4 pt-2">
              <button
                onClick={handleSaveAutoDownloadConfig}
                disabled={isAutoDownloadSaving}
                className="btn-primary"
              >
                {isAutoDownloadSaving ? 'Saving...' : 'Save Settings'}
              </button>
              <button
                onClick={handleTestAutoDownloadConnection}
                disabled={autoDownloadTestStatus === 'testing'}
                className="btn-secondary"
              >
                {autoDownloadTestStatus === 'testing' ? (
                  <span className="flex items-center gap-2">
                    <ArrowPathIcon className="w-4 h-4 animate-spin" />
                    Testing...
                  </span>
                ) : 'Test Connection'}
              </button>
            </div>

            <p className="text-xs text-gray-500">
              Auto-download automatically downloads outputs from completed jobs that have the correctness tag
              and the "Auto Download" custom field set to "Enable" in Rescale.
            </p>
          </div>
        </div>
      </div>
    </div>
  );
}

export default SetupTab;
