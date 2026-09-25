// Package validation provides input validation utilities for rescale-int.
package validation

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// ValidateFilename validates a filename (not a full path) to prevent path traversal.
// This should be used for validating filenames received from external sources
// (like API responses) before using them in filepath.Join operations.
//
// Returns an error if the filename:
//   - Is empty, or only dots ("." and ".." among them)
//   - Contains a path separator (/ or \), a control character, or any of : < > " | ? *
//   - Ends in a dot or a space
//   - Is a Windows device name, with or without an extension ("NUL.txt")
//
// The Windows rules apply on every platform: the name comes from the server,
// and on Windows a colon names an alternate data stream, the other characters
// cannot be stored, a trailing dot or space is dropped (so the file lands under
// another name), and a device name opens the device instead of a file.
func ValidateFilename(filename string) error {
	if filename == "" {
		return fmt.Errorf("filename cannot be empty")
	}
	name := Quote(filename)
	if strings.IndexFunc(filename, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return fmt.Errorf("filename cannot contain a control character: %s", name)
	}
	if strings.ContainsAny(filename, `/\`) {
		return fmt.Errorf("filename cannot contain path separators: %s", name)
	}
	// "." and ".." name directories; interior dots ("data..v2.csv") are fine.
	if strings.Trim(filename, ".") == "" {
		return fmt.Errorf("filename cannot be only dots: %s", name)
	}
	if i := strings.IndexAny(filename, `:<>"|?*`); i >= 0 {
		return fmt.Errorf("filename cannot contain %q: %s", filename[i], name)
	}
	if strings.HasSuffix(filename, ".") || strings.HasSuffix(filename, " ") {
		return fmt.Errorf("filename cannot end in a dot or a space: %s", name)
	}
	if base, _, _ := strings.Cut(filename, "."); windowsDeviceName.MatchString(base) {
		return fmt.Errorf("filename is a reserved Windows device name: %s", name)
	}
	return nil
}

// Quote quotes an untrusted name for a message as %q does, so control
// characters and trailing spaces show, but leaves each backslash single, so a
// Windows path reads as it was typed.
func Quote(s string) string {
	return strings.ReplaceAll(strconv.Quote(s), `\\`, `\`)
}

// QuoteUnsafe returns a server name as it is when ValidateFilename accepts it,
// and quoted otherwise, so a name that reaches a message though it names no
// file here cannot put control characters on a terminal.
func QuoteUnsafe(name string) string {
	if ValidateFilename(name) == nil {
		return name
	}
	return Quote(name)
}

// windowsDeviceName matches the part of a name before its first dot that
// Windows maps to a device, ignoring trailing spaces.
var windowsDeviceName = regexp.MustCompile(`(?i)^(CON|PRN|AUX|NUL|CONIN\$|CONOUT\$|(COM|LPT)[1-9\x{b9}\x{b2}\x{b3}]) *$`)

// ValidateID checks a job or file ID before it becomes part of a local path
// (a per-job folder, a collision suffix). Platform IDs are letters and digits;
// '-' and '_' are allowed too, since neither can move a path anywhere.
func ValidateID(id string) error {
	if !idPattern.MatchString(id) {
		return fmt.Errorf("%s is not a valid ID (letters, digits, '-' and '_' only)", Quote(id))
	}
	return nil
}

var idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// DownloadPath returns where a server file lands under dir: at its relative
// path when it has one, else under its name, which the caller has checked.
// Every component of the relative path is held to ValidateFilename's rules
// before the path is cleaned, so none can climb out of dir, name a stream or a
// device, or be renamed by Windows.
func DownloadPath(dir, name, relativePath string) (string, error) {
	if relativePath == "" {
		return ResolvePathInDirectory(name, dir)
	}
	for _, part := range strings.Split(relativePath, "/") {
		if err := ValidateFilename(part); err != nil {
			return "", fmt.Errorf("in %s: %w", Quote(relativePath), err)
		}
	}
	return ResolvePathInDirectory(relativePath, dir)
}

// ValidateDownloadTarget refuses a download path that exists as anything but a
// file. A symbolic link or a FIFO there is left as it is, never removed,
// followed or taken for a finished download. An Lstat just before the use,
// because Windows has no O_NOFOLLOW.
func ValidateDownloadTarget(paths ...string) error {
	for _, path := range paths {
		info, err := os.Lstat(path)
		switch {
		case os.IsNotExist(err):
		case err != nil:
			return fmt.Errorf("cannot check download path %s: %w", Quote(path), err)
		case info.Mode()&os.ModeSymlink != 0:
			return fmt.Errorf("refusing to download to %s: it is a symbolic link", Quote(path))
		case !IsFile(info.Mode()):
			return fmt.Errorf("refusing to download to %s: it is not a regular file", Quote(path))
		}
	}
	return nil
}

// IsFile reports whether mode is a file's. ModeIrregular counts: Windows
// reports cloud-sync placeholders (OneDrive) that way, and those are files.
func IsFile(mode os.FileMode) bool {
	return mode.Type()&^os.ModeIrregular == 0
}

// ValidatePathInDirectory validates that a path, when resolved, stays within baseDir.
// This is used when you want to ensure a path doesn't escape a designated directory.
//
// Both path and baseDir are cleaned and made absolute before comparison.
// Returns an error if the resolved path is not within baseDir.
//
// Example:
//
//	ValidatePathInDirectory("../../etc/passwd", "/tmp/uploads") // Error: escapes base dir
//	ValidatePathInDirectory("subdir/file.txt", "/tmp/uploads")   // OK: within base dir
func ValidatePathInDirectory(path string, baseDir string) error {
	_, err := ResolvePathInDirectory(path, baseDir)
	return err
}

// ResolvePathInDirectory validates that a path stays within baseDir and returns
// the resolved path to use for local filesystem operations.
func ResolvePathInDirectory(path string, baseDir string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("path cannot be empty")
	}
	if baseDir == "" {
		return "", fmt.Errorf("base directory cannot be empty")
	}

	// Clean both paths
	cleanPath := filepath.Clean(path)
	cleanBase := filepath.Clean(baseDir)

	// Make baseDir absolute if it isn't already
	var err error
	if !filepath.IsAbs(cleanBase) {
		cleanBase, err = filepath.Abs(cleanBase)
		if err != nil {
			return "", fmt.Errorf("failed to resolve base directory: %w", err)
		}
	}

	// Resolve path relative to base directory
	var resolvedPath string
	if filepath.IsAbs(cleanPath) {
		resolvedPath = cleanPath
	} else {
		resolvedPath = filepath.Join(cleanBase, cleanPath)
	}

	// Clean the resolved path
	resolvedPath = filepath.Clean(resolvedPath)

	// Check if resolved path is within base directory
	// Use filepath.Rel to check containment
	relPath, err := filepath.Rel(cleanBase, resolvedPath)
	if err != nil {
		return "", fmt.Errorf("failed to compute relative path: %w", err)
	}

	// If the relative path starts with "..", it's outside the base directory
	if strings.HasPrefix(relPath, ".."+string(filepath.Separator)) || relPath == ".." {
		return "", fmt.Errorf("path escapes base directory: %s (base: %s)", path, baseDir)
	}

	return resolvedPath, nil
}
