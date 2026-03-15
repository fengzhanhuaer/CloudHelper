//go:build !windows

package main

import "errors"

type systemProxySnapshot struct{}

func captureSystemProxySnapshot() (systemProxySnapshot, error) {
	return systemProxySnapshot{}, errors.New("automatic system proxy update is only supported on windows")
}

func applySocks5SystemProxy(_ string) error {
	return errors.New("automatic system proxy update is only supported on windows")
}

func restoreSystemProxy(_ systemProxySnapshot) error {
	return errors.New("automatic system proxy update is only supported on windows")
}
