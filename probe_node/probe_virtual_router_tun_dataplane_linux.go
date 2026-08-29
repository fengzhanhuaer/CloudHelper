//go:build linux

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	probeLocalLinuxTUNPacketBufferSize       = 65535
	probeLocalLinuxTUNInboundQueueFrames     = 2048
	probeLocalLinuxTUNInboundInitialFrames   = 128
	probeLocalLinuxTUNInboundDispatchShards  = 8
	probeLocalLinuxTUNOutboundQueueFrames    = 4096
	probeLocalLinuxTUNOutboundInitialFrames  = 256
	probeLocalLinuxTUNWriteTimeout           = 500 * time.Millisecond
	probeLocalLinuxTUNWritePollTimeoutMS     = 50
	probeLocalLinuxTUNSlowWriteThreshold     = 10 * time.Millisecond
	probeLocalLinuxTUNAbnormalWriteThreshold = 100 * time.Millisecond
	probeLocalLinuxTUNSlowWriteLogInterval   = 5 * time.Second
	probeLocalLinuxTUNAbnormalQueuePercent   = 75
	probeLocalLinuxTUNCloseWaitTimeout       = 2 * time.Second
)

type probeVirtualRouterLinuxTUNSlowWriteSummary struct {
	packets       uint64
	maxWrite      time.Duration
	maxBytes      int
	maxQueueDepth int
}

var probeLocalLinuxTUNDataPlaneState = struct {
	mu     sync.Mutex
	runner probeVirtualRouterTUNDataPlane
	dev    string
}{}

var probeVirtualRouterLinuxNewTUNDataPlaneRunner func(dev string) (probeVirtualRouterTUNDataPlane, error)

type probeVirtualRouterLinuxTUNDataPlaneRunner struct {
	file *os.File
	dev  string

	writeMu sync.Mutex
	closed  atomic.Bool

	inboundCh        *probeAdaptiveQueue[[]byte]
	inboundDispatch  []*probeAdaptiveQueue[[]byte]
	outboundCh       *probeAdaptiveQueue[[]byte]
	stopCh           chan struct{}
	rxPackets        atomic.Uint64
	rxBytes          atomic.Uint64
	txPackets        atomic.Uint64
	txBytes          atomic.Uint64
	txDropped        atomic.Uint64
	txErrors         atomic.Uint64
	txSlowWrites     atomic.Uint64
	txLastWriteMs    atomic.Uint64
	txMaxWriteMs     atomic.Uint64
	slowWrite        probeVirtualRouterLinuxTUNSlowWriteSummary
	slowWriteEpisode bool
	logf             func(string, ...any)
	doneCh           chan struct{}
	writeDoneCh      chan struct{}
	closeOnce        sync.Once
}

func startProbeVirtualRouterTUNDataPlane() error {
	probeLocalLinuxTUNDataPlaneState.mu.Lock()
	if runner := probeLocalLinuxTUNDataPlaneState.runner; runner != nil {
		stats := runner.Stats()
		probeLocalLinuxTUNDataPlaneState.mu.Unlock()
		if stats.Running {
			return nil
		}
	}
	probeLocalLinuxTUNDataPlaneState.mu.Unlock()

	dev, err := ensureProbeLocalLinuxTUNDeviceReady()
	if err != nil {
		return err
	}
	runner, err := currentProbeVirtualRouterLinuxNewTUNDataPlaneRunner()(dev)
	if err != nil {
		return err
	}

	probeLocalLinuxTUNDataPlaneState.mu.Lock()
	if probeLocalLinuxTUNDataPlaneState.runner != nil {
		probeLocalLinuxTUNDataPlaneState.mu.Unlock()
		_ = runner.Close()
		return nil
	}
	probeLocalLinuxTUNDataPlaneState.runner = runner
	probeLocalLinuxTUNDataPlaneState.dev = dev
	probeLocalLinuxTUNDataPlaneState.mu.Unlock()

	logProbeInfof("probe local linux tun data plane started: dev=%s", dev)
	probeVirtualRouterDNSAfterTUNReady()
	return nil
}

