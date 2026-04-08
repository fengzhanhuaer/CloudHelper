package core

import (
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	tunnelUDPAssociationIdleTTL     = 90 * time.Second
	tunnelUDPAssociationGCInterval  = 15 * time.Second
	tunnelUDPAssociationReadBufSize = 64 * 1024
)

type tunnelUDPAssociation struct {
	key              string
	target           string
	assocKeyV2       string
	flowID           string
	routeGroup       string
	routeNodeID      string
	routeTarget      string
	routeFingerprint string
	conn             *net.UDPConn
	pool             *tunnelUDPAssociationPool

	refs           atomic.Int32
	lastActiveUnix atomic.Int64
	closeOnce      sync.Once
}

type tunnelUDPAssociationPool struct {
	mu         sync.Mutex
	items      map[string]*tunnelUDPAssociation
	reaperOnce sync.Once
}

var globalTunnelUDPAssociationPool = newTunnelUDPAssociationPool()

func newTunnelUDPAssociationPool() *tunnelUDPAssociationPool {
	p := &tunnelUDPAssociationPool{items: make(map[string]*tunnelUDPAssociation)}
	p.startReaper()
	return p
}

func buildTunnelUDPAssociationKey(associationV2 *tunnelAssociationV2Meta, target string) string {
	if associationV2 != nil {
		if assocKey := strings.TrimSpace(associationV2.AssocKeyV2); assocKey != "" {
			return assocKey
		}
	}
	return strings.ToLower(strings.TrimSpace(target))
}

func (p *tunnelUDPAssociationPool) startReaper() {
	if p == nil {
		return
	}
	p.reaperOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(tunnelUDPAssociationEffectiveGCInterval(tunnelUDPAssociationIdleTTL))
			defer ticker.Stop()
			for range ticker.C {
				p.collectIdle()
			}
		}()
	})
}

func tunnelUDPAssociationEffectiveGCInterval(idle time.Duration) time.Duration {
	gcInterval := tunnelUDPAssociationGCInterval
	if half := idle / 2; half > 0 {
		if gcInterval <= 0 || half < gcInterval {
			gcInterval = half
		}
	}
	if gcInterval <= 0 {
		gcInterval = time.Second
	}
	return gcInterval
}

func (p *tunnelUDPAssociationPool) Acquire(associationV2 *tunnelAssociationV2Meta, target string) (*tunnelUDPAssociation, error) {
	if p == nil {
		return nil, fmt.Errorf("udp association pool is nil")
	}
	cleanTarget := strings.TrimSpace(target)
	if cleanTarget == "" {
		return nil, fmt.Errorf("udp association target is required")
	}
	key := buildTunnelUDPAssociationKey(associationV2, cleanTarget)

	p.mu.Lock()
	if existing, ok := p.items[key]; ok && existing != nil && existing.conn != nil {
		existing.refs.Add(1)
		if associationV2 != nil {
			if existing.assocKeyV2 == "" {
				existing.assocKeyV2 = strings.TrimSpace(associationV2.AssocKeyV2)
			}
			if existing.flowID == "" {
				existing.flowID = strings.TrimSpace(associationV2.FlowID)
			}
			if existing.routeGroup == "" {
				existing.routeGroup = strings.TrimSpace(associationV2.RouteGroup)
			}
			if existing.routeNodeID == "" {
				existing.routeNodeID = strings.TrimSpace(associationV2.RouteNodeID)
			}
			if existing.routeTarget == "" {
				existing.routeTarget = strings.TrimSpace(associationV2.RouteTarget)
			}
			if existing.routeFingerprint == "" {
				existing.routeFingerprint = strings.TrimSpace(associationV2.RouteFingerprint)
			}
		}
		existing.Touch()
		p.mu.Unlock()
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
	assoc := &tunnelUDPAssociation{
		key:              key,
		target:           cleanTarget,
		conn:             conn,
		pool:             p,
		assocKeyV2:       cleanTarget,
		flowID:           cleanTarget,
		routeTarget:      cleanTarget,
		routeFingerprint: strings.ToLower(cleanTarget),
	}
	if associationV2 != nil {
		assoc.assocKeyV2 = strings.TrimSpace(associationV2.AssocKeyV2)
		assoc.flowID = strings.TrimSpace(associationV2.FlowID)
		assoc.routeGroup = strings.TrimSpace(associationV2.RouteGroup)
		assoc.routeNodeID = strings.TrimSpace(associationV2.RouteNodeID)
		assoc.routeTarget = strings.TrimSpace(associationV2.RouteTarget)
		assoc.routeFingerprint = strings.TrimSpace(associationV2.RouteFingerprint)
	}
	assoc.refs.Store(1)
	assoc.Touch()

	p.mu.Lock()
	if existing, ok := p.items[key]; ok && existing != nil && existing.conn != nil {
		existing.refs.Add(1)
		existing.Touch()
		p.mu.Unlock()
		_ = conn.Close()
		return existing, nil
	}
	p.items[key] = assoc
	p.mu.Unlock()
	return assoc, nil
}

func (p *tunnelUDPAssociationPool) collectIdle() {
	if p == nil {
		return
	}
	var stale []*tunnelUDPAssociation
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
		if now.Sub(time.Unix(lastActive, 0)) >= tunnelUDPAssociationIdleTTL {
			delete(p.items, key)
			stale = append(stale, assoc)
		}
	}
	p.mu.Unlock()
	for _, assoc := range stale {
		assoc.close()
	}
}

func (a *tunnelUDPAssociation) Touch() {
	if a == nil {
		return
	}
	a.lastActiveUnix.Store(time.Now().Unix())
}


func (a *tunnelUDPAssociation) Write(payload []byte) error {
	if a == nil || a.conn == nil {
		return fmt.Errorf("udp association is unavailable")
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

func (a *tunnelUDPAssociation) Read(buffer []byte) (int, error) {
	if a == nil || a.conn == nil {
		return 0, fmt.Errorf("udp association is unavailable")
	}
	n, err := a.conn.Read(buffer)
	if n > 0 {
		a.Touch()
	}
	return n, err
}

func (a *tunnelUDPAssociation) Release() {
	if a == nil {
		return
	}
	remaining := a.refs.Add(-1)
	if remaining < 0 {
		a.refs.Store(0)
	}
	a.Touch()
}

func (a *tunnelUDPAssociation) close() {
	if a == nil {
		return
	}
	a.closeOnce.Do(func() {
		if a.conn != nil {
			_ = a.conn.Close()
		}
	})
}
