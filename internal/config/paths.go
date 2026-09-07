// Package config provides configuration management for Rescale Interlink.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// LogDirectory returns the unified log directory for all Interlink logs.
//
// Locations:
//   - Windows: %LOCALAPPDATA%\Rescale\Interlink\logs
//   - macOS and Linux: ~/.config/rescale/logs (spec §9.1 target; pinned
//     explicitly so macOS does not resolve to ~/Library/Application Support/
//     via os.UserConfigDir()).
func LogDirectory() string {
	if runtime.GOOS == "windows" {
		localAppData := os.Getenv("LOCALAPPDATA")
		if localAppData == "" {
			homeDir, err := os.UserHomeDir()
			if err != nil {
				return filepath.Join(os.TempDir(), "rescale-interlink-logs")
			}
			localAppData = filepath.Join(homeDir, "AppData", "Local")
		}
		return filepath.Join(localAppData, "Rescale", "Interlink", "logs")
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "rescale-interlink-logs")
	}
	return filepath.Join(homeDir, ".config", "rescale", "logs")
}

// MacOSLegacyLogDirectory returns the pre-Plan-2 macOS log directory
// (~/Library/Application Support/rescale/logs), used only by the one-time
// log-file migration in RunStartupMigrations.
func MacOSLegacyLogDirectory() string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	configDir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(configDir, "rescale", "logs")
}

// LogDirectoryForUser returns the log directory for a specific user profile.
//
// On Windows, uses the user's profile path to construct the log directory:
//   - profilePath\AppData\Local\Rescale\Interlink\logs
func LogDirectoryForUser(profilePath string) string {
	if runtime.GOOS == "windows" {
		// Windows: profilePath\AppData\Local\Rescale\Interlink\logs
		return filepath.Join(profilePath, "AppData", "Local", "Rescale", "Interlink", "logs")
	}
	// Unix: Use profile-specific config directory
	return filepath.Join(profilePath, ".config", "rescale", "logs")
}

// ReportDirectory returns the directory for error report files.
//
// Locations:
//   - Windows: %LOCALAPPDATA%\Rescale\Interlink\reports
//   - Unix: ~/.config/rescale/reports
func ReportDirectory() string {
	if runtime.GOOS == "windows" {
		localAppData := os.Getenv("LOCALAPPDATA")
		if localAppData == "" {
			homeDir, err := os.UserHomeDir()
			if err != nil {
				return filepath.Join(os.TempDir(), "rescale-interlink-reports")
			}
			localAppData = filepath.Join(homeDir, "AppData", "Local")
		}
		return filepath.Join(localAppData, "Rescale", "Interlink", "reports")
	}

	configDir, err := os.UserConfigDir()
	if err != nil {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return filepath.Join(os.TempDir(), "rescale-interlink-reports")
		}
		return filepath.Join(homeDir, ".config", "rescale", "reports")
	}
	return filepath.Join(configDir, "rescale", "reports")
}

// EnsureReportDirectory creates the report directory if it doesn't exist.
func EnsureReportDirectory() error {
	return os.MkdirAll(ReportDirectory(), 0700)
}

// installIDFile names the installation identifier inside the per-user
// configuration directory, beside apiconfig and the token.
const installIDFile = "install-id"

// InstallID returns the identifier of the installation this process belongs to:
// a random string written once into this user's configuration directory and read
// back on every later call.
//
// It exists because no other string identifies the place a PID means something.
// Two machines can be configured with the same hostname and can carry the same
// uid, so a record naming both still says nothing about whether the process it
// names is running here; an identifier generated here does. The configuration
// directory is already per user, so the identifier is per user per installation
// — which is the boundary a PID is meaningful inside.
//
// Two processes creating it at once do not disagree: the file is created with
// O_EXCL and the loser reads the winner's.
func InstallID() (string, error) {
	dir := getConfigDir()
	if dir == "" {
		return "", errors.New("there is no configuration directory to keep the installation identifier in")
	}
	path := filepath.Join(dir, installIDFile)

	id, err := readInstallID(path)
	switch {
	case err == nil:
		return id, nil
	case !os.IsNotExist(err):
		return "", err
	}

	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("failed to create %s: %w", dir, err)
	}
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("failed to generate an installation identifier: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		if os.IsExist(err) {
			return readInstallID(path)
		}
		return "", fmt.Errorf("failed to create %s: %w", path, err)
	}
	id = hex.EncodeToString(buf)
	_, writeErr := file.WriteString(id)
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		// A half-written identifier would be read back as this installation's
		// by one process and as another's by the next.
		_ = os.Remove(path)
		return "", fmt.Errorf("failed to write %s: %w", path, err)
	}
	return id, nil
}

// readInstallID reads an identifier that is already on disk. A file holding
// nothing usable is not an identifier: reporting one would let every
// installation whose file is empty answer to the same name.
func readInstallID(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(data))
	if id == "" {
		return "", fmt.Errorf("%s holds no installation identifier", path)
	}
	return id, nil
}
