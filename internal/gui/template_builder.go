package gui

import (
	"context"
	"fmt"
	"image/color"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/core"
	"github.com/rescale/rescale-int/internal/models"
	"github.com/rescale/rescale-int/internal/pur/parser"
)

// TemplateBuilderDialog creates a dialog for building a job template from scratch
type TemplateBuilderDialog struct {
	window   fyne.Window
	engine   *core.Engine
	apiCache *APICache
	cfg      *config.Config
	workflow *JobsWorkflow
	dialog   dialog.Dialog // Store dialog reference so we can close it

	// Job Configuration
	jobNameEntry           *widget.Entry
	analysisCodeSelect     *widget.Select    // Legacy dropdown (kept for fallback)
	analysisCodeSearchable *SearchableSelect // New searchable entry
	useSearchable          bool              // Flag to track which widget is active
	analysisVersionEntry   *widget.Entry
	commandEntry           *widget.Entry

	// Hardware Configuration
	coreTypeSelect     *widget.Select        // Legacy dropdown (kept for fallback)
	coreTypeSearchable *SearchableSelect     // New searchable entry (mirrors software)
	coresPerSlotEntry  *widget.Entry
	walltimeEntry     *widget.Entry
	slotsEntry        *widget.Entry

	// Project Configuration
	orgCodeEntry   *widget.Entry
	projectIDEntry *widget.Entry
	tagsEntry      *widget.Entry

	// License Configuration
	licenseTypeSelect *widget.Select
	licenseValueEntry *widget.Entry

	// Input Files Configuration
	inputFilesList   *widget.List
	inputFiles       []string // List of selected file/folder paths
	tarInputsCheck   *widget.Check
	addFilesBtn      *widget.Button
	addFolderBtn     *widget.Button
	removeFilesBtn   *widget.Button

	// Submit Options
	submitModeSelect *widget.Select

	// Callback when template is created
	onTemplateCreated func(models.JobSpec)

	// Software/Hardware scan state
	scannedAnalyses      []models.Analysis
	selectedAnalysis     *models.Analysis
	selectedVersionIndex int
	scanSoftwareBtn      *widget.Button
	scanHardwareBtn      *widget.Button
}

// NewTemplateBuilderDialog creates a new template builder dialog
func NewTemplateBuilderDialog(window fyne.Window, engine *core.Engine, apiCache *APICache, cfg *config.Config, workflow *JobsWorkflow, onCreated func(models.JobSpec)) *TemplateBuilderDialog {
	return &TemplateBuilderDialog{
		window:               window,
		engine:               engine,
		apiCache:             apiCache,
		cfg:                  cfg,
		workflow:             workflow,
		onTemplateCreated:    onCreated,
		selectedVersionIndex: -1,
	}
}

// Show displays the template builder dialog
func (tb *TemplateBuilderDialog) Show() {
	// Initialize with defaults from workflow memory or system defaults
	template := tb.workflow.Memory.LastTemplate
	if template.JobName == "" {
		template = GetDefaultTemplate()
	}

	// Config defaults can be applied here if needed
	// ProjectID is stored in workflow memory, not config

	tb.buildUI(template)
}

// createSectionHeader creates a styled section header with centered text and larger font
func (tb *TemplateBuilderDialog) createSectionHeader(title string) fyne.CanvasObject {
	text := canvas.NewText(title, theme.Color(theme.ColorNameForeground))
	text.TextSize = 16
	text.TextStyle = fyne.TextStyle{Bold: true}
	text.Alignment = fyne.TextAlignCenter

	// Add a line under the header
	line := canvas.NewRectangle(color.NRGBA{R: 128, G: 128, B: 128, A: 128})
	line.SetMinSize(fyne.NewSize(0, 1))

	return container.NewVBox(
		container.NewPadded(text),
		line,
	)
}

// createFormRow creates a form-style row with label and widget
func (tb *TemplateBuilderDialog) createFormRow(label string, widget fyne.CanvasObject) fyne.CanvasObject {
	labelText := canvas.NewText(label, theme.Color(theme.ColorNameForeground))
	labelText.TextSize = 14
	labelText.Alignment = fyne.TextAlignTrailing

	labelContainer := container.NewGridWrap(fyne.NewSize(150, 30), labelText)
	return container.NewBorder(nil, nil, labelContainer, nil, widget)
}

