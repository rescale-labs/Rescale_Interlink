package paths

import (
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"

	"github.com/rescale/rescale-int/internal/validation"
)

// WritePrivateFile replaces path with data, readable by the owner alone on
// Unix. It stages the data beside path and renames it over, so a reader never
// sees half of it. The staging name is fixed per path, so what a killed write
// left there is reclaimed by the next one: a regular file there is removed, and
// a link or anything else is refused, since it could carry the data somewhere
// else. The staging file is then created afresh, exclusively and 0600.
func WritePrivateFile(path string, data []byte) error {
	tmp := stagingPath(path)
	if info, err := os.Lstat(tmp); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("refusing to stage %s: %s is a link or not a regular file", validation.Quote(path), validation.Quote(tmp))
		}
		if err := os.Remove(tmp); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		beforeRename()
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
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
