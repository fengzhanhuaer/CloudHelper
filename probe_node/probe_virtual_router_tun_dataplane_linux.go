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
	probeLocalLinuxTUNPacketBufferSize   = 65535
	probeLocalLinuxTUNInboundQueueFrames = 2048
	probeLocalLinuxTUNInboundWorkerCount = 4
	probeLocalLinuxTUNWriteTimeout       = 500 * time.Millisecond
	probeLocalLinuxTUNWritePollTimeoutMS = 50
	probeLocalLinuxTUNCloseWaitTimeout   = 2 * time.Second
)

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

	inboundCh chan []byte
	stopCh    chan struct{}
	rxPackets atomic.Uint64
	rxBytes   atomic.Uint64
	txPackets atomic.Uint64
	txBytes   atomic.Uint64
	doneCh    chan struct{}
	closeOnce sync.Once
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
		file:      file,
		dev:       dev,
		inboundCh: make(chan []byte, probeLocalLinuxTUNInboundQueueFrames),
		stopCh:    make(chan struct{}),
		doneCh:    make(chan struct{}),
	}
	for i := 0; i < probeLocalLinuxTUNInboundWorkerCount; i++ {
		go runner.inboundWorker()
	}
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
	select {
	case r.inboundCh <- packet:
	case <-r.stopCh:
	default:
		logProbeWarnf("probe local linux tun inbound packet drop: dev=%s reason=handler_queue_full depth=%d capacity=%d", r.dev, len(r.inboundCh), cap(r.inboundCh))
	}
}

func (r *probeVirtualRouterLinuxTUNDataPlaneRunner) inboundWorker() {
	for {
		select {
		case <-r.stopCh:
			return
		case packet := <-r.inboundCh:
			if len(packet) == 0 {
				continue
			}
			handleProbeVirtualRouterTUNInboundPacket(packet)
		}
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
		closeErr = r.file.Close()
		select {
		case <-r.doneCh:
		case <-time.After(probeLocalLinuxTUNCloseWaitTimeout):
			logProbeWarnf("probe local linux tun data plane close wait timeout: dev=%s", r.dev)
		}
	})
	return closeErr
}

func (r *probeVirtualRouterLinuxTUNDataPlaneRunner) Stats() probeVirtualRouterTUNDataPlaneStats {
	if r == nil {
		return probeVirtualRouterTUNDataPlaneStats{}
	}
	inboundDepth, inboundCapacity := 0, 0
	if r.inboundCh != nil {
		inboundDepth = len(r.inboundCh)
		inboundCapacity = cap(r.inboundCh)
	}
	return probeVirtualRouterTUNDataPlaneStats{
		Running:              !r.closed.Load(),
		RXPackets:            r.rxPackets.Load(),
		RXBytes:              r.rxBytes.Load(),
		TXPackets:            r.txPackets.Load(),
		TXBytes:              r.txBytes.Load(),
		InboundQueueDepth:    inboundDepth,
		InboundQueueCapacity: inboundCapacity,
	}
}

func (r *probeVirtualRouterLinuxTUNDataPlaneRunner) WritePacket(packet []byte) error {
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
