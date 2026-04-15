package s3

import "testing"

func TestShouldUseFIPSEndpoint(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		expected bool
	}{
		{"ITAR GovCloud", "https://itar.rescale-gov.com", true},
		{"ITAR Rescale", "https://itar.rescale.com", true},
		{"ITAR GovCloud with path", "https://itar.rescale-gov.com/api/v2/", true},
		{"ITAR case insensitive", "https://ITAR.RESCALE.COM", true},
		{"standard platform", "https://platform.rescale.com", false},
		{"EU platform", "https://eu.rescale.com", false},
		{"KR platform", "https://kr.rescale.com", false},
		{"JP platform", "https://platform.rescale.jp", false},
		{"empty string", "", false},
		{"partial match rescale-gov without itar", "https://rescale-gov.com", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldUseFIPSEndpoint(tt.url)
			if got != tt.expected {
				t.Errorf("shouldUseFIPSEndpoint(%q) = %v, want %v", tt.url, got, tt.expected)
			}
		})
	}
}