// buildUI constructs the template builder form with proper layout
func (tb *TemplateBuilderDialog) buildUI(defaults models.JobSpec) {
	// Initialize input files list
	tb.inputFiles = make([]string, 0)

	// Job Configuration Section
	tb.jobNameEntry = widget.NewEntry()
	tb.jobNameEntry.SetText(defaults.JobName)
	tb.jobNameEntry.SetPlaceHolder("Run_1")

	// Analysis code - use searchable entry for better UX with large lists
	tb.analysisCodeSearchable = NewSearchableSelect("Type to search software...", func(selected string) {
		tb.onAnalysisCodeChanged(selected)
	})
	analysisCodes := tb.apiCache.GetAnalysisCodes()
	tb.analysisCodeSearchable.SetOptions(analysisCodes)
	if defaults.AnalysisCode != "" {
		tb.analysisCodeSearchable.SetSelected(defaults.AnalysisCode)
	}
	tb.useSearchable = true

	// Legacy dropdown (hidden, kept for compatibility)
	tb.analysisCodeSelect = widget.NewSelect(analysisCodes, func(selected string) {
		tb.onAnalysisCodeChanged(selected)
	})
	if defaults.AnalysisCode != "" {
		tb.analysisCodeSelect.SetSelected(defaults.AnalysisCode)
	}

	// Scan Software button - constrained size
	tb.scanSoftwareBtn = widget.NewButton("Scan Available Software", func() {
		tb.handleScanSoftware()
	})
	tb.scanSoftwareBtn.Importance = widget.HighImportance

	tb.analysisVersionEntry = widget.NewEntry()
	tb.analysisVersionEntry.SetText(defaults.AnalysisVersion)
	tb.analysisVersionEntry.SetPlaceHolder("(auto-populated after scan)")

	tb.commandEntry = widget.NewEntry()
	tb.commandEntry.SetText(defaults.Command)
	tb.commandEntry.SetPlaceHolder("./run.sh")
	tb.commandEntry.MultiLine = true
	tb.commandEntry.Wrapping = fyne.TextWrapWord

	// Hardware Configuration Section
	coreTypes, isLoading, err := tb.apiCache.GetCoreTypes()
	var coreTypeOptions []string

	if isLoading {
		coreTypeOptions = []string{"Loading core types..."}
	} else if err != nil || len(coreTypes) == 0 {
		for _, ct := range getDefaultCoreTypes() {
			coreTypeOptions = append(coreTypeOptions, ct.Code)
		}
	} else {
		for _, ct := range coreTypes {
			coreTypeOptions = append(coreTypeOptions, ct.Code)
		}
	}

	// Hardware - use searchable entry for better UX with large lists (mirrors software)
	tb.coreTypeSearchable = NewSearchableSelect("Type to search hardware...", nil)
	tb.coreTypeSearchable.SetOptions(coreTypeOptions)
	if defaults.CoreType != "" {
		tb.coreTypeSearchable.SetSelected(defaults.CoreType)
	}

	// Legacy dropdown (hidden, kept for compatibility)
	tb.coreTypeSelect = widget.NewSelect(coreTypeOptions, nil)
	if defaults.CoreType != "" {
		tb.coreTypeSelect.SetSelected(defaults.CoreType)
	}

	// Scan Hardware button - constrained size
	tb.scanHardwareBtn = widget.NewButton("Scan Compatible Hardware", func() {
		tb.handleScanHardware()
	})
	tb.scanHardwareBtn.Importance = widget.HighImportance
	tb.scanHardwareBtn.Disable() // Initially disabled

	tb.coresPerSlotEntry = widget.NewEntry()
	tb.coresPerSlotEntry.SetText(strconv.Itoa(defaults.CoresPerSlot))
	tb.coresPerSlotEntry.SetPlaceHolder("4")

	tb.walltimeEntry = widget.NewEntry()
	tb.walltimeEntry.SetText(fmt.Sprintf("%.1f", defaults.WalltimeHours))
	tb.walltimeEntry.SetPlaceHolder("1.0")

	tb.slotsEntry = widget.NewEntry()
	tb.slotsEntry.SetText(strconv.Itoa(defaults.Slots))
	tb.slotsEntry.SetPlaceHolder("1")

	// Project Configuration Section
	tb.orgCodeEntry = widget.NewEntry()
	if tb.cfg != nil {
		tb.orgCodeEntry.SetText(tb.workflow.Memory.LastOrgCode)
	}
	tb.orgCodeEntry.SetPlaceHolder("organization-name")

	tb.projectIDEntry = widget.NewEntry()
	if defaults.ProjectID != "" {
		tb.projectIDEntry.SetText(defaults.ProjectID)
	} else if tb.workflow.Memory.LastProjectID != "" {
		tb.projectIDEntry.SetText(tb.workflow.Memory.LastProjectID)
	}
	tb.projectIDEntry.SetPlaceHolder("project-id")

	tb.tagsEntry = widget.NewEntry()
	if len(defaults.Tags) > 0 {
		tb.tagsEntry.SetText(strings.Join(defaults.Tags, ", "))
	}
	tb.tagsEntry.SetPlaceHolder("tag1, tag2")

	// License Configuration Section
	licenseTypes := GetCommonLicenseTypes()
	var licenseTypeNames []string
	for _, lt := range licenseTypes {
		licenseTypeNames = append(licenseTypeNames, lt.DisplayName)
	}

	// Parse existing license settings
	licenseKey := ""
	licenseValue := ""
	if defaults.LicenseSettings != "" {
		if key, value, err := ParseLicenseJSON(defaults.LicenseSettings); err == nil {
			licenseKey = key
			licenseValue = value
		}
	}

	tb.licenseValueEntry = widget.NewEntry()
	tb.licenseValueEntry.SetText(licenseValue)
	tb.licenseValueEntry.SetPlaceHolder("port@license-server")

	tb.licenseTypeSelect = widget.NewSelect(licenseTypeNames, func(selected string) {
		for _, lt := range licenseTypes {
			if lt.DisplayName == selected {
				tb.licenseValueEntry.SetPlaceHolder(lt.Placeholder)
				return
			}
		}
		tb.licenseValueEntry.SetPlaceHolder("port@license-server")
	})

	found := false
	for _, lt := range licenseTypes {
		if lt.Key == licenseKey {
			tb.licenseTypeSelect.SetSelected(lt.DisplayName)
			found = true
			break
		}
	}
	if !found && licenseKey != "" {
		tb.licenseTypeSelect.SetSelected("Custom")
	}

	// Input Files Section
	// Use wrapping labels to display long file/folder paths
	tb.inputFilesList = widget.NewList(
		func() int { return len(tb.inputFiles) },
		func() fyne.CanvasObject {
			label := widget.NewLabel("template file path that might be very long")
			label.Wrapping = fyne.TextWrapWord
			return label
		},
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			if id < len(tb.inputFiles) {
				label := obj.(*widget.Label)
				label.SetText(tb.inputFiles[id])
				label.Wrapping = fyne.TextWrapWord
			}
		},
	)
	tb.inputFilesList.OnSelected = func(id widget.ListItemID) {
		// Enable remove button when item selected
		if tb.removeFilesBtn != nil {
			tb.removeFilesBtn.Enable()
		}
	}

	tb.addFilesBtn = widget.NewButton("Add Files...", func() {
		tb.handleAddFiles()
	})
	tb.addFilesBtn.Importance = widget.HighImportance

	tb.addFolderBtn = widget.NewButton("Add Folder...", func() {
		tb.handleAddFolder()
	})
	tb.addFolderBtn.Importance = widget.HighImportance

	tb.removeFilesBtn = widget.NewButton("Remove Selected", func() {
		tb.handleRemoveFile()
	})
	tb.removeFilesBtn.Importance = widget.HighImportance
	tb.removeFilesBtn.Disable() // Initially disabled

	tb.tarInputsCheck = widget.NewCheck("Tar inputs before upload", nil)
	tb.tarInputsCheck.SetChecked(true) // Default to tar

	// Submit Mode
	tb.submitModeSelect = widget.NewSelect(GetSubmitModes(), nil)
	if defaults.SubmitMode != "" {
		tb.submitModeSelect.SetSelected(defaults.SubmitMode)
	} else {
		tb.submitModeSelect.SetSelected("create_and_submit")
	}

	// Build the form content using VBox with proper sections
	softwareSection := container.NewVBox(
		tb.createSectionHeader("Job Software Configuration"),
		tb.createFormRow("Job Name", tb.jobNameEntry),
		tb.createFormRow("Analysis Code", tb.analysisCodeSearchable),
		container.NewHBox(container.NewGridWrap(fyne.NewSize(150, 30)), tb.scanSoftwareBtn),
		tb.createFormRow("Analysis Version", tb.analysisVersionEntry),
		tb.createFormRow("Command", tb.commandEntry),
	)

	hardwareSection := container.NewVBox(
		tb.createSectionHeader("Job Hardware Configuration"),
		tb.createFormRow("Core Type", tb.coreTypeSearchable),
		container.NewHBox(container.NewGridWrap(fyne.NewSize(150, 30)), tb.scanHardwareBtn),
		tb.createFormRow("Cores Per Slot", tb.coresPerSlotEntry),
		tb.createFormRow("Walltime (hours)", tb.walltimeEntry),
	)

	// Input files section - use full width for long paths with more height
	inputFilesButtons := container.NewHBox(tb.addFilesBtn, tb.addFolderBtn, tb.removeFilesBtn)
	inputFilesListContainer := container.NewGridWrap(fyne.NewSize(750, 150), tb.inputFilesList)
	inputFilesSection := container.NewVBox(
		tb.createSectionHeader("Input Files"),
		widget.NewLabel("Select files and folders to upload with the job:"),
		inputFilesListContainer,
		inputFilesButtons,
		tb.tarInputsCheck,
	)

	otherSection := container.NewVBox(
		tb.createSectionHeader("Other Job Attributes"),
		tb.createFormRow("Project ID", tb.projectIDEntry),
		tb.createFormRow("Tags (comma-separated)", tb.tagsEntry),
	)

	licenseSection := container.NewVBox(
		tb.createSectionHeader("Job License Settings"),
		tb.createFormRow("License Type", tb.licenseTypeSelect),
		tb.createFormRow("License Value", tb.licenseValueEntry),
	)

	submitSection := container.NewVBox(
		tb.createSectionHeader("Submit Options"),
		tb.createFormRow("Submit Mode", tb.submitModeSelect),
	)

	// Combine all sections
	formContent := container.NewVBox(
		softwareSection,
		widget.NewSeparator(),
		hardwareSection,
		widget.NewSeparator(),
		inputFilesSection,
		widget.NewSeparator(),
		otherSection,
		widget.NewSeparator(),
		licenseSection,
		widget.NewSeparator(),
		submitSection,
	)

	// Wrap in scroll container to prevent overflow
	scrollContent := container.NewVScroll(formContent)
	scrollContent.SetMinSize(fyne.NewSize(800, 600))

	// Create custom buttons with controlled sizing
	useButton := widget.NewButton("Use Template", func() {
		tb.handleSubmit()
	})
	useButton.Importance = widget.HighImportance

	saveCSVButton := widget.NewButton("Save as CSV...", func() {
		tb.handleSaveTemplate()
	})
	saveCSVButton.Importance = widget.HighImportance

	saveJSONButton := widget.NewButton("Save as JSON...", func() {
		tb.handleSaveAsJSON()
	})
	saveJSONButton.Importance = widget.HighImportance

	saveSGEButton := widget.NewButton("Save as SGE...", func() {
		tb.handleSaveAsSGE()
	})
	saveSGEButton.Importance = widget.HighImportance

	cancelButton := widget.NewButton("Cancel", func() {
		tb.dialog.Hide()
	})
	cancelButton.Importance = widget.HighImportance

	buttons := container.NewHBox(
		useButton,
		saveCSVButton,
		saveJSONButton,
		saveSGEButton,
		cancelButton,
	)

	content := container.NewBorder(nil, container.NewPadded(buttons), nil, nil, scrollContent)

	// Create dialog and store reference
	tb.dialog = dialog.NewCustomWithoutButtons("Configure New Job", content, tb.window)
	tb.dialog.Resize(fyne.NewSize(900, 800))
	tb.dialog.Show()
}

