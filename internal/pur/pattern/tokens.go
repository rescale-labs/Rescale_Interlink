package pattern

import (
	"regexp"
	"strings"
	"unicode"
)

// Named-token substitution, kept separate from the numeric pattern detection in
// pattern.go. Numeric iteration guesses which numbers in a command are indices;
// named tokens are explicit, so none of those heuristics apply here.

// tokenPattern matches a {{name}} placeholder, tolerating whitespace inside the
// braces. Names start with a letter or underscore and continue with letters,
// digits, underscores, hyphens or dots, which covers parameter names like
// "alpha", "inlet_velocity", "mesh-size" and "bc.inlet".
var tokenPattern = regexp.MustCompile(`\{\{\s*([A-Za-z_][A-Za-z0-9_.\-]*)\s*\}\}`)

// ExtractTokens returns the distinct token names appearing in s, in order of
// first appearance. Returns nil when s contains no tokens.
//
// Callers use this two ways: to discover what a command template expects, and
// as a post-substitution check that nothing was left unresolved.
func ExtractTokens(s string) []string {
	matches := tokenPattern.FindAllStringSubmatch(s, -1)
	if len(matches) == 0 {
		return nil
	}

	seen := make(map[string]bool, len(matches))
	var names []string
	for _, m := range matches {
		name := m[1]
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	return names
}

// HasTokens reports whether s contains at least one {{name}} placeholder.
func HasTokens(s string) bool {
	return tokenPattern.MatchString(s)
}

// SubstituteTokens replaces every {{name}} in s with values[name].
//
// Substitution is single-pass: replacement text is never rescanned, so a value
// that itself looks like "{{other}}" is inserted literally rather than
// triggering another round of substitution. This keeps rendering
// non-recursive and independent of map iteration order.
//
// A token with no entry in values is left in place verbatim. Callers detect
// that with ExtractTokens on the result rather than getting a silently
// mangled command.
func SubstituteTokens(s string, values map[string]string) string {
	if s == "" || len(values) == 0 {
		return s
	}

	return tokenPattern.ReplaceAllStringFunc(s, func(match string) string {
		name := tokenName(match)
		value, ok := values[name]
		if !ok {
			return match
		}
		return value
	})
}

// tokenName pulls the name out of a full "{{ name }}" match.
func tokenName(match string) string {
	inner := strings.TrimSuffix(strings.TrimPrefix(match, "{{"), "}}")
	return strings.TrimSpace(inner)
}

// The value rules and the length bounds live here rather than in a caller
// because every substitution site has the same exposure: a DOE parameter value
// and a scanned filename land in one command line the same way, and both reach
// the same job-creation request.

// UnsafeValueChars are characters that would change the structure of a rendered
// command rather than just supply a value: redirection, command separators,
// substitution and quoting. A substituted value containing one of these is
// rejected by callers, since a token value is meant to be a datum, not syntax.
const UnsafeValueChars = "`$;|&><\n\r\"'\\"

// GlobValueChars are characters a shell expands against the filesystem or the
// argument list. A value written by hand — a categorical level, an explicit
// case value, a filename — that expands is no longer the value the user wrote.
const GlobValueChars = "*?[](){}~"

// Bounds on rendered output: a length one surface refuses is not one another
// should submit.
const (
	MaxCommandLength = 32 << 10
	MaxJobNameLength = 128
)

// FirstUnsafeChar returns the first character of s a shell would read as syntax
// rather than as data, and whether one was found. That is both sets: command
// structure and filename expansion.
//
// This is the check for a value written by hand. FirstUnsafeStructureChar is the
// narrower one, for text whose shape is produced rather than typed.
func FirstUnsafeChar(s string) (byte, bool) {
	return firstCharFrom(s, UnsafeValueChars+GlobValueChars)
}

// FirstUnsafeStructureChar is FirstUnsafeChar without the expansion characters,
// for a value produced by a numeric format: fmt cannot emit a glob character
// from a number, so rejecting one there would only ever reject the literal text
// the format itself carries.
func FirstUnsafeStructureChar(s string) (byte, bool) {
	return firstCharFrom(s, UnsafeValueChars)
}

func firstCharFrom(s, chars string) (byte, bool) {
	if idx := strings.IndexAny(s, chars); idx >= 0 {
		return s[idx], true
	}
	return 0, false
}

// FirstSpaceOrControl returns the first rune of s that is whitespace or a
// control character — whichever appears first — and whether one was found.
//
// Quoting is not available to a substituted value (quote characters are in
// UnsafeValueChars), so any space splits one argument into two, and that is
// every kind of space, not just the plain one: a non-breaking space and a
// vertical tab reach the API verbatim and mean something else there. A control
// character does not survive the trip to a command line at all. Callers that
// report the two differently tell them apart with unicode.IsSpace, the same
// test applied here; scanning once keeps the rune reported the earliest one.
func FirstSpaceOrControl(s string) (rune, bool) {
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return r, true
		}
	}
	return 0, false
}

// HasWhitespace reports whether s carries a character that cannot cross into a
// command line as part of a single argument — any Unicode whitespace, or a
// control character. See FirstSpaceOrControl for why both count.
func HasWhitespace(s string) bool {
	_, found := FirstSpaceOrControl(s)
	return found
}
