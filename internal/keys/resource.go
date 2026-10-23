package keys

import (
	"errors"
	"fmt"
	"strings"
)

// MaxResourceName is the longest resource name in bytes. Loosening it later breaks no stored name.
const MaxResourceName = 64

// ReservedNamespace is refused at machine reach. Wider-reach names use it, and a machine resource
// with the same spelling would match their name entries.
const ReservedNamespace = "repo"

// ErrResourceName matches every error ValidateResource returns.
var ErrResourceName = errors.New("invalid resource name")

// ValidateResource checks a resource name against the grammar: `<namespace>:<name>`, each part
// holding lowercase ASCII letters, digits, `-`, `_`, and `.`.
//
// A name is never folded. A name that fails only for case gets the lowercase spelling in its error,
// and the caller decides whether to use it.
func ValidateResource(name string) error {
	if name == "" {
		return fmt.Errorf("%w: the name is empty; want <namespace>:<name>, such as cpu:heavy", ErrResourceName)
	}

	if len(name) > MaxResourceName {
		return fmt.Errorf("%w: %q is %d bytes, and the limit is %d", ErrResourceName, name, len(name), MaxResourceName)
	}

	namespace, rest, found := strings.Cut(name, ":")
	if !found || namespace == "" || rest == "" || strings.Contains(rest, ":") {
		return fmt.Errorf("%w: %q must be <namespace>:<name> with exactly one colon and both parts non-empty, "+
			"such as cpu:heavy", ErrResourceName, name)
	}

	if strings.EqualFold(namespace, ReservedNamespace) {
		return fmt.Errorf("%w: the namespace %q is reserved for names that reach beyond this machine", ErrResourceName,
			ReservedNamespace)
	}

	if lower := strings.ToLower(name); lower != name && resourceChars(lower) {
		return fmt.Errorf("%w: %q uses uppercase letters. Names are lowercase and never folded. Did you mean %q?",
			ErrResourceName, name, lower)
	}

	if !resourceChars(name) {
		return fmt.Errorf("%w: %q may hold only lowercase letters, digits, '-', '_', and '.' in each part",
			ErrResourceName, name)
	}

	return nil
}

// resourceChars reports whether every byte of name is allowed, with the one colon the caller counted
// already.
func resourceChars(name string) bool {
	for i := 0; i < len(name); i++ {
		c := name[i]

		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_', c == '.', c == ':':
		default:
			return false
		}
	}

	return true
}
