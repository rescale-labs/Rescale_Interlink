package tar

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// fnvSuffixRe mirrors pathutil.fnvSuffixRe. Duplicated rather than imported so
// the tar package keeps no dependency on pathutil, but the shapes must agree:
// the pipeline refuses to delete an archive whose name does not match.
var fnvSuffixRe = regexp.MustCompile(`_[0-9a-f]{8}\.(tar\.gz|tar)$`)

// writeFile creates a file with known contents, making parents as needed.
func writeFile(t *testing.T, path, contents string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(contents), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// archiveContents reads an archive back as name -> contents.
func archiveContents(t *testing.T, path string) map[string]string {
	t.Helper()

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer f.Close()

	var r io.Reader = f
	if strings.HasSuffix(path, ".gz") {
		gz, err := gzip.NewReader(f)
		if err != nil {
			t.Fatalf("gzip reader: %v", err)
		}
		defer gz.Close()
		r = gz
	}

	out := make(map[string]string)
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read archive: %v", err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("read entry %s: %v", hdr.Name, err)
		}
		out[hdr.Name] = string(body)
	}
	return out
}

// The point of the function: exactly the listed files, at the archive root, even
// when they come from different directories. Anything under the primary file's
// folder that was not matched must stay out.
func TestCreateTarGzFromFiles_FlattensExplicitSet(t *testing.T) {
	root := t.TempDir()
	primary := writeFile(t, filepath.Join(root, "inputs", "case1.inp"), "primary")
	sibling := writeFile(t, filepath.Join(root, "inputs", "case1.mesh"), "sibling")
	outside := writeFile(t, filepath.Join(root, "meshes", "case1.cfg"), "outside")
	writeFile(t, filepath.Join(root, "inputs", "case2.inp"), "other job")
	writeFile(t, filepath.Join(root, "inputs", "notes.log"), "noise")

	want := map[string]string{
		"case1.inp":  "primary",
		"case1.mesh": "sibling",
		"case1.cfg":  "outside",
	}

	// Both archive flavors, since the entry set must not depend on compression.
	for _, flavor := range []struct{ compression, ext string }{{"gzip", ".tar.gz"}, {"none", ".tar"}} {
		t.Run(flavor.compression, func(t *testing.T) {
			out := filepath.Join(root, "case1_00000000"+flavor.ext)
			if err := CreateTarGzFromFiles([]string{primary, sibling, outside}, out, flavor.compression); err != nil {
				t.Fatalf("CreateTarGzFromFiles: %v", err)
			}

			got := archiveContents(t, out)
			if len(got) != len(want) {
				t.Fatalf("archive has %d entries (%v), want %d", len(got), got, len(want))
			}
			for name, contents := range want {
				if got[name] != contents {
					t.Errorf("entry %q = %q, want %q", name, got[name], contents)
				}
			}
		})
	}
}

// Every refusal happens before anything is written: a half-written archive is
// one the pipeline may find and upload.
func TestCreateTarGzFromFiles_Rejects(t *testing.T) {
	root := t.TempDir()
	dupA := writeFile(t, filepath.Join(root, "a", "mesh.cfg"), "a")
	dupB := writeFile(t, filepath.Join(root, "b", "mesh.cfg"), "b")

	tests := []struct {
		name   string
		files  []string
		wantIn string
	}{
		// Named rather than silently dropped; see CreateTarGzFromFiles.
		{"duplicate base names", []string{dupA, dupB}, "duplicate filename"},
		{"a file that is not there", []string{filepath.Join(root, "nope.inp")}, "failed to stat"},
		{"an empty list", nil, "no files to archive"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "out_00000000.tar.gz")

			err := CreateTarGzFromFiles(tt.files, out, "gzip")
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tt.wantIn) {
				t.Errorf("error = %v, want it to mention %q", err, tt.wantIn)
			}
			if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
				t.Error("partial archive was left behind after the error")
			}
		})
	}
}

