package parser

import (
	"bufio"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/rescale/rescale-int/internal/models"
	"github.com/rescale/rescale-int/internal/pur/validation"
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

	Automations []string // Automation IDs to attach to the job

	// Advanced settings
	InboundSSHCIDR             string
	PublicKey                  string
	UseLicense                 bool
	EnvVariables               map[string]string
	UserDefinedLicenseSettings *models.UserDefinedLicense

	// licenseSettingsLine is the script line UserDefinedLicenseSettings came
	// from, for SGEMetadataToJobSpec to name.
	licenseSettingsLine int

	// Input files referenced in script
	InputFiles []string
}

// ParseOptions controls optional behavior during SGE script parsing.
type ParseOptions struct {
	CompatDefaults bool // Fill missing fields with CLI-compatible defaults before validation
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
			"user_license_settings": regexp.MustCompile(`^#RESCALE_USER_DEFINED_LICENSE_SETTINGS(?:=|\s+|$)(.*)`),
			"automation":            regexp.MustCompile(`^#RESCALE_AUTOMATION\s+(\S+)`),
		},
	}
}

// Parse reads an SGE script and extracts metadata.
func (p *SGEParser) Parse(scriptPath string) (*SGEMetadata, error) {
	return p.ParseWithOptions(scriptPath, ParseOptions{})
}

// ParseWithOptions reads an SGE script and extracts metadata with configurable behavior.
func (p *SGEParser) ParseWithOptions(scriptPath string, opts ParseOptions) (*SGEMetadata, error) {
	file, err := os.Open(scriptPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open script: %w", err)
	}
	defer file.Close()

	metadata := &SGEMetadata{
		EnvVariables: make(map[string]string),
		InputFiles:   []string{},
		Tags:         []string{},
		Automations:  []string{},
	}

	var scriptBodyLines []string
	scanner := bufio.NewScanner(file)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()

		// SGE qsub format: #$ -l key=value[,key=value,...]
		// #RESCALE_* directives take precedence if both are present.
		if strings.HasPrefix(line, "#$ -l ") {
			p.parseQsubDirective(line[6:], metadata)
			continue
		}

		// #$ -N NAME: SGE job name directive (only sets if not already set by #RESCALE_NAME)
		if strings.HasPrefix(line, "#$ -N ") {
			if name := strings.TrimSpace(line[6:]); name != "" && metadata.Name == "" {
				metadata.Name = name
			}
			continue
		}

		// #$ -pe smp N: SGE parallel environment cores per slot (only sets if not already set)
		if strings.HasPrefix(line, "#$ -pe smp ") {
			if val := strings.TrimSpace(line[11:]); val != "" && metadata.CoresPerSlot == 0 {
				if v, err := strconv.Atoi(val); err == nil && v > 0 {
					metadata.CoresPerSlot = v
				}
			}
			continue
		}

		// Collect non-comment, non-empty, non-shebang lines as potential script body
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			scriptBodyLines = append(scriptBodyLines, trimmed)
		}

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
			// The API's userDefinedLicenseSettings object as JSON, after "="
			// (rescale-cli's spelling), a space, or nothing at all. Refused unless
			// it decodes into feature sets: sent on as text, or dropped, it would
			// leave the job without the license queuing the script asks for.
			settings, err := decodeLicenseSettings(strings.TrimSpace(matches[1]))
			if err != nil {
				return nil, fmt.Errorf("invalid RESCALE_USER_DEFINED_LICENSE_SETTINGS at line %d: %w; write it as %s",
					lineNum, err, licenseSettingsExample)
			}
			metadata.UserDefinedLicenseSettings, metadata.licenseSettingsLine = settings, lineNum
		} else if matches := p.patterns["automation"].FindStringSubmatch(line); matches != nil {
			automationID := strings.TrimSpace(matches[1])
			if automationID != "" {
				metadata.Automations = append(metadata.Automations, automationID)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading script: %w", err)
	}

	// Script body as command fallback: if #RESCALE_COMMAND is absent,
	// use the collected non-comment script body as the command.
	if metadata.Command == "" && len(scriptBodyLines) > 0 {
		metadata.Command = strings.Join(scriptBodyLines, "\n")
	}

	// Apply CLI-compatible defaults for fields not set by any directive.
	// Only active when called from compat submit path.
	if opts.CompatDefaults {
		if metadata.Name == "" {
			metadata.Name = "Unnamed Job"
		}
		if metadata.CoreType == "" {
			metadata.CoreType = "emerald"
		}
		if metadata.CoresPerSlot == 0 {
			metadata.CoresPerSlot = 1
		}
		if metadata.Walltime == 0 {
			metadata.Walltime = 48
		}
		if metadata.Analysis == "" {
			metadata.Analysis = "user_included"
		}
	}

	// Validate required fields
	if err := p.validate(metadata); err != nil {
		return nil, err
	}

	return metadata, nil
}

