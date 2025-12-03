package gui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/rescale/rescale-int/internal/models"
)

// JobEditorDialog allows editing an individual job after scan
// Version 2.7.1 - CSV-less PUR GUI feature
type JobEditorDialog struct {
	window   fyne.Window
	apiCache *APICache
	dialog   dialog.Dialog

	// Job being edited
	jobIndex int
	job      *models.JobSpec

	// Callback when job is saved
	onJobSaved func(index int, job models.JobSpec)

	// Job Configuration
	jobNameEntry         *widget.Entry
	analysisCodeEntry    *widget.Entry
	analysisVersionEntry *widget.Entry
	commandEntry         *widget.Entry

	// Hardware Configuration
	coreTypeSelect    *widget.Select
	coresPerSlotEntry *widget.Entry
	walltimeEntry     *widget.Entry

	// Project Configuration
	projectIDEntry *widget.Entry
	tagsEntry      *widget.Entry

	// License Configuration
	licenseTypeSelect *widget.Select
	licenseValueEntry *widget.Entry

	// Submit Options
	submitModeSelect *widget.Select

	// Directory (read-only display)
	directoryLabel *widget.Label
}

// NewJobEditorDialog creates a new job editor dialog
func NewJobEditorDialog(window fyne.Window, apiCache *APICache, jobIndex int, job *models.JobSpec, onSaved func(int, models.JobSpec)) *JobEditorDialog {
	return &JobEditorDialog{
		window:     window,
		apiCache:   apiCache,
		jobIndex:   jobIndex,
		job:        job,
		onJobSaved: onSaved,
	}
}

// Show displays the job editor dialog
func (je *JobEditorDialog) Show() {
	je.buildUI()
}

