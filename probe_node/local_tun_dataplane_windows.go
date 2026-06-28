//go:build windows

package main

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
	probeLocalTUNSessionRingCapacity    = 0x400000
	probeLocalTUNReadWaitTimeoutMillis  = 250
	probeLocalTUNReadLoopSleepOnNoEvent = 50 * time.Millisecond
)

var (
	probeLocalEnsureWintunLibraryForDataPlane = ensureProbeEmbeddedWintunLibrary
	probeLocalResolveWintunPathForDataPlane   = resolveProbeWintunPath
	probeLocalCreateWintunAdapterForDataPlane = createProbeLocalWintunAdapter
	probeLocalCloseWintunAdapterForDataPlane  = closeProbeLocalWintunAdapter
	probeLocalNewTUNDataPlaneRunner           = newProbeLocalTUNDataPlaneRunner
	probeLocalTUNInboundPacketHandler         = handleProbeLocalTUNInboundPacket
)

var probeLocalTUNDataPlaneState = struct {
	mu            sync.Mutex
	libraryPath   string
	adapterHandle uintptr
	interfaceLUID uint64
	ifIndex       int
	dataPlane     probeLocalTUNDataPlane
	packetStack   probeLocalTUNPacketStack
}{}

func startProbeLocalTUNDataPlane() error {
	probeLocalTUNDataPlaneState.mu.Lock()
	if probeLocalTUNDataPlaneState.dataPlane != nil {
		dataPlane := probeLocalTUNDataPlaneState.dataPlane
		interfaceLUID := probeLocalTUNDataPlaneState.interfaceLUID
		ifIndex := probeLocalTUNDataPlaneState.ifIndex
		probeLocalTUNDataPlaneState.mu.Unlock()
		stats := dataPlane.Stats()
		healthy := stats.Running && probeLocalTUNDataPlaneRouteTargetHealthy(interfaceLUID, ifIndex)
		if healthy {
			return nil
		}
		logProbeWarnf("probe local tun data plane is stale; restarting session: running=%v if_luid=%d if_index=%d", stats.Running, interfaceLUID, ifIndex)
		if err := stopProbeLocalTUNDataPlane(); err != nil {
			logProbeWarnf("probe local tun stale data plane stop failed before restart: %v", err)
		}
		return startProbeLocalTUNDataPlane()
	}
	probeLocalTUNDataPlaneState.mu.Unlock()

	if err := probeLocalEnsureWintunLibraryForDataPlane(); err != nil {
		clearProbeLocalWindowsDirectBypassRouteTarget()
		return fmt.Errorf("prepare wintun library: %w", err)
	}
	libraryPath, err := probeLocalResolveWintunPathForDataPlane()
	if err != nil {
		return fmt.Errorf("resolve wintun path: %w", err)
	}
	handle, err := probeLocalCreateWintunAdapterForDataPlane(libraryPath, probeLocalTUNAdapterName, probeLocalTUNTunnelType)
	if err != nil {
		clearProbeLocalWindowsDirectBypassRouteTarget()
		return fmt.Errorf("create/open wintun adapter: %w", err)
	}
	routeTarget, err := prepareProbeLocalTUNDataPlaneRouteTarget(libraryPath, handle)
	if err != nil {
		_ = probeLocalCloseWintunAdapterForDataPlane(libraryPath, handle)
		clearProbeLocalWindowsDirectBypassRouteTarget()
		return err
	}
	if err := prepareProbeLocalWindowsDirectBypassRouteTarget(); err != nil {
		_ = probeLocalCloseWintunAdapterForDataPlane(libraryPath, handle)
		clearProbeLocalWindowsDirectBypassRouteTarget()
		return fmt.Errorf("prepare direct bypass route target: %w", err)
	}
	dataPlane, err := probeLocalNewTUNDataPlaneRunner(libraryPath, handle, func(packet []byte) {
		handler := probeLocalTUNInboundPacketHandler
		if handler != nil && len(packet) > 0 {
			handler(packet)
		}
	}, func(format string, args ...any) {
		logProbeInfof(format, args...)
	})
	if err != nil {
		_ = probeLocalCloseWintunAdapterForDataPlane(libraryPath, handle)
		clearProbeLocalWindowsDirectBypassRouteTarget()
		return err
	}

	probeLocalTUNDataPlaneState.mu.Lock()
	if probeLocalTUNDataPlaneState.dataPlane != nil {
		probeLocalTUNDataPlaneState.mu.Unlock()
		_ = dataPlane.Close()
		_ = probeLocalCloseWintunAdapterForDataPlane(libraryPath, handle)
		return nil
	}
	probeLocalTUNDataPlaneState.libraryPath = strings.TrimSpace(libraryPath)
	probeLocalTUNDataPlaneState.adapterHandle = handle
	probeLocalTUNDataPlaneState.interfaceLUID = routeTarget.InterfaceLUID
	probeLocalTUNDataPlaneState.ifIndex = routeTarget.InterfaceIndex
	probeLocalTUNDataPlaneState.dataPlane = dataPlane
	probeLocalTUNDataPlaneState.mu.Unlock()

	if probeLocalVNetFeatureActive() {
		if err := startProbeLocalTUNPacketStack(); err != nil {
			probeLocalTUNDataPlaneState.mu.Lock()
			if probeLocalTUNDataPlaneState.dataPlane == dataPlane {
				probeLocalTUNDataPlaneState.dataPlane = nil
				probeLocalTUNDataPlaneState.adapterHandle = 0
				probeLocalTUNDataPlaneState.libraryPath = ""
				probeLocalTUNDataPlaneState.interfaceLUID = 0
				probeLocalTUNDataPlaneState.ifIndex = 0
			}
			probeLocalTUNDataPlaneState.mu.Unlock()
			_ = dataPlane.Close()
			_ = probeLocalCloseWintunAdapterForDataPlane(libraryPath, handle)
			clearProbeLocalWindowsDirectBypassRouteTarget()
			return err
		}
	}
	ensureProbeVirtualRouterLocalInterfaceIP()

	stats := dataPlane.Stats()
	logProbeInfof("probe local tun data plane started: running=%v rx_packets=%d rx_bytes=%d tx_packets=%d tx_bytes=%d if_index=%d if_luid=%d gateway=%s", stats.Running, stats.RXPackets, stats.RXBytes, stats.TXPackets, stats.TXBytes, routeTarget.InterfaceIndex, routeTarget.InterfaceLUID, strings.TrimSpace(routeTarget.Gateway))
	return nil
}

