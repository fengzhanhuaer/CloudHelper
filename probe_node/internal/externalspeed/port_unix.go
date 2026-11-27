//go:build !windows

package externalspeed

import "golang.org/x/sys/unix"

// Bind without listen/connect: reserve a local TCP port without opening an
// inbound service or sending anything outside the selected test path.
func reservePort() (uint16, func(), error) {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_STREAM, 0)
	if err != nil {
		return 0, nil, err
	}
	unix.CloseOnExec(fd)
	closeFD := func() { _ = unix.Close(fd) }
	if err = unix.Bind(fd, &unix.SockaddrInet4{}); err != nil {
		closeFD()
		return 0, nil, err
	}
	addr, err := unix.Getsockname(fd)
	if err != nil {
		closeFD()
		return 0, nil, err
	}
	return uint16(addr.(*unix.SockaddrInet4).Port), closeFD, nil
}
