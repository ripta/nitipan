//go:build darwin

package peercred

import "golang.org/x/sys/unix"

func read(fd int) (Peer, error) {
	cred, err := unix.GetsockoptXucred(fd, unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	if err != nil {
		return Peer{}, err
	}

	pid, err := unix.GetsockoptInt(fd, unix.SOL_LOCAL, unix.LOCAL_PEERPID)
	if err != nil {
		return Peer{}, err
	}

	return Peer{UID: int(cred.Uid), PID: pid}, nil
}
