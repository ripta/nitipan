package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/space-armada/nitipan/internal/gitfence"
)

// fenceNoticeEnv marks the git calls of `nitipan push`. It has printed the notice about the old key itself,
// so the pre-push hook stays quiet on those calls and each push prints one line. A hook run by a plain
// agent `git push` has no mark and prints it.
const fenceNoticeEnv = "NITIPAN_FENCE_NOTICE"

// fenceFallbackEnv names the remote for which `nitipan push` sanctions one fence-less push, after the host
// refused the fence. The pre-push hook then runs its own lease check on that push, as it does under
// fence=false. Only the git call that pushes the fallback carries it.
const fenceFallbackEnv = "NITIPAN_FENCE_FALLBACK"

// fenceMode is what a remote's configuration says about fencing.
type fenceMode int

const (
	// fenceUnset tries the fence, and falls back to a lease check when the host refuses it.
	fenceUnset fenceMode = iota

	// fenceTrue always fences. A host refusal is an error.
	fenceTrue

	// fenceFalse never tries the fence. Every push gets a lease check.
	fenceFalse
)

// fenceSetting is the mode of one remote and where it came from. The pusher and the hook both read it
// here, so they cannot disagree about it.
type fenceSetting struct {
	Remote string
	Mode   fenceMode

	// ViaLegacy is set when fenceFalse comes from the old nitipan.<remote>.atomic key.
	ViaLegacy bool

	// LegacyOrigin is where an old atomic=false sits, whether or not it decided the mode. It is empty when
	// there is none.
	LegacyOrigin string
}

func fenceKey(remote string) string { return "nitipan." + remote + ".fence" }

func legacyKey(remote string) string { return "nitipan." + remote + ".atomic" }

// offCause finishes the sentence "the push gets a lease check because ..." for a remote in fenceFalse.
func (s fenceSetting) offCause() string {
	if s.ViaLegacy {
		return fmt.Sprintf("%s is false, which acts as %s=false", legacyKey(s.Remote), fenceKey(s.Remote))
	}

	return fmt.Sprintf("%s is false", fenceKey(s.Remote))
}

// legacyNotice is the one line that every push affected by the old key prints. It branches on whether the
// old key set the mode. Any explicit fence beside a leftover atomic=false decides the mode, whichever way
// it is set. The notice gives no advice on whether to rename or unset the key. It says to reinstall the hook first, since an older hook binary
// reads only the old key.
func (s fenceSetting) legacyNotice() string {
	where := strings.TrimPrefix(s.LegacyOrigin, "file:")

	if s.ViaLegacy {
		return fmt.Sprintf("nitipan: %s is false in %s. The old key acts as %s=false. After nitipan setup git --install, "+
			"it can be renamed to %s=false or unset", legacyKey(s.Remote), where, fenceKey(s.Remote), fenceKey(s.Remote))
	}

	return fmt.Sprintf("nitipan: %s is false in %s, and %s is set, so %s wins here. An older nitipan hook still obeys the old key. "+
		"After nitipan setup git --install, the old key can be unset", legacyKey(s.Remote), where, fenceKey(s.Remote), fenceKey(s.Remote))
}

// readFenceSetting reads nitipan.<remote>.fence in every scope git reads. With fence unset it reads the
// old atomic key the same way, and atomic=false then means fence=false. A value that is not a boolean is
// an error naming the key and its file, since it is not one the user could have meant. The old key is
// consulted only when fence is unset, so a bad old value beside a set fence is not an error.
//
// git answers the lookup for every name. Only a name that cannot form a config key has no mode.
func readFenceSetting(g *pushGit, remote string) (fenceSetting, error) {
	s := fenceSetting{Remote: remote}

	if remote == "" || strings.ContainsRune(remote, '\n') {
		return s, nil
	}

	fence, err := readBoolKey(g, fenceKey(remote))
	if err != nil {
		return s, err
	}

	legacy, err := readBoolKey(g, legacyKey(remote))

	if fence.Set {
		if err == nil && legacy.Set && !legacy.Value {
			s.LegacyOrigin = legacy.Origin
		}

		s.Mode = fenceTrue
		if !fence.Value {
			s.Mode = fenceFalse
		}

		return s, nil
	}

	if err != nil {
		return s, err
	}

	if legacy.Set && !legacy.Value {
		s.Mode, s.ViaLegacy, s.LegacyOrigin = fenceFalse, true, legacy.Origin
	}

	return s, nil
}

// boolKey is one git config key read as a boolean.
type boolKey struct {
	Set    bool
	Value  bool
	Origin string
}

const badBooleanMessage = "bad boolean config value"

func readBoolKey(g *pushGit, key string) (boolKey, error) {
	out, err := g.run("", nil, "config", "--show-origin", "--bool", "--get", key)
	if err != nil {
		var gitErr *gitfence.GitError

		switch {
		case gitfence.ExitCode(err) == 1:
			return boolKey{}, nil
		case errors.As(err, &gitErr) && strings.Contains(gitErr.Stderr, badBooleanMessage):
			return boolKey{}, invalidBoolError(g, key)
		default:
			return boolKey{}, fmt.Errorf("reading %s: %w", key, err)
		}
	}

	origin, value, found := strings.Cut(strings.TrimSpace(string(out)), "\t")
	if !found {
		return boolKey{}, fmt.Errorf("reading %s: want an origin and a value, got %q", key, strings.TrimSpace(string(out)))
	}

	return boolKey{Set: true, Value: value == "true", Origin: origin}, nil
}

// invalidBoolError names the key and the file of a value that is not a boolean. Git does not name the
// file when it rejects the value, so it is asked again without the boolean conversion.
func invalidBoolError(g *pushGit, key string) error {
	out, err := g.run("", nil, "config", "--show-origin", "--get", key)
	if err != nil {
		return fmt.Errorf("%s is not a boolean. Set it to true or false, or unset it", key)
	}

	origin, value, _ := strings.Cut(strings.TrimSpace(string(out)), "\t")

	return fmt.Errorf("%s is %q in %s, which is not a boolean. Set it to true or false, or unset it",
		key, value, strings.TrimPrefix(origin, "file:"))
}
