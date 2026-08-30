package wailsapp

import (
	"strings"

	"github.com/rescale/rescale-int/internal/models"
	"github.com/rescale/rescale-int/internal/pur/doe"
)

// DOEParameterDTO is the JSON-safe version of doe.Parameter.
//
// A parameter is either continuous (min/max, with levels for the grid designs)
// or categorical (values). A non-empty values list makes it categorical and the
// numeric range is then ignored.
type DOEParameterDTO struct {
	Name   string   `json:"name"`
	Min    float64  `json:"min"`
	Max    float64  `json:"max"`
	Levels int      `json:"levels"`
	Values []string `json:"values,omitempty"`
	Format string   `json:"format,omitempty"`
}

// DOEOptionsDTO is the JSON-safe version of doe.Options.
type DOEOptionsDTO struct {
	Template        JobSpecDTO          `json:"template"`
	Parameters      []DOEParameterDTO   `json:"parameters"`
	Method          string              `json:"method"`
	Samples         int                 `json:"samples"`
	Seed            uint64              `json:"seed"`
	CenterPoints    int                 `json:"centerPoints"`
	Cases           []map[string]string `json:"cases,omitempty"`
	BaseFileIDs     []string            `json:"baseFileIds,omitempty"`
	JobNameTemplate string              `json:"jobNameTemplate"`
	TagTemplates    []string            `json:"tagTemplates,omitempty"`
	MaxCases        int                 `json:"maxCases"`
}

// DOECaseDTO is one generated case.
type DOECaseDTO struct {
	Index   int               `json:"index"`
	Values  map[string]string `json:"values"`
	JobName string            `json:"jobName"`
	Command string            `json:"command"`
	Tags    []string          `json:"tags,omitempty"`
}

// DOEProblemDTO is one validation finding. Code is a stable string the UI can
// branch on; Message is the text to show.
type DOEProblemDTO struct {
	Code    string `json:"code"`
	Param   string `json:"param,omitempty"`
	Message string `json:"message"`
}

// DOEResultDTO is the outcome of a sweep.
//
// Cases may be truncated for preview, so CaseCount is the size of the whole
// design rather than len(Cases).
type DOEResultDTO struct {
	OK        bool            `json:"ok"`
	CaseCount int             `json:"caseCount"`
	Truncated bool            `json:"truncated"`
	Cases     []DOECaseDTO    `json:"cases"`
	Jobs      []JobSpecDTO    `json:"jobs,omitempty"`
	Errors    []DOEProblemDTO `json:"errors,omitempty"`
	Warnings  []DOEProblemDTO `json:"warnings,omitempty"`
}

// DOECasesCSVDTO is a parsed explicit-cases CSV: the parameter names in column
// order and one map of name to value per case.
type DOECasesCSVDTO struct {
	Names []string            `json:"names"`
	Cases []map[string]string `json:"cases"`
}

// DOEMethodDTO describes a sampling method for the method menu, including which
// fields the method reads so the form can hide the ones it ignores.
type DOEMethodDTO struct {
	Method           string `json:"method"`
	Label            string `json:"label"`
	Description      string `json:"description"`
	UsesSamples      bool   `json:"usesSamples"`
	UsesLevels       bool   `json:"usesLevels"`
	UsesCenterPoints bool   `json:"usesCenterPoints"`
	UsesCases        bool   `json:"usesCases"`
	MinParameters    int    `json:"minParameters"`
	MaxParameters    int    `json:"maxParameters"`
}

// DefaultDOEMaxCases exposes the sweep size cap so the UI can show it.
func (a *App) DefaultDOEMaxCases() int {
	return doe.DefaultMaxCases
}

// GetDOEMethods returns the supported sampling methods. Sourced from the doe
// package so the menu cannot drift from what generation accepts.
func (a *App) GetDOEMethods() []DOEMethodDTO {
	infos := doe.Methods()

	dtos := make([]DOEMethodDTO, len(infos))
	for i, info := range infos {
		dtos[i] = DOEMethodDTO{
			Method:           string(info.Method),
			Label:            info.Label,
			Description:      info.Description,
			UsesSamples:      info.UsesSamples,
			UsesLevels:       info.UsesLevels,
			UsesCenterPoints: info.UsesCenterPoints,
			UsesCases:        info.UsesCases,
			MinParameters:    info.MinParameters,
			MaxParameters:    info.MaxParameters,
		}
	}
	return dtos
}

