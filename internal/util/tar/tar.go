package tar

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

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

// GenerateTarPath generates a path for the tar file with correct extension based on compression
func GenerateTarPath(directory, basePath, compression string) string {
	// Clean the directory path
	cleanDir := filepath.Clean(directory)

	// Replace path separators with underscores to create filename
	tarName := strings.ReplaceAll(cleanDir, string(os.PathSeparator), "_")
	tarName = strings.TrimPrefix(tarName, "_")

	// Remove any leading dots
	tarName = strings.TrimPrefix(tarName, ".")

	// Add extension based on compression setting (.tar or .tar.gz)
	if compression == "none" {
		tarName = tarName + ".tar"
	} else {
		tarName = tarName + ".tar.gz"
	}

	// Join with base path (typically a .pur_temp directory)
	return filepath.Join(basePath, tarName)
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
