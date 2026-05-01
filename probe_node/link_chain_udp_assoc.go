package main

import (
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	probeChainUDPAssociationMinIdleTTL    = 30 * time.Second
	probeChainUDPAssociationMaxIdleTTL    = 15 * time.Minute
	probeChainUDPAssociationMinGCInterval = 10 * time.Second
	probeChainUDPAssociationMaxGCInterval = 2 * time.Minute

	probeChainUDPAssociationTTLProfileDNSFast    = "profile_dns_fast"
	probeChainUDPAssociationTTLProfileDefault    = "profile_udp_default"
	probeChainUDPAssociationTTLProfileQUICWarm   = "profile_quic_warm"
	probeChainUDPAssociationTTLProfileQUICStable = "profile_quic_stable"
	probeChainUDPAssociationTTLProfileQUICLong   = "profile_quic_long"

	probeChainUDPAssociationNATModeDefault = "preserve_src_port"
)

type probeChainUDPAssociation struct {
	key              string
	target           string
	assocKeyV2       string
	flowID           string
	sourceKey        string
	sourceRefs       int64
	routeGroup       string
	routeNodeID      string
	routeTarget      string
	routeFingerprint string
	natMode          string
	ttlProfile       string
	idleTimeout      time.Duration
	gcInterval       time.Duration
	createdAtUnixMS  int64
	conn             *net.UDPConn
	pool             *probeChainUDPAssociationPool

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

func buildProbeChainUDPAssociationKey(associationV2 *probeChainAssociationV2Meta, target string) string {
	if associationV2 != nil {
		if assocKey := strings.TrimSpace(associationV2.AssocKeyV2); assocKey != "" {
			return assocKey
		}
	}
	return strings.ToLower(strings.TrimSpace(target))
}

func (p *probeChainUDPAssociationPool) startReaper() {
	if p == nil {
		return
	}
	p.reaperOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(probeChainUDPAssociationEffectiveGCInterval(probeChainPortForwardSessionIdleTTL))
			defer ticker.Stop()
			for range ticker.C {
				p.collectIdle()
			}
		}()
	})
}

func probeChainUDPAssociationEffectiveGCInterval(idle time.Duration) time.Duration {
	gcInterval := probeChainPortForwardSessionGCInterval
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

func normalizeProbeChainUDPAssociationDurationMS(rawMS int64, fallback, minValue, maxValue time.Duration) time.Duration {
	if rawMS <= 0 {
		return fallback
	}
	candidate := time.Duration(rawMS) * time.Millisecond
	if minValue > 0 && candidate < minValue {
		candidate = minValue
	}
	if maxValue > 0 && candidate > maxValue {
		candidate = maxValue
	}
	return candidate
}

func resolveProbeChainUDPAssociationPolicy(associationV2 *probeChainAssociationV2Meta) (ttlProfile string, idleTTL time.Duration, gcInterval time.Duration, natMode string, createdAtUnixMS int64) {
	ttlProfile = probeChainUDPAssociationTTLProfileDefault
	idleTTL = probeChainPortForwardSessionIdleTTL
	gcInterval = probeChainPortForwardSessionGCInterval
	natMode = probeChainUDPAssociationNATModeDefault
	createdAtUnixMS = time.Now().UnixMilli()
	if associationV2 != nil {
		if profile := strings.TrimSpace(associationV2.TTLProfile); profile != "" {
			ttlProfile = profile
		}
		idleTTL = normalizeProbeChainUDPAssociationDurationMS(
			associationV2.IdleTimeoutMS,
			probeChainPortForwardSessionIdleTTL,
			probeChainUDPAssociationMinIdleTTL,
			probeChainUDPAssociationMaxIdleTTL,
		)
		gcInterval = normalizeProbeChainUDPAssociationDurationMS(
			associationV2.GCIntervalMS,
			probeChainPortForwardSessionGCInterval,
			probeChainUDPAssociationMinGCInterval,
			probeChainUDPAssociationMaxGCInterval,
		)
		if mode := strings.TrimSpace(associationV2.NATMode); mode != "" {
			natMode = mode
		}
		if associationV2.CreatedAtUnixMS > 0 {
			createdAtUnixMS = associationV2.CreatedAtUnixMS
		}
	}
	if half := idleTTL / 2; half > 0 && gcInterval > half {
		gcInterval = half
	}
	if gcInterval <= 0 {
		gcInterval = time.Second
	}
	return ttlProfile, idleTTL, gcInterval, natMode, createdAtUnixMS
}

func (p *probeChainUDPAssociationPool) Acquire(associationV2 *probeChainAssociationV2Meta, target string) (*probeChainUDPAssociation, error) {
	if p == nil {
		return nil, fmt.Errorf("probe chain udp association pool is nil")
	}
	cleanTarget := strings.TrimSpace(target)
	if cleanTarget == "" {
		return nil, fmt.Errorf("probe chain udp association target is required")
	}
	key := buildProbeChainUDPAssociationKey(associationV2, cleanTarget)
	ttlProfile, idleTTL, gcInterval, natMode, createdAtUnixMS := resolveProbeChainUDPAssociationPolicy(associationV2)

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
			if existing.sourceKey == "" {
				existing.sourceKey = strings.TrimSpace(associationV2.SourceKey)
			}
			if existing.sourceRefs <= 0 && associationV2.SourceRefs > 0 {
				existing.sourceRefs = associationV2.SourceRefs
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
		if existing.ttlProfile == "" {
			existing.ttlProfile = ttlProfile
		}
		if existing.idleTimeout <= 0 {
			existing.idleTimeout = idleTTL
		}
		if existing.gcInterval <= 0 {
			existing.gcInterval = gcInterval
		}
		if existing.natMode == "" {
			existing.natMode = natMode
		}
		if existing.createdAtUnixMS <= 0 {
			existing.createdAtUnixMS = createdAtUnixMS
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
	assoc := &probeChainUDPAssociation{
		key:              key,
		target:           cleanTarget,
		conn:             conn,
		pool:             p,
		assocKeyV2:       cleanTarget,
		flowID:           cleanTarget,
		routeTarget:      cleanTarget,
		routeFingerprint: strings.ToLower(cleanTarget),
		natMode:          natMode,
		ttlProfile:       ttlProfile,
		idleTimeout:      idleTTL,
		gcInterval:       gcInterval,
		createdAtUnixMS:  createdAtUnixMS,
	}
	if associationV2 != nil {
		assoc.assocKeyV2 = strings.TrimSpace(associationV2.AssocKeyV2)
		assoc.flowID = strings.TrimSpace(associationV2.FlowID)
		assoc.sourceKey = strings.TrimSpace(associationV2.SourceKey)
		assoc.sourceRefs = associationV2.SourceRefs
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
		idleTTL := assoc.idleTimeout
		if idleTTL <= 0 {
			idleTTL = probeChainPortForwardSessionIdleTTL
		}
		if now.Sub(time.Unix(lastActive, 0)) >= idleTTL {
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
