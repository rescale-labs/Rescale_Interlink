// Package wailsapp provides the Wails-based GUI for Rescale Interlink.
// This replaces the Fyne-based GUI with a web-based frontend.
package wailsapp

import (
	"context"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/rs/zerolog"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/linux"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	"github.com/wailsapp/wails/v2/pkg/options/windows"

	"github.com/rescale/rescale-int/internal/cli"
	"github.com/rescale/rescale-int/internal/cloud"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/core"
	"github.com/rescale/rescale-int/internal/events"
	"github.com/rescale/rescale-int/internal/logging"
)

// Assets holds the embedded frontend files, passed in from main package.
var Assets embed.FS

var (
	// wailsLogger is the package-level logger for Wails mode
	wailsLogger *logging.Logger
)

// App is the main Wails application struct.
// All public methods are exposed to the frontend as callable functions.
type App struct {
	ctx    context.Context
	engine *core.Engine
	config *config.Config

	// Event bridge for forwarding EventBus events to frontend
	eventBridge *EventBridge

	// v4.0.0: Run cancellation function for active pipeline runs
	// v4.0.5: Protected by runMu to prevent race conditions
	runMu     sync.Mutex
	runCancel context.CancelFunc

	// v4.0.8: Caching for software/hardware catalogs to avoid repeated slow scans
	catalogCacheMu    sync.RWMutex
	cachedCoreTypes   []CoreTypeDTO
	cachedAnalyses    []AnalysisCodeDTO
	cachedAutomations []AutomationDTO
}

// NewApp creates a new Wails application instance.
func NewApp() *App {
	return &App{}
}

// v4.0.8: Unified logging helper that logs to BOTH terminal AND Activity Logs tab.
// This ensures users don't need to run from terminal to see detailed logs.
// Levels: "DEBUG", "INFO", "WARN", "ERROR"
// Messages are truncated to 1000 chars to prevent unbounded log growth.
const maxLogMessageLen = 1000

func (a *App) log(level string, stage string, message string) {
	// Truncate very long messages to prevent memory issues
	if len(message) > maxLogMessageLen {
		message = message[:maxLogMessageLen] + "... (truncated)"
	}

	// Always log to terminal for debugging
	fmt.Printf("[%s] %s: %s\n", level, stage, message)

	// Also emit to EventBus if available (appears in Activity Logs tab)
	if a.engine == nil {
		fmt.Println("  [log] WARNING: engine is nil, cannot emit to Activity Logs")
		return
	}
	if a.engine.Events() == nil {
		fmt.Println("  [log] WARNING: engine.Events() is nil, cannot emit to Activity Logs")
		return
	}

	var logLevel events.LogLevel
	switch level {
	case "DEBUG":
		logLevel = events.DebugLevel
	case "INFO":
		logLevel = events.InfoLevel
	case "WARN":
		logLevel = events.WarnLevel
	case "ERROR":
		logLevel = events.ErrorLevel
	default:
		logLevel = events.InfoLevel
	}
	a.engine.Events().PublishLog(logLevel, message, stage, "", nil)
}

// logDebug logs a debug-level message to terminal and Activity Log.
func (a *App) logDebug(stage, message string) {
	a.log("DEBUG", stage, message)
}

// logInfo logs an info-level message to terminal and Activity Log.
func (a *App) logInfo(stage, message string) {
	a.log("INFO", stage, message)
}

// logWarn logs a warning-level message to terminal and Activity Log.
func (a *App) logWarn(stage, message string) {
	a.log("WARN", stage, message)
}

// logError logs an error-level message to terminal and Activity Log.
func (a *App) logError(stage, message string) {
	a.log("ERROR", stage, message)
}

// ClearCatalogCache clears the cached software/hardware catalogs.
// v4.0.8: Called when API key changes to ensure fresh data from new account.
func (a *App) ClearCatalogCache() {
	a.catalogCacheMu.Lock()
	defer a.catalogCacheMu.Unlock()
	a.cachedCoreTypes = nil
	a.cachedAnalyses = nil
	a.cachedAutomations = nil
}

