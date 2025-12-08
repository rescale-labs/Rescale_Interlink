package gui

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/core"
)

// SetupTab manages the configuration interface
type SetupTab struct {
	engine *core.Engine
	window fyne.Window

	// Form fields
	tenantURLEntry     *widget.Entry
	apiKeyEntry        *widget.Entry
	tokenSourceRadio   *widget.RadioGroup
	tokenFileButton    *widget.Button
	proxyModeSelect    *widget.Select
	proxyHostEntry     *widget.Entry
	proxyPortEntry     *widget.Entry
	proxyUserEntry     *widget.Entry
	proxyPassEntry     *widget.Entry
	tarWorkersEntry    *widget.Entry
	uploadWorkersEntry *widget.Entry
	jobWorkersEntry    *widget.Entry
	excludeEntry       *widget.Entry
	includeEntry       *widget.Entry
	flattenCheck       *widget.Check
	compressionSelect  *widget.Select

	runSubpathEntry        *widget.Entry
	validationEnabledCheck *widget.Check
	validationPatternEntry *widget.Entry

	// Status
	statusLabel      *widget.Label
	connectionStatus *widget.Label
	configFilePath   string

	// Connection state (persisted)
	lastConnectionTest   time.Time
	lastConnectionResult bool
	lastConnectionError  string
}

// NewSetupTab creates a new setup tab
func NewSetupTab(engine *core.Engine, window fyne.Window) *SetupTab {
	return &SetupTab{
		engine:           engine,
		window:           window,
		statusLabel:      widget.NewLabel("Ready"),
		connectionStatus: widget.NewLabel("Not connected"),
	}
}

