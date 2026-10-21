//go:build darwin && !cgo

package machine

// machineID returns no ID. gethostuuid(2) is reachable only through libSystem, which needs cgo, so
// a build without it compares records by host name.
func machineID() string {
	return ""
}
