package tar

import (
	"path/filepath"
	"strings"
	"testing"
)

// The system tar read a job folder named "-run1" as an option, and bsdtar reads
// "@run2" as an archive to copy entries from. Each has to be archived as the
// folder it is, in both the relative and the -P form.
func TestCreateTarGzArchivesFoldersNamedLikeOptions(t *testing.T) {
	for _, name := range []string{"-run1", "@run2"} {
		for _, absolute := range []bool{false, true} {
			root := t.TempDir()
			writeFile(t, filepath.Join(root, name, "data.txt"), "payload")
			out := filepath.Join(t.TempDir(), "out.tar.gz")

			source := filepath.Join(root, name)
			if absolute {
				// The -P form archives the path as given, and a job's directory
				// can be relative.
				t.Chdir(root)
				source = name
			}
			if err := CreateTarGz(source, out, absolute, "gzip"); err != nil {
				t.Errorf("%s (absolute=%v): %v", name, absolute, err)
				continue
			}
			found := false
			for entry, body := range archiveContents(t, out) {
				found = found || (strings.HasSuffix(entry, name+"/data.txt") && body == "payload")
			}
			if !found {
				t.Errorf("%s (absolute=%v): the archive does not hold %s/data.txt", name, absolute, name)
			}
		}
	}
}
