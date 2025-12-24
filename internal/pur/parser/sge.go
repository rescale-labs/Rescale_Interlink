package parser

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/rescale/rescale-int/internal/models"
)

// SGEMetadata represents parsed metadata from an SGE script
type SGEMetadata struct {
	// Core job settings
	Name            string
	Command         string
	Analysis        string
	AnalysisVersion string
	CoreType        string
	CoresPerSlot    int
	Slots           int
	Walltime        int

	// Metadata
	Tags      []string
	ProjectID string

	// Automations (v3.6.1)
	Automations []string // Automation IDs to attach to the job

	// Advanced settings
	InboundSSHCIDR             string
	PublicKey                  string
	UseLicense                 bool
	EnvVariables               map[string]string
	UserDefinedLicenseSettings string

	// Input files referenced in script
	InputFiles []string
}

// SGEParser parses SGE-style scripts with #RESCALE_* metadata comments
type SGEParser struct {
	patterns map[string]*regexp.Regexp
}

// NewSGEParser creates a new SGE script parser
func NewSGEParser() *SGEParser {
	return &SGEParser{
		patterns: map[string]*regexp.Regexp{
			"name":                  regexp.MustCompile(`^#RESCALE_NAME\s+(.+)`),
			"command":               regexp.MustCompile(`^#RESCALE_COMMAND\s+(.+)`),
			"analysis":              regexp.MustCompile(`^#RESCALE_ANALYSIS\s+(.+)`),
			"version":               regexp.MustCompile(`^#RESCALE_ANALYSIS_VERSION\s+(.+)`),
			"cores":                 regexp.MustCompile(`^#RESCALE_CORES\s+(.+)`),
			"cores_per_slot":        regexp.MustCompile(`^#RESCALE_CORES_PER_SLOT\s+(\d+)`),
			"slots":                 regexp.MustCompile(`^#RESCALE_SLOTS\s+(\d+)`),
			"walltime":              regexp.MustCompile(`^#RESCALE_WALLTIME\s+(\d+)`),
			"tags":                  regexp.MustCompile(`^#RESCALE_TAGS\s+(.+)`),
			"project":               regexp.MustCompile(`^#RESCALE_PROJECT_ID\s+(.+)`),
			"ssh_cidr":              regexp.MustCompile(`^#RESCALE_INBOUND_SSH_CIDR\s+(.+)`),
			"public_key":            regexp.MustCompile(`^#RESCALE_PUBLIC_KEY\s+(.+)`),
			"license":               regexp.MustCompile(`^#USE_RESCALE_LICENSE\s+(true|false)`),
			"env":                   regexp.MustCompile(`^#RESCALE_ENV_(\w+)\s+(.+)`),
			"user_license_settings": regexp.MustCompile(`^#RESCALE_USER_DEFINED_LICENSE_SETTINGS\s+(.+)`),
			"automation":            regexp.MustCompile(`^#RESCALE_AUTOMATION\s+(\S+)`), // v3.6.1
		},
	}
}

