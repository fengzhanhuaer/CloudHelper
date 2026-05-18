//go:build windows

package main

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

func restartCurrentProcess(executablePath string) error {
	exePath := strings.TrimSpace(executablePath)
	if exePath == "" {
		var err error
		exePath, err = os.Executable()
		if err != nil {
			return err
		}
	}
	if resolved, err := filepath.EvalSymlinks(exePath); err == nil && resolved != "" {
		exePath = resolved
	}
	if probeProcessStartedByWinSW() {
		log.Printf("probe restart delegated to WinSW supervisor: exe=%s", exePath)
		os.Exit(1)
		return nil
	}
	cmd := exec.Command(exePath, os.Args[1:]...)
	hideWindowSysProcAttr(cmd)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Start(); err != nil {
		return err
	}
	os.Exit(0)
	return nil
}

func probeProcessStartedByWinSW() bool {
	parentPID, err := currentParentProcessID()
	if err != nil || parentPID == 0 {
		return false
	}
	parentName, err := processExecutableBaseName(parentPID)
	if err != nil {
		return false
	}
	parentName = strings.ToLower(strings.TrimSpace(parentName))
	return parentName == "probe_node-service.exe" || strings.Contains(parentName, "winsw")
}

func currentParentProcessID() (uint32, error) {
	currentPID := uint32(os.Getpid())
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(snapshot)

	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := windows.Process32First(snapshot, &entry); err != nil {
		return 0, err
	}
	for {
		if entry.ProcessID == currentPID {
			return entry.ParentProcessID, nil
		}
		if err := windows.Process32Next(snapshot, &entry); err != nil {
			return 0, err
		}
	}
}

func processExecutableBaseName(pid uint32) (string, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(snapshot)

	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := windows.Process32First(snapshot, &entry); err != nil {
		return "", err
	}
	for {
		if entry.ProcessID == pid {
			return windows.UTF16ToString(entry.ExeFile[:]), nil
		}
		if err := windows.Process32Next(snapshot, &entry); err != nil {
			return "", err
		}
	}
}
