package mobilecore

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/dns/dnsmessage"
	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"
)

const (
	vpnNICID            = tcpip.NICID(1)
	vpnQueueSize        = 4096
	vpnMTU              = 1500
	vpnTCPWindow        = 1024
	vpnTCPInFlight      = 512
	vpnRelayIdle        = 5 * time.Minute
	vpnUDPRelayTimeout  = 30 * time.Second
	vpnDNSCacheTTL      = 10 * time.Minute
	vpnDNSPersistDelay  = 10 * time.Second
	vpnDNSReadTimeout   = 20 * time.Second
	vpnDNSLookupTimeout = 5 * time.Second
	vpnTunWriteTimeout  = 5 * time.Second
	vpnDNSCacheFileName = "android_vpn_dns_cache.json"
)

var (
	vpnIPv4GatewayAddress = tcpip.AddrFrom4([4]byte{10, 111, 0, 1})
	vpnIPv4Address        = tcpip.AddrFrom4([4]byte{10, 111, 0, 2})
)

var vpnRuntime = &androidVPNRuntime{}
var vpnDNSState = &androidVPNDNSState{
	nextFakeOffset: 2,
	fakeDomainToIP: map[string]string{},
	fakeIPToEntry:  map[string]androidVPNDNSFakeEntry{},
	routeIPHints:   map[string]androidVPNDNSRouteHintEntry{},
	realIPToFake:   map[string]androidVPNDNSRealIPFakeEntry{},
	fakeFlowToReal: map[string]androidVPNDNSRealIPFakeEntry{},
}

type androidVPNRuntime struct {
	mu        sync.Mutex
	configDir string
	tun       *os.File
	stack     *androidVPNDataPlane
	status    string
	lastError string
	updatedAt string
	selfCheck map[string]any
}

type androidVPNDataPlane struct {
	stack      *stack.Stack
	linkEP     *channel.Endpoint
	tun        *os.File
	cancel     context.CancelFunc
	doneCh     chan struct{}
	tunWriteMu sync.Mutex
	closeOnce  sync.Once
	closed     atomic.Bool
}

type vpnRouteDecision struct {
	Direct          bool
	Reject          bool
	TargetAddr      string
	Group           string
	SelectedRouteID string
}

type androidVPNDNSState struct {
	mu             sync.Mutex
	nextFakeOffset uint32
	fakeDomainToIP map[string]string
	fakeIPToEntry  map[string]androidVPNDNSFakeEntry
	routeIPHints   map[string]androidVPNDNSRouteHintEntry
	realIPToFake   map[string]androidVPNDNSRealIPFakeEntry
	fakeFlowToReal map[string]androidVPNDNSRealIPFakeEntry
	cacheDir       string
	cacheLoaded    bool
	cacheDirty     bool
	cacheTimer     *time.Timer
}

type androidVPNDNSFakeEntry struct {
	Domain            string
	Group             string
	Direct            bool
	Reject            bool
	SelectedRouteID   string
	ControllerManaged bool
	ExpiresAt         time.Time
}

type androidVPNDNSRouteHintEntry struct {
	Domain    string
	IP        string
	IPv4      []string
	IPv6      []string
	Group     string
	ExpiresAt time.Time
}

type androidVPNDNSRealIPFakeEntry struct {
	Domain          string
	RealIP          string
	FakeIP          string
	Port            string
	Group           string
	SelectedRouteID string
	Protocol        uint8
	ExpiresAt       time.Time
}

type androidVPNIPv4TransportInfo struct {
	Protocol        uint8
	SourceIP        string
	DestinationIP   string
	SourcePort      uint16
	DestinationPort uint16
	HeaderLength    int
	TotalLength     int
}

type androidVPNDNSPersistFile struct {
	Version        int                              `json:"version"`
	SavedAt        time.Time                        `json:"saved_at"`
	NextFakeOffset uint32                           `json:"next_fake_offset,omitempty"`
	FakeIPs        []androidVPNDNSPersistFakeEntry  `json:"fake_ips,omitempty"`
	RouteHints     []androidVPNDNSPersistRouteEntry `json:"route_hints,omitempty"`
}

type androidVPNDNSPersistFakeEntry struct {
	IP                string    `json:"ip"`
	Domain            string    `json:"domain"`
	Group             string    `json:"group,omitempty"`
	Direct            bool      `json:"direct,omitempty"`
	Reject            bool      `json:"reject,omitempty"`
	SelectedRouteID   string    `json:"selected_route_id,omitempty"`
	ControllerManaged bool      `json:"controller_managed,omitempty"`
	ExpiresAt         time.Time `json:"expires_at"`
}

type androidVPNDNSPersistRouteEntry struct {
	IP        string    `json:"ip"`
	Domain    string    `json:"domain"`
	IPv4      []string  `json:"ipv4,omitempty"`
	IPv6      []string  `json:"ipv6,omitempty"`
	Group     string    `json:"group,omitempty"`
	ExpiresAt time.Time `json:"expires_at"`
}

func currentAndroidVPNConfigDir() string {
	vpnRuntime.mu.Lock()
	dir := strings.TrimSpace(vpnRuntime.configDir)
	vpnRuntime.mu.Unlock()
	return normalizeMobileConfigDir(dir)
}

func resolveAndroidVPNDNSCachePath(configDir string) (string, bool) {
	dir := strings.TrimSpace(configDir)
	if dir == "" {
		return "", false
	}
	return filepath.Join(dir, vpnDNSCacheFileName), true
}

func ensureAndroidVPNDNSCacheLoaded(configDir string) {
	dir := strings.TrimSpace(configDir)
	if dir == "" {
		return
	}
	vpnDNSState.mu.Lock()
	if vpnDNSState.cacheLoaded && strings.EqualFold(strings.TrimSpace(vpnDNSState.cacheDir), dir) {
		vpnDNSState.mu.Unlock()
		return
	}
	vpnDNSState.mu.Unlock()

	payload := loadAndroidVPNDNSCacheFile(dir)
	now := time.Now().UTC()
	vpnDNSState.mu.Lock()
	defer vpnDNSState.mu.Unlock()
	if vpnDNSState.cacheLoaded && strings.EqualFold(strings.TrimSpace(vpnDNSState.cacheDir), dir) {
		return
	}
	keepRuntime := strings.TrimSpace(vpnDNSState.cacheDir) == "" && (len(vpnDNSState.fakeIPToEntry) > 0 || len(vpnDNSState.routeIPHints) > 0 || len(vpnDNSState.realIPToFake) > 0 || len(vpnDNSState.fakeFlowToReal) > 0)
	vpnDNSState.cacheDir = dir
	vpnDNSState.cacheLoaded = true
	if vpnDNSState.fakeDomainToIP == nil || !keepRuntime {
		vpnDNSState.fakeDomainToIP = map[string]string{}
	}
	if vpnDNSState.fakeIPToEntry == nil || !keepRuntime {
		vpnDNSState.fakeIPToEntry = map[string]androidVPNDNSFakeEntry{}
	}
	if vpnDNSState.routeIPHints == nil || !keepRuntime {
		vpnDNSState.routeIPHints = map[string]androidVPNDNSRouteHintEntry{}
	}
	if vpnDNSState.realIPToFake == nil || !keepRuntime {
		vpnDNSState.realIPToFake = map[string]androidVPNDNSRealIPFakeEntry{}
	}
	if vpnDNSState.fakeFlowToReal == nil || !keepRuntime {
		vpnDNSState.fakeFlowToReal = map[string]androidVPNDNSRealIPFakeEntry{}
	}
	if payload.NextFakeOffset > vpnDNSState.nextFakeOffset || !keepRuntime {
		vpnDNSState.nextFakeOffset = payload.NextFakeOffset
	}
	if vpnDNSState.nextFakeOffset < 2 {
		vpnDNSState.nextFakeOffset = 2
	}
	for _, item := range payload.FakeIPs {
		ip := net.ParseIP(strings.TrimSpace(strings.Trim(item.IP, "[]")))
		domain := strings.TrimSpace(strings.ToLower(strings.Trim(item.Domain, ".")))
		if ip == nil || ip.To4() == nil || domain == "" || item.ExpiresAt.IsZero() || now.After(item.ExpiresAt) {
			continue
		}
		ipText := ip.To4().String()
		vpnDNSState.fakeDomainToIP[domain] = ipText
		vpnDNSState.fakeIPToEntry[ipText] = androidVPNDNSFakeEntry{
			Domain:            domain,
			Group:             strings.TrimSpace(item.Group),
			Direct:            item.Direct,
			Reject:            item.Reject,
			SelectedRouteID:   strings.TrimSpace(item.SelectedRouteID),
			ControllerManaged: item.ControllerManaged,
			ExpiresAt:         item.ExpiresAt,
		}
	}
	for _, item := range payload.RouteHints {
		ip := net.ParseIP(strings.TrimSpace(strings.Trim(item.IP, "[]")))
		domain := strings.TrimSpace(strings.ToLower(strings.Trim(item.Domain, ".")))
		if ip == nil || domain == "" || item.ExpiresAt.IsZero() || now.After(item.ExpiresAt) {
			continue
		}
		ipText := ip.String()
		vpnDNSState.routeIPHints[ipText] = androidVPNDNSRouteHintEntry{
			Domain:    domain,
			IP:        ipText,
			IPv4:      append([]string(nil), item.IPv4...),
			IPv6:      append([]string(nil), item.IPv6...),
			Group:     strings.TrimSpace(item.Group),
			ExpiresAt: item.ExpiresAt,
		}
	}
}

func loadAndroidVPNDNSCacheFile(configDir string) androidVPNDNSPersistFile {
	path, ok := resolveAndroidVPNDNSCachePath(configDir)
	if !ok {
		return androidVPNDNSPersistFile{}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			androidLogStore.add("vpn", "warn", "dns cache load failed: "+err.Error())
		}
		return androidVPNDNSPersistFile{}
	}
	var payload androidVPNDNSPersistFile
	if err := json.Unmarshal(raw, &payload); err != nil {
		androidLogStore.add("vpn", "warn", "dns cache decode failed: "+err.Error())
		return androidVPNDNSPersistFile{}
	}
	return payload
}

func markAndroidVPNDNSCacheDirty(configDir string) {
	markAndroidVPNDNSCacheDirtyForState(configDir, vpnDNSState)
}

func markAndroidVPNDNSCacheDirtyForState(configDir string, state *androidVPNDNSState) {
	dir := strings.TrimSpace(configDir)
	if dir == "" || state == nil {
		return
	}
	state.mu.Lock()
	state.cacheDirty = true
	if strings.TrimSpace(state.cacheDir) == "" {
		state.cacheDir = dir
	}
	if state.cacheTimer != nil {
		state.cacheTimer.Stop()
	}
	state.cacheTimer = time.AfterFunc(vpnDNSPersistDelay, func() {
		persistAndroidVPNDNSCacheForState(dir, state)
	})
	state.mu.Unlock()
}

func persistAndroidVPNDNSCache(configDir string) {
	persistAndroidVPNDNSCacheForState(configDir, vpnDNSState)
}

