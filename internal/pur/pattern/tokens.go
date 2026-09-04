package pattern

import (
	"regexp"
	"strings"
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

// UnsafeValueChars are characters that would change the structure of a rendered
// command rather than just supply a value: redirection, command separators,
// substitution and quoting. A substituted value containing one of these is
// rejected by callers, since a token value is meant to be a datum, not syntax.
//
// This lives here rather than in a caller because every substitution site has
// the same exposure: a DOE parameter value and a scanned filename both land in
// a command line the same way.
const UnsafeValueChars = "`$;|&><\n\r\"'\\"

// FirstUnsafeChar returns the first character of s drawn from UnsafeValueChars,
// and whether one was found.
func FirstUnsafeChar(s string) (byte, bool) {
	if idx := strings.IndexAny(s, UnsafeValueChars); idx >= 0 {
		return s[idx], true
	}
	return 0, false
}

// HasWhitespace reports whether s contains a space or tab. Quoting is not
// available to a substituted value (quote characters are in UnsafeValueChars),
// so whitespace would silently split one command argument into two.
func HasWhitespace(s string) bool {
	return strings.ContainsAny(s, " \t")
}
