// Package strings provides string utility functions.
// v3.2.0 (December 2, 2025)
package strings

// Pluralize returns singular or plural form based on count.
// Example: Pluralize("file", 1) returns "file", Pluralize("file", 2) returns "files"
func Pluralize(word string, count int64) string {
	if count == 1 {
		return word
	}
	return word + "s"
}
