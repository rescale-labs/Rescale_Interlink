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
	"fyne.io/fyne/v2/container"
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
	if os.Getenv("RESCALE_DEBUG") != "" {
		logging.SetGlobalLevel(zerolog.DebugLevel)
		guiLogger.Info().Msg("Debug logging enabled via RESCALE_DEBUG")
	} else {
		logging.SetGlobalLevel(zerolog.InfoLevel)
	}

	// Check for display on Linux
	if runtime.GOOS == "linux" {
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			return fmt.Errorf("GUI mode requires a display. No display detected.\n" +
				"DISPLAY and WAYLAND_DISPLAY are not set.\n" +
				"Use 'rescale-int' without --gui flag for CLI mode")
		}
	}

	// Enable profiling on localhost:6060
	runtime.SetBlockProfileRate(1)
	go func() {
		guiLogger.Info().Msg("[PROFILING] pprof server listening on http://localhost:6060")
		if err := http.ListenAndServe("localhost:6060", nil); err != nil {
			guiLogger.Error().Err(err).Msg("[PROFILING] pprof server failed")
		}
	}()

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
		cfg, err = config.LoadConfigCSV(configFile)
		if err != nil {
			guiLogger.Warn().Err(err).Msg("Failed to load config, falling back to defaults")
			// Fall back to defaults
			cfg, _ = config.LoadConfigCSV("")
		}
	} else {
		// Load default config
		cfg, err = config.LoadConfigCSV("")
		if err != nil {
			return fmt.Errorf("failed to create default config: %w", err)
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
	mainWindow.Resize(fyne.NewSize(1300, 850))
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
	engine      *core.Engine
	window      fyne.Window
	app         fyne.App
	setupTab    *SetupTab
	jobsTab     *JobsTab
	activityTab *ActivityTab
	ctx         context.Context
	cancel      context.CancelFunc
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
	ui.activityTab = NewActivityTab(engine, window)

	return ui
}

// Build creates the UI layout
func (ui *UI) Build() fyne.CanvasObject {
	tabs := container.NewAppTabs(
		container.NewTabItem("Setup", ui.setupTab.Build()),
		container.NewTabItem("Jobs", ui.jobsTab.Build()),
		container.NewTabItem("Activity", ui.activityTab.Build()),
	)

	// Select Jobs tab by default
	tabs.SelectIndex(1)

	return tabs
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

	// Read events and update UI directly - no need for fyne.Do()
	for {
		select {
		case event, ok := <-ch:
			if !ok {
				return
			}
			progress := event.(*events.ProgressEvent)

			// Call updates directly - widgets are thread-safe
			ui.jobsTab.UpdateProgress(progress)
			ui.activityTab.UpdateOverallProgress(progress)

		case <-ui.ctx.Done():
			return
		}
	}
}

func (ui *UI) monitorLogs() {
	ch := ui.engine.Events().Subscribe(events.EventLog)

	// Read events and update UI directly - no need for fyne.Do()
	for {
		select {
		case event, ok := <-ch:
			if !ok {
				return
			}
			logEvent := event.(*events.LogEvent)
			ui.activityTab.AddLog(logEvent)

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

		guiLogger.Info().
			Int("count", count).
			Int64("delta", delta).
			Msg("[MONITOR] Goroutines")

		// Alert if count is high or growing rapidly
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