func prepareProbeLocalTUNDataPlaneRouteTarget(libraryPath string, handle uintptr) (probeLocalWindowsRouteTarget, error) {
	luid, err := probeLocalGetWintunAdapterLUIDFromHandle(libraryPath, handle)
	if err != nil {
		return probeLocalWindowsRouteTarget{}, fmt.Errorf("resolve tun adapter luid from handle: %w", err)
	}
	if err := ensureProbeLocalWindowsRouteTargetByInterfaceLUID(luid); err != nil {
		return probeLocalWindowsRouteTarget{}, fmt.Errorf("prepare tun adapter route target: %w", err)
	}
	routeTarget, err := resolveProbeLocalWindowsRouteTarget()
	if err != nil {
		return probeLocalWindowsRouteTarget{}, fmt.Errorf("resolve prepared tun route target: %w", err)
	}
	if routeTarget.InterfaceLUID == 0 || routeTarget.InterfaceIndex <= 0 {
		return probeLocalWindowsRouteTarget{}, fmt.Errorf("prepared tun route target is incomplete: if_luid=%d if_index=%d", routeTarget.InterfaceLUID, routeTarget.InterfaceIndex)
	}
	if routeTarget.InterfaceLUID != luid {
		logProbeWarnf("probe local tun route target luid changed after prepare: handle_luid=%d env_luid=%d if_index=%d", luid, routeTarget.InterfaceLUID, routeTarget.InterfaceIndex)
	}
	return routeTarget, nil
}

func stopProbeLocalTUNDataPlane() error {
	probeLocalTUNDataPlaneState.mu.Lock()
	libraryPath := strings.TrimSpace(probeLocalTUNDataPlaneState.libraryPath)
	handle := probeLocalTUNDataPlaneState.adapterHandle
	dataPlane := probeLocalTUNDataPlaneState.dataPlane
	probeLocalTUNDataPlaneState.libraryPath = ""
	probeLocalTUNDataPlaneState.adapterHandle = 0
	probeLocalTUNDataPlaneState.interfaceLUID = 0
	probeLocalTUNDataPlaneState.ifIndex = 0
	probeLocalTUNDataPlaneState.dataPlane = nil
	probeLocalTUNDataPlaneState.mu.Unlock()

	defer clearProbeLocalWindowsDirectBypassRouteTarget()
	errStack := stopProbeLocalTUNPacketStack()
	var allErr error
	if dataPlane != nil {
		stats := dataPlane.Stats()
		if err := dataPlane.Close(); err != nil {
			allErr = errors.Join(allErr, err)
		}
		logProbeInfof("probe local tun data plane stopped: rx_packets=%d rx_bytes=%d tx_packets=%d tx_bytes=%d", stats.RXPackets, stats.RXBytes, stats.TXPackets, stats.TXBytes)
	}
	if closeErr := probeLocalCloseWintunAdapterForDataPlane(libraryPath, handle); closeErr != nil {
		allErr = errors.Join(allErr, closeErr)
	}
	allErr = errors.Join(allErr, errStack)
	stopProbeLocalDNSTUNListener()
	return allErr
}

