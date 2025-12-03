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

	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/core"
	"github.com/rescale/rescale-int/internal/events"
	"github.com/rescale/rescale-int/internal/logging"
)

var (
	// guiLogger is the package-level logger for GUI mode
	guiLogger *logging.Logger
)

// LaunchGUI launches the full GUI application.
// This is the actual implementation that will replace the stub in launcher.go.
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
	}

	// Start goroutine monitoring
	go monitorGoroutines()

	// Create Fyne app
	myApp := app.NewWithID("com.rescale.interlink")
	myApp.Settings().SetTheme(&rescaleTheme{})

	// Create main window
	mainWindow := myApp.NewWindow("Rescale Interlink")
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
	activityTab    *ActivityTab
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
	ui.activityTab = NewActivityTab(engine, window)

	return ui
}

// Build creates the UI layout
func (ui *UI) Build() fyne.CanvasObject {
	// Use tabs with icons for better visual identification
	// Tab order: Setup | Single Job | PUR | File Browser | Activity
	// Note: Extra spaces in names provide visual separation between tabs (Fyne limitation)
	tabs := container.NewAppTabs(
		container.NewTabItemWithIcon("    Setup    ", theme.SettingsIcon(), ui.setupTab.Build()),
		container.NewTabItemWithIcon("    Single Job    ", theme.ComputerIcon(), ui.singleJobTab.Build()),
		container.NewTabItemWithIcon("    PUR (Multiple Jobs)    ", theme.ComputerIcon(), ui.jobsTab.Build()),
		container.NewTabItemWithIcon("    File Browser    ", theme.DocumentIcon(), ui.fileBrowserTab.Build()),
		container.NewTabItemWithIcon("    Activity    ", theme.InfoIcon(), ui.activityTab.Build()),
	)

	// Track previous tab to detect when leaving Setup tab
	var previousTabIndex int

	// Auto-apply config when navigating away from Setup tab (index 0)
	tabs.OnSelected = func(tab *container.TabItem) {
		currentIndex := tabs.SelectedIndex()
		if previousTabIndex == 0 && currentIndex != 0 {
			// Leaving Setup tab - auto-apply configuration
			if err := ui.setupTab.ApplyConfig(); err != nil {
				guiLogger.Warn().Err(err).Msg("Auto-apply config failed when leaving Setup tab")
				// Don't show error dialog - just log it. User can fix and re-apply manually.
			} else {
				guiLogger.Debug().Msg("Auto-applied config when leaving Setup tab")
			}
		}
		previousTabIndex = currentIndex
	}

	// Select Setup tab by default (index 0)
	tabs.SelectIndex(0)

	// Create header bar with logos on sides and tabs in center
	headerWithTabs := ui.buildHeaderWithTabs(tabs)

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
}

// Stop stops event monitoring
func (ui *UI) Stop() {
	ui.cancel()
	ui.engine.Stop()
}

func (ui *UI) monitorProgress() {
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

func monitorGoroutines() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
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
