//go:build linux

package peercred

import "golang.org/x/sys/unix"

func read(fd int) (Peer, error) {
	cred, err := unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil {
		return Peer{}, err
	}

	return Peer{UID: int(cred.Uid), PID: int(cred.Pid)}, nil
}
