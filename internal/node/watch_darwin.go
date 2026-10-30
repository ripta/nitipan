//go:build darwin

package node

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// SystemWatch watches a directory with kqueue. A file published into it, removed, or renamed over
// another raises a write event on the directory.
func SystemWatch(ctx context.Context, dir string) (<-chan struct{}, error) {
	out := make(chan struct{}, 1)

	go func() {
		defer close(out)

		for ctx.Err() == nil {
			if !dirExists(dir) {
				sleepCtx(ctx, pollEvery)

				continue
			}

			// A directory that appears raises no event of its own, so report it as a change.
			notify(out)

			if err := watchOnce(ctx, dir, out); err != nil {
				sleepCtx(ctx, pollEvery)
			}
		}
	}()

	return out, nil
}

func dirExists(dir string) bool {
	info, err := os.Stat(dir)

	return err == nil && info.IsDir()
}

func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()

	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// watchOnce watches until the directory is replaced or the context ends.
func watchOnce(ctx context.Context, dir string, out chan<- struct{}) error {
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("opening %s to watch it: %w", dir, err)
	}

	defer unix.Close(fd)

	kq, err := unix.Kqueue()
	if err != nil {
		return fmt.Errorf("creating a kqueue: %w", err)
	}

	defer unix.Close(kq)

	var change unix.Kevent_t

	unix.SetKevent(&change, fd, unix.EVFILT_VNODE, unix.EV_ADD|unix.EV_CLEAR)

	change.Fflags = unix.NOTE_WRITE | unix.NOTE_EXTEND | unix.NOTE_ATTRIB | unix.NOTE_LINK | unix.NOTE_DELETE |
		unix.NOTE_RENAME | unix.NOTE_REVOKE

	if _, err := unix.Kevent(kq, []unix.Kevent_t{change}, nil, nil); err != nil {
		return fmt.Errorf("registering %s with kqueue: %w", dir, err)
	}

	events := make([]unix.Kevent_t, 8)
	timeout := unix.NsecToTimespec(int64(pollEvery))

	for ctx.Err() == nil {
		n, err := unix.Kevent(kq, nil, events, &timeout)
		if errors.Is(err, unix.EINTR) {
			continue
		}

		if err != nil {
			return fmt.Errorf("waiting on kqueue for %s: %w", dir, err)
		}

		for _, ev := range events[:n] {
			notify(out)

			if ev.Fflags&(unix.NOTE_DELETE|unix.NOTE_RENAME|unix.NOTE_REVOKE) != 0 {
				return nil
			}
		}
	}

	return nil
}
