//go:build !darwin

package node

import (
	"context"
	"os"
	"strings"
	"time"
)

// SystemWatch polls the directory, since kqueue is macOS only. It compares a signature of the
// names, sizes, and modification times.
func SystemWatch(ctx context.Context, dir string) (<-chan struct{}, error) {
	out := make(chan struct{}, 1)

	go func() {
		defer close(out)

		last := "\x00"
		t := time.NewTicker(pollEvery)

		defer t.Stop()

		for {
			sig := dirSignature(dir)
			if sig != last {
				last = sig

				notify(out)
			}

			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()

	return out, nil
}

func dirSignature(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}

	var b strings.Builder

	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}

		b.WriteString(e.Name())
		b.WriteString(info.ModTime().String())
		b.WriteString(string(rune(info.Size())))
	}

	return b.String()
}
