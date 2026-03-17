//go:build linux

package platform

import (
	"fmt"
	"log"
	"os/exec"
	"sync"
)

func inhibitSleep(reason string) (func(), error) {
	inhibitPath, err := exec.LookPath("systemd-inhibit")
	if err != nil {
		log.Printf("[PLATFORM] systemd-inhibit not found — sleep prevention unavailable")
		return func() {}, fmt.Errorf("systemd-inhibit not found: %w", err)
	}

	cmd := exec.Command(
		inhibitPath,
		"--what=sleep",
		"--who=Rescale Interlink",
		"--why="+reason,
		"--mode=block",
		"sleep", "infinity",
	)

	if err := cmd.Start(); err != nil {
		return func() {}, fmt.Errorf("systemd-inhibit start failed: %w", err)
	}

	var once sync.Once
	release := func() {
		once.Do(func() {
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait() // reap zombie
			}
		})
	}
	return release, nil
}
