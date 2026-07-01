package installer

import (
	"cmp"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

// TestWixFile checks the WiX configuration's elements, not its comments: it
// installs the GUI, the CLI and the tray, upgrades in place, ends the user's
// own Interlink processes before the in-use check whenever files go or are
// replaced, removes an earlier version's service on uninstall, starts the tray
// at logon by a quoted path, launches nothing itself, and neither installs nor
// starts a service. The script that builds the MSI must be there.
func TestWixFile(t *testing.T) {
	if _, err := os.Stat("build-installer.ps1"); err != nil { // go test runs in this directory
		t.Errorf("the installer build script: %v", err)
	}
	f, err := os.Open("rescale-interlink.wxs")
	if err != nil {
		t.Fatalf("reading the WiX file: %v", err)
	}
	defer f.Close()

	// The attributes of each element, under "Element" and, for one with an Id,
	// an Action or a Name, "Element#<it>"; and each attribute value.
	found := map[string]map[string]string{}
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
		attrs := map[string]string{}
		for _, a := range el.Attr {
			attrs[a.Name.Local] = a.Value
			values = append(values, a.Value)
		}
		found[el.Name.Local] = attrs
		if key := cmp.Or(attrs["Id"], attrs["Action"], attrs["Name"]); key != "" {
			found[el.Name.Local+"#"+key] = attrs
		}
	}
	for _, want := range []string{
		"Package", "MajorUpgrade", "Feature#MainFeature", "Feature#TrayFeature",
		"ComponentGroup#MainComponents", "ComponentGroup#TrayComponents",
		"File#RescaleIntExe", "File#RescaleIntGuiExe", "File#TrayExe",
		"CustomAction#UninstallService", "CustomAction#StopInterlinkProcesses",
	} {
		if found[want] == nil {
			t.Errorf("WiX file lacks %s", want)
		}
	}
	for _, unwanted := range []string{"ServiceInstall", "ServiceControl", "ServiceConfig", "ComponentGroup#ServiceComponents"} {
		if found[unwanted] != nil {
			t.Errorf("WiX file has %s: the package installs no service", unwanted)
		}
	}
	for _, unwanted := range []string{"CustomAction#LaunchTray", "CustomAction#StopDaemon"} {
		if found[unwanted] != nil {
			t.Errorf("WiX file has %s", unwanted)
		}
	}

	// The in-use check is InstallValidate's, so the processes must be gone
	// before it whenever installed files are removed or replaced (uninstall,
	// a feature removed, repair, upgrade), and only the installing user's:
	// other signed-in users keep theirs.
	if kill := found["Custom#StopInterlinkProcesses"]; kill["Before"] != "InstallValidate" ||
		kill["Condition"] != "REMOVE OR REINSTALL OR WIX_UPGRADE_DETECTED" {
		t.Errorf("StopInterlinkProcesses is scheduled %v, want before InstallValidate on removal, repair and upgrade", kill)
	}
	const kill = `[SystemFolder]taskkill.exe /F /FI "USERNAME eq [LogonUser]" /IM rescale-int-gui.exe /IM rescale-int-tray.exe /IM rescale-int.exe`
	if cmd := found["CustomAction#StopInterlinkProcesses"]["ExeCommand"]; cmd != kill {
		t.Errorf("StopInterlinkProcesses runs %q, want %q: the installing user's Interlink processes only", cmd, kill)
	}
	if run := found["RegistryValue#RescaleInterlinkTray"]["Value"]; run != `"[INSTALLFOLDER]rescale-int-tray.exe"` {
		t.Errorf("the tray starts at logon as %q, want the path quoted", run)
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