func stopProbeVirtualRouterTUNDataPlane() error {
	probeLocalLinuxTUNDataPlaneState.mu.Lock()
	runner := probeLocalLinuxTUNDataPlaneState.runner
	dev := probeLocalLinuxTUNDataPlaneState.dev
	probeLocalLinuxTUNDataPlaneState.runner = nil
	probeLocalLinuxTUNDataPlaneState.dev = ""
	probeLocalLinuxTUNDataPlaneState.mu.Unlock()
	if runner == nil {
		return nil
	}
	stats := runner.Stats()
	err := runner.Close()
	logProbeInfof("probe local linux tun data plane stopped: dev=%s rx_packets=%d rx_bytes=%d tx_packets=%d tx_bytes=%d", dev, stats.RXPackets, stats.RXBytes, stats.TXPackets, stats.TXBytes)
	return err
}

func probeVirtualRouterTUNDataPlaneStatsSnapshot() probeVirtualRouterTUNDataPlaneStats {
	probeLocalLinuxTUNDataPlaneState.mu.Lock()
	runner := probeLocalLinuxTUNDataPlaneState.runner
	probeLocalLinuxTUNDataPlaneState.mu.Unlock()
	if runner == nil {
		return probeVirtualRouterTUNDataPlaneStats{}
	}
	return runner.Stats()
}

func probeVirtualRouterTUNDataPlaneRunning() bool {
	return probeVirtualRouterTUNDataPlaneStatsSnapshot().Running
}

func writeProbeVirtualRouterTUNPacket(packet []byte) error {
	probeLocalLinuxTUNDataPlaneState.mu.Lock()
	runner := probeLocalLinuxTUNDataPlaneState.runner
	probeLocalLinuxTUNDataPlaneState.mu.Unlock()
	if runner == nil {
		return errors.New("probe local linux tun data plane is not running")
	}
	return runner.WritePacket(packet)
}

func handleProbeVirtualRouterTUNInboundPacket(packet []byte) {
	if handleProbeVirtualRouterTUNPacket(packet) {
		return
	}
}

func resetProbeVirtualRouterTUNDataPlaneHooksForTest() {
	probeVirtualRouterLinuxNewTUNDataPlaneRunner = nil
	probeVirtualRouterDNSAfterTUNReady = func() {}
	_ = stopProbeVirtualRouterTUNDataPlane()
}

func currentProbeVirtualRouterLinuxNewTUNDataPlaneRunner() func(dev string) (probeVirtualRouterTUNDataPlane, error) {
	if probeVirtualRouterLinuxNewTUNDataPlaneRunner != nil {
		return probeVirtualRouterLinuxNewTUNDataPlaneRunner
	}
	return func(dev string) (probeVirtualRouterTUNDataPlane, error) {
		return newProbeLocalLinuxTUNDataPlaneRunner(dev)
	}
}

func newProbeLocalLinuxTUNDataPlaneRunner(dev string) (*probeVirtualRouterLinuxTUNDataPlaneRunner, error) {
	file, err := openProbeLocalLinuxTUNDevice(dev)
	if err != nil {
		return nil, err
	}
	runner := &probeVirtualRouterLinuxTUNDataPlaneRunner{
		file: file,
		dev:  dev,
		inboundCh: newProbeAdaptiveQueue[[]byte](probeAdaptiveQueueOptions{
			ID:              "tun.linux.inbound.entry",
			Stage:           "tun_inbound_entry",
			Direction:       "rx",
			InitialCapacity: probeLocalLinuxTUNInboundInitialFrames,
			MaxCapacity:     probeLocalLinuxTUNInboundQueueFrames,
		}),
		inboundDispatch: makeProbeVirtualRouterTUNInboundDispatchShards("tun.linux", probeLocalLinuxTUNInboundDispatchShards, probeLocalLinuxTUNInboundQueueFrames),
		outboundCh: newProbeAdaptiveQueue[[]byte](probeAdaptiveQueueOptions{
			ID:              "tun.linux.outbound",
			Stage:           "tun_outbound",
			Direction:       "tx",
			InitialCapacity: probeLocalLinuxTUNOutboundInitialFrames,
			MaxCapacity:     probeLocalLinuxTUNOutboundQueueFrames,
		}),
		logf:        logProbeWarnf,
		stopCh:      make(chan struct{}),
		doneCh:      make(chan struct{}),
		writeDoneCh: make(chan struct{}),
	}
	for shardID, shard := range runner.inboundDispatch {
		go runner.inboundShardWorker(shardID, shard)
	}
	go runner.inboundDispatchWorker()
	go runner.writeLoop()
	go runner.readLoop()
	return runner, nil
}