func persistAndroidVPNDNSCacheForState(configDir string, state *androidVPNDNSState) {
	dir := strings.TrimSpace(configDir)
	if dir == "" || state == nil {
		return
	}
	now := time.Now().UTC()
	state.mu.Lock()
	pruneAndroidVPNDNSStateLocked(state, now)
	if !state.cacheDirty {
		state.mu.Unlock()
		return
	}
	if state.cacheTimer != nil {
		state.cacheTimer.Stop()
		state.cacheTimer = nil
	}
	payload := androidVPNDNSPersistFile{
		Version:        1,
		SavedAt:        now,
		NextFakeOffset: state.nextFakeOffset,
		FakeIPs:        make([]androidVPNDNSPersistFakeEntry, 0, len(state.fakeIPToEntry)),
		RouteHints:     make([]androidVPNDNSPersistRouteEntry, 0, len(state.routeIPHints)),
	}
	for ip, entry := range state.fakeIPToEntry {
		payload.FakeIPs = append(payload.FakeIPs, androidVPNDNSPersistFakeEntry{
			IP:                ip,
			Domain:            entry.Domain,
			Group:             entry.Group,
			Direct:            entry.Direct,
			Reject:            entry.Reject,
			SelectedRouteID:   entry.SelectedRouteID,
			ControllerManaged: entry.ControllerManaged,
			ExpiresAt:         entry.ExpiresAt,
		})
	}
	for ip, entry := range state.routeIPHints {
		payload.RouteHints = append(payload.RouteHints, androidVPNDNSPersistRouteEntry{
			IP:        ip,
			Domain:    entry.Domain,
			IPv4:      append([]string(nil), entry.IPv4...),
			IPv6:      append([]string(nil), entry.IPv6...),
			Group:     entry.Group,
			ExpiresAt: entry.ExpiresAt,
		})
	}
	state.cacheDirty = false
	state.mu.Unlock()

	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		androidLogStore.add("vpn", "warn", "dns cache encode failed: "+err.Error())
		markAndroidVPNDNSCacheDirtyForState(dir, state)
		return
	}
	path, ok := resolveAndroidVPNDNSCachePath(dir)
	if !ok {
		markAndroidVPNDNSCacheDirtyForState(dir, state)
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		androidLogStore.add("vpn", "warn", "dns cache mkdir failed: "+err.Error())
		markAndroidVPNDNSCacheDirtyForState(dir, state)
		return
	}
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, raw, 0o644); err != nil {
		androidLogStore.add("vpn", "warn", "dns cache write failed: "+err.Error())
		markAndroidVPNDNSCacheDirtyForState(dir, state)
		return
	}
	if err := os.Rename(tmpPath, path); err != nil {
		androidLogStore.add("vpn", "warn", "dns cache rename failed: "+err.Error())
		markAndroidVPNDNSCacheDirtyForState(dir, state)
	}
}

// VpnStart attaches a VpnService TUN fd to the mobilecore data plane.
func VpnStart(fd int64, configDir string) string {
	androidLogStore.add("vpn", "info", "vpn start attach requested")
	if fd < 0 {
		return "vpn start failed: invalid tun fd"
	}
	configDir = normalizeMobileConfigDir(configDir)
	tun := os.NewFile(uintptr(fd), "cloudhelper-vpn-tun")
	if tun == nil {
		return "vpn start failed: open tun fd failed"
	}
	androidLogStore.add("vpn", "info", "vpn tun fd opened")
	dataPlane, err := newandroidVPNDataPlane(tun)
	if err != nil {
		_ = tun.Close()
		return "vpn start failed: " + err.Error()
	}
	androidLogStore.add("vpn", "info", "vpn data plane created")
	vpnRuntime.mu.Lock()
	oldStack := vpnRuntime.stack
	oldTun := vpnRuntime.tun
	vpnRuntime.configDir = strings.TrimSpace(configDir)
	vpnRuntime.tun = tun
	vpnRuntime.stack = dataPlane
	vpnRuntime.status = "running"
	vpnRuntime.lastError = ""
	vpnRuntime.updatedAt = time.Now().UTC().Format(time.RFC3339)
	vpnRuntime.selfCheck = map[string]any{"ok": false, "status": "pending", "updated_at": vpnRuntime.updatedAt}
	vpnRuntime.mu.Unlock()
	setMobileRouteConfigDir(configDir)
	startMobileVRouteCarrierWorkersFromConfigDir(strings.TrimSpace(configDir))
	go cleanupPreviousAndroidVPNDataPlane(oldStack, oldTun)
	go ensureAndroidVPNDNSCacheLoaded(strings.TrimSpace(configDir))
	go runVPNStartupSelfCheck(strings.TrimSpace(configDir))
	androidLogStore.add("vpn", "info", "vpn data plane running")
	return "vpn running"
}

func cleanupPreviousAndroidVPNDataPlane(oldStack *androidVPNDataPlane, oldTun *os.File) {
	if oldTun != nil {
		_ = oldTun.Close()
	}
	if oldStack != nil {
		_ = oldStack.Close()
	}
}

func VpnStop() string {
	vpnRuntime.mu.Lock()
	dataPlane := vpnRuntime.stack
	tun := vpnRuntime.tun
	configDir := strings.TrimSpace(vpnRuntime.configDir)
	vpnRuntime.stack = nil
	vpnRuntime.tun = nil
	vpnRuntime.status = "stopped"
	vpnRuntime.updatedAt = time.Now().UTC().Format(time.RFC3339)
	vpnRuntime.selfCheck = map[string]any{"ok": false, "status": "stopped", "updated_at": vpnRuntime.updatedAt}
	vpnRuntime.mu.Unlock()
	if dataPlane != nil {
		_ = dataPlane.Close()
	}
	stopMobileVRouteCarrierWorkers()
	closeMobileVRouteCarriers()
	closeMobileVRouteTrackedFlows("vpn_stopped")
	if tun != nil {
		_ = tun.Close()
	}
	persistAndroidVPNDNSCache(configDir)
	return "vpn stopped"
}

func VpnStatus() string {
	cleanupMobileVRouteTrackedFlows()
	vpnRuntime.mu.Lock()
	selfCheck := cloneVPNMap(vpnRuntime.selfCheck)
	running := vpnRuntime.stack != nil
	status := firstNonEmptyString(vpnRuntime.status, "stopped")
	lastError := vpnRuntime.lastError
	updatedAt := vpnRuntime.updatedAt
	vpnRuntime.mu.Unlock()
	dnsStatus := snapshotAndroidVPNDNSStatus()
	connections := globalandroidRouteConnectionState.snapshot()
	configDir := currentAndroidVPNConfigDir()
	return marshalRouteJSON(map[string]any{
		"ok":          true,
		"running":     running,
		"status":      status,
		"last_error":  lastError,
		"updated_at":  updatedAt,
		"dns":         dnsStatus,
		"connections": connections,
		"vroute":      mobileVRouteRuntimeStatusPayload(configDir),
		"self_check":  selfCheck,
	})
}

func VpnSelfCheck(configDir string) string {
	result := runAndroidVPNSelfCheck(strings.TrimSpace(configDir))
	setVPNSelfCheckResult(result)
	return marshalRouteJSON(result)
}

func runVPNStartupSelfCheck(configDir string) {
	result := runAndroidVPNSelfCheck(configDir)
	setVPNSelfCheckResult(result)
	level := "info"
	if ok, _ := result["ok"].(bool); !ok {
		level = "warn"
	}
	androidLogStore.add("vpn", level, "startup self-check: "+firstNonEmptyString(stringFromAny(result["status"]), stringFromAny(result["error"]), "unknown"))
}

func runAndroidVPNSelfCheck(configDir string) map[string]any {
	startedAt := time.Now().UTC()
	result := map[string]any{
		"ok":         false,
		"status":     "running",
		"target":     "www.google.com:443",
		"started_at": startedAt.Format(time.RFC3339),
	}
	if strings.TrimSpace(configDir) == "" {
		vpnRuntime.mu.Lock()
		configDir = vpnRuntime.configDir
		vpnRuntime.mu.Unlock()
	}
	if strings.TrimSpace(configDir) == "" {
		result["status"] = "config_missing"
		result["error"] = "config dir is empty"
		result["updated_at"] = time.Now().UTC().Format(time.RFC3339)
		return result
	}
	vpnRuntime.mu.Lock()
	vpnRuntime.configDir = strings.TrimSpace(configDir)
	vpnRuntime.mu.Unlock()
	setMobileRouteConfigDir(configDir)
	result["vroute"] = mobileVRouteRuntimeStatusPayload(configDir)
	query, err := buildAndroidVPNDNSQuery("www.google.com", dnsmessage.TypeA)
	if err != nil {
		result["status"] = "dns_query_build_failed"
		result["error"] = err.Error()
		result["updated_at"] = time.Now().UTC().Format(time.RFC3339)
		return result
	}
	response, err := resolveAndroidVPNDNSPacket(query)
	if err != nil {
		result["status"] = "dns_failed"
		result["error"] = err.Error()
		result["updated_at"] = time.Now().UTC().Format(time.RFC3339)
		return result
	}
	ips := extractAndroidVPNDNSResponseIPs(response)
	result["dns_ips"] = ips
	if len(ips) == 0 {
		result["status"] = "dns_empty"
		result["error"] = "dns response has no A record"
		result["updated_at"] = time.Now().UTC().Format(time.RFC3339)
		return result
	}
	route, err := decideVPNRouteForTarget(net.JoinHostPort(ips[0], "443"))
	if err != nil {
		result["status"] = "route_failed"
		result["error"] = err.Error()
		result["updated_at"] = time.Now().UTC().Format(time.RFC3339)
		return result
	}
	result["route"] = map[string]any{
		"group":             route.Group,
		"direct":            route.Direct,
		"reject":            route.Reject,
		"target":            route.TargetAddr,
		"selected_route_id": route.SelectedRouteID,
	}
	if route.Reject {
		result["status"] = "route_rejected"
		result["error"] = "self-check target is rejected"
		result["updated_at"] = time.Now().UTC().Format(time.RFC3339)
		return result
	}
	if route.Direct {
		result["ok"] = true
		result["status"] = "direct_ready"
		result["updated_at"] = time.Now().UTC().Format(time.RFC3339)
		return result
	}
	if strings.TrimSpace(route.SelectedRouteID) == "" {
		result["status"] = "route_missing"
		result["error"] = "tunnel route missing selected_route_id"
		result["updated_at"] = time.Now().UTC().Format(time.RFC3339)
		return result
	}
	result["ok"] = true
	result["status"] = "vroute_packet_ready"
	result["updated_at"] = time.Now().UTC().Format(time.RFC3339)
	result["duration_ms"] = time.Since(startedAt).Milliseconds()
	return result
}

func setVPNSelfCheckResult(result map[string]any) {
	if result == nil {
		return
	}
	if _, ok := result["duration_ms"]; !ok {
		if startedAt, ok := parseRFC3339Time(stringFromAny(result["started_at"])); ok {
			result["duration_ms"] = time.Since(startedAt).Milliseconds()
		}
	}
	vpnRuntime.mu.Lock()
	vpnRuntime.selfCheck = cloneVPNMap(result)
	if ok, _ := result["ok"].(bool); !ok {
		if errText := strings.TrimSpace(stringFromAny(result["error"])); errText != "" {
			vpnRuntime.lastError = "self_check: " + errText
		}
	}
	vpnRuntime.updatedAt = time.Now().UTC().Format(time.RFC3339)
	vpnRuntime.mu.Unlock()
}

