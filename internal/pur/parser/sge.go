package parser

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/models"
	"github.com/rescale/rescale-int/internal/pur/validation"
	"github.com/rescale/rescale-int/internal/reporting"
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
	IsLowPriority   bool

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

	// A rescale-cli script names its project, and may name its core type, where
	// the job request carries the project's ID and the core type's code, which
	// ToJobRequest looks up. The lines are where the script gave them.
	projectName               string
	projectLine, coreTypeLine int

	// coresDefaulted says compat's default filled CoresPerSlot.
	coresDefaulted bool

	// InputFiles are the IDs of files already on Rescale that the job takes as
	// inputs, from #RESCALE_EXISTING_FILES.
	InputFiles []string

	// Warnings are about lines the script gives that are accepted but not
	// sent, for the caller to show.
	Warnings []string
}

// ParseOptions controls optional behavior during SGE script parsing.
type ParseOptions struct {
	CompatDefaults bool // Fill missing fields with CLI-compatible defaults before validation
}

// SGEParser parses SGE-style scripts with #RESCALE_* metadata comments
type SGEParser struct{}

// NewSGEParser creates a new SGE script parser
func NewSGEParser() *SGEParser {
	return &SGEParser{}
}

// directiveLine splits a directive line into its name, its separator and its
// value: Interlink writes "#RESCALE_NAME job1", rescale-cli
// "#RESCALE_NAME=job1", and #USE_RESCALE_LICENSE may stand alone. rescale-cli
// also reads an indented directive.
var directiveLine = regexp.MustCompile(`^\s*#(\w+)(=|\s+|$)(.*)`)

// qsubCores finds the core count on a "#$ -pe" line, the way rescale-cli does.
var qsubCores = regexp.MustCompile(`^#\$ -pe .* ([1-9]\d*)`)

// rescaleCLIEnv is rescale-cli's "#RESCALE_ENV_NAME=value", whose name runs to
// the line's last "=".
var rescaleCLIEnv = regexp.MustCompile(`^\s*#RESCALE_ENV_(.*)=(.*)`)

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
	first := map[string]int{} // the line of each directive's first "=" form
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
			if name := unquote(strings.TrimSpace(line[6:])); name != "" && metadata.Name == "" {
				metadata.Name = name
			}
			continue
		}

		// #$ -pe ENV N: cores per slot, for any parallel environment, as
		// rescale-cli reads it (only sets if not already set)
		if strings.HasPrefix(line, "#$ -pe ") {
			if c := qsubCores.FindStringSubmatch(line); c != nil && metadata.CoresPerSlot == 0 {
				metadata.CoresPerSlot, _ = strconv.Atoi(c[1])
			}
			continue
		}

		// Collect non-comment, non-empty, non-shebang lines as potential script body
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			scriptBodyLines = append(scriptBodyLines, trimmed)
		}

		// An environment line in Interlink's form, a name and a space, keeps
		// Interlink's reading, "=" in its value and all.
		var err error
		d := directiveLine.FindStringSubmatch(line)
		if e := rescaleCLIEnv.FindStringSubmatch(line); e != nil && (d == nil || d[2] == "=") {
			err = setEnv(metadata, first, e[1], strings.TrimSpace(e[2]), lineNum)
		} else if d != nil {
			err = setDirective(metadata, first, d[1], d[2] == "=", strings.TrimSpace(d[3]), lineNum)
		}
		if err != nil {
			return nil, reporting.UsageError(err) // the script's own mistake
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
			metadata.CoresPerSlot, metadata.coresDefaulted = 1, true
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
		return nil, reporting.UsageError(err)
	}

	return metadata, nil
}

