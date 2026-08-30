package doe

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/rescale/rescale-int/internal/models"
	"github.com/rescale/rescale-int/internal/pur/pattern"
	"github.com/rescale/rescale-int/internal/util/tags"
)

// Built-in tokens available to the job name and tag templates alongside the
// parameter tokens. Double-underscored so they cannot collide with a parameter
// name a user would plausibly choose.
const (
	tokenIndex = "__index"
	tokenBase  = "__base"
)

// indexSuffix matches a trailing _<digits> on a job name, which is how PUR names
// iterated jobs. It is stripped to derive {{__base}} so sweeping a job called
// "sim_1" produces "sim_1", "sim_2", ... rather than "sim_1_1".
var indexSuffix = regexp.MustCompile(`_\d+$`)

// renderCases turns design points into cases and job specs.
//
// Rendering is where the parameter values reach the command line, so it also
// enforces the post-condition that nothing is left unresolved: a rendered
// command that still contains a {{token}} is a fatal problem rather than a job
// submitted with a literal placeholder in its command.
func renderCases(opts Options, points []designPoint) (cases []Case, jobs []models.JobSpec, errs []Problem, warns []Problem) {
	base := indexSuffix.ReplaceAllString(opts.Template.JobName, "")

	// A preview needs the first page, not the whole sweep. Sampling has already
	// run over every point, so what is rendered here is an exact prefix of what a
	// full generation would produce.
	rendered := len(points)
	if opts.RenderLimit > 0 && opts.RenderLimit < rendered {
		rendered = opts.RenderLimit
	}
	points = points[:rendered]

	cases = make([]Case, 0, len(points))
	jobs = make([]models.JobSpec, 0, len(points))

	seenNames := make(map[string]int, len(points))
	seenValues := make(map[string]int, len(points))
	duplicateValueCases := 0

	for i, point := range points {
		index := i + 1

		values, valueErrs := pointValues(opts.Parameters, point)
		if len(valueErrs) > 0 {
			for _, problem := range valueErrs {
				problem.Message = fmt.Sprintf("case %d: %s", index, problem.Message)
				errs = append(errs, problem)
			}
			continue
		}

		// Substitution reads parameters and built-ins from one map. Built-ins are
		// added second so a parameter can never shadow them.
		substitutions := make(map[string]string, len(values)+2)
		for name, value := range values {
			substitutions[name] = value
		}
		substitutions[tokenIndex] = strconv.Itoa(index)
		substitutions[tokenBase] = base

		command := pattern.SubstituteTokens(opts.Template.Command, substitutions)
		if problem := checkResidualTokens(index, "command", command); problem != nil {
			errs = append(errs, *problem)
			continue
		}
		if problem := checkLength(index, "command", command, maxCommandLength); problem != nil {
			errs = append(errs, *problem)
			continue
		}

		jobName := strings.TrimSpace(pattern.SubstituteTokens(opts.JobNameTemplate, substitutions))
		if jobName == "" {
			errs = append(errs, Problem{
				Code:    CodeEmptyJobName,
				Message: fmt.Sprintf("case %d: job name template rendered to an empty name", index),
			})
			continue
		}
		if problem := checkResidualTokens(index, "job name", jobName); problem != nil {
			errs = append(errs, *problem)
			continue
		}
		if problem := checkLength(index, "job name", jobName, maxJobNameLength); problem != nil {
			errs = append(errs, *problem)
			continue
		}
		if first, dup := seenNames[jobName]; dup {
			errs = append(errs, Problem{
				Code: CodeDupJobName,
				Message: fmt.Sprintf("cases %d and %d both render to job name %q; "+
					"include {{__index}} in the job name template to keep names unique",
					first, index, jobName),
			})
			continue
		}
		seenNames[jobName] = index

		caseTags, tagWarns := renderTags(opts, substitutions, index)
		warns = append(warns, tagWarns...)

		if tagProblems := checkTags(index, caseTags); len(tagProblems) > 0 {
			errs = append(errs, tagProblems...)
			continue
		}

		// Identical parameter values mean identical work. Possible when a
		// categorical parameter has fewer values than the design has levels, so it
		// is worth surfacing but not worth blocking.
		if key := valuesKey(opts.Parameters, values); key != "" {
			if _, dup := seenValues[key]; dup {
				duplicateValueCases++
			} else {
				seenValues[key] = index
			}
		}

		cases = append(cases, Case{
			Index:   index,
			Values:  values,
			JobName: jobName,
			Command: command,
			Tags:    caseTags,
		})
		jobs = append(jobs, buildJobSpec(opts, jobName, command, caseTags))
	}

	if duplicateValueCases > 0 {
		warns = append(warns, Problem{
			Code: CodeDuplicateCase,
			Message: fmt.Sprintf("%d of %d cases repeat parameter values already covered by an "+
				"earlier case, so they would run identical work", duplicateValueCases, len(points)),
		})
	}

	return cases, jobs, errs, warns
}

// checkResidualTokens is the post-condition on substitution: whatever the
// template asked for was either supplied or is fatal, because SubstituteTokens
// leaves an unknown token alone and the literal "{{alpha}}" then reaches Rescale
// in the field it was written into.
func checkResidualTokens(index int, field, rendered string) *Problem {
	residual := pattern.ExtractTokens(rendered)
	if len(residual) == 0 {
		return nil
	}

	return &Problem{
		Code:  CodeResidualToken,
		Param: residual[0],
		Message: fmt.Sprintf("case %d: rendered %s still contains {{%s}}; the job would carry the "+
			"placeholder text", index, field, strings.Join(residual, "}}, {{")),
	}
}

