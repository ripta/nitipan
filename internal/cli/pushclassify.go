package cli

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/space-armada/nitipan/internal/fenceref"
)

const (
	// noAtomicMessage is git's own client-side message when the remote did not advertise atomic push. It is
	// the one stderr line the classifier reads.
	noAtomicMessage = "does not support --atomic"

	// remoteRejected is the summary git prints for a ref that the remote refused.
	remoteRejected = "[remote rejected]"
)

// collateralReasons are the reasons receive-pack gives a ref only because of what happened to the other
// refs of an atomic push. "atomic push failure" blames a ref that another ref's refusal took down.
// "atomic transaction failed" lands on every ref when the transaction fails to commit, whichever ref
// caused it. Neither says the host refused the ref itself.
var collateralReasons = []string{
	"atomic push failure",
	"atomic transaction failed",
}

// fenceRefusedError is a host that refused a fence ref with a reason of its own. It is the host's policy
// about the fence namespace, not a lost lease.
type fenceRefusedError struct {
	Remote string
	Ref    string
	Reason string
}

func (e *fenceRefusedError) Error() string {
	return fmt.Sprintf("%s refused the fence ref %s: %s", e.Remote, e.Ref, e.Reason)
}

// porcelainRef is one ref line of git push --porcelain. The line is a flag, from:to, and a summary,
// separated by tabs. A rejected ref's summary is a bracketed status and the reason in parentheses.
type porcelainRef struct {
	Flag    byte
	From    string
	To      string
	Status  string
	Reason  string
	Summary string
}

type porcelainResult struct {
	Refs []porcelainRef

	// Malformed is set when any line that looks like a ref line does not parse. The classifier then has no
	// positive evidence about any ref.
	Malformed bool
}

// porcelainFlags are the flag characters git push --porcelain puts in front of a ref line.
const porcelainFlags = " +-*=!"

var porcelainStatus = regexp.MustCompile(`^(\[[^\]]+\]) \((.*)\)$`)

// parsePorcelain reads the stdout of git push --porcelain. Lines without a tab are the "To" header and
// the "Done" trailer, and carry no ref.
func parsePorcelain(out string) porcelainResult {
	var res porcelainResult

	for line := range strings.SplitSeq(out, "\n") {
		if !strings.Contains(line, "\t") {
			continue
		}

		ref, ok := parsePorcelainLine(line)
		if !ok {
			res.Malformed = true

			continue
		}

		res.Refs = append(res.Refs, ref)
	}

	return res
}

func parsePorcelainLine(line string) (porcelainRef, bool) {
	fields := strings.Split(line, "\t")
	if len(fields) != 3 || len(fields[0]) != 1 || !strings.Contains(porcelainFlags, fields[0]) {
		return porcelainRef{}, false
	}

	from, to, found := strings.Cut(fields[1], ":")
	if !found || to == "" {
		return porcelainRef{}, false
	}

	ref := porcelainRef{Flag: fields[0][0], From: from, To: to, Summary: fields[2]}

	if ref.Flag == '!' {
		m := porcelainStatus.FindStringSubmatch(fields[2])
		if m == nil {
			return porcelainRef{}, false
		}

		ref.Status, ref.Reason = m[1], m[2]
	}

	return ref, true
}

func isCollateral(reason string) bool {
	for _, c := range collateralReasons {
		if reason == c {
			return true
		}
	}

	return false
}

// pushFailure is what the classifier reads. Moved asks the remote whether a fence ref this push meant
// to update holds another value than the one the push expected. It is nil when the push carried no fence
// ref.
type pushFailure struct {
	Remote string
	Stdout string
	Stderr string
	Moved  func() (bool, error)
}

// classifyPushFailure judges a failed fenced push by the first rule that matches, and returns nil when
// the push failed for any other reason. A nil return fails the push with git's own output and no
// fallback. Only errNoAtomic and a fenceRefusedError say the host refused the fence itself.
func classifyPushFailure(f pushFailure) error {
	if f.Moved != nil {
		moved, err := f.Moved()
		if err != nil {
			return fmt.Errorf("git push failed, and %s could not be asked whether a fence ref moved: %w", f.Remote, err)
		}

		if moved {
			return errLostCAS
		}
	}

	if strings.Contains(f.Stderr, noAtomicMessage) {
		return errNoAtomic
	}

	res := parsePorcelain(f.Stdout)
	if res.Malformed {
		return nil
	}

	for _, ref := range res.Refs {
		if !strings.HasPrefix(ref.To, fenceref.Namespace) || ref.Flag != '!' || ref.Status != remoteRejected || isCollateral(ref.Reason) {
			continue
		}

		return &fenceRefusedError{Remote: f.Remote, Ref: ref.To, Reason: ref.Reason}
	}

	return nil
}
