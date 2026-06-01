package mobilecore

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

const (
	linkProxyChainFileName             = "proxy_chain.json"
	linkRelayAPIPath                   = "/api/node/chain/relay"
	linkLegacyChainIDHeader            = "X-CH-Chain-ID"
	linkCodexChainIDHeader             = "X-Codex-Chain-Id"
	linkCodexAuthModeHeader            = "X-Codex-Auth-Mode"
	linkCodexMACHeader                 = "X-Codex-Mac"
	linkCodexVersionHeader             = "X-Codex-Api-Version"
	linkCodexRelayModeHeader           = "X-Codex-Relay-Mode"
	linkCodexRelayRoleHeader           = "X-Codex-Relay-Role"
	linkCodexSpeedBytesHeader          = "X-Codex-Speed-Bytes"
	linkAuthPacketVersion              = "2025-03-22"
	linkRelayModeBridge                = "bridge"
	linkRelayModeStream                = "stream"
	linkRelayModeSpeedTest             = "speed_test"
	linkRelayModePingPong              = "ping_pong"
	linkBridgeRoleToNext               = "to_next"
	linkRelayProtocolProbeTimeout      = 6 * time.Second
	linkPortForwardResponseReadTimeout = 10 * time.Second
	linkRelaySpeedTestBytes            = 128 * 1024 * 1024
	linkRelaySpeedTestMaxBytes         = 256 * 1024 * 1024
	linkRelaySpeedTestTimeout          = 10 * time.Second
	linkRelayWebSocketBufferBytes      = 512 * 1024
	linkRelayQUICInitialStreamWindow   = 128 * 1024 * 1024
	linkRelayQUICMaxStreamWindow       = 512 * 1024 * 1024
	linkRelayQUICInitialConnWindow     = 512 * 1024 * 1024
	linkRelayQUICMaxConnWindow         = 1024 * 1024 * 1024
)

type linkChainServerItem struct {
	ChainID         string             `json:"chain_id"`
	RelayChainID    string             `json:"relay_chain_id"`
	ClientEntryID   string             `json:"client_entry_id"`
	ClientEntryType string             `json:"client_entry_type"`
	ChainType       string             `json:"chain_type"`
	Name            string             `json:"name"`
	Secret          string             `json:"secret"`
	EntryNodeID     string             `json:"entry_node_id"`
	ExitNodeID      string             `json:"exit_node_id"`
	CascadeNodeIDs  []string           `json:"cascade_node_ids"`
	LinkLayer       string             `json:"link_layer"`
	HopConfigs      []linkChainHopItem `json:"hop_configs"`
	PortForwards    []json.RawMessage  `json:"port_forwards"`
	EgressHost      string             `json:"egress_host"`
	EgressPort      int                `json:"egress_port"`
}

type linkChainHopItem struct {
	NodeNo       int    `json:"node_no"`
	ListenHost   string `json:"listen_host"`
	ListenPort   int    `json:"listen_port"`
	ExternalPort int    `json:"external_port"`
	LinkLayer    string `json:"link_layer"`
	DialMode     string `json:"dial_mode"`
	RelayHost    string `json:"relay_host"`
}

type linkEndpoint struct {
	ChainID     string `json:"chain_id"`
	ChainName   string `json:"chain_name,omitempty"`
	EntryNodeID string `json:"entry_node_id,omitempty"`
	EntryHost   string `json:"entry_host,omitempty"`
	EntryPort   int    `json:"entry_port,omitempty"`
	LinkLayer   string `json:"link_layer,omitempty"`
	ChainSecret string `json:"-"`
}

type linkStatusItem struct {
	ChainID       string `json:"chain_id"`
	RelayChainID  string `json:"relay_chain_id,omitempty"`
	ClientEntryID string `json:"client_entry_id,omitempty"`
	ChainName     string `json:"chain_name,omitempty"`
	ChainType     string `json:"chain_type,omitempty"`
	Status        string `json:"status"`
	EntryNodeID   string `json:"entry_node_id,omitempty"`
	EntryHost     string `json:"entry_host,omitempty"`
	EntryPort     int    `json:"entry_port,omitempty"`
	LinkLayer     string `json:"link_layer,omitempty"`
	Error         string `json:"error,omitempty"`
}

type linkReachabilityResult struct {
	Protocol  string `json:"protocol"`
	OK        bool   `json:"ok"`
	LatencyMS int64  `json:"latency_ms,omitempty"`
	Error     string `json:"error,omitempty"`
}

type linkSpeedTestResult struct {
	Protocol         string `json:"protocol"`
	OK               bool   `json:"ok"`
	LatencyMS        int64  `json:"latency_ms,omitempty"`
	Bytes            int64  `json:"bytes,omitempty"`
	RequestedBytes   int64  `json:"requested_bytes,omitempty"`
	DurationMS       int64  `json:"duration_ms,omitempty"`
	RateBPS          int64  `json:"rate_bps,omitempty"`
	ReadCalls        int64  `json:"read_calls,omitempty"`
	ReadChunkBytes   int64  `json:"read_chunk_bytes,omitempty"`
	AvgReadBytes     int64  `json:"avg_read_bytes,omitempty"`
	FirstByteMS      int64  `json:"first_byte_ms,omitempty"`
	TotalReadBlockMS int64  `json:"total_read_block_ms,omitempty"`
	MaxReadBlockMS   int64  `json:"max_read_block_ms,omitempty"`
	LastReadBlockMS  int64  `json:"last_read_block_ms,omitempty"`
	OpenHandshakeMS  int64  `json:"open_handshake_ms,omitempty"`
	LocalStartedAt   string `json:"local_started_at,omitempty"`
	LocalFirstByteAt string `json:"local_first_byte_at,omitempty"`
	LocalCompletedAt string `json:"local_completed_at,omitempty"`
	Error            string `json:"error,omitempty"`
	StartedAt        string `json:"started_at,omitempty"`
	EndedAt          string `json:"ended_at,omitempty"`
}

type linkTunnelOpenRequest struct {
	Type          string                 `json:"type"`
	Network       string                 `json:"network,omitempty"`
	Address       string                 `json:"address,omitempty"`
	FlowID        string                 `json:"flow_id,omitempty"`
	AssociationV2 *linkAssociationV2Meta `json:"association_v2,omitempty"`
	SpeedBytes    int64                  `json:"speed_bytes,omitempty"`
	PingBytes     int64                  `json:"ping_bytes,omitempty"`
}

