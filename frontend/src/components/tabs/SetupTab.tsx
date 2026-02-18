import { useEffect, useState, useCallback, useRef } from 'react';
import { useConfigStore } from '../../stores';
import {
  CheckCircleIcon,
  XCircleIcon,
  ArrowPathIcon,
  ClipboardDocumentIcon,
  FolderOpenIcon,
  EyeIcon,
  EyeSlashIcon,
  ChevronDownIcon,
  ChevronRightIcon,
  ShieldCheckIcon,
} from '@heroicons/react/24/outline';
import clsx from 'clsx';
import { ClipboardGetText, EventsOn, EventsOff } from '../../../wailsjs/runtime/runtime';
import {
  SelectDirectory,
  SaveConfigAs,
  GetDefaultConfigPath,
  GetDefaultDownloadFolder,
  SaveFile,
  GetDaemonStatus,
  StartDaemon,
  StopDaemon,
  TriggerDaemonScan,
  PauseDaemon,
  ResumeDaemon,
  GetDaemonConfig,
  SaveDaemonConfig,
  ValidateAutoDownloadSetup,
  TestAutoDownloadConnection,
  GetFileLoggingSettings,
  SetFileLoggingEnabled,
  GetServiceStatus,
  StartServiceElevated,
  StopServiceElevated,
  TriggerProfileRescan,
} from '../../../wailsjs/go/wailsapp/App';
import { wailsapp } from '../../../wailsjs/go/models';

// Token source options matching Fyne
type TokenSource = 'environment' | 'file' | 'direct';

const PROXY_MODES = ['no-proxy', 'system', 'ntlm', 'basic'] as const;
const COMPRESSION_OPTIONS = ['gzip', 'none'] as const;

// v4.6.6: Check if URL is a FedRAMP platform (requires FIPS compliance)
// NTLM proxy mode uses non-FIPS algorithms (MD4/MD5) and must be disabled for these platforms
// R2: Use proper URL hostname parsing instead of substring match to prevent spoofing
const isFRMPlatform = (url: string): boolean => {
  try {
    const normalized = url.includes('://') ? url : `https://${url}`;
    const hostname = new URL(normalized).hostname.toLowerCase();
    return hostname === 'rescale-gov.com' || hostname.endsWith('.rescale-gov.com');
  } catch { return false; }
};

