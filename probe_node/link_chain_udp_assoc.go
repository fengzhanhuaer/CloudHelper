package main

import (
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type probeChainUDPAssociation struct {
	key    string
	target string
	conn   *net.UDPConn
	pool   *probeChainUDPAssociationPool

	mu             sync.Mutex
	attached       bool
	refs           atomic.Int32
	lastActiveUnix atomic.Int64
	closeOnce      sync.Once
}

type probeChainUDPAssociationPool struct {
	mu         sync.Mutex
	items      map[string]*probeChainUDPAssociation
	reaperOnce sync.Once
}

var globalProbeChainUDPAssociationPool = newProbeChainUDPAssociationPool()

func newProbeChainUDPAssociationPool() *probeChainUDPAssociationPool {
	p := &probeChainUDPAssociationPool{items: make(map[string]*probeChainUDPAssociation)}
	p.startReaper()
	return p
}

func buildProbeChainUDPAssociationKey(associationID string, target string) string {
	cleanAssociationID := strings.TrimSpace(associationID)
	cleanTarget := strings.ToLower(strings.TrimSpace(target))
	if cleanAssociationID == "" {
		return cleanTarget
	}
	return cleanAssociationID + "|" + cleanTarget
}

func (p *probeChainUDPAssociationPool) startReaper() {
	if p == nil {
		return
	}
	p.reaperOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(probeChainPortForwardSessionGCInterval)
			defer ticker.Stop()
			for range ticker.C {
				p.collectIdle()
			}
		}()
	})
}

func (p *probeChainUDPAssociationPool) Acquire(associationID string, target string) (*probeChainUDPAssociation, error) {
	if p == nil {
		return nil, fmt.Errorf("probe chain udp association pool is nil")
	}
	cleanTarget := strings.TrimSpace(target)
	if cleanTarget == "" {
		return nil, fmt.Errorf("probe chain udp association target is required")
	}
	key := buildProbeChainUDPAssociationKey(associationID, cleanTarget)

	p.mu.Lock()
	if existing, ok := p.items[key]; ok && existing != nil && existing.conn != nil {
		existing.refs.Add(1)
		existing.Touch()
		p.mu.Unlock()
		if err := existing.attach(); err != nil {
			existing.Release()
			return nil, err
		}
		return existing, nil
	}
	p.mu.Unlock()

	udpAddr, err := net.ResolveUDPAddr("udp", cleanTarget)
	if err != nil {
		return nil, err
	}
	conn, err := net.DialUDP("udp", nil, udpAddr)
	if err != nil {
		return nil, err
	}
	assoc := &probeChainUDPAssociation{
		key:    key,
		target: cleanTarget,
		conn:   conn,
		pool:   p,
	}
	assoc.refs.Store(1)
	assoc.Touch()
	if err := assoc.attach(); err != nil {
		_ = conn.Close()
		return nil, err
	}

	p.mu.Lock()
	if existing, ok := p.items[key]; ok && existing != nil && existing.conn != nil {
		existing.refs.Add(1)
		existing.Touch()
		p.mu.Unlock()
		assoc.detach()
		_ = conn.Close()
		if err := existing.attach(); err != nil {
			existing.Release()
			return nil, err
		}
		return existing, nil
	}
	p.items[key] = assoc
	p.mu.Unlock()
	return assoc, nil
}

func (p *probeChainUDPAssociationPool) collectIdle() {
	if p == nil {
		return
	}
	var stale []*probeChainUDPAssociation
	now := time.Now()
	p.mu.Lock()
	for key, assoc := range p.items {
		if assoc == nil {
			delete(p.items, key)
			continue
		}
		if assoc.refs.Load() > 0 {
			continue
		}
		lastActive := assoc.lastActiveUnix.Load()
		if lastActive <= 0 {
			continue
		}
		if now.Sub(time.Unix(lastActive, 0)) >= probeChainPortForwardSessionIdleTTL {
			delete(p.items, key)
			stale = append(stale, assoc)
		}
	}
	p.mu.Unlock()
	for _, assoc := range stale {
		assoc.close()
	}
}

func (a *probeChainUDPAssociation) Touch() {
	if a == nil {
		return
	}
	a.lastActiveUnix.Store(time.Now().Unix())
}

func (a *probeChainUDPAssociation) attach() error {
	if a == nil {
		return fmt.Errorf("probe chain udp association is nil")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.attached {
		return fmt.Errorf("probe chain udp association is busy: %s", a.target)
	}
	a.attached = true
	a.Touch()
	return nil
}

func (a *probeChainUDPAssociation) detach() {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.attached = false
	a.mu.Unlock()
	a.Touch()
}

func (a *probeChainUDPAssociation) Write(payload []byte) error {
	if a == nil || a.conn == nil {
		return fmt.Errorf("probe chain udp association is unavailable")
	}
	if len(payload) == 0 {
		return nil
	}
	a.Touch()
	_, err := a.conn.Write(payload)
	if err == nil {
		a.Touch()
	}
	return err
}

func (a *probeChainUDPAssociation) Read(buffer []byte) (int, error) {
	if a == nil || a.conn == nil {
		return 0, fmt.Errorf("probe chain udp association is unavailable")
	}
	n, err := a.conn.Read(buffer)
	if n > 0 {
		a.Touch()
	}
	return n, err
}

func (a *probeChainUDPAssociation) Release() {
	if a == nil {
		return
	}
	a.detach()
	remaining := a.refs.Add(-1)
	if remaining < 0 {
		a.refs.Store(0)
	}
	a.Touch()
}

func (a *probeChainUDPAssociation) close() {
	if a == nil {
		return
	}
	a.closeOnce.Do(func() {
		if a.conn != nil {
			_ = a.conn.Close()
		}
	})
}