type linkAssociationV2Meta struct {
	Version          int    `json:"version"`
	Transport        string `json:"transport,omitempty"`
	RouteGroup       string `json:"route_group,omitempty"`
	RouteNodeID      string `json:"route_node_id,omitempty"`
	RouteTarget      string `json:"route_target,omitempty"`
	RouteFingerprint string `json:"route_fingerprint,omitempty"`
	NATMode          string `json:"nat_mode,omitempty"`
	TTLProfile       string `json:"ttl_profile,omitempty"`
	IdleTimeoutMS    int64  `json:"idle_timeout_ms,omitempty"`
	GCIntervalMS     int64  `json:"gc_interval_ms,omitempty"`
	CreatedAtUnixMS  int64  `json:"created_at_unix_ms,omitempty"`
	AssocKeyV2       string `json:"assoc_key_v2,omitempty"`
	FlowID           string `json:"flow_id,omitempty"`
	SrcIP            string `json:"src_ip,omitempty"`
	SrcPort          uint16 `json:"src_port,omitempty"`
	DstIP            string `json:"dst_ip,omitempty"`
	DstPort          uint16 `json:"dst_port,omitempty"`
	IPFamily         int    `json:"ip_family,omitempty"`
	SourceKey        string `json:"source_key,omitempty"`
	SourceRefs       int64  `json:"source_refs,omitempty"`
}

type linkTunnelOpenResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// LinkStatus returns the Android-visible proxy chain endpoint inventory from proxy_chain.json.
func LinkStatus(configDir string) string {
	items, err := loadLinkProxyChains(configDir)
	if err != nil {
		return marshalLinkJSON(map[string]any{
			"ok":         false,
			"error":      err.Error(),
			"chains":     []linkStatusItem{},
			"updated_at": time.Now().UTC().Format(time.RFC3339),
		})
	}
	out := make([]linkStatusItem, 0, len(items))
	for _, item := range items {
		status := linkStatusItem{
			ChainID:       strings.TrimSpace(item.ChainID),
			RelayChainID:  strings.TrimSpace(item.RelayChainID),
			ClientEntryID: strings.TrimSpace(item.ClientEntryID),
			ChainName:     strings.TrimSpace(item.Name),
			ChainType:     strings.TrimSpace(item.ChainType),
			Status:        "configured",
		}
		endpoint, err := resolveLinkEndpoint(item)
		if err != nil {
			status.Status = "unconfigured"
			status.Error = err.Error()
		} else {
			status.EntryNodeID = endpoint.EntryNodeID
			status.EntryHost = endpoint.EntryHost
			status.EntryPort = endpoint.EntryPort
			status.LinkLayer = endpoint.LinkLayer
		}
		out = append(out, status)
	}
	return marshalLinkJSON(map[string]any{
		"ok":         true,
		"chains":     out,
		"count":      len(out),
		"updated_at": time.Now().UTC().Format(time.RFC3339),
	})
}

// LinkLatency runs the same relay ping-pong probe used by the desktop probe node.
func LinkLatency(configDir string, chainID string) string {
	item, endpoint, err := loadLinkEndpointByID(configDir, chainID)
	if err != nil {
		return marshalLinkJSON(map[string]any{
			"ok":         false,
			"chain_id":   strings.TrimSpace(chainID),
			"status":     "unconfigured",
			"error":      err.Error(),
			"updated_at": time.Now().UTC().Format(time.RFC3339),
		})
	}
	protocols := linkReachabilityProtocolsForEndpoint(item, endpoint)
	resultsCh := make(chan linkReachabilityResult, len(protocols))
	for _, protocol := range protocols {
		go func(protocol string) {
			latency, err := linkPingPongProbe(endpoint, protocol)
			result := linkReachabilityResult{
				Protocol:  normalizeLinkLayer(protocol),
				OK:        err == nil,
				LatencyMS: durationMilliseconds(latency),
			}
			if err != nil {
				result.Error = strings.TrimSpace(err.Error())
				result.LatencyMS = 0
			}
			resultsCh <- result
		}(protocol)
	}
	results := make([]linkReachabilityResult, 0, len(protocols))
	reachableCount := 0
	bestProtocol := ""
	bestLatencyMS := int64(0)
	for range protocols {
		result := <-resultsCh
		results = append(results, result)
		if !result.OK {
			continue
		}
		reachableCount++
		if bestProtocol == "" || bestLatencyMS <= 0 || (result.LatencyMS > 0 && result.LatencyMS < bestLatencyMS) || (result.LatencyMS == bestLatencyMS && linkProtocolOrder(result.Protocol) < linkProtocolOrder(bestProtocol)) {
			bestProtocol = normalizeLinkLayer(result.Protocol)
			bestLatencyMS = result.LatencyMS
		}
	}
	sort.Slice(results, func(i, j int) bool {
		return linkProtocolOrder(results[i].Protocol) < linkProtocolOrder(results[j].Protocol)
	})
	base := map[string]any{
		"ok":              bestProtocol != "",
		"chain_id":        strings.TrimSpace(item.ChainID),
		"chain_name":      strings.TrimSpace(item.Name),
		"status":          "reachable",
		"latency_ms":      bestLatencyMS,
		"reachable_count": reachableCount,
		"tested_count":    len(results),
		"best_protocol":   bestProtocol,
		"entry_host":      endpoint.EntryHost,
		"entry_port":      endpoint.EntryPort,
		"link_layer":      endpoint.LinkLayer,
		"results":         results,
		"updated_at":      time.Now().UTC().Format(time.RFC3339),
	}
	if bestProtocol == "" {
		base["status"] = "unreachable"
		base["error"] = "all relay protocols are unreachable"
	}
	return marshalLinkJSON(base)
}

// LinkSpeed runs the real relay speed_test mode and consumes server-sent bytes.
func LinkSpeed(configDir string, chainID string, protocol string) string {
	item, endpoint, err := loadLinkEndpointByID(configDir, chainID)
	if err != nil {
		return marshalLinkJSON(map[string]any{
			"ok":         false,
			"chain_id":   strings.TrimSpace(chainID),
			"status":     "unconfigured",
			"error":      err.Error(),
			"updated_at": time.Now().UTC().Format(time.RFC3339),
		})
	}
	cleanProtocol := ""
	if strings.TrimSpace(protocol) != "" {
		cleanProtocol = normalizeLinkLayer(protocol)
		if !isLinkRelaySupportedProtocol(cleanProtocol) {
			return marshalLinkJSON(map[string]any{"ok": false, "error": "protocol must be websocket-h3 or websocket"})
		}
	}
	if isLinkCFEntry(item) && cleanProtocol != "" && cleanProtocol != "websocket" {
		return marshalLinkJSON(map[string]any{"ok": false, "error": "cf entry only supports websocket speed test"})
	}
	if isLinkCFEntry(item) {
		cleanProtocol = "websocket"
	}
	results := linkRelaySpeedTestAuto(endpoint, cleanProtocol, linkRelaySpeedTestBytes)
	rateBPS := int64(0)
	okResult := false
	for _, result := range results {
		if result.OK {
			okResult = true
			if result.RateBPS > rateBPS {
				rateBPS = result.RateBPS
			}
		}
	}
	status := "unreachable"
	if okResult {
		status = "tested"
	} else if len(results) == 0 {
		status = "no_result"
	}
	return marshalLinkJSON(map[string]any{
		"ok":         okResult,
		"chain_id":   strings.TrimSpace(item.ChainID),
		"chain_name": strings.TrimSpace(item.Name),
		"status":     status,
		"protocol":   cleanProtocol,
		"rate_bps":   rateBPS,
		"source":     "active_speed_test",
		"results":    results,
		"updated_at": time.Now().UTC().Format(time.RFC3339),
	})
}