// Build creates the setup tab UI
func (st *SetupTab) Build() fyne.CanvasObject {
	// Load current config
	cfg := st.engine.GetConfig()

	// API Configuration Section
	st.tenantURLEntry = widget.NewEntry()
	st.tenantURLEntry.SetPlaceHolder("https://platform.rescale.com")
	st.tenantURLEntry.SetText(cfg.APIBaseURL)

	st.apiKeyEntry = widget.NewPasswordEntry()
	st.apiKeyEntry.SetPlaceHolder("API Key")
	if cfg.APIKey != "" {
		st.apiKeyEntry.SetText(cfg.APIKey)
	}

	// Paste button for API key - workaround for Linux clipboard issues with PasswordEntry
	// On some Linux systems (RHEL/CentOS 8+), paste via Ctrl+V may not work in password fields
	pasteAPIKeyBtn := widget.NewButtonWithIcon("", theme.ContentPasteIcon(), func() {
		clipboard := st.window.Clipboard()
		if clipboard != nil {
			content := clipboard.Content()
			if content != "" {
				st.apiKeyEntry.SetText(strings.TrimSpace(content))
			}
		}
	})
	pasteAPIKeyBtn.Importance = widget.LowImportance

	// Wrap API key entry with paste button
	apiKeyRow := container.NewBorder(nil, nil, nil, pasteAPIKeyBtn, st.apiKeyEntry)

	// Create token file button first (before radio group triggers callbacks)
	st.tokenFileButton = widget.NewButton("Select Token File...", st.selectTokenFile)
	st.tokenFileButton.Importance = widget.HighImportance
	st.tokenFileButton.Disable()

	// Now create radio group and set selection (will trigger callback safely)
	st.tokenSourceRadio = widget.NewRadioGroup([]string{
		"Environment Variable (RESCALE_API_KEY)",
		"Token File",
		"Direct Input",
	}, st.onTokenSourceChange)
	st.tokenSourceRadio.SetSelected("Direct Input")
	if cfg.APIKey == "" {
		if os.Getenv("RESCALE_API_KEY") != "" {
			st.tokenSourceRadio.SetSelected("Environment Variable (RESCALE_API_KEY)")
		}
	}

	testButton := widget.NewButton("Test Connection", st.testConnection)
	testButton.Importance = widget.HighImportance

	apiSection := widget.NewForm(
		widget.NewFormItem("Platform URL", st.tenantURLEntry),
		widget.NewFormItem("Token Source", st.tokenSourceRadio),
		widget.NewFormItem("Token File", st.tokenFileButton),
		widget.NewFormItem("API Key", apiKeyRow), // Use row with paste button
		widget.NewFormItem("", container.NewHBox(testButton, st.connectionStatus)),
	)

	// Proxy Configuration Section
	// Create all proxy entry widgets first (before Select triggers callback)
	st.proxyHostEntry = widget.NewEntry()
	st.proxyHostEntry.SetPlaceHolder("proxy.company.com")
	st.proxyHostEntry.SetText(cfg.ProxyHost)

	st.proxyPortEntry = widget.NewEntry()
	st.proxyPortEntry.SetPlaceHolder("8080")
	if cfg.ProxyPort > 0 {
		st.proxyPortEntry.SetText(strconv.Itoa(cfg.ProxyPort))
	}

	st.proxyUserEntry = widget.NewEntry()
	st.proxyUserEntry.SetPlaceHolder("Username (for Basic auth)")
	st.proxyUserEntry.SetText(cfg.ProxyUser)

	st.proxyPassEntry = widget.NewPasswordEntry()
	st.proxyPassEntry.SetPlaceHolder("Password (for Basic auth)")
	st.proxyPassEntry.SetText(cfg.ProxyPassword)

	// Now create Select and set selection (will trigger callback safely)
	st.proxyModeSelect = widget.NewSelect([]string{
		"no-proxy",
		"system",
		"ntlm",
		"basic",
	}, st.onProxyModeChange)
	st.proxyModeSelect.SetSelected(cfg.ProxyMode)

	proxySection := widget.NewForm(
		widget.NewFormItem("Proxy Mode", st.proxyModeSelect),
		widget.NewFormItem("Proxy Host", st.proxyHostEntry),
		widget.NewFormItem("Proxy Port", st.proxyPortEntry),
		widget.NewFormItem("Username", st.proxyUserEntry),
		widget.NewFormItem("Password", st.proxyPassEntry),
	)

	// Worker Configuration Section
	st.tarWorkersEntry = widget.NewEntry()
	st.tarWorkersEntry.SetPlaceHolder("4")
	st.tarWorkersEntry.SetText(strconv.Itoa(cfg.TarWorkers))

	st.uploadWorkersEntry = widget.NewEntry()
	st.uploadWorkersEntry.SetPlaceHolder("4")
	st.uploadWorkersEntry.SetText(strconv.Itoa(cfg.UploadWorkers))

	st.jobWorkersEntry = widget.NewEntry()
	st.jobWorkersEntry.SetPlaceHolder("4")
	st.jobWorkersEntry.SetText(strconv.Itoa(cfg.JobWorkers))

	workerSection := widget.NewForm(
		widget.NewFormItem("Tar Workers", st.tarWorkersEntry),
		widget.NewFormItem("Upload Workers", st.uploadWorkersEntry),
		widget.NewFormItem("Job Workers", st.jobWorkersEntry),
	)

	// Tar Options Section
	st.excludeEntry = widget.NewEntry()
	st.excludeEntry.SetPlaceHolder("*.tmp,*.log,*.bak")
	st.excludeEntry.SetText(strings.Join(cfg.ExcludePatterns, ","))

	st.includeEntry = widget.NewEntry()
	st.includeEntry.SetPlaceHolder("*.dat,*.csv,*.inp")
	st.includeEntry.SetText(strings.Join(cfg.IncludePatterns, ","))

	st.flattenCheck = widget.NewCheck("Flatten directory structure in tar", nil)
	st.flattenCheck.SetChecked(cfg.FlattenTar)

	st.compressionSelect = widget.NewSelect([]string{
		"gzip",
		"none",
	}, nil)
	if cfg.TarCompression == "none" {
		st.compressionSelect.SetSelected("none")
	} else {
		st.compressionSelect.SetSelected("gzip")
	}

	// Help text for tar patterns
	tarHelpLabel := widget.NewLabel("Patterns support wildcards (*). Use comma-separated list.\nExclude: skip these files when creating tar archives\nInclude: only include these files (leave empty to include all)")
	tarHelpLabel.Wrapping = fyne.TextWrapWord
	tarHelpLabel.Importance = widget.LowImportance

	tarSection := widget.NewForm(
		widget.NewFormItem("Exclude Pattern", st.excludeEntry),
		widget.NewFormItem("Include Pattern", st.includeEntry),
		widget.NewFormItem("Compression", st.compressionSelect),
		widget.NewFormItem("", st.flattenCheck),
		widget.NewFormItem("", tarHelpLabel),
	)

	// Run Folders Subpath and Validation Configuration
	st.runSubpathEntry = widget.NewEntry()
	st.runSubpathEntry.SetPlaceHolder("Optional subpath within each run directory to each run's files, e.g. RunData/Files/")
	st.runSubpathEntry.SetText(cfg.RunSubpath)

	// Validation pattern checkbox and entry
	st.validationPatternEntry = widget.NewEntry()
	st.validationPatternEntry.SetPlaceHolder("e.g., *.avg.fnc or results.dat")
	st.validationPatternEntry.SetText(cfg.ValidationPattern)

	// Create checkbox to enable/disable validation
	st.validationEnabledCheck = widget.NewCheck("Enable validation", func(checked bool) {
		if checked {
			st.validationPatternEntry.Enable()
		} else {
			st.validationPatternEntry.Disable()
		}
	})

	// Default: validation disabled unless explicitly enabled
	// This prevents annoying errors when users don't need validation
	st.validationEnabledCheck.SetChecked(false)
	st.validationPatternEntry.Disable()

	// Only enable if there's an explicitly set validation pattern
	// AND it's not the default pattern that might come from templates
	if cfg.ValidationPattern != "" && cfg.ValidationPattern != "*.avg.fnc" {
		st.validationEnabledCheck.SetChecked(true)
		st.validationPatternEntry.Enable()
	}

	// Help text for validation
	validationHelpLabel := widget.NewLabel("Validation checks that each run directory contains files matching the pattern.\nIf any directory is missing these files, it will be flagged during scan.")
	validationHelpLabel.Wrapping = fyne.TextWrapWord
	validationHelpLabel.Importance = widget.LowImportance

	scanSection := widget.NewForm(
		widget.NewFormItem("Run Subpath", st.runSubpathEntry),
		widget.NewFormItem("Validation", st.validationEnabledCheck),
		widget.NewFormItem("Validation Pattern", st.validationPatternEntry),
		widget.NewFormItem("", validationHelpLabel),
	)

	// Action Buttons - pinned at top
	// All buttons use HighImportance for white text on blue background
	loadButton := widget.NewButton("Load Config", st.loadConfig)
	loadButton.Importance = widget.HighImportance
	saveButton := widget.NewButton("Save Config", st.saveConfig)
	saveButton.Importance = widget.HighImportance
	saveDefaultButton := widget.NewButton("Save to Default", st.saveConfigToDefault)
	saveDefaultButton.Importance = widget.HighImportance
	applyButton := widget.NewButton("Apply Changes", func() {
		if err := st.applyConfig(); err != nil {
			dialog.ShowError(err, st.window)
		} else {
			// Show success feedback
			dialog.ShowInformation("Success",
				"Configuration has been applied successfully.",
				st.window)
		}
	})
	applyButton.Importance = widget.HighImportance

	buttons := container.NewHBox(
		loadButton,
		HorizontalSpacer(8),
		saveButton,
		HorizontalSpacer(4),
		saveDefaultButton,
		HorizontalSpacer(8),
		applyButton,
	)

	// Scrollable content with all configuration sections
	// Using VerticalSpacer for consistent spacing between cards
	scrollableContent := container.NewVBox(
		VerticalSpacer(8),
		widget.NewCard("API Configuration", "", apiSection),
		VerticalSpacer(4),
		widget.NewCard("Run Folders Subpath and Validation Configuration", "", scanSection),
		VerticalSpacer(4),
		widget.NewCard("Worker Configuration", "", workerSection),
		VerticalSpacer(4),
		widget.NewCard("Tar Options", "", tarSection),
		VerticalSpacer(4),
		widget.NewCard("Proxy Configuration", "", proxySection),
		VerticalSpacer(8),
		widget.NewSeparator(),
		VerticalSpacer(4),
		container.NewHBox(st.statusLabel),
		VerticalSpacer(8),
	)

	// Restore connection state if it exists
	st.restoreConnectionState()

	// Layout: Pinned buttons at top, scrollable content below
	return container.NewBorder(
		container.NewVBox(
			VerticalSpacer(8),
			container.NewPadded(buttons),
			VerticalSpacer(4),
			widget.NewSeparator(),
		), // Top (pinned)
		nil,                                     // Bottom
		nil,                                     // Left
		nil,                                     // Right
		container.NewVScroll(scrollableContent), // Center (scrollable)
	)
}