// v4.3.0: Platform URL options for dropdown
const PLATFORM_URLS = [
  { value: 'https://platform.rescale.com', label: 'North America (platform.rescale.com)' },
  { value: 'https://kr.rescale.com', label: 'Korea (kr.rescale.com)' },
  { value: 'https://platform.rescale.jp', label: 'Japan (platform.rescale.jp)' },
  { value: 'https://eu.rescale.com', label: 'Europe (eu.rescale.com)' },
  { value: 'https://itar.rescale.com', label: 'US ITAR (itar.rescale.com)' },
  { value: 'https://itar.rescale-gov.com', label: 'US ITAR FRM (itar.rescale-gov.com)' },
] as const;

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
  const [advancedExpanded, setAdvancedExpanded] = useState(false); // v4.3.0: Collapsible advanced settings

  // v4.3.1: Auto-download state unified with daemon config
  // Old autoDownloadConfig removed - now use daemonConfig for all settings
  const [autoDownloadTestStatus, setAutoDownloadTestStatus] = useState<'idle' | 'testing' | 'success' | 'failed'>('idle');
  const [autoDownloadTestResult, setAutoDownloadTestResult] = useState<{
    success: boolean;
    email?: string;
    folderOk?: boolean;
    folderError?: string;
    error?: string;
  } | null>(null);

  // Daemon control state (v4.1.0)
  const [daemonStatus, setDaemonStatus] = useState<wailsapp.DaemonStatusDTO | null>(null);
  const [isDaemonLoading, setIsDaemonLoading] = useState(false);

  // Daemon config state (v4.2.0)
  const [daemonConfig, setDaemonConfig] = useState<wailsapp.DaemonConfigDTO | null>(null);

  // Workspace validation state (v4.2.1)
  const [workspaceValidation, setWorkspaceValidation] = useState<wailsapp.AutoDownloadValidationDTO | null>(null);
  const [isValidating, setIsValidating] = useState(false);

  // v4.3.2: File logging state
  const [fileLoggingEnabled, setFileLoggingEnabled] = useState(false);
  const [logFilePath, setLogFilePath] = useState('');

  // v4.5.1: Service status state (Windows SCM, separate from IPC-based daemon status)
  const [serviceStatus, setServiceStatus] = useState<wailsapp.ServiceStatusDTO | null>(null);
  const [isServiceLoading, setIsServiceLoading] = useState(false);
  const [showUACConfirmDialog, setShowUACConfirmDialog] = useState<'start' | 'stop' | null>(null);

  // v4.5.8: Debounced auto-save state for daemon config
  const [isDaemonConfigSaving, setIsDaemonConfigSaving] = useState(false);
  const [lastSavedConfig, setLastSavedConfig] = useState<wailsapp.DaemonConfigDTO | null>(null);
  const debounceTimeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const prevLookbackRef = useRef<number | null>(null);

  // Setup event listeners and fetch initial data
  useEffect(() => {
    const cleanup = setupEventListeners();
    fetchConfig();
    fetchAppInfo();
    // v4.0.8: Fetch default config path to show in UI
    GetDefaultConfigPath().then(setDefaultConfigPath).catch(console.error);
    return cleanup;
  }, []);

  // v4.3.1: Listen for test connection result
  useEffect(() => {
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

  // Fetch daemon status on mount and periodically (v4.1.0)
  useEffect(() => {
    const fetchDaemonStatus = async () => {
      try {
        const status = await GetDaemonStatus();
        setDaemonStatus(status);
      } catch (err) {
        console.error('Failed to fetch daemon status:', err);
      }
    };

    fetchDaemonStatus();

    // Poll every 5 seconds when tab is visible
    const interval = setInterval(fetchDaemonStatus, 5000);

    return () => clearInterval(interval);
  }, []);

  // v4.5.1: Fetch service status (Windows SCM) on mount and periodically
  useEffect(() => {
    const fetchServiceStatus = async () => {
      try {
        const status = await GetServiceStatus();
        setServiceStatus(status);
      } catch (err) {
        console.error('Failed to fetch service status:', err);
      }
    };

    fetchServiceStatus();

    // Poll every 5 seconds (same interval as daemon status)
    const interval = setInterval(fetchServiceStatus, 5000);

    return () => clearInterval(interval);
  }, []);

  // Fetch daemon config on mount (v4.2.0)
  // v4.3.1: Pre-populate default download folder if empty
  useEffect(() => {
    const fetchDaemonConfig = async () => {
      try {
        const cfg = await GetDaemonConfig();
        // Pre-populate default download folder if empty
        if (!cfg.downloadFolder) {
          const defaultFolder = await GetDefaultDownloadFolder();
          if (defaultFolder) {
            cfg.downloadFolder = defaultFolder;
          }
        }
        setDaemonConfig(cfg);
        // v4.5.8: Initialize lastSavedConfig when config loads
        setLastSavedConfig({ ...cfg });
        prevLookbackRef.current = cfg.lookbackDays;
      } catch (err) {
        console.error('Failed to fetch daemon config:', err);
      }
    };
    fetchDaemonConfig();
  }, []);

  // v4.5.8: Debounced auto-save for daemon config changes
  const debouncedSaveDaemonConfig = useCallback((config: wailsapp.DaemonConfigDTO) => {
    // Cancel any pending debounce
    if (debounceTimeoutRef.current) {
      clearTimeout(debounceTimeoutRef.current);
    }

    debounceTimeoutRef.current = setTimeout(async () => {
      try {
        setIsDaemonConfigSaving(true);
        await SaveDaemonConfig(config);
        setLastSavedConfig({ ...config });
        setStatusMessage('Settings saved');
      } catch (err) {
        setStatusMessage(`Failed to save settings: ${err}`);
      } finally {
        setIsDaemonConfigSaving(false);
      }
    }, 1000);
  }, []);

  // v4.5.8: Trigger debounced save when daemon config changes (but not on initial load)
  useEffect(() => {
    if (daemonConfig && lastSavedConfig) {
      const configChanged = JSON.stringify(daemonConfig) !== JSON.stringify(lastSavedConfig);
      if (configChanged) {
        debouncedSaveDaemonConfig(daemonConfig);
      }
    }
  }, [daemonConfig, lastSavedConfig, debouncedSaveDaemonConfig]);

  // v4.5.8: Trigger rescan when lookback increases significantly (more than doubled)
  useEffect(() => {
    if (daemonConfig?.lookbackDays && prevLookbackRef.current !== null) {
      const newLookback = daemonConfig.lookbackDays;
      const oldLookback = prevLookbackRef.current;

      // If lookback increased significantly and auto-download is enabled, trigger rescan
      if (newLookback > oldLookback * 2 && daemonConfig.enabled) {
        // Wait for debounced save to complete, then trigger rescan
        const triggerRescanAfterSave = async () => {
          try {
            await TriggerProfileRescan();
            setStatusMessage(`Lookback extended to ${newLookback} days. Scanning for older jobs...`);
          } catch {
            // Silent fail - rescan will happen on next poll
          }
        };

        // Delay rescan to ensure save completes first
        setTimeout(triggerRescanAfterSave, 1500);
      }
    }
    prevLookbackRef.current = daemonConfig?.lookbackDays ?? null;
  }, [daemonConfig?.lookbackDays, daemonConfig?.enabled]);

  // v4.5.8: Cleanup debounce timeout on unmount
  useEffect(() => {
    return () => {
      if (debounceTimeoutRef.current) {
        clearTimeout(debounceTimeoutRef.current);
      }
    };
  }, []);

  // v4.3.2: Fetch file logging settings on mount
  useEffect(() => {
    const fetchFileLoggingSettings = async () => {
      try {
        const settings = await GetFileLoggingSettings();
        setFileLoggingEnabled(settings.enabled);
        setLogFilePath(settings.filePath);
      } catch (err) {
        console.error('Failed to fetch file logging settings:', err);
      }
    };
    fetchFileLoggingSettings();
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

  // v4.5.1: Auto-switch proxy mode when selecting FRM platform with NTLM
  useEffect(() => {
    if (config && config.proxyMode === 'ntlm' && isFRMPlatform(config.apiBaseUrl || '')) {
      // NTLM is not allowed for FRM platforms - switch to basic
      updateConfig({ proxyMode: 'basic' });
      setStatusMessage('Proxy mode switched to "basic": NTLM uses non-FIPS algorithms not allowed for FedRAMP');
    }
  }, [config?.apiBaseUrl]);

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

  // v4.3.0: Unified save handler - saves all config sections at once
  const [isUnifiedSaving, setIsUnifiedSaving] = useState(false);

  // v4.3.1: Unified save handler - saves main config and daemon config
  const handleSaveAllSettings = async () => {
    try {
      setIsUnifiedSaving(true);
      setStatusMessage('Saving all settings...');

      // Save main config
      await saveConfig();

      // Save daemon config (includes all auto-download settings)
      if (daemonConfig) {
        await SaveDaemonConfig(daemonConfig);
      }

      setStatusMessage(`All settings saved${defaultConfigPath ? ` to ${defaultConfigPath}` : ''}`);
    } catch (err) {
      setStatusMessage(`Failed to save settings: ${err}`);
    } finally {
      setIsUnifiedSaving(false);
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

  // v4.3.1: Select download folder handler (uses daemon config)
  const handleSelectDownloadFolder = async () => {
    try {
      const path = await SelectDirectory('Select Download Folder');
      if (path && daemonConfig) {
        setDaemonConfig({ ...daemonConfig, downloadFolder: path });
      }
    } catch (err) {
      console.error('Failed to select folder:', err);
    }
  };

  // v4.3.1: Test connection handler (now takes folder from daemon config)
  const handleTestAutoDownloadConnection = async () => {
    setAutoDownloadTestStatus('testing');
    setAutoDownloadTestResult(null);
    await TestAutoDownloadConnection(daemonConfig?.downloadFolder || '');
  };

  // Daemon control handlers (v4.1.0)
  const refreshDaemonStatus = async () => {
    try {
      const status = await GetDaemonStatus();
      setDaemonStatus(status);
    } catch (err) {
      console.error('Failed to refresh daemon status:', err);
    }
  };

  // v4.3.2: Auto-save daemon config before starting to prevent stale settings
  const handleStartDaemon = async () => {
    try {
      setIsDaemonLoading(true);

      // Auto-save current settings before starting daemon
      if (daemonConfig) {
        setStatusMessage('Saving settings...');
        await SaveDaemonConfig(daemonConfig);
      }

      setStatusMessage('Starting daemon...');
      await StartDaemon();
      setStatusMessage('Daemon started successfully');
      await refreshDaemonStatus();
    } catch (err) {
      setStatusMessage(`Failed to start daemon: ${err}`);
    } finally {
      setIsDaemonLoading(false);
    }
  };

  const handleStopDaemon = async () => {
    try {
      setIsDaemonLoading(true);
      setStatusMessage('Stopping daemon...');
      await StopDaemon();
      setStatusMessage('Daemon stopped');
      await refreshDaemonStatus();
    } catch (err) {
      setStatusMessage(`Failed to stop daemon: ${err}`);
    } finally {
      setIsDaemonLoading(false);
    }
  };

  const handleTriggerScan = async () => {
    try {
      setStatusMessage('Triggering scan...');
      await TriggerDaemonScan();
      setStatusMessage('Scan triggered');
    } catch (err) {
      setStatusMessage(`Failed to trigger scan: ${err}`);
    }
  };

  const handlePauseDaemon = async () => {
    try {
      setIsDaemonLoading(true);
      await PauseDaemon();
      setStatusMessage('Daemon paused');
      await refreshDaemonStatus();
    } catch (err) {
      setStatusMessage(`Failed to pause daemon: ${err}`);
    } finally {
      setIsDaemonLoading(false);
    }
  };

  const handleResumeDaemon = async () => {
    try {
      setIsDaemonLoading(true);
      await ResumeDaemon();
      setStatusMessage('Daemon resumed');
      await refreshDaemonStatus();
    } catch (err) {
      setStatusMessage(`Failed to resume daemon: ${err}`);
    } finally {
      setIsDaemonLoading(false);
    }
  };

  // v4.5.8: Handler for Enable Auto-Download checkbox with auto-save and rollback on failure
  // Also cancels any pending debounced save to prevent race conditions
  const handleAutoDownloadToggle = async (checked: boolean) => {
    if (!daemonConfig) return;

    // v4.5.8: Cancel any pending debounced save
    if (debounceTimeoutRef.current) {
      clearTimeout(debounceTimeoutRef.current);
      debounceTimeoutRef.current = null;
    }

    const previousConfig = { ...daemonConfig };  // Save for rollback
    const newConfig = { ...daemonConfig, enabled: checked };

    // Optimistic update for responsive UI
    setDaemonConfig(newConfig);

    try {
      setIsDaemonConfigSaving(true);
      // Immediately save config to disk
      await SaveDaemonConfig(newConfig);
      // v4.5.8: Update lastSavedConfig to prevent debounced save from re-triggering
      setLastSavedConfig({ ...newConfig });

      if (checked) {
        // Notify service to rescan profiles so it picks up this user
        try {
          await TriggerProfileRescan();
          setStatusMessage('Auto-download enabled. Scanning for your jobs now...');
        } catch {
          // Service might not support rescan - that's OK, it will pick up on next 5-min cycle
          setStatusMessage('Auto-download enabled. Service will detect within 5 minutes.');
        }
      } else {
        setStatusMessage('Auto-download disabled. You will no longer receive automatic job downloads.');
      }

      // Refresh status to show updated user state
      await refreshDaemonStatus();
    } catch (err) {
      // Rollback checkbox state on failure
      setDaemonConfig(previousConfig);
      setStatusMessage(`Failed to save settings: ${err}. Checkbox reverted.`);
    } finally {
      setIsDaemonConfigSaving(false);
    }
  };

  // v4.5.6: Helper to check if user can perform actions (Scan/Pause/Resume)
  const canUserPerformActions = () => {
    // User must be configured AND registered with service
    return daemonStatus?.userState === 'running' || daemonStatus?.userState === 'paused';
  };

  // v4.5.6: Reason why buttons are disabled
  const getActionDisabledReason = () => {
    if (daemonStatus?.userState === 'not_configured') {
      return 'Enable auto-download first';
    }
    if (daemonStatus?.userState === 'pending') {
      return 'Waiting for service to detect your configuration...';
    }
    return '';
  };

  // v4.3.1: Daemon config changes now inline with setDaemonConfig
  // handleDaemonConfigChange removed as unused after UI refactor

  // v4.5.1: Elevated service control handlers
  const handleStartServiceElevated = async () => {
    try {
      setIsServiceLoading(true);
      setShowUACConfirmDialog(null);
      setStatusMessage('Starting Windows Service (UAC prompt will appear)...');

      const result = await StartServiceElevated();
      if (result.success) {
        setStatusMessage('Service start command executed. Waiting for service to start...');
        // Poll for status change
        let attempts = 0;
        const maxAttempts = 20; // 10 seconds at 500ms intervals
        const pollInterval = setInterval(async () => {
          attempts++;
          const status = await GetServiceStatus();
          setServiceStatus(status);
          if (status.running) {
            clearInterval(pollInterval);
            setStatusMessage('Windows Service started successfully');
            setIsServiceLoading(false);
            // Also refresh daemon status as it will now report Windows Service mode
            await refreshDaemonStatus();
          } else if (attempts >= maxAttempts) {
            clearInterval(pollInterval);
            setStatusMessage('Service may still be starting. Check status in a moment.');
            setIsServiceLoading(false);
          }
        }, 500);
      } else {
        setStatusMessage(`Failed to start service: ${result.error}`);
        setIsServiceLoading(false);
      }
    } catch (err) {
      setStatusMessage(`Failed to start service: ${err}`);
      setIsServiceLoading(false);
    }
  };

  const handleStopServiceElevated = async () => {
    try {
      setIsServiceLoading(true);
      setShowUACConfirmDialog(null);
      setStatusMessage('Stopping Windows Service (UAC prompt will appear)...');

      const result = await StopServiceElevated();
      if (result.success) {
        setStatusMessage('Service stop command executed. Waiting for service to stop...');
        // Poll for status change
        let attempts = 0;
        const maxAttempts = 20; // 10 seconds at 500ms intervals
        const pollInterval = setInterval(async () => {
          attempts++;
          const status = await GetServiceStatus();
          setServiceStatus(status);
          if (!status.running) {
            clearInterval(pollInterval);
            setStatusMessage('Windows Service stopped successfully');
            setIsServiceLoading(false);
            // Also refresh daemon status
            await refreshDaemonStatus();
          } else if (attempts >= maxAttempts) {
            clearInterval(pollInterval);
            setStatusMessage('Service may still be stopping. Check status in a moment.');
            setIsServiceLoading(false);
          }
        }, 500);
      } else {
        setStatusMessage(`Failed to stop service: ${result.error}`);
        setIsServiceLoading(false);
      }
    } catch (err) {
      setStatusMessage(`Failed to stop service: ${err}`);
      setIsServiceLoading(false);
    }
  };

  // Workspace validation handler (v4.2.1)
  const handleValidateWorkspace = async () => {
    try {
      setIsValidating(true);
      setStatusMessage('Validating workspace configuration...');
      const result = await ValidateAutoDownloadSetup();
      setWorkspaceValidation(result);
      if (result.hasAutoDownloadField) {
        setStatusMessage('Workspace validation successful');
      } else if (result.errors && result.errors.length > 0) {
        setStatusMessage(`Validation error: ${result.errors[0]}`);
      } else {
        setStatusMessage('Auto Download custom field not found in workspace');
      }
    } catch (err) {
      setStatusMessage(`Validation failed: ${err}`);
    } finally {
      setIsValidating(false);
    }
  };

  // v4.3.2: Toggle file logging
  const handleToggleFileLogging = async (enabled: boolean) => {
    try {
      await SetFileLoggingEnabled(enabled);
      setFileLoggingEnabled(enabled);
      if (enabled) {
        const settings = await GetFileLoggingSettings();
        setLogFilePath(settings.filePath);
        setStatusMessage(`File logging enabled: ${settings.filePath}`);
      } else {
        setStatusMessage('File logging disabled');
      }
    } catch (err) {
      setStatusMessage(`Failed to toggle file logging: ${err}`);
    }
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
  // v4.5.1: Check if current platform is FRM (NTLM not allowed for FIPS compliance)
  const isFRM = isFRMPlatform(config?.apiBaseUrl || '');

  // v4.5.8: Check if there are unsaved daemon config changes
  const hasUnsavedDaemonChanges = daemonConfig && lastSavedConfig
    ? JSON.stringify(daemonConfig) !== JSON.stringify(lastSavedConfig)
    : false;

  return (
    <div className="tab-panel flex flex-col h-full">
      {/* v4.3.0: Unified Action Bar - sticky at top */}
      {/* v4.5.7: Updated save button shows saved state */}
      <div className="sticky top-0 z-10 bg-white flex items-center gap-2 mb-4 pb-4 border-b border-gray-200">
        <button
          onClick={handleSaveAllSettings}
          disabled={isUnifiedSaving || isSaving || isDaemonConfigSaving}
          className={clsx(
            "btn-primary",
            !hasUnsavedDaemonChanges && !isDaemonConfigSaving && "bg-green-600 hover:bg-green-700"
          )}
          title={hasUnsavedDaemonChanges ? "Save all settings" : "All settings saved"}
        >
          {isDaemonConfigSaving ? (
            <span className="flex items-center gap-1">
              <ArrowPathIcon className="h-4 w-4 animate-spin" />
              Saving...
            </span>
          ) : hasUnsavedDaemonChanges ? (
            'Save All Settings'
          ) : (
            <span className="flex items-center gap-1">
              <CheckCircleIcon className="h-4 w-4" />
              Saved
            </span>
          )}
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
            {/* Platform URL - v4.3.0: Changed to dropdown */}
            <div>
              <label className="label">Platform Region</label>
              <select
                className="input"
                value={config?.apiBaseUrl || 'https://platform.rescale.com'}
                onChange={(e) => updateConfig({ apiBaseUrl: e.target.value })}
              >
                {PLATFORM_URLS.map((opt) => (
                  <option key={opt.value} value={opt.value}>{opt.label}</option>
                ))}
              </select>
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

        {/* v4.3.0: Collapsible Advanced Settings Section */}
        <div className="border border-gray-200 rounded-lg overflow-hidden">
          <button
            onClick={() => setAdvancedExpanded(!advancedExpanded)}
            className="w-full flex items-center justify-between px-4 py-3 bg-gray-50 hover:bg-gray-100 transition-colors"
          >
            <span className="text-base font-semibold text-gray-900">Advanced Settings</span>
            {advancedExpanded ? (
              <ChevronDownIcon className="w-5 h-5 text-gray-500" />
            ) : (
              <ChevronRightIcon className="w-5 h-5 text-gray-500" />
            )}
          </button>

          {advancedExpanded && (
          <div className="p-4 space-y-6">

        {/* Directory Scan Settings */}
        <div className="card">
          <h3 className="text-base font-semibold text-gray-900 mb-4">
            Directory Scan Settings
          </h3>
          <div className="space-y-4">
            <div>
              {/* v4.6.0: Clarified label from "Run Subpath" to "Scan Prefix" */}
              <label className="label">Scan Prefix</label>
              <input
                type="text"
                className="input"
                placeholder="Navigate into subpath before scanning (e.g., Simcodes/Powerflow)"
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

            {/* v4.3.2: File Logging */}
            <div className="flex items-center">
              <input
                type="checkbox"
                id="fileLoggingEnabled"
                checked={fileLoggingEnabled}
                onChange={(e) => handleToggleFileLogging(e.target.checked)}
                className="h-4 w-4 rounded border border-gray-300 text-rescale-blue focus:ring-rescale-blue focus:ring-2 bg-white cursor-pointer"
              />
              <label htmlFor="fileLoggingEnabled" className="ml-2 text-sm text-gray-700 cursor-pointer">
                Save logs to file
              </label>
            </div>
            {fileLoggingEnabled && logFilePath && (
              <p className="text-xs text-gray-500 ml-6">
                Logs saved to: {logFilePath}
              </p>
            )}
            <p className="text-xs text-gray-500">
              When enabled, all activity logs are also saved to a rotating log file for troubleshooting.
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
                onChange={(e) => {
                  // v4.5.1: Prevent selecting NTLM for FRM platforms
                  if (e.target.value === 'ntlm' && isFRM) {
                    setStatusMessage('NTLM is not available for FedRAMP platforms (non-FIPS algorithms)');
                    return;
                  }
                  updateConfig({ proxyMode: e.target.value });
                }}
              >
                {PROXY_MODES.map((mode) => (
                  <option
                    key={mode}
                    value={mode}
                    disabled={mode === 'ntlm' && isFRM}
                  >
                    {mode}{mode === 'ntlm' && isFRM ? ' (unavailable for FRM)' : ''}
                  </option>
                ))}
              </select>
              {/* v4.5.1: Warning when NTLM is disabled for FRM */}
              {isFRM && (
                <p className="mt-1 text-xs text-amber-600">
                  NTLM proxy mode is unavailable for FedRAMP platforms (uses non-FIPS MD4/MD5 algorithms).
                  Use 'basic' proxy mode over TLS instead.
                </p>
              )}
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
            {/* v4.5.9: No Proxy bypass list */}
            {proxyEnabled && (
              <div>
                <label className="label">No Proxy (bypass list)</label>
                <input
                  type="text"
                  className="input"
                  placeholder="*.example.com, 10.0.0.0/8, internal.corp"
                  value={config?.noProxy || ''}
                  onChange={(e) => updateConfig({ noProxy: e.target.value })}
                />
                <p className="mt-1 text-xs text-gray-500">
                  Comma-separated hosts/patterns that bypass the proxy.{' '}
                  <code>example.com</code> matches root + subdomains; <code>*.example.com</code> matches subdomains only.
                  Also supports IPs and CIDR ranges (e.g., <code>10.0.0.0/8</code>).
                </p>
              </div>
            )}
          </div>
        </div>

        {/* v4.3.1: Unified Auto-Download Section - merged settings, eligibility, and service */}
        <div className="card">
          <h3 className="text-base font-semibold text-gray-900 mb-4">Auto-Download</h3>
          <div className="space-y-4">
            {/* Info Banner explaining per-job mode - evergreen, no version refs */}
            <div className="p-3 rounded-md bg-blue-50 text-blue-800 text-sm">
              <p>
                Jobs are downloaded based on the <strong>"Auto Download"</strong> custom field in your Rescale workspace:
              </p>
              <ul className="mt-2 ml-4 list-disc text-xs space-y-1">
                <li><strong>Enabled</strong> - Job is always downloaded</li>
                <li><strong>Conditional</strong> - Job is downloaded only if it has the configured tag</li>
                <li><strong>Disabled</strong> - Job is never auto-downloaded</li>
              </ul>
              <p className="mt-2 text-xs text-blue-600">
                Required field: "Auto Download" (select type). Optional: "Auto Download Path" (per-job download location).
              </p>
            </div>

            {/* Enable Auto-Download - v4.5.6: Auto-save on toggle */}
            <div className="flex items-center">
              <input
                type="checkbox"
                id="autoDownloadEnabled"
                checked={daemonConfig?.enabled || false}
                onChange={(e) => handleAutoDownloadToggle(e.target.checked)}
                className="h-4 w-4 rounded border border-gray-300 text-rescale-blue focus:ring-rescale-blue focus:ring-2 bg-white cursor-pointer"
              />
              <label htmlFor="autoDownloadEnabled" className="ml-2 text-sm text-gray-700 cursor-pointer">
                Enable Auto-Download
              </label>
            </div>

            {/* Download Folder - v4.5.7: Editable before checkbox is checked */}
            <div>
              <label className="label">Download Folder</label>
              <div className="flex gap-2">
                <input
                  type="text"
                  className="input flex-1"
                  placeholder="/path/to/downloads"
                  value={daemonConfig?.downloadFolder || ''}
                  onChange={(e) => daemonConfig && setDaemonConfig({ ...daemonConfig, downloadFolder: e.target.value })}
                />
                <button
                  onClick={handleSelectDownloadFolder}
                  className="btn-secondary p-2"
                  title="Browse for folder"
                >
                  <FolderOpenIcon className="w-5 h-5" />
                </button>
              </div>
            </div>

            {/* Scan Interval and Lookback Days - v4.5.7: Editable before checkbox is checked */}
            <div className="grid grid-cols-2 gap-4">
              <div>
                <label className="label">Scan Interval (minutes)</label>
                <input
                  type="number"
                  min="1"
                  max="1440"
                  className="input"
                  value={daemonConfig?.pollIntervalMinutes || 5}
                  onChange={(e) => daemonConfig && setDaemonConfig({ ...daemonConfig, pollIntervalMinutes: parseInt(e.target.value) || 5 })}
                />
              </div>
              <div>
                <label className="label">Lookback Days</label>
                <input
                  type="number"
                  min="1"
                  max="365"
                  className="input"
                  value={daemonConfig?.lookbackDays || 7}
                  onChange={(e) => daemonConfig && setDaemonConfig({ ...daemonConfig, lookbackDays: parseInt(e.target.value) || 7 })}
                />
              </div>
            </div>

            {/* Tag for Conditional Jobs - v4.5.7: Editable before checkbox is checked */}
            <div>
              <label className="label">Tag for Conditional Jobs</label>
              <input
                type="text"
                className="input"
                placeholder="autoDownload"
                value={daemonConfig?.autoDownloadTag || ''}
                onChange={(e) => daemonConfig && setDaemonConfig({ ...daemonConfig, autoDownloadTag: e.target.value })}
              />
              <p className="mt-1 text-xs text-gray-500">
                Jobs with "Auto Download" set to "Conditional" must have this tag to be downloaded.
              </p>
            </div>

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

            {/* Buttons row */}
            <div className="flex items-center gap-4 pt-2">
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

            {/* Workspace Validation Status */}
            {workspaceValidation && (
              <div className={clsx(
                'p-3 rounded-md text-sm',
                workspaceValidation.hasAutoDownloadField ? 'bg-green-50 text-green-700' :
                workspaceValidation.errors && workspaceValidation.errors.length > 0 ? 'bg-red-50 text-red-700' :
                'bg-yellow-50 text-yellow-700'
              )}>
                <div className="flex items-center gap-2">
                  {workspaceValidation.hasAutoDownloadField ? (
                    <CheckCircleIcon className="w-5 h-5 text-green-500" />
                  ) : (
                    <XCircleIcon className="w-5 h-5 text-yellow-500" />
                  )}
                  <span className="font-medium">
                    {workspaceValidation.hasAutoDownloadField
                      ? 'Auto Download custom field found'
                      : 'Auto Download custom field not found'}
                  </span>
                </div>
                {workspaceValidation.hasAutoDownloadField && (
                  <div className="mt-2 text-xs space-y-1">
                    <p>Type: {workspaceValidation.autoDownloadFieldType || 'text'}</p>
                    <p>Section: {workspaceValidation.autoDownloadFieldSection || 'N/A'}</p>
                    {workspaceValidation.availableValues && workspaceValidation.availableValues.length > 0 && (
                      <p>Available values: {workspaceValidation.availableValues.join(', ')}</p>
                    )}
                  </div>
                )}
                {workspaceValidation.warnings && workspaceValidation.warnings.length > 0 && (
                  <div className="mt-2">
                    {workspaceValidation.warnings.map((w, i) => (
                      <p key={i} className="text-xs text-yellow-600">⚠️ {w}</p>
                    ))}
                  </div>
                )}
                {workspaceValidation.errors && workspaceValidation.errors.length > 0 && (
                  <div className="mt-2">
                    {workspaceValidation.errors.map((e, i) => (
                      <p key={i} className="text-xs text-red-600">❌ {e}</p>
                    ))}
                  </div>
                )}
              </div>
            )}

            {/* Validation Button */}
            <div className="flex items-center gap-4">
              <button
                onClick={handleValidateWorkspace}
                disabled={isValidating || connectionStatus !== 'connected'}
                className="btn-secondary"
                title={connectionStatus !== 'connected' ? 'Connect to API first' : 'Check workspace custom fields'}
              >
                {isValidating ? (
                  <span className="flex items-center gap-2">
                    <ArrowPathIcon className="w-4 h-4 animate-spin" />
                    Validating...
                  </span>
                ) : 'Validate Workspace Setup'}
              </button>
              <span className="text-xs text-gray-500">
                Check if your workspace has the required custom field
              </span>
            </div>

            {/* v4.5.1: Service Control Section - Windows Service lifecycle (SCM-based, requires UAC) */}
            {/* v4.5.2: Also show when SCM blocked but IPC indicates service mode */}
            {/* Show when Windows Service is installed OR when SCM blocked + IPC service mode */}
            {(serviceStatus?.installed || (serviceStatus?.scmBlocked && daemonStatus?.serviceMode)) && (
              <div className="border-t border-gray-200 pt-4 mt-4">
                <div className="flex items-center gap-2 mb-3">
                  <h4 className="text-sm font-medium text-gray-700">Service Control</h4>
                  <span className="text-xs bg-amber-100 text-amber-700 px-2 py-0.5 rounded flex items-center gap-1">
                    <ShieldCheckIcon className="w-3 h-3" />
                    Admin
                  </span>
                </div>
                {/* v4.5.2: Show SCM blocked warning when applicable */}
                {serviceStatus?.scmBlocked && (
                  <div className="mb-3 p-2 bg-amber-50 rounded text-xs text-amber-700">
                    SCM status unavailable (access denied). Run as administrator for full access.
                  </div>
                )}
                <div className={clsx(
                  'p-4 rounded-lg',
                  serviceStatus?.running ? 'bg-green-50' : 'bg-gray-50'
                )}>
                  <div className="flex items-center justify-between">
                    <div className="flex items-center gap-3">
                      <div className={clsx(
                        'w-3 h-3 rounded-full',
                        serviceStatus?.running ? 'bg-green-500' : 'bg-gray-400'
                      )} />
                      <div>
                        <div className="font-medium text-gray-900">
                          Status: {serviceStatus?.status || 'Unknown'}
                        </div>
                      </div>
                    </div>
                    <div className="flex items-center gap-2">
                      {!serviceStatus?.running ? (
                        <button
                          onClick={() => setShowUACConfirmDialog('start')}
                          disabled={isServiceLoading}
                          className="btn-primary text-sm flex items-center gap-1"
                          title="Start Windows Service (requires administrator privileges)"
                        >
                          <ShieldCheckIcon className="w-4 h-4" />
                          {isServiceLoading ? 'Starting...' : 'Start Service'}
                        </button>
                      ) : (
                        <button
                          onClick={() => setShowUACConfirmDialog('stop')}
                          disabled={isServiceLoading}
                          className="btn-secondary text-sm text-red-600 hover:text-red-700 flex items-center gap-1"
                          title="Stop Windows Service (requires administrator privileges)"
                        >
                          <ShieldCheckIcon className="w-4 h-4" />
                          {isServiceLoading ? 'Stopping...' : 'Stop Service (Admin)'}
                        </button>
                      )}
                    </div>
                  </div>
                </div>
                <p className="mt-2 text-xs text-gray-500">
                  These actions require administrator privileges. A Windows security prompt (UAC) will appear.
                </p>
              </div>
            )}

            {/* v4.5.6: My Downloads Section - Redesigned with separate service/user status */}
            {/* Show when daemon is running (IPC connected or service mode) */}
            {(daemonStatus?.ipcConnected || daemonStatus?.running) && (
              <div className="border-t border-gray-200 pt-4 mt-4">
                <h4 className="text-sm font-medium text-gray-700 mb-3">My Downloads</h4>

                {/* v4.5.6: Your Auto-Download Status (user-level) */}
                <div className={clsx(
                  'p-4 rounded-lg mb-4',
                  daemonStatus?.userState === 'running' ? 'bg-green-50' :
                  daemonStatus?.userState === 'not_configured' ? 'bg-yellow-50' :
                  daemonStatus?.userState === 'pending' ? 'bg-blue-50' :
                  daemonStatus?.userState === 'paused' ? 'bg-orange-50' :
                  'bg-gray-50'
                )}>
                  <div className="flex items-center justify-between">
                    <div className="flex items-center gap-3">
                      <div className={clsx(
                        'w-3 h-3 rounded-full',
                        daemonStatus?.userState === 'running' ? 'bg-green-500' :
                        daemonStatus?.userState === 'not_configured' ? 'bg-yellow-500' :
                        daemonStatus?.userState === 'pending' ? 'bg-blue-500 animate-pulse' :
                        daemonStatus?.userState === 'paused' ? 'bg-orange-500' :
                        'bg-gray-400'
                      )} />
                      <div>
                        <div className="font-medium text-gray-900">
                          {daemonStatus?.userState === 'running' ? 'Active' :
                           daemonStatus?.userState === 'not_configured' ? 'Setup Required' :
                           daemonStatus?.userState === 'pending' ? 'Activating...' :
                           daemonStatus?.userState === 'paused' ? 'Paused' :
                           'Unknown'}
                        </div>
                        {daemonStatus?.userState === 'running' && daemonStatus.jobsDownloaded > 0 && (
                          <div className="text-xs text-gray-500">
                            {daemonStatus.jobsDownloaded} job{daemonStatus.jobsDownloaded > 1 ? 's' : ''} downloaded
                          </div>
                        )}
                      </div>
                    </div>
                    <div className="flex items-center gap-2">
                      {/* v4.5.6: Disable buttons when user is not configured/registered */}
                      {canUserPerformActions() ? (
                        <>
                          {daemonStatus?.userState === 'paused' ? (
                            <button
                              onClick={handleResumeDaemon}
                              disabled={isDaemonLoading}
                              className="btn-secondary text-sm"
                            >
                              {isDaemonLoading ? 'Resuming...' : 'Resume'}
                            </button>
                          ) : (
                            <button
                              onClick={handlePauseDaemon}
                              disabled={isDaemonLoading}
                              className="btn-secondary text-sm"
                            >
                              {isDaemonLoading ? 'Pausing...' : 'Pause'}
                            </button>
                          )}
                          <button
                            onClick={handleTriggerScan}
                            disabled={isDaemonLoading || daemonStatus?.userState === 'paused'}
                            className="btn-outline text-sm"
                          >
                            Scan Now
                          </button>
                        </>
                      ) : (
                        <span className="text-xs text-gray-500">
                          {getActionDisabledReason()}
                        </span>
                      )}
                    </div>
                  </div>

                  {/* v4.5.6: Status-specific guidance messages */}
                  {daemonStatus?.userState === 'not_configured' && (
                    <div className="mt-3 p-2 bg-yellow-100 rounded border border-yellow-300">
                      <p className="text-sm font-medium text-yellow-800">
                        Action Required
                      </p>
                      <p className="text-xs text-yellow-700 mt-1">
                        To start receiving automatic job downloads:
                      </p>
                      <ol className="text-xs text-yellow-700 mt-1 list-decimal list-inside">
                        <li>Check "Enable Auto-Download" above</li>
                        <li>Set your download folder</li>
                        <li>Your jobs will start downloading automatically</li>
                      </ol>
                    </div>
                  )}

                  {daemonStatus?.userState === 'pending' && (
                    <div className="mt-3 p-2 bg-blue-100 rounded border border-blue-300">
                      <p className="text-sm font-medium text-blue-800 flex items-center gap-2">
                        <ArrowPathIcon className="w-4 h-4 animate-spin" /> Activating...
                      </p>
                      <p className="text-xs text-blue-700 mt-1">
                        The service is detecting your configuration. This usually takes a few seconds.
                      </p>
                    </div>
                  )}

                  {daemonStatus?.userState === 'running' && daemonStatus.jobsDownloaded === 0 && (
                    <div className="mt-3 p-2 bg-green-100 rounded border border-green-300">
                      <p className="text-sm font-medium text-green-800">
                        Auto-Download Active
                      </p>
                      <p className="text-xs text-green-700 mt-1">
                        Monitoring for completed jobs. Downloads will appear in your folder automatically.
                      </p>
                    </div>
                  )}

                  {daemonStatus?.userState === 'paused' && (
                    <div className="mt-3 p-2 bg-orange-100 rounded border border-orange-300">
                      <p className="text-sm font-medium text-orange-800">
                        Downloads Paused
                      </p>
                      <p className="text-xs text-orange-700 mt-1">
                        Click "Resume" to continue automatic downloads.
                      </p>
                    </div>
                  )}
                </div>

                {/* Service Details - only show when running */}
                {canUserPerformActions() && (
                  <div className="grid grid-cols-2 gap-4 text-sm p-3 bg-gray-50 rounded-lg">
                    <div>
                      <span className="text-gray-500">Uptime:</span>
                      <span className="ml-2 text-gray-900">{daemonStatus?.uptime || 'N/A'}</span>
                    </div>
                    <div>
                      <span className="text-gray-500">Version:</span>
                      <span className="ml-2 text-gray-900">{daemonStatus?.version || 'N/A'}</span>
                    </div>
                    <div>
                      <span className="text-gray-500">Active Downloads:</span>
                      <span className="ml-2 text-gray-900">{daemonStatus?.activeDownloads || 0}</span>
                    </div>
                    <div>
                      <span className="text-gray-500">Last Scan:</span>
                      <span className="ml-2 text-gray-900">
                        {daemonStatus?.lastScan ? new Date(daemonStatus.lastScan).toLocaleTimeString() : 'Never'}
                      </span>
                    </div>
                    {daemonStatus?.downloadFolder && (
                      <div className="col-span-2">
                        <span className="text-gray-500">Download Folder:</span>
                        <span className="ml-2 text-gray-900 break-all">{daemonStatus.downloadFolder}</span>
                      </div>
                    )}
                  </div>
                )}

                {/* Error message */}
                {daemonStatus?.error && (
                  <div className="mt-3 text-sm text-yellow-700">
                    <span className="font-medium">Note:</span> {daemonStatus.error}
                  </div>
                )}

                <p className="mt-2 text-xs text-gray-500">
                  These controls only affect your downloads. The service continues running for other users.
                </p>
              </div>
            )}

            {/* Subprocess mode: Show start button when service not installed and not running */}
            {!serviceStatus?.installed && !daemonStatus?.running && (
              <div className="border-t border-gray-200 pt-4 mt-4">
                <h4 className="text-sm font-medium text-gray-700 mb-3">Service Control</h4>
                <div className="p-4 rounded-lg bg-gray-50">
                  <div className="flex items-center justify-between">
                    <div className="flex items-center gap-3">
                      <div className="w-3 h-3 rounded-full bg-gray-400" />
                      <div className="font-medium text-gray-900">Stopped</div>
                    </div>
                    <div className="flex items-center gap-2">
                      {!daemonConfig?.enabled && (
                        <div className="text-sm text-amber-600 bg-amber-50 px-3 py-1 rounded-md mr-2">
                          Enable "Auto-Download" above to start.
                        </div>
                      )}
                      <button
                        onClick={handleStartDaemon}
                        disabled={isDaemonLoading || !daemonConfig?.enabled}
                        className={clsx(
                          "btn-primary text-sm",
                          !daemonConfig?.enabled && "opacity-50 cursor-not-allowed"
                        )}
                        title={!daemonConfig?.enabled ? 'Enable auto-download settings first' : 'Start auto-download service'}
                      >
                        {isDaemonLoading ? 'Starting...' : 'Start Service'}
                      </button>
                    </div>
                  </div>
                </div>
                <p className="mt-2 text-xs text-gray-500">
                  The auto-download service runs in the background and automatically downloads completed jobs.
                </p>
              </div>
            )}

            {/* Subprocess mode: Show controls when running in subprocess mode (no Windows Service) */}
            {!serviceStatus?.installed && daemonStatus?.running && (
              <div className="border-t border-gray-200 pt-4 mt-4">
                <h4 className="text-sm font-medium text-gray-700 mb-3">Service Control</h4>
                <div className={clsx(
                  'p-4 rounded-lg',
                  daemonStatus?.ipcConnected ? 'bg-green-50' : 'bg-yellow-50'
                )}>
                  <div className="flex items-center justify-between">
                    <div className="flex items-center gap-3">
                      <div className={clsx(
                        'w-3 h-3 rounded-full',
                        daemonStatus?.state === 'running' ? 'bg-green-500' :
                        daemonStatus?.state === 'paused' ? 'bg-yellow-500' :
                        'bg-gray-400'
                      )} />
                      <div>
                        <div className="font-medium text-gray-900">
                          {daemonStatus?.state === 'running' ? 'Running' :
                           daemonStatus?.state === 'paused' ? 'Paused' :
                           daemonStatus?.ipcConnected ? 'Running' : 'Running (IPC unavailable)'}
                        </div>
                        {daemonStatus?.pid > 0 && (
                          <div className="text-xs text-gray-500">PID: {daemonStatus.pid}</div>
                        )}
                      </div>
                    </div>
                    <div className="flex items-center gap-2">
                      {daemonStatus?.ipcConnected ? (
                        <>
                          {daemonStatus?.state === 'paused' ? (
                            <button
                              onClick={handleResumeDaemon}
                              disabled={isDaemonLoading}
                              className="btn-secondary text-sm"
                            >
                              Resume
                            </button>
                          ) : (
                            <button
                              onClick={handlePauseDaemon}
                              disabled={isDaemonLoading}
                              className="btn-secondary text-sm"
                            >
                              Pause My Downloads
                            </button>
                          )}
                          <button
                            onClick={handleTriggerScan}
                            disabled={isDaemonLoading || daemonStatus?.state === 'paused'}
                            className="btn-outline text-sm"
                          >
                            Scan My Downloads
                          </button>
                          <button
                            onClick={handleStopDaemon}
                            disabled={isDaemonLoading}
                            className="btn-secondary text-sm text-red-600 hover:text-red-700"
                          >
                            {isDaemonLoading ? 'Stopping...' : 'Stop'}
                          </button>
                        </>
                      ) : (
                        <span className="text-sm text-amber-600">
                          IPC unavailable - controls disabled
                        </span>
                      )}
                    </div>
                  </div>

                  {/* Service Details (when IPC available) */}
                  {daemonStatus?.ipcConnected && (
                    <div className="mt-4 pt-4 border-t border-gray-200 grid grid-cols-2 gap-4 text-sm">
                      <div>
                        <span className="text-gray-500">Uptime:</span>
                        <span className="ml-2 text-gray-900">{daemonStatus.uptime || 'N/A'}</span>
                      </div>
                      <div>
                        <span className="text-gray-500">Version:</span>
                        <span className="ml-2 text-gray-900">{daemonStatus.version || 'N/A'}</span>
                      </div>
                      <div>
                        <span className="text-gray-500">Jobs Downloaded:</span>
                        <span className="ml-2 text-gray-900">{daemonStatus.jobsDownloaded}</span>
                      </div>
                      <div>
                        <span className="text-gray-500">Active Downloads:</span>
                        <span className="ml-2 text-gray-900">{daemonStatus.activeDownloads}</span>
                      </div>
                    </div>
                  )}

                  {/* Error message */}
                  {daemonStatus?.error && (
                    <div className="mt-3 text-sm text-yellow-700">
                      <span className="font-medium">Note:</span> {daemonStatus.error}
                    </div>
                  )}
                </div>
              </div>
            )}

            {/* v4.5.1: UAC Confirmation Dialog */}
            {showUACConfirmDialog && (
              <div className="fixed inset-0 bg-black bg-opacity-50 flex items-center justify-center z-50">
                <div className="bg-white rounded-lg shadow-xl p-6 max-w-md mx-4">
                  <div className="flex items-center gap-3 mb-4">
                    <ShieldCheckIcon className="w-8 h-8 text-amber-500" />
                    <h3 className="text-lg font-semibold text-gray-900">
                      {showUACConfirmDialog === 'start' ? 'Start Service?' : 'Stop Service?'}
                    </h3>
                  </div>
                  <p className="text-gray-600 mb-6">
                    This will show a Windows security prompt (UAC) asking for administrator permission.
                  </p>
                  <div className="flex justify-end gap-3">
                    <button
                      onClick={() => setShowUACConfirmDialog(null)}
                      className="btn-secondary"
                    >
                      Cancel
                    </button>
                    <button
                      onClick={showUACConfirmDialog === 'start' ? handleStartServiceElevated : handleStopServiceElevated}
                      className="btn-primary flex items-center gap-2"
                    >
                      <ShieldCheckIcon className="w-4 h-4" />
                      Continue
                    </button>
                  </div>
                </div>
              </div>
            )}
          </div>
        </div> {/* End unified Auto-Download card */}

          </div>
          )} {/* End advancedExpanded conditional and inner div */}
        </div> {/* End collapsible container */}
      </div>

      {/* v4.5.7: Floating saving indicator */}
      {isDaemonConfigSaving && (
        <div className="fixed bottom-4 right-4 bg-rescale-blue text-white px-4 py-2 rounded-lg shadow-lg flex items-center gap-2 z-50">
          <ArrowPathIcon className="h-4 w-4 animate-spin" />
          <span className="text-sm">Saving...</span>
        </div>
      )}
    </div>
  );
}

export default SetupTab;
