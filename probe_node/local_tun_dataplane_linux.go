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

const probeLocalLinuxTUNPacketBufferSize = 65535

var probeLocalLinuxTUNDataPlaneState = struct {
	mu     sync.Mutex
	runner probeLocalTUNDataPlane
	dev    string
}{}

var probeLocalLinuxNewTUNDataPlaneRunner func(dev string) (probeLocalTUNDataPlane, error)

type probeLocalLinuxTUNDataPlaneRunner struct {
	file *os.File
	dev  string

	writeMu sync.Mutex
	closed  atomic.Bool

	rxPackets atomic.Uint64
	rxBytes   atomic.Uint64
	txPackets atomic.Uint64
	txBytes   atomic.Uint64
	doneCh    chan struct{}
}

func startProbeLocalTUNDataPlane() error {
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
	runner, err := currentProbeLocalLinuxNewTUNDataPlaneRunner()(dev)
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

func stopProbeLocalTUNDataPlane() error {
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

func probeLocalTUNDataPlaneStatsSnapshot() probeLocalTUNDataPlaneStats {
	probeLocalLinuxTUNDataPlaneState.mu.Lock()
	runner := probeLocalLinuxTUNDataPlaneState.runner
	probeLocalLinuxTUNDataPlaneState.mu.Unlock()
	if runner == nil {
		return probeLocalTUNDataPlaneStats{}
	}
	return runner.Stats()
}

func probeLocalTUNDataPlaneRunning() bool {
	return probeLocalTUNDataPlaneStatsSnapshot().Running
}

func writeProbeLocalTUNPacket(packet []byte) error {
	probeLocalLinuxTUNDataPlaneState.mu.Lock()
	runner := probeLocalLinuxTUNDataPlaneState.runner
	probeLocalLinuxTUNDataPlaneState.mu.Unlock()
	if runner == nil {
		return errors.New("probe local linux tun data plane is not running")
	}
	return runner.WritePacket(packet)
}

func handleProbeLocalTUNInboundPacket(packet []byte) {
	if handleProbeVirtualRouterTUNPacket(packet) {
		return
	}
}

func resetProbeLocalTUNDataPlaneHooksForTest() {
	probeLocalLinuxNewTUNDataPlaneRunner = nil
	_ = stopProbeLocalTUNDataPlane()
}

func currentProbeLocalLinuxNewTUNDataPlaneRunner() func(dev string) (probeLocalTUNDataPlane, error) {
	if probeLocalLinuxNewTUNDataPlaneRunner != nil {
		return probeLocalLinuxNewTUNDataPlaneRunner
	}
	return func(dev string) (probeLocalTUNDataPlane, error) {
		return newProbeLocalLinuxTUNDataPlaneRunner(dev)
	}
}

func newProbeLocalLinuxTUNDataPlaneRunner(dev string) (*probeLocalLinuxTUNDataPlaneRunner, error) {
	file, err := openProbeLocalLinuxTUNDevice(dev)
	if err != nil {
		return nil, err
	}
	runner := &probeLocalLinuxTUNDataPlaneRunner{
		file:   file,
		dev:    dev,
		doneCh: make(chan struct{}),
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

func (r *probeLocalLinuxTUNDataPlaneRunner) readLoop() {
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
		handleProbeLocalTUNInboundPacket(packet)
	}
}

func (r *probeLocalLinuxTUNDataPlaneRunner) Close() error {
	if r == nil {
		return nil
	}
	if r.closed.CompareAndSwap(false, true) {
		err := r.file.Close()
		<-r.doneCh
		return err
	}
	return nil
}

func (r *probeLocalLinuxTUNDataPlaneRunner) Stats() probeLocalTUNDataPlaneStats {
	if r == nil {
		return probeLocalTUNDataPlaneStats{}
	}
	return probeLocalTUNDataPlaneStats{
		Running:   !r.closed.Load(),
		RXPackets: r.rxPackets.Load(),
		RXBytes:   r.rxBytes.Load(),
		TXPackets: r.txPackets.Load(),
		TXBytes:   r.txBytes.Load(),
	}
}

func (r *probeLocalLinuxTUNDataPlaneRunner) WritePacket(packet []byte) error {
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
	for len(packet) > 0 {
		n, err := unix.Write(fd, packet)
		if err != nil {
			if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EINTR) {
				pollFDs[0].Revents = 0
				_, _ = unix.Poll(pollFDs, 250)
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