func loadLinkEndpointByID(configDir string, chainID string) (linkChainServerItem, linkEndpoint, error) {
	items, err := loadLinkProxyChains(configDir)
	if err != nil {
		return linkChainServerItem{}, linkEndpoint{}, err
	}
	item, ok := findLinkItemByID(chainID, items)
	if !ok {
		return linkChainServerItem{}, linkEndpoint{}, errors.New("chain not found")
	}
	endpoint, err := resolveLinkEndpoint(item)
	if err != nil {
		return item, linkEndpoint{}, err
	}
	return item, endpoint, nil
}

func loadLinkProxyChains(configDir string) ([]linkChainServerItem, error) {
	configDir = strings.TrimSpace(configDir)
	if configDir == "" {
		return nil, errors.New("config dir is required")
	}
	raw, err := os.ReadFile(filepath.Join(configDir, linkProxyChainFileName))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", linkProxyChainFileName, err)
	}
	var cache struct {
		Items []linkChainServerItem `json:"items"`
	}
	if err := json.Unmarshal(raw, &cache); err == nil && cache.Items != nil {
		return cache.Items, nil
	}
	var items []linkChainServerItem
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("decode %s: %w", linkProxyChainFileName, err)
	}
	return items, nil
}

func findLinkItemByID(chainID string, items []linkChainServerItem) (linkChainServerItem, bool) {
	cleanID := strings.TrimSpace(chainID)
	if cleanID == "" {
		return linkChainServerItem{}, false
	}
	for _, item := range items {
		if strings.EqualFold(strings.TrimSpace(item.ChainID), cleanID) || strings.EqualFold(strings.TrimSpace(item.ClientEntryID), cleanID) {
			return item, true
		}
	}
	return linkChainServerItem{}, false
}

func resolveLinkEndpoint(item linkChainServerItem) (linkEndpoint, error) {
	chainID := strings.TrimSpace(item.ChainID)
	if chainID == "" {
		return linkEndpoint{}, errors.New("chain_id is required")
	}
	route := buildLinkChainRoute(item)
	if len(route) == 0 {
		return linkEndpoint{}, fmt.Errorf("chain route is empty: %s", chainID)
	}
	entryNodeID := strings.TrimSpace(route[0])
	entryHost := ""
	entryPort := 0
	linkLayer := normalizeLinkLayer(strings.TrimSpace(item.LinkLayer))
	for _, hop := range item.HopConfigs {
		hopNodeID := normalizeLinkNodeID(strconv.Itoa(hop.NodeNo))
		if hopNodeID == "" || hopNodeID != normalizeLinkNodeID(entryNodeID) {
			continue
		}
		entryHost = strings.TrimSpace(hop.RelayHost)
		if hop.ExternalPort > 0 {
			entryPort = hop.ExternalPort
		} else if hop.ListenPort > 0 {
			entryPort = hop.ListenPort
		}
		linkLayer = normalizeLinkLayer(firstNonEmptyString(strings.TrimSpace(hop.LinkLayer), strings.TrimSpace(item.LinkLayer), "auto"))
		break
	}
	if entryHost == "" {
		return linkEndpoint{}, fmt.Errorf("selected chain entry host is unavailable: %s", chainID)
	}
	if entryPort <= 0 {
		return linkEndpoint{}, fmt.Errorf("selected chain entry port is unavailable: %s", chainID)
	}
	if linkLayer == "" {
		linkLayer = "auto"
	}
	return linkEndpoint{
		ChainID:     effectiveLinkRelayChainID(item),
		ChainName:   strings.TrimSpace(item.Name),
		EntryNodeID: entryNodeID,
		EntryHost:   entryHost,
		EntryPort:   entryPort,
		LinkLayer:   linkLayer,
		ChainSecret: strings.TrimSpace(item.Secret),
	}, nil
}

func buildLinkChainRoute(item linkChainServerItem) []string {
	route := make([]string, 0, 2+len(item.CascadeNodeIDs))
	seen := make(map[string]struct{}, 2+len(item.CascadeNodeIDs))
	push := func(raw string) {
		nodeID := normalizeLinkNodeID(raw)
		if nodeID == "" {
			return
		}
		if _, exists := seen[nodeID]; exists {
			return
		}
		seen[nodeID] = struct{}{}
		route = append(route, nodeID)
	}
	push(item.EntryNodeID)
	for _, id := range item.CascadeNodeIDs {
		push(id)
	}
	push(item.ExitNodeID)
	return route
}

func normalizeLinkNodeID(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	lower := strings.ToLower(value)
	if strings.HasPrefix(lower, "node-") || strings.HasPrefix(lower, "node_") {
		suffix := strings.TrimPrefix(strings.TrimPrefix(lower, "node-"), "node_")
		suffix = strings.TrimSpace(suffix)
		if suffix != "" {
			if n, err := strconv.Atoi(suffix); err == nil && n > 0 {
				return strconv.Itoa(n)
			}
			return suffix
		}
	}
	if n, err := strconv.Atoi(value); err == nil && n > 0 {
		return strconv.Itoa(n)
	}
	return value
}

func effectiveLinkRelayChainID(item linkChainServerItem) string {
	if relayID := strings.TrimSpace(item.RelayChainID); relayID != "" {
		return relayID
	}
	return strings.TrimSpace(item.ChainID)
}

func normalizeLinkLayer(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "auto", "default", "http", "http2", "h2", "http3", "h3":
		return "auto"
	case "websocket", "ws", "wss":
		return "websocket"
	case "websocket-h3", "ws-h3", "h3-websocket", "h3-ws":
		return "websocket-h3"
	default:
		return "auto"
	}
}

func linkRelayProtocolCandidates(layer string) []string {
	switch normalizeLinkLayer(layer) {
	case "websocket":
		return []string{"websocket"}
	case "websocket-h3":
		return []string{"websocket-h3"}
	default:
		return []string{"websocket-h3", "websocket"}
	}
}

