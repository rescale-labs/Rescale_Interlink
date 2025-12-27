// Package gui provides the graphical user interface for rescale-int.
package gui

import (
	"context"
	"fmt"
	"net/http"
	_ "net/http/pprof"
	"os"
	"runtime"
	"sync/atomic"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"github.com/rs/zerolog"

	"github.com/rescale/rescale-int/internal/cli"
	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/core"
	"github.com/rescale/rescale-int/internal/events"
	"github.com/rescale/rescale-int/internal/logging"
	"github.com/rescale/rescale-int/internal/mesa"
	"github.com/rescale/rescale-int/internal/transfer"
)

var (
	// guiLogger is the package-level logger for GUI mode
	guiLogger *logging.Logger

	// v3.4.0: Package-level cancel function for monitorGoroutines shutdown
	cancelMonitorGoroutines context.CancelFunc
)

// LaunchGUI launches the full GUI application.
func LaunchGUI(configFile string) error {
	// Initialize GUI logger
	guiLogger = logging.NewLogger("gui", nil)

	// Set log level based on RESCALE_DEBUG environment variable
	// In GUI mode, default to WarnLevel for a cleaner console experience
	// Set RESCALE_DEBUG=1 to see debug/info messages
	if os.Getenv("RESCALE_DEBUG") != "" {
		logging.SetGlobalLevel(zerolog.DebugLevel)
		guiLogger.Info().Msg("Debug logging enabled via RESCALE_DEBUG")

		// Enable profiling on localhost:6060 (debug mode only)
		runtime.SetBlockProfileRate(1)
		go func() {
			// v3.4.0: Panic recovery for debug server
			defer func() {
				if r := recover(); r != nil {
					guiLogger.Error().Msgf("PANIC in pprof server: %v", r)
				}
			}()
			guiLogger.Debug().Msg("[PROFILING] pprof server listening on http://localhost:6060")
			if err := http.ListenAndServe("localhost:6060", nil); err != nil {
				guiLogger.Error().Err(err).Msg("[PROFILING] pprof server failed")
			}
		}()
	} else {
		logging.SetGlobalLevel(zerolog.WarnLevel) // Only show warnings and errors in GUI mode
	}

	// Check for display on Linux
	if runtime.GOOS == "linux" {
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			return fmt.Errorf("GUI mode requires a display. No display detected.\n" +
				"DISPLAY and WAYLAND_DISPLAY are not set.\n" +
				"Use 'rescale-int' without --gui flag for CLI mode")
		}

		// Force X11 backend on Wayland for consistent window decorations
		// RHEL 9+ defaults to Wayland which can cause missing minimize/maximize buttons
		// User can opt-in to native Wayland with RESCALE_USE_WAYLAND=1
		if os.Getenv("WAYLAND_DISPLAY") != "" && os.Getenv("RESCALE_USE_WAYLAND") == "" {
			guiLogger.Info().Msg("Wayland detected - forcing X11 backend for consistent window decorations")
			guiLogger.Info().Msg("Set RESCALE_USE_WAYLAND=1 to use native Wayland support")
			os.Unsetenv("WAYLAND_DISPLAY")
			// Ensure DISPLAY is set for X11 fallback
			if os.Getenv("DISPLAY") == "" {
				os.Setenv("DISPLAY", ":0")
			}
		}
	}

	// Windows: Set up Mesa software rendering for maximum compatibility
	// Works on VMs, RDP, and systems without GPU drivers
	// Mesa DLLs are embedded and extracted to %LOCALAPPDATA%\rescale-int\mesa
	// Set RESCALE_HARDWARE_RENDER=1 to use GPU acceleration instead
	// IMPORTANT: Must be called BEFORE Fyne/OpenGL initialization (app.NewWithID)
	if err := mesa.EnsureSoftwareRendering(); err != nil {
		guiLogger.Warn().Err(err).Msg("Mesa software rendering setup failed - GPU may be required")
	} else if mesa.IsSoftwareRenderingEnabled() {
		guiLogger.Info().Msg("Using Mesa software rendering (set RESCALE_HARDWARE_RENDER=1 for GPU)")
	}

	// Start goroutine monitoring with context for clean shutdown
	monitorCtx, monitorCancel := context.WithCancel(context.Background())
	cancelMonitorGoroutines = monitorCancel
	go monitorGoroutinesWithContext(monitorCtx)

	// Create Fyne app
	myApp := app.NewWithID("com.rescale.interlink")
	myApp.Settings().SetTheme(&rescaleTheme{})

	// Create main window with version in title
	windowTitle := fmt.Sprintf("Rescale Interlink %s", cli.Version)
	if cli.FIPSStatus() != "" {
		windowTitle += " " + cli.FIPSStatus()
	}
	mainWindow := myApp.NewWindow(windowTitle)
	mainWindow.SetMaster()

	// Initialize engine
	var cfg *config.Config
	var err error

	if configFile != "" {
		// Explicit config file specified
		cfg, err = config.LoadConfigCSV(configFile)
		if err != nil {
			guiLogger.Warn().Err(err).Msg("Failed to load config, falling back to defaults")
			// Fall back to defaults
			cfg, err = config.LoadConfigCSV("")
			if err != nil {
				return fmt.Errorf("failed to create default config: %w", err)
			}
		} else {
			guiLogger.Info().Str("path", configFile).Msg("Loaded configuration from specified file")
		}
	} else {
		// Try to auto-load from default location (~/.config/rescale-int/config.csv)
		defaultConfigPath := config.GetDefaultConfigPath()
		if _, statErr := os.Stat(defaultConfigPath); statErr == nil {
			cfg, err = config.LoadConfigCSV(defaultConfigPath)
			if err != nil {
				guiLogger.Warn().Err(err).Str("path", defaultConfigPath).Msg("Failed to load default config file, using defaults")
				cfg, err = config.LoadConfigCSV("")
				if err != nil {
					return fmt.Errorf("failed to create default config: %w", err)
				}
			} else {
				guiLogger.Info().Str("path", defaultConfigPath).Msg("Auto-loaded configuration from default location")
			}
		} else {
			// No default config exists, use defaults
			cfg, err = config.LoadConfigCSV("")
			if err != nil {
				return fmt.Errorf("failed to create default config: %w", err)
			}
		}
	}

	// Also try to load API key from default token file if not already set
	if cfg.APIKey == "" {
		defaultTokenPath := config.GetDefaultTokenPath()
		if tokenKey, tokenErr := config.ReadTokenFile(defaultTokenPath); tokenErr == nil && tokenKey != "" {
			cfg.APIKey = tokenKey
			guiLogger.Info().Str("path", defaultTokenPath).Msg("Loaded API key from default token file")
		}
	}

	engine, err := core.NewEngine(cfg)
	if err != nil {
		return fmt.Errorf("failed to create engine: %w", err)
	}

	// Create UI
	ui := NewUI(engine, mainWindow, myApp)

	// Start event listeners
	ui.Start()

	// Set window content
	mainWindow.SetContent(ui.Build())
	mainWindow.Resize(fyne.NewSize(1300, 700))
	mainWindow.CenterOnScreen()

	// Handle close
	mainWindow.SetOnClosed(func() {
		ui.Stop()
	})

	// Show and run
	mainWindow.ShowAndRun()

	// v3.4.0: Clean shutdown of monitorGoroutines when GUI exits
	if cancelMonitorGoroutines != nil {
		cancelMonitorGoroutines()
	}

	return nil
}

