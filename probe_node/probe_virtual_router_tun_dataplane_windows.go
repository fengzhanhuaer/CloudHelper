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
	probeLocalTUNInboundQueueFrames     = 2048
	probeLocalTUNInboundDispatchShards  = 8
	probeLocalTUNOutboundQueueFrames    = 4096
	probeLocalTUNPooledPacketCapacity   = 2048
	probeLocalTUNSlowWriteThreshold     = 10 * time.Millisecond
	probeLocalTUNAbnormalWriteThreshold = 100 * time.Millisecond
	probeLocalTUNSlowWriteLogInterval   = 5 * time.Second
	probeLocalTUNAbnormalQueuePercent   = 75
)

var (
	probeLocalEnsureWintunLibraryForDataPlane = ensureProbeEmbeddedWintunLibrary
	probeLocalResolveWintunPathForDataPlane   = resolveProbeWintunPath
	probeLocalCreateWintunAdapterForDataPlane = createProbeLocalWintunAdapter
	probeLocalCloseWintunAdapterForDataPlane  = closeProbeLocalWintunAdapter
	probeVirtualRouterNewTUNDataPlaneRunner   = newProbeVirtualRouterTUNDataPlaneRunner
	probeLocalTUNInboundPacketHandler         func([]byte)
	probeLocalTUNOutboundPacketPool           = sync.Pool{New: func() any {
		return &probeVirtualRouterTUNOutboundPacket{payload: make([]byte, probeLocalTUNPooledPacketCapacity)}
	}}
)

type probeVirtualRouterTUNOutboundPacket struct {
	payload    []byte
	enqueuedAt time.Time
}

type probeVirtualRouterTUNWriteTiming struct {
	total    time.Duration
	lockWait time.Duration
	allocate time.Duration
	copy     time.Duration
	send     time.Duration
}

type probeVirtualRouterTUNSlowWriteSummary struct {
	packets       uint64
	writeSlow     uint64
	queueSlow     uint64
	maxWrite      time.Duration
	maxQueueWait  time.Duration
	maxLockWait   time.Duration
	maxAllocate   time.Duration
	maxCopy       time.Duration
	maxSend       time.Duration
	maxBytes      int
	maxQueueDepth int
}

var probeVirtualRouterTUNDataPlaneState = struct {
	mu            sync.Mutex
	startMu       sync.Mutex
	libraryPath   string
	adapterHandle uintptr
	interfaceLUID uint64
	ifIndex       int
	dataPlane     probeVirtualRouterTUNDataPlane
}{}