func (st *SetupTab) onTokenSourceChange(value string) {
	switch value {
	case "Environment Variable (RESCALE_API_KEY)":
		st.apiKeyEntry.Disable()
		st.tokenFileButton.Disable()
		// Load from env
		if key := os.Getenv("RESCALE_API_KEY"); key != "" {
			st.apiKeyEntry.SetText(key)
		}
	case "Token File":
		st.apiKeyEntry.Disable()
		st.tokenFileButton.Enable()
	case "Direct Input":
		st.apiKeyEntry.Enable()
		st.tokenFileButton.Disable()
	}
}

func (st *SetupTab) selectTokenFile() {
	dialog.ShowFileOpen(func(reader fyne.URIReadCloser, err error) {
		if err != nil {
			dialog.ShowError(err, st.window)
			return
		}
		if reader == nil {
			return
		}
		defer reader.Close()

		// Read token from file
		data := make([]byte, 1024)
		n, err := reader.Read(data)
		if err != nil {
			dialog.ShowError(err, st.window)
			return
		}

		token := string(data[:n])
		st.apiKeyEntry.SetText(token)
		st.statusLabel.SetText("Token loaded from file")
	}, st.window)
}

func (st *SetupTab) onProxyModeChange(value string) {
	enabled := value != "no-proxy" && value != "system"
	if enabled {
		st.proxyHostEntry.Enable()
		st.proxyPortEntry.Enable()
	} else {
		st.proxyHostEntry.Disable()
		st.proxyPortEntry.Disable()
	}

	// Basic auth needs credentials
	basicAuth := value == "basic"
	if basicAuth {
		st.proxyUserEntry.Enable()
		st.proxyPassEntry.Enable()
	} else {
		st.proxyUserEntry.Disable()
		st.proxyPassEntry.Disable()
	}
}