func openProbeLocalLinuxTUNDevice(dev string) (*os.File, error) {
	file, err := os.OpenFile("/dev/net/tun", os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open /dev/net/tun failed: %w", err)
	}
	if err := attachProbeLocalLinuxTUNDevice(file, dev); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := unix.SetNonblock(int(file.Fd()), true); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("set linux tun fd nonblock failed: dev=%s: %w", dev, err)
	}
	return file, nil
}

func attachProbeLocalLinuxTUNDevice(file *os.File, dev string) error {
	var ifr [unix.IFNAMSIZ + 64]byte
	copy(ifr[:unix.IFNAMSIZ], []byte(dev))
	*(*uint16)(unsafe.Pointer(&ifr[unix.IFNAMSIZ])) = uint16(unix.IFF_TUN | unix.IFF_NO_PI)
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, file.Fd(), uintptr(unix.TUNSETIFF), uintptr(unsafe.Pointer(&ifr[0])))
	if errno != 0 {
		return fmt.Errorf("attach linux tun device failed: dev=%s: %w", dev, errno)
	}
	return nil
}

func (r *probeVirtualRouterLinuxTUNDataPlaneRunner) readLoop() {
	defer close(r.doneCh)
	buf := make([]byte, probeLocalLinuxTUNPacketBufferSize)
	fd := int(r.file.Fd())
	pollFDs := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	for {
		if r.closed.Load() {
			return
		}
		pollFDs[0].Revents = 0
		ready, err := unix.Poll(pollFDs, 250)
		if err != nil {
			if r.closed.Load() || errors.Is(err, unix.EINTR) {
				continue
			}
			logProbeWarnf("probe local linux tun poll failed: dev=%s err=%v", r.dev, err)
			time.Sleep(50 * time.Millisecond)
			continue
		}
		if ready == 0 {
			continue
		}
		if pollFDs[0].Revents&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 {
			if r.closed.Load() {
				return
			}
			logProbeWarnf("probe local linux tun poll closed: dev=%s revents=%d", r.dev, pollFDs[0].Revents)
			return
		}
		if pollFDs[0].Revents&unix.POLLIN == 0 {
			continue
		}
		n, err := unix.Read(fd, buf)
		if err != nil {
			if r.closed.Load() || errors.Is(err, os.ErrClosed) || errors.Is(err, io.ErrClosedPipe) {
				return
			}
			if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EINTR) {
				continue
			}
			logProbeWarnf("probe local linux tun read failed: dev=%s err=%v", r.dev, err)
			time.Sleep(50 * time.Millisecond)
			continue
		}
		if n <= 0 {
			continue
		}
		packet := append([]byte(nil), buf[:n]...)
		r.rxPackets.Add(1)
		r.rxBytes.Add(uint64(n))
		r.handleInboundPayload(packet)
	}
}

func (r *probeVirtualRouterLinuxTUNDataPlaneRunner) handleInboundPayload(payload []byte) {
	if len(payload) == 0 {
		return
	}
	packet := append([]byte(nil), payload...)
	if r.inboundCh == nil {
		go handleProbeVirtualRouterTUNInboundPacket(packet)
		return
	}
	if !r.inboundCh.TryPush(packet) {
		select {
		case <-r.stopCh:
			return
		default:
		}
		logProbeWarnf("probe local linux tun inbound packet drop: dev=%s reason=handler_queue_full depth=%d capacity=%d limit=%d", r.dev, r.inboundCh.Len(), r.inboundCh.Capacity(), r.inboundCh.MaxCapacity())
	}
}