// UI represents the main user interface
type UI struct {
	engine         *core.Engine
	window         fyne.Window
	app            fyne.App
	setupTab       *SetupTab
	singleJobTab   *SingleJobTab   // Single job submission (v2.7.1)
	jobsTab        *JobsTab
	fileBrowserTab *FileBrowserTab
	transfersTab   *TransfersTab   // v3.6.3: Transfer queue tab
	activityTab    *ActivityTab
	transferQueue  *transfer.Queue // v3.6.3: Shared transfer queue
	tabs           *container.AppTabs // v3.6.3: Tab container for programmatic switching
	ctx            context.Context
	cancel         context.CancelFunc
}

// NewUI creates a new UI instance
func NewUI(engine *core.Engine, window fyne.Window, app fyne.App) *UI {
	ctx, cancel := context.WithCancel(context.Background())

	ui := &UI{
		engine: engine,
		window: window,
		app:    app,
		ctx:    ctx,
		cancel: cancel,
	}

	ui.setupTab = NewSetupTab(engine, window)
	ui.jobsTab = NewJobsTab(engine, window, app)
	// Single Job tab shares APICache with Jobs tab for efficiency
	ui.singleJobTab = NewSingleJobTab(engine, window, app, ui.jobsTab.apiCache)
	ui.fileBrowserTab = NewFileBrowserTab(engine, window)

	// v3.6.3: Transfer queue enabled - observer pattern (tracks transfers, doesn't execute them)
	ui.transferQueue = transfer.NewQueue(engine.Events())
	ui.transfersTab = NewTransfersTab(ui.transferQueue, engine.Events(), window)

	ui.activityTab = NewActivityTab(engine, window)

	return ui
}

