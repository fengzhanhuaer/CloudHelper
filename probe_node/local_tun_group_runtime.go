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
	Group           string                               `json:"group"`
	SelectedChainID string                               `json:"selected_chain_id,omitempty"`
	EntryNodeID     string                               `json:"entry_node_id,omitempty"`
	EntryHost       string                               `json:"entry_host,omitempty"`
	EntryPort       int                                  `json:"entry_port,omitempty"`
	LinkLayer       string                               `json:"link_layer,omitempty"`
	RuntimeStatus   string                               `json:"runtime_status,omitempty"`
	LastError       string                               `json:"last_error,omitempty"`
	FailureCount    int                                  `json:"failure_count,omitempty"`
	UpdatedAt       string                               `json:"updated_at,omitempty"`
	LastConnectedAt string                               `json:"last_connected_at,omitempty"`
	Connected       bool                                 `json:"connected"`
	ProtocolState   probeChainRelayProtocolStateSnapshot `json:"protocol_state,omitempty"`
}

const probeLocalTUNGroupRuntimeControlTimeout = 15 * time.Second

var probeLocalTUNGroupRuntimeRegistry = struct {
	mu    sync.RWMutex
	items map[string]*probeLocalTUNGroupRuntime
}{items: make(map[string]*probeLocalTUNGroupRuntime)}

var probeLocalTUNOpenChainRelayNetConn = openProbeLocalTUNChainRelayNetConn
var probeLocalTUNOpenChainRelayDataStreamNetConn = openProbeLocalTUNChainRelayDataStreamNetConn

// Group runtime is the aggregation boundary for proxy behavior.
// DNS records must not persist action or selected_chain_id as their primary state.
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
		rt.mu.Lock()
		err := rt.ensureConnectedLocked()
		rt.mu.Unlock()
		snapshot = rt.snapshot()
		updatedAt := firstNonEmpty(strings.TrimSpace(snapshot.UpdatedAt), time.Now().UTC().Format(time.RFC3339))
		if err != nil {
			reason := strings.TrimSpace(err.Error())
			logProbeWarnf(
				"probe local tun group runtime reachability failed: group=%s chain=%s entry=%s:%d layer=%s phase=connect status=%s reason=%s",
				strings.TrimSpace(snapshot.Group),
				strings.TrimSpace(snapshot.SelectedChainID),
				strings.TrimSpace(snapshot.EntryHost),
				snapshot.EntryPort,
				strings.TrimSpace(snapshot.LinkLayer),
				firstNonEmpty(strings.TrimSpace(snapshot.RuntimeStatus), "unavailable"),
				reason,
			)
			return firstNonEmpty(strings.TrimSpace(snapshot.RuntimeStatus), "unavailable"), nil, updatedAt, reason
		}
		logProbeInfof(
			"probe local tun group runtime keepalive connected: group=%s chain=%s entry=%s:%d layer=%s phase=connect",
			strings.TrimSpace(snapshot.Group),
			strings.TrimSpace(snapshot.SelectedChainID),
			strings.TrimSpace(snapshot.EntryHost),
			snapshot.EntryPort,
			strings.TrimSpace(snapshot.LinkLayer),
		)
	}

	endpoint, err := resolveProbeLocalChainEntryEndpointByID(snapshot.SelectedChainID)
	updatedAt := time.Now().UTC().Format(time.RFC3339)
	if err != nil {
		reason := strings.TrimSpace(err.Error())
		logProbeWarnf(
			"probe local tun group runtime reachability failed: group=%s chain=%s entry=%s:%d layer=%s phase=resolve_endpoint status=%s reason=%s",
			strings.TrimSpace(snapshot.Group),
			strings.TrimSpace(snapshot.SelectedChainID),
			strings.TrimSpace(snapshot.EntryHost),
			snapshot.EntryPort,
			strings.TrimSpace(snapshot.LinkLayer),
			firstNonEmpty(strings.TrimSpace(snapshot.RuntimeStatus), "connected"),
			reason,
		)
		return firstNonEmpty(strings.TrimSpace(snapshot.RuntimeStatus), "connected"), nil, updatedAt, reason
	}

	protocol := probeLocalTUNGroupRuntimePingPongProtocol(snapshot, endpoint)
	latency, err := probeLocalProxyLinkProtocolProbe(endpoint, protocol)
	if err != nil {
		reason := strings.TrimSpace(err.Error())
		logProbeWarnf(
			"probe local tun group runtime reachability failed: group=%s chain=%s entry=%s:%d layer=%s protocol=%s phase=ping_pong status=%s reason=%s",
			strings.TrimSpace(snapshot.Group),
			strings.TrimSpace(endpoint.ChainID),
			strings.TrimSpace(endpoint.EntryHost),
			endpoint.EntryPort,
			strings.TrimSpace(endpoint.LinkLayer),
			strings.TrimSpace(protocol),
			firstNonEmpty(strings.TrimSpace(snapshot.RuntimeStatus), "connected"),
			reason,
		)
		return firstNonEmpty(strings.TrimSpace(snapshot.RuntimeStatus), "connected"), nil, updatedAt, reason
	}
	latencyMS := probeDurationMilliseconds(latency)
	logProbeInfof(
		"probe local tun group runtime reachability ok: group=%s chain=%s entry=%s:%d layer=%s protocol=%s phase=ping_pong latency_ms=%d",
		strings.TrimSpace(snapshot.Group),
		strings.TrimSpace(endpoint.ChainID),
		strings.TrimSpace(endpoint.EntryHost),
		endpoint.EntryPort,
		strings.TrimSpace(endpoint.LinkLayer),
		strings.TrimSpace(protocol),
		latencyMS,
	)
	return firstNonEmpty(strings.TrimSpace(snapshot.RuntimeStatus), "connected"), &latencyMS, updatedAt, ""
}

