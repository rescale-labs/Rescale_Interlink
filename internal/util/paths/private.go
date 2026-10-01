package paths

import (
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"path/filepath"

	"github.com/rescale/rescale-int/internal/validation"
)

// WritePrivateFile replaces path with data, readable by the owner alone on
// Unix, staged under a fixed name (see WriteFileAtomic).
func WritePrivateFile(path string, data []byte) error {
	return WriteFileAtomic(path, 0600, false, func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	})
}

// WriteFileAtomic replaces path with what write writes, staged beside path in
// a new file and renamed over path once closed, so a reader never sees half of
// it. Whatever fails, the staging file is removed.
//
// With unique, each write stages under a name of its own, so writers that may
// overlap never write into one file, and a write killed before its rename
// leaves its staging file behind; the file's mode is set to perm exactly.
// Otherwise the staging name is fixed per path, so what a killed write left
// there is reclaimed by the next one: a regular file there is removed, and a
// link or anything else is refused, since it could carry the data somewhere
// else. The file is then created with perm as reduced by the umask.
func WriteFileAtomic(path string, perm os.FileMode, unique bool, write func(io.Writer) error) error {
	f, err := createStaging(path, perm, unique)
	if err != nil {
		return err
	}
	err = write(f)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		beforeRename()
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		os.Remove(f.Name())
	}
	return err
}

// createStaging creates WriteFileAtomic's staging file for path.
func createStaging(path string, perm os.FileMode, unique bool) (*os.File, error) {
	if unique {
		f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
		if err == nil {
			if err = f.Chmod(perm); err != nil {
				f.Close()
				os.Remove(f.Name())
				return nil, err
			}
		}
		return f, err
	}
	tmp := stagingPath(path)
	if info, err := os.Lstat(tmp); err == nil {
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("refusing to stage %s: %s is a link or not a regular file", validation.Quote(path), validation.Quote(tmp))
		}
		if err := os.Remove(tmp); err != nil {
			return nil, err
		}
	}
	return os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
}

// stagingPath is "<path>.tmp" when that fits in a 255-byte file name, and
// otherwise a short name derived from path's, so a path at the length limit
// can still be written.
func stagingPath(path string) string {
	dir, name := filepath.Split(path)
	if len(name) <= 251 {
		return path + ".tmp"
	}
	h := fnv.New64a()
	h.Write([]byte(name))
	return filepath.Join(dir, fmt.Sprintf(".interlink-%016x.tmp", h.Sum64()))
}

// beforeRename runs between writing the staging file and renaming it into
// place. Only a test sets it, to stop the process there.
var beforeRename = func() {}
