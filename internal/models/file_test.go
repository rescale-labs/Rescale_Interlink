package models

import (
	"slices"
	"testing"
)

// TestFileChecksumsSHA512: verification ran only for three exact spellings of
// SHA-512, so a checksum reported as "sha-512" meant a download that was never
// checked, and said nothing about it.
func TestFileChecksumsSHA512(t *testing.T) {
	for _, spelling := range []string{"sha512", "SHA-512", "SHA512", "sha-512", "Sha512"} {
		if got := (FileChecksums{{HashFunction: spelling, FileHash: "abc"}}).SHA512(); got != "abc" {
			t.Errorf("%q: got %q, want the checksum it carries", spelling, got)
		}
	}
	for _, other := range []string{"md5", "sha256", "SHA-512/256"} {
		if got := (FileChecksums{{HashFunction: other, FileHash: "abc"}}).SHA512(); got != "" {
			t.Errorf("%q was taken for SHA-512", other)
		}
	}
	// An entry with no hash is not a checksum, whatever it is called.
	if got := (FileChecksums{{HashFunction: "sha512"}, {HashFunction: "SHA-512", FileHash: "abc"}}).SHA512(); got != "abc" {
		t.Errorf("an empty SHA-512 hid the one after it: got %q", got)
	}
	if got := (FileChecksums{{HashFunction: "sha512"}, {HashFunction: "md5", FileHash: "abc"}}).Algorithms(); !slices.Equal(got, []string{"md5"}) {
		t.Errorf("Algorithms() = %q, want only the checksum that carries a hash", got)
	}
}