// Tab indices for programmatic switching
const (
	tabIndexSetup      = 0
	tabIndexSingleJob  = 1
	tabIndexPUR        = 2
	tabIndexFileBrowser = 3
	tabIndexTransfers  = 4
	tabIndexActivity   = 5
)

// Build creates the UI layout
func (ui *UI) Build() fyne.CanvasObject {
	// Use tabs with icons for better visual identification
	// Tab order: Setup | Single Job | PUR | File Browser | Transfers | Activity
	// Note: Extra spaces in names provide visual separation between tabs (Fyne limitation)
	ui.tabs = container.NewAppTabs(
		container.NewTabItemWithIcon("    Setup    ", theme.SettingsIcon(), ui.setupTab.Build()),
		container.NewTabItemWithIcon("    Single Job    ", theme.ComputerIcon(), ui.singleJobTab.Build()),
		container.NewTabItemWithIcon("    PUR (Multiple Jobs)    ", theme.ComputerIcon(), ui.jobsTab.Build()),
		container.NewTabItemWithIcon("    File Browser    ", theme.DocumentIcon(), ui.fileBrowserTab.Build()),
		container.NewTabItemWithIcon("    Transfers    ", theme.DownloadIcon(), ui.transfersTab.Build()),
		container.NewTabItemWithIcon("    Activity    ", theme.InfoIcon(), ui.activityTab.Build()),
	)

	// v3.6.3: Wire up transfer queue to file browser
	ui.fileBrowserTab.SetTransferQueue(ui.transferQueue)
	// Auto-switch to Transfers tab when a transfer starts
	ui.fileBrowserTab.SetOnTransferQueued(func() {
		fyne.Do(func() {
			ui.tabs.SelectIndex(tabIndexTransfers)
		})
	})

	// Track previous tab to detect when leaving Setup tab
	var previousTabIndex int

	// Auto-apply config when navigating away from Setup tab (index 0)
	// Also force refresh on Linux to work around rendering issues (RHEL/CentOS 8+)
	ui.tabs.OnSelected = func(tab *container.TabItem) {
		currentIndex := ui.tabs.SelectedIndex()
		if previousTabIndex == 0 && currentIndex != 0 {
			// Leaving Setup tab - auto-apply configuration
			// v3.4.0: Run in background goroutine to prevent GUI freeze during proxy warmup
			go func() {
				defer func() {
					if r := recover(); r != nil {
						guiLogger.Error().Msgf("PANIC in auto-apply config: %v", r)
					}
				}()
				if err := ui.setupTab.ApplyConfig(); err != nil {
					guiLogger.Warn().Err(err).Msg("Auto-apply config failed when leaving Setup tab")
					// Don't show error dialog - just log it. User can fix and re-apply manually.
				} else {
					guiLogger.Debug().Msg("Auto-applied config when leaving Setup tab")
				}
			}()
		}
		previousTabIndex = currentIndex

		// Linux workaround: force refresh on tab change to fix rendering issues
		// Some Linux systems (RHEL/CentOS 8+) don't properly redraw tabs
		if runtime.GOOS == "linux" {
			ui.tabs.Refresh()
		}
	}

	// Select Setup tab by default (index 0)
	ui.tabs.SelectIndex(0)

	// Create header bar with logos on sides and tabs in center
	headerWithTabs := ui.buildHeaderWithTabs(ui.tabs)

	return headerWithTabs
}

// buildHeaderWithTabs creates a layout with logos on the left, above the tab bar
// Layout: [Logo1 Logo2 centered] on top, then [AppTabs] below
func (ui *UI) buildHeaderWithTabs(tabs *container.AppTabs) fyne.CanvasObject {
	// Logo 1 (Rescale with text) - compact horizontal logo
	logo1 := canvas.NewImageFromResource(LogoLeft1())
	logo1.FillMode = canvas.ImageFillContain
	logo1.SetMinSize(fyne.NewSize(130, 40)) // Reduced from 200x60

	// Logo 2 (Interlink) - proportionally smaller
	logo2 := canvas.NewImageFromResource(LogoLeft2())
	logo2.FillMode = canvas.ImageFillContain
	logo2.SetMinSize(fyne.NewSize(180, 50)) // Reduced from 280x80

	// Logos side by side, centered, with minimal spacing
	logosRow := container.NewHBox(
		logo1,
		HorizontalSpacer(2), // Minimal spacing between logos
		logo2,
	)

	// Center the logos
	centeredLogos := container.NewCenter(logosRow)

	// Combine: centered logos on top (minimal vertical space), tabs below
	return container.NewBorder(
		container.NewVBox(
			VerticalSpacer(2), // Reduced from 4
			centeredLogos,
			// No additional vertical spacer - go straight to tabs
		),
		nil, nil, nil,
		tabs, // AppTabs takes full width below header
	)
}