func newandroidVPNDataPlane(tun *os.File) (*androidVPNDataPlane, error) {
	gStack := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol},
	})
	linkEP := channel.New(vpnQueueSize, vpnMTU, "")
	if err := tcpipErrToError(gStack.CreateNIC(vpnNICID, linkEP)); err != nil {
		gStack.Destroy()
		return nil, err
	}
	if err := tcpipErrToError(gStack.SetPromiscuousMode(vpnNICID, true)); err != nil {
		gStack.Destroy()
		return nil, err
	}
	if err := tcpipErrToError(gStack.SetSpoofing(vpnNICID, true)); err != nil {
		gStack.Destroy()
		return nil, err
	}
	if err := addAndroidVPNProtocolAddress(gStack, ipv4.ProtocolNumber, vpnIPv4GatewayAddress, 32); err != nil {
		gStack.Destroy()
		return nil, err
	}
	if err := addAndroidVPNProtocolAddress(gStack, ipv4.ProtocolNumber, vpnIPv4Address, 32); err != nil {
		gStack.Destroy()
		return nil, err
	}
	gStack.SetRouteTable([]tcpip.Route{
		{Destination: header.IPv4EmptySubnet, NIC: vpnNICID},
	})
	ctx, cancel := context.WithCancel(context.Background())
	runner := &androidVPNDataPlane{
		stack:  gStack,
		linkEP: linkEP,
		tun:    tun,
		cancel: cancel,
		doneCh: make(chan struct{}),
	}
	tcpForwarder := tcp.NewForwarder(gStack, vpnTCPWindow, vpnTCPInFlight, runner.handleTCPForwarder)
	udpForwarder := udp.NewForwarder(gStack, runner.handleUDPForwarder)
	gStack.SetTransportProtocolHandler(tcp.ProtocolNumber, tcpForwarder.HandlePacket)
	gStack.SetTransportProtocolHandler(udp.ProtocolNumber, udpForwarder.HandlePacket)
	go runner.inputLoop(ctx)
	go runner.outputLoop(ctx)
	return runner, nil
}

func addAndroidVPNProtocolAddress(gStack *stack.Stack, protocol tcpip.NetworkProtocolNumber, address tcpip.Address, prefixLen int) error {
	return tcpipErrToError(gStack.AddProtocolAddress(vpnNICID, tcpip.ProtocolAddress{
		Protocol: protocol,
		AddressWithPrefix: tcpip.AddressWithPrefix{
			Address:   address,
			PrefixLen: prefixLen,
		},
	}, stack.AddressProperties{}))
}

func (n *androidVPNDataPlane) inputLoop(ctx context.Context) {
	defer close(n.doneCh)
	buf := make([]byte, vpnMTU+128)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		readN, err := n.tun.Read(buf)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, os.ErrClosed) {
				return
			}
			recordVPNRuntimeError("tun_read", err)
			continue
		}
		if readN <= 0 {
			continue
		}
		packet := append([]byte(nil), buf[:readN]...)
		if rewritten, ok, rewriteErr := rewriteAndroidVPNRealIPPacketToFake(packet); rewriteErr != nil {
			recordVPNRuntimeError("real_ip_fake_rewrite", rewriteErr)
		} else if ok {
			packet = rewritten
		}
		handled, routeErr := mobileVRouteHandleVPNPacket(currentAndroidVPNConfigDir(), packet, func(reply []byte) error {
			if len(reply) == 0 {
				return nil
			}
			if rewritten, ok, rewriteErr := rewriteAndroidVPNFakeIPPacketToReal(reply); rewriteErr != nil {
				recordVPNRuntimeError("fake_ip_real_rewrite", rewriteErr)
			} else if ok {
				reply = rewritten
			}
			return n.writeTunPacket(reply)
		})
		if routeErr != nil {
			logAndroidVPNDiagnostic("tun_takeover_error", "error", "vpn tun takeover failed: "+androidVPNPacketSummary(packet)+" err="+routeErr.Error(), 2*time.Second)
			recordVPNRuntimeError("vroute_packet", routeErr)
		}
		if handled {
			continue
		}
		_, _ = n.Write(packet)
	}
}

func (n *androidVPNDataPlane) outputLoop(ctx context.Context) {
	for {
		packet := n.linkEP.ReadContext(ctx)
		if packet == nil {
			if ctx.Err() != nil {
				return
			}
			continue
		}
		view := packet.ToView()
		payload := append([]byte(nil), view.AsSlice()...)
		view.Release()
		packet.DecRef()
		if len(payload) == 0 {
			continue
		}
		if err := n.writeTunPacket(payload); err != nil {
			if ctx.Err() != nil || errors.Is(err, os.ErrClosed) {
				return
			}
			recordVPNRuntimeError("tun_write", err)
		}
	}
}

func (n *androidVPNDataPlane) writeTunPacket(packet []byte) error {
	if len(packet) == 0 {
		return nil
	}
	if n == nil || n.tun == nil || n.closed.Load() {
		return io.ErrClosedPipe
	}
	n.tunWriteMu.Lock()
	defer n.tunWriteMu.Unlock()
	if vpnTunWriteTimeout > 0 {
		_ = n.tun.SetWriteDeadline(time.Now().Add(vpnTunWriteTimeout))
		defer n.tun.SetWriteDeadline(time.Time{})
	}
	nn, err := n.tun.Write(packet)
	if err != nil {
		return err
	}
	if nn != len(packet) {
		return io.ErrShortWrite
	}
	return nil
}

func (n *androidVPNDataPlane) Write(packet []byte) (int, error) {
	if len(packet) == 0 {
		return 0, nil
	}
	if n == nil || n.closed.Load() {
		return 0, io.ErrClosedPipe
	}
	protocol, err := vpnProtocolFromPacket(packet)
	if err != nil {
		return 0, err
	}
	pkt := stack.NewPacketBuffer(stack.PacketBufferOptions{
		Payload: buffer.MakeWithData(append([]byte(nil), packet...)),
	})
	defer pkt.DecRef()
	n.linkEP.InjectInbound(protocol, pkt)
	return len(packet), nil
}

func (n *androidVPNDataPlane) Close() error {
	if n == nil {
		return nil
	}
	n.closeOnce.Do(func() {
		n.closed.Store(true)
		if n.cancel != nil {
			n.cancel()
		}
		if n.linkEP != nil {
			n.linkEP.Close()
		}
		select {
		case <-n.doneCh:
		case <-time.After(2 * time.Second):
		}
		if n.stack != nil {
			n.stack.Destroy()
		}
	})
	return nil
}

func (n *androidVPNDataPlane) handleTCPForwarder(req *tcp.ForwarderRequest) {
	if req == nil {
		return
	}
	id := req.ID()
	targetAddr, err := vpnTransportIDToTarget(id.LocalAddress, id.LocalPort)
	if err != nil {
		req.Complete(true)
		return
	}
	var wq waiter.Queue
	ep, createErr := req.CreateEndpoint(&wq)
	if createErr != nil {
		req.Complete(true)
		return
	}
	req.Complete(false)
	inbound := gonet.NewTCPConn(&wq, ep)
	if isAndroidVPNDNSTarget(targetAddr) {
		go serveAndroidVPNTCPDNS(inbound, targetAddr)
		return
	}
	preface, dialTarget, sni := prepareVPNTCPDialTarget(inbound, targetAddr)
	flowID := newAndroidRouteFlowID("vpn_tcp", dialTarget)
	outbound, route, err := openVPNDirectTCPForTarget(dialTarget, flowID, targetAddr)
	if err != nil {
		if sni != "" && !route.Direct && !route.Reject && strings.TrimSpace(route.SelectedRouteID) != "" {
			_ = inbound.Close()
			return
		}
		stage := "tcp_open " + targetAddr
		if dialTarget != targetAddr {
			stage += " via_sni " + dialTarget
		}
		globalandroidRouteConnectionState.recordFailure("open_failed", androidRouteConnectionOptions{
			Scope:  "vpn_tcp",
			FlowID: flowID,
			Side:   "local",
			Target: dialTarget,
			Route:  androidRouteConnectionRouteFromVPN(route),
		}, err)
		recordVPNRuntimeError(stage, err)
		_ = inbound.Close()
		return
	}
	if sni != "" {
		androidLogStore.add("vpn", "debug", "tcp sni route "+targetAddr+" -> "+dialTarget)
	}
	if len(preface) > 0 {
		_ = outbound.SetWriteDeadline(time.Now().Add(mobileRouteResponseReadTimeout))
		_, writeErr := outbound.Write(preface)
		_ = outbound.SetWriteDeadline(time.Time{})
		if writeErr != nil {
			recordVPNRuntimeError("tcp_preface "+dialTarget, writeErr)
			_ = inbound.Close()
			_ = outbound.Close()
			return
		}
	}
	relay := globalandroidRouteConnectionState.begin(androidRouteConnectionOptions{
		Scope:  "vpn_tcp",
		FlowID: flowID,
		Side:   "local",
		Target: dialTarget,
		Route:  androidRouteConnectionRouteFromVPN(route),
	})
	go pipeVPNConn(outbound, inbound, relay, "up")
	go pipeVPNConn(inbound, outbound, relay, "down")
}

func (n *androidVPNDataPlane) handleUDPForwarder(req *udp.ForwarderRequest) {
	if req == nil {
		return
	}
	id := req.ID()
	targetAddr, err := vpnTransportIDToTarget(id.LocalAddress, id.LocalPort)
	if err != nil {
		return
	}
	if isAndroidVPNDNSTarget(targetAddr) {
		n.handleDNSForwarder(req, targetAddr)
		return
	}
	var wq waiter.Queue
	ep, createErr := req.CreateEndpoint(&wq)
	if createErr != nil {
		return
	}
	inbound := gonet.NewUDPConn(&wq, ep)
	outbound, route, flowID, err := openVPNDirectUDPConn(id, targetAddr)
	if err != nil {
		globalandroidRouteConnectionState.recordFailure("open_failed", androidRouteConnectionOptions{
			Scope:     "vpn_udp",
			FlowID:    flowID,
			Side:      "local",
			Target:    targetAddr,
			Route:     androidRouteConnectionRouteFromVPN(route),
			Transport: "udp",
		}, err)
		recordVPNRuntimeError("udp_open "+targetAddr, err)
		_ = inbound.Close()
		return
	}
	relay := globalandroidRouteConnectionState.begin(androidRouteConnectionOptions{
		Scope:     "vpn_udp",
		FlowID:    flowID,
		Side:      "local",
		Target:    targetAddr,
		Route:     androidRouteConnectionRouteFromVPN(route),
		Transport: "udp",
	})
	go relayVPNUDP(inbound, outbound, relay)
}

func (n *androidVPNDataPlane) handleDNSForwarder(req *udp.ForwarderRequest, targetAddr string) {
	var wq waiter.Queue
	ep, createErr := req.CreateEndpoint(&wq)
	if createErr != nil {
		recordVPNRuntimeError("dns_create "+targetAddr, errors.New(createErr.String()))
		return
	}
	inbound := gonet.NewUDPConn(&wq, ep)
	go serveAndroidVPNDNS(inbound, targetAddr)
}

func openVPNDirectTCPForTarget(targetAddr string, flowID string, originalTargetAddr string) (net.Conn, vpnRouteDecision, error) {
	route, err := decideVPNRouteForTarget(targetAddr)
	if err != nil {
		return nil, route, err
	}
	if route.Reject {
		return nil, route, errors.New("route rejected")
	}
	rememberAndroidVPNSNIFakeIP(originalTargetAddr, targetAddr, route)
	conn, err := dialVPNDirectTCPWithFlow(route, flowID)
	if err == nil {
		return conn, route, nil
	}
	if fallbackRoute, ok := buildAndroidVPNIPv4FallbackRoute(route, err); ok {
		androidLogStore.add("vpn", "warn", "tcp ipv6 fallback "+route.TargetAddr+" -> "+fallbackRoute.TargetAddr+" group="+fallbackRoute.Group)
		if fallbackConn, fallbackErr := dialVPNDirectTCPWithFlow(fallbackRoute, flowID); fallbackErr == nil {
			return fallbackConn, fallbackRoute, nil
		} else {
			return nil, fallbackRoute, fmt.Errorf("%w; ipv4 fallback %s failed: %v", err, fallbackRoute.TargetAddr, fallbackErr)
		}
	}
	return nil, route, err
}