func (st *SetupTab) testConnection() {
	// First apply current settings
	if err := st.applyConfig(); err != nil {
		dialog.ShowError(fmt.Errorf("Failed to apply config: %w", err), st.window)
		return
	}

	st.statusLabel.SetText("Testing connection...")
	st.connectionStatus.SetText("Testing...")

	progress := dialog.NewProgressInfinite("Testing Connection",
		"Connecting to Rescale API...", st.window)
	progress.Show()

	go func() {
		// Panic recovery - must hide progress and show error
		defer func() {
			if r := recover(); r != nil {
				guiLogger.Error().Msgf("PANIC in test connection goroutine: %v\n", r)
				fyne.Do(func() {
					progress.Hide()
					st.connectionStatus.SetText("❌ Failed")
					dialog.ShowError(
						fmt.Errorf("An unexpected error occurred during connection test: %v\n\nPlease check the console for details.", r),
						st.window,
					)
					st.statusLabel.SetText("Connection test failed")
				})
			}
		}()

		err := st.engine.TestConnection()

		// Store connection state for persistence
		st.lastConnectionTest = time.Now()
		st.lastConnectionResult = (err == nil)
		if err != nil {
			st.lastConnectionError = err.Error()
		} else {
			st.lastConnectionError = ""
		}

		// Update UI - hide progress FIRST, then show result dialog
		// All in one fyne.Do() to ensure proper sequencing
		fyne.Do(func() {
			progress.Hide()

			if err != nil {
				st.updateConnectionStatusDisplay(false, err.Error())
				dialog.ShowError(err, st.window)
				st.statusLabel.SetText("Connection failed")
			} else {
				st.updateConnectionStatusDisplay(true, "")
				dialog.ShowInformation("Connection Successful",
					"✓ Successfully connected to Rescale API.\n\nYour API key and platform URL are valid.",
					st.window)
				st.statusLabel.SetText("Connection successful")
			}
		})
	}()
}