func startProbeVirtualRouterTUNDataPlane() error {
	probeVirtualRouterTUNDataPlaneState.startMu.Lock()
	defer probeVirtualRouterTUNDataPlaneState.startMu.Unlock()

	probeVirtualRouterTUNDataPlaneState.mu.Lock()
	dataPlane := probeVirtualRouterTUNDataPlaneState.dataPlane
	interfaceLUID := probeVirtualRouterTUNDataPlaneState.interfaceLUID
	ifIndex := probeVirtualRouterTUNDataPlaneState.ifIndex
	probeVirtualRouterTUNDataPlaneState.mu.Unlock()
	if dataPlane != nil {
		stats := dataPlane.Stats()
		if stats.Running && probeVirtualRouterTUNDataPlaneRouteTargetHealthy(interfaceLUID, ifIndex) {
			return nil
		}
		logProbeWarnf("probe virtual router tun data plane is stale; restarting session: running=%v if_luid=%d if_index=%d", stats.Running, interfaceLUID, ifIndex)
		if err := stopProbeVirtualRouterTUNDataPlane(); err != nil {
			logProbeWarnf("probe virtual router tun stale data plane stop failed before restart: %v", err)
		}
		probeVirtualRouterTUNDataPlaneState.mu.Lock()
		dataPlane = probeVirtualRouterTUNDataPlaneState.dataPlane
		probeVirtualRouterTUNDataPlaneState.mu.Unlock()
		if dataPlane != nil {
			return nil
		}
	}

	if err := probeLocalEnsureWintunLibraryForDataPlane(); err != nil {
		clearProbeRouteWindowsDirectRouteTarget()
		return fmt.Errorf("prepare wintun library: %w", err)
	}
	libraryPath, err := probeLocalResolveWintunPathForDataPlane()
	if err != nil {
		return fmt.Errorf("resolve wintun path: %w", err)
	}
	handle, err := probeLocalCreateWintunAdapterForDataPlane(libraryPath, probeLocalTUNAdapterName, probeLocalTUNTunnelType)
	if err != nil {
		clearProbeRouteWindowsDirectRouteTarget()
		return fmt.Errorf("create/open wintun adapter: %w", err)
	}
	routeTarget, err := prepareProbeVirtualRouterTUNDataPlaneRouteTarget(libraryPath, handle)
	if err != nil {
		_ = probeLocalCloseWintunAdapterForDataPlane(libraryPath, handle)
		clearProbeRouteWindowsDirectRouteTarget()
		return err
	}
	if err := prepareProbeRouteWindowsDirectRouteTarget(); err != nil {
		_ = probeLocalCloseWintunAdapterForDataPlane(libraryPath, handle)
		clearProbeRouteWindowsDirectRouteTarget()
		return fmt.Errorf("prepare direct route target: %w", err)
	}
	dataPlane, err = probeVirtualRouterNewTUNDataPlaneRunner(libraryPath, handle, func(packet []byte) {
		handler := currentProbeVirtualRouterTUNInboundPacketHandler()
		if handler != nil && len(packet) > 0 {
			handler(packet)
		}
	}, func(format string, args ...any) {
		logProbeInfof(format, args...)
	})
	if err != nil {
		_ = probeLocalCloseWintunAdapterForDataPlane(libraryPath, handle)
		clearProbeRouteWindowsDirectRouteTarget()
		return err
	}

	probeVirtualRouterTUNDataPlaneState.mu.Lock()
	if probeVirtualRouterTUNDataPlaneState.dataPlane != nil {
		probeVirtualRouterTUNDataPlaneState.mu.Unlock()
		_ = dataPlane.Close()
		_ = probeLocalCloseWintunAdapterForDataPlane(libraryPath, handle)
		return nil
	}
	probeVirtualRouterTUNDataPlaneState.libraryPath = strings.TrimSpace(libraryPath)
	probeVirtualRouterTUNDataPlaneState.adapterHandle = handle
	probeVirtualRouterTUNDataPlaneState.interfaceLUID = routeTarget.InterfaceLUID
	probeVirtualRouterTUNDataPlaneState.ifIndex = routeTarget.InterfaceIndex
	probeVirtualRouterTUNDataPlaneState.dataPlane = dataPlane
	probeVirtualRouterTUNDataPlaneState.mu.Unlock()

	ensureProbeVirtualRouterLocalInterfaceIP()

	stats := dataPlane.Stats()
	logProbeInfof("probe virtual router tun data plane started: running=%v rx_packets=%d rx_bytes=%d tx_packets=%d tx_bytes=%d if_index=%d if_luid=%d gateway=%s", stats.Running, stats.RXPackets, stats.RXBytes, stats.TXPackets, stats.TXBytes, routeTarget.InterfaceIndex, routeTarget.InterfaceLUID, strings.TrimSpace(routeTarget.Gateway))
	return nil
}

func prepareProbeVirtualRouterTUNDataPlaneRouteTarget(libraryPath string, handle uintptr) (probeRouteWindowsTUNRouteTarget, error) {
	luid, err := probeLocalGetWintunAdapterLUIDFromHandle(libraryPath, handle)
	if err != nil {
		return probeRouteWindowsTUNRouteTarget{}, fmt.Errorf("resolve tun adapter luid from handle: %w", err)
	}
	if err := ensureProbeRouteWindowsRouteTargetByInterfaceLUID(luid); err != nil {
		return probeRouteWindowsTUNRouteTarget{}, fmt.Errorf("prepare tun adapter route target: %w", err)
	}
	routeTarget, err := resolveProbeRouteWindowsTUNRouteTarget()
	if err != nil {
		return probeRouteWindowsTUNRouteTarget{}, fmt.Errorf("resolve prepared tun route target: %w", err)
	}
	if routeTarget.InterfaceLUID == 0 || routeTarget.InterfaceIndex <= 0 {
		return probeRouteWindowsTUNRouteTarget{}, fmt.Errorf("prepared tun route target is incomplete: if_luid=%d if_index=%d", routeTarget.InterfaceLUID, routeTarget.InterfaceIndex)
	}
	if routeTarget.InterfaceLUID != luid {
		logProbeWarnf("probe virtual router tun route target luid changed after prepare: handle_luid=%d env_luid=%d if_index=%d", luid, routeTarget.InterfaceLUID, routeTarget.InterfaceIndex)
	}
	return routeTarget, nil
}

