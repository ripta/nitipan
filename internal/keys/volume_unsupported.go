//go:build !darwin && !linux

package keys

// SystemVolumes reads volume facts on platforms nitipan does not support.
type SystemVolumes struct{}

// Sensitivity treats the volume as case and normalization sensitive.
func (SystemVolumes) Sensitivity(string) Sensitivity {
	return Sensitivity{}
}

// CanonicalCase returns the path unchanged.
func (SystemVolumes) CanonicalCase(resolved string) (string, error) {
	return resolved, nil
}
