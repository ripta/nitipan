package fenceref

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/space-armada/nitipan/internal/gitfence"
)

// identity is the author and committer of every fence commit. It names no person, so nothing
// identifying reaches a remote.
const (
	identityName  = "nitipan"
	identityEmail = "nitipan@invalid"
)

// Store reads and writes fence objects in one clone.
type Store struct {
	// Dir is the clone's working directory. Empty uses the process's.
	Dir string

	// Clean drops the variables that point git at another repository. A command sets it. A hook does
	// not, since the hook's own environment names the repository.
	Clean bool

	// Now stamps the fence commits.
	Now func() time.Time
}

func (s Store) plain() gitfence.Runner { return gitfence.ExecWith(s.Clean) }

func (s Store) committing() gitfence.Runner {
	when := s.Now().UTC()
	stamp := strconv.FormatInt(when.Unix(), 10) + " +0000"

	return gitfence.ExecWith(s.Clean,
		"GIT_AUTHOR_NAME="+identityName, "GIT_AUTHOR_EMAIL="+identityEmail, "GIT_AUTHOR_DATE="+stamp,
		"GIT_COMMITTER_NAME="+identityName, "GIT_COMMITTER_EMAIL="+identityEmail, "GIT_COMMITTER_DATE="+stamp,
	)
}

// Read returns the object of the fence commit sha, checked against the ref it was found under. The
// error wraps ErrHigherMajor, ErrUnusable, or a git failure.
func (s Store) Read(sha string, ref Ref) (Object, error) {
	data, err := s.plain()(s.Dir, nil, "cat-file", "blob", sha+":"+FileName)
	if err != nil {
		return Object{}, fmt.Errorf("%w: reading %s of %s: %w", ErrUnusable, FileName, sha, err)
	}

	return Decode(data, ref)
}

// Create writes a fence commit that carries the object and has the given parent. An empty parent
// makes a root commit, as the first fence of a key does.
func (s Store) Create(obj Object, parent string) (string, error) {
	data, err := obj.Marshal()
	if err != nil {
		return "", err
	}

	blob, err := s.plain()(s.Dir, data, "hash-object", "-w", "--stdin")
	if err != nil {
		return "", fmt.Errorf("writing the fence object: %w", err)
	}

	tree, err := s.plain()(s.Dir, []byte("100644 blob "+strings.TrimSpace(string(blob))+"\t"+FileName+"\n"), "mktree")
	if err != nil {
		return "", fmt.Errorf("writing the fence tree: %w", err)
	}

	return s.commit(strings.TrimSpace(string(tree)), parent)
}

// Refresh writes a new commit over the same tree as prior, with prior as its parent. It changes only
// the parent and the commit time, so a binary never drops a field it does not know.
func (s Store) Refresh(prior string) (string, error) {
	tree, err := s.plain()(s.Dir, nil, "rev-parse", "--verify", prior+"^{tree}")
	if err != nil {
		return "", fmt.Errorf("reading the tree of the fence commit %s: %w", prior, err)
	}

	return s.commit(strings.TrimSpace(string(tree)), prior)
}

func (s Store) commit(tree, parent string) (string, error) {
	args := []string{"commit-tree", tree, "-m", "nitipan fence"}
	if parent != "" {
		args = append(args, "-p", parent)
	}

	out, err := s.committing()(s.Dir, nil, args...)
	if err != nil {
		return "", fmt.Errorf("writing the fence commit: %w", err)
	}

	return strings.TrimSpace(string(out)), nil
}
