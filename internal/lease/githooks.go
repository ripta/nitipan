package lease

import (
	"fmt"
	"strings"

	"github.com/space-armada/nitipan/internal/keys"
)

// GitHooks says whether a path lies in a git checkout, and whether nitipan's git hooks are in force
// there.
type GitHooks struct {
	// Checkout is the top of the git checkout that holds the path. It is empty for a path outside
	// any checkout.
	Checkout string

	// InForce is true when git runs nitipan's hook in that checkout.
	InForce bool

	// Why says what is missing when the hooks are not in force.
	Why string
}

// hooksFor reports the hooks state of each distinct checkout the keys lie in. A path outside any
// checkout is reported once, with an empty Checkout. Without a GitHooks function there is nothing
// to report.
func (e *Engine) hooksFor(held []keys.Key) ([]GitHooks, error) {
	if e.GitHooks == nil {
		return nil, nil
	}

	var (
		out  []GitHooks
		seen = map[string]bool{}
	)

	for _, k := range held {
		st, err := e.GitHooks(k.Path)
		if err != nil {
			return out, fmt.Errorf("checking the git hooks for %s: %w", k.Path, err)
		}

		if seen[st.Checkout] {
			continue
		}

		seen[st.Checkout] = true
		out = append(out, st)
	}

	return out, nil
}

// hookCheck is what the hooks check found for a path claim, before the lock was taken.
type hookCheck struct {
	states []GitHooks
	err    error
}

// hooksBeforeLock asks about the claim's checkouts. A resource claim has none, and a claim whose
// domain does not canonicalize is refused later for that reason.
func (e *Engine) hooksBeforeLock(req AcquireRequest) hookCheck {
	if req.Resource != "" || e.GitHooks == nil {
		return hookCheck{}
	}

	domain, err := e.Canon.Domain(req.Domain.Checkout, req.Domain.Scopes)
	if err != nil {
		return hookCheck{}
	}

	states, err := e.hooksFor(domain.Keys)

	return hookCheck{states: states, err: err}
}

// verdict refuses a path claim inside a git checkout whose hooks are not in force. The ref update
// that the hooks refuse is the only thing that fences a lease against a holder that lost it, so a
// claim without them would promise protection nothing gives. A path outside any checkout is not
// affected. A failure to ask git refuses the claim and says what failed.
func (h hookCheck) verdict() error {
	if h.err != nil {
		return h.err
	}

	var missing []string

	for _, st := range h.states {
		if st.Checkout != "" && !st.InForce {
			missing = append(missing, fmt.Sprintf("%s (%s). Run: nitipan setup git --install %s", st.Checkout, st.Why,
				shellQuote(st.Checkout)))
		}
	}

	if len(missing) == 0 {
		return nil
	}

	return fmt.Errorf("the claim is inside %s where nitipan's git hooks are not in force, so a lease there would "+
		"not fence a ref update:\n  %s\nThen claim again", Plural(len(missing), "a git checkout", "git checkouts"),
		strings.Join(missing, "\n  "))
}
