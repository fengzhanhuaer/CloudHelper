//go:build windows

package main

func collectDiskPlatform(path string) disk {
	return disk{Path: path}
}