// handleAddFiles opens a file picker to add input files
func (tb *TemplateBuilderDialog) handleAddFiles() {
	fileDialog := dialog.NewFileOpen(func(reader fyne.URIReadCloser, err error) {
		if err != nil {
			dialog.ShowError(err, tb.window)
			return
		}
		if reader == nil {
			return // User cancelled
		}
		defer reader.Close()

		path := reader.URI().Path()
		// Check if already added
		for _, existing := range tb.inputFiles {
			if existing == path {
				return
			}
		}
		tb.inputFiles = append(tb.inputFiles, path)
		tb.inputFilesList.Refresh()
	}, tb.window)
	fileDialog.Show()
}

// handleAddFolder opens a folder picker to add input folder
func (tb *TemplateBuilderDialog) handleAddFolder() {
	folderDialog := dialog.NewFolderOpen(func(uri fyne.ListableURI, err error) {
		if err != nil {
			dialog.ShowError(err, tb.window)
			return
		}
		if uri == nil {
			return // User cancelled
		}

		path := uri.Path()
		// Check if already added
		for _, existing := range tb.inputFiles {
			if existing == path {
				return
			}
		}
		tb.inputFiles = append(tb.inputFiles, path)
		tb.inputFilesList.Refresh()
	}, tb.window)
	folderDialog.Show()
}