func (r *probeVirtualRouterLinuxTUNDataPlaneRunner) inboundDispatchWorker() {
	for {
		select {
		case <-r.stopCh:
			return
		case <-r.inboundCh.Ready():
			packet, ok := r.inboundCh.TryPop()
			if !ok {
				continue
			}
			if len(packet) == 0 {
				continue
			}
			if len(r.inboundDispatch) == 0 {
				handleProbeVirtualRouterTUNInboundPacket(packet)
				continue
			}
			shardID := probeVirtualRouterPacketDispatchShard(packet, len(r.inboundDispatch))
			shard := r.inboundDispatch[shardID]
			if !shard.TryPush(packet) {
				select {
				case <-r.stopCh:
					return
				default:
				}
				logProbeWarnf("probe local linux tun inbound packet drop: dev=%s reason=dispatch_queue_full shard=%d depth=%d capacity=%d limit=%d", r.dev, shardID, shard.Len(), shard.Capacity(), shard.MaxCapacity())
			}
		}
	}
}

func (r *probeVirtualRouterLinuxTUNDataPlaneRunner) inboundShardWorker(shardID int, shard *probeAdaptiveQueue[[]byte]) {
	for {
		select {
		case <-r.stopCh:
			return
		case <-shard.Ready():
			packet, ok := shard.TryPop()
			if !ok {
				continue
			}
			if len(packet) == 0 {
				continue
			}
			handleProbeVirtualRouterTUNInboundPacket(packet)
		}
	}
}

func (r *probeVirtualRouterLinuxTUNDataPlaneRunner) writeLoop() {
	ticker := time.NewTicker(probeLocalLinuxTUNSlowWriteLogInterval)
	defer func() {
		ticker.Stop()
		r.flushSlowWriteSummary()
		close(r.writeDoneCh)
	}()
	for {
		select {
		case <-r.stopCh:
			return
		case <-ticker.C:
			r.flushSlowWriteSummary()
		case <-r.outboundCh.Ready():
			packet, ok := r.outboundCh.TryPop()
			if !ok {
				continue
			}
			if len(packet) == 0 {
				continue
			}
			startedAt := time.Now()
			if err := r.writePacketDirect(packet); err != nil {
				r.txErrors.Add(1)
				logProbeWarnf("probe local linux tun outbound packet write failed: dev=%s err=%v", r.dev, err)
				continue
			}
			elapsed := time.Since(startedAt)
			elapsedMs := uint64(probeDurationMilliseconds(elapsed))
			r.txLastWriteMs.Store(elapsedMs)
			for {
				old := r.txMaxWriteMs.Load()
				if elapsedMs <= old || r.txMaxWriteMs.CompareAndSwap(old, elapsedMs) {
					break
				}
			}
			if elapsed >= probeLocalLinuxTUNSlowWriteThreshold {
				r.txSlowWrites.Add(1)
				r.recordSlowWriteSummary(len(packet), r.outboundCh.Len(), elapsed)
			}
		}
	}
}

func (r *probeVirtualRouterLinuxTUNDataPlaneRunner) recordSlowWriteSummary(packetBytes int, queueDepth int, elapsed time.Duration) {
	summary := &r.slowWrite
	summary.packets++
	summary.maxWrite = max(summary.maxWrite, elapsed)
	summary.maxBytes = max(summary.maxBytes, packetBytes)
	summary.maxQueueDepth = max(summary.maxQueueDepth, queueDepth)
}

func (r *probeVirtualRouterLinuxTUNDataPlaneRunner) flushSlowWriteSummary() {
	summary := r.slowWrite
	if summary.packets == 0 {
		r.slowWriteEpisode = false
		return
	}
	r.slowWrite = probeVirtualRouterLinuxTUNSlowWriteSummary{}
	queueCapacity := r.outboundCh.Capacity()
	queueAbnormal := queueCapacity > 0 && summary.maxQueueDepth*100 >= queueCapacity*probeLocalLinuxTUNAbnormalQueuePercent
	if summary.maxWrite < probeLocalLinuxTUNAbnormalWriteThreshold && !queueAbnormal {
		r.slowWriteEpisode = false
		return
	}
	if r.slowWriteEpisode {
		return
	}
	r.slowWriteEpisode = true
	if r.logf != nil {
		r.logf(
			"probe local linux tun outbound stall detected: dev=%s packets=%d write_max_ms=%d bytes_max=%d queue_max=%d/%d",
			r.dev,
			summary.packets,
			probeDurationMilliseconds(summary.maxWrite),
			summary.maxBytes,
			summary.maxQueueDepth,
			queueCapacity,
		)
	}
}

