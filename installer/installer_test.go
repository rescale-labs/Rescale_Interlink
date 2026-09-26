package installer

import (
	"encoding/xml"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

// TestWixFile checks the WiX configuration's elements, not its comments: it
// installs the GUI, the CLI and the tray, upgrades in place, launches the tray
// and removes an earlier version's service on uninstall, and it neither
// installs nor starts a service. The script that builds the MSI must be there.
func TestWixFile(t *testing.T) {
	if _, err := os.Stat("build-installer.ps1"); err != nil { // go test runs in this directory
		t.Errorf("the installer build script: %v", err)
	}
	f, err := os.Open("rescale-interlink.wxs")
	if err != nil {
		t.Fatalf("reading the WiX file: %v", err)
	}
	defer f.Close()

	// "Element" for an element, "Element#Id" for one with an Id, and each
	// attribute value, for the checks below.
	found := map[string]bool{}
	var values []string
	for dec := xml.NewDecoder(f); ; {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("WiX file is not valid XML: %v", err)
		}
		el, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		found[el.Name.Local] = true
		for _, a := range el.Attr {
			if a.Name.Local == "Id" {
				found[el.Name.Local+"#"+a.Value] = true
			}
			values = append(values, a.Value)
		}
	}
	for _, want := range []string{
		"Package", "MajorUpgrade", "Feature#MainFeature", "Feature#TrayFeature",
		"ComponentGroup#MainComponents", "ComponentGroup#TrayComponents",
		"File#RescaleIntExe", "File#RescaleIntGuiExe", "File#TrayExe",
		"CustomAction#UninstallService", "CustomAction#LaunchTray",
	} {
		if !found[want] {
			t.Errorf("WiX file lacks %s", want)
		}
	}
	for _, unwanted := range []string{"ServiceInstall", "ServiceControl", "ServiceConfig", "ComponentGroup#ServiceComponents"} {
		if found[unwanted] {
			t.Errorf("WiX file has %s: the package installs no service", unwanted)
		}
	}
	joined := strings.Join(values, "\n")
	if !strings.Contains(joined, "rescale-int.exe service uninstall") {
		t.Error("WiX file does not remove an earlier version's service")
	}
	for _, unwanted := range []string{"service install", "service start"} {
		if strings.Contains(joined, unwanted) {
			t.Errorf("WiX file runs %q", unwanted)
		}
	}
}
