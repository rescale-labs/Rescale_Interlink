//go:build !windows

package wailsapp

import "testing"

// The app saves the workspace folder settings and reads them back.
func TestDaemonConfigWorkspaceFolderSettingsRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	a := &App{}
	if err := a.SaveDaemonConfig(DaemonConfigDTO{
		DownloadFolder: t.TempDir(), PollIntervalMinutes: 5, MaxConcurrent: 5, LookbackDays: 7,
		IncludeWorkspaceFolders: true, FlattenFolderStructure: true,
	}); err != nil {
		t.Fatalf("SaveDaemonConfig: %v", err)
	}
	if got := a.GetDaemonConfig(); !got.IncludeWorkspaceFolders || !got.FlattenFolderStructure {
		t.Errorf("GetDaemonConfig = %+v, want both workspace folder settings on", got)
	}
}