// licenseSettingsExample is the shape #RESCALE_USER_DEFINED_LICENSE_SETTINGS
// takes, shown when another is refused.
const licenseSettingsExample = `{"featureSets":[{"name":"USER_SPECIFIED_0",` +
	`"features":[{"name":"<feature>","count":<seats>}]}]}`

// decodeLicenseSettings reads the directive's value strictly: a key the API's
// object does not have is refused, where a misspelt "count" would otherwise go
// out as zero seats. Its errors say what is wrong without the decoder's Go
// type names.
func decodeLicenseSettings(value string) (*models.UserDefinedLicense, error) {
	dec := json.NewDecoder(strings.NewReader(value))
	dec.DisallowUnknownFields()
	settings := &models.UserDefinedLicense{}
	var typeErr *json.UnmarshalTypeError
	switch err := dec.Decode(settings); {
	case err == io.EOF:
		return nil, errors.New("it has no value")
	case errors.Is(err, io.ErrUnexpectedEOF):
		return nil, errors.New("the JSON ends early")
	case errors.As(err, &typeErr):
		return nil, fmt.Errorf("%s cannot be a JSON %s", cmp.Or(typeErr.Field, "the value"), typeErr.Value)
	case err != nil:
		return nil, errors.New(strings.TrimPrefix(err.Error(), "json: "))
	}
	// Decode stops after one value, so text after it would go unread.
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("there is more after the JSON object")
	}
	if len(settings.FeatureSets) == 0 {
		return nil, errors.New("it has no feature sets")
	}
	return settings, nil
}

// parseQsubDirective parses a #$ -l directive value like "rescale_code=openfoam,rescale_cores=4".
// #RESCALE_* directives take precedence: qsub values only fill empty fields.
func (p *SGEParser) parseQsubDirective(directive string, m *SGEMetadata) {
	pairs := strings.Split(directive, ",")
	for _, pair := range pairs {
		kv := strings.SplitN(strings.TrimSpace(pair), "=", 2)
		if len(kv) != 2 {
			continue
		}
		key, val := strings.TrimSpace(kv[0]), strings.TrimSpace(kv[1])
		switch key {
		case "rescale_code":
			if m.Analysis == "" {
				m.Analysis = val
			}
		case "rescale_coretype":
			if m.CoreType == "" {
				m.CoreType = val
			}
		case "rescale_cores":
			if m.CoresPerSlot == 0 {
				if v, err := strconv.Atoi(val); err == nil && v > 0 {
					m.CoresPerSlot = v
				}
			}
		case "rescale_walltime":
			if m.Walltime == 0 {
				if v, err := strconv.Atoi(val); err == nil && v > 0 {
					m.Walltime = v
				}
			}
		case "rescale_name":
			if m.Name == "" {
				m.Name = val
			}
		}
	}
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
	// RESCALE_WALLTIME is in HOURS. Reject implausibly large values, which almost
	// always mean a legacy seconds-style script. The common seconds values —
	// 3600 (1h), 7200 (2h), 86400 (24h) — all far exceed any realistic single-job
	// walltime, so they are caught here and surfaced with a clear error instead
	// of silently submitting a job of thousands of hours.
	if m.Walltime > maxWalltimeHours {
		return fmt.Errorf("RESCALE_WALLTIME=%d is too large — this value is in HOURS, not seconds. "+
			"Values like 3600, 7200, or 86400 look like a legacy seconds-based script; convert to hours "+
			"(e.g. 3600 seconds = 1, 86400 seconds = 24)", m.Walltime)
	}
	return nil
}

// maxWalltimeHours is the upper bound for a plausible single-job walltime in
// hours (2 weeks). Larger values are rejected as almost-certain legacy
// seconds-based input (3600, 7200, 86400, ...).
const maxWalltimeHours = 336

// ToJobRequest converts SGE metadata to a Rescale API JobRequest. It refuses a
// license feature the platform would take but no job can use, naming its line;
// loading the script into the job template keeps such a feature, for the
// template's validation to report.
func (m *SGEMetadata) ToJobRequest() (*models.JobRequest, error) {
	if err := m.checkLicenseFeatures(); err != nil {
		return nil, fmt.Errorf("invalid RESCALE_USER_DEFINED_LICENSE_SETTINGS at line %d: %w", m.licenseSettingsLine, err)
	}

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
		// From #RESCALE_INBOUND_SSH_CIDR and #RESCALE_PUBLIC_KEY. Both are
		// required for the job to accept an SSH connection, so a script that
		// declares them has to reach the create call with them.
		CIDRRule:  m.InboundSSHCIDR,
		PublicKey: m.PublicKey,
	}

	if m.UserDefinedLicenseSettings != nil {
		jobReq.JobAnalyses[0].UserDefinedLicenseSettings = m.UserDefinedLicenseSettings
	}

	// Add automations if specified
	if len(m.Automations) > 0 {
		jobReq.JobAutomations = make([]models.JobAutomationRequest, len(m.Automations))
		for i, autoID := range m.Automations {
			jobReq.JobAutomations[i] = models.JobAutomationRequest{
				Automation:           models.AutomationRef{ID: autoID},
				EnvironmentVariables: map[string]string{},
			}
		}
	}

	return jobReq, nil
}

