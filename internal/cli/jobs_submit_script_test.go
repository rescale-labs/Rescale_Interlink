package cli

import (
	"testing"

	"github.com/rescale/rescale-int/internal/api"
	"github.com/rescale/rescale-int/internal/pur/parser/testsupport"
)

// submitJobs runs jobs submit --create with one file in --files against a fake
// API, with the upload stood in for, and returns the body of the one job it
// creates.
func submitJobs(t *testing.T, args ...string) []byte {
	t.Helper()
	fake := &fakeJobsAPI{}
	orig := getAPIClientFn
	getAPIClientFn = func() (*api.Client, error) { return fake.client(t), nil }
	t.Cleanup(func() { getAPIClientFn = orig })
	var uploads uploadRecorder
	uploads.install(t)

	args = append(args, "--create", "--files", writeTempFile(t, "input.dat", "in"))
	if err := runPURCommand(t, newJobsSubmitCmd(), args...); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.creates) != 1 {
		t.Fatalf("%d job create(s), want 1 (requests: %v)", len(fake.creates), fake.requests)
	}
	return fake.creates[0]
}

// TestJobsSubmitScriptSendsWhatRescaleCLIWould: jobs submit --script takes a
// script written for rescale-cli and creates the job compat submit does,
// checked field by field, but for what the two commands do differently by
// design: here the command is the script body, and the --files upload follows
// the existing file where compat submit uploads the script itself.
func TestJobsSubmitScriptSendsWhatRescaleCLIWould(t *testing.T) {
	body := submitJobs(t, "--script", writeTempFile(t, "job.sh", testsupport.RescaleCLIScript))
	testsupport.CheckRequest(t, body, testsupport.RescaleCLIRequest("mpirun -np 8 simpleFoam -parallel",
		`[{"id": "FILE1", "decompress": true}, {"id": "uploaded-file-id", "decompress": false}]`))
}

// TestJobsSubmitJobFileKeepsItsInputFiles: --files adds its uploads after the
// input files a --job-file names, instead of replacing them.
func TestJobsSubmitJobFileKeepsItsInputFiles(t *testing.T) {
	body := submitJobs(t, "--job-file", writeTempFile(t, "job.json", `{"name":"jf","jobanalyses":[{"command":"./run.sh",`+
		`"analysis":{"code":"user_included"},"hardware":{"coreType":{"code":"emerald"},"coresPerSlot":1},`+
		`"inputFiles":[{"id":"JF1","decompress":true}]}]}`))
	want := `[{"decompress":true,"id":"JF1"},{"decompress":false,"id":"uploaded-file-id"}]`
	if got := testsupport.RequestFields(t, body)["jobanalyses[0].inputFiles"]; got != want {
		t.Errorf("inputFiles = %s, want %s", got, want)
	}
}