func prepareVPNTCPDialTarget(inbound net.Conn, targetAddr string) ([]byte, string, string) {
	host, port, err := net.SplitHostPort(strings.TrimSpace(targetAddr))
	if err != nil || strings.TrimSpace(port) != "443" {
		return nil, targetAddr, ""
	}
	if ip := net.ParseIP(strings.TrimSpace(strings.Trim(host, "[]"))); ip == nil {
		return nil, targetAddr, ""
	}
	if inbound == nil {
		return nil, targetAddr, ""
	}
	_ = inbound.SetReadDeadline(time.Now().Add(1200 * time.Millisecond))
	buf := make([]byte, 16*1024)
	n, readErr := readVPNInitialTCPPreface(inbound, buf)
	_ = inbound.SetReadDeadline(time.Time{})
	if n <= 0 {
		return nil, targetAddr, ""
	}
	preface := append([]byte(nil), buf[:n]...)
	if readErr != nil && !isTimeoutError(readErr) {
		return preface, targetAddr, ""
	}
	sni := extractVPNTLSClientHelloSNI(preface)
	if !isValidVPNTLSSNIHost(sni) {
		return preface, targetAddr, ""
	}
	return preface, net.JoinHostPort(sni, port), sni
}

func readVPNInitialTCPPreface(inbound net.Conn, buf []byte) (int, error) {
	if inbound == nil || len(buf) == 0 {
		return 0, nil
	}
	total := 0
	for {
		n, err := inbound.Read(buf[total:])
		if n > 0 {
			total += n
		}
		if err != nil {
			return total, err
		}
		if !needsMoreVPNTLSClientHelloBytes(buf[:total]) {
			return total, nil
		}
		if total >= len(buf) {
			return total, nil
		}
	}
}

func needsMoreVPNTLSClientHelloBytes(data []byte) bool {
	if len(data) == 0 {
		return true
	}
	if len(data) < 5 {
		return data[0] == 0x16
	}
	if data[0] != 0x16 {
		return false
	}
	recordLen := int(binary.BigEndian.Uint16(data[3:5]))
	if recordLen <= 0 {
		return false
	}
	return len(data) < 5+recordLen
}

func extractVPNTLSClientHelloSNI(data []byte) string {
	if len(data) < 5 || data[0] != 0x16 {
		return ""
	}
	recordLen := int(binary.BigEndian.Uint16(data[3:5]))
	if recordLen <= 0 || len(data) < 5+recordLen {
		return ""
	}
	offset := 5
	if len(data[offset:]) < 4 || data[offset] != 0x01 {
		return ""
	}
	helloLen := int(data[offset+1])<<16 | int(data[offset+2])<<8 | int(data[offset+3])
	offset += 4
	if helloLen <= 0 || offset+helloLen > len(data) || offset+34 > len(data) {
		return ""
	}
	offset += 34
	if offset >= len(data) {
		return ""
	}
	sessionLen := int(data[offset])
	offset++
	if offset+sessionLen+2 > len(data) {
		return ""
	}
	offset += sessionLen
	cipherLen := int(binary.BigEndian.Uint16(data[offset : offset+2]))
	offset += 2
	if offset+cipherLen+1 > len(data) {
		return ""
	}
	offset += cipherLen
	compressionLen := int(data[offset])
	offset++
	if offset+compressionLen+2 > len(data) {
		return ""
	}
	offset += compressionLen
	extensionsLen := int(binary.BigEndian.Uint16(data[offset : offset+2]))
	offset += 2
	extensionsEnd := offset + extensionsLen
	if extensionsEnd > len(data) {
		return ""
	}
	for offset+4 <= extensionsEnd {
		extType := binary.BigEndian.Uint16(data[offset : offset+2])
		extLen := int(binary.BigEndian.Uint16(data[offset+2 : offset+4]))
		offset += 4
		if offset+extLen > extensionsEnd {
			return ""
		}
		if extType == 0 {
			return extractVPNTLSSNIFromExtension(data[offset : offset+extLen])
		}
		offset += extLen
	}
	return ""
}

func extractVPNTLSSNIFromExtension(data []byte) string {
	if len(data) < 2 {
		return ""
	}
	listLen := int(binary.BigEndian.Uint16(data[:2]))
	offset := 2
	end := offset + listLen
	if end > len(data) {
		return ""
	}
	for offset+3 <= end {
		nameType := data[offset]
		nameLen := int(binary.BigEndian.Uint16(data[offset+1 : offset+3]))
		offset += 3
		if offset+nameLen > end {
			return ""
		}
		if nameType == 0 {
			return strings.ToLower(strings.TrimSpace(string(data[offset : offset+nameLen])))
		}
		offset += nameLen
	}
	return ""
}

func isValidVPNTLSSNIHost(host string) bool {
	host = strings.TrimSpace(strings.Trim(host, "."))
	if host == "" || len(host) > 253 || strings.ContainsAny(host, " \t\r\n:/\\") {
		return false
	}
	if net.ParseIP(strings.Trim(host, "[]")) != nil {
		return false
	}
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return false
	}
	for _, label := range labels {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
		for _, r := range label {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' {
				continue
			}
			return false
		}
	}
	return true
}

func dialVPNDirectTCPWithFlow(route vpnRouteDecision, flowID string) (net.Conn, error) {
	if !route.Direct {
		return nil, errors.New("non-direct tcp packet should be handled by vroute ip data plane")
	}
	dialer := net.Dialer{Timeout: mobileRouteConnectTimeout}
	return dialer.Dial("tcp", route.TargetAddr)
}

func openVPNDirectUDPConn(id stack.TransportEndpointID, targetAddr string) (io.ReadWriteCloser, vpnRouteDecision, string, error) {
	route, err := decideVPNRouteForTarget(targetAddr)
	if err != nil {
		return nil, route, "", err
	}
	flowID := newAndroidRouteFlowID("vpn_udp", targetAddr)
	if route.Reject {
		return nil, route, flowID, errors.New("route rejected")
	}
	if !route.Direct {
		return nil, route, flowID, errors.New("non-direct udp packet should be handled by vroute ip data plane")
	}
	udpAddr, err := net.ResolveUDPAddr("udp", route.TargetAddr)
	if err != nil {
		return nil, route, flowID, err
	}
	conn, err := net.DialUDP("udp", nil, udpAddr)
	return conn, route, flowID, err
}

func decideVPNRouteForTarget(targetAddr string) (vpnRouteDecision, error) {
	if rewrittenTarget, fakeEntry, ok := rewriteAndroidVPNFakeIPTarget(targetAddr); ok {
		route := androidRouteDecision{
			Direct:          fakeEntry.Direct,
			Reject:          fakeEntry.Reject,
			TargetAddr:      rewrittenTarget,
			Group:           firstNonEmptyString(strings.TrimSpace(fakeEntry.Group), "fallback"),
			SelectedRouteID: strings.TrimSpace(fakeEntry.SelectedRouteID),
		}
		if !route.Direct && !route.Reject && route.SelectedRouteID == "" {
			return vpnRouteDecision{}, errors.New("fake ip tunnel route missing selected_route_id")
		}
		if strings.TrimSpace(route.Group) == "" {
			route.Group = "fallback"
		}
		androidLogStore.add("vpn", "debug", "fake ip route "+targetAddr+" -> "+fakeEntry.Domain+" via "+route.Group)
		return vpnRouteDecision{
			Direct:          route.Direct,
			Reject:          route.Reject,
			TargetAddr:      route.TargetAddr,
			Group:           route.Group,
			SelectedRouteID: route.SelectedRouteID,
		}, nil
	}
	route, err := decideAndroidRouteForTarget(targetAddr)
	if err != nil {
		return vpnRouteDecision{}, err
	}
	return vpnRouteDecision{
		Direct:          route.Direct,
		Reject:          route.Reject,
		TargetAddr:      route.TargetAddr,
		Group:           route.Group,
		SelectedRouteID: route.SelectedRouteID,
	}, nil
}

func decideAndroidRouteForTarget(targetAddr string) (androidRouteDecision, error) {
	host, port, err := net.SplitHostPort(strings.TrimSpace(targetAddr))
	if err != nil {
		return androidRouteDecision{}, err
	}
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if host == "" || strings.TrimSpace(port) == "" {
		return androidRouteDecision{}, errors.New("invalid target address")
	}
	if decision, ok, err := decideMobileVRouteForTarget(currentAndroidVPNConfigDir(), net.JoinHostPort(host, port)); err != nil {
		return androidRouteDecision{}, err
	} else if ok {
		return androidRouteDecision{
			Direct:          decision.Direct,
			Reject:          decision.Reject,
			TargetAddr:      decision.TargetAddr,
			Group:           firstNonEmptyString(strings.TrimSpace(decision.Group), "vroute"),
			SelectedRouteID: mobileVRouteRouteIDForDecision(decision),
		}, nil
	}
	return androidRouteDecision{
		Direct:     true,
		TargetAddr: net.JoinHostPort(host, port),
		Group:      "direct",
	}, nil
}

func pipeVPNConn(dst net.Conn, src net.Conn, relay *androidRouteConnectionRelay, direction string) {
	defer func() {
		if relay != nil {
			relay.releaseSide()
		}
	}()
	defer closeVPNWrite(dst)
	defer closeVPNRead(src)
	if vpnRelayIdle > 0 {
		deadline := time.Now().Add(vpnRelayIdle)
		_ = src.SetReadDeadline(deadline)
		_ = dst.SetReadDeadline(deadline)
	}
	writer := io.Writer(dst)
	if relay != nil {
		writer = &androidRouteConnectionWriter{dst: dst, relay: relay, direction: direction}
	}
	if _, err := mobileRelayCopy(writer, src); err != nil {
		if relay != nil {
			relay.markCloseReason(direction + "_" + classifyAndroidRouteRelayClose(err))
		}
		globalandroidRouteConnectionState.recordRelayFailure(relay, err)
	} else if relay != nil {
		relay.markCloseReason(direction + "_eof")
	}
}

func relayVPNUDP(inbound *gonet.UDPConn, outbound io.ReadWriteCloser, relay *androidRouteConnectionRelay) {
	defer inbound.Close()
	defer outbound.Close()
	done := make(chan struct{}, 2)
	go func() {
		defer func() {
			if relay != nil {
				relay.releaseSide()
			}
		}()
		writer := io.Writer(outbound)
		if relay != nil {
			writer = &androidRouteConnectionWriter{dst: outbound, relay: relay, direction: "up"}
		}
		if _, err := mobileRelayCopy(writer, inbound); err != nil {
			if relay != nil {
				relay.markCloseReason("up_" + classifyAndroidRouteRelayClose(err))
			}
			globalandroidRouteConnectionState.recordRelayFailure(relay, err)
		} else if relay != nil {
			relay.markCloseReason("up_eof")
		}
		done <- struct{}{}
	}()
	go func() {
		defer func() {
			if relay != nil {
				relay.releaseSide()
			}
		}()
		writer := io.Writer(inbound)
		if relay != nil {
			writer = &androidRouteConnectionWriter{dst: inbound, relay: relay, direction: "down"}
		}
		if _, err := mobileRelayCopy(writer, outbound); err != nil {
			if relay != nil {
				relay.markCloseReason("down_" + classifyAndroidRouteRelayClose(err))
			}
			globalandroidRouteConnectionState.recordRelayFailure(relay, err)
		} else if relay != nil {
			relay.markCloseReason("down_eof")
		}
		done <- struct{}{}
	}()
	select {
	case <-done:
	case <-time.After(vpnUDPRelayTimeout):
		if relay != nil {
			relay.markCloseReason("udp_timeout")
		}
	}
}

