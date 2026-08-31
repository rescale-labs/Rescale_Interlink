package tar

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// gnuTarOnce caches the one-time detection of whether the `tar` on PATH is GNU
// tar, so the version probe runs once per process rather than once per archive.
var (
	gnuTarOnce sync.Once
	gnuTar     bool
)

// isGNUTar reports whether the `tar` binary on PATH is GNU tar.
//
// GNU tar interprets any argument containing a colon as a remote `host:path`
// spec, so a Windows absolute path like `C:\Users\...` makes it try to connect
// to a host named "C" ("Cannot connect to C: resolve failed"). Passing
// --force-local disables that. The Windows-native bsdtar does not have this
// behavior and does not accept --force-local, so the flag must only be added
// for GNU tar.
func isGNUTar() bool {
	gnuTarOnce.Do(func() {
		out, err := exec.Command("tar", "--version").CombinedOutput()
		if err == nil && strings.Contains(string(out), "GNU tar") {
			gnuTar = true
		}
	})
	return gnuTar
}

// CreateTarGz creates a tar archive of a directory using system tar command
// This matches the Python PUR behavior of using subprocess tar
// Supports both compressed (gzip) and uncompressed archives via the compression parameter
func CreateTarGz(sourceDir, outputPath string, useAbsolutePaths bool, compression string) error {
	// Validate source directory exists
	info, err := os.Stat(sourceDir)
	if err != nil {
		return fmt.Errorf("source directory does not exist: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("source path is not a directory: %s", sourceDir)
	}

	// Create output directory if needed
	outputDir := filepath.Dir(outputPath)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	// Build tar command based on compression setting
	// Python PUR uses: tar -czf output.tar.gz -C /parent/dir dirname (with compression)
	// Or: tar -cf output.tar -C /parent/dir dirname (without compression)

	var tarFlags string
	if compression == "none" {
		tarFlags = "-cf" // Create, no compression
	} else {
		tarFlags = "-czf" // Create with gzip compression (default)
	}

	var args []string
	if useAbsolutePaths {
		// For multi-part mode: use absolute paths
		args = []string{tarFlags, outputPath, "-P", sourceDir}
	} else {
		// Normal mode: relative paths, archive contents without parent directory
		parent := filepath.Dir(sourceDir)
		dirname := filepath.Base(sourceDir)
		args = []string{tarFlags, outputPath, "-C", parent, dirname}
	}

	// On Windows, GNU tar reads the drive-letter colon in an absolute path
	// (C:\...) as a remote host and fails with "Cannot connect to C: resolve
	// failed". --force-local keeps colons local. Only GNU tar needs (and
	// accepts) the flag; see isGNUTar.
	if isGNUTar() {
		args = append([]string{"--force-local"}, args...)
	}

	// Execute tar command
	cmd := exec.Command("tar", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("tar command failed: %w: %s", err, string(output))
	}

	// Verify output file was created
	if _, err := os.Stat(outputPath); err != nil {
		return fmt.Errorf("tar output file not created: %w", err)
	}

	return nil
}

// CreateTarGzWithOptions creates a tar archive with filtering and flattening options
// This uses Go's archive/tar package for fine-grained control
// Supports both compressed (gzip) and uncompressed archives via the compression parameter
func CreateTarGzWithOptions(sourceDir, outputPath string, useAbsolutePaths bool, includePatterns, excludePatterns []string, flatten bool, compression string) error {
	// Validate source directory exists
	info, err := os.Stat(sourceDir)
	if err != nil {
		return fmt.Errorf("source directory does not exist: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("source path is not a directory: %s", sourceDir)
	}

	// Create output directory if needed
	outputDir := filepath.Dir(outputPath)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	// Create output file
	outFile, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("failed to create tar file: %w", err)
	}
	defer outFile.Close()

	// Create tar writer (with or without compression based on config)
	var tarWriter *tar.Writer
	if compression == "none" {
		// No compression - write directly to file
		tarWriter = tar.NewWriter(outFile)
	} else {
		// With gzip compression (default)
		gzWriter := gzip.NewWriter(outFile)
		defer gzWriter.Close()
		tarWriter = tar.NewWriter(gzWriter)
	}
	defer tarWriter.Close()

	// Track filenames in flatten mode to detect duplicates
	fileNames := make(map[string]string) // filename -> original_path

	// Walk the source directory
	dirName := filepath.Base(sourceDir)

	err = filepath.Walk(sourceDir, func(filePath string, fileInfo os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip the root directory itself
		if filePath == sourceDir {
			return nil
		}

		// Get relative path from source directory
		relPath, err := filepath.Rel(sourceDir, filePath)
		if err != nil {
			return fmt.Errorf("failed to get relative path: %w", err)
		}

		// Apply filtering for files
		if !fileInfo.IsDir() {
			fileName := filepath.Base(filePath)
			if !shouldIncludeFile(fileName, includePatterns, excludePatterns) {
				return nil // Skip this file
			}
		}

		// Determine the tar entry name
		var tarPath string
		if flatten {
			// Flatten mode: use only the filename (no directories)
			if fileInfo.IsDir() {
				return nil // Skip directories in flatten mode
			}
			tarPath = filepath.Base(filePath)

			// Check for duplicate filenames
			if existingPath, exists := fileNames[tarPath]; exists {
				return fmt.Errorf("duplicate filename '%s' found in '%s' and '%s'",
					tarPath, existingPath, filePath)
			}
			fileNames[tarPath] = filePath
		} else if useAbsolutePaths {
			// Absolute path mode
			tarPath = filePath
		} else {
			// Normal mode: relative to parent directory
			tarPath = filepath.Join(dirName, relPath)
		}

		// Create tar header
		header, err := tar.FileInfoHeader(fileInfo, "")
		if err != nil {
			return fmt.Errorf("failed to create tar header: %w", err)
		}

		// Set the header name
		header.Name = tarPath

		// Write header
		if err := tarWriter.WriteHeader(header); err != nil {
			return fmt.Errorf("failed to write tar header: %w", err)
		}

		// Write file contents if it's a regular file
		if fileInfo.Mode().IsRegular() {
			file, err := os.Open(filePath)
			if err != nil {
				return fmt.Errorf("failed to open file: %w", err)
			}
			defer file.Close()

			if _, err := io.Copy(tarWriter, file); err != nil {
				return fmt.Errorf("failed to write file contents: %w", err)
			}
		}

		return nil
	})

	if err != nil {
		os.Remove(outputPath) // Clean up partial file
		return fmt.Errorf("failed to create tar: %w", err)
	}

	return nil
}

// CreateTarGzFromFiles archives an explicit list of files, each written at the
// archive root so it lands directly in the job's working directory.
//
// This is the archive shape file-scan mode needs: the file set is already known
// exactly, and its members can come from different directories (a secondary
// pattern such as "../meshes/*.cfg" resolves outside the primary file's folder).
// Walking a source directory cannot express either, which is why this does not
// go through CreateTarGzWithOptions.
//
// Flattening means two files can claim the same name, so a duplicate base name
// is an error rather than a silently dropped input.
func CreateTarGzFromFiles(files []string, outputPath, compression string) error {
	if len(files) == 0 {
		return fmt.Errorf("no files to archive")
	}

	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	outFile, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("failed to create tar file: %w", err)
	}

	err = writeFilesArchive(outFile, files, compression)
	if closeErr := outFile.Close(); err == nil && closeErr != nil {
		err = fmt.Errorf("failed to close tar file: %w", closeErr)
	}

	if err != nil {
		// Cleanup happens here, after the handle is closed: Windows refuses to
		// remove a file while anything still has it open, so deleting inside the
		// write loop would leave the partial archive behind.
		os.Remove(outputPath)
		return err
	}

	return nil
}