// Start begins event monitoring
func (ui *UI) Start() {
	go ui.monitorProgress()
	go ui.monitorLogs()
	go ui.monitorStateChanges()

	// v3.6.3: Start transfer queue observer (tracks transfers, doesn't execute them)
	ui.setupQueueObserver()
	if ui.transfersTab != nil {
		ui.transfersTab.Start()
	}
}

// setupQueueObserver configures the observer queue for transfer tracking.
// v3.6.3: Queue is now an OBSERVER that tracks transfers, not an executor.
// The actual transfer execution happens in executeUploadConcurrent/executeDownloadConcurrent.
// This function will be called when queue integration is enabled.
func (ui *UI) setupQueueObserver() {
	if ui.transferQueue == nil {
		return
	}
	// Queue is ready to track transfers - no handlers needed
	// FileBrowserTab will register as RetryExecutor when queue integration is enabled
	guiLogger.Debug().Msg("Transfer queue observer configured")
}

// Stop stops event monitoring
func (ui *UI) Stop() {
	ui.cancel()

	// v3.6.3: Stop transfers tab event subscription
	if ui.transfersTab != nil {
		ui.transfersTab.Stop()
	}

	ui.engine.Stop()
}

func (ui *UI) monitorProgress() {
	// v3.4.0 fix: Add panic recovery to prevent GUI freezes if event processing panics
	defer func() {
		if r := recover(); r != nil {
			guiLogger.Error().Msgf("PANIC in monitorProgress: %v", r)
		}
	}()

	ch := ui.engine.Events().Subscribe(events.EventProgress)

	// Read events and call update methods. Note: The called methods (UpdateProgress,
	// UpdateOverallProgress, AddLog) handle thread safety internally via fyne.Do().
	for {
		select {
		case event, ok := <-ch:
			if !ok {
				return
			}
			progress := event.(*events.ProgressEvent)

			// These methods internally use fyne.Do() to ensure thread-safe UI updates
			ui.jobsTab.UpdateProgress(progress)
			ui.activityTab.UpdateOverallProgress(progress)

		case <-ui.ctx.Done():
			return
		}
	}
}

func (ui *UI) monitorLogs() {
	// v3.4.0 fix: Add panic recovery to prevent GUI freezes if event processing panics
	defer func() {
		if r := recover(); r != nil {
			guiLogger.Error().Msgf("PANIC in monitorLogs: %v", r)
		}
	}()

	ch := ui.engine.Events().Subscribe(events.EventLog)

	// Read events and call AddLog. Note: AddLog handles thread safety internally via fyne.Do().
	for {
		select {
		case event, ok := <-ch:
			if !ok {
				return
			}
			logEvent := event.(*events.LogEvent)
			ui.activityTab.AddLog(logEvent) // AddLog internally uses fyne.Do()

		case <-ui.ctx.Done():
			return
		}
	}
}

func (ui *UI) monitorStateChanges() {
	// v3.4.0 fix: Add panic recovery to prevent GUI freezes if event processing panics
	defer func() {
		if r := recover(); r != nil {
			guiLogger.Error().Msgf("PANIC in monitorStateChanges: %v", r)
		}
	}()

	ch := ui.engine.Events().Subscribe(events.EventStateChange)

	// Read events and update UI directly
	for {
		select {
		case event, ok := <-ch:
			if !ok {
				return
			}
			stateEvent := event.(*events.StateChangeEvent)
			ui.jobsTab.UpdateJobState(stateEvent)
			// Activity tab doesn't need state updates, only progress and logs

		case <-ui.ctx.Done():
			return
		}
	}
}

// Goroutine monitoring (from original main.go)
var goroutineCount int64

// monitorGoroutinesWithContext monitors goroutine count with clean shutdown support
// v3.4.0: Added context parameter for clean shutdown when GUI exits
func monitorGoroutinesWithContext(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// v3.4.0: Clean exit when GUI shuts down
			return
		case <-ticker.C:
			count := runtime.NumGoroutine()
			prev := atomic.SwapInt64(&goroutineCount, int64(count))
			delta := int64(count) - prev

			// Use Debug level instead of Info to reduce console spam
			guiLogger.Debug().
				Int("count", count).
				Int64("delta", delta).
				Msg("[MONITOR] Goroutines")

			// Alert if count is high or growing rapidly (keep these as warnings)
			if count > 100 {
				guiLogger.Warn().
					Int("count", count).
					Msg("[MONITOR] High goroutine count")
			}

			if prev > 0 && delta > 20 {
				guiLogger.Warn().
					Int64("delta", delta).
					Msg("[MONITOR] Rapid goroutine growth")
			}
		}
	}
}