// Parse reads an SGE script and extracts metadata
func (p *SGEParser) Parse(scriptPath string) (*SGEMetadata, error) {
	file, err := os.Open(scriptPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open script: %w", err)
	}
	defer file.Close()

	metadata := &SGEMetadata{
		EnvVariables: make(map[string]string),
		InputFiles:   []string{},
		Tags:         []string{},
		Automations:  []string{}, // v3.6.1
	}

	scanner := bufio.NewScanner(file)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()

		// Parse each metadata field
		if matches := p.patterns["name"].FindStringSubmatch(line); matches != nil {
			metadata.Name = strings.TrimSpace(matches[1])
		} else if matches := p.patterns["command"].FindStringSubmatch(line); matches != nil {
			metadata.Command = strings.TrimSpace(matches[1])
		} else if matches := p.patterns["analysis"].FindStringSubmatch(line); matches != nil {
			metadata.Analysis = strings.TrimSpace(matches[1])
		} else if matches := p.patterns["version"].FindStringSubmatch(line); matches != nil {
			metadata.AnalysisVersion = strings.TrimSpace(matches[1])
		} else if matches := p.patterns["cores"].FindStringSubmatch(line); matches != nil {
			metadata.CoreType = strings.TrimSpace(matches[1])
		} else if matches := p.patterns["cores_per_slot"].FindStringSubmatch(line); matches != nil {
			val, err := strconv.Atoi(matches[1])
			if err != nil {
				return nil, fmt.Errorf("invalid RESCALE_CORES_PER_SLOT at line %d: %w", lineNum, err)
			}
			metadata.CoresPerSlot = val
		} else if matches := p.patterns["slots"].FindStringSubmatch(line); matches != nil {
			val, err := strconv.Atoi(matches[1])
			if err != nil {
				return nil, fmt.Errorf("invalid RESCALE_SLOTS at line %d: %w", lineNum, err)
			}
			metadata.Slots = val
		} else if matches := p.patterns["walltime"].FindStringSubmatch(line); matches != nil {
			val, err := strconv.Atoi(matches[1])
			if err != nil {
				return nil, fmt.Errorf("invalid RESCALE_WALLTIME at line %d: %w", lineNum, err)
			}
			metadata.Walltime = val
		} else if matches := p.patterns["tags"].FindStringSubmatch(line); matches != nil {
			tags := strings.Split(matches[1], ",")
			for _, tag := range tags {
				trimmed := strings.TrimSpace(tag)
				if trimmed != "" {
					metadata.Tags = append(metadata.Tags, trimmed)
				}
			}
		} else if matches := p.patterns["project"].FindStringSubmatch(line); matches != nil {
			metadata.ProjectID = strings.TrimSpace(matches[1])
		} else if matches := p.patterns["ssh_cidr"].FindStringSubmatch(line); matches != nil {
			metadata.InboundSSHCIDR = strings.TrimSpace(matches[1])
		} else if matches := p.patterns["public_key"].FindStringSubmatch(line); matches != nil {
			metadata.PublicKey = strings.TrimSpace(matches[1])
		} else if matches := p.patterns["license"].FindStringSubmatch(line); matches != nil {
			metadata.UseLicense = matches[1] == "true"
		} else if matches := p.patterns["env"].FindStringSubmatch(line); matches != nil {
			envName := matches[1]
			envValue := strings.TrimSpace(matches[2])
			metadata.EnvVariables[envName] = envValue
		} else if matches := p.patterns["user_license_settings"].FindStringSubmatch(line); matches != nil {
			metadata.UserDefinedLicenseSettings = strings.TrimSpace(matches[1])
		} else if matches := p.patterns["automation"].FindStringSubmatch(line); matches != nil {
			// v3.6.1: Support multiple automation IDs
			automationID := strings.TrimSpace(matches[1])
			if automationID != "" {
				metadata.Automations = append(metadata.Automations, automationID)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading script: %w", err)
	}

	// Validate required fields
	if err := p.validate(metadata); err != nil {
		return nil, err
	}

	return metadata, nil
}

// validate checks that required metadata fields are present
func (p *SGEParser) validate(m *SGEMetadata) error {
	if m.Name == "" {
		return fmt.Errorf("missing required field: RESCALE_NAME")
	}
	if m.Command == "" {
		return fmt.Errorf("missing required field: RESCALE_COMMAND")
	}
	if m.Analysis == "" {
		return fmt.Errorf("missing required field: RESCALE_ANALYSIS")
	}
	if m.CoreType == "" {
		return fmt.Errorf("missing required field: RESCALE_CORES")
	}
	if m.CoresPerSlot <= 0 {
		return fmt.Errorf("missing or invalid field: RESCALE_CORES_PER_SLOT must be > 0")
	}
	if m.Walltime <= 0 {
		return fmt.Errorf("missing or invalid field: RESCALE_WALLTIME must be > 0")
	}
	return nil
}

// ToJobRequest converts SGE metadata to a Rescale API JobRequest
func (m *SGEMetadata) ToJobRequest() *models.JobRequest {
	// Set default slots if not specified
	slots := m.Slots
	if slots == 0 {
		slots = 1
	}

	jobReq := &models.JobRequest{
		Name: m.Name,
		JobAnalyses: []models.JobAnalysisRequest{
			{
				Command: m.Command,
				Analysis: models.AnalysisRequest{
					Code:    m.Analysis,
					Version: m.AnalysisVersion,
				},
				Hardware: models.HardwareRequest{
					CoreType: models.CoreTypeRequest{
						Code: m.CoreType,
					},
					CoresPerSlot: m.CoresPerSlot,
					Slots:        slots,
					Walltime:     m.Walltime,
				},
				EnvVars:           m.EnvVariables,
				UseRescaleLicense: m.UseLicense,
			},
		},
		Tags:      m.Tags,
		ProjectID: m.ProjectID,
	}

	// Add user-defined license settings if provided
	if m.UserDefinedLicenseSettings != "" {
		jobReq.JobAnalyses[0].UserDefinedLicenseSettings = &m.UserDefinedLicenseSettings
	}

	// v3.6.1: Add automations if specified
	if len(m.Automations) > 0 {
		jobReq.JobAutomations = make([]models.JobAutomationRequest, len(m.Automations))
		for i, autoID := range m.Automations {
			jobReq.JobAutomations[i] = models.JobAutomationRequest{
				AutomationID: autoID,
			}
		}
	}

	return jobReq
}

// String returns a human-readable representation of the metadata
func (m *SGEMetadata) String() string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Job Name: %s\n", m.Name))
	sb.WriteString(fmt.Sprintf("Command: %s\n", m.Command))
	sb.WriteString(fmt.Sprintf("Analysis: %s", m.Analysis))
	if m.AnalysisVersion != "" {
		sb.WriteString(fmt.Sprintf(" (v%s)", m.AnalysisVersion))
	}
	sb.WriteString(fmt.Sprintf("\nHardware: %s (%d cores/slot, %d slots)\n",
		m.CoreType, m.CoresPerSlot, m.Slots))
	sb.WriteString(fmt.Sprintf("Walltime: %d seconds\n", m.Walltime))

	if len(m.Tags) > 0 {
		sb.WriteString(fmt.Sprintf("Tags: %s\n", strings.Join(m.Tags, ", ")))
	}
	if m.ProjectID != "" {
		sb.WriteString(fmt.Sprintf("Project ID: %s\n", m.ProjectID))
	}
	if len(m.Automations) > 0 {
		sb.WriteString(fmt.Sprintf("Automations: %s\n", strings.Join(m.Automations, ", ")))
	}
	if len(m.EnvVariables) > 0 {
		sb.WriteString("Environment Variables:\n")
		for k, v := range m.EnvVariables {
			sb.WriteString(fmt.Sprintf("  %s=%s\n", k, v))
		}
	}

	return sb.String()
}