// writeFilesArchive writes the archive stream, closing its own writers so their
// buffers are flushed before CreateTarGzFromFiles inspects the result. The gzip
// and tar writers are closed explicitly rather than by defer because a failure
// to flush the trailer is a corrupt archive, not something to discard.
func writeFilesArchive(out io.Writer, files []string, compression string) error {
	var gzWriter *gzip.Writer
	var tarWriter *tar.Writer
	if compression == "none" {
		tarWriter = tar.NewWriter(out)
	} else {
		gzWriter = gzip.NewWriter(out)
		tarWriter = tar.NewWriter(gzWriter)
	}

	seen := make(map[string]string, len(files)) // archive name -> source path

	for _, filePath := range files {
		info, err := os.Stat(filePath)
		if err != nil {
			return fmt.Errorf("failed to stat %s: %w", filePath, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("not a regular file: %s (mode=%s)", filePath, info.Mode())
		}

		name := filepath.Base(filePath)
		if existing, dup := seen[name]; dup {
			return fmt.Errorf("duplicate filename '%s' found in '%s' and '%s'", name, existing, filePath)
		}
		seen[name] = filePath

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return fmt.Errorf("failed to create tar header for %s: %w", filePath, err)
		}
		header.Name = name

		if err := tarWriter.WriteHeader(header); err != nil {
			return fmt.Errorf("failed to write tar header for %s: %w", name, err)
		}

		if err := copyFileInto(tarWriter, filePath); err != nil {
			return err
		}
	}

	if err := tarWriter.Close(); err != nil {
		return fmt.Errorf("failed to finalize tar: %w", err)
	}
	if gzWriter != nil {
		if err := gzWriter.Close(); err != nil {
			return fmt.Errorf("failed to finalize gzip stream: %w", err)
		}
	}

	return nil
}

