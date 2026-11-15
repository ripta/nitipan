// Package peercred reads who is on the other end of a unix socket, from the kernel.
//
// The kernel fills these in when the peer connects, so a process cannot claim another identity. A peer
// that connected and then handed its socket to another process still reads as the first process.
package peercred

import (
	"errors"
	"fmt"
	"net"
)

// ErrUnsupported means this platform has no way to read a socket peer.
var ErrUnsupported = errors.New("reading a socket peer is not supported on this platform")

// Peer is what the kernel reports about the other end of a unix socket.
type Peer struct {
	UID int
	PID int
}

// Of returns the peer of a unix connection.
func Of(conn net.Conn) (Peer, error) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return Peer{}, fmt.Errorf("%T is not a unix connection", conn)
	}

	raw, err := uc.SyscallConn()
	if err != nil {
		return Peer{}, fmt.Errorf("reading the socket descriptor: %w", err)
	}

	var (
		peer    Peer
		readErr error
	)

	if err := raw.Control(func(fd uintptr) { peer, readErr = read(int(fd)) }); err != nil {
		return Peer{}, fmt.Errorf("reading the socket descriptor: %w", err)
	}

	if readErr != nil {
		return Peer{}, fmt.Errorf("reading the socket peer: %w", readErr)
	}

	return peer, nil
}