// setDirective applies one directive line to m. eq says it is in rescale-cli's
// "#NAME=value" form, which reads as rescale-cli reads it; first holds the
// line of each directive's first line in that form. A directive Interlink
// cannot carry, or a known one whose value does not fit, is refused naming
// its line, where leaving it out would run the job without it.
func setDirective(m *SGEMetadata, first map[string]int, name string, eq bool, value string, line int) error {
	prev, again := first[name]
	if eq {
		// rescale-cli drops one double quote from each end of a value.
		value = unquote(value)
	}
	// blank is rescale-cli's test for a blank value, which some directives
	// read as no setting: spaces in quotes are blank too.
	blank := strings.TrimSpace(value) == ""
	// Of a directive given twice, rescale-cli reads the first line, but of
	// #RESCALE_CORES= the first that holds a count.
	if eq && !again && !(name == "RESCALE_CORES" && blank) {
		first[name] = line
	}
	env, isEnv := strings.CutPrefix(name, "RESCALE_ENV_")
	switch {
	case eq && again && (name == "RESCALE_ANALYSIS" || name == "RESCALE_ANALYSIS_VERSION"):
		return fmt.Errorf("%s at line %d: given before, at line %d, and Interlink's job runs one analysis",
			name, line, prev)
	case isEnv && env != "" && value != "": // Interlink's form; setEnv reads rescale-cli's
		m.EnvVariables[env] = value
	case eq && again && name != "RESCALE_TAGS" && name != "RESCALE_AUTOMATION":
		// rescale-cli reads a directive given twice from its first line.
	case name == "RESCALE_NAME":
		if !blank {
			m.Name = value
		}
	case name == "RESCALE_COMMAND":
		m.Command = cmp.Or(value, m.Command)
	case name == "RESCALE_ANALYSIS":
		if eq && value == "" {
			// rescale-cli would ask for an analysis with no code.
			return fmt.Errorf("invalid RESCALE_ANALYSIS at line %d: it has no value", line)
		}
		m.Analysis = cmp.Or(value, m.Analysis)
	case name == "RESCALE_ANALYSIS_VERSION":
		m.AnalysisVersion = cmp.Or(value, m.AnalysisVersion) // none: the latest version
	case name == "RESCALE_CORES" && !eq:
		// Interlink's #RESCALE_CORES names the core type, while rescale-cli's
		// #RESCALE_CORES= counts the cores and names the core type with
		// #RESCALE_CORE_TYPE=. Each spelling keeps its own tool's meaning, so
		// scripts written for either run unchanged.
		m.CoreType = cmp.Or(value, m.CoreType)
	case blank && (name == "RESCALE_CORES" || name == "RESCALE_WALLTIME"):
		// rescale-cli reads only digits from these, so spaces in quotes set
		// nothing.
	case name == "RESCALE_CORES", name == "RESCALE_CORES_PER_SLOT":
		return setCount(&m.CoresPerSlot, name, value, line)
	case name == "RESCALE_SLOTS":
		return setCount(&m.Slots, name, value, line)
	case name == "RESCALE_WALLTIME":
		return setCount(&m.Walltime, name, value, line)
	case name == "RESCALE_CORE_TYPE":
		if !blank { // a code or a name, as rescale-cli takes it
			m.CoreType, m.coreTypeLine = value, line
		}
	case name == "RESCALE_TAGS":
		for _, tag := range strings.Split(value, ",") {
			if tag = strings.TrimSpace(tag); tag != "" {
				m.Tags = append(m.Tags, tag)
			}
		}
	case name == "RESCALE_PROJECT_ID" && !eq:
		if value != "" { // an ID, which takes over from a name given before
			m.ProjectID, m.projectName, m.projectLine = value, "", 0
		}
	case name == "RESCALE_PROJECT_ID":
		// rescale-cli's #RESCALE_PROJECT_ID= gives the project's name, where
		// Interlink's gives its ID.
		if value != "" {
			m.projectName, m.projectLine = value, line
		}
	case name == "RESCALE_INBOUND_SSH_CIDR":
		if eq && value == "" {
			// rescale-cli sends the blank as the job's rule, which a request can
			// only leave out, and the platform does not always read an omitted
			// rule the same way as an empty one.
			return fmt.Errorf("RESCALE_INBOUND_SSH_CIDR at line %d: it has no value, which rescale-cli sends as an "+
				"empty setting and Interlink cannot; delete the line to use your profile's setting", line)
		}
		m.InboundSSHCIDR = cmp.Or(value, m.InboundSSHCIDR)
	case name == "RESCALE_PUBLIC_KEY":
		// A blank key sets nothing: the job's cluster takes the profile's key
		// either way.
		m.PublicKey = cmp.Or(value, m.PublicKey)
	case name == "USE_RESCALE_LICENSE":
		return setUseLicense(m, eq, value, line)
	case name == "RESCALE_USER_DEFINED_LICENSE_SETTINGS":
		// The API's userDefinedLicenseSettings object as JSON, refused unless it
		// decodes into feature sets: sent on as text, or dropped, it would leave
		// the job without the license queuing the script asks for. rescale-cli
		// reads a blank value after "=" as no setting.
		if eq && blank {
			return nil
		}
		settings, err := decodeLicenseSettings(value)
		if err != nil {
			return fmt.Errorf("invalid RESCALE_USER_DEFINED_LICENSE_SETTINGS at line %d: %w; write it as %s",
				line, err, licenseSettingsExample)
		}
		m.UserDefinedLicenseSettings, m.licenseSettingsLine = settings, line
	case name == "RESCALE_AUTOMATION":
		if f := strings.Fields(value); len(f) > 0 {
			m.Automations = append(m.Automations, f[0])
		}
	case name == "RESCALE_EXISTING_FILES":
		for _, id := range strings.Split(value, ",") {
			if id = strings.TrimSpace(id); id != "" {
				m.InputFiles = append(m.InputFiles, id)
			}
		}
	case name == "RESCALE_PRIORITY":
		// rescale-cli's billing priority, where the job request says only
		// whether the job is low priority (ON_DEMAND) or not (INSTANT).
		if value != "ON_DEMAND" && value != "INSTANT" {
			return fmt.Errorf("invalid RESCALE_PRIORITY at line %d: Interlink can set ON_DEMAND or INSTANT, not %q",
				line, value)
		}
		m.IsLowPriority = value == "ON_DEMAND"
	case name == "RESCALE_LOW_PRIORITY":
		// #RESCALE_PRIORITY= decides when both are given, as in rescale-cli.
		if _, decided := first["RESCALE_PRIORITY"]; !decided && value != "" {
			m.IsLowPriority = isTrue(value)
		}
	case name == "RESCALE_AUTO_TERMINATE_CLUSTER":
		// The platform no longer reads this setting, so the line is accepted,
		// whatever it says, and nothing is sent.
		if !blank {
			m.Warnings = append(m.Warnings, fmt.Sprintf("RESCALE_AUTO_TERMINATE_CLUSTER at line %d is ignored: "+
				"the platform no longer reads this setting", line))
		}
	case !blank && unsupported[name] != "":
		return fmt.Errorf("%s at line %d: %s", name, line, unsupported[name])
	}
	return nil
}

