package installer

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWixFileValid ensures the WiX XML configuration is valid.
func TestWixFileValid(t *testing.T) {
	// Find the installer directory
	wxsPath := filepath.Join("rescale-interlink.wxs")
	if _, err := os.Stat(wxsPath); os.IsNotExist(err) {
		// Try from project root
		wxsPath = filepath.Join("..", "installer", "rescale-interlink.wxs")
	}

	data, err := os.ReadFile(wxsPath)
	if err != nil {
		t.Skipf("WiX file not found at %s: %v", wxsPath, err)
	}

	// Validate XML structure
	var result interface{}
	if err := xml.Unmarshal(data, &result); err != nil {
		t.Errorf("WiX file is not valid XML: %v", err)
	}
}

// TestWixFileContents checks for required elements in the WiX file.
func TestWixFileContents(t *testing.T) {
	wxsPath := filepath.Join("rescale-interlink.wxs")
	if _, err := os.Stat(wxsPath); os.IsNotExist(err) {
		wxsPath = filepath.Join("..", "installer", "rescale-interlink.wxs")
	}

	data, err := os.ReadFile(wxsPath)
	if err != nil {
		t.Skipf("WiX file not found: %v", err)
	}

	content := string(data)

	// Check for required elements
	required := []string{
		"Package",
		"Feature",
		"MainComponents",
		"ServiceComponents",
		"TrayComponents",
		"rescale-int.exe",
		"rescale-int-tray.exe",
		"InstallService",
		"UninstallService",
		"UpgradeCode",
	}

	for _, req := range required {
		if !strings.Contains(content, req) {
			t.Errorf("WiX file missing required element: %s", req)
		}
	}
}

// TestBuildScriptExists checks that the build script exists.
func TestBuildScriptExists(t *testing.T) {
	scripts := []string{
		"build-installer.ps1",
		filepath.Join("..", "installer", "build-installer.ps1"),
	}

	found := false
	for _, script := range scripts {
		if _, err := os.Stat(script); err == nil {
			found = true
			break
		}
	}

	if !found {
		t.Error("Build script (build-installer.ps1) not found")
	}
}
