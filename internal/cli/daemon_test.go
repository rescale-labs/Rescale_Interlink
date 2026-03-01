package cli

import (
	"strings"
	"testing"
)

func TestRedactArgs_SpaceSeparated(t *testing.T) {
	args := []string{"rescale-int", "daemon", "run", "--api-key", "secret123", "--poll-interval", "2m"}
	result := redactArgs(args)

	if result[4] != "[REDACTED]" {
		t.Errorf("Expected [REDACTED], got %q", result[4])
	}
	// Original should not be mutated
	if args[4] != "secret123" {
		t.Errorf("Original args were mutated: %q", args[4])
	}
	// Other args should be unchanged
	if result[5] != "--poll-interval" {
		t.Errorf("Non-key arg was modified: %q", result[5])
	}
}

func TestRedactArgs_EqualsSeparated(t *testing.T) {
	args := []string{"rescale-int", "daemon", "run", "--api-key=secret123", "--poll-interval", "2m"}
	result := redactArgs(args)

	if result[3] != "--api-key=[REDACTED]" {
		t.Errorf("Expected --api-key=[REDACTED], got %q", result[3])
	}
	// Original should not be mutated
	if args[3] != "--api-key=secret123" {
		t.Errorf("Original args were mutated: %q", args[3])
	}
}

func TestRedactArgs_NoApiKey(t *testing.T) {
	args := []string{"rescale-int", "daemon", "run", "--poll-interval", "2m"}
	result := redactArgs(args)

	if strings.Join(result, " ") != strings.Join(args, " ") {
		t.Errorf("Args without --api-key should be unchanged, got %v", result)
	}
}

func TestRedactArgs_ApiKeyAtEnd(t *testing.T) {
	// --api-key as last arg with no value following (edge case)
	args := []string{"rescale-int", "--api-key"}
	result := redactArgs(args)

	// Should not panic or modify anything since there's no next arg
	if result[1] != "--api-key" {
		t.Errorf("Expected --api-key unchanged (no value to redact), got %q", result[1])
	}
}

func TestRedactArgs_EmptyArgs(t *testing.T) {
	result := redactArgs([]string{})
	if len(result) != 0 {
		t.Errorf("Expected empty result, got %v", result)
	}
}
