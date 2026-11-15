//go:build !darwin

package machine

import "os"

// machineIDPath is where systemd and most distributions keep the machine ID.
const machineIDPath = "/etc/machine-id"

// machineID reads /etc/machine-id. A missing or empty file returns no ID.
func machineID() string {
	data, err := os.ReadFile(machineIDPath)
	if err != nil {
		return ""
	}

	return CleanID(string(data))
}