// archiveHeaders reads an archive back as name -> header, for the parts of an
// entry that are not its contents (link targets, types).
func archiveHeaders(t *testing.T, path string) map[string]*tar.Header {
	t.Helper()

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer f.Close()

	var r io.Reader = f
	if strings.HasSuffix(path, ".gz") {
		gz, err := gzip.NewReader(f)
		if err != nil {
			t.Fatalf("gzip reader: %v", err)
		}
		defer gz.Close()
		r = gz
	}

	out := make(map[string]*tar.Header)
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read archive: %v", err)
		}
		out[hdr.Name] = hdr
	}
	return out
}

// countingWriter records how many bytes a complete archive takes, so a test can
// place a budget exactly one byte short of it.
type countingWriter struct{ n int64 }

func (c *countingWriter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	return len(p), nil
}

// budgetWriter accepts writes until the budget is spent, then fails every write
// the way a volume that has just filled up does.
type budgetWriter struct {
	budget  int64
	written int64
}

var errNoSpace = errors.New("no space left on device")

func (b *budgetWriter) Write(p []byte) (int, error) {
	if b.written+int64(len(p)) > b.budget {
		return 0, errNoSpace
	}
	b.written += int64(len(p))
	return len(p), nil
}

// A write that fails while the archive is being finalized — the tar padding or
// the gzip trailer — is the disk filling up on the last block. Discarding it
// returns a truncated archive as a success, and the pipeline uploads it.
func TestCreateTarGzWithOptions_FinalizationFailure(t *testing.T) {
	source := t.TempDir()
	writeFile(t, filepath.Join(source, "case.inp"), strings.Repeat("input deck\n", 200))
	writeFile(t, filepath.Join(source, "sub", "mesh.cfg"), strings.Repeat("mesh\n", 200))

	for _, compression := range []string{"gzip", "none"} {
		t.Run(compression, func(t *testing.T) {
			// One byte short of the whole stream, so every entry is written and
			// only the closing block fails. A smaller budget would fail during a
			// file body instead, which the walk already reports.
			var counter countingWriter
			if err := writeDirArchive(&counter, source, false, nil, nil, false, compression); err != nil {
				t.Fatalf("sizing run: %v", err)
			}

			budget := &budgetWriter{budget: counter.n - 1}
			err := writeDirArchive(budget, source, false, nil, nil, false, compression)
			if err == nil {
				t.Fatal("archive finalization failed but the archiver reported success")
			}
			if !errors.Is(err, errNoSpace) {
				t.Errorf("error = %v, want it to carry %v", err, errNoSpace)
			}
		})
	}
}

// The partial archive has to go, and it can only go once nothing holds it open:
// Windows refuses to remove a file with a live handle, so a removal ordered
// before the closes leaves the truncated archive at the path the pipeline reads.
func TestCreateTarGzWithOptions_RemovesPartialArchive(t *testing.T) {
	source := t.TempDir()
	writeFile(t, filepath.Join(source, "a", "mesh.cfg"), "a")
	writeFile(t, filepath.Join(source, "b", "mesh.cfg"), "b")

	out := filepath.Join(t.TempDir(), "out_00000000.tar.gz")

	// Flatten mode makes the two mesh.cfg files collide, which the walk refuses.
	err := CreateTarGzWithOptions(source, out, false, nil, nil, true, "gzip")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "duplicate filename") {
		t.Errorf("error = %v, want it to mention the duplicate", err)
	}
	if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
		t.Error("partial archive was left behind after the error")
	}
}

