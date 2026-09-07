//go:build windows

package state

import (
	"fmt"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// machineGUIDKey and machineGUIDValue name the identifier Windows writes once,
// when it is installed. Windows numbers processes machine-wide and has no PID
// namespaces, so the machine is the whole domain.
//
// The 64-bit view is asked for explicitly: this key is one of the ones the
// registry redirects for a 32-bit process, and a 32-bit build reading its own
// view would answer to a different domain from a 64-bit one on the same
// machine.
const (
	machineGUIDKey   = `SOFTWARE\Microsoft\Cryptography`
	machineGUIDValue = "MachineGuid"
)

func readPIDDomain() (string, error) {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, machineGUIDKey, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return "", fmt.Errorf(`cannot open HKLM\%s: %w`, machineGUIDKey, err)
	}
	defer key.Close()

	guid, _, err := key.GetStringValue(machineGUIDValue)
	if err != nil {
		return "", fmt.Errorf(`cannot read HKLM\%s\%s: %w`, machineGUIDKey, machineGUIDValue, err)
	}
	if guid = strings.TrimSpace(guid); guid == "" {
		return "", fmt.Errorf(`HKLM\%s\%s names no machine`, machineGUIDKey, machineGUIDValue)
	}
	return guid, nil
}