func (r *probeVirtualRouterLinuxTUNDataPlaneRunner) Close() error {
	if r == nil {
		return nil
	}
	var closeErr error
	r.closeOnce.Do(func() {
		r.closed.Store(true)
		if r.stopCh != nil {
			close(r.stopCh)
		}
		if r.inboundCh != nil {
			r.inboundCh.Close()
		}
		for _, shard := range r.inboundDispatch {
			if shard != nil {
				shard.Close()
			}
		}
		if r.outboundCh != nil {
			r.outboundCh.Close()
		}
		closeErr = r.file.Close()
		select {
		case <-r.doneCh:
		case <-time.After(probeLocalLinuxTUNCloseWaitTimeout):
			logProbeWarnf("probe local linux tun data plane close wait timeout: dev=%s", r.dev)
		}
		select {
		case <-r.writeDoneCh:
		case <-time.After(probeLocalLinuxTUNCloseWaitTimeout):
			logProbeWarnf("probe local linux tun data plane write close wait timeout: dev=%s", r.dev)
		}
	})
	return closeErr
}

func (r *probeVirtualRouterLinuxTUNDataPlaneRunner) Stats() probeVirtualRouterTUNDataPlaneStats {
	if r == nil {
		return probeVirtualRouterTUNDataPlaneStats{}
	}
	entryDepth, entryCapacity, dispatchDepth, dispatchCapacity, dispatchWorkers := snapshotProbeVirtualRouterTUNInboundQueues(r.inboundCh, r.inboundDispatch)
	outDepth, outCapacity, outWorkers := snapshotProbeVirtualRouterTUNOutboundQueue(r.outboundCh)
	return probeVirtualRouterTUNDataPlaneStats{
		Running:                      !r.closed.Load(),
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
		TXLastWriteMs:                r.txLastWriteMs.Load(),
		TXMaxWriteMs:                 r.txMaxWriteMs.Load(),
	}
}

func (r *probeVirtualRouterLinuxTUNDataPlaneRunner) WritePacket(packet []byte) error {
	if r == nil || r.closed.Load() {
		return io.ErrClosedPipe
	}
	if len(packet) == 0 {
		return nil
	}
	if r.outboundCh == nil {
		return r.writePacketDirect(packet)
	}
	payload := append([]byte(nil), packet...)
	if r.outboundCh.TryPush(payload) {
		return nil
	}
	select {
	case <-r.stopCh:
		return io.ErrClosedPipe
	default:
		r.txDropped.Add(1)
		return fmt.Errorf("probe local linux tun outbound queue full: dev=%s depth=%d capacity=%d limit=%d", r.dev, r.outboundCh.Len(), r.outboundCh.Capacity(), r.outboundCh.MaxCapacity())
	}
}

func (r *probeVirtualRouterLinuxTUNDataPlaneRunner) writePacketDirect(packet []byte) error {
	if r == nil || r.closed.Load() {
		return io.ErrClosedPipe
	}
	if len(packet) == 0 {
		return nil
	}
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	originalLen := len(packet)
	fd := int(r.file.Fd())
	pollFDs := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLOUT}}
	deadline := time.Now().Add(probeLocalLinuxTUNWriteTimeout)
	for len(packet) > 0 {
		if r.closed.Load() {
			return io.ErrClosedPipe
		}
		if probeLocalLinuxTUNWriteTimeout > 0 && time.Now().After(deadline) {
			return os.ErrDeadlineExceeded
		}
		n, err := unix.Write(fd, packet)
		if err != nil {
			if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EINTR) {
				pollFDs[0].Revents = 0
				_, _ = unix.Poll(pollFDs, probeLocalLinuxTUNWritePollTimeoutMS)
				continue
			}
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		packet = packet[n:]
	}
	r.txPackets.Add(1)
	r.txBytes.Add(uint64(originalLen))
	return nil
}
