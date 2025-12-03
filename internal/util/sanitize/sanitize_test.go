package sanitize

import (
	"testing"
)

func TestSanitizeCommand(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Windows line endings (CRLF)",
			input:    "command1\r\ncommand2",
			expected: "command1\ncommand2",
		},
		{
			name:     "Mac line endings (CR)",
			input:    "command1\rcommand2",
			expected: "command1\ncommand2",
		},
		{
			name:     "Zero-width space",
			input:    "command\u200Bwith\u200Bzero",
			expected: "commandwithzero",
		},
		{
			name:     "Zero-width non-joiner",
			input:    "test\u200Ccommand",
			expected: "testcommand",
		},
		{
			name:     "Zero-width joiner",
			input:    "test\u200Dcommand",
			expected: "testcommand",
		},
		{
			name:     "BOM (zero-width no-break space)",
			input:    "\uFEFFcommand",
			expected: "command",
		},
		{
			name:     "Soft hyphen",
			input:    "test\u00ADcommand",
			expected: "testcommand",
		},
		{
			name:     "Multiple spaces",
			input:    "command  with   many    spaces",
			expected: "command with many spaces",
		},
		{
			name:     "Multiple tabs",
			input:    "command\t\t\twith\t\ttabs",
			expected: "command with tabs",
		},
		{
			name:     "Trim leading whitespace",
			input:    "   command",
			expected: "command",
		},
		{
			name:     "Trim trailing whitespace",
			input:    "command   ",
			expected: "command",
		},
		{
			name:     "Trim both",
			input:    "  command  ",
			expected: "command",
		},
		{
			name:     "Multiple newlines",
			input:    "line1\n\n\nline2",
			expected: "line1\nline2",
		},
		{
			name:     "Combined issues",
			input:    "  ./run_1.sh\r\n  &&  python3  process_001.py  ",
			expected: "./run_1.sh\n && python3 process_001.py",
		},
		{
			name:     "Empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "Only whitespace",
			input:    "   \t\t   ",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := SanitizeCommand(tt.input)
			if result != tt.expected {
				t.Errorf("SanitizeCommand() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestSanitizeField(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Normal field",
			input:    "TestJob",
			expected: "TestJob",
		},
		{
			name:     "Field with whitespace",
			input:    "  TestJob  ",
			expected: "TestJob",
		},
		{
			name:     "Field with invisible chars",
			input:    "Test\u200BJob",
			expected: "TestJob",
		},
		{
			name:     "Empty field",
			input:    "",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := SanitizeField(tt.input)
			if result != tt.expected {
				t.Errorf("SanitizeField() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestRemoveInvisibleChars(t *testing.T) {
	input := "\u200B\u200C\u200D\uFEFF\u00ADtest\u2060\u180E"
	expected := "test"
	result := removeInvisibleChars(input)
	if result != expected {
		t.Errorf("removeInvisibleChars() = %q, want %q", result, expected)
	}
}

func TestNormalizeWhitespace(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Multiple spaces",
			input:    "a    b    c",
			expected: "a b c",
		},
		{
			name:     "Mixed spaces and tabs",
			input:    "a \t  \t b",
			expected: "a b",
		},
		{
			name:     "Multiple newlines",
			input:    "line1\n\n\n\nline2",
			expected: "line1\nline2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := normalizeWhitespace(tt.input)
			if result != tt.expected {
				t.Errorf("normalizeWhitespace() = %q, want %q", result, tt.expected)
			}
		})
	}
}
