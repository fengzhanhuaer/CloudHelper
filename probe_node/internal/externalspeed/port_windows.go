package externalspeed

import "golang.org/x/sys/windows"

func reservePort() (uint16, func(), error) {
	fd, err := windows.Socket(windows.AF_INET, windows.SOCK_STREAM, windows.IPPROTO_TCP)
	if err != nil {
		return 0, nil, err
	}
	closeFD := func() { _ = windows.Closesocket(fd) }
	if err = windows.Bind(fd, &windows.SockaddrInet4{}); err != nil {
		closeFD()
		return 0, nil, err
	}
	addr, err := windows.Getsockname(fd)
	if err != nil {
		closeFD()
		return 0, nil, err
	}
	return uint16(addr.(*windows.SockaddrInet4).Port), closeFD, nil
}
