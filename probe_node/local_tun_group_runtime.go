package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
)

type probeLocalTUNChainEndpoint struct {
	ChainID           string
	ChainName         string
	EntryNodeID       string
	EntryHost         string
	EntryPort         int
	LinkLayer         string
	ChainSecret       string
	Unavailable       bool
	UnavailableReason string
}

type probeLocalTUNGroupRuntime struct {
	mu sync.Mutex

	Group           string
	SelectedChainID string
	Endpoint        probeLocalTUNChainEndpoint
	SessionID       string
	RuntimeStatus   string
	LastError       string
	FailureCount    int
	UpdatedAt       string
	LastConnectedAt string

	relayConn net.Conn
	session   *yamux.Session
}

type probeLocalTUNGroupRuntimeSnapshot struct {
	Group           string `json:"group"`
	SelectedChainID string `json:"selected_chain_id,omitempty"`
	EntryNodeID     string `json:"entry_node_id,omitempty"`
	EntryHost       string `json:"entry_host,omitempty"`
	EntryPort       int    `json:"entry_port,omitempty"`
	LinkLayer       string `json:"link_layer,omitempty"`
	RuntimeStatus   string `json:"runtime_status,omitempty"`
	LastError       string `json:"last_error,omitempty"`
	FailureCount    int    `json:"failure_count,omitempty"`
	UpdatedAt       string `json:"updated_at,omitempty"`
	LastConnectedAt string `json:"last_connected_at,omitempty"`
	Connected       bool   `json:"connected"`
}

var probeLocalTUNGroupRuntimeRegistry = struct {
	mu    sync.RWMutex
	items map[string]*probeLocalTUNGroupRuntime
}{items: make(map[string]*probeLocalTUNGroupRuntime)}

func normalizeProbeLocalGroupKey(group string) string {
	return strings.ToLower(strings.TrimSpace(group))
}

func normalizeProbeLocalSelectedChainID(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	if len(trimmed) >= len("chain:") && strings.EqualFold(trimmed[:len("chain:")], "chain:") {
		trimmed = strings.TrimSpace(trimmed[len("chain:"):])
	}
	if trimmed == "" {
		return "", &probeLocalHTTPError{Status: 400, Message: "selected_chain_id is invalid"}
	}
	return trimmed, nil
}

func formatProbeLocalLegacyTunnelNodeID(selectedChainID string) string {
	cleanID := strings.TrimSpace(selectedChainID)
	if cleanID == "" {
		return ""
	}
	return "chain:" + cleanID
}

func currentProbeLocalTUNGroupRuntime(group string) *probeLocalTUNGroupRuntime {
	key := normalizeProbeLocalGroupKey(group)
	if key == "" {
		return nil
	}
	probeLocalTUNGroupRuntimeRegistry.mu.RLock()
	rt := probeLocalTUNGroupRuntimeRegistry.items[key]
	probeLocalTUNGroupRuntimeRegistry.mu.RUnlock()
	return rt
}

func getOrCreateProbeLocalTUNGroupRuntime(group string) *probeLocalTUNGroupRuntime {
	cleanGroup := strings.TrimSpace(group)
	key := normalizeProbeLocalGroupKey(cleanGroup)
	if key == "" {
		return nil
	}
	probeLocalTUNGroupRuntimeRegistry.mu.Lock()
	defer probeLocalTUNGroupRuntimeRegistry.mu.Unlock()
	if rt := probeLocalTUNGroupRuntimeRegistry.items[key]; rt != nil {
		return rt
	}
	rt := &probeLocalTUNGroupRuntime{
		Group:         cleanGroup,
		RuntimeStatus: "idle",
		UpdatedAt:     time.Now().UTC().Format(time.RFC3339),
	}
	probeLocalTUNGroupRuntimeRegistry.items[key] = rt
	return rt
}

func resetProbeLocalTUNGroupRuntimeRegistryForTest() {
	probeLocalTUNGroupRuntimeRegistry.mu.Lock()
	items := probeLocalTUNGroupRuntimeRegistry.items
	probeLocalTUNGroupRuntimeRegistry.items = make(map[string]*probeLocalTUNGroupRuntime)
	probeLocalTUNGroupRuntimeRegistry.mu.Unlock()
	for _, rt := range items {
		if rt != nil {
			rt.close()
		}
	}
}