// buildUI constructs the job editor form
func (je *JobEditorDialog) buildUI() {
	job := je.job

	// Directory (read-only)
	je.directoryLabel = widget.NewLabel(job.Directory)

	// Job Configuration Section
	je.jobNameEntry = widget.NewEntry()
	je.jobNameEntry.SetText(job.JobName)

	je.analysisCodeEntry = widget.NewEntry()
	je.analysisCodeEntry.SetText(job.AnalysisCode)

	je.analysisVersionEntry = widget.NewEntry()
	je.analysisVersionEntry.SetText(job.AnalysisVersion)

	je.commandEntry = widget.NewEntry()
	je.commandEntry.SetText(job.Command)
	je.commandEntry.MultiLine = true
	je.commandEntry.Wrapping = fyne.TextWrapWord

	// Hardware Configuration Section
	coreTypes, isLoading, err := je.apiCache.GetCoreTypes()
	var coreTypeOptions []string

	if isLoading {
		coreTypeOptions = []string{"Loading core types..."}
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

	// Ensure current job's core type is in the list
	if job.CoreType != "" {
		found := false
		for _, ct := range coreTypeOptions {
			if ct == job.CoreType {
				found = true
				break
			}
		}
		if !found {
			coreTypeOptions = append([]string{job.CoreType}, coreTypeOptions...)
		}
	}

	je.coreTypeSelect = widget.NewSelect(coreTypeOptions, nil)
	if job.CoreType != "" {
		je.coreTypeSelect.SetSelected(job.CoreType)
	}

	je.coresPerSlotEntry = widget.NewEntry()
	je.coresPerSlotEntry.SetText(strconv.Itoa(job.CoresPerSlot))

	je.walltimeEntry = widget.NewEntry()
	je.walltimeEntry.SetText(fmt.Sprintf("%.1f", job.WalltimeHours))

	// Project Configuration Section
	je.projectIDEntry = widget.NewEntry()
	je.projectIDEntry.SetText(job.ProjectID)

	je.tagsEntry = widget.NewEntry()
	if len(job.Tags) > 0 {
		je.tagsEntry.SetText(strings.Join(job.Tags, ", "))
	}

	// License Configuration Section
	licenseTypes := GetCommonLicenseTypes()
	var licenseTypeNames []string
	for _, lt := range licenseTypes {
		licenseTypeNames = append(licenseTypeNames, lt.DisplayName)
	}
	licenseTypeNames = append(licenseTypeNames, "Custom")

	// Parse existing license settings
	licenseKey := ""
	licenseValue := ""
	if job.LicenseSettings != "" {
		if key, value, err := ParseLicenseJSON(job.LicenseSettings); err == nil {
			licenseKey = key
			licenseValue = value
		}
	}

	// Create the entry widget BEFORE the select widget
	je.licenseValueEntry = widget.NewEntry()
	je.licenseValueEntry.SetText(licenseValue)
	je.licenseValueEntry.SetPlaceHolder("port@license-server")

	je.licenseTypeSelect = widget.NewSelect(licenseTypeNames, func(selected string) {
		for _, lt := range licenseTypes {
			if lt.DisplayName == selected {
				je.licenseValueEntry.SetPlaceHolder(lt.Placeholder)
				return
			}
		}
		je.licenseValueEntry.SetPlaceHolder("port@license-server")
	})

	// Set initial license type selection
	if licenseKey != "" {
		found := false
		for _, lt := range licenseTypes {
			if lt.Key == licenseKey {
				je.licenseTypeSelect.SetSelected(lt.DisplayName)
				found = true
				break
			}
		}
		if !found {
			je.licenseTypeSelect.SetSelected("Custom")
		}
	}

	// Submit Mode
	je.submitModeSelect = widget.NewSelect(GetSubmitModes(), nil)
	if job.SubmitMode != "" {
		je.submitModeSelect.SetSelected(job.SubmitMode)
	} else {
		je.submitModeSelect.SetSelected("create_and_submit")
	}

	// Build form layout
	form := &widget.Form{
		Items: []*widget.FormItem{
			// Directory (read-only)
			{Text: "Directory", Widget: je.directoryLabel},

			// Job Configuration
			{Text: "Job Configuration", Widget: widget.NewLabel("")},
			{Text: "Job Name", Widget: je.jobNameEntry},
			{Text: "Analysis Code", Widget: je.analysisCodeEntry},
			{Text: "Analysis Version", Widget: je.analysisVersionEntry},
			{Text: "Command", Widget: container.NewVBox(
				widget.NewLabel("Multi-line command"),
				je.commandEntry,
			)},

			// Hardware
			{Text: "Hardware", Widget: widget.NewLabel("")},
			{Text: "Coretype", Widget: je.coreTypeSelect},
			{Text: "Cores Per Slot", Widget: je.coresPerSlotEntry},
			{Text: "Walltime (hours)", Widget: je.walltimeEntry},

			// Project
			{Text: "Project", Widget: widget.NewLabel("")},
			{Text: "Project ID", Widget: je.projectIDEntry},
			{Text: "Tags (comma-separated)", Widget: je.tagsEntry},

			// License
			{Text: "License Settings", Widget: widget.NewLabel("")},
			{Text: "License Type", Widget: je.licenseTypeSelect},
			{Text: "License Value", Widget: je.licenseValueEntry},

			// Submit
			{Text: "Submit Options", Widget: widget.NewLabel("")},
			{Text: "Submit Mode", Widget: je.submitModeSelect},
		},
	}

	// Create custom buttons
	saveButton := widget.NewButton("Save Changes", func() {
		je.handleSave()
	})
	saveButton.Importance = widget.HighImportance

	cancelButton := widget.NewButton("Cancel", func() {
		je.dialog.Hide()
	})

	buttons := container.NewHBox(
		saveButton,
		cancelButton,
	)

	content := container.NewBorder(nil, buttons, nil, nil, form)

	// Create dialog and store reference
	je.dialog = dialog.NewCustomWithoutButtons(
		fmt.Sprintf("Edit Job: %s", je.job.JobName),
		content,
		je.window,
	)
	je.dialog.Resize(fyne.NewSize(650, 700))
	je.dialog.Show()
}

// handleSave validates and saves the job changes
func (je *JobEditorDialog) handleSave() {
	// Build updated job
	updatedJob := *je.job // Start with original (preserves Directory, etc.)

	updatedJob.JobName = strings.TrimSpace(je.jobNameEntry.Text)
	updatedJob.AnalysisCode = strings.TrimSpace(je.analysisCodeEntry.Text)
	updatedJob.AnalysisVersion = strings.TrimSpace(je.analysisVersionEntry.Text)
	updatedJob.Command = strings.TrimSpace(je.commandEntry.Text)
	updatedJob.CoreType = je.coreTypeSelect.Selected
	updatedJob.SubmitMode = je.submitModeSelect.Selected
	updatedJob.ProjectID = strings.TrimSpace(je.projectIDEntry.Text)

	var parseErrors []string

	// Parse numeric fields
	coresPerSlot, err := strconv.Atoi(strings.TrimSpace(je.coresPerSlotEntry.Text))
	if err != nil {
		parseErrors = append(parseErrors, "Cores Per Slot must be a valid number")
	} else {
		updatedJob.CoresPerSlot = coresPerSlot
	}

	walltime, err := strconv.ParseFloat(strings.TrimSpace(je.walltimeEntry.Text), 64)
	if err != nil {
		parseErrors = append(parseErrors, "Walltime must be a valid number")
	} else {
		updatedJob.WalltimeHours = walltime
	}

	// Parse tags (optional)
	tagsText := strings.TrimSpace(je.tagsEntry.Text)
	if tagsText != "" {
		tags := strings.Split(tagsText, ",")
		updatedJob.Tags = nil // Reset
		for _, tag := range tags {
			trimmed := strings.TrimSpace(tag)
			if trimmed != "" {
				updatedJob.Tags = append(updatedJob.Tags, trimmed)
			}
		}
	} else {
		updatedJob.Tags = nil
	}

	// Build license JSON (optional)
	licenseKey := je.getLicenseKey()
	licenseValue := strings.TrimSpace(je.licenseValueEntry.Text)

	if licenseKey != "" && licenseValue != "" {
		licenseJSON, err := BuildLicenseJSON(licenseKey, licenseValue)
		if err != nil {
			parseErrors = append(parseErrors, fmt.Sprintf("License settings error: %v", err))
		} else {
			updatedJob.LicenseSettings = licenseJSON
		}
	} else {
		updatedJob.LicenseSettings = ""
	}

	// Show errors if any
	if len(parseErrors) > 0 {
		dialog.ShowError(
			fmt.Errorf("Please fix the following errors:\n\n%s", strings.Join(parseErrors, "\n")),
			je.window,
		)
		return
	}

	// Validate the complete job
	validationErrors := ValidateJobSpec(updatedJob)
	if len(validationErrors) > 0 {
		var errorMsgs []string
		for _, err := range validationErrors {
			errorMsgs = append(errorMsgs, "• "+err.Error())
		}
		dialog.ShowError(
			fmt.Errorf("Job validation failed:\n\n%s\n\nPlease fix these issues and try again.",
				strings.Join(errorMsgs, "\n")),
			je.window,
		)
		return
	}

	// Job is valid, close dialog and save
	je.dialog.Hide()

	if je.onJobSaved != nil {
		je.onJobSaved(je.jobIndex, updatedJob)
	}
}

// getLicenseKey returns the license key based on selected type
func (je *JobEditorDialog) getLicenseKey() string {
	selected := je.licenseTypeSelect.Selected
	if selected == "" {
		return ""
	}

	if selected == "Custom" {
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

// DoubleClickDetector helps detect double-clicks on table rows
// Since Fyne doesn't have native double-click for tables
type DoubleClickDetector struct {
	lastClickTime time.Time
	lastClickRow  int
	threshold     time.Duration
}

// NewDoubleClickDetector creates a new double-click detector
func NewDoubleClickDetector() *DoubleClickDetector {
	return &DoubleClickDetector{
		threshold: 400 * time.Millisecond,
	}
}

// IsDoubleClick checks if the current click is a double-click on the same row
func (d *DoubleClickDetector) IsDoubleClick(row int) bool {
	now := time.Now()
	elapsed := now.Sub(d.lastClickTime)

	isDouble := row == d.lastClickRow && elapsed <= d.threshold

	d.lastClickTime = now
	d.lastClickRow = row

	return isDouble
}