// Tar entry names are slash-separated whatever the host separator is, and a
// symlink entry without its target is a link to nothing once extracted.
func TestCreateTarGzWithOptions_EntryNamesAndSymlinks(t *testing.T) {
	source := filepath.Join(t.TempDir(), "run")
	writeFile(t, filepath.Join(source, "inputs", "case.inp"), "deck")

	link := filepath.Join(source, "inputs", "latest.inp")
	if err := os.Symlink("case.inp", link); err != nil {
		t.Skipf("symlinks unavailable here: %v", err)
	}

	out := filepath.Join(t.TempDir(), "run_00000000.tar.gz")
	if err := CreateTarGzWithOptions(source, out, false, nil, nil, false, "gzip"); err != nil {
		t.Fatalf("CreateTarGzWithOptions: %v", err)
	}

	headers := archiveHeaders(t, out)

	// path.Join, not filepath.Join: the expected name is the archive's, not the
	// host's, and on Windows the two disagree.
	wantName := path.Join("run", "inputs", "latest.inp")
	hdr, ok := headers[wantName]
	if !ok {
		names := make([]string, 0, len(headers))
		for name := range headers {
			names = append(names, name)
		}
		sort.Strings(names)
		t.Fatalf("no entry named %q; archive holds %v", wantName, names)
	}
	if hdr.Typeflag != tar.TypeSymlink {
		t.Errorf("%s recorded as type %q, want a symlink", wantName, hdr.Typeflag)
	}
	if hdr.Linkname != "case.inp" {
		t.Errorf("%s links to %q, want case.inp — an empty target extracts as a link to nothing", wantName, hdr.Linkname)
	}

	for name := range headers {
		if strings.Contains(name, `\`) {
			t.Errorf("entry name %q uses the host separator; tar names are always slash-separated", name)
		}
	}
}

// The archive path must vary with everything that identifies an archive and with
// nothing else — see GenerateTarPathForFiles for what each part is for. The
// regression it guards: every job scanned out of one folder resolved to a single
// tar path, so the workers raced over it and uploads arrived truncated.
func TestGenerateTarPathForFiles_Identity(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "inputs")
	set1 := []string{filepath.Join(dir, "case1.inp"), filepath.Join(dir, "case1.mesh")}
	set2 := []string{filepath.Join(dir, "case2.inp"), filepath.Join(dir, "case2.mesh")}
	path := func(files []string, index int) string {
		return GenerateTarPathForFiles(files, index, dir, "gzip")
	}

	distinct := map[string][2]string{
		"two file sets from one directory": {path(set1, 1), path(set2, 2)},
		"two jobs over one file set":       {path(set1, 1), path(set1, 2)},
		// The member list is hashed with separators, so regrouping the same
		// characters across names still changes the path.
		"the same characters regrouped across names": {
			path([]string{"/d/ab", "/d/c"}, 1), path([]string{"/d/a", "/d/bc"}, 1),
		},
		// Compared against the directory-only namer, which is what collided.
		"against the directory-only namer": {path(set1, 1), GenerateTarPath(dir, dir, "gzip")},
	}
	for name, pair := range distinct {
		if pair[0] == pair[1] {
			t.Errorf("%s: both resolved to %s", name, pair[0])
		}
	}

	// A resumed run recomputes this path rather than reading it back from state.
	if a, b := path(set1, 3), path(set1, 3); a != b {
		t.Errorf("not stable across calls: %s vs %s", a, b)
	}

	base := filepath.Base(path(set1, 1))
	// The job index leads, then the primary file's stem.
	if !strings.HasPrefix(base, "1_case1_") {
		t.Errorf("tar name %q does not start with the job index and the primary file's stem", base)
	}
	if !fnvSuffixRe.MatchString(strings.ToLower(base)) {
		t.Errorf("%q lacks the FNV suffix safeRemoveTar gates deletion on", base)
	}

	// However odd the primary file is called, the name still has to carry the
	// suffix and still has to be one os.Create will accept.
	edge := []struct {
		name  string
		files []string
	}{
		{"a dotfile", []string{"/data/.config"}}, // all extension by filepath's reckoning
		{"a name that already looks like an archive", []string{"/data/archive.tar.gz"}},
		{"no extension", []string{"/data/no-extension"}},
		// The whole stem used to reach the name, so a long one produced a
		// basename os.Create refuses — long after the scan accepted the file.
		{"a 240-byte stem", []string{"/data/" + strings.Repeat("a", 240) + ".inp"}},
	}
	for _, tc := range edge {
		for _, compression := range []string{"gzip", "none"} {
			got := filepath.Base(GenerateTarPathForFiles(tc.files, 1, "/tmp", compression))
			if !fnvSuffixRe.MatchString(strings.ToLower(got)) {
				t.Errorf("%s (%q) = %q, which lacks the FNV suffix", tc.name, compression, got)
			}
			// 255 bytes is the per-component limit os.Create fails at.
			if len(got) >= 255 {
				t.Errorf("%s (%q) = a %d-byte name, which the filesystem will not create",
					tc.name, compression, len(got))
			}
		}
	}
}