func serveAndroidVPNDNS(conn *gonet.UDPConn, targetAddr string) {
	defer conn.Close()
	buf := make([]byte, 4096)
	for {
		_ = conn.SetReadDeadline(time.Now().Add(vpnDNSReadTimeout))
		n, err := conn.Read(buf)
		if err != nil {
			if !errors.Is(err, os.ErrDeadlineExceeded) && !isTimeoutError(err) {
				recordVPNRuntimeError("dns_read "+targetAddr, err)
			}
			return
		}
		if n <= 0 {
			continue
		}
		response, err := resolveAndroidVPNDNSPacket(buf[:n])
		if err != nil {
			recordVPNRuntimeError("dns_resolve "+targetAddr, err)
			response = buildAndroidVPNDNSRCode(buf[:n], dnsmessage.RCodeServerFailure)
		}
		if len(response) == 0 {
			response = buildAndroidVPNDNSRCode(buf[:n], dnsmessage.RCodeServerFailure)
		}
		if len(response) == 0 {
			continue
		}
		_ = conn.SetWriteDeadline(time.Now().Add(vpnDNSLookupTimeout))
		if _, err := conn.Write(response); err != nil {
			recordVPNRuntimeError("dns_write "+targetAddr, err)
			return
		}
	}
}

func serveAndroidVPNTCPDNS(conn net.Conn, targetAddr string) {
	defer conn.Close()
	header := make([]byte, 2)
	for {
		_ = conn.SetReadDeadline(time.Now().Add(vpnDNSReadTimeout))
		if _, err := io.ReadFull(conn, header); err != nil {
			if !errors.Is(err, os.ErrDeadlineExceeded) && !isTimeoutError(err) && !errors.Is(err, io.EOF) {
				recordVPNRuntimeError("dns_tcp_read "+targetAddr, err)
			}
			return
		}
		packetLen := int(binary.BigEndian.Uint16(header))
		if packetLen <= 0 || packetLen > 4096 {
			recordVPNRuntimeError("dns_tcp_read "+targetAddr, fmt.Errorf("invalid dns tcp packet length: %d", packetLen))
			return
		}
		packet := make([]byte, packetLen)
		if _, err := io.ReadFull(conn, packet); err != nil {
			if !errors.Is(err, os.ErrDeadlineExceeded) && !isTimeoutError(err) && !errors.Is(err, io.EOF) {
				recordVPNRuntimeError("dns_tcp_read "+targetAddr, err)
			}
			return
		}
		response, err := resolveAndroidVPNDNSPacket(packet)
		if err != nil {
			recordVPNRuntimeError("dns_tcp_resolve "+targetAddr, err)
			response = buildAndroidVPNDNSRCode(packet, dnsmessage.RCodeServerFailure)
		}
		if len(response) == 0 {
			response = buildAndroidVPNDNSRCode(packet, dnsmessage.RCodeServerFailure)
		}
		if len(response) == 0 || len(response) > 65535 {
			return
		}
		out := make([]byte, 2+len(response))
		binary.BigEndian.PutUint16(out[:2], uint16(len(response)))
		copy(out[2:], response)
		_ = conn.SetWriteDeadline(time.Now().Add(vpnDNSLookupTimeout))
		if _, err := conn.Write(out); err != nil {
			recordVPNRuntimeError("dns_tcp_write "+targetAddr, err)
			return
		}
	}
}

func resolveAndroidVPNDNSPacket(packet []byte) ([]byte, error) {
	domain, qType, err := parseAndroidVPNDNSQuestion(packet)
	if err != nil {
		return nil, err
	}
	if domain == "" {
		return buildAndroidVPNDNSRCode(packet, dnsmessage.RCodeNameError), nil
	}
	route, routeErr := decideAndroidRouteForTarget(net.JoinHostPort(domain, "443"))
	if routeErr != nil {
		return nil, routeErr
	}
	if route.Reject {
		return buildAndroidVPNDNSRCode(packet, dnsmessage.RCodeRefused), nil
	}
	if shouldUseAndroidVPNDNSFakeIP(route, qType, domain) {
		fakeIP, ok := allocateAndroidVPNDNSFakeIP(domain, route)
		if !ok {
			return nil, errors.New("allocate fake ip failed")
		}
		androidLogStore.add("vpn", "debug", "dns fake "+domain+" -> "+fakeIP+" group="+route.Group)
		return buildAndroidVPNDNSSuccess(packet, []net.IP{net.ParseIP(fakeIP).To4()}, dnsmessage.TypeA), nil
	}
	if qType != dnsmessage.TypeA && qType != dnsmessage.TypeAAAA {
		return buildAndroidVPNDNSSuccess(packet, nil, qType), nil
	}
	if qType == dnsmessage.TypeAAAA {
		androidLogStore.add("vpn", "debug", "dns suppress AAAA "+domain+": android vroute ipv6 disabled")
		return buildAndroidVPNDNSSuccess(packet, nil, qType), nil
	}
	response, err := queryAndroidVPNDNSUpstream(packet)
	if err != nil {
		return nil, err
	}
	storeAndroidVPNDNSRouteHints(domain, response, route)
	return response, nil
}

func parseAndroidVPNDNSQuestion(packet []byte) (string, dnsmessage.Type, error) {
	parser := dnsmessage.Parser{}
	if _, err := parser.Start(packet); err != nil {
		return "", dnsmessage.TypeA, err
	}
	question, err := parser.Question()
	if err != nil {
		return "", dnsmessage.TypeA, err
	}
	domain := strings.TrimSpace(strings.TrimSuffix(strings.ToLower(question.Name.String()), "."))
	return domain, question.Type, nil
}

func buildAndroidVPNDNSRCode(request []byte, rcode dnsmessage.RCode) []byte {
	parser := dnsmessage.Parser{}
	requestHeader, err := parser.Start(request)
	if err != nil {
		return nil
	}
	questions, err := collectAndroidVPNDNSQuestions(&parser)
	if err != nil {
		return nil
	}
	header := dnsmessage.Header{
		ID:                 requestHeader.ID,
		Response:           true,
		OpCode:             requestHeader.OpCode,
		RecursionDesired:   requestHeader.RecursionDesired,
		RecursionAvailable: true,
		RCode:              rcode,
	}
	builder := dnsmessage.NewBuilder(nil, header)
	builder.EnableCompression()
	if err := builder.StartQuestions(); err != nil {
		return nil
	}
	for _, question := range questions {
		if err := builder.Question(question); err != nil {
			return nil
		}
	}
	message, err := builder.Finish()
	if err != nil {
		return nil
	}
	return message
}

func buildAndroidVPNDNSSuccess(request []byte, ips []net.IP, qType dnsmessage.Type) []byte {
	parser := dnsmessage.Parser{}
	requestHeader, err := parser.Start(request)
	if err != nil {
		return nil
	}
	questions, err := collectAndroidVPNDNSQuestions(&parser)
	if err != nil {
		return nil
	}
	header := dnsmessage.Header{
		ID:                 requestHeader.ID,
		Response:           true,
		OpCode:             requestHeader.OpCode,
		RecursionDesired:   requestHeader.RecursionDesired,
		RecursionAvailable: true,
		RCode:              dnsmessage.RCodeSuccess,
	}
	builder := dnsmessage.NewBuilder(nil, header)
	builder.EnableCompression()
	if err := builder.StartQuestions(); err != nil {
		return nil
	}
	var answerName dnsmessage.Name
	answerNameSet := false
	for _, question := range questions {
		if err := builder.Question(question); err != nil {
			return nil
		}
		if !answerNameSet && question.Type == qType {
			answerName = question.Name
			answerNameSet = true
		}
	}
	if err := builder.StartAnswers(); err != nil {
		return nil
	}
	if answerNameSet {
		for _, ip := range ips {
			if ip == nil {
				continue
			}
			if qType == dnsmessage.TypeA {
				ip4 := ip.To4()
				if ip4 == nil {
					continue
				}
				var answer dnsmessage.AResource
				copy(answer.A[:], ip4)
				if err := builder.AResource(dnsmessage.ResourceHeader{Name: answerName, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: uint32(vpnDNSCacheTTL / time.Second)}, answer); err != nil {
					return nil
				}
				continue
			}
			if qType == dnsmessage.TypeAAAA {
				ip16 := ip.To16()
				if ip16 == nil || ip.To4() != nil {
					continue
				}
				var answer dnsmessage.AAAAResource
				copy(answer.AAAA[:], ip16)
				if err := builder.AAAAResource(dnsmessage.ResourceHeader{Name: answerName, Type: dnsmessage.TypeAAAA, Class: dnsmessage.ClassINET, TTL: uint32(vpnDNSCacheTTL / time.Second)}, answer); err != nil {
					return nil
				}
			}
		}
	}
	message, err := builder.Finish()
	if err != nil {
		return nil
	}
	return message
}

func collectAndroidVPNDNSQuestions(parser *dnsmessage.Parser) ([]dnsmessage.Question, error) {
	questions := make([]dnsmessage.Question, 0, 1)
	for {
		question, err := parser.Question()
		if err != nil {
			if errors.Is(err, dnsmessage.ErrSectionDone) {
				break
			}
			return nil, err
		}
		questions = append(questions, question)
	}
	return questions, nil
}

func queryAndroidVPNDNSUpstream(packet []byte) ([]byte, error) {
	upstreams := []string{"1.1.1.1:53", "8.8.8.8:53"}
	var lastErr error
	for _, upstream := range upstreams {
		conn, err := net.DialTimeout("udp", upstream, vpnDNSLookupTimeout)
		if err != nil {
			lastErr = err
			continue
		}
		_ = conn.SetDeadline(time.Now().Add(vpnDNSLookupTimeout))
		if _, err := conn.Write(packet); err != nil {
			lastErr = err
			_ = conn.Close()
			continue
		}
		buf := make([]byte, 4096)
		n, err := conn.Read(buf)
		_ = conn.Close()
		if err != nil {
			lastErr = err
			continue
		}
		if n <= 0 {
			lastErr = errors.New("empty dns upstream response")
			continue
		}
		return append([]byte(nil), buf[:n]...), nil
	}
	if lastErr == nil {
		lastErr = errors.New("dns upstream resolve failed")
	}
	return nil, lastErr
}

func storeAndroidVPNDNSRouteHints(domain string, response []byte, route androidRouteDecision) {
	cleanDomain := strings.TrimSpace(strings.ToLower(strings.Trim(domain, ".")))
	if cleanDomain == "" {
		return
	}
	ips := extractAndroidVPNDNSResponseIPs(response)
	if len(ips) == 0 {
		return
	}
	configDir := currentAndroidVPNConfigDir()
	ensureAndroidVPNDNSCacheLoaded(configDir)
	ipv4, ipv6 := splitAndroidVPNDNSResponseIPFamilies(ips)
	now := time.Now().UTC()
	vpnDNSState.mu.Lock()
	pruneAndroidVPNDNSFakeLocked(now)
	if vpnDNSState.routeIPHints == nil {
		vpnDNSState.routeIPHints = map[string]androidVPNDNSRouteHintEntry{}
	}
	for _, ip := range ips {
		parsed := net.ParseIP(strings.TrimSpace(strings.Trim(ip, "[]")))
		if parsed == nil {
			continue
		}
		ipText := parsed.String()
		vpnDNSState.routeIPHints[ipText] = androidVPNDNSRouteHintEntry{
			Domain:    cleanDomain,
			IP:        ipText,
			IPv4:      append([]string(nil), ipv4...),
			IPv6:      append([]string(nil), ipv6...),
			Group:     strings.TrimSpace(route.Group),
			ExpiresAt: now.Add(vpnDNSCacheTTL),
		}
	}
	vpnDNSState.mu.Unlock()
	markAndroidVPNDNSCacheDirty(configDir)
}

