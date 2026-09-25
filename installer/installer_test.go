package installer

import (
	"encoding/xml"
	"os"
	"strings"
	"testing"
)

// TestWixFile checks that the WiX configuration is valid XML and still names
// the components, executables, service actions and upgrade code the MSI needs,
// and that the script that builds the MSI from it is there.
func TestWixFile(t *testing.T) {
	if _, err := os.Stat("build-installer.ps1"); err != nil { // go test runs in this directory
		t.Errorf("the installer build script: %v", err)
	}
	data, err := os.ReadFile("rescale-interlink.wxs")
	if err != nil {
		t.Fatalf("reading the WiX file: %v", err)
	}

	var result interface{}
	if err := xml.Unmarshal(data, &result); err != nil {
		t.Errorf("WiX file is not valid XML: %v", err)
	}

	for _, req := range []string{
		"Package", "Feature", "MainComponents", "ServiceComponents", "TrayComponents",
		"rescale-int.exe", "rescale-int-tray.exe", "InstallService", "UninstallService", "UpgradeCode",
	} {
		if !strings.Contains(string(data), req) {
			t.Errorf("WiX file missing required element: %s", req)
		}
	}
}
