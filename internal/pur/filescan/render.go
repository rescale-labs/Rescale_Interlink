package filescan

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/rescale/rescale-int/internal/pur/pattern"
)

// Per-file command rendering. A scan finds the files; this turns each one into
// the command that job will actually run, so a folder of cases no longer yields
// N jobs all running an identical command line.
//
// The tokens are built in and need no declaration step, unlike a DOE sweep where
// the user declares the parameters being swept. They are also deliberately
// unprefixed, where DOE's {{__index}} and {{__base}} are double-underscored only
// to keep them clear of user-chosen parameter names. Note {{base}} here is the
// primary file's stem, whereas DOE's {{__base}} is the base job name.
const (
	TokenFile  = "file"  // "case1.inp"
	TokenBase  = "base"  // "case1"
	TokenExt   = "ext"   // "inp", with no leading dot
	TokenDir   = "dir"   // "inputs", the primary file's containing folder
	TokenIndex = "index" // "1", 1-based
)

// KnownTokens returns every token file-scan mode substitutes, in the order they
// are listed to the user.
func KnownTokens() []string {
	return []string{TokenFile, TokenBase, TokenExt, TokenDir, TokenIndex}
}

// Substitutions returns the token values for one job.
//
// Every value is what the job sees in its working directory rather than a path
// on the submitting machine: the archive is flattened, so the primary file
// arrives as a bare name no matter which directory it was scanned from.
func Substitutions(jf JobFiles, index int) map[string]string {
	name := filepath.Base(jf.PrimaryFile)

	base := jf.PrimaryBase
	if base == "" {
		base = strings.TrimSuffix(name, filepath.Ext(name))
	}

	return map[string]string{
		TokenFile:  name,
		TokenBase:  base,
		TokenExt:   strings.TrimPrefix(filepath.Ext(name), "."),
		TokenDir:   dirToken(jf.PrimaryDir),
		TokenIndex: strconv.Itoa(index),
	}
}

// dirToken resolves {{dir}} to the containing folder's name, which is what
// distinguishes jobs in a "case1/model.inp", "case2/model.inp" layout where the
// filenames are all identical.
//
// A relative scan can leave PrimaryDir as "." or "", whose basename is not a
// name at all, so those resolve through the absolute path instead.
func dirToken(primaryDir string) string {
	base := filepath.Base(primaryDir)
	if base != "." && base != string(filepath.Separator) && base != "" {
		return base
	}

	abs, err := filepath.Abs(primaryDir)
	if err != nil {
		return ""
	}
	base = filepath.Base(abs)
	if base == "." || base == string(filepath.Separator) {
		return ""
	}
	return base
}

// ValidateCommandTemplate checks a command template before any job is built.
//
// An unknown token is fatal. Substitution leaves what it cannot resolve in
// place, so a typo like {{bse}} would otherwise submit every job with a literal
// "{{bse}}" on its command line — the same reason DOE refuses to render a
// command with residual tokens. A command with no tokens at all is only a
// warning: every job then runs the same command, which is occasionally what the
// user wants.
func ValidateCommandTemplate(command string) (warnings []string, err error) {
	if strings.TrimSpace(command) == "" {
		return nil, fmt.Errorf("command is empty")
	}

	tokens := pattern.ExtractTokens(command)
	if len(tokens) == 0 {
		return []string{fmt.Sprintf("command contains no tokens, so every job will run the same "+
			"command; add one of %s to vary it per file", tokenList())}, nil
	}

	for _, token := range tokens {
		if !isKnownToken(token) {
			return nil, fmt.Errorf("command contains unknown token {{%s}}; valid tokens are %s",
				token, tokenList())
		}
	}

	return nil, nil
}

// ValidateJobNameTemplate reports unknown tokens in a job name template.
//
// These are warnings rather than errors: an unresolved token in a name is
// cosmetic, where one in a command changes what the job runs.
func ValidateJobNameTemplate(jobName string) []string {
	var warnings []string
	for _, token := range pattern.ExtractTokens(jobName) {
		if !isKnownToken(token) {
			warnings = append(warnings, fmt.Sprintf("job name references unknown token {{%s}}; "+
				"valid tokens are %s", token, tokenList()))
		}
	}
	return warnings
}

// Render produces the command and job name for one scanned file set.
//
// index is 1-based and supplies {{index}}. A job name template with no tokens
// keeps the existing "Name_1", "Name_2" numbering, so setups written before
// tokens existed behave exactly as they did.
//
// An error here concerns this one file — a filename that cannot be substituted
// safely — and callers should record it as a skipped file rather than failing
// the whole scan: one badly named file should not cost 200 good ones. Errors
// that condemn the template itself belong to ValidateCommandTemplate, which
// callers run once beforehand.
func Render(commandTemplate, jobNameTemplate string, jf JobFiles, index int) (command, jobName string, err error) {
	values := Substitutions(jf, index)

	// A filename lands in the command line exactly as a swept parameter value
	// does, so it answers to the same rule: a token supplies a datum, never
	// syntax. Checked before substituting, so the offending token can be named,
	// and only for tokens the command actually uses — a folder with a space in
	// its name is nobody's problem unless {{dir}} is being substituted.
	for _, token := range pattern.ExtractTokens(commandTemplate) {
		value, known := values[token]
		if !known {
			continue // ValidateCommandTemplate's business, reported below.
		}
		if bad, found := pattern.FirstUnsafeChar(value); found {
			return "", "", fmt.Errorf("{{%s}} is %q, which contains %q and would change the structure "+
				"of the rendered command rather than supply a value", token, value, string(bad))
		}
		if pattern.HasWhitespace(value) {
			return "", "", fmt.Errorf("{{%s}} is %q, which contains whitespace and would split into "+
				"separate command arguments", token, value)
		}
	}

	command = pattern.SubstituteTokens(commandTemplate, values)

	// Post-condition. ValidateCommandTemplate should already have caught this,
	// but a command that reaches Rescale with a literal "{{...}}" in it runs the
	// wrong thing, so it is worth not depending on a caller having asked.
	if residual := pattern.ExtractTokens(command); len(residual) > 0 {
		return "", "", fmt.Errorf("rendered command still contains {{%s}}; valid tokens are %s",
			residual[0], tokenList())
	}

	return command, renderJobName(jobNameTemplate, values, index), nil
}

// renderJobName substitutes into the job name, falling back to index numbering
// when the template has nothing to substitute.
func renderJobName(jobNameTemplate string, values map[string]string, index int) string {
	if pattern.HasTokens(jobNameTemplate) {
		return pattern.SubstituteTokens(jobNameTemplate, values)
	}

	// The pre-token behavior of both callers, preserved: a named template is
	// numbered, an empty one becomes "Job_N".
	if strings.TrimSpace(jobNameTemplate) == "" {
		return fmt.Sprintf("Job_%d", index)
	}
	return fmt.Sprintf("%s_%d", jobNameTemplate, index)
}

func isKnownToken(name string) bool {
	for _, known := range KnownTokens() {
		if name == known {
			return true
		}
	}
	return false
}

// tokenList renders the valid tokens for an error message: "{{file}}, {{base}}, ...".
func tokenList() string {
	known := KnownTokens()
	parts := make([]string, len(known))
	for i, token := range known {
		parts[i] = "{{" + token + "}}"
	}
	return strings.Join(parts, ", ")
}