func rememberAndroidVPNSNIFakeIP(originalTargetAddr string, sniTargetAddr string, route vpnRouteDecision) {
	if route.Direct || route.Reject || strings.TrimSpace(route.SelectedRouteID) == "" {
		return
	}
	sniHost, port, err := net.SplitHostPort(strings.TrimSpace(sniTargetAddr))
	if err != nil {
		return
	}
	domain := strings.TrimSpace(strings.ToLower(strings.Trim(sniHost, ".")))
	if domain == "" || net.ParseIP(strings.Trim(domain, "[]")) != nil {
		return
	}
	_, ok := allocateAndroidVPNDNSFakeIP(domain, androidRouteDecision{
		Direct:          route.Direct,
		Reject:          route.Reject,
		TargetAddr:      net.JoinHostPort(domain, strings.TrimSpace(port)),
		Group:           strings.TrimSpace(route.Group),
		SelectedRouteID: strings.TrimSpace(route.SelectedRouteID),
	})
	if ok {
		androidLogStore.add("vpn", "debug", "tcp sni fake-ip warmed "+domain+" group="+route.Group)
	}
	rememberAndroidVPNRealIPFakeMapping(originalTargetAddr, domain, strings.TrimSpace(port), route, 6)
	rememberAndroidVPNRealIPFakeMapping(originalTargetAddr, domain, strings.TrimSpace(port), route, 17)
}

func rememberAndroidVPNRealIPFakeMapping(originalTargetAddr string, domain string, port string, route vpnRouteDecision, protocol uint8) {
	if route.Direct || route.Reject || strings.TrimSpace(route.SelectedRouteID) == "" {
		return
	}
	originalHost, originalPort, err := net.SplitHostPort(strings.TrimSpace(originalTargetAddr))
	if err != nil {
		return
	}
	realIP := net.ParseIP(strings.TrimSpace(strings.Trim(originalHost, "[]"))).To4()
	if realIP == nil {
		return
	}
	cleanDomain := strings.TrimSpace(strings.ToLower(strings.Trim(domain, ".")))
	cleanPort := firstNonEmptyString(strings.TrimSpace(port), strings.TrimSpace(originalPort))
	if cleanDomain == "" || cleanPort == "" {
		return
	}
	fakeIP, ok := allocateAndroidVPNDNSFakeIP(cleanDomain, androidRouteDecision{
		Direct:          route.Direct,
		Reject:          route.Reject,
		TargetAddr:      net.JoinHostPort(cleanDomain, cleanPort),
		Group:           strings.TrimSpace(route.Group),
		SelectedRouteID: strings.TrimSpace(route.SelectedRouteID),
	})
	if !ok {
		return
	}
	entry := androidVPNDNSRealIPFakeEntry{
		Domain:          cleanDomain,
		RealIP:          realIP.String(),
		FakeIP:          fakeIP,
		Port:            cleanPort,
		Group:           strings.TrimSpace(route.Group),
		SelectedRouteID: strings.TrimSpace(route.SelectedRouteID),
		Protocol:        protocol,
		ExpiresAt:       time.Now().UTC().Add(vpnDNSCacheTTL),
	}
	configDir := currentAndroidVPNConfigDir()
	ensureAndroidVPNDNSCacheLoaded(configDir)
	vpnDNSState.mu.Lock()
	pruneAndroidVPNDNSFakeLocked(time.Now().UTC())
	if vpnDNSState.realIPToFake == nil {
		vpnDNSState.realIPToFake = map[string]androidVPNDNSRealIPFakeEntry{}
	}
	vpnDNSState.realIPToFake[androidVPNRealIPFakeKey(protocol, entry.RealIP, cleanPort)] = entry
	vpnDNSState.mu.Unlock()
	androidLogStore.add("vpn", "debug", "real-ip fake-ip mapping "+entry.RealIP+":"+cleanPort+" -> "+fakeIP+" domain="+cleanDomain+" group="+route.Group)
}

func rewriteAndroidVPNRealIPPacketToFake(packet []byte) ([]byte, bool, error) {
	info, ok := parseAndroidVPNIPv4TransportPacket(packet)
	if !ok {
		return nil, false, nil
	}
	vpnDNSState.mu.Lock()
	pruneAndroidVPNDNSFakeLocked(time.Now().UTC())
	entry, ok := vpnDNSState.realIPToFake[androidVPNRealIPFakeKey(info.Protocol, info.DestinationIP, strconv.Itoa(int(info.DestinationPort)))]
	vpnDNSState.mu.Unlock()
	if !ok || strings.TrimSpace(entry.FakeIP) == "" {
		return nil, false, nil
	}
	rewritten, err := rewriteAndroidVPNIPv4Packet(packet, "", entry.FakeIP)
	if err != nil {
		return nil, false, err
	}
	replyKey := androidVPNFlowKey(info.Protocol, entry.FakeIP, info.DestinationPort, info.SourceIP, info.SourcePort)
	entry.ExpiresAt = time.Now().UTC().Add(vpnDNSCacheTTL)
	vpnDNSState.mu.Lock()
	if vpnDNSState.fakeFlowToReal == nil {
		vpnDNSState.fakeFlowToReal = map[string]androidVPNDNSRealIPFakeEntry{}
	}
	vpnDNSState.fakeFlowToReal[replyKey] = entry
	vpnDNSState.mu.Unlock()
	return rewritten, true, nil
}

func rewriteAndroidVPNFakeIPPacketToReal(packet []byte) ([]byte, bool, error) {
	info, ok := parseAndroidVPNIPv4TransportPacket(packet)
	if !ok {
		return nil, false, nil
	}
	key := androidVPNFlowKey(info.Protocol, info.SourceIP, info.SourcePort, info.DestinationIP, info.DestinationPort)
	vpnDNSState.mu.Lock()
	pruneAndroidVPNDNSFakeLocked(time.Now().UTC())
	entry, ok := vpnDNSState.fakeFlowToReal[key]
	vpnDNSState.mu.Unlock()
	if !ok || strings.TrimSpace(entry.RealIP) == "" {
		return nil, false, nil
	}
	rewritten, err := rewriteAndroidVPNIPv4Packet(packet, entry.RealIP, "")
	if err != nil {
		return nil, false, err
	}
	return rewritten, true, nil
}

func parseAndroidVPNIPv4TransportPacket(packet []byte) (androidVPNIPv4TransportInfo, bool) {
	if len(packet) < 20 || packet[0]>>4 != 4 {
		return androidVPNIPv4TransportInfo{}, false
	}
	ihl := int(packet[0]&0x0f) * 4
	if ihl < 20 || len(packet) < ihl {
		return androidVPNIPv4TransportInfo{}, false
	}
	totalLen := int(binary.BigEndian.Uint16(packet[2:4]))
	if totalLen < ihl || totalLen > len(packet) {
		totalLen = len(packet)
	}
	protocol := packet[9]
	if protocol != 6 && protocol != 17 {
		return androidVPNIPv4TransportInfo{}, false
	}
	if totalLen < ihl+4 {
		return androidVPNIPv4TransportInfo{}, false
	}
	return androidVPNIPv4TransportInfo{
		Protocol:        protocol,
		SourceIP:        net.IPv4(packet[12], packet[13], packet[14], packet[15]).String(),
		DestinationIP:   net.IPv4(packet[16], packet[17], packet[18], packet[19]).String(),
		SourcePort:      binary.BigEndian.Uint16(packet[ihl : ihl+2]),
		DestinationPort: binary.BigEndian.Uint16(packet[ihl+2 : ihl+4]),
		HeaderLength:    ihl,
		TotalLength:     totalLen,
	}, true
}

func rewriteAndroidVPNIPv4Packet(packet []byte, sourceIP string, destinationIP string) ([]byte, error) {
	info, ok := parseAndroidVPNIPv4TransportPacket(packet)
	if !ok {
		return nil, errors.New("packet is not IPv4 TCP/UDP")
	}
	out := append([]byte(nil), packet...)
	if cleanSource := strings.TrimSpace(sourceIP); cleanSource != "" {
		ip := net.ParseIP(cleanSource).To4()
		if ip == nil {
			return nil, fmt.Errorf("invalid source IPv4: %s", cleanSource)
		}
		copy(out[12:16], ip)
	}
	if cleanDestination := strings.TrimSpace(destinationIP); cleanDestination != "" {
		ip := net.ParseIP(cleanDestination).To4()
		if ip == nil {
			return nil, fmt.Errorf("invalid destination IPv4: %s", cleanDestination)
		}
		copy(out[16:20], ip)
	}
	setAndroidVPNIPv4HeaderChecksum(out, info.HeaderLength)
	setAndroidVPNTransportChecksum(out, info)
	return out, nil
}

func setAndroidVPNIPv4HeaderChecksum(packet []byte, ihl int) {
	if len(packet) < ihl || ihl < 20 {
		return
	}
	packet[10] = 0
	packet[11] = 0
	binary.BigEndian.PutUint16(packet[10:12], androidVPNChecksum(packet[:ihl]))
}

func setAndroidVPNTransportChecksum(packet []byte, info androidVPNIPv4TransportInfo) {
	if len(packet) < info.TotalLength || info.TotalLength <= info.HeaderLength {
		return
	}
	transport := packet[info.HeaderLength:info.TotalLength]
	switch info.Protocol {
	case 6:
		if len(transport) < 18 {
			return
		}
		transport[16] = 0
		transport[17] = 0
		binary.BigEndian.PutUint16(transport[16:18], androidVPNTransportChecksum(packet, transport, info.Protocol))
	case 17:
		if len(transport) < 8 {
			return
		}
		transport[6] = 0
		transport[7] = 0
		checksum := androidVPNTransportChecksum(packet, transport, info.Protocol)
		if checksum == 0 {
			checksum = 0xffff
		}
		binary.BigEndian.PutUint16(transport[6:8], checksum)
	}
}

func androidVPNTransportChecksum(packet []byte, transport []byte, protocol uint8) uint16 {
	pseudo := make([]byte, 12+len(transport))
	copy(pseudo[0:4], packet[12:16])
	copy(pseudo[4:8], packet[16:20])
	pseudo[9] = protocol
	binary.BigEndian.PutUint16(pseudo[10:12], uint16(len(transport)))
	copy(pseudo[12:], transport)
	return androidVPNChecksum(pseudo)
}

func androidVPNChecksum(data []byte) uint16 {
	var sum uint32
	for len(data) >= 2 {
		sum += uint32(binary.BigEndian.Uint16(data[:2]))
		data = data[2:]
	}
	if len(data) > 0 {
		sum += uint32(data[0]) << 8
	}
	for sum>>16 != 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return ^uint16(sum)
}

func androidVPNRealIPFakeKey(protocol uint8, ip string, port string) string {
	return strconv.Itoa(int(protocol)) + "|" + strings.TrimSpace(ip) + "|" + strings.TrimSpace(port)
}

func androidVPNFlowKey(protocol uint8, sourceIP string, sourcePort uint16, destinationIP string, destinationPort uint16) string {
	return strconv.Itoa(int(protocol)) + "|" + strings.TrimSpace(sourceIP) + "|" + strconv.Itoa(int(sourcePort)) + "|" + strings.TrimSpace(destinationIP) + "|" + strconv.Itoa(int(destinationPort))
}

