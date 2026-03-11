package config

import (
	"strings"
	"testing"
)

// Positive cases

func TestValidatePlatformURL_AllAllowed(t *testing.T) {
	for _, p := range AllowedPlatformURLs {
		if err := ValidatePlatformURL(p.URL); err != nil {
			t.Errorf("ValidatePlatformURL(%q) = %v, want nil", p.URL, err)
		}
	}
}

func TestValidatePlatformURL_NoScheme(t *testing.T) {
	// Auto-adds https://
	if err := ValidatePlatformURL("platform.rescale.com"); err != nil {
		t.Errorf("ValidatePlatformURL(no scheme) = %v, want nil", err)
	}
}

func TestValidatePlatformURL_TrailingSlash(t *testing.T) {
	if err := ValidatePlatformURL("https://eu.rescale.com/"); err != nil {
		t.Errorf("ValidatePlatformURL(trailing slash) = %v, want nil", err)
	}
}

func TestValidatePlatformURL_CaseInsensitive(t *testing.T) {
	if err := ValidatePlatformURL("PLATFORM.RESCALE.COM"); err != nil {
		t.Errorf("ValidatePlatformURL(uppercase) = %v, want nil", err)
	}
}

// Negative cases — unknown host

func TestValidatePlatformURL_Invalid(t *testing.T) {
	err := ValidatePlatformURL("https://evil.example.com")
	if err == nil {
		t.Fatal("expected error for unknown host")
	}
	if !strings.Contains(err.Error(), "unrecognized platform URL") {
		t.Errorf("error = %q, want 'unrecognized platform URL'", err.Error())
	}
}

func TestValidatePlatformURL_SubdomainAttack(t *testing.T) {
	// "platform.rescale.com.evil.com" should NOT match
	err := ValidatePlatformURL("https://platform.rescale.com.evil.com")
	if err == nil {
		t.Fatal("expected error for subdomain attack")
	}
}

func TestValidatePlatformURL_EmptyURL(t *testing.T) {
	err := ValidatePlatformURL("")
	if err == nil {
		t.Fatal("expected error for empty URL")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("error = %q, want 'empty'", err.Error())
	}
}

// Negative cases — strict origin enforcement (credential exfiltration vectors)

func TestValidatePlatformURL_HttpScheme(t *testing.T) {
	err := ValidatePlatformURL("http://platform.rescale.com")
	if err == nil {
		t.Fatal("expected error for http:// scheme")
	}
	if !strings.Contains(err.Error(), "HTTPS") {
		t.Errorf("error = %q, want mention of HTTPS", err.Error())
	}
}

func TestValidatePlatformURL_CustomPort(t *testing.T) {
	err := ValidatePlatformURL("https://platform.rescale.com:8443")
	if err == nil {
		t.Fatal("expected error for custom port")
	}
	if !strings.Contains(err.Error(), "port") {
		t.Errorf("error = %q, want mention of port", err.Error())
	}
}

func TestValidatePlatformURL_Userinfo(t *testing.T) {
	err := ValidatePlatformURL("https://user@platform.rescale.com")
	if err == nil {
		t.Fatal("expected error for userinfo")
	}
	if !strings.Contains(err.Error(), "userinfo") {
		t.Errorf("error = %q, want mention of userinfo", err.Error())
	}
}

func TestValidatePlatformURL_WithPath(t *testing.T) {
	err := ValidatePlatformURL("https://platform.rescale.com/foo")
	if err == nil {
		t.Fatal("expected error for path")
	}
	if !strings.Contains(err.Error(), "path") {
		t.Errorf("error = %q, want mention of path", err.Error())
	}
}

func TestValidatePlatformURL_WithQuery(t *testing.T) {
	err := ValidatePlatformURL("https://platform.rescale.com?bar=1")
	if err == nil {
		t.Fatal("expected error for query parameters")
	}
	if !strings.Contains(err.Error(), "query") {
		t.Errorf("error = %q, want mention of query", err.Error())
	}
}

func TestValidatePlatformURL_WithFragment(t *testing.T) {
	err := ValidatePlatformURL("https://platform.rescale.com#frag")
	if err == nil {
		t.Fatal("expected error for fragment")
	}
	if !strings.Contains(err.Error(), "fragment") {
		t.Errorf("error = %q, want mention of fragment", err.Error())
	}
}