// updateConnectionStatusDisplay updates the connection status label with result and timestamp
func (st *SetupTab) updateConnectionStatusDisplay(success bool, errorMsg string) {
	var statusText string
	if success {
		statusText = "✓ Connected"
	} else {
		statusText = "❌ Failed"
	}

	// Add timestamp if we have one
	if !st.lastConnectionTest.IsZero() {
		elapsed := time.Since(st.lastConnectionTest)
		if elapsed < time.Minute {
			statusText += fmt.Sprintf(" (%ds ago)", int(elapsed.Seconds()))
		} else if elapsed < time.Hour {
			statusText += fmt.Sprintf(" (%dm ago)", int(elapsed.Minutes()))
		} else {
			statusText += fmt.Sprintf(" (%s)", st.lastConnectionTest.Format("15:04"))
		}
	}

	st.connectionStatus.SetText(statusText)
}

// restoreConnectionState updates the display based on stored connection state
func (st *SetupTab) restoreConnectionState() {
	if !st.lastConnectionTest.IsZero() {
		st.updateConnectionStatusDisplay(st.lastConnectionResult, st.lastConnectionError)
	}
}

func (st *SetupTab) loadConfig() {
	dialog.ShowFileOpen(func(reader fyne.URIReadCloser, err error) {
		if err != nil {
			dialog.ShowError(err, st.window)
			return
		}
		if reader == nil {
			return
		}
		defer reader.Close()

		// Validate file extension
		if err := validateCSVExtension(reader.URI(), st.window); err != nil {
			dialog.ShowError(err, st.window)
			return
		}

		path := reader.URI().Path()
		if err := st.engine.LoadConfig(path); err != nil {
			dialog.ShowError(err, st.window)
			return
		}

		st.configFilePath = path
		st.refreshFromEngine()
		st.statusLabel.SetText("Config loaded from " + path)
		dialog.ShowInformation("Success", "Configuration loaded successfully", st.window)
	}, st.window)
}

func (st *SetupTab) saveConfig() {
	// Apply current settings first
	if err := st.applyConfig(); err != nil {
		dialog.ShowError(err, st.window)
		return
	}

	dialog.ShowFileSave(func(writer fyne.URIWriteCloser, err error) {
		if err != nil {
			dialog.ShowError(err, st.window)
			return
		}
		if writer == nil {
			return
		}
		defer writer.Close()

		// Validate and adjust file extension
		path := writer.URI().Path()
		if !strings.HasSuffix(strings.ToLower(path), ".csv") {
			path = path + ".csv"
		}
		if err := st.engine.SaveConfig(path); err != nil {
			dialog.ShowError(err, st.window)
			return
		}

		st.configFilePath = path
		st.statusLabel.SetText("Config saved to " + path)
		dialog.ShowInformation("Success",
			"Configuration saved successfully.\n\n"+
				"Note: API key and proxy password are not saved to the config file for security.\n"+
				"Use RESCALE_API_KEY environment variable or token file for API key.",
			st.window)
	}, st.window)
}

