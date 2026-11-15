package node

import (
	"errors"
	"os"
	"path/filepath"
	"sort"

	"github.com/space-armada/nitipan/internal/record"
	"github.com/space-armada/nitipan/internal/state"
	"github.com/space-armada/nitipan/internal/wire"
)

// recallsDir is where recall deadlines would be read from. Nothing writes it yet.
const recallsDir = "recalls"

// snapshot reads every lease in the state directory and returns the mirror to send.
//
// A record this binary can use travels byte for byte, so a V1 record is mirrored with no change. One
// it cannot use, such as a record of a higher major, travels as an unknown holder with an all
// footprint, because every local claim refuses it.
func (n *Node) snapshot() (wire.Snapshot, error) {
	dir, err := state.OpenRead(n.opts.StateDir, state.Options{UID: n.opts.UID})
	if err != nil {
		return wire.Snapshot{}, err
	}

	files, err := dir.Scan(state.LeasesDir)
	if err != nil {
		return wire.Snapshot{}, err
	}

	leases := make([]wire.MirroredLease, 0, len(files))

	for _, f := range files {
		leases = append(leases, mirrorOf(f))
	}

	sort.Slice(leases, func(i, j int) bool { return leases[i].ID < leases[j].ID })

	return wire.Snapshot{Version: wire.Current, Leases: n.applyRecalls(leases)}, nil
}

func mirrorOf(f state.File) wire.MirroredLease {
	if f.BadName {
		return unknownLease(f.ID, "the file name is not a lease ID", "")
	}

	rec, err := record.Decode(f.Data)

	var bad *record.UnusableError

	switch {
	case errors.As(err, &bad):
		version := ""
		if bad.Version != (record.Version{}) {
			version = bad.Version.String()
		}

		return unknownLease(f.ID, string(bad.Reason), version)
	case err != nil:
		return unknownLease(f.ID, err.Error(), "")
	case rec.ID != f.ID:
		return unknownLease(f.ID, "the record's ID differs from its file name", "")
	}

	return wire.MirroredLease{ID: f.ID, Record: append([]byte(nil), f.Data...)}
}

func unknownLease(id, reason, version string) wire.MirroredLease {
	return wire.MirroredLease{ID: id, Unknown: &wire.UnknownLease{Reason: reason, Version: version, Footprint: wire.FootprintAll}}
}

// applyRecalls is the hook for recall deadlines. The recalls directory is read only when it exists,
// and the mirror is not changed yet.
func (n *Node) applyRecalls(leases []wire.MirroredLease) []wire.MirroredLease {
	info, err := os.Stat(filepath.Join(n.opts.StateDir, recallsDir))
	if err != nil || !info.IsDir() {
		return leases
	}

	return leases
}