// handleRemoveFile removes the selected file from the input list
func (tb *TemplateBuilderDialog) handleRemoveFile() {
	// Get selected index - this is a simplified approach
	// In practice, we'd track selected items better
	if len(tb.inputFiles) == 0 {
		return
	}
	// Remove last item for now - proper implementation would track selection
	tb.inputFiles = tb.inputFiles[:len(tb.inputFiles)-1]
	tb.inputFilesList.Refresh()
	if len(tb.inputFiles) == 0 {
		tb.removeFilesBtn.Disable()
	}
}

// handleSaveTemplate saves the template to a user-selected file
// NOTE: This does NOT close the dialog or trigger workflow state changes
// It only saves the template to disk for later reuse
func (tb *TemplateBuilderDialog) handleSaveTemplate() {
	// Build and validate the template
	template, parseErrors, validationErrors := tb.buildAndValidateTemplate()

	// Show errors if any
	if len(parseErrors) > 0 {
		dialog.ShowError(
			fmt.Errorf("Please fix the following errors:\n\n%s", strings.Join(parseErrors, "\n")),
			tb.window,
		)
		return
	}

	if len(validationErrors) > 0 {
		var errorMsgs []string
		for _, err := range validationErrors {
			errorMsgs = append(errorMsgs, "• "+err.Error())
		}
		dialog.ShowError(
			fmt.Errorf("Template validation failed:\n\n%s\n\nPlease fix these issues and try again.",
				strings.Join(errorMsgs, "\n")),
			tb.window,
		)
		return
	}

	// Template is valid, ask where to save it
	dialog.ShowFileSave(func(writer fyne.URIWriteCloser, err error) {
		if err != nil {
			dialog.ShowError(err, tb.window)
			return
		}
		if writer == nil {
			return // User cancelled
		}
		defer writer.Close()

		// Validate and adjust file extension
		filePath := writer.URI().Path()
		if !strings.HasSuffix(strings.ToLower(filePath), ".csv") {
			filePath = filePath + ".csv"
		}

		// Save template as single-row CSV
		err = config.SaveJobsCSV(filePath, []models.JobSpec{template})
		if err != nil {
			dialog.ShowError(fmt.Errorf("Failed to save template: %w", err), tb.window)
			return
		}

		// Show success message
		// Dialog remains open so user can continue editing or click "Use Template"
		dialog.ShowInformation("Template Saved",
			fmt.Sprintf("Template saved successfully to:\n%s\n\nYou can now:\n• Click 'Use Template' to continue with workflow\n• Click 'Cancel' to close this dialog\n• Make changes and save again", filePath),
			tb.window)
	}, tb.window)
}