// ToSGEScript generates an executable SGE script from metadata.
// The generated script includes a shebang, metadata comments, and the command.
func (m *SGEMetadata) ToSGEScript() string {
	var sb strings.Builder

	// Shebang and header
	sb.WriteString("#!/bin/bash\n")
	sb.WriteString("#\n")
	sb.WriteString("# Rescale job script - generated by rescale-int GUI\n")
	sb.WriteString("#\n\n")

	// Required metadata
	sb.WriteString(fmt.Sprintf("#RESCALE_NAME %s\n", m.Name))
	sb.WriteString(fmt.Sprintf("#RESCALE_ANALYSIS %s\n", m.Analysis))
	if m.AnalysisVersion != "" {
		sb.WriteString(fmt.Sprintf("#RESCALE_ANALYSIS_VERSION %s\n", m.AnalysisVersion))
	}
	sb.WriteString(fmt.Sprintf("#RESCALE_CORES %s\n", m.CoreType))
	sb.WriteString(fmt.Sprintf("#RESCALE_CORES_PER_SLOT %d\n", m.CoresPerSlot))
	if m.Slots > 0 {
		sb.WriteString(fmt.Sprintf("#RESCALE_SLOTS %d\n", m.Slots))
	}
	sb.WriteString(fmt.Sprintf("#RESCALE_WALLTIME %d\n", m.Walltime))

	// Optional metadata
	if len(m.Tags) > 0 {
		sb.WriteString(fmt.Sprintf("#RESCALE_TAGS %s\n", strings.Join(m.Tags, ",")))
	}
	if m.ProjectID != "" {
		sb.WriteString(fmt.Sprintf("#RESCALE_PROJECT_ID %s\n", m.ProjectID))
	}
	if m.InboundSSHCIDR != "" {
		sb.WriteString(fmt.Sprintf("#RESCALE_INBOUND_SSH_CIDR %s\n", m.InboundSSHCIDR))
	}
	if m.PublicKey != "" {
		sb.WriteString(fmt.Sprintf("#RESCALE_PUBLIC_KEY %s\n", m.PublicKey))
	}
	if m.UseLicense {
		sb.WriteString("#USE_RESCALE_LICENSE true\n")
	}
	if m.UserDefinedLicenseSettings != "" {
		sb.WriteString(fmt.Sprintf("#RESCALE_USER_DEFINED_LICENSE_SETTINGS %s\n", m.UserDefinedLicenseSettings))
	}

	// Automations (v3.6.1)
	for _, autoID := range m.Automations {
		sb.WriteString(fmt.Sprintf("#RESCALE_AUTOMATION %s\n", autoID))
	}

	// Environment variables
	for name, value := range m.EnvVariables {
		sb.WriteString(fmt.Sprintf("#RESCALE_ENV_%s %s\n", name, value))
	}

	// Command as metadata comment
	sb.WriteString(fmt.Sprintf("#RESCALE_COMMAND %s\n", m.Command))

	// Command as executable script body
	sb.WriteString("\n")
	sb.WriteString("# Execute the command\n")
	sb.WriteString(m.Command + "\n")

	return sb.String()
}

