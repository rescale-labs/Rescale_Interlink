package storage

import (
	"errors"
	"testing"
)

func TestIsDiskFullError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"unrelated", errors.New("connection reset by peer"), false},

		{"ENOSPC", errors.New("write /data/f.dat: no space left on device"), true},
		{"errno name", errors.New("pwrite: ENOSPC"), true},
		{"windows", errors.New("The disk is out of disk space."), true},
		{"generic", errors.New("target volume is disk full"), true},

		// EDQUOT is spelled "disk quota exceeded" on Linux and "disc quota
		// exceeded" on macOS/BSD. Only the Linux spelling used to be recognized,
		// so a quota-limited macOS home directory reported an unclassified
		// failure instead of a disk-space problem.
		{"EDQUOT linux", errors.New("write /net/home/f.dat: disk quota exceeded"), true},
		{"EDQUOT macos", errors.New("write /net/home/f.dat: disc quota exceeded"), true},
		{"EDQUOT mixed case", errors.New("Disc Quota Exceeded"), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsDiskFullError(tt.err); got != tt.want {
				t.Errorf("IsDiskFullError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