// startup is called when the app starts. The context is saved
// so we can call the Wails runtime methods.
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx

	// Initialize event bridge if engine exists
	if a.engine != nil {
		a.eventBridge = NewEventBridge(ctx, a.engine.Events())
		if err := a.eventBridge.Start(); err != nil {
			wailsLogger.Error().Err(err).Msg("Failed to start event bridge")
		}

		// v4.0.0: Set EventBus for timing infrastructure
		// This allows timing logs to appear in Activity tab when DetailedLogging is enabled
		cloud.SetEventBus(a.engine.Events())
	}

	// v4.0.0: Initialize detailed logging from config
	if a.config != nil {
		cloud.SetDetailedLogging(a.config.DetailedLogging)
	}

	wailsLogger.Info().Msg("Wails application started")
}

// domReady is called after the frontend DOM is ready.
func (a *App) domReady(ctx context.Context) {
	wailsLogger.Debug().Msg("Frontend DOM ready")
}

// beforeClose is called when the window close is requested.
// Return true to prevent closing.
func (a *App) beforeClose(ctx context.Context) bool {
	return false
}

// shutdown is called at application termination.
func (a *App) shutdown(ctx context.Context) {
	wailsLogger.Info().Msg("Wails application shutting down")

	if a.eventBridge != nil {
		a.eventBridge.Stop()
	}

	if a.engine != nil {
		a.engine.Stop()
	}
}

// Run launches the Wails GUI application.
func Run(args []string) error {
	// Initialize Wails logger
	wailsLogger = logging.NewLogger("wails", nil)

	// Set log level based on RESCALE_DEBUG environment variable
	if os.Getenv("RESCALE_DEBUG") != "" {
		logging.SetGlobalLevel(zerolog.DebugLevel)
		wailsLogger.Info().Msg("Debug logging enabled via RESCALE_DEBUG")
	} else {
		logging.SetGlobalLevel(zerolog.WarnLevel)
	}

	// Check for display on Linux
	if runtime.GOOS == "linux" {
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			return fmt.Errorf("GUI mode requires a display. No display detected.\n" +
				"DISPLAY and WAYLAND_DISPLAY are not set.\n" +
				"Use 'rescale-int' without --gui flag for CLI mode")
		}
	}

	// Load configuration
	cfg, err := loadConfiguration("")
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	// Create engine
	engine, err := core.NewEngine(cfg)
	if err != nil {
		return fmt.Errorf("failed to create engine: %w", err)
	}

	// Create application
	app := NewApp()
	app.engine = engine
	app.config = cfg

	// Window title
	windowTitle := fmt.Sprintf("Rescale Interlink %s", cli.Version)
	if cli.FIPSStatus() != "" {
		windowTitle += " " + cli.FIPSStatus()
	}

	// Create Wails application
	err = wails.Run(&options.App{
		Title:     windowTitle,
		Width:     1300,
		Height:    700,
		MinWidth:  800,
		MinHeight: 600,
		AssetServer: &assetserver.Options{
			Assets: Assets,
		},
		BackgroundColour: &options.RGBA{R: 248, G: 250, B: 252, A: 1}, // slate-50
		OnStartup:        app.startup,
		OnDomReady:       app.domReady,
		OnBeforeClose:    app.beforeClose,
		OnShutdown:       app.shutdown,
		Bind: []interface{}{
			app,
		},
		// Platform-specific options
		Mac: &mac.Options{
			TitleBar: &mac.TitleBar{
				TitlebarAppearsTransparent: false,
				HideTitle:                  false,
				HideTitleBar:               false,
				FullSizeContent:            false,
				UseToolbar:                 false,
			},
			About: &mac.AboutInfo{
				Title:   "Rescale Interlink",
				Message: fmt.Sprintf("Version %s\n\nUnified CLI and GUI for Rescale HPC platform.", cli.Version),
			},
		},
		Windows: &windows.Options{
			WebviewIsTransparent:              false,
			WindowIsTranslucent:               false,
			DisableWindowIcon:                 false,
			DisableFramelessWindowDecorations: false,
			WebviewUserDataPath:               "",
			// v4.0.0: Use bundled WebView2 Fixed Version Runtime if present
			// This allows running on Windows Server 2019 without installing WebView2 system-wide
			WebviewBrowserPath: getWebView2BrowserPath(),
		},
		Linux: &linux.Options{
			WindowIsTranslucent: false,
		},
	})

	if err != nil {
		return fmt.Errorf("wails application error: %w", err)
	}

	return nil
}

