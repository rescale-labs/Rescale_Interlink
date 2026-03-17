package tags

import (
	"reflect"
	"testing"
)

func TestNormalizeTags(t *testing.T) {
	tests := []struct {
		name  string
		input []string
		want  []string
	}{
		{"nil input", nil, nil},
		{"empty input", []string{}, nil},
		{"single tag", []string{"foo"}, []string{"foo"}},
		{"trim whitespace", []string{" foo ", " bar "}, []string{"foo", "bar"}},
		{"remove empty", []string{"foo", "", "bar"}, []string{"foo", "bar"}},
		{"deduplicate", []string{"foo", "bar", "foo"}, []string{"foo", "bar"}},
		{"all empty", []string{"", " ", "  "}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeTags(tt.input)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("NormalizeTags(%v) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseCommaSeparated(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{"empty", "", nil},
		{"whitespace only", "  ", nil},
		{"single", "foo", []string{"foo"}},
		{"multiple", "foo,bar,baz", []string{"foo", "bar", "baz"}},
		{"with spaces", " foo , bar , baz ", []string{"foo", "bar", "baz"}},
		{"with duplicates", "foo,bar,foo", []string{"foo", "bar"}},
		{"trailing comma", "foo,bar,", []string{"foo", "bar"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseCommaSeparated(tt.input)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseCommaSeparated(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}
