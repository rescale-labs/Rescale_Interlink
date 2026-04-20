package ipc

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"testing"
)

// TestErrorCodeTSParity asserts that frontend/src/lib/errors.ts declares
// exactly the same set of ErrorCode string values as internal/ipc/errors.go.
// Keeps the handwritten TS mirror in sync with the Go source of truth.
func TestErrorCodeTSParity(t *testing.T) {
	tsPath := locateTSFile(t)
	data, err := os.ReadFile(tsPath)
	if err != nil {
		t.Fatalf("reading TS file %s: %v", tsPath, err)
	}

	// Lines of the form: export const CodeXxx = "some_value";
	re := regexp.MustCompile(`(?m)^export\s+const\s+Code\w+\s*=\s*"([^"]+)"\s*;`)
	matches := re.FindAllSubmatch(data, -1)
	tsSet := make(map[string]struct{}, len(matches))
	for _, m := range matches {
		tsSet[string(m[1])] = struct{}{}
	}

	goSet := make(map[string]struct{}, len(CanonicalText))
	for code := range CanonicalText {
		goSet[string(code)] = struct{}{}
	}

	var missingInTS, extraInTS []string
	for v := range goSet {
		if _, ok := tsSet[v]; !ok {
			missingInTS = append(missingInTS, v)
		}
	}
	for v := range tsSet {
		if _, ok := goSet[v]; !ok {
			extraInTS = append(extraInTS, v)
		}
	}
	sort.Strings(missingInTS)
	sort.Strings(extraInTS)

	if len(missingInTS) > 0 {
		t.Errorf("codes present in internal/ipc/errors.go but missing from frontend/src/lib/errors.ts: %v", missingInTS)
	}
	if len(extraInTS) > 0 {
		t.Errorf("codes present in frontend/src/lib/errors.ts but missing from internal/ipc/errors.go: %v", extraInTS)
	}
}

// locateTSFile resolves the path to frontend/src/lib/errors.ts starting from
// the directory containing the running test source. Walks up until a repo
// root marker (go.mod) is found.
func locateTSFile(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("runtime.Caller failed")
	}
	dir := filepath.Dir(thisFile)
	for i := 0; i < 10; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, "frontend", "src", "lib", "errors.ts")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("could not locate go.mod walking up from %s", thisFile)
	return ""
}