// copyFileInto streams one file into the archive. Split out so the file handle
// is closed as each file finishes rather than deferred to the end of the loop.
func copyFileInto(w io.Writer, filePath string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	if _, err := io.Copy(w, file); err != nil {
		return fmt.Errorf("failed to write contents of %s: %w", filePath, err)
	}
	return nil
}

// shouldIncludeFile determines if a file should be included based on patterns
// Logic matches Python PUR:
//   - If include_patterns specified: ONLY include files matching those patterns
//   - If exclude_patterns specified: Include all EXCEPT files matching those patterns
//   - If neither specified: Include everything
//   - include_patterns and exclude_patterns are mutually exclusive
func shouldIncludeFile(fileName string, includePatterns, excludePatterns []string) bool {
	if len(includePatterns) > 0 {
		// Include-only mode: file must match at least one include pattern
		for _, pattern := range includePatterns {
			matched, err := filepath.Match(pattern, fileName)
			if err == nil && matched {
				return true
			}
		}
		return false
	} else if len(excludePatterns) > 0 {
		// Exclude mode: file must NOT match any exclude pattern
		for _, pattern := range excludePatterns {
			matched, err := filepath.Match(pattern, fileName)
			if err == nil && matched {
				return false
			}
		}
		return true
	}
	// No patterns: include everything
	return true
}

// GenerateTarPath generates a path for the tar file with correct extension based on compression.
// Produces human-readable names using last 1-2 path components with an FNV hash
// suffix for collision safety. Example: "Testing_Run_6_a1b2c3d4.tar.gz"
func GenerateTarPath(directory, basePath, compression string) string {
	absDir, err := filepath.Abs(directory)
	if err != nil {
		absDir = filepath.Clean(directory)
	}

	baseName := filepath.Base(absDir)
	parentName := filepath.Base(filepath.Dir(absDir))

	var tarName string
	if parentName != "" && parentName != "." && parentName != string(os.PathSeparator) {
		tarName = parentName + "_" + baseName
	} else {
		tarName = baseName
	}

	// Append short FNV hash of the full absolute path for collision safety
	h := fnv.New32a()
	h.Write([]byte(absDir))
	tarName = fmt.Sprintf("%s_%08x", tarName, h.Sum32())

	ext := ".tar.gz"
	if compression == "none" {
		ext = ".tar"
	}

	return filepath.Join(basePath, tarName+ext)
}

// GenerateTarPathForFiles names the archive for an explicit file set, as
// produced by CreateTarGzFromFiles.
//
// The name comes from the first file's stem and the hash covers every member's
// absolute path, so two jobs drawing different files out of one directory get
// different archives. GenerateTarPath cannot be used for this: it hashes only
// the source directory, so every job scanned from the same folder would resolve
// to a single filename and the tar and upload workers would race over it.
//
// The "_<8 hex>.tar[.gz]" shape is required, not cosmetic — pathutil.HasFNVSuffix
// gates whether the pipeline is willing to delete the file afterwards.
func GenerateTarPathForFiles(files []string, basePath, compression string) string {
	h := fnv.New32a()

	stem := "job"
	for i, file := range files {
		abs, err := filepath.Abs(file)
		if err != nil {
			abs = filepath.Clean(file)
		}
		if i == 0 {
			base := filepath.Base(abs)
			stem = strings.TrimSuffix(base, filepath.Ext(base))
			if stem == "" {
				// A dotfile is all extension by filepath's reckoning (".config"),
				// which would leave the name starting at the hash separator.
				stem = strings.TrimPrefix(base, ".")
			}
		}
		h.Write([]byte(abs))
		h.Write([]byte{0}) // separator, so {"ab","c"} and {"a","bc"} differ
	}

	ext := ".tar.gz"
	if compression == "none" {
		ext = ".tar"
	}

	return filepath.Join(basePath, fmt.Sprintf("%s_%08x%s", stem, h.Sum32(), ext))
}

// ValidateTarExists checks if a tar file exists and is valid
func ValidateTarExists(tarPath string) (bool, error) {
	info, err := os.Stat(tarPath)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("failed to check tar file: %w", err)
	}

	// Check if file is not empty
	if info.Size() == 0 {
		return false, fmt.Errorf("tar file is empty")
	}

	return true, nil
}