// loadConfiguration loads config from file or defaults.
func loadConfiguration(configFile string) (*config.Config, error) {
	var cfg *config.Config
	var err error

	if configFile != "" {
		cfg, err = config.LoadConfigCSV(configFile)
		if err != nil {
			wailsLogger.Warn().Err(err).Msg("Failed to load config, falling back to defaults")
			cfg, err = config.LoadConfigCSV("")
			if err != nil {
				return nil, fmt.Errorf("failed to create default config: %w", err)
			}
		} else {
			wailsLogger.Info().Str("path", configFile).Msg("Loaded configuration from specified file")
		}
	} else {
		// Try to auto-load from default location
		defaultConfigPath := config.GetDefaultConfigPath()
		if _, statErr := os.Stat(defaultConfigPath); statErr == nil {
			cfg, err = config.LoadConfigCSV(defaultConfigPath)
			if err != nil {
				wailsLogger.Warn().Err(err).Str("path", defaultConfigPath).Msg("Failed to load default config file, using defaults")
				cfg, err = config.LoadConfigCSV("")
				if err != nil {
					return nil, fmt.Errorf("failed to create default config: %w", err)
				}
			} else {
				wailsLogger.Info().Str("path", defaultConfigPath).Msg("Auto-loaded configuration from default location")
			}
		} else {
			cfg, err = config.LoadConfigCSV("")
			if err != nil {
				return nil, fmt.Errorf("failed to create default config: %w", err)
			}
		}
	}

	// Also try to load API key from default token file if not already set
	if cfg.APIKey == "" {
		defaultTokenPath := config.GetDefaultTokenPath()
		if tokenKey, tokenErr := config.ReadTokenFile(defaultTokenPath); tokenErr == nil && tokenKey != "" {
			cfg.APIKey = tokenKey
			wailsLogger.Info().Str("path", defaultTokenPath).Msg("Loaded API key from default token file")
		}
	}

	// v4.0.1: Also check RESCALE_API_KEY environment variable
	// This enables Linux AppDir and other environments where env vars are the primary config method
	if cfg.APIKey == "" {
		if envKey := os.Getenv("RESCALE_API_KEY"); envKey != "" {
			cfg.APIKey = envKey
			wailsLogger.Info().Msg("Loaded API key from RESCALE_API_KEY environment variable")
		}
	}

	return cfg, nil
}

// getWebView2BrowserPath returns the path to a bundled WebView2 Fixed Version Runtime.
// Returns empty string to use system-installed WebView2, or path to bundled runtime.
//
// v4.0.0: Enables running on Windows Server 2019 without system-wide WebView2 installation.
// The portable distribution includes webview2/ folder with Fixed Version Runtime.
func getWebView2BrowserPath() string {
	if runtime.GOOS != "windows" {
		return "" // Only relevant for Windows
	}

	// Get the directory of the current executable
	exePath, err := os.Executable()
	if err != nil {
		return "" // Fall back to system WebView2
	}

	exeDir := filepath.Dir(exePath)

	// Check for bundled WebView2 runtime in webview2/ folder
	webview2Dir := filepath.Join(exeDir, "webview2")
	if info, err := os.Stat(webview2Dir); err == nil && info.IsDir() {
		// Check if it contains the expected runtime files
		// The Fixed Version Runtime contains msedgewebview2.exe
		runtimeExe := filepath.Join(webview2Dir, "msedgewebview2.exe")
		if _, err := os.Stat(runtimeExe); err == nil {
			return webview2Dir // Use bundled runtime
		}
	}

	return "" // Use system WebView2
}