func lookupAndroidVPNDNSRouteHint(configDir string, ipText string, port string) (androidRouteDecision, bool) {
	ip := net.ParseIP(strings.TrimSpace(strings.Trim(ipText, "[]")))
	if ip == nil {
		return androidRouteDecision{}, false
	}
	ensureAndroidVPNDNSCacheLoaded(configDir)
	now := time.Now().UTC()
	vpnDNSState.mu.Lock()
	pruneAndroidVPNDNSFakeLocked(now)
	entry, ok := vpnDNSState.routeIPHints[ip.String()]
	vpnDNSState.mu.Unlock()
	if !ok || strings.TrimSpace(entry.Domain) == "" {
		return androidRouteDecision{}, false
	}
	route, err := decideAndroidRouteForTarget(net.JoinHostPort(entry.Domain, firstNonEmptyString(strings.TrimSpace(port), "443")))
	if err != nil {
		return androidRouteDecision{}, false
	}
	if !route.Direct && !route.Reject && strings.TrimSpace(route.SelectedRouteID) != "" {
		route.TargetAddr = net.JoinHostPort(entry.Domain, firstNonEmptyString(strings.TrimSpace(port), "443"))
	} else {
		route.TargetAddr = net.JoinHostPort(ip.String(), firstNonEmptyString(strings.TrimSpace(port), "443"))
	}
	return route, true
}

func buildAndroidVPNIPv4FallbackRoute(route vpnRouteDecision, err error) (vpnRouteDecision, bool) {
	if err == nil || !isTimeoutError(err) || route.Reject {
		return vpnRouteDecision{}, false
	}
	host, port, splitErr := net.SplitHostPort(strings.TrimSpace(route.TargetAddr))
	if splitErr != nil {
		return vpnRouteDecision{}, false
	}
	ip := net.ParseIP(strings.TrimSpace(strings.Trim(host, "[]")))
	if ip == nil || ip.To4() != nil {
		return vpnRouteDecision{}, false
	}
	hint, ok := lookupAndroidVPNDNSRouteHintEntry(ip.String())
	if !ok || strings.TrimSpace(hint.Domain) == "" {
		return vpnRouteDecision{}, false
	}
	ipv4s := append([]string(nil), hint.IPv4...)
	if len(ipv4s) == 0 {
		resolved, resolveErr := resolveAndroidVPNDomainIPv4s(hint.Domain)
		if resolveErr != nil || len(resolved) == 0 {
			return vpnRouteDecision{}, false
		}
		ipv4s = resolved
		rememberAndroidVPNDNSRouteHintIPv4s(ip.String(), ipv4s)
	}
	for _, ipv4 := range ipv4s {
		ip4 := net.ParseIP(strings.TrimSpace(ipv4)).To4()
		if ip4 == nil {
			continue
		}
		route4, routeErr := decideAndroidRouteForTarget(net.JoinHostPort(hint.Domain, firstNonEmptyString(strings.TrimSpace(port), "443")))
		if routeErr != nil || route4.Reject {
			continue
		}
		return vpnRouteDecision{
			Direct:          route4.Direct,
			Reject:          route4.Reject,
			TargetAddr:      net.JoinHostPort(ip4.String(), firstNonEmptyString(strings.TrimSpace(port), "443")),
			Group:           firstNonEmptyString(strings.TrimSpace(route4.Group), strings.TrimSpace(hint.Group)),
			SelectedRouteID: route4.SelectedRouteID,
		}, true
	}
	return vpnRouteDecision{}, false
}

func lookupAndroidVPNDNSRouteHintEntry(ipText string) (androidVPNDNSRouteHintEntry, bool) {
	ip := net.ParseIP(strings.TrimSpace(strings.Trim(ipText, "[]")))
	if ip == nil {
		return androidVPNDNSRouteHintEntry{}, false
	}
	ensureAndroidVPNDNSCacheLoaded(currentAndroidVPNConfigDir())
	now := time.Now().UTC()
	vpnDNSState.mu.Lock()
	defer vpnDNSState.mu.Unlock()
	pruneAndroidVPNDNSFakeLocked(now)
	entry, ok := vpnDNSState.routeIPHints[ip.String()]
	if !ok || strings.TrimSpace(entry.Domain) == "" {
		return androidVPNDNSRouteHintEntry{}, false
	}
	entry.IPv4 = append([]string(nil), entry.IPv4...)
	entry.IPv6 = append([]string(nil), entry.IPv6...)
	return entry, true
}

func rememberAndroidVPNDNSRouteHintIPv4s(ipText string, ipv4s []string) {
	ip := net.ParseIP(strings.TrimSpace(strings.Trim(ipText, "[]")))
	if ip == nil || len(ipv4s) == 0 {
		return
	}
	configDir := currentAndroidVPNConfigDir()
	ensureAndroidVPNDNSCacheLoaded(configDir)
	now := time.Now().UTC()
	vpnDNSState.mu.Lock()
	pruneAndroidVPNDNSFakeLocked(now)
	entry, ok := vpnDNSState.routeIPHints[ip.String()]
	if !ok {
		vpnDNSState.mu.Unlock()
		return
	}
	entry.IPv4 = append([]string(nil), ipv4s...)
	entry.ExpiresAt = now.Add(vpnDNSCacheTTL)
	vpnDNSState.routeIPHints[ip.String()] = entry
	vpnDNSState.mu.Unlock()
	markAndroidVPNDNSCacheDirty(configDir)
}

func extractAndroidVPNDNSResponseIPs(packet []byte) []string {
	parser := dnsmessage.Parser{}
	if _, err := parser.Start(packet); err != nil {
		return nil
	}
	for {
		if _, err := parser.Question(); err != nil {
			if errors.Is(err, dnsmessage.ErrSectionDone) {
				break
			}
			return nil
		}
	}
	seen := map[string]struct{}{}
	var out []string
	for {
		header, err := parser.AnswerHeader()
		if err != nil {
			if errors.Is(err, dnsmessage.ErrSectionDone) {
				break
			}
			return out
		}
		switch header.Type {
		case dnsmessage.TypeA:
			answer, err := parser.AResource()
			if err != nil {
				return out
			}
			ip := net.IP(answer.A[:]).String()
			if ip == "" {
				continue
			}
			if _, ok := seen[ip]; ok {
				continue
			}
			seen[ip] = struct{}{}
			out = append(out, ip)
		case dnsmessage.TypeAAAA:
			answer, err := parser.AAAAResource()
			if err != nil {
				return out
			}
			ip := net.IP(answer.AAAA[:]).String()
			if ip == "" {
				continue
			}
			if _, ok := seen[ip]; ok {
				continue
			}
			seen[ip] = struct{}{}
			out = append(out, ip)
		default:
			if err := parser.SkipAnswer(); err != nil {
				return out
			}
		}
	}
	return out
}

func splitAndroidVPNDNSResponseIPFamilies(ips []string) ([]string, []string) {
	seen4 := map[string]struct{}{}
	seen6 := map[string]struct{}{}
	var ipv4 []string
	var ipv6 []string
	for _, value := range ips {
		ip := net.ParseIP(strings.TrimSpace(strings.Trim(value, "[]")))
		if ip == nil {
			continue
		}
		if ip4 := ip.To4(); ip4 != nil {
			text := ip4.String()
			if _, ok := seen4[text]; ok {
				continue
			}
			seen4[text] = struct{}{}
			ipv4 = append(ipv4, text)
			continue
		}
		text := ip.String()
		if _, ok := seen6[text]; ok {
			continue
		}
		seen6[text] = struct{}{}
		ipv6 = append(ipv6, text)
	}
	return ipv4, ipv6
}

func resolveAndroidVPNDomainIPv4s(domain string) ([]string, error) {
	query, err := buildAndroidVPNDNSQuery(domain, dnsmessage.TypeA)
	if err != nil {
		return nil, err
	}
	response, err := queryAndroidVPNDNSUpstream(query)
	if err != nil {
		return nil, err
	}
	ips := extractAndroidVPNDNSResponseIPs(response)
	ipv4, _ := splitAndroidVPNDNSResponseIPFamilies(ips)
	if len(ipv4) == 0 {
		return nil, errors.New("empty ipv4 dns response")
	}
	return ipv4, nil
}

func buildAndroidVPNDNSQuery(domain string, qType dnsmessage.Type) ([]byte, error) {
	cleanDomain := strings.TrimSpace(strings.Trim(domain, "."))
	if cleanDomain == "" {
		return nil, errors.New("dns domain is empty")
	}
	name, err := dnsmessage.NewName(cleanDomain + ".")
	if err != nil {
		return nil, err
	}
	builder := dnsmessage.NewBuilder(nil, dnsmessage.Header{
		ID:               uint16(time.Now().UnixNano()),
		RecursionDesired: true,
	})
	if err := builder.StartQuestions(); err != nil {
		return nil, err
	}
	if err := builder.Question(dnsmessage.Question{Name: name, Type: qType, Class: dnsmessage.ClassINET}); err != nil {
		return nil, err
	}
	return builder.Finish()
}

func snapshotAndroidVPNDNSStatus() map[string]any {
	now := time.Now().UTC()
	ensureAndroidVPNDNSCacheLoaded(currentAndroidVPNConfigDir())
	vpnDNSState.mu.Lock()
	defer vpnDNSState.mu.Unlock()
	pruneAndroidVPNDNSFakeLocked(now)
	fakeItems := make([]map[string]any, 0, len(vpnDNSState.fakeIPToEntry))
	for ip, entry := range vpnDNSState.fakeIPToEntry {
		fakeItems = append(fakeItems, map[string]any{
			"ip":                ip,
			"domain":            entry.Domain,
			"group":             entry.Group,
			"direct":            entry.Direct,
			"reject":            entry.Reject,
			"selected_route_id": entry.SelectedRouteID,
			"expires_at":        entry.ExpiresAt.UTC().Format(time.RFC3339),
		})
	}
	routeItems := make([]map[string]any, 0, len(vpnDNSState.routeIPHints))
	for ip, entry := range vpnDNSState.routeIPHints {
		routeItems = append(routeItems, map[string]any{
			"ip":         ip,
			"domain":     entry.Domain,
			"ipv4_count": len(entry.IPv4),
			"ipv6_count": len(entry.IPv6),
			"group":      entry.Group,
			"expires_at": entry.ExpiresAt.UTC().Format(time.RFC3339),
		})
	}
	natItems := make([]map[string]any, 0, len(vpnDNSState.realIPToFake))
	for _, entry := range vpnDNSState.realIPToFake {
		natItems = append(natItems, map[string]any{
			"domain":            entry.Domain,
			"real_ip":           entry.RealIP,
			"fake_ip":           entry.FakeIP,
			"port":              entry.Port,
			"group":             entry.Group,
			"selected_route_id": entry.SelectedRouteID,
			"protocol":          entry.Protocol,
			"expires_at":        entry.ExpiresAt.UTC().Format(time.RFC3339),
		})
	}
	if len(fakeItems) > 8 {
		fakeItems = fakeItems[:8]
	}
	if len(routeItems) > 8 {
		routeItems = routeItems[:8]
	}
	if len(natItems) > 8 {
		natItems = natItems[:8]
	}
	return map[string]any{
		"enabled":          true,
		"listen":           "10.111.0.1:53",
		"fake_ip_cidr":     "198.18.0.0/15",
		"fake_ip_count":    len(vpnDNSState.fakeIPToEntry),
		"route_hint_count": len(vpnDNSState.routeIPHints),
		"real_nat_count":   len(vpnDNSState.realIPToFake),
		"flow_nat_count":   len(vpnDNSState.fakeFlowToReal),
		"fake_ip_items":    fakeItems,
		"route_hint_items": routeItems,
		"real_nat_items":   natItems,
	}
}

