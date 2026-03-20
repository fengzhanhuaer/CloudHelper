//go:build !windows

package backend

import "errors"

func createConfiguredTUNAdapter(_, _, _ string) (uintptr, error) {
	return 0, errors.New("automatic tun adapter creation is only supported on windows")
}

func closeConfiguredTUNAdapter(_ string, _ uintptr) error {
	return nil
}