// JobSpecToSGEMetadata converts a JobSpec to SGEMetadata for script generation.
// This enables saving job configurations as SGE scripts.
func JobSpecToSGEMetadata(job models.JobSpec) *SGEMetadata {
	// Convert walltime from hours to seconds
	walltimeSeconds := int(job.WalltimeHours * 3600)
	if walltimeSeconds <= 0 {
		walltimeSeconds = 3600 // Default to 1 hour
	}

	// Set default slots if not specified
	slots := job.Slots
	if slots <= 0 {
		slots = 1
	}

	return &SGEMetadata{
		Name:            job.JobName,
		Command:         job.Command,
		Analysis:        job.AnalysisCode,
		AnalysisVersion: job.AnalysisVersion,
		CoreType:        job.CoreType,
		CoresPerSlot:    job.CoresPerSlot,
		Slots:           slots,
		Walltime:        walltimeSeconds,
		Tags:            job.Tags,
		ProjectID:       job.ProjectID,
		Automations:     job.Automations, // v3.6.1
		// Note: LicenseSettings JSON from CSV doesn't map directly to SGE fields
		// UseLicense could be derived from LicenseSettings if needed
		EnvVariables: make(map[string]string),
	}
}

// SGEMetadataToJobSpec converts SGEMetadata to JobSpec for GUI use.
// This enables loading SGE scripts into the job configuration UI.
func SGEMetadataToJobSpec(m *SGEMetadata) models.JobSpec {
	// Convert walltime from seconds to hours
	walltimeHours := float64(m.Walltime) / 3600.0
	if walltimeHours <= 0 {
		walltimeHours = 1.0 // Default to 1 hour
	}

	// Set default slots if not specified
	slots := m.Slots
	if slots <= 0 {
		slots = 1
	}

	return models.JobSpec{
		JobName:         m.Name,
		Command:         m.Command,
		AnalysisCode:    m.Analysis,
		AnalysisVersion: m.AnalysisVersion,
		CoreType:        m.CoreType,
		CoresPerSlot:    m.CoresPerSlot,
		Slots:           slots,
		WalltimeHours:   walltimeHours,
		Tags:            m.Tags,
		ProjectID:       m.ProjectID,
		Automations:     m.Automations, // v3.6.1
		// Note: InputFiles from script are stored in SGEMetadata.InputFiles
		// and should be handled separately by the caller
	}
}
