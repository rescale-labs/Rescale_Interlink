// Package sanitize provides command and field sanitization for CSV input.
//
// This package removes problematic characters from commands and fields:
//   - Windows/Mac line endings (CRLF/CR → LF)
//   - Invisible Unicode characters (zero-width spaces, etc.)
//   - Multiple whitespace normalization
//
// Part of PUR (Parallel Uploader and Runner) v1.0.0
package sanitize

import (
	"regexp"
	"strings"
)

// SanitizeCommand removes problematic characters from commands
func SanitizeCommand(cmd string) string {
	if cmd == "" {
		return cmd
	}

	// Remove Windows line endings (CRLF -> LF)
	cmd = strings.ReplaceAll(cmd, "\r\n", "\n")
	cmd = strings.ReplaceAll(cmd, "\r", "\n")

	// Remove invisible/zero-width characters
	cmd = removeInvisibleChars(cmd)

	// Normalize whitespace (multiple spaces/tabs -> single space)
	cmd = normalizeWhitespace(cmd)

	// Trim leading/trailing whitespace
	return strings.TrimSpace(cmd)
}

// removeInvisibleChars removes zero-width and other invisible Unicode characters
func removeInvisibleChars(s string) string {
	// List of invisible characters to remove
	invisibleChars := []string{
		"\u200B", // Zero-width space
		"\u200C", // Zero-width non-joiner
		"\u200D", // Zero-width joiner
		"\uFEFF", // Zero-width no-break space (BOM)
		"\u00AD", // Soft hyphen
		"\u2060", // Word joiner
		"\u180E", // Mongolian vowel separator
	}

	for _, char := range invisibleChars {
		s = strings.ReplaceAll(s, char, "")
	}

	return s
}

// normalizeWhitespace replaces sequences of whitespace with single spaces
func normalizeWhitespace(s string) string {
	// Replace multiple whitespace characters (spaces, tabs, etc.) with single space
	re := regexp.MustCompile(`[ \t]+`)
	s = re.ReplaceAllString(s, " ")

	// Replace multiple newlines with single newline
	re = regexp.MustCompile(`\n+`)
	s = re.ReplaceAllString(s, "\n")

	return s
}

// SanitizeField sanitizes a general CSV field (not command-specific)
func SanitizeField(field string) string {
	if field == "" {
		return field
	}

	// Remove invisible characters
	field = removeInvisibleChars(field)

	// Trim whitespace
	return strings.TrimSpace(field)
}