func stopProbeVirtualRouterTUNDataPlane() error {
	probeVirtualRouterTUNDataPlaneState.mu.Lock()
	libraryPath := strings.TrimSpace(probeVirtualRouterTUNDataPlaneState.libraryPath)
	handle := probeVirtualRouterTUNDataPlaneState.adapterHandle
	dataPlane := probeVirtualRouterTUNDataPlaneState.dataPlane
	probeVirtualRouterTUNDataPlaneState.libraryPath = ""
	probeVirtualRouterTUNDataPlaneState.adapterHandle = 0
	probeVirtualRouterTUNDataPlaneState.interfaceLUID = 0
	probeVirtualRouterTUNDataPlaneState.ifIndex = 0
	probeVirtualRouterTUNDataPlaneState.dataPlane = nil
	probeVirtualRouterTUNDataPlaneState.mu.Unlock()

	defer clearProbeRouteWindowsDirectRouteTarget()
	var allErr error
	if dataPlane != nil {
		stats := dataPlane.Stats()
		if err := dataPlane.Close(); err != nil {
			allErr = errors.Join(allErr, err)
		}
		logProbeInfof("probe virtual router tun data plane stopped: rx_packets=%d rx_bytes=%d tx_packets=%d tx_bytes=%d", stats.RXPackets, stats.RXBytes, stats.TXPackets, stats.TXBytes)
	}
	if closeErr := probeLocalCloseWintunAdapterForDataPlane(libraryPath, handle); closeErr != nil {
		allErr = errors.Join(allErr, closeErr)
	}
	return allErr
}

func probeVirtualRouterTUNDataPlaneRouteTargetHealthy(interfaceLUID uint64, ifIndex int) bool {
	if interfaceLUID > 0 {
		if err := ensureProbeRouteWindowsRouteTargetByInterfaceLUID(interfaceLUID); err == nil {
			return true
		} else {
			logProbeWarnf("probe virtual router tun data plane route target health check by luid failed: if_luid=%d if_index=%d err=%v", interfaceLUID, ifIndex, err)
		}
	}
	if ifIndex > 0 {
		if err := ensureProbeRouteWindowsRouteTargetByInterfaceIndex(ifIndex); err == nil {
			return true
		} else {
			logProbeWarnf("probe virtual router tun data plane route target health check by ifindex failed: if_luid=%d if_index=%d err=%v", interfaceLUID, ifIndex, err)
		}
	}
	return false
}

func probeVirtualRouterTUNDataPlaneStatsSnapshot() probeVirtualRouterTUNDataPlaneStats {
	probeVirtualRouterTUNDataPlaneState.mu.Lock()
	defer probeVirtualRouterTUNDataPlaneState.mu.Unlock()
	if probeVirtualRouterTUNDataPlaneState.dataPlane == nil {
		return probeVirtualRouterTUNDataPlaneStats{}
	}
	return probeVirtualRouterTUNDataPlaneState.dataPlane.Stats()
}

func probeVirtualRouterTUNDataPlaneRunning() bool {
	stats := probeVirtualRouterTUNDataPlaneStatsSnapshot()
	return stats.Running
}

func writeProbeVirtualRouterTUNPacket(packet []byte) error {
	probeVirtualRouterTUNDataPlaneState.mu.Lock()
	dataPlane := probeVirtualRouterTUNDataPlaneState.dataPlane
	probeVirtualRouterTUNDataPlaneState.mu.Unlock()
	if dataPlane == nil {
		return errors.New("probe virtual router tun data plane is not running")
	}
	return dataPlane.WritePacket(packet)
}

