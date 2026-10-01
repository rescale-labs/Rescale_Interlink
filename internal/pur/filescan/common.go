package filescan

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/rescale/rescale-int/internal/localfs"
	"github.com/rescale/rescale-int/internal/validation"
)

// FilesInFolder lists every entry under dir that is not a folder: what a
// folder stands for as job inputs, in Single Job's Add Folder and in PUR's
// common files alike. Hidden files are included, links are listed rather than
// followed, and a folder that cannot be read is passed over.
//
// dir itself may be a link to a folder. Walked as it is, it would be listed as
// one entry, the link, which the upload then refuses as a directory; with a
// separator after it the walk starts in the folder it names. Links inside are
// still only listed, so one back to a parent cannot loop.
func FilesInFolder(dir string) []string {
	var files []string
	// The callback passes over every error, so the walk itself returns none.
	_ = filepath.WalkDir(dir+string(filepath.Separator), func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			files = append(files, path)
		}
		return nil
	})
	return files
}

// CommonFiles checks a PUR batch's common input files, written as one
// comma-separated value, and returns what the run attaches to every job, in
// the order written and each once: an id:<fileId> reference as it is, a local
// file by its absolute path, and a folder as the files FilesInFolder finds in
// it, hidden ones aside.
//
// Called before the run starts, so an entry the upload would refuse stops the
// run there, naming it, instead of failing it once the upload folder exists and
// other files are on their way. Every common file arrives under its own name
// beside each job's inputs, so two of one name would land on each other: the
// reason ScanFiles skips a job whose files would flatten onto one name.
func CommonFiles(value string) ([]string, error) {
	var entries []string
	seen := make(map[string]bool)     // entries kept, so a repeat is dropped
	byName := make(map[string]string) // upload name -> the file uploaded under it

	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item == "" || seen[item] {
			continue
		}
		if strings.HasPrefix(item, "id:") {
			if item == "id:" {
				return nil, errors.New("common input file id: names no file ID")
			}
			seen[item] = true
			entries = append(entries, item)
			continue
		}

		abs, err := filepath.Abs(item)
		if err != nil {
			return nil, fmt.Errorf("common input file %s: %w", item, err)
		}
		info, err := os.Stat(abs)
		if err != nil {
			return nil, fmt.Errorf("common input file %s %s", abs, notRegularReason(nil, err))
		}
		files := []string{abs}
		if info.IsDir() {
			// Hidden files and folders are left out, as PUR's scans leave them
			// out: the .DS_Store of every folder browsed in Finder, or a
			// checkout's .git/HEAD and .git/logs/HEAD, would collide by name. A
			// hidden file listed by name still goes.
			files = slices.DeleteFunc(FilesInFolder(abs), func(file string) bool {
				rel, _ := filepath.Rel(abs, file)
				return slices.ContainsFunc(strings.Split(rel, string(filepath.Separator)), localfs.IsHiddenName)
			})
			if len(files) == 0 {
				return nil, fmt.Errorf("common input folder %s holds no files (hidden ones are left out)", abs)
			}
		}

		for _, file := range files {
			if seen[file] {
				continue
			}
			// Stat follows a link, as the upload does, so a link to a file is
			// one; validation.IsFile is the upload's own test.
			if info, err := os.Stat(file); err != nil || !validation.IsFile(info.Mode()) {
				return nil, fmt.Errorf("common input file %s %s", file, notRegularReason(info, err))
			}
			name := filepath.Base(file)
			if first, taken := byName[name]; taken {
				return nil, fmt.Errorf("common input files %s and %s would both be uploaded as %q", first, file, name)
			}
			byName[name] = file
			seen[file] = true
			entries = append(entries, file)
		}
	}
	return entries, nil
}