func probeLocalTUNDataPlaneRouteTargetHealthy(interfaceLUID uint64, ifIndex int) bool {
	if interfaceLUID > 0 {
		if err := ensureProbeLocalWindowsRouteTargetByInterfaceLUID(interfaceLUID); err == nil {
			return true
		} else {
			logProbeWarnf("probe local tun data plane route target health check by luid failed: if_luid=%d if_index=%d err=%v", interfaceLUID, ifIndex, err)
		}
	}
	if ifIndex > 0 {
		if err := ensureProbeLocalWindowsRouteTargetByInterfaceIndex(ifIndex); err == nil {
			return true
		} else {
			logProbeWarnf("probe local tun data plane route target health check by ifindex failed: if_luid=%d if_index=%d err=%v", interfaceLUID, ifIndex, err)
		}
	}
	return false
}

func probeLocalTUNDataPlaneStatsSnapshot() probeLocalTUNDataPlaneStats {
	probeLocalTUNDataPlaneState.mu.Lock()
	defer probeLocalTUNDataPlaneState.mu.Unlock()
	if probeLocalTUNDataPlaneState.dataPlane == nil {
		return probeLocalTUNDataPlaneStats{}
	}
	return probeLocalTUNDataPlaneState.dataPlane.Stats()
}

func probeLocalTUNDataPlaneRunning() bool {
	stats := probeLocalTUNDataPlaneStatsSnapshot()
	return stats.Running
}

func writeProbeLocalTUNPacket(packet []byte) error {
	probeLocalTUNDataPlaneState.mu.Lock()
	dataPlane := probeLocalTUNDataPlaneState.dataPlane
	probeLocalTUNDataPlaneState.mu.Unlock()
	if dataPlane == nil {
		return errors.New("probe local tun data plane is not running")
	}
	return dataPlane.WritePacket(packet)
}

func writeProbeLocalTUNInboundPacketToStack(packet []byte) error {
	if len(packet) == 0 {
		return nil
	}
	probeLocalTUNDataPlaneState.mu.Lock()
	packetStack := probeLocalTUNDataPlaneState.packetStack
	probeLocalTUNDataPlaneState.mu.Unlock()
	if packetStack == nil {
		return nil
	}
	_, err := packetStack.Write(packet)
	return err
}

func handleProbeLocalTUNInboundPacket(packet []byte) {
	if handleProbeVirtualRouterTUNPacket(packet) {
		return
	}
	if !probeLocalVNetFeatureActive() {
		return
	}
	if handleProbeLocalTUNICMPDirectBypass(packet) {
		return
	}
	if err := writeProbeLocalTUNInboundPacketToStack(packet); err != nil {
		logProbeWarnf("probe local tun packet stack write failed: %v", err)
	}
}

func resetProbeLocalTUNDataPlaneHooksForTest() {
	probeLocalVNetFeatureEnabled = func() bool { return true }
	probeLocalEnsureWintunLibraryForDataPlane = ensureProbeEmbeddedWintunLibrary
	probeLocalResolveWintunPathForDataPlane = resolveProbeWintunPath
	probeLocalCreateWintunAdapterForDataPlane = createProbeLocalWintunAdapter
	probeLocalCloseWintunAdapterForDataPlane = closeProbeLocalWintunAdapter
	probeLocalNewTUNDataPlaneRunner = newProbeLocalTUNDataPlaneRunner
	probeLocalTUNInboundPacketHandler = handleProbeLocalTUNInboundPacket
	_ = stopProbeLocalTUNDataPlane()
}

type probeLocalTUNDataPlaneRunner struct {
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
	txPackets atomic.Uint64
	txBytes   atomic.Uint64
}

