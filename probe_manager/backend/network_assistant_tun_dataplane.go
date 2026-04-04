package backend

import (
	"errors"
	"fmt"
	"strings"
)

type localTUNDataPlaneStats struct {
	Running   bool
	RXPackets uint64
	RXBytes   uint64
}

type localTUNPacketStack interface {
	Write([]byte) (int, error)
	Close() error
}

type localTUNUDPHandlerCloser interface {
	CloseAll()
}

type localTUNDataPlane interface {
	Close() error
	Stats() localTUNDataPlaneStats
	WritePacket(packet []byte) error
}

func (s *networkAssistantService) startLocalTUNDataPlane() error {
	s.mu.Lock()
	if s.tunDataPlane != nil {
		s.mu.Unlock()
		return nil
	}
	libraryPath := strings.TrimSpace(s.tunLibraryPath)
	adapterHandle := s.tunAdapterHandle
	s.mu.Unlock()

	if libraryPath == "" {
		return errors.New("tun library path is empty")
	}
	if adapterHandle == 0 {
		handle, err := createConfiguredTUNAdapter(libraryPath, tunAdapterName, tunAdapterTunnelType, tunAdapterRequestedGUID)
		if err != nil {
			return err
		}
		adapterHandle = handle
		s.mu.Lock()
		if s.tunAdapterHandle == 0 {
			s.tunAdapterHandle = handle
		} else if handle != 0 && s.tunAdapterHandle != handle {
			_ = closeConfiguredTUNAdapter(libraryPath, handle)
		}
		adapterHandle = s.tunAdapterHandle
		s.mu.Unlock()
	}
	if adapterHandle == 0 {
		return errors.New("tun adapter handle is empty")
	}

	dataPlane, err := newLocalTUNDataPlaneRunner(
		libraryPath,
		adapterHandle,
		s.handleLocalTUNPacket,
		func(format string, args ...any) {
			s.logf(format, args...)
		})
	if err != nil {
		return err
	}

	s.mu.Lock()
	if s.tunDataPlane != nil {
		s.mu.Unlock()
		_ = dataPlane.Close()
		return nil
	}
	s.tunDataPlane = dataPlane
	s.mu.Unlock()

	if err := s.startLocalTUNPacketStack(); err != nil {
		_ = dataPlane.Close()
		s.mu.Lock()
		if s.tunDataPlane == dataPlane {
			s.tunDataPlane = nil
		}
		s.mu.Unlock()
		return err
	}

	stats := dataPlane.Stats()
	s.logf("local tun data plane started: running=%v rx_packets=%d rx_bytes=%d", stats.Running, stats.RXPackets, stats.RXBytes)
	return nil
}

func (s *networkAssistantService) stopLocalTUNDataPlane() error {
	errStack := s.stopLocalTUNPacketStack()
	s.closeAllLocalTUNUDPRelays()

	s.mu.Lock()
	dataPlane := s.tunDataPlane
	s.tunDataPlane = nil
	s.mu.Unlock()

	if dataPlane == nil {
		return errStack
	}
	stats := dataPlane.Stats()
	err := dataPlane.Close()
	if err != nil || errStack != nil {
		return errors.Join(errStack, fmt.Errorf("close tun data plane: %w", err))
	}
	s.logf("local tun data plane stopped: rx_packets=%d rx_bytes=%d", stats.RXPackets, stats.RXBytes)
	return errStack
}
