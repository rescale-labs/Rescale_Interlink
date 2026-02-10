//go:build windows

package wailsapp

import "testing"

func TestMatchesWindowsUsername(t *testing.T) {
	tests := []struct {
		name         string
		ipcUsername  string
		guiUsername  string
		wantMatch    bool
	}{
		{
			name:        "exact match",
			ipcUsername: "Peter Klein",
			guiUsername: "Peter Klein",
			wantMatch:   true,
		},
		{
			name:        "case insensitive match",
			ipcUsername: "peter klein",
			guiUsername: "Peter Klein",
			wantMatch:   true,
		},
		{
			name:        "DOMAIN\\user vs user",
			ipcUsername: "DESKTOP-PC\\Peter Klein",
			guiUsername: "Peter Klein",
			wantMatch:   true,
		},
		{
			name:        "domain\\user case insensitive",
			ipcUsername: "CORP\\pklein",
			guiUsername: "PKlein",
			wantMatch:   true,
		},
		{
			name:        "user@domain UPN format",
			ipcUsername: "pklein@corp.example.com",
			guiUsername: "pklein",
			wantMatch:   true,
		},
		{
			name:        "UPN case insensitive",
			ipcUsername: "PKlein@corp.example.com",
			guiUsername: "pklein",
			wantMatch:   true,
		},
		{
			name:        "completely different usernames",
			ipcUsername: "alice",
			guiUsername: "bob",
			wantMatch:   false,
		},
		{
			name:        "different user in domain",
			ipcUsername: "CORP\\alice",
			guiUsername: "bob",
			wantMatch:   false,
		},
		{
			name:        "empty strings",
			ipcUsername: "",
			guiUsername: "",
			wantMatch:   true, // EqualFold("", "") is true
		},
		{
			name:        "empty ipc username",
			ipcUsername: "",
			guiUsername: "Peter",
			wantMatch:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchesWindowsUsername(tt.ipcUsername, tt.guiUsername)
			if got != tt.wantMatch {
				t.Errorf("matchesWindowsUsername(%q, %q) = %v, want %v",
					tt.ipcUsername, tt.guiUsername, got, tt.wantMatch)
			}
		})
	}
}