func newProbeLocalTUNDataPlaneRunner(libraryPath string, adapterHandle uintptr, onPacket func([]byte), logf func(string, ...any)) (probeLocalTUNDataPlane, error) {
	path := strings.TrimSpace(libraryPath)
	if path == "" {
		return nil, errors.New("empty wintun.dll path")
	}
	if absPath, err := filepath.Abs(path); err == nil {
		path = absPath
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

	sessionHandle, _, callErr := startSessionProc.Call(adapterHandle, uintptr(probeLocalTUNSessionRingCapacity))
	if sessionHandle == 0 {
		if callErr != nil && !probeLocalTUNIsZeroErrno(callErr) {
			return nil, fmt.Errorf("WintunStartSession failed: %w", callErr)
		}
		return nil, errors.New("WintunStartSession returned empty session")
	}

	readWaitHandle, _, waitErr := getReadWaitEventProc.Call(sessionHandle)
	if readWaitHandle == 0 {
		_, _, _ = endSessionProc.Call(sessionHandle)
		if waitErr != nil && !probeLocalTUNIsZeroErrno(waitErr) {
			return nil, fmt.Errorf("WintunGetReadWaitEvent failed: %w", waitErr)
		}
		return nil, errors.New("WintunGetReadWaitEvent returned empty handle")
	}

	runner := &probeLocalTUNDataPlaneRunner{
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
		runner.logf("probe local tun session started: adapter_handle=%d session_handle=%d", adapterHandle, sessionHandle)
	}
	return runner, nil
}

func (r *probeLocalTUNDataPlaneRunner) readLoop() {
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
			payload := make([]byte, int(packetSize))
			copy(payload, probeLocalTUNUintptrToByteSlice(packetPtr, int(packetSize)))
			r.rxPackets.Add(1)
			r.rxBytes.Add(uint64(packetSize))
			_, _, _ = r.releaseReceivePacketProc.Call(r.sessionHandle, packetPtr)
			r.handleInboundPayload(payload)
			continue
		}

		if recvErr != nil && !probeLocalTUNIsZeroErrno(recvErr) && !probeLocalTUNIsNoMoreItemsErr(recvErr) {
			if r.logf != nil {
				r.logf("probe local tun receive packet failed: %v", recvErr)
			}
			time.Sleep(probeLocalTUNReadLoopSleepOnNoEvent)
			continue
		}

		if r.readWaitEvent == 0 {
			time.Sleep(probeLocalTUNReadLoopSleepOnNoEvent)
			continue
		}
		waitResult, waitErr := windows.WaitForSingleObject(r.readWaitEvent, probeLocalTUNReadWaitTimeoutMillis)
		if waitErr != nil && !probeLocalTUNIsZeroErrno(waitErr) {
			if r.logf != nil {
				r.logf("probe local tun wait event failed: %v", waitErr)
			}
			time.Sleep(probeLocalTUNReadLoopSleepOnNoEvent)
			continue
		}
		if waitResult == uint32(windows.WAIT_TIMEOUT) {
			continue
		}
	}
}

func (r *probeLocalTUNDataPlaneRunner) handleInboundPayload(payload []byte) {
	if len(payload) == 0 || r.onPacket == nil {
		return
	}
	r.onPacket(payload)
}

func (r *probeLocalTUNDataPlaneRunner) Close() error {
	var closeErr error
	r.closeOnce.Do(func() {
		close(r.stopCh)
		select {
		case <-r.doneCh:
		case <-time.After(2 * time.Second):
		}
		if r.endSessionProc != nil && r.sessionHandle != 0 {
			_, _, callErr := r.endSessionProc.Call(r.sessionHandle)
			if callErr != nil && !probeLocalTUNIsZeroErrno(callErr) {
				closeErr = fmt.Errorf("WintunEndSession failed: %w", callErr)
			}
		}
		r.running.Store(false)
	})
	return closeErr
}

func (r *probeLocalTUNDataPlaneRunner) Stats() probeLocalTUNDataPlaneStats {
	return probeLocalTUNDataPlaneStats{
		Running:   r.running.Load(),
		RXPackets: r.rxPackets.Load(),
		RXBytes:   r.rxBytes.Load(),
		TXPackets: r.txPackets.Load(),
		TXBytes:   r.txBytes.Load(),
	}
}

func (r *probeLocalTUNDataPlaneRunner) WritePacket(packet []byte) error {
	if len(packet) == 0 {
		return nil
	}
	if !r.running.Load() {
		return errors.New("probe local tun data plane is not running")
	}
	packetPtr, _, allocErr := r.allocateSendPacketProc.Call(r.sessionHandle, uintptr(len(packet)))
	if packetPtr == 0 {
		if allocErr != nil && !probeLocalTUNIsZeroErrno(allocErr) {
			return fmt.Errorf("WintunAllocateSendPacket failed: %w", allocErr)
		}
		return errors.New("WintunAllocateSendPacket returned empty packet pointer")
	}
	copy(probeLocalTUNUintptrToByteSlice(packetPtr, len(packet)), packet)
	_, _, sendErr := r.sendPacketProc.Call(r.sessionHandle, packetPtr)
	if sendErr != nil && !probeLocalTUNIsZeroErrno(sendErr) {
		return fmt.Errorf("WintunSendPacket failed: %w", sendErr)
	}
	r.txPackets.Add(1)
	r.txBytes.Add(uint64(len(packet)))
	return nil
}

func probeLocalTUNIsZeroErrno(err error) bool {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno == 0
	}
	return false
}

func probeLocalTUNIsNoMoreItemsErr(err error) bool {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	return errno == syscall.Errno(windows.ERROR_NO_MORE_ITEMS)
}

func probeLocalTUNUintptrToByteSlice(ptr uintptr, n int) []byte {
	var s []byte
	h := (*[3]uintptr)(unsafe.Pointer(&s))
	h[0] = ptr
	h[1] = uintptr(n)
	h[2] = uintptr(n)
	return s
}
