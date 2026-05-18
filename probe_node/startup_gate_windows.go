//go:build windows

package main

import (
	"errors"
	"sync"

	"golang.org/x/sys/windows"
)

const probeNodeStartupGateName = `Global\CloudHelper.ProbeNode.StartupGate`

var probeNodeStartupGateState = struct {
	mu     sync.Mutex
	handle windows.Handle
}{}

func enterProbeNodeStartupGate() (func(), error) {
	name, err := windows.UTF16PtrFromString(probeNodeStartupGateName)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateMutex(nil, false, name)
	if err != nil {
		return nil, err
	}
	waitRes, waitErr := windows.WaitForSingleObject(handle, windows.INFINITE)
	if waitErr != nil {
		_ = windows.CloseHandle(handle)
		return nil, waitErr
	}
	if waitRes != windows.WAIT_OBJECT_0 && waitRes != windows.WAIT_ABANDONED {
		_ = windows.CloseHandle(handle)
		return nil, errors.New("startup gate wait returned unexpected state")
	}

	probeNodeStartupGateState.mu.Lock()
	probeNodeStartupGateState.handle = handle
	probeNodeStartupGateState.mu.Unlock()

	return func() {
		releaseProbeNodeStartupGate()
	}, nil
}

func releaseProbeNodeStartupGate() {
	probeNodeStartupGateState.mu.Lock()
	handle := probeNodeStartupGateState.handle
	probeNodeStartupGateState.handle = 0
	probeNodeStartupGateState.mu.Unlock()
	if handle == 0 {
		return
	}
	_ = windows.ReleaseMutex(handle)
	_ = windows.CloseHandle(handle)
}
