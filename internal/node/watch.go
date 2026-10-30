package node

import (
	"context"
	"time"
)

// WatchFunc reports changes to a directory on the returned channel until ctx ends. A report carries
// no detail. The node re-reads the whole directory. A directory that does not exist yet is watched
// from when it appears.
type WatchFunc func(ctx context.Context, dir string) (<-chan struct{}, error)

// pollEvery is how often the polling watcher and the kqueue watcher check for a missing directory
// or a cancelled context.
const pollEvery = 500 * time.Millisecond

func notify(out chan<- struct{}) {
	select {
	case out <- struct{}{}:
	default:
	}
}