// checkLicenseFeatures holds every feature to the job template's rule, a name
// and a count above zero, and every set to having a feature.
func (m *SGEMetadata) checkLicenseFeatures() error {
	if m.UserDefinedLicenseSettings == nil {
		return nil
	}
	for _, set := range m.UserDefinedLicenseSettings.FeatureSets {
		if len(set.Features) == 0 {
			return fmt.Errorf("feature set %q has no features", set.Name)
		}
		for _, feature := range set.Features {
			if feature.Name == "" && feature.Count == 0 {
				return fmt.Errorf("feature set %q has a feature with no name and no count", set.Name)
			}
			if err := validation.ValidateLicensePair(feature.Name, feature.Count); err != nil {
				return err
			}
		}
	}
	return nil
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
	sb.WriteString(fmt.Sprintf("Walltime: %d hours\n", m.Walltime))

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
	if m.UserDefinedLicenseSettings != nil {
		settings, _ := json.Marshal(m.UserDefinedLicenseSettings) // strings and ints: cannot fail
		sb.WriteString(fmt.Sprintf("#RESCALE_USER_DEFINED_LICENSE_SETTINGS %s\n", settings))
	}

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
	// Walltime is in hours (the Rescale API unit). Round fractional hours up,
	// minimum 1.
	walltimeHours := int(job.WalltimeHours)
	if float64(walltimeHours) < job.WalltimeHours {
		walltimeHours++
	}
	if walltimeHours <= 0 {
		walltimeHours = 1
	}

	// Set default slots if not specified
	slots := job.Slots
	if slots <= 0 {
		slots = 1
	}

	m := &SGEMetadata{
		Name:            job.JobName,
		Command:         job.Command,
		Analysis:        job.AnalysisCode,
		AnalysisVersion: job.AnalysisVersion,
		CoreType:        job.CoreType,
		CoresPerSlot:    job.CoresPerSlot,
		Slots:           slots,
		Walltime:        walltimeHours,
		Tags:            job.Tags,
		ProjectID:       job.ProjectID,
		Automations:     job.Automations,
		InboundSSHCIDR:  job.CIDRRule,
		PublicKey:       job.PublicKey,
		// Note: LicenseSettings JSON from CSV doesn't map directly to SGE fields
		// UseLicense could be derived from LicenseSettings if needed
		EnvVariables: make(map[string]string),
	}
	// Written when either half is set, as the CSV and JSON saves do, so half a
	// pair loaded from a jobs file loads back as it was, for validation to report.
	if job.LicenseFeatureName != "" || job.LicensesPerJob != 0 {
		m.UserDefinedLicenseSettings = models.NewUserDefinedLicense(job.LicenseFeatureName, job.LicensesPerJob)
	}
	return m
}

// SGEMetadataToJobSpec converts SGEMetadata to JobSpec for GUI use.
// This enables loading SGE scripts into the job configuration UI.
func SGEMetadataToJobSpec(m *SGEMetadata) (models.JobSpec, error) {
	// Walltime is already in hours (the Rescale API unit).
	walltimeHours := float64(m.Walltime)
	if walltimeHours <= 0 {
		walltimeHours = 1.0 // Default to 1 hour
	}

	// Set default slots if not specified
	slots := m.Slots
	if slots <= 0 {
		slots = 1
	}

	spec := models.JobSpec{
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
		Automations:     m.Automations,
		CIDRRule:        m.InboundSSHCIDR,
		PublicKey:       m.PublicKey,
		// Note: InputFiles from script are stored in SGEMetadata.InputFiles
		// and should be handled separately by the caller
	}
	// The template holds one license feature. Any other shape would load with
	// part of it dropped, so it is refused instead.
	if ls := m.UserDefinedLicenseSettings; ls != nil {
		if len(ls.FeatureSets) != 1 || len(ls.FeatureSets[0].Features) != 1 {
			return models.JobSpec{}, fmt.Errorf("RESCALE_USER_DEFINED_LICENSE_SETTINGS at line %d does not fit the job "+
				"template: the template holds one license feature, and this is not one feature set with one feature",
				m.licenseSettingsLine)
		}
		feature := ls.FeatureSets[0].Features[0]
		spec.LicenseFeatureName, spec.LicensesPerJob = feature.Name, feature.Count
	}
	return spec, nil
}