func ensureProbeLocalTUNGroupRuntime(group string, selectedChainID string) (*probeLocalTUNGroupRuntime, error) {
	cleanGroup := strings.TrimSpace(group)
	if cleanGroup == "" {
		return nil, &probeLocalHTTPError{Status: 400, Message: "group is required"}
	}
	chainID, err := normalizeProbeLocalSelectedChainID(selectedChainID)
	if err != nil {
		return nil, err
	}
	if chainID == "" {
		return nil, &probeLocalHTTPError{Status: 400, Message: "selected_chain_id is required"}
	}
	rt := getOrCreateProbeLocalTUNGroupRuntime(cleanGroup)
	if rt == nil {
		return nil, errors.New("group runtime is nil")
	}
	rt.mu.Lock()
	rt.Group = cleanGroup
	if !strings.EqualFold(strings.TrimSpace(rt.SelectedChainID), chainID) {
		rt.closeLocked()
		rt.Endpoint = probeLocalTUNChainEndpoint{}
		rt.SessionID = ""
		rt.SelectedChainID = chainID
		rt.RuntimeStatus = "selected"
		rt.LastError = ""
		rt.LastConnectedAt = ""
		rt.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	} else if strings.TrimSpace(rt.RuntimeStatus) == "" {
		rt.RuntimeStatus = "selected"
		rt.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	rt.mu.Unlock()
	return rt, nil
}

func syncProbeLocalTUNGroupRuntimeSelection(group, selectedChainID string) {
	rt, err := ensureProbeLocalTUNGroupRuntime(group, selectedChainID)
	if err != nil || rt == nil {
		return
	}
}

func resolveProbeLocalSelectedGroupRuntime(state probeLocalProxyStateFile) (string, *probeLocalTUNGroupRuntime) {
	for _, entry := range state.Groups {
		cleanGroup := strings.TrimSpace(entry.Group)
		if cleanGroup == "" {
			continue
		}
		selectedChainID := firstNonEmpty(
			mustProbeLocalSelectedChainIDFromLegacy(entry.TunnelNodeID),
			strings.TrimSpace(entry.SelectedChainID),
		)
		if selectedChainID == "" {
			continue
		}
		if rt := currentProbeLocalTUNGroupRuntime(cleanGroup); rt != nil {
			return cleanGroup, rt
		}
	}
	return "", nil
}

func mustProbeLocalSelectedChainIDFromLegacy(raw string) string {
	selectedChainID, err := normalizeProbeLocalSelectedChainID(raw)
	if err != nil {
		return ""
	}
	return selectedChainID
}

func resolveProbeLocalTUNGroupRuntimeKeepaliveAndLatency(rt *probeLocalTUNGroupRuntime) (string, *int64, string, string) {
	if rt == nil {
		return "none", nil, "", ""
	}
	snapshot := rt.snapshot()
	if strings.TrimSpace(snapshot.SelectedChainID) == "" {
		return "none", nil, "", ""
	}
	if !snapshot.Connected {
		if strings.TrimSpace(snapshot.LastError) != "" {
			return firstNonEmpty(strings.TrimSpace(snapshot.RuntimeStatus), "disconnected"), nil, "", strings.TrimSpace(snapshot.LastError)
		}
		return firstNonEmpty(strings.TrimSpace(snapshot.RuntimeStatus), "disconnected"), nil, "", ""
	}
	if strings.TrimSpace(snapshot.EntryHost) == "" || snapshot.EntryPort <= 0 {
		return firstNonEmpty(strings.TrimSpace(snapshot.RuntimeStatus), "connected"), nil, "", "entry target is unavailable"
	}
	addr := net.JoinHostPort(strings.TrimSpace(snapshot.EntryHost), strconv.Itoa(snapshot.EntryPort))
	startedAt := time.Now()
	conn, err := net.DialTimeout("tcp", addr, 1800*time.Millisecond)
	if err != nil {
		return firstNonEmpty(strings.TrimSpace(snapshot.RuntimeStatus), "connected"), nil, "", strings.TrimSpace(err.Error())
	}
	_ = conn.Close()
	latencyMS := int64(time.Since(startedAt) / time.Millisecond)
	if latencyMS < 0 {
		latencyMS = 0
	}
	return firstNonEmpty(strings.TrimSpace(snapshot.RuntimeStatus), "connected"), &latencyMS, time.Now().UTC().Format(time.RFC3339), ""
}

func resolveProbeLocalChainEntryEndpointByID(selectedChainID string) (probeLocalTUNChainEndpoint, error) {
	chainID, err := normalizeProbeLocalSelectedChainID(selectedChainID)
	if err != nil {
		return probeLocalTUNChainEndpoint{}, err
	}
	if chainID == "" {
		return probeLocalTUNChainEndpoint{}, errors.New("selected_chain_id is required")
	}
	items, err := loadProbeLocalProxyChainItems()
	if err != nil {
		return probeLocalTUNChainEndpoint{}, err
	}
	for _, item := range items {
		if !strings.EqualFold(strings.TrimSpace(item.ChainID), chainID) {
			continue
		}
		if len(buildChainRoute(item)) == 0 {
			return probeLocalTUNChainEndpoint{}, fmt.Errorf("chain route is empty: %s", chainID)
		}
		entryNodeID := strings.TrimSpace(buildChainRoute(item)[0])
		entryHost := ""
		entryPort := 0
		linkLayer := normalizeProbeChainLinkLayer(strings.TrimSpace(item.LinkLayer))
		for _, hop := range item.HopConfigs {
			hopNodeID := normalizeProbeChainNodeID(strconv.Itoa(hop.NodeNo))
			if hopNodeID == "" || hopNodeID != normalizeProbeChainNodeID(entryNodeID) {
				continue
			}
			entryHost = strings.TrimSpace(hop.RelayHost)
			if hop.ExternalPort > 0 {
				entryPort = hop.ExternalPort
			} else if hop.ListenPort > 0 {
				entryPort = hop.ListenPort
			}
			linkLayer = normalizeProbeChainLinkLayer(firstNonEmpty(strings.TrimSpace(hop.LinkLayer), strings.TrimSpace(item.LinkLayer), "http"))
			break
		}
		if entryHost == "" {
			return probeLocalTUNChainEndpoint{}, fmt.Errorf("selected chain entry host is unavailable: %s", chainID)
		}
		if entryPort <= 0 {
			return probeLocalTUNChainEndpoint{}, fmt.Errorf("selected chain entry port is unavailable: %s", chainID)
		}
		if linkLayer == "" {
			linkLayer = "http"
		}
		return probeLocalTUNChainEndpoint{
			ChainID:           chainID,
			ChainName:         strings.TrimSpace(item.Name),
			EntryNodeID:       entryNodeID,
			EntryHost:         entryHost,
			EntryPort:         entryPort,
			LinkLayer:         linkLayer,
			ChainSecret:       strings.TrimSpace(item.Secret),
			Unavailable:       false,
			UnavailableReason: "",
		}, nil
	}
	return probeLocalTUNChainEndpoint{}, &probeLocalHTTPError{Status: 400, Message: fmt.Sprintf("selected_chain_id %q not found in proxy chains", strings.TrimSpace(selectedChainID))}
}

func (rt *probeLocalTUNGroupRuntime) snapshot() probeLocalTUNGroupRuntimeSnapshot {
	if rt == nil {
		return probeLocalTUNGroupRuntimeSnapshot{}
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	connected := rt.session != nil && !rt.session.IsClosed()
	return probeLocalTUNGroupRuntimeSnapshot{
		Group:           strings.TrimSpace(rt.Group),
		SelectedChainID: strings.TrimSpace(rt.SelectedChainID),
		EntryNodeID:     strings.TrimSpace(rt.Endpoint.EntryNodeID),
		EntryHost:       strings.TrimSpace(rt.Endpoint.EntryHost),
		EntryPort:       rt.Endpoint.EntryPort,
		LinkLayer:       strings.TrimSpace(rt.Endpoint.LinkLayer),
		RuntimeStatus:   strings.TrimSpace(rt.RuntimeStatus),
		LastError:       strings.TrimSpace(rt.LastError),
		FailureCount:    rt.FailureCount,
		UpdatedAt:       strings.TrimSpace(rt.UpdatedAt),
		LastConnectedAt: strings.TrimSpace(rt.LastConnectedAt),
		Connected:       connected,
	}
}

func (rt *probeLocalTUNGroupRuntime) close() {
	if rt == nil {
		return
	}
	rt.mu.Lock()
	rt.closeLocked()
	rt.mu.Unlock()
}

func (rt *probeLocalTUNGroupRuntime) closeLocked() {
	if rt == nil {
		return
	}
	if rt.session != nil {
		_ = rt.session.Close()
		rt.session = nil
	}
	if rt.relayConn != nil {
		_ = rt.relayConn.Close()
		rt.relayConn = nil
	}
	if strings.TrimSpace(rt.RuntimeStatus) == "connected" {
		rt.RuntimeStatus = "disconnected"
	}
	rt.SessionID = ""
}

func (rt *probeLocalTUNGroupRuntime) markFailureLocked(err error, status string) error {
	if rt == nil {
		if err == nil {
			return errors.New("group runtime is nil")
		}
		return err
	}
	rt.FailureCount++
	rt.LastError = strings.TrimSpace(firstProbeLocalTUNErr(err, errors.New("group runtime failure")).Error())
	rt.RuntimeStatus = firstNonEmpty(strings.TrimSpace(status), "unavailable")
	rt.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return err
}

func (rt *probeLocalTUNGroupRuntime) ensureConnectedLocked() error {
	if rt == nil {
		return errors.New("group runtime is nil")
	}
	if rt.session != nil && !rt.session.IsClosed() {
		if strings.TrimSpace(rt.RuntimeStatus) == "" {
			rt.RuntimeStatus = "connected"
		}
		return nil
	}
	chainID := strings.TrimSpace(rt.SelectedChainID)
	if chainID == "" {
		return rt.markFailureLocked(errors.New("selected_chain_id is empty"), "unavailable")
	}
	endpoint, err := resolveProbeLocalChainEntryEndpointByID(chainID)
	if err != nil {
		return rt.markFailureLocked(err, "unavailable")
	}
	conn, err := openProbeChainRelayNetConn(endpoint.ChainID, endpoint.ChainSecret, endpoint.EntryHost, endpoint.EntryPort, endpoint.LinkLayer, probeChainBridgeRoleToNext)
	if err != nil {
		return rt.markFailureLocked(err, "unavailable")
	}
	session, err := yamux.Client(conn, newProbeChainYamuxConfig())
	if err != nil {
		_ = conn.Close()
		return rt.markFailureLocked(err, "unavailable")
	}
	rt.closeLocked()
	rt.Endpoint = endpoint
	rt.relayConn = conn
	rt.session = session
	rt.SessionID = ""
	rt.RuntimeStatus = "connected"
	rt.LastError = ""
	now := time.Now().UTC().Format(time.RFC3339)
	rt.UpdatedAt = now
	rt.LastConnectedAt = now
	return nil
}

func (rt *probeLocalTUNGroupRuntime) openStream(network string, targetAddr string, associationV2 *probeChainAssociationV2Meta) (net.Conn, error) {
	if rt == nil {
		return nil, errors.New("group runtime is nil")
	}
	cleanNetwork := strings.ToLower(strings.TrimSpace(network))
	if cleanNetwork == "" {
		cleanNetwork = "tcp"
	}
	for attempt := 0; attempt < 2; attempt++ {
		rt.mu.Lock()
		if err := rt.ensureConnectedLocked(); err != nil {
			rt.mu.Unlock()
			return nil, err
		}
		session := rt.session
		rt.mu.Unlock()
		if session == nil {
			return nil, errors.New("group runtime session is nil")
		}
		stream, err := session.Open()
		if err != nil {
			rt.mu.Lock()
			if rt.session == session {
				rt.closeLocked()
				_ = rt.markFailureLocked(err, "disconnected")
			}
			rt.mu.Unlock()
			if attempt == 0 {
				continue
			}
			return nil, err
		}
		request := probeChainTunnelOpenRequest{
			Type:          "open",
			Network:       cleanNetwork,
			Address:       strings.TrimSpace(targetAddr),
			AssociationV2: associationV2,
		}
		_ = stream.SetWriteDeadline(time.Now().Add(probeChainPortForwardResponseReadDeadline))
		if err := json.NewEncoder(stream).Encode(request); err != nil {
			_ = stream.Close()
			rt.mu.Lock()
			_ = rt.markFailureLocked(err, "disconnected")
			rt.mu.Unlock()
			return nil, err
		}
		_ = stream.SetWriteDeadline(time.Time{})
		_ = stream.SetReadDeadline(time.Now().Add(probeChainPortForwardResponseReadDeadline))
		var response probeChainTunnelOpenResponse
		if err := json.NewDecoder(stream).Decode(&response); err != nil {
			_ = stream.Close()
			rt.mu.Lock()
			_ = rt.markFailureLocked(err, "disconnected")
			rt.mu.Unlock()
			return nil, err
		}
		_ = stream.SetReadDeadline(time.Time{})
		if !response.OK {
			_ = stream.Close()
			openErr := errors.New(firstNonEmpty(strings.TrimSpace(response.Error), "open stream failed"))
			rt.mu.Lock()
			_ = rt.markFailureLocked(openErr, "unavailable")
			rt.mu.Unlock()
			return nil, openErr
		}
		rt.mu.Lock()
		rt.RuntimeStatus = "connected"
		rt.LastError = ""
		rt.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		rt.mu.Unlock()
		return stream, nil
	}
	return nil, errors.New("group runtime stream open failed")
}
