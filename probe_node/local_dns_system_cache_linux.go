//go:build linux

package main

import (
	"errors"
	"os/exec"
)

func flushProbeLocalSystemDNSCache() error {
	commands := []struct {
		name string
		args []string
	}{
		{name: "resolvectl", args: []string{"flush-caches"}},
		{name: "systemd-resolve", args: []string{"--flush-caches"}},
		{name: "nscd", args: []string{"-i", "hosts"}},
	}

	tried := false
	succeeded := false
	var allErr error
	for _, command := range commands {
		if _, err := exec.LookPath(command.name); err != nil {
			continue
		}
		tried = true
		if _, err := runProbeLocalCommand(probeLocalSystemDNSCacheFlushTimeout, command.name, command.args...); err != nil {
			allErr = errors.Join(allErr, err)
			continue
		}
		succeeded = true
	}
	if !tried || succeeded {
		return nil
	}
	return allErr
}