// buildAndValidateTemplate extracts template building logic for reuse
func (tb *TemplateBuilderDialog) buildAndValidateTemplate() (models.JobSpec, []string, []error) {
	// Get analysis code from searchable widget
	analysisCode := tb.analysisCodeSearchable.Selected()

	template := models.JobSpec{
		Directory:       "./Run_${index}", // Always use this pattern
		JobName:         strings.TrimSpace(tb.jobNameEntry.Text),
		AnalysisCode:    analysisCode,
		AnalysisVersion: strings.TrimSpace(tb.analysisVersionEntry.Text),
		Command:         strings.TrimSpace(tb.commandEntry.Text),
		CoreType:        tb.coreTypeSearchable.Selected(), // Use searchable widget
		SubmitMode:      tb.submitModeSelect.Selected,
		ProjectID:       strings.TrimSpace(tb.projectIDEntry.Text),
	}

	var parseErrors []string

	// Parse numeric fields
	coresPerSlot, err := strconv.Atoi(strings.TrimSpace(tb.coresPerSlotEntry.Text))
	if err != nil {
		parseErrors = append(parseErrors, "Cores Per Slot must be a valid number")
	} else {
		template.CoresPerSlot = coresPerSlot
	}

	walltime, err := strconv.ParseFloat(strings.TrimSpace(tb.walltimeEntry.Text), 64)
	if err != nil {
		parseErrors = append(parseErrors, "Walltime must be a valid number")
	} else {
		template.WalltimeHours = walltime
	}

	// Slots always set to 1 (not shown in UI)
	template.Slots = 1

	// Parse tags (optional)
	tagsText := strings.TrimSpace(tb.tagsEntry.Text)
	if tagsText != "" {
		tags := strings.Split(tagsText, ",")
		for _, tag := range tags {
			trimmed := strings.TrimSpace(tag)
			if trimmed != "" {
				template.Tags = append(template.Tags, trimmed)
			}
		}
	}

	// Build license JSON (optional)
	licenseKey := tb.getLicenseKey()
	licenseValue := strings.TrimSpace(tb.licenseValueEntry.Text)

	if licenseKey != "" && licenseValue != "" {
		licenseJSON, err := BuildLicenseJSON(licenseKey, licenseValue)
		if err != nil {
			parseErrors = append(parseErrors, fmt.Sprintf("License settings error: %v", err))
		} else {
			template.LicenseSettings = licenseJSON
		}
	}
	// License settings are optional - no error if empty

	// Validate the complete template
	validationErrors := ValidateJobSpec(template)

	return template, parseErrors, validationErrors
}

