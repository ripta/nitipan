package handoff

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/space-armada/nitipan/internal/keys"
	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/repoid"
	"github.com/space-armada/nitipan/internal/rootset"
	"github.com/space-armada/nitipan/internal/state"
	"github.com/space-armada/nitipan/internal/wire"
)

// DerivedDir holds the repo entries of machine leases, one file per lease ID. A V1 binary never scans
// it, so nothing the broker needs lands in a directory V1 reads.
const DerivedDir = "broker/derived"

// Who derived a sidecar. Only a CLI that writes the sidecar can also deliver a notice, so a lease whose
// sidecar the node wrote gets none.
const (
	DerivedByCLI  = "cli"
	DerivedByNode = "node"
)

// DerivedVersion is the version of the derived-entries sidecar this binary writes.
var DerivedVersion = record.Version{Major: 1, Minor: 0}

// DerivedRetry is how long the node waits before it derives again a lease whose derivation it failed.
const DerivedRetry = time.Hour

// Derived is the node's record of a machine lease's repo entries. It stores the keys it was derived
// from, so a lease rewritten to other paths reads as having no usable sidecar.
type Derived struct {
	Version record.Version `json:"version"`
	ID      string         `json:"id"`
	By      string         `json:"by"`

	State      string        `json:"state"`
	Derivation int           `json:"derivation"`
	Keys       []keys.Key    `json:"keys"`
	Entries    []repoid.Name `json:"entries,omitempty"`
	Reason     string        `json:"reason,omitempty"`
	At         time.Time     `json:"at"`
}

// Derive runs the root-set derivation for every key of a machine lease and builds its sidecar. A key
// outside any checkout names no repo, so it adds nothing. Any other refusal, such as a shallow clone or a
// timeout, makes the whole derivation failed, which discovery reads as unknown.
func Derive(d *rootset.Deriver, id, by string, ks []keys.Key, now time.Time) Derived {
	out := Derived{ID: id, By: by, State: wire.DerivedOK, Derivation: rootset.Version, Keys: ks, At: now.UTC()}

	var results []rootset.Result

	for _, k := range ks {
		r, err := d.Derive(k)

		switch {
		case errors.Is(err, rootset.ErrNotRepo):
			continue
		case err != nil:
			out.State, out.Reason = wire.DerivedFailed, err.Error()

			return out
		}

		results = append(results, r)
	}

	out.Entries = rootset.Union(results...)

	return out
}

// Wire is the form a mirror carries.
func (d Derived) Wire() *wire.Derived {
	return &wire.Derived{State: d.State, Derivation: d.Derivation, Entries: d.Entries, Reason: d.Reason}
}

// Matches reports whether the sidecar was derived from exactly these keys.
func (d Derived) Matches(ks []keys.Key) bool {
	if len(d.Keys) != len(ks) {
		return false
	}

	for _, k := range ks {
		found := false

		for _, o := range d.Keys {
			if keys.Equal(k, o) {
				found = true

				break
			}
		}

		if !found {
			return false
		}
	}

	return true
}

// WriteDerived writes a derived-entries sidecar under the state lock.
func WriteDerived(l *state.Locked, d Derived) error {
	d.Version = DerivedVersion

	data, err := json.Marshal(d)
	if err != nil {
		return fmt.Errorf("encoding the derived entries for %s: %w", d.ID, err)
	}

	return l.ReplaceNamed(DerivedDir, d.ID+fileSuffix, append(data, '\n'))
}

// RemoveDerived deletes a derived-entries sidecar under the state lock.
func RemoveDerived(l *state.Locked, id string) error {
	err := l.RemoveNamed(DerivedDir, id+fileSuffix)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("removing the derived entries for %s: %w", id, err)
	}

	return nil
}

// ReadDerived reads one sidecar. A missing one matches fs.ErrNotExist. One this binary cannot use
// matches ErrUnusableFile, and the lease it describes reads as unknown.
func ReadDerived(root, id string) (Derived, error) {
	data, err := state.ReadName(root, DerivedDir, id+fileSuffix)
	if err != nil {
		return Derived{}, err
	}

	var d Derived
	if err := decodeVersioned(data, &d); err != nil {
		return Derived{}, fmt.Errorf("derived entries %s: %w", id, err)
	}

	return d, nil
}

// DerivedIDs lists the lease IDs that have a derived-entries file.
func DerivedIDs(root string) ([]string, error) {
	names, err := state.ListNames(root, DerivedDir)
	if err != nil {
		return nil, err
	}

	var ids []string

	for _, name := range names {
		if id, ok := strings.CutSuffix(name, fileSuffix); ok {
			ids = append(ids, id)
		}
	}

	return ids, nil
}
