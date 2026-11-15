// Package keys canonicalizes path keys and decides whether two keys overlap.
//
// A key is an absolute path in canonical form. Every relation between keys goes through the
// functions in this package, so overlap, coverage, and later containment checks never disagree.
package keys

// Key is one canonical path key with the sensitivity of the volume it lives on.
//
// The sensitivity travels with the key so every reader compares a stored key the same way, without
// resolving the path again.
type Key struct {
	Path string `json:"path"`

	// FoldCase means the volume ignores case, so comparisons fold it.
	FoldCase bool `json:"fold_case,omitempty"`

	// Normalize means the volume ignores Unicode normalization form, so comparisons normalize.
	Normalize bool `json:"normalize,omitempty"`
}

// Paths returns the path of each key, in order.
func Paths(ks []Key) []string {
	out := make([]string, len(ks))
	for i, k := range ks {
		out[i] = k.Path
	}

	return out
}