// handleSubmit validates and creates the template for immediate use
func (tb *TemplateBuilderDialog) handleSubmit() {
	// Build and validate template
	template, parseErrors, validationErrors := tb.buildAndValidateTemplate()

	// Show errors if any
	if len(parseErrors) > 0 {
		dialog.ShowError(
			fmt.Errorf("Please fix the following errors:\n\n%s", strings.Join(parseErrors, "\n")),
			tb.window,
		)
		return
	}

	if len(validationErrors) > 0 {
		var errorMsgs []string
		for _, err := range validationErrors {
			errorMsgs = append(errorMsgs, "• "+err.Error())
		}
		dialog.ShowError(
			fmt.Errorf("Template validation failed:\n\n%s\n\nPlease fix these issues and try again.",
				strings.Join(errorMsgs, "\n")),
			tb.window,
		)
		return
	}

	// Template is valid, close dialog and use it immediately
	tb.dialog.Hide()

	if tb.onTemplateCreated != nil {
		tb.onTemplateCreated(template)
	}
}

// handleScanSoftware fetches available software from Rescale API
func (tb *TemplateBuilderDialog) handleScanSoftware() {
	// Disable button during scan
	tb.scanSoftwareBtn.SetText("Scanning...")
	tb.scanSoftwareBtn.Disable()

	go func() {
		// v3.4.0: Panic recovery to prevent GUI crashes
		defer func() {
			if r := recover(); r != nil {
				guiLogger.Error().Msgf("PANIC in software scan: %v", r)
				fyne.Do(func() {
					tb.scanSoftwareBtn.SetText("Scan Available Software")
					tb.scanSoftwareBtn.Enable()
					dialog.ShowError(fmt.Errorf("unexpected error during scan: %v", r), tb.window)
				})
			}
		}()

		// Fetch analyses from API via engine
		ctx := context.Background()
		analyses, err := tb.engine.GetAnalyses(ctx)

		// Re-enable button (use fyne.Do for thread safety)
		fyne.Do(func() {
			tb.scanSoftwareBtn.SetText("Scan Available Software")
			tb.scanSoftwareBtn.Enable()
		})

		if err != nil {
			fyne.Do(func() {
				dialog.ShowError(fmt.Errorf("Failed to fetch software list:\n\n%v\n\nPlease check API settings in Setup tab.", err), tb.window)
			})
			return
		}

		// Store scanned analyses
		tb.scannedAnalyses = analyses

		// Update with analysis codes
		var codes []string
		for _, a := range analyses {
			codes = append(codes, a.Code)
		}

		// Update UI (must happen on main thread)
		fyne.Do(func() {
			// Update searchable widget
			tb.analysisCodeSearchable.SetOptions(codes)

			// Also update legacy dropdown for compatibility
			tb.analysisCodeSelect.Options = codes
			tb.analysisCodeSelect.Refresh()

			dialog.ShowInformation("Software Scan Complete",
				fmt.Sprintf("Found %d software applications.\n\nType in the Analysis Code field to search and filter.", len(analyses)),
				tb.window)
		})
	}()
}

