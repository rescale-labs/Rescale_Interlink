package cli

import (
	"strings"
	"testing"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/pur/parser/testsupport"
	"github.com/rescale/rescale-int/internal/reporting"
)

// jobs submit --job-file refuses a publicKey the platform would refuse before
// any request, its --files upload included, as --script does, rather than at
// the create call once the files are uploaded; the refusal saves no error
// report. A key of a type the platform accepts is sent with the job.
func TestJobsSubmitJobFileChecksItsPublicKeyFirst(t *testing.T) {
	usePURConfig(t)
	jobFile := func(key string) string {
		return writeTempFile(t, "job.json", `{"name":"jf","jobanalyses":[{"command":"./run.sh",`+
			`"analysis":{"code":"user_included"},"hardware":{"coreType":{"code":"emerald"},"coresPerSlot":1}}],`+
			`"publicKey":"`+key+`"}`)
	}
	fake := &fakeJobsAPI{}
	orig := getAPIClientFn
	getAPIClientFn = func() (*api.Client, error) { return fake.client(t), nil }
	t.Cleanup(func() { getAPIClientFn = orig })
	var uploads uploadRecorder
	uploads.install(t)

	refused := jobFile("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5 user@host")
	err := runPURCommand(t, newJobsSubmitCmd(), "--job-file", refused, "--create", "--files", writeTempFile(t, "input.dat", "in"))
	want := refused + `: public key type "ssh-ed25519" is not one the platform accepts`
	if err == nil || !strings.Contains(err.Error(), want) || len(uploads.uploaded()) > 0 || len(fake.calls()) > 0 {
		t.Errorf("error %v after uploading %v and requests %v, want %q before any", err, uploads.uploaded(), fake.calls(), want)
	}
	if saved := reporting.HandleCLIError(err, "cli", "rescale-int jobs submit", ""); saved != "" {
		t.Errorf("the refusal saved an error report to %s", saved)
	}

	const key = "ssh-rsa AAAAB3NzaC1yc2E user@host"
	if got := testsupport.RequestFields(t, submitJobs(t, "--job-file", jobFile(key)))["publicKey"]; got != `"`+key+`"` {
		t.Errorf("publicKey sent as %s, want %q", got, key)
	}
}