func probeLocalTUNGroupRuntimePingPongProtocol(snapshot probeLocalTUNGroupRuntimeSnapshot, endpoint probeLocalTUNChainEndpoint) string {
	candidates := probeChainRelayProtocolCandidates(endpoint.LinkLayer)
	if len(candidates) == 0 {
		candidates = probeLocalProxyLinkReachabilityProtocols()
	}
	seen := make(map[string]struct{}, len(candidates))
	normalized := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		protocol := normalizeProbeChainLinkLayer(candidate)
		if !isProbeChainRelaySupportedProtocol(protocol) {
			continue
		}
		if _, ok := seen[protocol]; ok {
			continue
		}
		seen[protocol] = struct{}{}
		normalized = append(normalized, protocol)
	}
	selected := normalizeProbeChainLinkLayer(snapshot.ProtocolState.SelectedProtocol)
	if _, ok := seen[selected]; ok {
		return selected
	}
	endpointKey := probeChainRelayProtocolEndpointKey(endpoint.EntryHost, endpoint.EntryPort)
	if preferred := normalizeProbeChainLinkLayer(getProbeChainRelayProtocolPreferred(endpointKey, normalized, time.Now())); preferred != "" {
		if _, ok := seen[preferred]; ok {
			return preferred
		}
	}
	if len(normalized) > 0 {
		return normalized[0]
	}
	return "websocket-h3"
}