// onAnalysisCodeChanged is called when the user selects an analysis code
func (tb *TemplateBuilderDialog) onAnalysisCodeChanged(selected string) {
	// Find the selected analysis in our scanned list (if available)
	tb.selectedAnalysis = nil
	tb.selectedVersionIndex = -1

	for i := range tb.scannedAnalyses {
		if tb.scannedAnalyses[i].Code == selected {
			tb.selectedAnalysis = &tb.scannedAnalyses[i]
			break
		}
	}

	// If we found the analysis and it has versions, update the version entry
	if tb.selectedAnalysis != nil && len(tb.selectedAnalysis.Versions) > 0 {
		// Use the first version as default
		tb.selectedVersionIndex = 0
		// Nil check: entry may not exist yet during initial buildUI (SetSelected triggers this callback)
		if tb.analysisVersionEntry != nil {
			if tb.selectedAnalysis.Versions[0].Version != "" {
				tb.analysisVersionEntry.SetText(tb.selectedAnalysis.Versions[0].Version)
			} else if tb.selectedAnalysis.Versions[0].VersionCode != "" {
				tb.analysisVersionEntry.SetText(tb.selectedAnalysis.Versions[0].VersionCode)
			}
		}
	}

	// Enable hardware scan button whenever ANY non-empty software code is entered
	// (not just when it's in our scanned list - user may type a valid code directly)
	if tb.scanHardwareBtn != nil {
		if selected != "" {
			tb.scanHardwareBtn.Enable()
		} else {
			tb.scanHardwareBtn.Disable()
		}
	}
}

// handleScanHardware populates hardware options based on selected software
func (tb *TemplateBuilderDialog) handleScanHardware() {
	// Get the current software code from the searchable widget
	selectedCode := tb.analysisCodeSearchable.Selected()
	if selectedCode == "" {
		dialog.ShowError(fmt.Errorf("Please enter or select a software code first."), tb.window)
		return
	}

	// If we don't have analysis info (user typed code directly), fetch it from API
	if tb.selectedAnalysis == nil || tb.selectedAnalysis.Code != selectedCode {
		tb.scanHardwareBtn.SetText("Fetching...")
		tb.scanHardwareBtn.Disable()

		go func() {
			// v3.4.0: Panic recovery to prevent GUI crashes
			defer func() {
				if r := recover(); r != nil {
					guiLogger.Error().Msgf("PANIC in hardware scan fetch: %v", r)
					fyne.Do(func() {
						tb.scanHardwareBtn.SetText("Scan Compatible Hardware")
						tb.scanHardwareBtn.Enable()
						dialog.ShowError(fmt.Errorf("unexpected error: %v", r), tb.window)
					})
				}
			}()

			ctx := context.Background()
			analyses, err := tb.engine.GetAnalyses(ctx)

			fyne.Do(func() {
				tb.scanHardwareBtn.SetText("Scan Compatible Hardware")
				tb.scanHardwareBtn.Enable()

				if err != nil {
					dialog.ShowError(fmt.Errorf("Failed to fetch software info: %v", err), tb.window)
					return
				}

				// Find the analysis matching the entered code
				var found *models.Analysis
				for i := range analyses {
					if analyses[i].Code == selectedCode {
						found = &analyses[i]
						tb.scannedAnalyses = analyses // Store for future use
						break
					}
				}

				if found == nil {
					dialog.ShowError(fmt.Errorf("Software code '%s' not found on Rescale.\n\nPlease check the code and try again.", selectedCode), tb.window)
					return
				}

				tb.selectedAnalysis = found
				if len(found.Versions) > 0 {
					tb.selectedVersionIndex = 0
				}

				// Now continue with the hardware scan logic
				tb.showCompatibleHardware()
			})
		}()
		return
	}

	// We already have the analysis info, show compatible hardware
	tb.showCompatibleHardware()
}

// showCompatibleHardware displays compatible hardware for the selected software
func (tb *TemplateBuilderDialog) showCompatibleHardware() {
	if tb.selectedAnalysis == nil || len(tb.selectedAnalysis.Versions) == 0 {
		dialog.ShowInformation("Hardware",
			"No version information available for this software.\n\nAll coretypes are available.",
			tb.window)
		return
	}

	// Get allowed core types from the selected version
	version := tb.selectedAnalysis.Versions[tb.selectedVersionIndex]
	if len(version.AllowedCoreTypes) == 0 {
		// No filtering - show all core types
		dialog.ShowInformation("Hardware",
			"No coretype restrictions for this software version.\n\nAll coretypes are available.",
			tb.window)
		return
	}

	// Update the core type searchable dropdown with allowed types only
	tb.coreTypeSearchable.SetOptions(version.AllowedCoreTypes)

	// Select the first allowed type if current selection is invalid
	currentSelection := tb.coreTypeSearchable.Selected()
	isValid := false
	for _, ct := range version.AllowedCoreTypes {
		if ct == currentSelection {
			isValid = true
			break
		}
	}
	if !isValid && len(version.AllowedCoreTypes) > 0 {
		tb.coreTypeSearchable.SetSelected(version.AllowedCoreTypes[0])
	}

	dialog.ShowInformation("Hardware Scan Complete",
		fmt.Sprintf("Found %d compatible coretypes for %s.\n\nSelect one from the Coretype dropdown.",
			len(version.AllowedCoreTypes), tb.selectedAnalysis.Name),
		tb.window)
}

