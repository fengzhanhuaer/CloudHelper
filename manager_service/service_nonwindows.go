//go:build !windows

package main

func tryRunWindowsService() (bool, error) {
	return false, nil
}