// checkLength bounds one rendered field. A format or a template can multiply its
// input by a large factor, so the bound goes where the rendered result is, not
// on what went in.
func checkLength(index int, field, rendered string, limit int) *Problem {
	if len(rendered) <= limit {
		return nil
	}

	return &Problem{
		Code: CodeTooLong,
		Message: fmt.Sprintf("case %d: rendered %s is %d bytes, which exceeds the limit of %d",
			index, field, len(rendered), limit),
	}
}

// checkTags applies the rendered-field checks to one case's whole tag list.
func checkTags(index int, caseTags []string) []Problem {
	var problems []Problem
	for _, tag := range caseTags {
		if problem := checkResidualTokens(index, "tag", tag); problem != nil {
			problems = append(problems, *problem)
			continue
		}
		if problem := checkLength(index, "tag", tag, maxTagLength); problem != nil {
			problems = append(problems, *problem)
		}
	}
	return problems
}

// pointValues resolves one design point into rendered parameter values.
func pointValues(params []Parameter, point designPoint) (map[string]string, []Problem) {
	// Explicit cases already carry their values; they were checked against the
	// parameter set by validateExplicitCases.
	if point.Values != nil {
		return point.Values, nil
	}

	values := make(map[string]string, len(params))
	var errs []Problem

	for i, p := range params {
		if i >= len(point.Coords) {
			errs = append(errs, Problem{
				Code:    CodeMissingToken,
				Param:   p.Name,
				Message: "design point has no coordinate for this parameter",
			})
			continue
		}

		value := formatValue(p, point.Coords[i])

		// fmt reports a verb it cannot apply in the output itself, so a Format of
		// "%d" against a float would otherwise reach the command line as
		// "%!d(float64=10)".
		if strings.Contains(value, "%!") {
			errs = append(errs, Problem{
				Code:  CodeBadFormat,
				Param: p.Name,
				Message: fmt.Sprintf("Format %q cannot render a numeric value; it produced %q",
					p.Format, value),
			})
			continue
		}

		// The formatted result is checked, not just the inputs: a Format such as
		// "'%g'" or "%8.2f" can introduce characters the raw value never had.
		// A categorical level is the user's own string and gets the stricter
		// policy; numeric output is fmt's, where a leading "-" is expected.
		policy := policyNumeric
		if p.Values != nil {
			policy = policyLiteral
		}
		if problems := validateValue(p.Name, value, policy); len(problems) > 0 {
			errs = append(errs, problems...)
			continue
		}

		values[p.Name] = value
	}

	return values, errs
}

// formatValue maps a unit coordinate onto a parameter's domain.
func formatValue(p Parameter, coord float64) string {
	if p.Values != nil {
		if len(p.Values) == 0 {
			return ""
		}
		return p.Values[valueIndex(coord, len(p.Values))]
	}

	format := p.Format
	if format == "" {
		format = "%g"
	}
	return fmt.Sprintf(format, p.Min+coord*(p.Max-p.Min))
}

// renderTags builds one case's tag list: the template's tags plus the rendered
// tag templates, normalized and deduped.
func renderTags(opts Options, substitutions map[string]string, index int) ([]string, []Problem) {
	var warns []Problem

	rendered := make([]string, 0, len(opts.Template.Tags)+len(opts.TagTemplates))
	rendered = append(rendered, opts.Template.Tags...)

	for _, template := range opts.TagTemplates {
		tag := strings.TrimSpace(pattern.SubstituteTokens(template, substitutions))
		if tag == "" {
			warns = append(warns, Problem{
				Code:    CodeBadTag,
				Message: fmt.Sprintf("case %d: tag template %q rendered to an empty tag and was dropped", index, template),
			})
			continue
		}
		rendered = append(rendered, tag)
	}

	// Reuse the shared normalizer so DOE tags behave exactly like tags supplied
	// anywhere else in the tool.
	return tags.NormalizeTags(rendered), warns
}

// buildJobSpec derives one case's JobSpec from the template.
//
// A sweep varies the command against one shared input deck, so a case never tars
// a per-case directory the way folder-scanned PUR jobs do. Every case therefore
// carries no Directory, which puts it on the pipeline's skip path: no tar, no
// upload, straight to job creation. The shared deck comes from Shared Input File
// IDs (referenced directly here) or from batch-level Common Files (uploaded once
// and attached to every job by the pipeline) — never from silently zipping the
// template's working directory.
func buildJobSpec(opts Options, jobName, command string, caseTags []string) models.JobSpec {
	spec := opts.Template

	spec.JobName = jobName
	spec.Command = command
	spec.Tags = caseTags

	spec.Directory = ""
	spec.TarSubpath = ""
	if len(opts.BaseFileIDs) > 0 {
		spec.InputFiles = append([]string(nil), opts.BaseFileIDs...)
	} else {
		spec.InputFiles = nil
	}

	return spec
}

// valuesKey is a stable identity for a case's parameter values, used to spot
// cases that duplicate work. Returns "" when there is nothing to compare.
func valuesKey(params []Parameter, values map[string]string) string {
	if len(values) == 0 {
		return ""
	}

	var b strings.Builder
	for _, p := range params {
		b.WriteString(p.Name)
		b.WriteByte('=')
		b.WriteString(values[p.Name])
		b.WriteByte('\x00')
	}
	return b.String()
}
