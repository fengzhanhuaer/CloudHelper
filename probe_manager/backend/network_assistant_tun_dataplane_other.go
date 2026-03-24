//go:build !windows

package backend

import "errors"

func newLocalTUNDataPlaneRunner(_ string, _ uintptr, _ func([]byte), _ func(string, ...any)) (localTUNDataPlane, error) {
	return nil, errors.New("local tun data plane is only supported on windows")
}