// GenerateDOE expands the template into a full sweep, returning both the cases
// and the job specs to hand to StartBulkRun.
//
// Generation is pure: no files are read and no API calls are made, so the
// frontend can call it as freely as it likes while the user edits the design.
func (a *App) GenerateDOE(opts DOEOptionsDTO) DOEResultDTO {
	return doeResultToDTO(doe.Generate(dtoToDOEOptions(opts)), 0, true)
}

// PreviewDOECases generates the sweep and returns at most limit cases without
// the job specs, which is what the live preview needs. A limit of zero or less
// returns every case.
//
// The limit is passed to Generate as well as applied here, so a preview stops
// rendering once it has its page instead of building every case's job spec only
// for the conversion below to discard them.
func (a *App) PreviewDOECases(opts DOEOptionsDTO, limit int) DOEResultDTO {
	doeOpts := dtoToDOEOptions(opts)
	doeOpts.RenderLimit = limit
	return doeResultToDTO(doe.Generate(doeOpts), limit, false)
}

// ParseDOECasesCSV parses pasted explicit cases with the same parser the CLI
// uses for --cases-csv, so identical text yields an identical sweep on either
// surface. Returns the parameter names in column order and one map per case.
func (a *App) ParseDOECasesCSV(text string) (DOECasesCSVDTO, error) {
	names, cases, err := doe.ParseCasesCSV(strings.NewReader(text))
	if err != nil {
		return DOECasesCSVDTO{}, err
	}
	return DOECasesCSVDTO{Names: names, Cases: cases}, nil
}

// dtoToDOEOptions converts the DTO to doe.Options.
func dtoToDOEOptions(opts DOEOptionsDTO) doe.Options {
	params := make([]doe.Parameter, len(opts.Parameters))
	for i, p := range opts.Parameters {
		params[i] = doe.Parameter{
			Name:   p.Name,
			Min:    p.Min,
			Max:    p.Max,
			Levels: p.Levels,
			Values: p.Values,
			Format: p.Format,
		}
	}

	return doe.Options{
		Template:        dtoToJobSpec(opts.Template),
		Parameters:      params,
		Method:          doe.Method(opts.Method),
		Samples:         opts.Samples,
		Seed:            opts.Seed,
		CenterPoints:    opts.CenterPoints,
		Cases:           opts.Cases,
		BaseFileIDs:     opts.BaseFileIDs,
		JobNameTemplate: opts.JobNameTemplate,
		TagTemplates:    opts.TagTemplates,
		MaxCases:        opts.MaxCases,
	}
}

// doeResultToDTO converts a doe.Result, optionally truncating the case list and
// omitting the job specs.
func doeResultToDTO(result doe.Result, limit int, includeJobs bool) DOEResultDTO {
	dto := DOEResultDTO{
		OK:        result.OK(),
		CaseCount: result.CaseCount,
		Errors:    doeProblemsToDTO(result.Errors),
		Warnings:  doeProblemsToDTO(result.Warnings),
		Cases:     []DOECaseDTO{},
	}

	// Cases may already be short of the design because Generate was given the
	// same limit, so truncation is measured against the design size rather than
	// against what came back.
	shown := len(result.Cases)
	if limit > 0 && limit < shown {
		shown = limit
	}
	dto.Truncated = shown < result.CaseCount

	for _, c := range result.Cases[:shown] {
		dto.Cases = append(dto.Cases, DOECaseDTO{
			Index:   c.Index,
			Values:  c.Values,
			JobName: c.JobName,
			Command: c.Command,
			Tags:    c.Tags,
		})
	}

	if includeJobs {
		dto.Jobs = jobSpecsToDTOs(result.Jobs)
	}

	return dto
}

// doeProblemsToDTO converts validation findings, returning nil when there are
// none so the field is omitted rather than sent as an empty array.
func doeProblemsToDTO(problems []doe.Problem) []DOEProblemDTO {
	if len(problems) == 0 {
		return nil
	}

	dtos := make([]DOEProblemDTO, len(problems))
	for i, p := range problems {
		dtos[i] = DOEProblemDTO{
			Code:    p.Code,
			Param:   p.Param,
			Message: p.Message,
		}
	}
	return dtos
}

// jobSpecsToDTOs converts a slice of job specs.
func jobSpecsToDTOs(specs []models.JobSpec) []JobSpecDTO {
	dtos := make([]JobSpecDTO, len(specs))
	for i, spec := range specs {
		dtos[i] = jobSpecToDTO(spec)
	}
	return dtos
}
