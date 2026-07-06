//go:build linux

package main

import (
	"os"
	"time"
)

type fakeProbeLocalLinuxFileInfo struct{}

func (fakeProbeLocalLinuxFileInfo) Name() string       { return "tun" }
func (fakeProbeLocalLinuxFileInfo) Size() int64        { return 0 }
func (fakeProbeLocalLinuxFileInfo) Mode() os.FileMode  { return os.ModeCharDevice | 0o600 }
func (fakeProbeLocalLinuxFileInfo) ModTime() time.Time { return time.Time{} }
func (fakeProbeLocalLinuxFileInfo) IsDir() bool        { return false }
func (fakeProbeLocalLinuxFileInfo) Sys() any           { return nil }
