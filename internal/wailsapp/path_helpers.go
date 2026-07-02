package wailsapp

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/rescale/rescale-int/internal/transfer/scan"
	"github.com/rescale/rescale-int/internal/validation"
)

// resolveSafeDownloadPath validates that relativePath stays within baseDir
// and returns the resolved absolute path. Returns error if path escapes baseDir.
func resolveSafeDownloadPath(relativePath, baseDir string) (string, error) {
	localPath, err := validation.ResolvePathInDirectory(relativePath, baseDir)
	if err != nil {
		return "", fmt.Errorf("path traversal rejected: %w", err)
	}
	return localPath, nil
}

// stripJobIOPrefix removes a leading "Input" or "Output" path segment (the
// platform's job-folder split) from a scanned relative path, so a job folder
// downloads with its files directly under the job folder — matching the
// auto-download layout. Deeper structure is preserved (e.g. "Output/run1/a.dat"
// -> "run1/a.dat"). A bare "Input"/"Output" segment maps to "". Comparison is
// case-insensitive since the platform's casing is not guaranteed. Paths without
// such a prefix are returned unchanged.
func stripJobIOPrefix(relPath string) string {
	if relPath == "" {
		return ""
	}
	// Normalize separators so the split is consistent regardless of how the
	// scanner joined the path on this platform.
	norm := filepath.ToSlash(relPath)
	first := norm
	rest := ""
	if idx := strings.IndexByte(norm, '/'); idx >= 0 {
		first = norm[:idx]
		rest = norm[idx+1:]
	}
	switch strings.ToLower(first) {
	case "input", "output":
		// An Input or Output folder inside one stays: moved up, it could hold
		// the split path a refused file's row keeps.
		if next, _, _ := strings.Cut(rest, "/"); jobIOSplit(next) != "" {
			return relPath
		}
		return filepath.FromSlash(rest)
	default:
		return relPath
	}
}

// jobIOSplit returns "input" or "output" for a name that is one half of a job
// folder's split, and "" for any other.
func jobIOSplit(name string) string {
	if lower := strings.ToLower(name); lower == "input" || lower == "output" {
		return lower
	}
	return ""
}

// jobIOMove returns where flattening puts a scanned file and the half of the
// split it leaves, or the path unchanged and "" for a file it does not move,
// a top-level file named Input or Output among them.
func jobIOMove(relPath string) (string, string) {
	flat := stripJobIOPrefix(relPath)
	if flat == "" || flat == relPath {
		return relPath, ""
	}
	first, _, _ := strings.Cut(filepath.ToSlash(relPath), "/")
	return flat, jobIOSplit(first)
}

// holdSplitFiles passes a flattened download's scan events on in an order
// that settles every clash the same way, whatever order the scan meets the
// files in: a file that is not moved takes its path first, then an Output
// file, then an Input file, which waits for the scan to end. Output files
// wait as well when holdOutput is set, where a file that is not moved may
// still follow them.
func holdSplitFiles(ctx context.Context, events <-chan scan.ScanEvent, holdOutput bool) <-chan scan.ScanEvent {
	out := make(chan scan.ScanEvent)
	go func() {
		defer close(out)
		send := func(event scan.ScanEvent) bool {
			select {
			case out <- event:
				return true
			case <-ctx.Done():
				return false
			}
		}
		held := map[string][]scan.ScanEvent{}
		for event := range events {
			split := ""
			if event.File != nil {
				_, split = jobIOMove(event.File.RelativePath)
			}
			if split == "input" || (split == "output" && holdOutput) {
				held[split] = append(held[split], event)
			} else if !send(event) {
				return
			}
		}
		for _, event := range append(held["output"], held["input"]...) {
			if !send(event) {
				return
			}
		}
	}()
	return out
}