func probeLocalLatencyMilliseconds(startedAt time.Time) int64 {
	elapsed := time.Since(startedAt)
	if elapsed <= 0 {
		return 1
	}
	ms := int64(elapsed / time.Millisecond)
	if ms <= 0 {
		return 1
	}
	return ms
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
		if !matchesProbeLocalProxyChainSelection(item, chainID) {
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
			linkLayer = normalizeProbeChainLinkLayer(firstNonEmpty(strings.TrimSpace(hop.LinkLayer), strings.TrimSpace(item.LinkLayer)))
			break
		}
		if entryHost == "" {
			return probeLocalTUNChainEndpoint{}, fmt.Errorf("selected chain entry host is unavailable: %s", chainID)
		}
		if entryPort <= 0 {
			return probeLocalTUNChainEndpoint{}, fmt.Errorf("selected chain entry port is unavailable: %s", chainID)
		}
		return probeLocalTUNChainEndpoint{
			ChainID:           effectiveProbeLocalRelayChainID(item),
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

func matchesProbeLocalProxyChainSelection(item probeLinkChainServerItem, selectedID string) bool {
	clean := strings.TrimSpace(selectedID)
	if clean == "" {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(item.ChainID), clean) {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(item.ClientEntryID), clean) {
		return true
	}
	return false
}

func effectiveProbeLocalRelayChainID(item probeLinkChainServerItem) string {
	if relayID := strings.TrimSpace(item.RelayChainID); relayID != "" {
		return relayID
	}
	return strings.TrimSpace(item.ChainID)
}

func (rt *probeLocalTUNGroupRuntime) snapshot() probeLocalTUNGroupRuntimeSnapshot {
	if rt == nil {
		return probeLocalTUNGroupRuntimeSnapshot{}
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.snapshotLocked()
}

func (rt *probeLocalTUNGroupRuntime) snapshotLocked() probeLocalTUNGroupRuntimeSnapshot {
	if rt == nil {
		return probeLocalTUNGroupRuntimeSnapshot{}
	}
	connected := rt.session != nil && !rt.session.IsClosed()
	snapshot := probeLocalTUNGroupRuntimeSnapshot{
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
	if strings.TrimSpace(rt.Endpoint.EntryHost) != "" && rt.Endpoint.EntryPort > 0 {
		snapshot.ProtocolState = snapshotProbeLocalTUNChainRelayProtocolState(rt.Endpoint.EntryHost, rt.Endpoint.EntryPort)
	}
	return snapshot
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
	failureErr := err
	if failureErr == nil {
		failureErr = errors.New("group runtime failure")
	}
	if rt == nil {
		return failureErr
	}
	rt.FailureCount++
	rt.LastError = strings.TrimSpace(failureErr.Error())
	rt.RuntimeStatus = firstNonEmpty(strings.TrimSpace(status), "unavailable")
	rt.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return failureErr
}

func (rt *probeLocalTUNGroupRuntime) markStreamFailureLocked(session *yamux.Session, err error) error {
	if rt == nil {
		if err != nil {
			return err
		}
		return errors.New("group runtime failure")
	}
	if session != nil && rt.session == session && session.IsClosed() {
		rt.closeLocked()
		return rt.markFailureLocked(err, "disconnected")
	}
	return rt.markFailureLocked(err, "degraded")
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
	conn, err := probeLocalTUNOpenChainRelayNetConn(endpoint.ChainID, endpoint.ChainSecret, endpoint.EntryHost, endpoint.EntryPort, endpoint.LinkLayer, probeChainBridgeRoleToNext)
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

func (rt *probeLocalTUNGroupRuntime) openStream(network string, targetAddr string, associationV2 *probeChainAssociationV2Meta, flowID string) (net.Conn, string, error) {
	if rt == nil {
		return nil, "", errors.New("group runtime is nil")
	}
	cleanNetwork := strings.ToLower(strings.TrimSpace(network))
	if cleanNetwork == "" {
		cleanNetwork = "tcp"
	}
	cleanFlowID := strings.TrimSpace(flowID)
	if associationV2 != nil && strings.TrimSpace(associationV2.FlowID) != "" {
		cleanFlowID = strings.TrimSpace(associationV2.FlowID)
	}
	if cleanFlowID == "" && cleanNetwork == "tcp" {
		cleanFlowID = newProbeTCPDebugFlowID("tun", targetAddr)
	}
	for attempt := 0; attempt < 2; attempt++ {
		rt.mu.Lock()
		if err := rt.ensureConnectedLocked(); err != nil {
			rt.mu.Unlock()
			return nil, cleanFlowID, err
		}
		endpoint := rt.Endpoint
		rt.mu.Unlock()
		if strings.TrimSpace(endpoint.ChainID) == "" {
			return nil, cleanFlowID, errors.New("group runtime endpoint is nil")
		}
		stream, err := probeLocalTUNOpenChainRelayDataStreamNetConn(endpoint.ChainID, endpoint.ChainSecret, endpoint.EntryHost, endpoint.EntryPort, endpoint.LinkLayer)
		if err != nil {
			rt.mu.Lock()
			_ = rt.markFailureLocked(err, "degraded")
			rt.mu.Unlock()
			if attempt == 0 && shouldReconnectProbeLocalTUNGroupRuntimeOpenError(err) {
				continue
			}
			return nil, cleanFlowID, err
		}
		request := probeChainTunnelOpenRequest{
			Type:          "open",
			Network:       cleanNetwork,
			Address:       strings.TrimSpace(targetAddr),
			FlowID:        cleanFlowID,
			AssociationV2: associationV2,
		}
		_ = stream.SetWriteDeadline(time.Now().Add(probeChainPortForwardResponseReadDeadline))
		if err := json.NewEncoder(stream).Encode(request); err != nil {
			_ = stream.Close()
			rt.mu.Lock()
			_ = rt.markFailureLocked(err, "degraded")
			rt.mu.Unlock()
			if attempt == 0 && shouldReconnectProbeLocalTUNGroupRuntimeOpenError(err) {
				continue
			}
			return nil, cleanFlowID, err
		}
		_ = stream.SetWriteDeadline(time.Time{})
		_ = stream.SetReadDeadline(time.Now().Add(probeChainPortForwardResponseReadDeadline))
		var response probeChainTunnelOpenResponse
		if err := json.NewDecoder(stream).Decode(&response); err != nil {
			_ = stream.Close()
			rt.mu.Lock()
			_ = rt.markFailureLocked(err, "degraded")
			rt.mu.Unlock()
			if attempt == 0 && shouldReconnectProbeLocalTUNGroupRuntimeOpenError(err) {
				continue
			}
			return nil, cleanFlowID, err
		}
		_ = stream.SetReadDeadline(time.Time{})
		if !response.OK {
			_ = stream.Close()
			openErr := errors.New(firstNonEmpty(strings.TrimSpace(response.Error), "open stream failed"))
			rt.mu.Lock()
			_ = rt.markFailureLocked(openErr, "degraded")
			rt.mu.Unlock()
			if attempt == 0 && shouldReconnectProbeLocalTUNGroupRuntimeOpenError(openErr) {
				continue
			}
			return nil, cleanFlowID, openErr
		}
		rt.mu.Lock()
		rt.RuntimeStatus = "connected"
		rt.LastError = ""
		rt.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		rt.mu.Unlock()
		return stream, cleanFlowID, nil
	}
	return nil, cleanFlowID, errors.New("group runtime stream open failed")
}

func (rt *probeLocalTUNGroupRuntime) fetchRemoteTCPDebug() (probeTCPDebugResultPayload, error) {
	if rt == nil {
		return probeTCPDebugResultPayload{}, errors.New("group runtime is nil")
	}
	requestID := "remote-tcp-debug-" + randomHexToken(8)
	for attempt := 0; attempt < 2; attempt++ {
		rt.mu.Lock()
		if err := rt.ensureConnectedLocked(); err != nil {
			rt.mu.Unlock()
			return probeTCPDebugResultPayload{}, err
		}
		session := rt.session
		rt.mu.Unlock()
		if session == nil {
			return probeTCPDebugResultPayload{}, errors.New("group runtime management session is nil")
		}
		stream, err := session.Open()
		if err != nil {
			reconnect := session.IsClosed()
			if !reconnect && shouldReconnectProbeLocalTUNGroupRuntimeOpenError(err) {
				reconnect = shouldReconnectProbeLocalTUNGroupRuntimeSessionLocked(rt, session)
			}
			rt.mu.Lock()
			if rt.session == session {
				if reconnect {
					rt.closeLocked()
					_ = rt.markFailureLocked(err, "disconnected")
				} else {
					_ = rt.markFailureLocked(err, "degraded")
				}
			}
			rt.mu.Unlock()
			if attempt == 0 && reconnect {
				continue
			}
			return probeTCPDebugResultPayload{}, err
		}
		_ = stream.SetDeadline(time.Now().Add(probeLocalTUNGroupRuntimeControlTimeout))
		req := probeChainTunnelOpenRequest{Type: "tcp_debug_get", RequestID: requestID}
		if err := json.NewEncoder(stream).Encode(req); err != nil {
			_ = stream.Close()
			if attempt == 0 && shouldReconnectProbeLocalTUNGroupRuntimeAfterIOFailure(rt, session, err) {
				continue
			}
			return probeTCPDebugResultPayload{}, err
		}
		var payload probeTCPDebugResultPayload
		if err := json.NewDecoder(stream).Decode(&payload); err != nil {
			_ = stream.Close()
			if attempt == 0 && shouldReconnectProbeLocalTUNGroupRuntimeAfterIOFailure(rt, session, err) {
				continue
			}
			return probeTCPDebugResultPayload{}, err
		}
		_ = stream.Close()
		if strings.TrimSpace(payload.RequestID) == "" {
			payload.RequestID = requestID
		}
		if strings.TrimSpace(payload.Scope) == "" {
			payload.Scope = "chain_exit"
		}
		return payload, nil
	}
	return probeTCPDebugResultPayload{}, errors.New("remote tcp debug fetch failed")
}

func (rt *probeLocalTUNGroupRuntime) fetchRemoteSpeedDebug() (probeSpeedDebugResultPayload, error) {
	if rt == nil {
		return probeSpeedDebugResultPayload{}, errors.New("group runtime is nil")
	}
	requestID := "remote-speed-debug-" + randomHexToken(8)
	for attempt := 0; attempt < 2; attempt++ {
		rt.mu.Lock()
		if err := rt.ensureConnectedLocked(); err != nil {
			rt.mu.Unlock()
			return probeSpeedDebugResultPayload{}, err
		}
		session := rt.session
		rt.mu.Unlock()
		if session == nil {
			return probeSpeedDebugResultPayload{}, errors.New("group runtime management session is nil")
		}
		stream, err := session.Open()
		if err != nil {
			reconnect := session.IsClosed()
			if !reconnect && shouldReconnectProbeLocalTUNGroupRuntimeOpenError(err) {
				reconnect = shouldReconnectProbeLocalTUNGroupRuntimeSessionLocked(rt, session)
			}
			rt.mu.Lock()
			if rt.session == session {
				if reconnect {
					rt.closeLocked()
					_ = rt.markFailureLocked(err, "disconnected")
				} else {
					_ = rt.markFailureLocked(err, "degraded")
				}
			}
			rt.mu.Unlock()
			if attempt == 0 && reconnect {
				continue
			}
			return probeSpeedDebugResultPayload{}, err
		}
		_ = stream.SetDeadline(time.Now().Add(probeLocalTUNGroupRuntimeControlTimeout))
		req := probeChainTunnelOpenRequest{Type: "speed_debug_get", RequestID: requestID}
		if err := json.NewEncoder(stream).Encode(req); err != nil {
			_ = stream.Close()
			if attempt == 0 && shouldReconnectProbeLocalTUNGroupRuntimeAfterIOFailure(rt, session, err) {
				continue
			}
			return probeSpeedDebugResultPayload{}, err
		}
		var payload probeSpeedDebugResultPayload
		if err := json.NewDecoder(stream).Decode(&payload); err != nil {
			_ = stream.Close()
			if attempt == 0 && shouldReconnectProbeLocalTUNGroupRuntimeAfterIOFailure(rt, session, err) {
				continue
			}
			return probeSpeedDebugResultPayload{}, err
		}
		_ = stream.Close()
		if strings.TrimSpace(payload.RequestID) == "" {
			payload.RequestID = requestID
		}
		if strings.TrimSpace(payload.Scope) == "" {
			payload.Scope = "chain_exit"
		}
		return payload, nil
	}
	return probeSpeedDebugResultPayload{}, errors.New("remote speed debug fetch failed")
}

func shouldReconnectProbeLocalTUNGroupRuntimeAfterIOFailure(rt *probeLocalTUNGroupRuntime, session *yamux.Session, err error) bool {
	if !shouldReconnectProbeLocalTUNGroupRuntimeOpenError(err) {
		return false
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return shouldReconnectProbeLocalTUNGroupRuntimeSessionLocked(rt, session)
}

func shouldReconnectProbeLocalTUNGroupRuntimeSessionLocked(rt *probeLocalTUNGroupRuntime, session *yamux.Session) bool {
	if rt == nil || session == nil || rt.session != session {
		return false
	}
	if session.IsClosed() {
		return true
	}
	snapshot := rt.snapshotLocked()
	if strings.TrimSpace(snapshot.SelectedChainID) == "" {
		return true
	}
	endpoint, err := resolveProbeLocalChainEntryEndpointByID(snapshot.SelectedChainID)
	if err != nil {
		return true
	}
	probeConn, err := probeLocalTUNOpenChainRelayNetConn(endpoint.ChainID, endpoint.ChainSecret, endpoint.EntryHost, endpoint.EntryPort, endpoint.LinkLayer, probeChainBridgeRoleToNext)
	if err != nil {
		return true
	}
	_ = probeConn.Close()
	return false
}

func shouldReconnectProbeLocalTUNGroupRuntimeOpenError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	if text == "" {
		return false
	}
	if strings.Contains(text, "/api/node/chain/relay") ||
		strings.Contains(text, "probe relay") ||
		strings.Contains(text, "yamux") ||
		strings.Contains(text, "context canceled") ||
		strings.Contains(text, "use of closed network connection") ||
		strings.Contains(text, "closed pipe") ||
		strings.Contains(text, "broken pipe") ||
		strings.Contains(text, "connection reset") ||
		strings.Contains(text, "connection aborted") ||
		strings.Contains(text, "eof") ||
		strings.Contains(text, "i/o deadline reached") ||
		strings.Contains(text, "i/o timeout") {
		return true
	}
	return false
}
