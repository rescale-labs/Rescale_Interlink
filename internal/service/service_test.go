package service

import (
	"testing"
)

func TestIsWindowsService(t *testing.T) {
	// On non-Windows, this should return false, nil
	// On Windows (not running as service), this should return false, nil
	isService, err := IsWindowsService()
	if isService {
		t.Error("IsWindowsService() should return false when not running as service")
	}
	// Error may or may not be nil depending on platform
	_ = err
}
