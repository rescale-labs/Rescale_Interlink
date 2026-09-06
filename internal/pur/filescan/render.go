package filescan

import (
	"fmt"
	"path/filepath"
	"slices"
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
// The file tokens are what the job sees in its working directory rather than
// paths on the submitting machine: the archive is flattened, so the primary file
// arrives as a bare name no matter which directory it was scanned from. {{dir}}
// and {{index}} are not — {{dir}} names a folder on the submitting machine that
// the flattened archive does not reproduce, and {{index}} is the job's position
// in the batch. Both exist to tell jobs apart, not to be paths the job can use.
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

	if unknown := firstUnknownToken(command); unknown != "" {
		// The name comes before the word "token" on purpose: reporting's
		// redactor reads "token <word>" as a credential and would replace the
		// one detail this error exists to report (reporting/redactor.go:20).
		return nil, fmt.Errorf("command contains {{%s}}, which is not a file-scan token; valid tokens are %s",
			unknown, tokenList())
	}

	return nil, nil
}

// ValidateJobNameTemplate rejects unknown tokens in a job name template.
//
// A name is not cosmetic here: it is the identifier progress events and state
// records are matched by, so a template like "run-{{bse}}" leaves every job in
// the scan with the same literal name and their updates land on whichever row
// happens to match first. Checked once, up front, so a typo costs one message
// rather than one skip per scanned file.
func ValidateJobNameTemplate(jobName string) error {
	if unknown := firstUnknownToken(jobName); unknown != "" {
		// The name comes before the word "token" on purpose; see
		// ValidateCommandTemplate.
		return fmt.Errorf("job name contains {{%s}}, which is not a file-scan token; valid tokens are %s",
			unknown, tokenList())
	}
	return nil
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
	if len(command) > pattern.MaxCommandLength {
		return "", "", fmt.Errorf("rendered command is %d bytes, which exceeds the limit of %d",
			len(command), pattern.MaxCommandLength)
	}

	jobName = renderJobName(jobNameTemplate, values, index)

	// The same two post-conditions for the name, and a residual token there is
	// no more cosmetic than one in the command — see ValidateJobNameTemplate.
	if residual := pattern.ExtractTokens(jobName); len(residual) > 0 {
		return "", "", fmt.Errorf("rendered job name still contains {{%s}}; valid tokens are %s",
			residual[0], tokenList())
	}
	if len(jobName) > pattern.MaxJobNameLength {
		return "", "", fmt.Errorf("rendered job name is %d bytes, which exceeds the limit of %d",
			len(jobName), pattern.MaxJobNameLength)
	}

	return command, jobName, nil
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

// firstUnknownToken returns the first token in s that file-scan mode does not
// substitute, or "" when every token in s is known.
func firstUnknownToken(s string) string {
	for _, token := range pattern.ExtractTokens(s) {
		if !isKnownToken(token) {
			return token
		}
	}
	return ""
}

func isKnownToken(name string) bool {
	return slices.Contains(KnownTokens(), name)
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
