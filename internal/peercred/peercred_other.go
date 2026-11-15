//go:build !darwin && !linux

package peercred

func read(int) (Peer, error) { return Peer{}, ErrUnsupported }