func linkReachabilityProtocolsForEndpoint(item linkChainServerItem, endpoint linkEndpoint) []string {
	if isLinkCFEntry(item) {
		return []string{"websocket"}
	}
	candidates := linkRelayProtocolCandidates(endpoint.LinkLayer)
	out := make([]string, 0, len(candidates))
	seen := map[string]struct{}{}
	for _, candidate := range candidates {
		protocol := normalizeLinkLayer(candidate)
		if !isLinkRelaySupportedProtocol(protocol) {
			continue
		}
		if _, ok := seen[protocol]; ok {
			continue
		}
		seen[protocol] = struct{}{}
		out = append(out, protocol)
	}
	if len(out) == 0 {
		return []string{"websocket-h3", "websocket"}
	}
	return out
}

func isLinkRelaySupportedProtocol(protocol string) bool {
	switch normalizeLinkLayer(protocol) {
	case "websocket", "websocket-h3":
		return true
	default:
		return false
	}
}

func isLinkCFEntry(item linkChainServerItem) bool {
	for _, value := range []string{item.ClientEntryType, item.ClientEntryID, item.ChainID, item.Name} {
		clean := strings.ToLower(strings.TrimSpace(value))
		if clean == "cf" || strings.HasSuffix(clean, "_cf") {
			return true
		}
	}
	return false
}

func linkProtocolOrder(protocol string) int {
	if normalizeLinkLayer(protocol) == "websocket-h3" {
		return 0
	}
	return 1
}

func linkPingPongProbe(endpoint linkEndpoint, protocol string) (time.Duration, error) {
	const payloadBytes = 64
	conn, err := openLinkRelayConn(endpoint, protocol, linkRelayProtocolProbeTimeout)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	stream, err := openLinkPingPongStream(conn, payloadBytes)
	if err != nil {
		return 0, err
	}
	defer stream.Close()
	payload := make([]byte, payloadBytes)
	for i := range payload {
		payload[i] = byte((i * 31) % 251)
	}
	echo := make([]byte, payloadBytes)
	startedAt := time.Now()
	_ = stream.SetDeadline(time.Now().Add(linkRelayProtocolProbeTimeout))
	if _, err := stream.Write(payload); err != nil {
		_ = stream.SetDeadline(time.Time{})
		return 0, err
	}
	if _, err := io.ReadFull(stream, echo); err != nil {
		_ = stream.SetDeadline(time.Time{})
		return 0, err
	}
	_ = stream.SetDeadline(time.Time{})
	if !bytes.Equal(payload, echo) {
		return 0, errors.New("ping-pong echo mismatch")
	}
	elapsed := time.Since(startedAt)
	if elapsed <= 0 {
		return time.Millisecond, nil
	}
	return elapsed, nil
}

func openLinkPingPongStream(conn net.Conn, payloadBytes int64) (net.Conn, error) {
	session, err := yamux.Client(conn, newLinkYamuxConfig())
	if err != nil {
		return nil, err
	}
	stream, err := session.Open()
	if err != nil {
		_ = session.Close()
		return nil, err
	}
	if err := writeLinkPingPongRequest(stream, payloadBytes); err != nil {
		_ = stream.Close()
		_ = session.Close()
		return nil, err
	}
	return &linkPingPongStreamConn{Conn: stream, session: session}, nil
}

type linkPingPongStreamConn struct {
	net.Conn
	session *yamux.Session
}

