package validation

import "testing"

// The platform accepts a job's SSH public key only of the types below, and
// says so only as it creates the job, once the job's inputs are uploaded. The
// key is checked with the rest of the job instead.
func TestValidateJobSpec_PublicKeyType(t *testing.T) {
	for _, key := range []string{
		"",
		"ssh-rsa AAAAB3NzaC1yc2E user@host",
		"SSH-RSA AAAAB3NzaC1yc2E",
		"ssh-dss AAAAB3NzaC1kc3M",
		"ecdsa-sha2-nistp256 AAAAE2VjZHNh",
		"ecdsa-sha2-nistp384 AAAAE2VjZHNh",
		"ecdsa-sha2-nistp521 AAAAE2VjZHNh",
	} {
		job := validSpec()
		job.PublicKey = key
		if errs := ValidateJobSpec(job); len(errs) != 0 {
			t.Errorf("key %q was refused: %v", key, errs)
		}
	}

	for key, want := range map[string]string{
		"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5 user@host": `public key type "ssh-ed25519" is not one the platform accepts; ` +
			"use one of ecdsa-sha2-nistp256, ecdsa-sha2-nistp384, ecdsa-sha2-nistp521, ssh-dss, ssh-rsa",
		"AAAAB3NzaC1yc2E": `public key type "AAAAB3NzaC1yc2E" is not one the platform accepts`,
		"ssh-rsa":         `public key "ssh-rsa" has no key after its type`,
	} {
		job := validSpec()
		job.PublicKey = key
		if errs := ValidateJobSpec(job); !slicesContain(errs, want) {
			t.Errorf("key %q: errors %v, want %q", key, errs, want)
		}
	}
}