func handleProbeVirtualRouterTUNInboundPacket(packet []byte) {
	_ = handleProbeVirtualRouterTUNPacket(packet)
}

func resetProbeVirtualRouterTUNDataPlaneHooksForTest() {
	probeLocalEnsureWintunLibraryForDataPlane = ensureProbeEmbeddedWintunLibrary
	probeLocalResolveWintunPathForDataPlane = resolveProbeWintunPath
	probeLocalCreateWintunAdapterForDataPlane = createProbeLocalWintunAdapter
	probeLocalCloseWintunAdapterForDataPlane = closeProbeLocalWintunAdapter
	probeVirtualRouterNewTUNDataPlaneRunner = newProbeVirtualRouterTUNDataPlaneRunner
	probeLocalTUNInboundPacketHandler = nil
	_ = stopProbeVirtualRouterTUNDataPlane()
}

func currentProbeVirtualRouterTUNInboundPacketHandler() func([]byte) {
	if probeLocalTUNInboundPacketHandler != nil {
		return probeLocalTUNInboundPacketHandler
	}
	return handleProbeVirtualRouterTUNInboundPacket
}

type probeVirtualRouterTUNDataPlaneRunner struct {
	sessionHandle uintptr
	readWaitEvent windows.Handle

	endSessionProc           *syscall.LazyProc
	receivePacketProc        *syscall.LazyProc
	releaseReceivePacketProc *syscall.LazyProc
	allocateSendPacketProc   *syscall.LazyProc
	sendPacketProc           *syscall.LazyProc

	onPacket func([]byte)
	logf     func(string, ...any)

	inboundCh        chan []byte
	inboundDispatch  []chan []byte
	outboundCh       chan *probeVirtualRouterTUNOutboundPacket
	writeMu          sync.Mutex
	slowWrite        probeVirtualRouterTUNSlowWriteSummary
	slowWriteEpisode bool

	stopCh      chan struct{}
	doneCh      chan struct{}
	writeDoneCh chan struct{}
	closeOnce   sync.Once

	running           atomic.Bool
	rxPackets         atomic.Uint64
	rxBytes           atomic.Uint64
	txPackets         atomic.Uint64
	txBytes           atomic.Uint64
	txDropped         atomic.Uint64
	txErrors          atomic.Uint64
	txSlowWrites      atomic.Uint64
	txSlowQueueWaits  atomic.Uint64
	txLastWriteMs     atomic.Uint64
	txMaxWriteMs      atomic.Uint64
	txLastQueueWaitMs atomic.Uint64
	txMaxQueueWaitMs  atomic.Uint64
	txMaxLockWaitUS   atomic.Uint64
	txMaxAllocateUS   atomic.Uint64
	txMaxCopyUS       atomic.Uint64
	txMaxSendUS       atomic.Uint64
}

func newProbeVirtualRouterTUNDataPlaneRunner(libraryPath string, adapterHandle uintptr, onPacket func([]byte), logf func(string, ...any)) (probeVirtualRouterTUNDataPlane, error) {
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

	runner := &probeVirtualRouterTUNDataPlaneRunner{
		sessionHandle:            sessionHandle,
		readWaitEvent:            windows.Handle(readWaitHandle),
		endSessionProc:           endSessionProc,
		receivePacketProc:        receivePacketProc,
		releaseReceivePacketProc: releaseReceivePacketProc,
		allocateSendPacketProc:   allocateSendPacketProc,
		sendPacketProc:           sendPacketProc,
		onPacket:                 onPacket,
		logf:                     logf,
		inboundCh:                make(chan []byte, probeLocalTUNInboundQueueFrames),
		inboundDispatch:          makeProbeVirtualRouterTUNInboundDispatchShards(probeLocalTUNInboundDispatchShards, probeLocalTUNInboundQueueFrames),
		outboundCh:               make(chan *probeVirtualRouterTUNOutboundPacket, probeLocalTUNOutboundQueueFrames),
		stopCh:                   make(chan struct{}),
		doneCh:                   make(chan struct{}),
		writeDoneCh:              make(chan struct{}),
	}
	runner.running.Store(true)
	for shardID, shard := range runner.inboundDispatch {
		go runner.inboundShardWorker(shardID, shard)
	}
	go runner.inboundDispatchWorker()
	go runner.writeLoop()
	go runner.readLoop()

	if runner.logf != nil {
		runner.logf("probe local tun session started: adapter_handle=%d session_handle=%d", adapterHandle, sessionHandle)
	}
	return runner, nil
}