// saveConfigToDefault saves configuration to the default location (~/.config/rescale-int/config.csv)
// Also saves API key to the default token file if one is set
func (st *SetupTab) saveConfigToDefault() {
	// Apply current settings first
	if err := st.applyConfig(); err != nil {
		dialog.ShowError(err, st.window)
		return
	}

	// Ensure config directory exists
	if err := config.EnsureConfigDir(); err != nil {
		dialog.ShowError(fmt.Errorf("Failed to create config directory: %w", err), st.window)
		return
	}

	// Save config to default path
	configPath := config.GetDefaultConfigPath()
	if err := st.engine.SaveConfig(configPath); err != nil {
		dialog.ShowError(err, st.window)
		return
	}

	// Also save API key to token file if one is set
	apiKey := strings.TrimSpace(st.apiKeyEntry.Text)
	tokenSaved := false
	tokenPath := config.GetDefaultTokenPath()
	if apiKey != "" {
		if err := config.WriteTokenFile(tokenPath, apiKey); err != nil {
			// Don't fail the whole save, just warn
			dialog.ShowError(fmt.Errorf("Config saved, but failed to save API key: %w", err), st.window)
		} else {
			tokenSaved = true
		}
	}

	st.configFilePath = configPath
	st.statusLabel.SetText("Config saved to " + configPath)

	// Build success message based on what was saved
	var msg string
	if tokenSaved {
		msg = fmt.Sprintf("Configuration saved to:\n%s\n\nAPI key saved to:\n%s\n\n"+
			"These will be automatically loaded next time you start the GUI.\n\n"+
			"Note: Proxy password is not saved for security.",
			configPath, tokenPath)
	} else {
		msg = fmt.Sprintf("Configuration saved to:\n%s\n\n"+
			"This configuration will be automatically loaded next time you start the GUI.\n\n"+
			"Note: No API key was set, so token file was not created.",
			configPath)
	}
	dialog.ShowInformation("Success", msg, st.window)
}

// ApplyConfig applies the current form settings to the engine configuration
// This is called automatically when navigating away from the Setup tab
func (st *SetupTab) ApplyConfig() error {
	return st.applyConfig()
}

