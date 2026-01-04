import { create } from 'zustand';
import { wailsapp } from '../../wailsjs/go/models';
import * as App from '../../wailsjs/go/wailsapp/App';
import { EventsOn, EventsOff } from '../../wailsjs/runtime/runtime';
import type { ConnectionResultDTO } from '../types';

interface ConfigState {
  // Data
  config: wailsapp.ConfigDTO | null;
  appInfo: wailsapp.AppInfoDTO | null;

  // Connection state
  connectionStatus: 'unknown' | 'testing' | 'connected' | 'failed';
  connectionEmail: string | null;
  connectionFullName: string | null;
  workspaceId: string | null;
  workspaceName: string | null;
  connectionError: string | null;
  lastConnectionTest: Date | null;

  // Loading states
  isLoading: boolean;
  isSaving: boolean;
  error: string | null;

  // Actions
  fetchConfig: () => Promise<void>;
  fetchAppInfo: () => Promise<void>;
  updateConfig: (updates: Partial<wailsapp.ConfigDTO>) => void;
  saveConfig: () => Promise<void>;
  loadConfigFromFile: (path: string) => Promise<void>;
  testConnection: () => Promise<void>;
  selectDirectory: (title: string) => Promise<string>;
  selectFile: (title: string) => Promise<string>;

  // Internal
  setupEventListeners: () => () => void;
}

export const useConfigStore = create<ConfigState>((set, get) => ({
  // Initial state
  config: null,
  appInfo: null,
  connectionStatus: 'unknown',
  connectionEmail: null,
  connectionFullName: null,
  workspaceId: null,
  workspaceName: null,
  connectionError: null,
  lastConnectionTest: null,
  isLoading: false,
  isSaving: false,
  error: null,

  fetchConfig: async () => {
    set({ isLoading: true, error: null });
    try {
      const config = await App.GetConfig();
      set({ config, isLoading: false });
    } catch (err) {
      set({
        error: err instanceof Error ? err.message : String(err),
        isLoading: false
      });
    }
  },

  fetchAppInfo: async () => {
    try {
      const appInfo = await App.GetAppInfo();
      set({ appInfo });
    } catch (err) {
      console.error('Failed to fetch app info:', err);
    }
  },

  updateConfig: (updates) => {
    const { config } = get();
    if (!config) return;

    // Create a new config with updates
    const newConfig = new wailsapp.ConfigDTO({
      ...config,
      ...updates,
    });

    set({ config: newConfig });
  },

  saveConfig: async () => {
    const { config } = get();
    if (!config) return;

    set({ isSaving: true, error: null });
    try {
      await App.UpdateConfig(config);
      await App.SaveConfig();
      set({ isSaving: false });
    } catch (err) {
      set({
        error: err instanceof Error ? err.message : String(err),
        isSaving: false
      });
      throw err;
    }
  },

  loadConfigFromFile: async (path: string) => {
    set({ isLoading: true, error: null });
    try {
      await App.LoadConfigFromPath(path);
      await get().fetchConfig();
    } catch (err) {
      set({
        error: err instanceof Error ? err.message : String(err),
        isLoading: false
      });
      throw err;
    }
  },

  testConnection: async () => {
    set({
      connectionStatus: 'testing',
      connectionError: null,
    });

    try {
      // First update the config in Go
      const { config } = get();
      if (config) {
        await App.UpdateConfig(config);
      }

      // v4.0.8: TestConnection now returns result directly (synchronous call)
      const result = await App.TestConnection();

      if (result.success) {
        set({
          connectionStatus: 'connected',
          connectionEmail: result.email || null,
          connectionFullName: result.fullName || null,
          workspaceId: result.workspaceId || null,
          workspaceName: result.workspaceName || null,
          connectionError: null,
          lastConnectionTest: new Date(),
        });
      } else {
        set({
          connectionStatus: 'failed',
          connectionEmail: null,
          connectionFullName: null,
          workspaceId: null,
          workspaceName: null,
          connectionError: result.error || 'Connection failed',
          lastConnectionTest: new Date(),
        });
      }
    } catch (err) {
      set({
        connectionStatus: 'failed',
        connectionError: err instanceof Error ? err.message : String(err),
        lastConnectionTest: new Date(),
      });
    }
  },

  selectDirectory: async (title: string) => {
    return App.SelectDirectory(title);
  },

  selectFile: async (title: string) => {
    return App.SelectFile(title);
  },

  setupEventListeners: () => {
    const handleConnectionResult = (result: ConnectionResultDTO) => {
      if (result.success) {
        set({
          connectionStatus: 'connected',
          connectionEmail: result.email || null,
          connectionFullName: result.fullName || null,
          workspaceId: result.workspaceId || null,
          workspaceName: result.workspaceName || null,
          connectionError: null,
          lastConnectionTest: new Date(),
        });
      } else {
        set({
          connectionStatus: 'failed',
          connectionEmail: null,
          connectionFullName: null,
          workspaceId: null,
          workspaceName: null,
          connectionError: result.error || 'Connection failed',
          lastConnectionTest: new Date(),
        });
      }
    };

    EventsOn('interlink:connection_result', handleConnectionResult);

    // Return cleanup function
    return () => {
      EventsOff('interlink:connection_result');
    };
  },
}));