func (r *probeVirtualRouterTUNDataPlaneRunner) readLoop() {
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

func (r *probeVirtualRouterTUNDataPlaneRunner) handleInboundPayload(payload []byte) {
	if len(payload) == 0 || r.onPacket == nil {
		return
	}
	packet := append([]byte(nil), payload...)
	if r.inboundCh == nil {
		go r.onPacket(packet)
		return
	}
	select {
	case r.inboundCh <- packet:
	case <-r.stopCh:
	default:
		if r.logf != nil {
			r.logf("probe local tun inbound packet drop: reason=handler_queue_full depth=%d capacity=%d", len(r.inboundCh), cap(r.inboundCh))
		}
	}
}

func (r *probeVirtualRouterTUNDataPlaneRunner) inboundDispatchWorker() {
	for {
		select {
		case <-r.stopCh:
			return
		case payload := <-r.inboundCh:
			if len(payload) == 0 || r.onPacket == nil {
				continue
			}
			if len(r.inboundDispatch) == 0 {
				r.onPacket(payload)
				continue
			}
			shardID := probeVirtualRouterPacketDispatchShard(payload, len(r.inboundDispatch))
			shard := r.inboundDispatch[shardID]
			select {
			case shard <- payload:
			case <-r.stopCh:
				return
			default:
				if r.logf != nil {
					r.logf("probe local tun inbound packet drop: reason=dispatch_queue_full shard=%d depth=%d capacity=%d", shardID, len(shard), cap(shard))
				}
			}
		}
	}
}

func (r *probeVirtualRouterTUNDataPlaneRunner) inboundShardWorker(shardID int, shard chan []byte) {
	for {
		select {
		case <-r.stopCh:
			return
		case payload := <-shard:
			if len(payload) == 0 || r.onPacket == nil {
				continue
			}
			r.onPacket(payload)
		}
	}
}

func (r *probeVirtualRouterTUNDataPlaneRunner) writeLoop() {
	ticker := time.NewTicker(probeLocalTUNSlowWriteLogInterval)
	defer func() {
		ticker.Stop()
		r.flushSlowWriteSummary()
		r.releaseQueuedOutboundPackets()
		close(r.writeDoneCh)
	}()
	for {
		select {
		case <-r.stopCh:
			return
		case <-ticker.C:
			r.flushSlowWriteSummary()
		case outbound := <-r.outboundCh:
			if outbound == nil || len(outbound.payload) == 0 {
				releaseProbeVirtualRouterTUNOutboundPacket(outbound)
				continue
			}
			packet := outbound.payload
			packetBytes := len(packet)
			queueWait := time.Since(outbound.enqueuedAt)
			timing, err := r.writePacketDirectTimed(packet)
			releaseProbeVirtualRouterTUNOutboundPacket(outbound)
			r.recordWriteTimings(queueWait, timing)
			if err != nil {
				r.txErrors.Add(1)
				if r.logf != nil {
					r.logf("probe local tun outbound packet write failed: err=%v", err)
				}
				continue
			}
			writeSlow := timing.total >= probeLocalTUNSlowWriteThreshold
			queueSlow := queueWait >= probeLocalTUNSlowWriteThreshold
			if writeSlow {
				r.txSlowWrites.Add(1)
			}
			if queueSlow {
				r.txSlowQueueWaits.Add(1)
			}
			if writeSlow || queueSlow {
				r.recordSlowWriteSummary(packetBytes, len(r.outboundCh), queueWait, timing, writeSlow, queueSlow)
			}
		}
	}
}

func (r *probeVirtualRouterTUNDataPlaneRunner) recordWriteTimings(queueWait time.Duration, timing probeVirtualRouterTUNWriteTiming) {
	storeProbeVirtualRouterTUNDuration(&r.txLastWriteMs, &r.txMaxWriteMs, timing.total, time.Millisecond)
	storeProbeVirtualRouterTUNDuration(&r.txLastQueueWaitMs, &r.txMaxQueueWaitMs, queueWait, time.Millisecond)
}

func (r *probeVirtualRouterTUNDataPlaneRunner) recordSlowWriteSummary(packetBytes int, queueDepth int, queueWait time.Duration, timing probeVirtualRouterTUNWriteTiming, writeSlow bool, queueSlow bool) {
	summary := &r.slowWrite
	summary.packets++
	if writeSlow {
		summary.writeSlow++
	}
	if queueSlow {
		summary.queueSlow++
	}
	summary.maxWrite = max(summary.maxWrite, timing.total)
	summary.maxQueueWait = max(summary.maxQueueWait, queueWait)
	summary.maxLockWait = max(summary.maxLockWait, timing.lockWait)
	summary.maxAllocate = max(summary.maxAllocate, timing.allocate)
	summary.maxCopy = max(summary.maxCopy, timing.copy)
	summary.maxSend = max(summary.maxSend, timing.send)
	summary.maxBytes = max(summary.maxBytes, packetBytes)
	summary.maxQueueDepth = max(summary.maxQueueDepth, queueDepth)
	storeProbeVirtualRouterTUNMaximumDuration(&r.txMaxLockWaitUS, timing.lockWait, time.Microsecond)
	storeProbeVirtualRouterTUNMaximumDuration(&r.txMaxAllocateUS, timing.allocate, time.Microsecond)
	storeProbeVirtualRouterTUNMaximumDuration(&r.txMaxCopyUS, timing.copy, time.Microsecond)
	storeProbeVirtualRouterTUNMaximumDuration(&r.txMaxSendUS, timing.send, time.Microsecond)
}

func (r *probeVirtualRouterTUNDataPlaneRunner) flushSlowWriteSummary() {
	summary := r.slowWrite
	if summary.packets == 0 {
		r.slowWriteEpisode = false
		return
	}
	r.slowWrite = probeVirtualRouterTUNSlowWriteSummary{}
	queueCapacity := cap(r.outboundCh)
	queueAbnormal := queueCapacity > 0 && summary.maxQueueDepth*100 >= queueCapacity*probeLocalTUNAbnormalQueuePercent
	abnormal := summary.maxWrite >= probeLocalTUNAbnormalWriteThreshold ||
		summary.maxQueueWait >= probeLocalTUNAbnormalWriteThreshold ||
		queueAbnormal
	if !abnormal {
		r.slowWriteEpisode = false
		return
	}
	if r.slowWriteEpisode {
		return
	}
	r.slowWriteEpisode = true
	if r.logf == nil {
		return
	}
	r.logf(
		"probe local tun outbound stall detected: packets=%d write_slow=%d queue_slow=%d write_max_ms=%d queue_wait_max_ms=%d lock_wait_max_us=%d allocate_max_us=%d copy_max_us=%d send_max_us=%d bytes_max=%d queue_max=%d/%d",
		summary.packets,
		summary.writeSlow,
		summary.queueSlow,
		probeTUNDurationUnits(summary.maxWrite, time.Millisecond),
		probeTUNDurationUnits(summary.maxQueueWait, time.Millisecond),
		probeTUNDurationUnits(summary.maxLockWait, time.Microsecond),
		probeTUNDurationUnits(summary.maxAllocate, time.Microsecond),
		probeTUNDurationUnits(summary.maxCopy, time.Microsecond),
		probeTUNDurationUnits(summary.maxSend, time.Microsecond),
		summary.maxBytes,
		summary.maxQueueDepth,
		queueCapacity,
	)
}

func (r *probeVirtualRouterTUNDataPlaneRunner) Close() error {
	var closeErr error
	r.closeOnce.Do(func() {
		r.running.Store(false)
		close(r.stopCh)
		select {
		case <-r.doneCh:
		case <-time.After(2 * time.Second):
			if r.logf != nil {
				r.logf("probe local tun data plane close wait timeout")
			}
		}
		select {
		case <-r.writeDoneCh:
		case <-time.After(2 * time.Second):
			if r.logf != nil {
				r.logf("probe local tun data plane write close wait timeout")
			}
		}
		r.releaseQueuedOutboundPackets()
		r.writeMu.Lock()
		defer r.writeMu.Unlock()
		if r.endSessionProc != nil && r.sessionHandle != 0 {
			_, _, callErr := r.endSessionProc.Call(r.sessionHandle)
			if callErr != nil && !probeLocalTUNIsZeroErrno(callErr) {
				closeErr = fmt.Errorf("WintunEndSession failed: %w", callErr)
			}
		}
	})
	return closeErr
}

func (r *probeVirtualRouterTUNDataPlaneRunner) Stats() probeVirtualRouterTUNDataPlaneStats {
	entryDepth, entryCapacity, dispatchDepth, dispatchCapacity, dispatchWorkers := snapshotProbeVirtualRouterTUNInboundQueues(r.inboundCh, r.inboundDispatch)
	outDepth, outCapacity, outWorkers := 0, 0, 0
	if r.outboundCh != nil {
		outDepth, outCapacity, outWorkers = len(r.outboundCh), cap(r.outboundCh), 1
	}
	return probeVirtualRouterTUNDataPlaneStats{
		Running:                      r.running.Load(),
		RXPackets:                    r.rxPackets.Load(),
		RXBytes:                      r.rxBytes.Load(),
		TXPackets:                    r.txPackets.Load(),
		TXBytes:                      r.txBytes.Load(),
		InboundQueueDepth:            entryDepth + dispatchDepth,
		InboundQueueCapacity:         entryCapacity + dispatchCapacity,
		InboundEntryQueueDepth:       entryDepth,
		InboundEntryQueueCapacity:    entryCapacity,
		InboundDispatchQueueDepth:    dispatchDepth,
		InboundDispatchQueueCapacity: dispatchCapacity,
		InboundDispatchWorkers:       dispatchWorkers,
		OutboundQueueDepth:           outDepth,
		OutboundQueueCapacity:        outCapacity,
		OutboundWorkers:              outWorkers,
		TXDropped:                    r.txDropped.Load(),
		TXErrors:                     r.txErrors.Load(),
		TXSlowWrites:                 r.txSlowWrites.Load(),
		TXSlowQueueWaits:             r.txSlowQueueWaits.Load(),
		TXLastWriteMs:                r.txLastWriteMs.Load(),
		TXMaxWriteMs:                 r.txMaxWriteMs.Load(),
		TXLastQueueWaitMs:            r.txLastQueueWaitMs.Load(),
		TXMaxQueueWaitMs:             r.txMaxQueueWaitMs.Load(),
		TXMaxLockWaitUS:              r.txMaxLockWaitUS.Load(),
		TXMaxAllocateUS:              r.txMaxAllocateUS.Load(),
		TXMaxCopyUS:                  r.txMaxCopyUS.Load(),
		TXMaxSendUS:                  r.txMaxSendUS.Load(),
	}
}

func (r *probeVirtualRouterTUNDataPlaneRunner) WritePacket(packet []byte) error {
	if len(packet) == 0 {
		return nil
	}
	if !r.running.Load() {
		return errors.New("probe virtual router tun data plane is not running")
	}
	if r.outboundCh == nil {
		return r.writePacketDirect(packet)
	}
	outbound := acquireProbeVirtualRouterTUNOutboundPacket(packet)
	select {
	case r.outboundCh <- outbound:
		return nil
	case <-r.stopCh:
		releaseProbeVirtualRouterTUNOutboundPacket(outbound)
		return errors.New("probe virtual router tun data plane is not running")
	default:
		releaseProbeVirtualRouterTUNOutboundPacket(outbound)
		r.txDropped.Add(1)
		return fmt.Errorf("probe virtual router tun outbound queue full: depth=%d capacity=%d", len(r.outboundCh), cap(r.outboundCh))
	}
}

func (r *probeVirtualRouterTUNDataPlaneRunner) writePacketDirect(packet []byte) error {
	_, err := r.writePacketDirectTimed(packet)
	return err
}

func (r *probeVirtualRouterTUNDataPlaneRunner) writePacketDirectTimed(packet []byte) (timing probeVirtualRouterTUNWriteTiming, err error) {
	startedAt := time.Now()
	defer func() {
		timing.total = time.Since(startedAt)
	}()
	if len(packet) == 0 {
		return timing, nil
	}
	if !r.running.Load() {
		return timing, errors.New("probe virtual router tun data plane is not running")
	}
	lockStartedAt := time.Now()
	r.writeMu.Lock()
	timing.lockWait = time.Since(lockStartedAt)
	defer r.writeMu.Unlock()
	if !r.running.Load() {
		return timing, errors.New("probe virtual router tun data plane is not running")
	}
	allocateStartedAt := time.Now()
	packetPtr, _, allocErr := r.allocateSendPacketProc.Call(r.sessionHandle, uintptr(len(packet)))
	timing.allocate = time.Since(allocateStartedAt)
	if packetPtr == 0 {
		if allocErr != nil && !probeLocalTUNIsZeroErrno(allocErr) {
			return timing, fmt.Errorf("WintunAllocateSendPacket failed: %w", allocErr)
		}
		return timing, errors.New("WintunAllocateSendPacket returned empty packet pointer")
	}
	copyStartedAt := time.Now()
	copy(probeLocalTUNUintptrToByteSlice(packetPtr, len(packet)), packet)
	timing.copy = time.Since(copyStartedAt)
	sendStartedAt := time.Now()
	_, _, sendErr := r.sendPacketProc.Call(r.sessionHandle, packetPtr)
	timing.send = time.Since(sendStartedAt)
	if sendErr != nil && !probeLocalTUNIsZeroErrno(sendErr) {
		return timing, fmt.Errorf("WintunSendPacket failed: %w", sendErr)
	}
	r.txPackets.Add(1)
	r.txBytes.Add(uint64(len(packet)))
	return timing, nil
}

func acquireProbeVirtualRouterTUNOutboundPacket(packet []byte) *probeVirtualRouterTUNOutboundPacket {
	outbound := probeLocalTUNOutboundPacketPool.Get().(*probeVirtualRouterTUNOutboundPacket)
	if cap(outbound.payload) < len(packet) {
		outbound.payload = make([]byte, len(packet))
	} else {
		outbound.payload = outbound.payload[:len(packet)]
	}
	copy(outbound.payload, packet)
	outbound.enqueuedAt = time.Now()
	return outbound
}

func releaseProbeVirtualRouterTUNOutboundPacket(outbound *probeVirtualRouterTUNOutboundPacket) {
	if outbound == nil {
		return
	}
	if cap(outbound.payload) != probeLocalTUNPooledPacketCapacity {
		return
	}
	outbound.payload = outbound.payload[:probeLocalTUNPooledPacketCapacity]
	outbound.enqueuedAt = time.Time{}
	probeLocalTUNOutboundPacketPool.Put(outbound)
}

func (r *probeVirtualRouterTUNDataPlaneRunner) releaseQueuedOutboundPackets() {
	if r == nil || r.outboundCh == nil {
		return
	}
	for {
		select {
		case outbound := <-r.outboundCh:
			releaseProbeVirtualRouterTUNOutboundPacket(outbound)
		default:
			return
		}
	}
}

func storeProbeVirtualRouterTUNDuration(last *atomic.Uint64, maximum *atomic.Uint64, duration time.Duration, unit time.Duration) {
	value := probeTUNDurationUnits(duration, unit)
	last.Store(value)
	storeProbeVirtualRouterTUNMaximum(maximum, value)
}

func storeProbeVirtualRouterTUNMaximumDuration(maximum *atomic.Uint64, duration time.Duration, unit time.Duration) {
	storeProbeVirtualRouterTUNMaximum(maximum, probeTUNDurationUnits(duration, unit))
}

func storeProbeVirtualRouterTUNMaximum(maximum *atomic.Uint64, value uint64) {
	for {
		old := maximum.Load()
		if value <= old || maximum.CompareAndSwap(old, value) {
			return
		}
	}
}

func probeTUNDurationUnits(duration time.Duration, unit time.Duration) uint64 {
	if duration <= 0 || unit <= 0 {
		return 0
	}
	value := uint64(duration / unit)
	if value == 0 {
		return 1
	}
	return value
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
