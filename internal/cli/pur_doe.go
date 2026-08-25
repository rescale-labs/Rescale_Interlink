package cli

import (
	"encoding/csv"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/rescale/rescale-int/internal/config"
	"github.com/rescale/rescale-int/internal/pur/doe"
	"github.com/rescale/rescale-int/internal/util/tags"
)

// newDOECmd creates the 'doe' command, which expands one base job into a
// parameter sweep and writes it as a jobs CSV for 'pur run' or
// 'pur submit-existing'.
func newDOECmd() *cobra.Command {
	var templatePath string
	var outputPath string
	var overwrite bool
	var method string
	var paramSpecs []string
	var paramFormats []string
	var samples int
	var seed uint64
	var centerPoints int
	var maxCases int
	var jobNameTemplate string
	var tagTemplates []string
	var baseFileIDs string
	var casesCSV string
	var preview bool
	var previewLimit int

	cmd := &cobra.Command{
		Use:   "doe",
		Short: "Generate a parameter sweep from a base job",
		Long: `Expand one base job into a design of experiments (parameter sweep).

The base job's command must contain a {{name}} token for each swept parameter.
Each generated case renders its own values into that command, so every case's
configuration is visible in the command line on its Rescale job page:

  template:  starccm+ -param alpha {{alpha}} -param beta {{beta}} -load input.sim
  case 1:    starccm+ -param alpha 10 -param beta 15 -load input.sim

Parameters and command tokens are checked against each other in both directions.
A swept parameter with no matching token, or a token with no matching parameter,
is an error rather than a silently wrong job.

Parameter syntax:
  --param "alpha=10:20:5"        numeric range, 5 levels from 10 to 20
  --param "alpha=10:20"          numeric range, the two endpoints
  --param "model=kepsilon,komega,les"
                                 categorical, one case per value
  --param-format "alpha=%.3f"    how to render a numeric value (default %g)

Methods:
  full-factorial      every combination of every level
  ofat                one factor at a time from a baseline
  latin-hypercube     --samples points, every parameter evenly covered
  sobol               --samples low-discrepancy points
  monte-carlo         --samples uniform random points
  central-composite   corners, axial points and center, for a quadratic fit
  box-behnken         quadratic design avoiding the corners (3+ parameters)
  explicit            cases read from --cases-csv

Shared input files:
  Pass --base-file-ids with the IDs of an already-uploaded input deck and every
  case references those files directly. The deck is then transferred once for
  the whole sweep instead of once per case, and the generated CSV is run with
  'pur submit-existing'. Without it, every case keeps the template's directory
  and 'pur run' tars and uploads that directory per case.

Examples:
  rescale-int pur doe --template base.csv --output sweep.csv \
    --param "alpha=10:20:3" --param "beta=15:25:3"

  rescale-int pur doe --template base.csv --preview \
    --method latin-hypercube --samples 20 --seed 7 \
    --param "alpha=10:20" --param "beta=15:25"

  rescale-int pur doe --template base.csv --output sweep.csv \
    --base-file-ids abcde,fghij --tag-template "alpha={{alpha}}" \
    --param "alpha=10:20:5"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := GetLogger()

			if templatePath == "" {
				return fmt.Errorf("--template is required")
			}
			if outputPath == "" && !preview {
				return fmt.Errorf("--output is required unless --preview is set")
			}
			if outputPath != "" && !overwrite {
				if _, err := os.Stat(outputPath); err == nil {
					return fmt.Errorf("output file %s already exists (use --overwrite to replace)", outputPath)
				}
			}

			templateJobs, err := config.LoadJobsCSV(templatePath)
			if err != nil {
				return fmt.Errorf("failed to load template: %w", err)
			}
			if len(templateJobs) == 0 {
				return fmt.Errorf("template CSV is empty")
			}

			opts := doe.Options{
				Template:        templateJobs[0],
				Method:          doe.Method(method),
				Samples:         samples,
				Seed:            seed,
				CenterPoints:    centerPoints,
				MaxCases:        maxCases,
				JobNameTemplate: jobNameTemplate,
				TagTemplates:    tagTemplates,
				BaseFileIDs:     tags.ParseCommaSeparated(baseFileIDs),
			}

			formats, err := parseParamFormats(paramFormats)
			if err != nil {
				return err
			}

			opts.Parameters, err = parseParamSpecs(paramSpecs, formats)
			if err != nil {
				return err
			}

			// Explicit cases come from a CSV whose header names the parameters, so
			// the parameter list can be inferred when it was not given explicitly.
			if casesCSV != "" {
				names, cases, err := loadCasesCSV(casesCSV)
				if err != nil {
					return err
				}
				opts.Cases = cases
				if len(opts.Parameters) == 0 {
					opts.Parameters = make([]doe.Parameter, len(names))
					for i, name := range names {
						opts.Parameters[i] = doe.Parameter{Name: name}
					}
				}
				if !cmd.Flags().Changed("method") {
					opts.Method = doe.MethodExplicit
				}
			}

			if len(formats) > 0 {
				if err := checkFormatsMatchParameters(formats, opts.Parameters); err != nil {
					return err
				}
			}

			logger.Info().
				Str("template", templatePath).
				Str("method", string(opts.Method)).
				Int("parameters", len(opts.Parameters)).
				Msg("Generating parameter sweep")

			result := doe.Generate(opts)

			for _, warning := range result.Warnings {
				logger.Warn().Msg(warning.Error())
			}

			if !result.OK() {
				fmt.Fprintf(os.Stderr, "\nSweep rejected (%d problem(s)):\n", len(result.Errors))
				for _, problem := range result.Errors {
					fmt.Fprintf(os.Stderr, "  - %s\n", problem.Error())
				}
				return fmt.Errorf("sweep validation failed")
			}

			printDOEPreview(result, opts, previewLimit)

			if preview {
				fmt.Println("\n(preview mode: no CSV was written)")
				return nil
			}

			jobs := result.Jobs

			// The jobs CSV has no column for pre-uploaded input file IDs, but it does
			// have one for extra file IDs, and those are attached to the job the same
			// way. Moving them across is what lets the sweep survive the round trip
			// and be run with 'pur submit-existing'.
			if len(opts.BaseFileIDs) > 0 {
				for i := range jobs {
					jobs[i].ExtraInputFileIDs = strings.Join(opts.BaseFileIDs, ",")
					jobs[i].InputFiles = nil
				}
			}

			if err := config.SaveJobsCSV(outputPath, jobs); err != nil {
				return fmt.Errorf("failed to save jobs CSV: %w", err)
			}

			logger.Info().
				Int("cases", len(jobs)).
				Str("output", outputPath).
				Msg("Sweep generated successfully")

			fmt.Printf("\nGenerated %d cases in %s\n", len(jobs), outputPath)
			if len(opts.BaseFileIDs) > 0 {
				fmt.Printf("\nEvery case references the same %d pre-uploaded file(s), so run it with:\n", len(opts.BaseFileIDs))
				fmt.Printf("  rescale-int pur submit-existing --jobs-csv %s\n", outputPath)
			} else {
				fmt.Printf("\nRun it with:\n  rescale-int pur run --jobs-csv %s --state sweep.state\n", outputPath)
			}
			return nil
		},
	}

	cmd.Flags().StringVarP(&templatePath, "template", "t", "", "Template jobs CSV whose first row is the base job (required)")
	cmd.Flags().StringVarP(&outputPath, "output", "o", "", "Output jobs CSV file (required unless --preview)")
	cmd.Flags().BoolVar(&overwrite, "overwrite", false, "Overwrite existing output file")
	cmd.Flags().StringVarP(&method, "method", "m", string(doe.MethodFullFactorial), "Sampling method: "+doeMethodList())
	cmd.Flags().StringArrayVar(&paramSpecs, "param", nil, "Swept parameter, e.g. \"alpha=10:20:5\" or \"model=a,b,c\" (can repeat)")
	cmd.Flags().StringArrayVar(&paramFormats, "param-format", nil, "Numeric rendering format, e.g. \"alpha=%.3f\" (can repeat)")
	cmd.Flags().IntVar(&samples, "samples", 0, "Number of design points for latin-hypercube, sobol and monte-carlo")
	cmd.Flags().Uint64Var(&seed, "seed", 0, "Seed for the randomized samplers; the same seed always yields the same sweep")
	cmd.Flags().IntVar(&centerPoints, "center-points", 0, "Center point repeats for central-composite and box-behnken (default 1)")
	cmd.Flags().IntVar(&maxCases, "max-cases", 0, fmt.Sprintf("Maximum cases to generate; 0 uses the default of %d, negative removes the limit", doe.DefaultMaxCases))
	cmd.Flags().StringVar(&jobNameTemplate, "job-name-template", "", "Case name template; may use parameter tokens plus {{__base}} and {{__index}} (default \"{{__base}}_{{__index}}\")")
	cmd.Flags().StringArrayVar(&tagTemplates, "tag-template", nil, "Per-case job tag, e.g. \"alpha={{alpha}}\" (can repeat)")
	cmd.Flags().StringVar(&baseFileIDs, "base-file-ids", "", "Comma-separated IDs of already-uploaded input files shared by every case")
	cmd.Flags().StringVar(&casesCSV, "cases-csv", "", "CSV of explicit cases, one column per parameter (implies --method explicit)")
	cmd.Flags().BoolVar(&preview, "preview", false, "Show the generated cases without writing a CSV")
	cmd.Flags().IntVar(&previewLimit, "preview-limit", 20, "How many cases to list; 0 lists all of them")

	cmd.MarkFlagRequired("template")

	return cmd
}

// doeMethodList renders the supported methods for flag help.
func doeMethodList() string {
	all := doe.AllMethods()
	names := make([]string, len(all))
	for i, m := range all {
		names[i] = string(m)
	}
	return strings.Join(names, ", ")
}

// parseParamSpecs parses every --param value, applying any matching
// --param-format.
func parseParamSpecs(specs []string, formats map[string]string) ([]doe.Parameter, error) {
	if len(specs) == 0 {
		return nil, nil
	}

	params := make([]doe.Parameter, 0, len(specs))
	for _, spec := range specs {
		param, err := parseParamSpec(spec)
		if err != nil {
			return nil, err
		}
		param.Format = formats[param.Name]
		params = append(params, param)
	}
	return params, nil
}

// parseParamSpec parses one "name=domain" parameter specification.
//
// A domain containing ':' is a numeric range "min:max" or "min:max:levels".
// Anything else is a categorical list of comma-separated values, which is also
// how a single fixed value is expressed.
func parseParamSpec(spec string) (doe.Parameter, error) {
	name, domain, found := strings.Cut(spec, "=")
	name = strings.TrimSpace(name)
	domain = strings.TrimSpace(domain)

	if !found {
		return doe.Parameter{}, fmt.Errorf("invalid --param %q: expected name=domain, e.g. \"alpha=10:20:5\"", spec)
	}
	if name == "" {
		return doe.Parameter{}, fmt.Errorf("invalid --param %q: parameter name is empty", spec)
	}
	if domain == "" {
		return doe.Parameter{}, fmt.Errorf("invalid --param %q: no values or range given for %q", spec, name)
	}

	if !strings.Contains(domain, ":") {
		values := tags.ParseCommaSeparated(domain)
		if len(values) == 0 {
			return doe.Parameter{}, fmt.Errorf("invalid --param %q: no values given for %q", spec, name)
		}
		return doe.Parameter{Name: name, Values: values}, nil
	}

	parts := strings.Split(domain, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return doe.Parameter{}, fmt.Errorf("invalid --param %q: expected min:max or min:max:levels", spec)
	}

	low, err := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	if err != nil {
		return doe.Parameter{}, fmt.Errorf("invalid --param %q: %q is not a number", spec, parts[0])
	}
	high, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if err != nil {
		return doe.Parameter{}, fmt.Errorf("invalid --param %q: %q is not a number", spec, parts[1])
	}

	param := doe.Parameter{Name: name, Min: low, Max: high}

	if len(parts) == 3 {
		levels, err := strconv.Atoi(strings.TrimSpace(parts[2]))
		if err != nil {
			return doe.Parameter{}, fmt.Errorf("invalid --param %q: level count %q is not an integer", spec, parts[2])
		}
		param.Levels = levels
	}

	return param, nil
}

// parseParamFormats parses the --param-format values into a name-to-format map.
func parseParamFormats(specs []string) (map[string]string, error) {
	if len(specs) == 0 {
		return nil, nil
	}

	formats := make(map[string]string, len(specs))
	for _, spec := range specs {
		name, format, found := strings.Cut(spec, "=")
		name = strings.TrimSpace(name)

		if !found || name == "" || format == "" {
			return nil, fmt.Errorf("invalid --param-format %q: expected name=format, e.g. \"alpha=%%.3f\"", spec)
		}
		if _, dup := formats[name]; dup {
			return nil, fmt.Errorf("invalid --param-format %q: format for %q given more than once", spec, name)
		}
		formats[name] = format
	}
	return formats, nil
}

// checkFormatsMatchParameters rejects a format for a parameter that is not being
// swept, which is otherwise a silently ignored typo.
func checkFormatsMatchParameters(formats map[string]string, params []doe.Parameter) error {
	declared := make(map[string]bool, len(params))
	for _, p := range params {
		declared[p.Name] = true
	}

	for name := range formats {
		if !declared[name] {
			return fmt.Errorf("--param-format names %q, which is not a swept parameter", name)
		}
	}
	return nil
}

// loadCasesCSV reads explicit cases from a CSV whose header names the
// parameters. Returns the parameter names in column order and one map per row.
func loadCasesCSV(path string) ([]string, []map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to open cases CSV: %w", err)
	}
	defer file.Close()

	records, err := csv.NewReader(file).ReadAll()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read cases CSV: %w", err)
	}
	if len(records) < 2 {
		return nil, nil, fmt.Errorf("cases CSV must have a header row and at least one case")
	}

	names := make([]string, 0, len(records[0]))
	seen := make(map[string]bool, len(records[0]))
	for _, column := range records[0] {
		name := strings.TrimSpace(column)
		if name == "" {
			return nil, nil, fmt.Errorf("cases CSV has an unnamed column")
		}
		if seen[name] {
			return nil, nil, fmt.Errorf("cases CSV names column %q more than once", name)
		}
		seen[name] = true
		names = append(names, name)
	}

	cases := make([]map[string]string, 0, len(records)-1)
	for i, record := range records[1:] {
		if len(record) == 1 && strings.TrimSpace(record[0]) == "" {
			continue // Skip blank lines.
		}
		if len(record) != len(names) {
			return nil, nil, fmt.Errorf("cases CSV row %d has %d values but the header has %d columns",
				i+2, len(record), len(names))
		}

		values := make(map[string]string, len(names))
		for j, name := range names {
			values[name] = strings.TrimSpace(record[j])
		}
		cases = append(cases, values)
	}

	if len(cases) == 0 {
		return nil, nil, fmt.Errorf("cases CSV has a header but no cases")
	}

	return names, cases, nil
}

// printDOEPreview lists the generated cases and one full rendered command, so
// the parameter injection can be eyeballed before anything is submitted.
func printDOEPreview(result doe.Result, opts doe.Options, limit int) {
	fmt.Printf("\n=== DOE: %d case(s), method %s ===\n\n", len(result.Cases), opts.Method)

	shown := len(result.Cases)
	if limit > 0 && limit < shown {
		shown = limit
	}

	header := fmt.Sprintf("%-5s %-30s", "#", "Job Name")
	for _, p := range opts.Parameters {
		header += fmt.Sprintf(" %-12s", truncateField(p.Name, 12))
	}
	fmt.Println(header)
	fmt.Println(strings.Repeat("-", len(header)))

	for _, c := range result.Cases[:shown] {
		row := fmt.Sprintf("%-5d %-30s", c.Index, truncateField(c.JobName, 30))
		for _, p := range opts.Parameters {
			row += fmt.Sprintf(" %-12s", truncateField(c.Values[p.Name], 12))
		}
		fmt.Println(row)
	}

	if shown < len(result.Cases) {
		fmt.Printf("... and %d more (use --preview-limit 0 to list all)\n", len(result.Cases)-shown)
	}

	first := result.Cases[0]
	fmt.Printf("\nTemplate command:\n  %s\n", opts.Template.Command)
	fmt.Printf("Case %d command:\n  %s\n", first.Index, first.Command)

	if len(first.Tags) > 0 {
		fmt.Printf("Case %d tags:\n  %s\n", first.Index, strings.Join(first.Tags, ", "))
	}

	if len(opts.BaseFileIDs) > 0 {
		fmt.Printf("\nShared input files (%d), transferred once for the whole sweep:\n  %s\n",
			len(opts.BaseFileIDs), strings.Join(opts.BaseFileIDs, ", "))
	} else if opts.Template.Directory != "" {
		fmt.Printf("\nEvery case tars and uploads %s; pass --base-file-ids to share one\n"+
			"already-uploaded input deck instead.\n", opts.Template.Directory)
	}

	for _, warning := range result.Warnings {
		fmt.Printf("\nWarning: %s\n", warning.Message)
	}
}

// truncateField shortens s to width for table output.
func truncateField(s string, width int) string {
	if len(s) <= width {
		return s
	}
	if width <= 3 {
		return s[:width]
	}
	return s[:width-3] + "..."
}
