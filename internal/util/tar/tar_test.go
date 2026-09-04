package tar

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"regexp"
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

	out := filepath.Join(root, "case1_00000000.tar.gz")
	if err := CreateTarGzFromFiles([]string{primary, sibling, outside}, out, "gzip"); err != nil {
		t.Fatalf("CreateTarGzFromFiles: %v", err)
	}

	got := archiveContents(t, out)
	want := map[string]string{
		"case1.inp":  "primary",
		"case1.mesh": "sibling",
		"case1.cfg":  "outside",
	}

	if len(got) != len(want) {
		t.Fatalf("archive has %d entries (%v), want %d", len(got), got, len(want))
	}
	for name, contents := range want {
		if got[name] != contents {
			t.Errorf("entry %q = %q, want %q", name, got[name], contents)
		}
	}
}

func TestCreateTarGzFromFiles_Uncompressed(t *testing.T) {
	root := t.TempDir()
	primary := writeFile(t, filepath.Join(root, "case1.inp"), "primary")

	out := filepath.Join(root, "case1_00000000.tar")
	if err := CreateTarGzFromFiles([]string{primary}, out, "none"); err != nil {
		t.Fatalf("CreateTarGzFromFiles: %v", err)
	}

	if got := archiveContents(t, out); got["case1.inp"] != "primary" {
		t.Errorf("entry case1.inp = %q, want %q", got["case1.inp"], "primary")
	}
}

// Flattening makes two same-named files from different folders collide. Dropping
// one silently would give a job that is missing an input it was told it had.
func TestCreateTarGzFromFiles_RejectsDuplicateBaseNames(t *testing.T) {
	root := t.TempDir()
	a := writeFile(t, filepath.Join(root, "a", "mesh.cfg"), "a")
	b := writeFile(t, filepath.Join(root, "b", "mesh.cfg"), "b")

	out := filepath.Join(root, "dup_00000000.tar.gz")
	err := CreateTarGzFromFiles([]string{a, b}, out, "gzip")
	if err == nil {
		t.Fatal("expected an error for duplicate base names")
	}
	if !strings.Contains(err.Error(), "duplicate filename") {
		t.Errorf("error = %v, want it to mention a duplicate filename", err)
	}
	if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
		t.Error("partial archive was left behind after the error")
	}
}

func TestCreateTarGzFromFiles_MissingFile(t *testing.T) {
	root := t.TempDir()
	out := filepath.Join(root, "missing_00000000.tar.gz")

	err := CreateTarGzFromFiles([]string{filepath.Join(root, "nope.inp")}, out, "gzip")
	if err == nil {
		t.Fatal("expected an error for a missing file")
	}
	if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
		t.Error("partial archive was left behind after the error")
	}
}

func TestCreateTarGzFromFiles_EmptyList(t *testing.T) {
	if err := CreateTarGzFromFiles(nil, filepath.Join(t.TempDir(), "x.tar.gz"), "gzip"); err == nil {
		t.Fatal("expected an error for an empty file list")
	}
}

// The regression this guards: every job scanned out of one folder used to
// resolve to a single tar path, so the tar and upload workers raced over one
// file and uploads arrived truncated.
func TestGenerateTarPathForFiles_DistinctPerFileSet(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "inputs")

	one := GenerateTarPathForFiles([]string{
		filepath.Join(dir, "case1.inp"), filepath.Join(dir, "case1.mesh"),
	}, dir, "gzip")
	two := GenerateTarPathForFiles([]string{
		filepath.Join(dir, "case2.inp"), filepath.Join(dir, "case2.mesh"),
	}, dir, "gzip")

	if one == two {
		t.Fatalf("two file sets from one directory share the tar path %s", one)
	}

	// Named after the primary file so the log line is readable.
	if base := filepath.Base(one); !strings.HasPrefix(base, "case1_") {
		t.Errorf("tar name %q does not start with the primary file's stem", base)
	}

	// Compare against the directory-only namer, which is what collided.
	if GenerateTarPath(dir, dir, "gzip") == one {
		t.Error("file-set path matches the directory-only path")
	}
}

func TestGenerateTarPathForFiles_Stable(t *testing.T) {
	files := []string{"/data/inputs/case1.inp", "/data/inputs/case1.mesh"}
	if a, b := GenerateTarPathForFiles(files, "/tmp", "gzip"), GenerateTarPathForFiles(files, "/tmp", "gzip"); a != b {
		t.Errorf("not stable across calls: %s vs %s", a, b)
	}
}

// A member list is hashed with separators, so regrouping the same characters
// across names still changes the archive path.
func TestGenerateTarPathForFiles_SeparatorPreventsAmbiguity(t *testing.T) {
	a := GenerateTarPathForFiles([]string{"/d/ab", "/d/c"}, "/tmp", "none")
	b := GenerateTarPathForFiles([]string{"/d/a", "/d/bc"}, "/tmp", "none")
	if a == b {
		t.Errorf("ambiguous hash: %s", a)
	}
}

// safeRemoveTar refuses to delete a file whose name lacks the hash suffix, so a
// name this function produces must always carry one.
func TestGenerateTarPathForFiles_KeepsFNVSuffix(t *testing.T) {
	cases := [][]string{
		{"/data/case1.inp"},
		{"/data/.config"},        // all extension by filepath's reckoning
		{"/data/archive.tar.gz"}, // already looks like an archive
		{"/data/no-extension"},
	}

	for _, files := range cases {
		for _, compression := range []string{"gzip", "none"} {
			got := filepath.Base(GenerateTarPathForFiles(files, "/tmp", compression))
			if !fnvSuffixRe.MatchString(strings.ToLower(got)) {
				t.Errorf("GenerateTarPathForFiles(%v, %q) = %q, which lacks the FNV suffix",
					files, compression, got)
			}
		}
	}
}