// getLicenseKey returns the license key based on selected type
func (tb *TemplateBuilderDialog) getLicenseKey() string {
	selected := tb.licenseTypeSelect.Selected
	if selected == "" {
		return ""
	}

	if selected == "Custom" {
		// For custom, we'd need another field, for now use a default
		return "CUSTOM_LICENSE"
	}

	licenseTypes := GetCommonLicenseTypes()
	for _, lt := range licenseTypes {
		if lt.DisplayName == selected {
			return lt.Key
		}
	}

	return ""
}

// handleSaveAsSGE saves the current template as an SGE script
func (tb *TemplateBuilderDialog) handleSaveAsSGE() {
	// Build and validate the template
	template, parseErrors, validationErrors := tb.buildAndValidateTemplate()

	// Show errors if any
	if len(parseErrors) > 0 {
		dialog.ShowError(
			fmt.Errorf("Please fix the following errors:\n\n%s", strings.Join(parseErrors, "\n")),
			tb.window,
		)
		return
	}

	if len(validationErrors) > 0 {
		var errorMsgs []string
		for _, err := range validationErrors {
			errorMsgs = append(errorMsgs, "• "+err.Error())
		}
		dialog.ShowError(
			fmt.Errorf("Template validation failed:\n\n%s\n\nPlease fix these issues and try again.",
				strings.Join(errorMsgs, "\n")),
			tb.window,
		)
		return
	}

	// Template is valid, ask where to save it
	dialog.ShowFileSave(func(writer fyne.URIWriteCloser, err error) {
		if err != nil {
			dialog.ShowError(err, tb.window)
			return
		}
		if writer == nil {
			return // User cancelled
		}
		defer writer.Close()

		// Convert JobSpec to SGEMetadata and generate script
		metadata := parser.JobSpecToSGEMetadata(template)
		script := metadata.ToSGEScript()

		if _, err := writer.Write([]byte(script)); err != nil {
			dialog.ShowError(fmt.Errorf("Failed to write SGE script: %w", err), tb.window)
			return
		}

		dialog.ShowInformation("Template Saved",
			fmt.Sprintf("SGE script saved to:\n%s", writer.URI().Path()),
			tb.window)
	}, tb.window)
}

// handleSaveAsJSON saves the current template as a JSON file
func (tb *TemplateBuilderDialog) handleSaveAsJSON() {
	// Build and validate the template
	template, parseErrors, validationErrors := tb.buildAndValidateTemplate()

	// Show errors if any
	if len(parseErrors) > 0 {
		dialog.ShowError(
			fmt.Errorf("Please fix the following errors:\n\n%s", strings.Join(parseErrors, "\n")),
			tb.window,
		)
		return
	}

	if len(validationErrors) > 0 {
		var errorMsgs []string
		for _, err := range validationErrors {
			errorMsgs = append(errorMsgs, "• "+err.Error())
		}
		dialog.ShowError(
			fmt.Errorf("Template validation failed:\n\n%s\n\nPlease fix these issues and try again.",
				strings.Join(errorMsgs, "\n")),
			tb.window,
		)
		return
	}

	// Template is valid, ask where to save it
	dialog.ShowFileSave(func(writer fyne.URIWriteCloser, err error) {
		if err != nil {
			dialog.ShowError(err, tb.window)
			return
		}
		if writer == nil {
			return // User cancelled
		}
		defer writer.Close()

		// Validate and adjust file extension
		filePath := writer.URI().Path()
		if !strings.HasSuffix(strings.ToLower(filePath), ".json") {
			filePath = filePath + ".json"
		}

		// Save template as JSON
		err = config.SaveJobJSON(filePath, template)
		if err != nil {
			dialog.ShowError(fmt.Errorf("Failed to save template: %w", err), tb.window)
			return
		}

		dialog.ShowInformation("Template Saved",
			fmt.Sprintf("JSON template saved to:\n%s", filePath),
			tb.window)
	}, tb.window)
}

// NOTE: handleLoadFromSGE and populateFormFromJob were removed as dead code
// when the "Load from SGE" button was removed from the dialog.
// The "Save as SGE" functionality is still available.
