package filescan

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/rescale/rescale-int/internal/validation"
)

// FilesInFolder is Single Job's Add Folder: every entry under the folder that
// is not a folder, hidden ones too, a link listed rather than followed (here one
// back to the folder above, which would otherwise loop). A folder given as a
// link is the one link followed: it expands as the folder it names, under the
// link's path, where it used to expand to the link alone.
func TestFilesInFolder(t *testing.T) {
	root := t.TempDir()
	for _, f := range []string{"a.dat", ".hidden", filepath.Join("sub", "b.dat")} {
		writeScanFile(t, root, f)
	}
	names, dirs := []string{".hidden", "a.dat", filepath.Join("sub", "b.dat")}, []string{root}
	linked := filepath.Join(t.TempDir(), "linked")
	if os.Symlink(filepath.Dir(root), filepath.Join(root, "up")) == nil && os.Symlink(root, linked) == nil {
		names, dirs = append(names, "up"), append(dirs, linked)
	}

	for _, dir := range dirs {
		var want []string
		for _, name := range names {
			want = append(want, filepath.Join(dir, name))
		}
		if got := FilesInFolder(dir); !reflect.DeepEqual(got, want) {
			t.Errorf("FilesInFolder(%s) = %q, want %q", dir, got, want)
		}
	}
}

// What a run attaches to every job, in the order given and each once: an id:
// reference as written, a file by its absolute path, a folder as its files.
func TestCommonFiles_ExpandsFoldersInOrder(t *testing.T) {
	root := t.TempDir()
	for _, f := range []string{"deck.inp", filepath.Join("lib", "a.dat"), filepath.Join("lib", "sub", "b.dat")} {
		writeScanFile(t, root, f)
	}
	lib := filepath.Join(root, "lib")
	t.Chdir(root)
	deck, err := filepath.Abs("deck.inp")
	if err != nil {
		t.Fatal(err)
	}

	got, err := CommonFiles(" id:abc , deck.inp," + lib + ",,id:abc," + filepath.Join(lib, "a.dat"))
	want := []string{"id:abc", deck, filepath.Join(lib, "a.dat"), filepath.Join(lib, "sub", "b.dat")}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("CommonFiles = %q, %v; want %q", got, err, want)
	}
	if got, err := CommonFiles(" , "); err != nil || len(got) != 0 {
		t.Errorf("no entries: %q, %v; want none", got, err)
	}
}

// A listed folder's hidden files and folders are left out, so the .DS_Store in
// each folder browsed in Finder, or a checkout's .git/HEAD and .git/logs/HEAD,
// no longer collide. A hidden file listed by name still goes, and a hidden
// folder listed by name is expanded.
func TestCommonFiles_LeavesOutAFoldersHiddenEntries(t *testing.T) {
	root := t.TempDir()
	for _, f := range []string{"m1/.DS_Store", "m1/.env", "m1/a.dat", "m2/.DS_Store", "m2/b.dat",
		"repo/.git/HEAD", "repo/.git/logs/HEAD", "repo/run.sh", ".shared/c.dat"} {
		writeScanFile(t, root, filepath.FromSlash(f))
	}
	at := func(f string) string { return filepath.Join(root, filepath.FromSlash(f)) }

	got, err := CommonFiles(strings.Join([]string{at("m1"), at("m2"), at("repo"), at(".shared"), at("m1/.env")}, ","))
	want := []string{at("m1/a.dat"), at("m2/b.dat"), at("repo/run.sh"), at(".shared/c.dat"), at("m1/.env")}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("CommonFiles = %q, %v; want %q", got, err, want)
	}

	// A listed folder that is a link expands as the folder it names.
	if link := at("repo-link"); os.Symlink(at("repo"), link) == nil {
		if got, err := CommonFiles(link); err != nil || !reflect.DeepEqual(got, []string{filepath.Join(link, "run.sh")}) {
			t.Errorf("a linked folder: CommonFiles = %q, %v; want its one file that is not hidden", got, err)
		}
	}
}

// An entry the run could not upload refuses the run, naming it: a path that
// cannot be read, a folder with nothing to upload, a file that is not one, and
// two files that would land on one name, as ScanFiles refuses a job whose files
// would flatten onto one.
func TestCommonFiles_RefusesWhatCannotBeUploaded(t *testing.T) {
	root := t.TempDir()
	caseA, caseB, empty, dots := filepath.Join(root, "caseA"), filepath.Join(root, "caseB"), filepath.Join(root, "empty"), filepath.Join(root, "dots")
	writeScanFile(t, caseA, "model.inp")
	writeScanFile(t, caseB, filepath.Join("sub", "model.inp"))
	writeScanFile(t, dots, ".env")
	if err := os.Mkdir(empty, 0o755); err != nil {
		t.Fatal(err)
	}

	type refusal struct {
		name, value string
		want        []string
	}
	tests := []refusal{
		{"missing", filepath.Join(root, "missing.dat"), []string{filepath.Join(root, "missing.dat") + " cannot be read"}},
		{"empty folder", empty, []string{"common input folder " + empty + " holds no files"}},
		{"only hidden files", dots, []string{"common input folder " + dots + " holds no files (hidden ones are left out)"}},
		{"an id: with no ID", "id:", []string{"common input file id: names no file ID"}},
		{"one name twice", caseA + "," + caseB, []string{
			filepath.Join(caseA, "model.inp") + " and " + filepath.Join(caseB, "sub", "model.inp"), `uploaded as "model.inp"`,
		}},
	}
	// os.DevNull is the one special file every platform has, under the name
	// CommonFiles makes absolute.
	if devNull, err := filepath.Abs(os.DevNull); err == nil {
		if info, err := os.Stat(devNull); err == nil && !validation.IsFile(info.Mode()) {
			tests = append(tests, refusal{"not a file", os.DevNull, []string{devNull + " is not a regular file"}})
		}
	}
	// A link in a folder is listed, not followed, so a link to a folder is
	// refused as the upload would refuse it, and so is a link to nothing.
	linked, dangling := filepath.Join(root, "linked"), filepath.Join(root, "dangling")
	writeScanFile(t, linked, "ok.dat")
	writeScanFile(t, dangling, "ok.dat")
	if os.Symlink(caseA, filepath.Join(linked, "folder")) == nil &&
		os.Symlink(filepath.Join(root, "gone"), filepath.Join(dangling, "gone")) == nil {
		tests = append(tests,
			refusal{"a link to a folder", linked, []string{filepath.Join(linked, "folder") + " is a directory, not a file"}},
			refusal{"a link to nothing", dangling, []string{filepath.Join(dangling, "gone") + " cannot be read"}})
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CommonFiles(tt.value)
			if err == nil {
				t.Fatalf("CommonFiles(%q) = %q, want a refusal", tt.value, got)
			}
			for _, want := range tt.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("refusal %q does not say %q", err, want)
				}
			}
		})
	}
}
