package cli

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/rescale/rescale-int/internal/pur/filescan"
)

// --recursive is the GUI's Recursive scan box: "*.inp" then searches the
// subfolders too, as "**/*.inp" does.
func TestScanFilesRecursiveSearchesSubfolders(t *testing.T) {
	root := t.TempDir()
	for _, f := range []string{"top.inp", filepath.Join("a", "m1.inp"), filepath.Join("a", "b", "m2.inp")} {
		writeScanDeck(t, root, f)
	}

	out := runLogged(t, newScanFilesCmd(), "--root", root, "--primary", "*.inp", "--recursive", "--json")
	var result filescan.ScanResult
	if err := json.Unmarshal([]byte(out), &result); err != nil || len(result.Jobs) != 3 {
		t.Errorf("--recursive found %d files (%v), want all 3:\n%s", len(result.Jobs), err, out)
	}
}