// unsupported are rescale-cli's directives for settings Interlink's job request
// does not carry, with what to do instead. A blank value sets nothing, as
// rescale-cli reads it.
var unsupported = map[string]string{
	"RESCALE_CORE_TYPE_SET":     "Interlink cannot submit to a core type set; give one core type with #RESCALE_CORE_TYPE=",
	"RESCALE_ONDEMAND_LICENSE":  "Interlink cannot send an on-demand license seller; delete the line",
	"RESCALE_START_JOB_ON_HOUR": "Interlink cannot set when the job starts; delete the line to use your profile's setting",
}

// setCount reads a whole number above zero into n; an empty value sets nothing.
// It reads the leading digits ("24 # hours" is 24), as rescale-cli does and
// this parser always has, and refuses a value without any rather than leave
// the job to a default.
func setCount(n *int, name, value string, line int) error {
	if value == "" {
		return nil
	}
	v, err := strconv.Atoi(value[:len(value)-len(strings.TrimLeft(value, "0123456789"))])
	if err != nil || v <= 0 {
		return fmt.Errorf("invalid %s at line %d: %q is not a whole number above zero", name, line, value)
	}
	*n = v
	return nil
}

// setEnv applies rescale-cli's #RESCALE_ENV_NAME=value. A variable given twice
// gets both values, joined as for PATH. A name no environment variable can
// have is refused, where leaving it out, or reading it another way, would run
// the job without the variable the script sets.
func setEnv(m *SGEMetadata, first map[string]int, name, value string, line int) error {
	switch {
	case name == "":
		return fmt.Errorf("invalid RESCALE_ENV_ at line %d: the variable has no name", line)
	case strings.Contains(name, "="):
		return fmt.Errorf("invalid RESCALE_ENV_ at line %d: rescale-cli reads the variable's name as %q, up to the "+
			"last \"=\", and a variable's name cannot hold \"=\"", line, name)
	}
	value = unquote(value)
	if _, again := first["RESCALE_ENV_"+name]; again {
		m.EnvVariables[name] += ":" + value
	} else {
		first["RESCALE_ENV_"+name] = line
		m.EnvVariables[name] = value
	}
	return nil
}