func (st *SetupTab) applyConfig() error {
	// Parse worker counts with better error messages
	tarWorkers, err := strconv.Atoi(strings.TrimSpace(st.tarWorkersEntry.Text))
	if err != nil {
		return fmt.Errorf("Tar Workers must be a number.\n\nYou entered: '%s'", st.tarWorkersEntry.Text)
	}
	if tarWorkers < 1 {
		return fmt.Errorf("Tar Workers must be at least 1.\n\nYou entered: %d", tarWorkers)
	}

	uploadWorkers, err := strconv.Atoi(strings.TrimSpace(st.uploadWorkersEntry.Text))
	if err != nil {
		return fmt.Errorf("Upload Workers must be a number.\n\nYou entered: '%s'", st.uploadWorkersEntry.Text)
	}
	if uploadWorkers < 1 {
		return fmt.Errorf("Upload Workers must be at least 1.\n\nYou entered: %d", uploadWorkers)
	}

	jobWorkers, err := strconv.Atoi(strings.TrimSpace(st.jobWorkersEntry.Text))
	if err != nil {
		return fmt.Errorf("Job Workers must be a number.\n\nYou entered: '%s'", st.jobWorkersEntry.Text)
	}
	if jobWorkers < 1 {
		return fmt.Errorf("Job Workers must be at least 1.\n\nYou entered: %d", jobWorkers)
	}

	// Parse proxy port
	var proxyPort int
	if strings.TrimSpace(st.proxyPortEntry.Text) != "" {
		proxyPort, err = strconv.Atoi(strings.TrimSpace(st.proxyPortEntry.Text))
		if err != nil {
			return fmt.Errorf("Proxy Port must be a number.\n\nYou entered: '%s'", st.proxyPortEntry.Text)
		}
		if proxyPort < 1 || proxyPort > 65535 {
			return fmt.Errorf("Proxy Port must be between 1 and 65535.\n\nYou entered: %d", proxyPort)
		}
	}

	// Create new config by loading defaults
	cfg, err := config.LoadConfigCSV("")
	if err != nil {
		return fmt.Errorf("failed to create config: %w", err)
	}

	// Trim whitespace from all text fields to avoid common errors
	cfg.APIBaseURL = strings.TrimSpace(st.tenantURLEntry.Text)
	cfg.APIKey = strings.TrimSpace(st.apiKeyEntry.Text)
	cfg.ProxyMode = st.proxyModeSelect.Selected
	cfg.ProxyHost = strings.TrimSpace(st.proxyHostEntry.Text)
	cfg.ProxyPort = proxyPort
	cfg.ProxyUser = strings.TrimSpace(st.proxyUserEntry.Text)
	cfg.ProxyPassword = strings.TrimSpace(st.proxyPassEntry.Text)
	cfg.TarWorkers = tarWorkers
	cfg.UploadWorkers = uploadWorkers
	cfg.JobWorkers = jobWorkers

	// Parse comma-separated patterns
	if st.excludeEntry.Text != "" {
		cfg.ExcludePatterns = strings.Split(st.excludeEntry.Text, ",")
		for i := range cfg.ExcludePatterns {
			cfg.ExcludePatterns[i] = strings.TrimSpace(cfg.ExcludePatterns[i])
		}
	} else {
		cfg.ExcludePatterns = nil
	}

	if st.includeEntry.Text != "" {
		cfg.IncludePatterns = strings.Split(st.includeEntry.Text, ",")
		for i := range cfg.IncludePatterns {
			cfg.IncludePatterns[i] = strings.TrimSpace(cfg.IncludePatterns[i])
		}
	} else {
		cfg.IncludePatterns = nil
	}

	cfg.FlattenTar = st.flattenCheck.Checked
	cfg.TarCompression = st.compressionSelect.Selected
	cfg.RunSubpath = strings.TrimSpace(st.runSubpathEntry.Text)

	// Only save validation pattern if checkbox is checked
	if st.validationEnabledCheck.Checked {
		cfg.ValidationPattern = strings.TrimSpace(st.validationPatternEntry.Text)
	} else {
		cfg.ValidationPattern = ""
	}

	// Update engine
	if err := st.engine.UpdateConfig(cfg); err != nil {
		return err
	}

	st.statusLabel.SetText("Configuration applied")
	return nil
}

func (st *SetupTab) refreshFromEngine() {
	cfg := st.engine.GetConfig()

	st.tenantURLEntry.SetText(cfg.APIBaseURL)
	st.apiKeyEntry.SetText(cfg.APIKey)
	st.proxyModeSelect.SetSelected(cfg.ProxyMode)
	st.proxyHostEntry.SetText(cfg.ProxyHost)
	if cfg.ProxyPort > 0 {
		st.proxyPortEntry.SetText(strconv.Itoa(cfg.ProxyPort))
	}
	st.proxyUserEntry.SetText(cfg.ProxyUser)
	st.proxyPassEntry.SetText(cfg.ProxyPassword)
	st.tarWorkersEntry.SetText(strconv.Itoa(cfg.TarWorkers))
	st.uploadWorkersEntry.SetText(strconv.Itoa(cfg.UploadWorkers))
	st.jobWorkersEntry.SetText(strconv.Itoa(cfg.JobWorkers))
	st.excludeEntry.SetText(strings.Join(cfg.ExcludePatterns, ","))
	st.includeEntry.SetText(strings.Join(cfg.IncludePatterns, ","))
	st.flattenCheck.SetChecked(cfg.FlattenTar)
	st.compressionSelect.SetSelected(cfg.TarCompression)
	st.runSubpathEntry.SetText(cfg.RunSubpath)

	// Update validation checkbox and entry
	st.validationPatternEntry.SetText(cfg.ValidationPattern)
	if cfg.ValidationPattern != "" {
		st.validationEnabledCheck.SetChecked(true)
		st.validationPatternEntry.Enable()
	} else {
		st.validationEnabledCheck.SetChecked(false)
		st.validationPatternEntry.Disable()
	}
}