func (c *linkPingPongStreamConn) Close() error {
	var firstErr error
	if c != nil && c.Conn != nil {
		firstErr = c.Conn.Close()
	}
	if c != nil && c.session != nil {
		if err := c.session.Close(); firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func writeLinkPingPongRequest(stream net.Conn, payloadBytes int64) error {
	_ = stream.SetWriteDeadline(time.Now().Add(linkPortForwardResponseReadTimeout))
	if err := json.NewEncoder(stream).Encode(linkTunnelOpenRequest{Type: linkRelayModePingPong, PingBytes: payloadBytes}); err != nil {
		_ = stream.SetWriteDeadline(time.Time{})
		return err
	}
	_ = stream.SetWriteDeadline(time.Time{})
	_ = stream.SetReadDeadline(time.Now().Add(linkPortForwardResponseReadTimeout))
	var response linkTunnelOpenResponse
	if err := json.NewDecoder(stream).Decode(&response); err != nil {
		_ = stream.SetReadDeadline(time.Time{})
		return err
	}
	_ = stream.SetReadDeadline(time.Time{})
	if !response.OK {
		return errors.New(firstNonEmptyString(strings.TrimSpace(response.Error), "ping-pong open failed"))
	}
	return nil
}

func openLinkRelayConn(endpoint linkEndpoint, protocol string, openTimeout time.Duration) (net.Conn, error) {
	return openLinkRelayConnWithMode(endpoint, protocol, openTimeout, linkRelayModeBridge)
}

func openLinkRelayDataStreamConn(endpoint linkEndpoint, protocol string, openTimeout time.Duration) (net.Conn, error) {
	return openLinkRelayConnWithMode(endpoint, protocol, openTimeout, linkRelayModeStream)
}

func openLinkRelayConnWithMode(endpoint linkEndpoint, protocol string, openTimeout time.Duration, mode string) (net.Conn, error) {
	switch normalizeLinkLayer(protocol) {
	case "websocket":
		return openLinkRelayWebSocketConn(endpoint, openTimeout, mode, nil)
	case "websocket-h3":
		return openLinkRelayHTTP3WebSocketConn(endpoint, openTimeout, mode, nil)
	default:
		return nil, fmt.Errorf("unsupported relay protocol: %s", protocol)
	}
}

func openLinkRelayWebSocketConn(endpoint linkEndpoint, openTimeout time.Duration, mode string, extraHeaders http.Header) (net.Conn, error) {
	relayDialHost, relayHostHeader, err := resolveLinkDialHost(endpoint.EntryHost)
	if err != nil {
		return nil, err
	}
	if openTimeout <= 0 {
		openTimeout = linkRelayProtocolProbeTimeout
	}
	relayURL, err := buildLinkRelayWebSocketURL(relayHostHeader, endpoint.EntryPort, endpoint.ChainID)
	if err != nil {
		return nil, err
	}
	header, err := buildLinkRelayHeaders(endpoint.ChainID, endpoint.ChainSecret, firstNonEmptyString(strings.TrimSpace(mode), linkRelayModeBridge), linkBridgeRoleToNext, 0)
	if err != nil {
		return nil, err
	}
	for k, values := range extraHeaders {
		for _, value := range values {
			header.Add(k, value)
		}
	}
	dialHostPort := net.JoinHostPort(relayDialHost, strconv.Itoa(endpoint.EntryPort))
	dialer := websocket.Dialer{
		HandshakeTimeout:  openTimeout,
		Proxy:             nil,
		ReadBufferSize:    linkRelayWebSocketBufferBytes,
		WriteBufferSize:   linkRelayWebSocketBufferBytes,
		EnableCompression: false,
		NetDialContext: func(ctx context.Context, network string, addr string) (net.Conn, error) {
			netDialer := net.Dialer{Timeout: openTimeout}
			return netDialer.DialContext(ctx, network, dialHostPort)
		},
		TLSClientConfig: &tls.Config{
			MinVersion:         tls.VersionTLS12,
			ServerName:         resolveLinkTLSServerName(relayDialHost, relayHostHeader),
			InsecureSkipVerify: true,
		},
	}
	ws, response, err := dialer.Dial(relayURL, header)
	if err != nil {
		if response != nil && response.Body != nil {
			body, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
			_ = response.Body.Close()
			return nil, fmt.Errorf("probe relay websocket failed: status=%d body=%s", response.StatusCode, strings.TrimSpace(string(body)))
		}
		return nil, wrapLinkDialError("websocket", relayDialHost, endpoint.EntryPort, err)
	}
	return newWebSocketNetConn(ws), nil
}

func openLinkRelayHTTP3WebSocketConn(endpoint linkEndpoint, openTimeout time.Duration, mode string, extraHeaders http.Header) (net.Conn, error) {
	relayDialHost, relayHostHeader, err := resolveLinkDialHost(endpoint.EntryHost)
	if err != nil {
		return nil, err
	}
	if openTimeout <= 0 {
		openTimeout = linkRelayProtocolProbeTimeout
	}
	relayURL, err := buildLinkRelayURL(relayHostHeader, endpoint.EntryPort, endpoint.ChainID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), openTimeout)
	dialHostPort := net.JoinHostPort(relayDialHost, strconv.Itoa(endpoint.EntryPort))
	tlsConf := &tls.Config{
		MinVersion:         tls.VersionTLS13,
		NextProtos:         []string{http3.NextProtoH3},
		ServerName:         resolveLinkTLSServerName(relayDialHost, relayHostHeader),
		InsecureSkipVerify: true,
	}
	quicConn, err := quic.DialAddr(ctx, dialHostPort, tlsConf, newLinkQUICConfig())
	if err != nil {
		cancel()
		return nil, wrapLinkDialError("websocket-h3", relayDialHost, endpoint.EntryPort, err)
	}
	transport := &http3.Transport{}
	clientConn := transport.NewClientConn(quicConn)
	select {
	case <-clientConn.ReceivedSettings():
	case <-ctx.Done():
		_ = quicConn.CloseWithError(0, "h3 websocket settings timeout")
		cancel()
		return nil, fmt.Errorf("probe relay h3 websocket open timeout: relay=%s:%d", relayDialHost, endpoint.EntryPort)
	case <-clientConn.Context().Done():
		cancel()
		return nil, fmt.Errorf("probe relay h3 websocket failed: %w", context.Cause(clientConn.Context()))
	}
	if settings := clientConn.Settings(); settings == nil || !settings.EnableExtendedConnect {
		_ = quicConn.CloseWithError(0, "h3 websocket extended connect disabled")
		cancel()
		return nil, errors.New("probe relay h3 websocket failed: server did not enable extended connect")
	}
	stream, err := clientConn.OpenRequestStream(ctx)
	if err != nil {
		_ = quicConn.CloseWithError(0, "h3 websocket stream open failed")
		cancel()
		return nil, wrapLinkDialError("websocket-h3", relayDialHost, endpoint.EntryPort, err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodConnect, relayURL, nil)
	if err != nil {
		cancelLinkH3Stream(stream, quicConn, cancel, "h3 websocket request build failed")
		return nil, err
	}
	request.Proto = "websocket"
	request.ProtoMajor = 3
	request.ProtoMinor = 0
	request.Header, err = buildLinkRelayHeaders(endpoint.ChainID, endpoint.ChainSecret, firstNonEmptyString(strings.TrimSpace(mode), linkRelayModeBridge), linkBridgeRoleToNext, 0)
	if err != nil {
		cancelLinkH3Stream(stream, quicConn, cancel, "h3 websocket auth failed")
		return nil, err
	}
	for k, values := range extraHeaders {
		for _, value := range values {
			request.Header.Add(k, value)
		}
	}
	if strings.TrimSpace(relayHostHeader) != "" {
		request.Host = strings.TrimSpace(relayHostHeader)
	}
	if err := stream.SendRequestHeader(request); err != nil {
		cancelLinkH3Stream(stream, quicConn, cancel, "h3 websocket header send failed")
		return nil, wrapLinkDialError("websocket-h3", relayDialHost, endpoint.EntryPort, err)
	}
	response, err := stream.ReadResponse()
	if err != nil {
		cancelLinkH3Stream(stream, quicConn, cancel, "h3 websocket response failed")
		return nil, wrapLinkDialError("websocket-h3", relayDialHost, endpoint.EntryPort, err)
	}
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		_ = response.Body.Close()
		cancelLinkH3Stream(stream, quicConn, cancel, "h3 websocket status failed")
		return nil, fmt.Errorf("probe relay h3 websocket failed: status=%d body=%s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	cancelOnce := sync.Once{}
	return &linkHTTP3StreamNetConn{
		stream: stream,
		local:  dummyAddr("probe-chain-h3-local"),
		remote: dummyAddr(dialHostPort),
		closeFn: func() error {
			var closeErr error
			cancelOnce.Do(func() {
				cancel()
				stream.CancelRead(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
				stream.CancelWrite(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
				closeErr = quicConn.CloseWithError(0, "h3 websocket closed")
			})
			return closeErr
		},
	}, nil
}

func linkRelaySpeedTestAuto(endpoint linkEndpoint, protocol string, byteCount int64) []linkSpeedTestResult {
	candidates := linkRelaySpeedTestCandidates(endpoint.LinkLayer, protocol)
	if byteCount <= 0 {
		byteCount = linkRelaySpeedTestBytes
	}
	if byteCount > linkRelaySpeedTestMaxBytes {
		byteCount = linkRelaySpeedTestMaxBytes
	}
	results := make([]linkSpeedTestResult, 0, len(candidates))
	for _, candidate := range candidates {
		results = append(results, linkRelaySpeedTestWithLayer(endpoint, candidate, byteCount, linkRelaySpeedTestTimeout))
	}
	return results
}

func linkRelaySpeedTestCandidates(layer string, protocol string) []string {
	cleanProtocol := normalizeLinkLayer(protocol)
	switch cleanProtocol {
	case "websocket", "websocket-h3":
		return []string{cleanProtocol}
	}
	return linkRelayProtocolCandidates(layer)
}

func linkRelaySpeedTestWithLayer(endpoint linkEndpoint, layer string, byteCount int64, timeout time.Duration) linkSpeedTestResult {
	startedAt := time.Now()
	result := linkSpeedTestResult{
		Protocol:       normalizeLinkLayer(layer),
		StartedAt:      startedAt.UTC().Format(time.RFC3339),
		LocalStartedAt: startedAt.UTC().Format(time.RFC3339),
	}
	if byteCount <= 0 {
		byteCount = linkRelaySpeedTestBytes
	}
	if byteCount > linkRelaySpeedTestMaxBytes {
		byteCount = linkRelaySpeedTestMaxBytes
	}
	result.RequestedBytes = byteCount
	if timeout <= 0 {
		timeout = linkRelaySpeedTestTimeout
	}
	deadlineAt := startedAt.Add(timeout)
	speedConn, speedErr := openLinkRelaySpeedTestConn(endpoint, layer, byteCount, timeout)
	headerAt := time.Now()
	if speedErr != nil {
		result.LatencyMS = durationMilliseconds(headerAt.Sub(startedAt))
		result.OpenHandshakeMS = result.LatencyMS
		result.Error = speedErr.Error()
		result.EndedAt = time.Now().UTC().Format(time.RFC3339)
		return result
	}
	defer speedConn.Close()
	result.OpenHandshakeMS = durationMilliseconds(headerAt.Sub(startedAt))
	consumeLinkRelaySpeedTestData(speedConn, byteCount, time.Until(deadlineAt), &result)
	return result
}

func openLinkRelaySpeedTestConn(endpoint linkEndpoint, layer string, byteCount int64, openTimeout time.Duration) (net.Conn, error) {
	switch normalizeLinkLayer(layer) {
	case "websocket":
		return openLinkRelayWebSocketSpeedTestConn(endpoint, byteCount, openTimeout)
	case "websocket-h3":
		return openLinkRelayHTTP3WebSocketSpeedTestConn(endpoint, byteCount, openTimeout)
	default:
		return nil, fmt.Errorf("unsupported speed test protocol: %s", layer)
	}
}

func openLinkRelayWebSocketSpeedTestConn(endpoint linkEndpoint, byteCount int64, openTimeout time.Duration) (net.Conn, error) {
	relayDialHost, relayHostHeader, err := resolveLinkDialHost(endpoint.EntryHost)
	if err != nil {
		return nil, err
	}
	if openTimeout <= 0 {
		openTimeout = linkRelaySpeedTestTimeout
	}
	relayURL, err := buildLinkRelayWebSocketURL(relayHostHeader, endpoint.EntryPort, endpoint.ChainID)
	if err != nil {
		return nil, err
	}
	header, err := buildLinkRelayHeaders(endpoint.ChainID, endpoint.ChainSecret, linkRelayModeSpeedTest, "", byteCount)
	if err != nil {
		return nil, err
	}
	dialHostPort := net.JoinHostPort(relayDialHost, strconv.Itoa(endpoint.EntryPort))
	dialer := websocket.Dialer{
		HandshakeTimeout:  openTimeout,
		Proxy:             nil,
		ReadBufferSize:    linkRelayWebSocketBufferBytes,
		WriteBufferSize:   linkRelayWebSocketBufferBytes,
		EnableCompression: false,
		NetDialContext: func(ctx context.Context, network string, addr string) (net.Conn, error) {
			netDialer := net.Dialer{Timeout: openTimeout}
			return netDialer.DialContext(ctx, network, dialHostPort)
		},
		TLSClientConfig: &tls.Config{
			MinVersion:         tls.VersionTLS12,
			ServerName:         resolveLinkTLSServerName(relayDialHost, relayHostHeader),
			InsecureSkipVerify: true,
		},
	}
	ws, response, err := dialer.Dial(relayURL, header)
	if err != nil {
		if response != nil && response.Body != nil {
			body, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
			_ = response.Body.Close()
			return nil, fmt.Errorf("probe relay websocket speed test failed: status=%d body=%s", response.StatusCode, strings.TrimSpace(string(body)))
		}
		return nil, wrapLinkDialError("websocket", relayDialHost, endpoint.EntryPort, err)
	}
	return newWebSocketNetConn(ws), nil
}

func openLinkRelayHTTP3WebSocketSpeedTestConn(endpoint linkEndpoint, byteCount int64, openTimeout time.Duration) (net.Conn, error) {
	relayDialHost, relayHostHeader, err := resolveLinkDialHost(endpoint.EntryHost)
	if err != nil {
		return nil, err
	}
	if openTimeout <= 0 {
		openTimeout = linkRelaySpeedTestTimeout
	}
	relayURL, err := buildLinkRelayURL(relayHostHeader, endpoint.EntryPort, endpoint.ChainID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), openTimeout)
	dialHostPort := net.JoinHostPort(relayDialHost, strconv.Itoa(endpoint.EntryPort))
	tlsConf := &tls.Config{
		MinVersion:         tls.VersionTLS13,
		NextProtos:         []string{http3.NextProtoH3},
		ServerName:         resolveLinkTLSServerName(relayDialHost, relayHostHeader),
		InsecureSkipVerify: true,
	}
	quicConn, err := quic.DialAddr(ctx, dialHostPort, tlsConf, newLinkQUICConfig())
	if err != nil {
		cancel()
		return nil, wrapLinkDialError("websocket-h3", relayDialHost, endpoint.EntryPort, err)
	}
	transport := &http3.Transport{}
	clientConn := transport.NewClientConn(quicConn)
	select {
	case <-clientConn.ReceivedSettings():
	case <-ctx.Done():
		_ = quicConn.CloseWithError(0, "h3 speed websocket settings timeout")
		cancel()
		return nil, fmt.Errorf("probe relay h3 websocket open timeout: relay=%s:%d", relayDialHost, endpoint.EntryPort)
	case <-clientConn.Context().Done():
		cancel()
		return nil, fmt.Errorf("probe relay h3 websocket failed: %w", context.Cause(clientConn.Context()))
	}
	if settings := clientConn.Settings(); settings == nil || !settings.EnableExtendedConnect {
		_ = quicConn.CloseWithError(0, "h3 websocket extended connect disabled")
		cancel()
		return nil, errors.New("probe relay h3 websocket failed: server did not enable extended connect")
	}
	stream, err := clientConn.OpenRequestStream(ctx)
	if err != nil {
		_ = quicConn.CloseWithError(0, "h3 speed websocket stream open failed")
		cancel()
		return nil, wrapLinkDialError("websocket-h3", relayDialHost, endpoint.EntryPort, err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodConnect, relayURL, nil)
	if err != nil {
		cancelLinkH3Stream(stream, quicConn, cancel, "h3 speed websocket request build failed")
		return nil, err
	}
	request.Proto = "websocket"
	request.ProtoMajor = 3
	request.ProtoMinor = 0
	request.Header, err = buildLinkRelayHeaders(endpoint.ChainID, endpoint.ChainSecret, linkRelayModeSpeedTest, "", byteCount)
	if err != nil {
		cancelLinkH3Stream(stream, quicConn, cancel, "h3 speed websocket auth failed")
		return nil, err
	}
	if strings.TrimSpace(relayHostHeader) != "" {
		request.Host = strings.TrimSpace(relayHostHeader)
	}
	if err := stream.SendRequestHeader(request); err != nil {
		cancelLinkH3Stream(stream, quicConn, cancel, "h3 speed websocket header send failed")
		return nil, wrapLinkDialError("websocket-h3", relayDialHost, endpoint.EntryPort, err)
	}
	response, err := stream.ReadResponse()
	if err != nil {
		cancelLinkH3Stream(stream, quicConn, cancel, "h3 speed websocket response failed")
		return nil, wrapLinkDialError("websocket-h3", relayDialHost, endpoint.EntryPort, err)
	}
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		_ = response.Body.Close()
		cancelLinkH3Stream(stream, quicConn, cancel, "h3 speed websocket status failed")
		return nil, fmt.Errorf("probe relay h3 websocket failed: status=%d body=%s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	cancelOnce := sync.Once{}
	return &linkHTTP3StreamNetConn{
		stream: stream,
		local:  dummyAddr("probe-chain-h3-speed-local"),
		remote: dummyAddr(dialHostPort),
		closeFn: func() error {
			var closeErr error
			cancelOnce.Do(func() {
				cancel()
				stream.CancelRead(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
				stream.CancelWrite(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
				closeErr = quicConn.CloseWithError(0, "h3 speed websocket closed")
			})
			return closeErr
		},
	}, nil
}

func consumeLinkRelaySpeedTestData(reader io.Reader, byteCount int64, maxDuration time.Duration, result *linkSpeedTestResult) {
	if result == nil {
		return
	}
	readStartedAt := time.Now()
	result.LocalStartedAt = readStartedAt.UTC().Format(time.RFC3339)
	result.ReadChunkBytes = int64(linkRelayWebSocketBufferBytes)
	if maxDuration <= 0 {
		maxDuration = linkRelaySpeedTestTimeout
	}
	if deadliner, ok := reader.(interface{ SetReadDeadline(time.Time) error }); ok {
		_ = deadliner.SetReadDeadline(readStartedAt.Add(maxDuration))
		defer deadliner.SetReadDeadline(time.Time{})
	}
	var first [1]byte
	firstN, firstErr := io.ReadFull(reader, first[:])
	firstAt := time.Now()
	if firstN > 0 {
		result.LatencyMS = durationMilliseconds(firstAt.Sub(readStartedAt))
		result.FirstByteMS = result.LatencyMS
		result.LocalFirstByteAt = firstAt.UTC().Format(time.RFC3339)
		result.ReadCalls++
		result.TotalReadBlockMS += result.LatencyMS
		result.LastReadBlockMS = result.LatencyMS
		result.MaxReadBlockMS = result.LatencyMS
	}
	remaining := byteCount - int64(firstN)
	if remaining < 0 {
		remaining = 0
	}
	n, err := copyLinkRelaySpeedTestData(io.LimitReader(reader, remaining), result)
	endedAt := time.Now()
	result.EndedAt = endedAt.UTC().Format(time.RFC3339)
	result.LocalCompletedAt = result.EndedAt
	result.Bytes = int64(firstN) + n
	result.DurationMS = durationMilliseconds(endedAt.Sub(readStartedAt))
	if result.ReadCalls > 0 {
		result.AvgReadBytes = result.Bytes / result.ReadCalls
	}
	if firstErr != nil {
		if isLinkSpeedTestDurationLimitErr(firstErr, result.Bytes) {
			finalizeLinkSpeedTestResult(result, readStartedAt, endedAt)
			return
		}
		result.Error = firstErr.Error()
		return
	}
	if err != nil {
		if isLinkSpeedTestDurationLimitErr(err, result.Bytes) {
			finalizeLinkSpeedTestResult(result, readStartedAt, endedAt)
			return
		}
		result.Error = err.Error()
		return
	}
	if result.Bytes <= 0 {
		result.Error = "speed test returned no data"
		return
	}
	if result.Bytes < byteCount {
		result.Error = fmt.Sprintf("speed test returned incomplete data: bytes=%d want=%d", result.Bytes, byteCount)
		return
	}
	finalizeLinkSpeedTestResult(result, readStartedAt, endedAt)
}

func finalizeLinkSpeedTestResult(result *linkSpeedTestResult, startedAt time.Time, endedAt time.Time) {
	if result.Bytes <= 0 {
		result.Error = "speed test returned no data"
		return
	}
	elapsed := endedAt.Sub(startedAt)
	if elapsed <= 0 {
		elapsed = time.Millisecond
	}
	result.RateBPS = int64(float64(result.Bytes) / elapsed.Seconds())
	result.OK = true
	result.Error = ""
}

func copyLinkRelaySpeedTestData(reader io.Reader, result *linkSpeedTestResult) (int64, error) {
	if reader == nil {
		return 0, nil
	}
	buf := make([]byte, linkRelayWebSocketBufferBytes)
	var total int64
	for {
		startedAt := time.Now()
		n, err := reader.Read(buf)
		elapsedMS := durationMilliseconds(time.Since(startedAt))
		if n > 0 {
			total += int64(n)
			if result != nil {
				result.ReadCalls++
				result.TotalReadBlockMS += elapsedMS
				result.LastReadBlockMS = elapsedMS
				if elapsedMS > result.MaxReadBlockMS {
					result.MaxReadBlockMS = elapsedMS
				}
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return total, nil
			}
			return total, err
		}
		if n == 0 {
			return total, io.ErrNoProgress
		}
	}
}

func isLinkSpeedTestDurationLimitErr(err error, bytesRead int64) bool {
	if err == nil || bytesRead <= 0 {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(text, "timeout") || strings.Contains(text, "deadline") || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}

func buildLinkRelayHeaders(chainID string, secret string, mode string, role string, speedBytes int64) (http.Header, error) {
	header := http.Header{}
	header.Set(linkLegacyChainIDHeader, strings.TrimSpace(chainID))
	header.Set(linkCodexChainIDHeader, strings.TrimSpace(chainID))
	header.Set(linkCodexVersionHeader, linkAuthPacketVersion)
	header.Set(linkCodexRelayModeHeader, strings.TrimSpace(mode))
	if strings.TrimSpace(role) != "" {
		header.Set(linkCodexRelayRoleHeader, normalizeLinkBridgeRole(role))
	}
	if speedBytes > 0 {
		header.Set(linkCodexSpeedBytesHeader, strconv.FormatInt(speedBytes, 10))
	}
	if err := applyLinkSecretAuthHeaders(header, chainID, secret); err != nil {
		return nil, err
	}
	return header, nil
}

func applyLinkSecretAuthHeaders(headers http.Header, chainID string, secret string) error {
	cleanChainID := strings.TrimSpace(chainID)
	cleanSecret := strings.TrimSpace(secret)
	if cleanChainID == "" {
		return errors.New("chain_id is required")
	}
	if cleanSecret == "" {
		return errors.New("link_secret is required")
	}
	nonce := randomHexToken(16)
	headers.Set("Authorization", "Bearer "+nonce)
	headers.Set(linkCodexAuthModeHeader, "secret_hmac")
	headers.Set(linkCodexMACHeader, buildLinkHMAC(cleanSecret, cleanChainID, nonce))
	return nil
}

func buildLinkHMAC(secret string, chainID string, nonce string) string {
	mac := hmac.New(sha256.New, []byte(strings.TrimSpace(secret)))
	_, _ = mac.Write([]byte(strings.TrimSpace(chainID)))
	_, _ = mac.Write([]byte("\n"))
	_, _ = mac.Write([]byte(strings.TrimSpace(nonce)))
	return hex.EncodeToString(mac.Sum(nil))
}

func buildLinkRelayURL(host string, port int, chainID string) (string, error) {
	return buildLinkRelayURLWithScheme("https", host, port, chainID)
}

func buildLinkRelayWebSocketURL(host string, port int, chainID string) (string, error) {
	return buildLinkRelayURLWithScheme("wss", host, port, chainID)
}

func buildLinkRelayURLWithScheme(scheme string, host string, port int, chainID string) (string, error) {
	cleanHost := strings.TrimSpace(strings.Trim(host, "[]"))
	if cleanHost == "" {
		return "", errors.New("empty relay host")
	}
	if port <= 0 || port > 65535 {
		return "", errors.New("invalid relay port")
	}
	u := &url.URL{
		Scheme: scheme,
		Host:   net.JoinHostPort(cleanHost, strconv.Itoa(port)),
		Path:   linkRelayAPIPath,
	}
	query := u.Query()
	query.Set("chain_id", strings.TrimSpace(chainID))
	u.RawQuery = query.Encode()
	return u.String(), nil
}

func resolveLinkDialHost(rawHost string) (dialHost string, hostHeader string, err error) {
	cleanHost := strings.TrimSpace(strings.Trim(rawHost, "[]"))
	if cleanHost == "" {
		return "", "", errors.New("empty relay host")
	}
	if parsed := net.ParseIP(cleanHost); parsed != nil {
		return parsed.String(), cleanHost, nil
	}
	return cleanHost, cleanHost, nil
}

func resolveLinkTLSServerName(dialHost string, hostHeader string) string {
	for _, candidate := range []string{hostHeader, dialHost} {
		clean := strings.TrimSpace(strings.Trim(candidate, "[]"))
		if clean == "" {
			continue
		}
		if ip := net.ParseIP(clean); ip == nil {
			return clean
		}
	}
	return ""
}

func wrapLinkDialError(protocol string, host string, port int, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("probe relay %s dial failed: relay=%s:%d: %w", normalizeLinkLayer(protocol), strings.TrimSpace(host), port, err)
}

func newLinkYamuxConfig() *yamux.Config {
	cfg := yamux.DefaultConfig()
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = 20 * time.Second
	cfg.ConnectionWriteTimeout = 2 * time.Minute
	cfg.MaxStreamWindowSize = 64 * 1024 * 1024
	return cfg
}

func newLinkQUICConfig() *quic.Config {
	return &quic.Config{
		Versions:                       []quic.Version{quic.Version2, quic.Version1},
		EnableDatagrams:                true,
		KeepAlivePeriod:                10 * time.Second,
		InitialStreamReceiveWindow:     linkRelayQUICInitialStreamWindow,
		MaxStreamReceiveWindow:         linkRelayQUICMaxStreamWindow,
		InitialConnectionReceiveWindow: linkRelayQUICInitialConnWindow,
		MaxConnectionReceiveWindow:     linkRelayQUICMaxConnWindow,
	}
}

func normalizeLinkBridgeRole(raw string) string {
	if strings.EqualFold(strings.TrimSpace(raw), "to_prev") {
		return "to_prev"
	}
	return linkBridgeRoleToNext
}

func durationMilliseconds(elapsed time.Duration) int64 {
	if elapsed <= 0 {
		return 1
	}
	ms := int64(elapsed / time.Millisecond)
	if ms <= 0 {
		return 1
	}
	return ms
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func marshalLinkJSON(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return `{"ok":false,"error":"json marshal failed"}`
	}
	return string(raw)
}

func cancelLinkH3Stream(stream *http3.RequestStream, conn *quic.Conn, cancel context.CancelFunc, reason string) {
	stream.CancelRead(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
	stream.CancelWrite(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
	_ = conn.CloseWithError(0, reason)
	cancel()
}

type linkHTTP3StreamNetConn struct {
	stream  *http3.RequestStream
	local   net.Addr
	remote  net.Addr
	closeFn func() error
}

func (c *linkHTTP3StreamNetConn) Read(p []byte) (int, error) {
	return c.stream.Read(p)
}

func (c *linkHTTP3StreamNetConn) Write(p []byte) (int, error) {
	return c.stream.Write(p)
}

func (c *linkHTTP3StreamNetConn) Close() error {
	if c.closeFn != nil {
		return c.closeFn()
	}
	return nil
}

func (c *linkHTTP3StreamNetConn) LocalAddr() net.Addr {
	return c.local
}

func (c *linkHTTP3StreamNetConn) RemoteAddr() net.Addr {
	return c.remote
}

func (c *linkHTTP3StreamNetConn) SetDeadline(t time.Time) error {
	if err := c.stream.SetReadDeadline(t); err != nil {
		return err
	}
	return c.stream.SetWriteDeadline(t)
}

func (c *linkHTTP3StreamNetConn) SetReadDeadline(t time.Time) error {
	return c.stream.SetReadDeadline(t)
}

func (c *linkHTTP3StreamNetConn) SetWriteDeadline(t time.Time) error {
	return c.stream.SetWriteDeadline(t)
}