// setUseLicense reads #USE_RESCALE_LICENSE. rescale-cli turns the license on
// wherever the directive appears, whatever follows it; Interlink's own form
// says true or false, and may be followed by a comment.
func setUseLicense(m *SGEMetadata, eq bool, value string, line int) error {
	switch {
	case value == "" || eq && isTrue(value):
		m.UseLicense = true
	case eq:
		return fmt.Errorf("invalid USE_RESCALE_LICENSE at line %d: rescale-cli turns the license on whatever "+
			"follows \"=\"; delete the line to leave it off", line)
	case strings.EqualFold(strings.Fields(value)[0], "true"):
		m.UseLicense = true
	case strings.EqualFold(strings.Fields(value)[0], "false"):
		m.UseLicense = false
	default:
		return fmt.Errorf("invalid USE_RESCALE_LICENSE at line %d: want true or false, not %q", line, value)
	}
	return nil
}

// isTrue reads a yes-or-no value the way rescale-cli does.
func isTrue(value string) bool {
	switch strings.ToLower(value) {
	case "t", "true", "y", "yes", "on":
		return true
	}
	return false
}

// unquote drops one double quote from each end of value, as rescale-cli does.
func unquote(value string) string {
	return strings.TrimSuffix(strings.TrimPrefix(value, `"`), `"`)
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
		return fmt.Errorf("missing required field: RESCALE_CORES, the core type (rescale-cli's RESCALE_CORE_TYPE=)")
	}
	if m.CoresPerSlot <= 0 {
		return fmt.Errorf("missing or invalid field: RESCALE_CORES_PER_SLOT must be > 0 (rescale-cli's RESCALE_CORES=)")
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

// Lookup is what ToJobRequest asks the API for when a script gives its project,
// or its core type, by name. *api.Client provides it.
type Lookup interface {
	ListProjects(ctx context.Context) ([]api.Project, error)
	GetCoreTypes(ctx context.Context, includeInactive bool) ([]models.CoreType, error)
}

// ToJobRequest converts SGE metadata to a Rescale API JobRequest, looking up a
// project or core type the script gives by name. It refuses a license feature
// the platform would take but no job can use, naming its line, and a public key
// the platform would refuse; loading the script into the job template keeps
// either, for the template's validation to report.
func (m *SGEMetadata) ToJobRequest(ctx context.Context, lookup Lookup) (*models.JobRequest, error) {
	if err := m.checkLicenseFeatures(); err != nil {
		return nil, reporting.UsageError(fmt.Errorf("invalid RESCALE_USER_DEFINED_LICENSE_SETTINGS at line %d: %w",
			m.licenseSettingsLine, err))
	}
	if err := validation.ValidatePublicKey(m.PublicKey); err != nil {
		return nil, reporting.UsageError(fmt.Errorf("invalid RESCALE_PUBLIC_KEY: %w", err))
	}
	projectID, found, err := m.lookUpNames(ctx, lookup)
	if err != nil {
		return nil, err
	}
	coreType, cores := m.CoreType, m.CoresPerSlot
	if found != nil {
		// With no count in the script, rescale-cli asks for the core type's
		// smallest.
		coreType = found.Code
		if m.coresDefaulted && len(found.Cores) > 0 {
			cores = found.Cores[0]
		}
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
						Code: coreType,
					},
					CoresPerSlot: cores,
					Slots:        m.requestSlots(),
					Walltime:     m.Walltime,
				},
				EnvVars:           m.EnvVariables,
				UseRescaleLicense: m.UseLicense,
			},
		},
		IsLowPriority: m.IsLowPriority,
		Tags:          m.Tags,
		ProjectID:     projectID,
		// From #RESCALE_INBOUND_SSH_CIDR and #RESCALE_PUBLIC_KEY. Both are
		// required for the job to accept an SSH connection, so a script that
		// declares them has to reach the create call with them.
		CIDRRule:  m.InboundSSHCIDR,
		PublicKey: m.PublicKey,
	}

	if m.UserDefinedLicenseSettings != nil {
		jobReq.JobAnalyses[0].UserDefinedLicenseSettings = m.UserDefinedLicenseSettings
	}
	for _, id := range m.InputFiles {
		// Decompressed, as rescale-cli asks for them.
		jobReq.JobAnalyses[0].InputFiles = append(jobReq.JobAnalyses[0].InputFiles,
			models.InputFileRequest{ID: id, Decompress: true})
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

// lookUpNames returns the project ID the request carries, and the core type a
// #RESCALE_CORE_TYPE= line names, if the API has it. rescale-cli takes the
// first as the name of one of the user's projects and the second as a core
// type's code or name, sending a value that is neither as written.
func (m *SGEMetadata) lookUpNames(ctx context.Context, lookup Lookup) (string, *models.CoreType, error) {
	projectID := m.ProjectID
	if m.projectLine != 0 {
		projects, err := lookup.ListProjects(ctx)
		if err != nil {
			return "", nil, fmt.Errorf("RESCALE_PROJECT_ID at line %d: could not list your projects: %w", m.projectLine, err)
		}
		i := slices.IndexFunc(projects, func(p api.Project) bool { return p.Name == m.projectName })
		if i < 0 {
			return "", nil, reporting.UsageError(fmt.Errorf("RESCALE_PROJECT_ID at line %d: you have no project named %q "+
				"(#RESCALE_PROJECT_ID= takes a project's name, #RESCALE_PROJECT_ID <id> its ID)", m.projectLine, m.projectName))
		}
		projectID = projects[i].ID
	}
	if m.coreTypeLine == 0 {
		return projectID, nil, nil
	}
	coreTypes, err := lookup.GetCoreTypes(ctx, true)
	if err != nil {
		return "", nil, fmt.Errorf("RESCALE_CORE_TYPE at line %d: could not list the core types: %w", m.coreTypeLine, err)
	}
	if i := slices.IndexFunc(coreTypes, func(c models.CoreType) bool {
		return c.Code == m.CoreType || c.Name == m.CoreType
	}); i >= 0 {
		return projectID, &coreTypes[i], nil
	}
	return projectID, nil, nil
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

// requestSlots is the slot count a job from this script asks for: one when the
// script sets none. The summary reads it here too, so it states what the
// request sends.
func (m *SGEMetadata) requestSlots() int {
	return max(m.Slots, 1)
}

// plural is n with noun, which takes an s unless n is one.
func plural(n int, noun string) string {
	if n != 1 {
		noun += "s"
	}
	return fmt.Sprintf("%d %s", n, noun)
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
	sb.WriteString(fmt.Sprintf("\nHardware: %s (%s/slot, %s)\n",
		m.CoreType, plural(m.CoresPerSlot, "core"), plural(m.requestSlots(), "slot")))
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

	m := &SGEMetadata{
		Name:            job.JobName,
		Command:         job.Command,
		Analysis:        job.AnalysisCode,
		AnalysisVersion: job.AnalysisVersion,
		CoreType:        job.CoreType,
		CoresPerSlot:    job.CoresPerSlot,
		Slots:           job.Slots, // none reads as one, through requestSlots
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

	spec := models.JobSpec{
		JobName:         m.Name,
		Command:         m.Command,
		AnalysisCode:    m.Analysis,
		AnalysisVersion: m.AnalysisVersion,
		CoreType:        m.CoreType,
		CoresPerSlot:    m.CoresPerSlot,
		Slots:           m.requestSlots(),
		WalltimeHours:   walltimeHours,
		IsLowPriority:   m.IsLowPriority,
		Tags:            m.Tags,
		ProjectID:       m.ProjectID,
		Automations:     m.Automations,
		CIDRRule:        m.InboundSSHCIDR,
		PublicKey:       m.PublicKey,
		// Every run of the template attaches these, where each run sets
		// InputFiles from its own inputs.
		ExtraInputFileIDs: strings.Join(m.InputFiles, ","),
	}
	// The template takes a project by ID, and loading has no way to look one up
	// by name.
	if m.projectLine != 0 {
		return models.JobSpec{}, fmt.Errorf("RESCALE_PROJECT_ID at line %d names a project, which the job template "+
			"cannot look up; delete the line and choose the project in the template", m.projectLine)
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