func shouldUseAndroidVPNDNSFakeIP(route androidRouteDecision, qType dnsmessage.Type, domain string) bool {
	if qType != dnsmessage.TypeA {
		return false
	}
	if route.Direct || route.Reject {
		return false
	}
	if strings.TrimSpace(route.SelectedRouteID) == "" {
		return false
	}
	return strings.TrimSpace(domain) != ""
}

func allocateAndroidVPNDNSFakeIP(domain string, route androidRouteDecision) (string, bool) {
	cleanDomain := strings.TrimSpace(strings.ToLower(strings.Trim(domain, ".")))
	if cleanDomain == "" {
		return "", false
	}
	configDir := currentAndroidVPNConfigDir()
	ensureAndroidVPNDNSCacheLoaded(configDir)
	now := time.Now().UTC()
	remoteRoute := !route.Direct && !route.Reject && strings.TrimSpace(route.SelectedRouteID) != ""
	vpnDNSState.mu.Lock()
	pruneAndroidVPNDNSFakeLocked(now)
	existingIP := vpnDNSState.fakeDomainToIP[cleanDomain]
	if existingIP != "" {
		entry := vpnDNSState.fakeIPToEntry[existingIP]
		if !remoteRoute || entry.ControllerManaged {
			entry.Domain = cleanDomain
			entry.Group = strings.TrimSpace(route.Group)
			entry.Direct = route.Direct
			entry.Reject = route.Reject
			entry.SelectedRouteID = strings.TrimSpace(route.SelectedRouteID)
			entry.ExpiresAt = now.Add(vpnDNSCacheTTL)
			vpnDNSState.fakeIPToEntry[existingIP] = entry
			vpnDNSState.mu.Unlock()
			markAndroidVPNDNSCacheDirty(configDir)
			return existingIP, true
		}
	}
	vpnDNSState.mu.Unlock()
	if remoteRoute {
		fakeIP, controllerAvailable, err := requestMobileVRouteControllerFakeIP(cleanDomain, route)
		if err != nil {
			logAndroidVPNDiagnostic("fake_ip_controller_"+route.SelectedRouteID, "error", "controller fake ip allocation failed: domain="+cleanDomain+" route="+route.SelectedRouteID+" err="+err.Error(), 2*time.Second)
			return "", false
		}
		if controllerAvailable {
			return storeAndroidVPNControllerFakeIP(configDir, cleanDomain, fakeIP, route, now)
		}
		if existingIP != "" {
			vpnDNSState.mu.Lock()
			entry := vpnDNSState.fakeIPToEntry[existingIP]
			entry.Domain = cleanDomain
			entry.Group = strings.TrimSpace(route.Group)
			entry.Direct = route.Direct
			entry.Reject = route.Reject
			entry.SelectedRouteID = strings.TrimSpace(route.SelectedRouteID)
			entry.ExpiresAt = now.Add(vpnDNSCacheTTL)
			vpnDNSState.fakeIPToEntry[existingIP] = entry
			vpnDNSState.mu.Unlock()
			markAndroidVPNDNSCacheDirty(configDir)
			return existingIP, true
		}
	}
	vpnDNSState.mu.Lock()
	pruneAndroidVPNDNSFakeLocked(now)
	for attempts := 0; attempts < 131000; attempts++ {
		ip := nextAndroidVPNDNSFakeIPLocked()
		if ip == "" {
			vpnDNSState.mu.Unlock()
			return "", false
		}
		if _, exists := vpnDNSState.fakeIPToEntry[ip]; exists {
			continue
		}
		vpnDNSState.fakeDomainToIP[cleanDomain] = ip
		vpnDNSState.fakeIPToEntry[ip] = androidVPNDNSFakeEntry{
			Domain:            cleanDomain,
			Group:             strings.TrimSpace(route.Group),
			Direct:            route.Direct,
			Reject:            route.Reject,
			SelectedRouteID:   strings.TrimSpace(route.SelectedRouteID),
			ControllerManaged: false,
			ExpiresAt:         now.Add(vpnDNSCacheTTL),
		}
		vpnDNSState.mu.Unlock()
		markAndroidVPNDNSCacheDirty(configDir)
		return ip, true
	}
	vpnDNSState.mu.Unlock()
	return "", false
}

func storeAndroidVPNControllerFakeIP(configDir string, domain string, fakeIP string, route androidRouteDecision, now time.Time) (string, bool) {
	cleanDomain := normalizeMobileVRouteDomain(domain)
	ip := net.ParseIP(strings.TrimSpace(fakeIP)).To4()
	if cleanDomain == "" || ip == nil {
		return "", false
	}
	ipText := ip.String()
	vpnDNSState.mu.Lock()
	pruneAndroidVPNDNSFakeLocked(now)
	if oldIP := vpnDNSState.fakeDomainToIP[cleanDomain]; oldIP != "" && oldIP != ipText {
		delete(vpnDNSState.fakeIPToEntry, oldIP)
	}
	if oldEntry, exists := vpnDNSState.fakeIPToEntry[ipText]; exists && normalizeMobileVRouteDomain(oldEntry.Domain) != cleanDomain {
		delete(vpnDNSState.fakeDomainToIP, normalizeMobileVRouteDomain(oldEntry.Domain))
	}
	vpnDNSState.fakeDomainToIP[cleanDomain] = ipText
	vpnDNSState.fakeIPToEntry[ipText] = androidVPNDNSFakeEntry{
		Domain:            cleanDomain,
		Group:             strings.TrimSpace(route.Group),
		Direct:            route.Direct,
		Reject:            route.Reject,
		SelectedRouteID:   strings.TrimSpace(route.SelectedRouteID),
		ControllerManaged: true,
		ExpiresAt:         now.Add(vpnDNSCacheTTL),
	}
	vpnDNSState.mu.Unlock()
	markAndroidVPNDNSCacheDirty(configDir)
	return ipText, true
}

func nextAndroidVPNDNSFakeIPLocked() string {
	const fakeSize uint32 = 2 * 256 * 256
	offset := vpnDNSState.nextFakeOffset
	if offset < 2 || offset >= fakeSize-1 {
		offset = 2
	}
	vpnDNSState.nextFakeOffset = offset + 1
	second := byte(18 + offset/65536)
	third := byte((offset / 256) % 256)
	fourth := byte(offset % 256)
	return net.IPv4(198, second, third, fourth).String()
}

func pruneAndroidVPNDNSFakeLocked(now time.Time) {
	pruneAndroidVPNDNSStateLocked(vpnDNSState, now)
}

func pruneAndroidVPNDNSStateLocked(state *androidVPNDNSState, now time.Time) {
	if state == nil {
		return
	}
	for ip, entry := range state.fakeIPToEntry {
		if entry.ExpiresAt.IsZero() || now.Before(entry.ExpiresAt) {
			continue
		}
		delete(state.fakeIPToEntry, ip)
		if strings.TrimSpace(entry.Domain) != "" && state.fakeDomainToIP[entry.Domain] == ip {
			delete(state.fakeDomainToIP, entry.Domain)
		}
	}
	for ip, entry := range state.routeIPHints {
		if entry.ExpiresAt.IsZero() || now.Before(entry.ExpiresAt) {
			continue
		}
		delete(state.routeIPHints, ip)
	}
	for key, entry := range state.realIPToFake {
		if entry.ExpiresAt.IsZero() || now.Before(entry.ExpiresAt) {
			continue
		}
		delete(state.realIPToFake, key)
	}
	for key, entry := range state.fakeFlowToReal {
		if entry.ExpiresAt.IsZero() || now.Before(entry.ExpiresAt) {
			continue
		}
		delete(state.fakeFlowToReal, key)
	}
}

func rewriteAndroidVPNFakeIPTarget(targetAddr string) (string, androidVPNDNSFakeEntry, bool) {
	host, port, err := net.SplitHostPort(strings.TrimSpace(targetAddr))
	if err != nil {
		return "", androidVPNDNSFakeEntry{}, false
	}
	cleanHost := strings.TrimSpace(strings.Trim(host, "[]"))
	if net.ParseIP(cleanHost) == nil {
		return "", androidVPNDNSFakeEntry{}, false
	}
	ensureAndroidVPNDNSCacheLoaded(currentAndroidVPNConfigDir())
	now := time.Now().UTC()
	vpnDNSState.mu.Lock()
	defer vpnDNSState.mu.Unlock()
	pruneAndroidVPNDNSFakeLocked(now)
	entry, ok := vpnDNSState.fakeIPToEntry[net.ParseIP(cleanHost).String()]
	if !ok || strings.TrimSpace(entry.Domain) == "" {
		return "", androidVPNDNSFakeEntry{}, false
	}
	return net.JoinHostPort(entry.Domain, strings.TrimSpace(port)), entry, true
}

func isAndroidVPNDNSTarget(targetAddr string) bool {
	host, port, err := net.SplitHostPort(strings.TrimSpace(targetAddr))
	if err != nil {
		return false
	}
	if strings.TrimSpace(port) != "53" {
		return false
	}
	ip := net.ParseIP(strings.TrimSpace(strings.Trim(host, "[]")))
	return ip != nil
}

func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(text, "i/o timeout") ||
		strings.Contains(text, "timeout") ||
		strings.Contains(text, "deadline exceeded") ||
		strings.Contains(text, "deadline reached")
}

func cloneVPNMap(in map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range in {
		out[key] = value
	}
	return out
}

func stringFromAny(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	case nil:
		return ""
	default:
		return fmt.Sprint(v)
	}
}

func parseRFC3339Time(value string) (time.Time, bool) {
	if strings.TrimSpace(value) == "" {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(value))
	return parsed, err == nil
}

func vpnTransportIDToTarget(addr tcpip.Address, port uint16) (string, error) {
	if port == 0 {
		return "", errors.New("transport target port is empty")
	}
	host := strings.TrimSpace(addr.String())
	if host == "" {
		return "", errors.New("transport target address is empty")
	}
	return net.JoinHostPort(host, strconv.Itoa(int(port))), nil
}

func vpnProtocolFromPacket(packet []byte) (tcpip.NetworkProtocolNumber, error) {
	if len(packet) == 0 {
		return 0, errors.New("empty packet")
	}
	switch packet[0] >> 4 {
	case 4:
		return ipv4.ProtocolNumber, nil
	case 6:
		return 0, errors.New("android vpn ipv6 disabled")
	default:
		return 0, errors.New("unsupported ip version")
	}
}

func tcpipErrToError(err tcpip.Error) error {
	if err == nil {
		return nil
	}
	return errors.New(err.String())
}

func recordVPNRuntimeError(stage string, err error) {
	if err == nil {
		return
	}
	message := strings.TrimSpace(stage)
	if message == "" {
		message = "vpn"
	}
	message += ": " + err.Error()
	vpnRuntime.mu.Lock()
	vpnRuntime.lastError = message
	vpnRuntime.updatedAt = time.Now().UTC().Format(time.RFC3339)
	vpnRuntime.mu.Unlock()
	androidLogStore.add("vpn", "error", message)
}

func closeVPNWrite(conn net.Conn) {
	if conn == nil {
		return
	}
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		_ = tcpConn.CloseWrite()
		return
	}
	_ = conn.Close()
}

func closeVPNRead(conn net.Conn) {
	if conn == nil {
		return
	}
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		_ = tcpConn.CloseRead()
		return
	}
	_ = conn.Close()
}
