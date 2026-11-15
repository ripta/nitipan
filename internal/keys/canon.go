package keys

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Errors returned while canonicalizing a domain.
var (
	ErrNotAbsolute     = errors.New("checkout must be an absolute path")
	ErrEmptyScope      = errors.New("scope is empty")
	ErrScopeAbsolute   = errors.New("scope must be relative to the checkout")
	ErrScopeParent     = errors.New("scope must not contain a .. component")
	ErrOutsideCheckout = errors.New("scope resolves outside the checkout")
)

// Sensitivity describes how a volume compares file names.
type Sensitivity struct {
	FoldCase  bool
	Normalize bool
}

// Volumes answers the questions about a volume that canonicalization cannot get from the path.
type Volumes interface {
	// Sensitivity reports how the volume holding an existing, fully resolved path compares names.
	Sensitivity(resolved string) Sensitivity

	// CanonicalCase returns the path with the case the filesystem stores for each component.
	CanonicalCase(resolved string) (string, error)
}

// Canonicalizer turns checkouts and scopes into keys.
type Canonicalizer struct {
	Volumes Volumes
}

// NewCanonicalizer returns a Canonicalizer that asks the host operating system.
func NewCanonicalizer() Canonicalizer {
	return Canonicalizer{Volumes: SystemVolumes{}}
}

// Domain is the canonical form of a checkout plus its scopes.
type Domain struct {
	Checkout Key
	Keys     []Key
}

// ValidateScope rejects a scope that is absolute or has a `..` component. It resolves nothing.
func ValidateScope(scope string) error {
	if scope == "" || strings.ContainsRune(scope, 0) {
		return fmt.Errorf("scope %q: %w", scope, ErrEmptyScope)
	}

	if strings.HasPrefix(scope, "/") {
		return fmt.Errorf("scope %q: %w", scope, ErrScopeAbsolute)
	}

	for _, part := range strings.Split(scope, "/") {
		if part == ".." {
			return fmt.Errorf("scope %q: %w", scope, ErrScopeParent)
		}
	}

	return nil
}

// Domain canonicalizes a checkout and its scopes. With no scopes the one key is the checkout.
//
// The checkout is cleaned lexically before it is resolved. That is exact for a path built from the
// process's working directory, which the operating system already reports resolved. Scope problems
// are collected and returned together, so a caller fixes them in one pass.
func (c Canonicalizer) Domain(checkout string, scopes []string) (Domain, error) {
	if !filepath.IsAbs(checkout) {
		return Domain{}, fmt.Errorf("checkout %q: %w", checkout, ErrNotAbsolute)
	}

	root, err := c.key(checkout)
	if err != nil {
		return Domain{}, fmt.Errorf("canonicalizing checkout %q: %w", checkout, err)
	}

	if len(scopes) == 0 {
		return Domain{Checkout: root, Keys: []Key{root}}, nil
	}

	var (
		errs []error
		out  []Key
	)

	for _, scope := range scopes {
		if err := ValidateScope(scope); err != nil {
			errs = append(errs, err)

			continue
		}

		key, err := c.key(filepath.Join(root.Path, scope))
		if err != nil {
			errs = append(errs, fmt.Errorf("canonicalizing scope %q: %w", scope, err))

			continue
		}

		if !Contains(root, key, nil) {
			errs = append(errs, fmt.Errorf("scope %q resolves to %s: %w", scope, key.Path, ErrOutsideCheckout))

			continue
		}

		if !containsKey(out, key) {
			out = append(out, key)
		}
	}

	if len(errs) > 0 {
		return Domain{}, errors.Join(errs...)
	}

	return Domain{Checkout: root, Keys: out}, nil
}

func containsKey(keys []Key, key Key) bool {
	for _, k := range keys {
		if Equal(k, key) {
			return true
		}
	}

	return false
}

// key canonicalizes one absolute path. The longest existing ancestor is resolved and takes the
// filesystem's case. The rest is appended as written.
func (c Canonicalizer) key(abs string) (Key, error) {
	abs = filepath.Clean(abs)

	existing, tail, err := splitExisting(abs)
	if err != nil {
		return Key{}, err
	}

	resolved, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return Key{}, fmt.Errorf("resolving %s: %w", existing, err)
	}

	sens := c.Volumes.Sensitivity(resolved)

	if sens.FoldCase {
		canonical, err := c.Volumes.CanonicalCase(resolved)
		if err != nil {
			return Key{}, fmt.Errorf("reading canonical case of %s: %w", resolved, err)
		}

		resolved = canonical
	}

	path := resolved
	if len(tail) > 0 {
		path = filepath.Join(resolved, filepath.Join(tail...))
	}

	return Key{Path: path, FoldCase: sens.FoldCase, Normalize: sens.Normalize}, nil
}

// splitExisting splits a clean absolute path into its longest existing prefix and the components
// that do not exist yet. A symlink counts as existing, even a dangling one, so resolving it fails
// loudly instead of keying a name the link would later change.
func splitExisting(abs string) (existing string, tail []string, err error) {
	cur := abs

	for {
		_, statErr := os.Lstat(cur)
		if statErr == nil {
			slices.Reverse(tail)

			return cur, tail, nil
		}

		if !errors.Is(statErr, os.ErrNotExist) && !isNotDir(statErr) {
			return "", nil, fmt.Errorf("checking %s: %w", cur, statErr)
		}

		parent := filepath.Dir(cur)
		if parent == cur {
			return "", nil, fmt.Errorf("checking %s: %w", cur, statErr)
		}

		tail = append(tail, filepath.Base(cur))
		cur = parent
	}
}
