//go:build windows

package backend

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	tunSessionRingCapacity    = 0x400000
	tunReadWaitTimeoutMillis  = 250
	tunReadLoopSleepOnNoEvent = 50 * time.Millisecond
)

type localTUNDataPlaneRunner struct {
	sessionHandle uintptr
	readWaitEvent windows.Handle

	endSessionProc           *syscall.LazyProc
	receivePacketProc        *syscall.LazyProc
	releaseReceivePacketProc *syscall.LazyProc
	allocateSendPacketProc   *syscall.LazyProc
	sendPacketProc           *syscall.LazyProc

	onPacket func([]byte)
	logf     func(string, ...any)

	stopCh    chan struct{}
	doneCh    chan struct{}
	closeOnce sync.Once

	running   atomic.Bool
	rxPackets atomic.Uint64
	rxBytes   atomic.Uint64
}

func newLocalTUNDataPlaneRunner(libraryPath string, adapterHandle uintptr, onPacket func([]byte), logf func(string, ...any)) (localTUNDataPlane, error) {
	path := strings.TrimSpace(libraryPath)
	if path == "" {
		return nil, errors.New("empty wintun.dll path")
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	if adapterHandle == 0 {
		return nil, errors.New("empty tun adapter handle")
	}

	wintunDLL := syscall.NewLazyDLL(path)
	startSessionProc := wintunDLL.NewProc("WintunStartSession")
	endSessionProc := wintunDLL.NewProc("WintunEndSession")
	getReadWaitEventProc := wintunDLL.NewProc("WintunGetReadWaitEvent")
	receivePacketProc := wintunDLL.NewProc("WintunReceivePacket")
	releaseReceivePacketProc := wintunDLL.NewProc("WintunReleaseReceivePacket")
	allocateSendPacketProc := wintunDLL.NewProc("WintunAllocateSendPacket")
	sendPacketProc := wintunDLL.NewProc("WintunSendPacket")
	if err := wintunDLL.Load(); err != nil {
		return nil, fmt.Errorf("failed to load wintun.dll: %w", err)
	}

	sessionHandle, _, callErr := startSessionProc.Call(adapterHandle, uintptr(tunSessionRingCapacity))
	if sessionHandle == 0 {
		if callErr != nil && !isZeroErrno(callErr) {
			return nil, fmt.Errorf("WintunStartSession failed: %w", callErr)
		}
		return nil, errors.New("WintunStartSession returned empty session")
	}

	readWaitHandle, _, waitErr := getReadWaitEventProc.Call(sessionHandle)
	if readWaitHandle == 0 {
		_, _, _ = endSessionProc.Call(sessionHandle)
		if waitErr != nil && !isZeroErrno(waitErr) {
			return nil, fmt.Errorf("WintunGetReadWaitEvent failed: %w", waitErr)
		}
		return nil, errors.New("WintunGetReadWaitEvent returned empty handle")
	}

	runner := &localTUNDataPlaneRunner{
		sessionHandle:            sessionHandle,
		readWaitEvent:            windows.Handle(readWaitHandle),
		endSessionProc:           endSessionProc,
		receivePacketProc:        receivePacketProc,
		releaseReceivePacketProc: releaseReceivePacketProc,
		allocateSendPacketProc:   allocateSendPacketProc,
		sendPacketProc:           sendPacketProc,
		onPacket:                 onPacket,
		logf:                     logf,
		stopCh:                   make(chan struct{}),
		doneCh:                   make(chan struct{}),
	}
	runner.running.Store(true)
	go runner.readLoop()

	if runner.logf != nil {
		runner.logf("local tun session started: adapter_handle=%d session_handle=%d", adapterHandle, sessionHandle)
	}
	return runner, nil
}

func (r *localTUNDataPlaneRunner) readLoop() {
	defer close(r.doneCh)
	for {
		select {
		case <-r.stopCh:
			r.running.Store(false)
			return
		default:
		}

		var packetSize uint32
		packetPtr, _, recvErr := r.receivePacketProc.Call(r.sessionHandle, uintptr(unsafe.Pointer(&packetSize)))
		if packetPtr != 0 {
			packetCopy := make([]byte, int(packetSize))
			copy(packetCopy, unsafe.Slice((*byte)(unsafe.Pointer(packetPtr)), int(packetSize)))
			r.rxPackets.Add(1)
			r.rxBytes.Add(uint64(packetSize))
			_, _, _ = r.releaseReceivePacketProc.Call(r.sessionHandle, packetPtr)
			if r.onPacket != nil && len(packetCopy) > 0 {
				go r.onPacket(packetCopy)
			}
			continue
		}

		if recvErr != nil && !isZeroErrno(recvErr) && !isNoMoreItemsErr(recvErr) {
			if r.logf != nil {
				r.logf("local tun receive packet failed: %v", recvErr)
			}
			time.Sleep(tunReadLoopSleepOnNoEvent)
			continue
		}

		if r.readWaitEvent == 0 {
			time.Sleep(tunReadLoopSleepOnNoEvent)
			continue
		}

		waitResult, waitErr := windows.WaitForSingleObject(r.readWaitEvent, tunReadWaitTimeoutMillis)
		if waitErr != nil && !isZeroErrno(waitErr) {
			if r.logf != nil {
				r.logf("local tun wait event failed: %v", waitErr)
			}
			time.Sleep(tunReadLoopSleepOnNoEvent)
			continue
		}
		if waitResult == uint32(windows.WAIT_TIMEOUT) {
			continue
		}
	}
}

func (r *localTUNDataPlaneRunner) Close() error {
	var closeErr error
	r.closeOnce.Do(func() {
		close(r.stopCh)
		select {
		case <-r.doneCh:
		case <-time.After(2 * time.Second):
		}
		if r.endSessionProc != nil && r.sessionHandle != 0 {
			_, _, callErr := r.endSessionProc.Call(r.sessionHandle)
			if callErr != nil && !isZeroErrno(callErr) {
				closeErr = fmt.Errorf("WintunEndSession failed: %w", callErr)
			}
		}
		r.running.Store(false)
	})
	return closeErr
}

func (r *localTUNDataPlaneRunner) Stats() localTUNDataPlaneStats {
	return localTUNDataPlaneStats{
		Running:   r.running.Load(),
		RXPackets: r.rxPackets.Load(),
		RXBytes:   r.rxBytes.Load(),
	}
}

func (r *localTUNDataPlaneRunner) WritePacket(packet []byte) error {
	if len(packet) == 0 {
		return nil
	}
	if !r.running.Load() {
		return errors.New("tun data plane is not running")
	}
	packetPtr, _, allocErr := r.allocateSendPacketProc.Call(r.sessionHandle, uintptr(len(packet)))
	if packetPtr == 0 {
		if allocErr != nil && !isZeroErrno(allocErr) {
			return fmt.Errorf("WintunAllocateSendPacket failed: %w", allocErr)
		}
		return errors.New("WintunAllocateSendPacket returned empty packet pointer")
	}

	copy(unsafe.Slice((*byte)(unsafe.Pointer(packetPtr)), len(packet)), packet)
	_, _, sendErr := r.sendPacketProc.Call(r.sessionHandle, packetPtr)
	if sendErr != nil && !isZeroErrno(sendErr) {
		return fmt.Errorf("WintunSendPacket failed: %w", sendErr)
	}
	return nil
}

func isZeroErrno(err error) bool {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno == 0
	}
	return false
}

func isNoMoreItemsErr(err error) bool {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	return errno == syscall.Errno(windows.ERROR_NO_MORE_ITEMS)
}
