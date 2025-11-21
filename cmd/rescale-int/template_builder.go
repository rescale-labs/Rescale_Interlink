package main

import (
	"fmt"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/models"
)

// TemplateBuilderDialog creates a dialog for building a job template from scratch
type TemplateBuilderDialog struct {
	window   fyne.Window
	apiCache *APICache
	cfg      *config.Config
	workflow *JobsWorkflow
	dialog   dialog.Dialog // Store dialog reference so we can close it

	// Job Configuration
	jobNameEntry         *widget.Entry
	analysisCodeSelect   *widget.Select
	analysisVersionEntry *widget.Entry
	commandEntry         *widget.Entry

	// Hardware Configuration
	coreTypeSelect    *widget.Select
	coresPerSlotEntry *widget.Entry
	walltimeEntry     *widget.Entry
	slotsEntry        *widget.Entry

	// Project Configuration
	orgCodeEntry   *widget.Entry
	projectIDEntry *widget.Entry
	tagsEntry      *widget.Entry

	// License Configuration
	licenseTypeSelect *widget.Select
	licenseValueEntry *widget.Entry

	// Submit Options
	submitModeSelect *widget.Select

	// Callback when template is created
	onTemplateCreated func(models.JobSpec)
}

// NewTemplateBuilderDialog creates a new template builder dialog
func NewTemplateBuilderDialog(window fyne.Window, apiCache *APICache, cfg *config.Config, workflow *JobsWorkflow, onCreated func(models.JobSpec)) *TemplateBuilderDialog {
	return &TemplateBuilderDialog{
		window:            window,
		apiCache:          apiCache,
		cfg:               cfg,
		workflow:          workflow,
		onTemplateCreated: onCreated,
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

// buildUI constructs the template builder form
func (tb *TemplateBuilderDialog) buildUI(defaults models.JobSpec) {
	// Job Configuration Section
	tb.jobNameEntry = widget.NewEntry()
	tb.jobNameEntry.SetText(defaults.JobName)
	tb.jobNameEntry.SetPlaceHolder("Run_${index}")

	// Analysis code - use cached or default list
	analysisCodes := tb.apiCache.GetAnalysisCodes()
	tb.analysisCodeSelect = widget.NewSelect(analysisCodes, nil)
	if defaults.AnalysisCode != "" {
		tb.analysisCodeSelect.SetSelected(defaults.AnalysisCode)
	}

	tb.analysisVersionEntry = widget.NewEntry()
	tb.analysisVersionEntry.SetText(defaults.AnalysisVersion)
	tb.analysisVersionEntry.SetPlaceHolder("6-2024-hf1 Intel MPI 2021.13")

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
		// Note: Background fetch is already happening in apiCache
	} else if err != nil || len(coreTypes) == 0 {
		// Use fallback defaults
		for _, ct := range getDefaultCoreTypes() {
			coreTypeOptions = append(coreTypeOptions, ct.Code)
		}
	} else {
		for _, ct := range coreTypes {
			coreTypeOptions = append(coreTypeOptions, ct.Code)
		}
	}

	tb.coreTypeSelect = widget.NewSelect(coreTypeOptions, nil)
	if defaults.CoreType != "" {
		tb.coreTypeSelect.SetSelected(defaults.CoreType)
	}

	tb.coresPerSlotEntry = widget.NewEntry()
	tb.coresPerSlotEntry.SetText(strconv.Itoa(defaults.CoresPerSlot))
	tb.coresPerSlotEntry.SetPlaceHolder("4")

	tb.walltimeEntry = widget.NewEntry()
	tb.walltimeEntry.SetText(fmt.Sprintf("%.1f", defaults.WalltimeHours))
	tb.walltimeEntry.SetPlaceHolder("48.0")

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
	licenseTypeNames = append(licenseTypeNames, "Custom")

	// Parse existing license settings FIRST
	licenseKey := "RLM_LICENSE"
	licenseValue := "123@license-server"
	if defaults.LicenseSettings != "" {
		if key, value, err := ParseLicenseJSON(defaults.LicenseSettings); err == nil {
			licenseKey = key
			licenseValue = value
		}
	}

	// Create the entry widget BEFORE the select widget (so callback can reference it)
	tb.licenseValueEntry = widget.NewEntry()
	tb.licenseValueEntry.SetText(licenseValue)
	tb.licenseValueEntry.SetPlaceHolder("port@license-server")

	// Now create select widget with callback that references the entry
	tb.licenseTypeSelect = widget.NewSelect(licenseTypeNames, func(selected string) {
		// Update placeholder when type changes
		for _, lt := range licenseTypes {
			if lt.DisplayName == selected {
				tb.licenseValueEntry.SetPlaceHolder(lt.Placeholder)
				return
			}
		}
		tb.licenseValueEntry.SetPlaceHolder("port@license-server")
	})

	// Set initial license type selection (this will trigger callback)
	found := false
	for _, lt := range licenseTypes {
		if lt.Key == licenseKey {
			tb.licenseTypeSelect.SetSelected(lt.DisplayName)
			found = true
			break
		}
	}
	if !found {
		tb.licenseTypeSelect.SetSelected("Custom")
	}

	// Submit Mode
	tb.submitModeSelect = widget.NewSelect(GetSubmitModes(), nil)
	if defaults.SubmitMode != "" {
		tb.submitModeSelect.SetSelected(defaults.SubmitMode)
	} else {
		tb.submitModeSelect.SetSelected("create_and_submit")
	}

	// Build form layout
	form := &widget.Form{
		Items: []*widget.FormItem{
			// Job Configuration
			{Text: "Job Configuration", Widget: widget.NewLabel("")},
			{Text: "Job Name", Widget: tb.jobNameEntry},
			{Text: "Analysis Code", Widget: tb.analysisCodeSelect},
			{Text: "Analysis Version", Widget: tb.analysisVersionEntry},
			{Text: "Command", Widget: container.NewVBox(
				widget.NewLabel("Multi-line command (use ${index} for run number)"),
				tb.commandEntry,
			)},

			// Hardware
			{Text: "Hardware", Widget: widget.NewLabel("")},
			{Text: "Core Type", Widget: tb.coreTypeSelect},
			{Text: "Cores Per Slot", Widget: tb.coresPerSlotEntry},
			{Text: "Walltime (hours)", Widget: tb.walltimeEntry},
			{Text: "Slots", Widget: tb.slotsEntry},

			// Project
			{Text: "Project", Widget: widget.NewLabel("")},
			{Text: "Organization Code", Widget: tb.orgCodeEntry},
			{Text: "Project ID", Widget: tb.projectIDEntry},
			{Text: "Tags (comma-separated)", Widget: tb.tagsEntry},

			// License
			{Text: "License Settings", Widget: widget.NewLabel("")},
			{Text: "License Type", Widget: tb.licenseTypeSelect},
			{Text: "License Value", Widget: tb.licenseValueEntry},

			// Submit
			{Text: "Submit Options", Widget: widget.NewLabel("")},
			{Text: "Submit Mode", Widget: tb.submitModeSelect},
		},
	}

	// Create custom buttons
	useButton := widget.NewButton("Use Template", func() {
		tb.handleSubmit()
	})
	useButton.Importance = widget.HighImportance

	saveButton := widget.NewButton("Save Template to File...", func() {
		tb.handleSaveTemplate()
	})

	cancelButton := widget.NewButton("Cancel", func() {
		tb.dialog.Hide()
	})

	buttons := container.NewHBox(
		useButton,
		saveButton,
		cancelButton,
	)

	content := container.NewBorder(nil, buttons, nil, nil, form)

	// Create dialog and store reference
	tb.dialog = dialog.NewCustomWithoutButtons("Create Job Template", content, tb.window)
	tb.dialog.Resize(fyne.NewSize(700, 800))
	tb.dialog.Show()
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
	template := models.JobSpec{
		Directory:       "./Run_${index}", // Always use this pattern
		JobName:         strings.TrimSpace(tb.jobNameEntry.Text),
		AnalysisCode:    tb.analysisCodeSelect.Selected,
		AnalysisVersion: strings.TrimSpace(tb.analysisVersionEntry.Text),
		Command:         strings.TrimSpace(tb.commandEntry.Text),
		CoreType:        tb.coreTypeSelect.Selected,
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

	slots, err := strconv.Atoi(strings.TrimSpace(tb.slotsEntry.Text))
	if err != nil {
		parseErrors = append(parseErrors, "Slots must be a valid number")
	} else {
		template.Slots = slots
	}

	// Parse tags
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

	// Build license JSON
	licenseKey := tb.getLicenseKey()
	licenseValue := strings.TrimSpace(tb.licenseValueEntry.Text)

	if licenseKey != "" && licenseValue != "" {
		licenseJSON, err := BuildLicenseJSON(licenseKey, licenseValue)
		if err != nil {
			parseErrors = append(parseErrors, fmt.Sprintf("License settings error: %v", err))
		} else {
			template.LicenseSettings = licenseJSON
		}
	} else {
		parseErrors = append(parseErrors, "License settings are required")
	}

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
