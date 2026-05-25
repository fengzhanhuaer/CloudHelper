package main

import "path/filepath"

func joinProbeLocalPath(elem ...string) string {
	return filepath.Join(elem...)
}
