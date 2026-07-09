package mobilecore

import (
	"bufio"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

const (
	mobileVRouteRelayAPIPath = "/api/node/route/relay"

	mobileVRouteLegacyRouteIDHeader   = "X-CH-Route-ID"
	mobileVRouteCodexRouteIDHeader    = "X-Codex-Route-Id"
	mobileVRouteCodexAuthModeHeader   = "X-Codex-Auth-Mode"
	mobileVRouteCodexMACHeader        = "X-Codex-Mac"
	mobileVRouteCodexAuthTicketHeader = "X-Codex-User-Auth-Ticket"
	mobileVRouteCodexAuthTimeHeader   = "X-Codex-Auth-Timestamp"
	mobileVRouteCodexVersionHeader    = "X-Codex-Api-Version"
	mobileVRouteCodexRelayModeHeader  = "X-Codex-Relay-Mode"
	mobileVRouteCodexRelayRoleHeader  = "X-Codex-Relay-Role"

	mobileVRouteAuthPacketVersion = "2025-03-22"
	mobileVRouteRelayModeBridge   = "bridge"
	mobileVRouteBridgeRoleToNext  = "to_next"
	mobileVRouteBridgeRoleToPrev  = "to_prev"

	mobileVRouteFrameEnvelopeMagic      uint16 = 0x5652
	mobileVRouteFrameEnvelopeHeaderSize        = 12
	mobileVRouteFrameMaxControlBytes           = 8096
	mobileVRouteFrameMaxDataBytes              = 65535
	mobileVRouteFrameMaxBytes                  = mobileVRouteFrameEnvelopeHeaderSize + mobileVRouteFrameMaxControlBytes + mobileVRouteFrameMaxDataBytes
	mobileVRouteFrameMainTypeIP         uint16 = 1
	mobileVRouteIPSubTypeIPv4           uint16 = 1
	mobileVRouteFrameWriteTimeout              = 5 * time.Second
	mobileVRouteCarrierDialTimeout             = 12 * time.Second
)

type mobileVRouteFrame struct {
	MainType uint16
	SubType  uint16
	Control  []byte
	Data     []byte
}

type mobileVRouteFrameControlEnvelope struct {
	Path []string `json:"path,omitempty"`
}

type mobileVRouteForwardPlan struct {
	LocalNode  string
	ExitNode   string
	Path       []string
	NextNode   string
	Rule       mobileVRouteTopology
	RouteID    string
	RelayHost  string
	RelayPort  int
	BridgeRole string
	Layer      string
}

type mobileVRouteCarrier struct {
	key             string
	plan            mobileVRouteForwardPlan
	conn            net.Conn
	reader          *bufio.Reader
	writeMu         sync.Mutex
	closeOne        sync.Once
	createdUnixNS   int64
	lastActivityNS  atomic.Int64
	txFrames        atomic.Int64
	txBytes         atomic.Int64
	rxFrames        atomic.Int64
	rxBytes         atomic.Int64
	lastErrorMu     sync.Mutex
	lastError       string
	lastErrorUnixNS int64
}

var mobileVRouteCarrierState = struct {
	mu              sync.Mutex
	items           map[string]*mobileVRouteCarrier
	lastError       string
	lastErrorUnixNS int64
}{items: map[string]*mobileVRouteCarrier{}}

func mobileVRouteHandleVPNPacket(configDir string, packet []byte, writeBack func([]byte) error) (bool, error) {
	if len(packet) == 0 {
		return false, nil
	}
	dstIP, dstPort, ok := mobileVRouteIPv4PacketTarget(packet)
	if !ok {
		return false, nil
	}
	targetAddr := net.JoinHostPort(dstIP, firstNonEmptyString(dstPort, "0"))
	route, err := decideVPNRouteForTarget(targetAddr)
	if err != nil {
		return true, err
	}
	if route.Reject {
		return true, nil
	}
	if route.Direct || strings.TrimSpace(route.SelectedRouteID) == "" {
		return false, nil
	}
	if mobileVRouteRouteIsLocalExit(configDir, route.SelectedRouteID) {
		return false, nil
	}
	plan, err := buildMobileVRouteForwardPlan(configDir, route.SelectedRouteID)
	if err != nil {
		return true, err
	}
	carrier, err := ensureMobileVRouteCarrier(plan, writeBack)
	if err != nil {
		return true, err
	}
	return true, carrier.writeIPPacket(packet, plan.Path)
}

func buildMobileVRouteForwardPlan(configDir string, routeID string) (mobileVRouteForwardPlan, error) {
	config, err := loadMobileVRouteConfig(configDir)
	if err != nil {
		return mobileVRouteForwardPlan{}, err
	}
	localNode := normalizeMobileRouteNodeID(config.LocalNodeID)
	exitNode := mobileVRouteProbeExitNodeFromRouteID(routeID)
	if localNode == "" {
		return mobileVRouteForwardPlan{}, errors.New("vroute local node id is missing")
	}
	if exitNode == "" {
		return mobileVRouteForwardPlan{}, errors.New("vroute exit node id is missing")
	}
	path, err := mobileVRouteShortestPath(config, localNode, exitNode)
	if err != nil {
		return mobileVRouteForwardPlan{}, err
	}
	if len(path) < 2 {
		return mobileVRouteForwardPlan{}, fmt.Errorf("vroute path unavailable: %s>%s", localNode, exitNode)
	}
	nextNode := path[1]
	rule, reverse, ok := mobileVRouteFindTopologyRule(config, localNode, nextNode)
	if !ok {
		return mobileVRouteForwardPlan{}, fmt.Errorf("vroute adjacent topology unavailable: %s>%s", localNode, nextNode)
	}
	if reverse {
		return mobileVRouteForwardPlan{}, fmt.Errorf("vroute reverse first hop requires inbound relay listener and is not supported by mobilecore: %s>%s", localNode, nextNode)
	}
	host, port := mobileVRoutePeerEndpoint(config, rule, reverse)
	if host == "" || port <= 0 {
		return mobileVRouteForwardPlan{}, fmt.Errorf("vroute adjacent endpoint unavailable: %s>%s", localNode, nextNode)
	}
	if strings.TrimSpace(rule.Secret) == "" {
		return mobileVRouteForwardPlan{}, fmt.Errorf("vroute adjacent route secret missing: %s>%s", localNode, nextNode)
	}
	if strings.TrimSpace(rule.AuthTicket) == "" {
		return mobileVRouteForwardPlan{}, fmt.Errorf("vroute adjacent auth ticket missing: %s>%s", localNode, nextNode)
	}
	bridgeRole := mobileVRouteBridgeRoleToNext
	if reverse {
		bridgeRole = mobileVRouteBridgeRoleToPrev
	}
	return mobileVRouteForwardPlan{
		LocalNode:  localNode,
		ExitNode:   exitNode,
		Path:       path,
		NextNode:   nextNode,
		Rule:       rule,
		RouteID:    mobileVRouteRuntimeRouteID(rule),
		RelayHost:  host,
		RelayPort:  port,
		BridgeRole: bridgeRole,
		Layer:      normalizeMobileVRouteRelayLayer(rule.RouteLayer),
	}, nil
}

func ensureMobileVRouteCarrier(plan mobileVRouteForwardPlan, writeBack func([]byte) error) (*mobileVRouteCarrier, error) {
	key := mobileVRouteCarrierKey(plan)
	mobileVRouteCarrierState.mu.Lock()
	if existing := mobileVRouteCarrierState.items[key]; existing != nil {
		mobileVRouteCarrierState.mu.Unlock()
		return existing, nil
	}
	mobileVRouteCarrierState.mu.Unlock()

	conn, err := dialMobileVRouteCarrier(plan)
	if err != nil {
		return nil, err
	}
	carrier := &mobileVRouteCarrier{
		key:           key,
		plan:          plan,
		conn:          conn,
		reader:        bufio.NewReaderSize(conn, 256*1024),
		createdUnixNS: time.Now().UnixNano(),
	}
	carrier.markActivity()
	mobileVRouteCarrierState.mu.Lock()
	if existing := mobileVRouteCarrierState.items[key]; existing != nil {
		mobileVRouteCarrierState.mu.Unlock()
		_ = conn.Close()
		return existing, nil
	}
	mobileVRouteCarrierState.items[key] = carrier
	mobileVRouteCarrierState.mu.Unlock()
	go carrier.readLoop(writeBack)
	return carrier, nil
}

func (c *mobileVRouteCarrier) writeIPPacket(packet []byte, path []string) error {
	if c == nil || c.conn == nil {
		return io.ErrClosedPipe
	}
	frame, err := buildMobileVRouteIPFrame(packet, path)
	if err != nil {
		return err
	}
	payload, err := encodeMobileVRouteFrame(frame)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if mobileVRouteFrameWriteTimeout > 0 {
		_ = c.conn.SetWriteDeadline(time.Now().Add(mobileVRouteFrameWriteTimeout))
		defer c.conn.SetWriteDeadline(time.Time{})
	}
	if err := writeMobileVRouteAll(c.conn, payload); err != nil {
		c.markError(err)
		c.close()
		return err
	}
	c.txFrames.Add(1)
	c.txBytes.Add(int64(len(packet)))
	c.markActivity()
	return nil
}

func (c *mobileVRouteCarrier) readLoop(writeBack func([]byte) error) {
	defer c.close()
	for {
		frame, err := readMobileVRouteFrame(c.reader)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				c.markError(err)
				androidLogStore.add("vpn", "warn", "vroute carrier read failed: "+err.Error())
			}
			return
		}
		if frame.MainType != mobileVRouteFrameMainTypeIP || frame.SubType != mobileVRouteIPSubTypeIPv4 || len(frame.Data) == 0 {
			continue
		}
		c.rxFrames.Add(1)
		c.rxBytes.Add(int64(len(frame.Data)))
		c.markActivity()
		if writeBack != nil {
			if err := writeBack(append([]byte(nil), frame.Data...)); err != nil {
				c.markError(err)
				androidLogStore.add("vpn", "warn", "vroute packet writeback failed: "+err.Error())
			}
		}
	}
}

func (c *mobileVRouteCarrier) close() {
	if c == nil {
		return
	}
	c.closeOne.Do(func() {
		if c.conn != nil {
			_ = c.conn.Close()
		}
		mobileVRouteCarrierState.mu.Lock()
		if mobileVRouteCarrierState.items[c.key] == c {
			delete(mobileVRouteCarrierState.items, c.key)
		}
		mobileVRouteCarrierState.mu.Unlock()
	})
}

func (c *mobileVRouteCarrier) markActivity() {
	if c == nil {
		return
	}
	c.lastActivityNS.Store(time.Now().UnixNano())
}

func (c *mobileVRouteCarrier) markError(err error) {
	if c == nil || err == nil {
		return
	}
	now := time.Now().UnixNano()
	text := strings.TrimSpace(err.Error())
	c.lastErrorMu.Lock()
	c.lastError = text
	c.lastErrorUnixNS = now
	c.lastErrorMu.Unlock()
	mobileVRouteCarrierState.mu.Lock()
	mobileVRouteCarrierState.lastError = text
	mobileVRouteCarrierState.lastErrorUnixNS = now
	mobileVRouteCarrierState.mu.Unlock()
}

func closeMobileVRouteCarriers() {
	mobileVRouteCarrierState.mu.Lock()
	items := make([]*mobileVRouteCarrier, 0, len(mobileVRouteCarrierState.items))
	for _, item := range mobileVRouteCarrierState.items {
		items = append(items, item)
	}
	mobileVRouteCarrierState.items = map[string]*mobileVRouteCarrier{}
	mobileVRouteCarrierState.mu.Unlock()
	for _, item := range items {
		item.close()
	}
}

func mobileVRouteRouteIsLocalExit(configDir string, routeID string) bool {
	exitNode := mobileVRouteProbeExitNodeFromRouteID(routeID)
	if exitNode == "" {
		return false
	}
	config, err := loadMobileVRouteConfig(configDir)
	if err != nil {
		return false
	}
	return exitNode == normalizeMobileRouteNodeID(config.LocalNodeID)
}

func mobileVRouteRuntimeStatusPayload(configDir string) map[string]any {
	return map[string]any{
		"config":       mobileVRouteStatusPayload(configDir),
		"carriers":     snapshotMobileVRouteCarriers(),
		"capabilities": mobileVRouteCapabilitiesPayload(),
	}
}

func mobileVRouteCapabilitiesPayload() map[string]any {
	return map[string]any{
		"ip_frame":           true,
		"ipv4":               true,
		"websocket_carrier":  true,
		"websocket_h3":       false,
		"outbound_dialer":    true,
		"inbound_listener":   false,
		"reverse_first_hop":  false,
		"control_ping":       false,
		"path_rtt":           false,
		"route_test":         false,
		"speed_test":         false,
		"debug_log_pull":     false,
		"fake_ip_verify":     false,
		"vpn_tun_writeback":  true,
		"config_hot_refresh": true,
	}
}

func snapshotMobileVRouteCarriers() map[string]any {
	mobileVRouteCarrierState.mu.Lock()
	items := make([]*mobileVRouteCarrier, 0, len(mobileVRouteCarrierState.items))
	for _, item := range mobileVRouteCarrierState.items {
		items = append(items, item)
	}
	lastError := mobileVRouteCarrierState.lastError
	lastErrorUnixNS := mobileVRouteCarrierState.lastErrorUnixNS
	mobileVRouteCarrierState.mu.Unlock()

	carriers := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		item.lastErrorMu.Lock()
		itemLastError := item.lastError
		itemLastErrorUnixNS := item.lastErrorUnixNS
		item.lastErrorMu.Unlock()
		carriers = append(carriers, map[string]any{
			"route_id":         item.plan.RouteID,
			"path":             append([]string(nil), item.plan.Path...),
			"next_node":        item.plan.NextNode,
			"exit_node":        item.plan.ExitNode,
			"relay":            net.JoinHostPort(item.plan.RelayHost, strconv.Itoa(item.plan.RelayPort)),
			"bridge_role":      item.plan.BridgeRole,
			"layer":            item.plan.Layer,
			"tx_frames":        item.txFrames.Load(),
			"tx_bytes":         item.txBytes.Load(),
			"rx_frames":        item.rxFrames.Load(),
			"rx_bytes":         item.rxBytes.Load(),
			"created_at":       mobileVRouteUnixNanoRFC3339(item.createdUnixNS),
			"last_activity_at": mobileVRouteUnixNanoRFC3339(item.lastActivityNS.Load()),
			"last_error":       itemLastError,
			"last_error_at":    mobileVRouteUnixNanoRFC3339(itemLastErrorUnixNS),
		})
	}
	return map[string]any{
		"active":        len(carriers),
		"items":         carriers,
		"last_error":    strings.TrimSpace(lastError),
		"last_error_at": mobileVRouteUnixNanoRFC3339(lastErrorUnixNS),
	}
}

func mobileVRouteUnixNanoRFC3339(value int64) string {
	if value <= 0 {
		return ""
	}
	return time.Unix(0, value).UTC().Format(time.RFC3339Nano)
}

func dialMobileVRouteCarrier(plan mobileVRouteForwardPlan) (net.Conn, error) {
	switch normalizeMobileVRouteRelayLayer(plan.Layer) {
	case "websocket":
		return dialMobileVRouteWebSocketCarrier(plan)
	case "websocket-h3":
		return nil, errors.New("vroute relay protocol websocket-h3 is not supported by mobilecore yet")
	default:
		return nil, fmt.Errorf("unsupported vroute relay protocol: %s", strings.TrimSpace(plan.Layer))
	}
}

func dialMobileVRouteWebSocketCarrier(plan mobileVRouteForwardPlan) (net.Conn, error) {
	relayURL, err := mobileVRouteRelayWebSocketURL(plan.RelayHost, plan.RelayPort, plan.RouteID)
	if err != nil {
		return nil, err
	}
	header := http.Header{}
	header.Set(mobileVRouteLegacyRouteIDHeader, strings.TrimSpace(plan.RouteID))
	header.Set(mobileVRouteCodexRouteIDHeader, strings.TrimSpace(plan.RouteID))
	header.Set(mobileVRouteCodexVersionHeader, mobileVRouteAuthPacketVersion)
	header.Set(mobileVRouteCodexRelayModeHeader, mobileVRouteRelayModeBridge)
	header.Set(mobileVRouteCodexRelayRoleHeader, plan.BridgeRole)
	if err := applyMobileVRouteSecretAuthHeaders(header, plan.RouteID, plan.Rule.Secret, plan.Rule.AuthTicket); err != nil {
		return nil, err
	}
	dialer := websocket.Dialer{
		HandshakeTimeout:  mobileVRouteCarrierDialTimeout,
		Proxy:             nil,
		ReadBufferSize:    512 * 1024,
		WriteBufferSize:   512 * 1024,
		EnableCompression: false,
		TLSClientConfig: &tls.Config{
			MinVersion:         tls.VersionTLS12,
			ServerName:         strings.TrimSpace(plan.RelayHost),
			InsecureSkipVerify: true,
		},
	}
	ws, resp, err := dialer.Dial(relayURL, header)
	if err != nil {
		if resp != nil && resp.Body != nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
			_ = resp.Body.Close()
			return nil, fmt.Errorf("vroute websocket failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		return nil, err
	}
	androidLogStore.add("vpn", "info", "vroute carrier connected: route="+plan.RouteID+" path="+strings.Join(plan.Path, ">"))
	return newWebSocketNetConn(ws), nil
}

func applyMobileVRouteSecretAuthHeaders(headers http.Header, routeID string, secret string, authTicket string) error {
	cleanRouteID := strings.TrimSpace(routeID)
	cleanSecret := strings.TrimSpace(secret)
	if cleanRouteID == "" {
		return errors.New("route_id is required")
	}
	if cleanSecret == "" {
		return errors.New("route_secret is required")
	}
	nonce := randomHexToken(16)
	headers.Set("Authorization", "Bearer "+nonce)
	headers.Set(mobileVRouteCodexAuthModeHeader, "secret_hmac")
	headers.Set(mobileVRouteCodexAuthTimeHeader, time.Now().UTC().Format(time.RFC3339Nano))
	headers.Set(mobileVRouteCodexMACHeader, buildMobileVRouteHMAC(cleanSecret, cleanRouteID, nonce))
	if strings.TrimSpace(authTicket) != "" {
		headers.Set(mobileVRouteCodexAuthTicketHeader, strings.TrimSpace(authTicket))
	}
	return nil
}

func buildMobileVRouteHMAC(secret string, routeID string, nonce string) string {
	h := hmac.New(sha256.New, []byte(strings.TrimSpace(secret)))
	_, _ = h.Write([]byte(strings.TrimSpace(routeID)))
	_, _ = h.Write([]byte("\n"))
	_, _ = h.Write([]byte(strings.TrimSpace(nonce)))
	return hex.EncodeToString(h.Sum(nil))
}

func mobileVRouteRelayWebSocketURL(host string, port int, routeID string) (string, error) {
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if host == "" {
		return "", errors.New("relay host is required")
	}
	if port <= 0 || port > 65535 {
		return "", fmt.Errorf("invalid relay port: %d", port)
	}
	u := &url.URL{
		Scheme: "wss",
		Host:   net.JoinHostPort(host, strconv.Itoa(port)),
		Path:   mobileVRouteRelayAPIPath,
	}
	query := u.Query()
	query.Set("route_id", strings.TrimSpace(routeID))
	u.RawQuery = query.Encode()
	return u.String(), nil
}

func buildMobileVRouteIPFrame(packet []byte, path []string) (mobileVRouteFrame, error) {
	if len(packet) == 0 {
		return mobileVRouteFrame{}, errors.New("vroute ip packet is empty")
	}
	control, err := json.Marshal(mobileVRouteFrameControlEnvelope{Path: mobileVRouteCleanPath(path)})
	if err != nil {
		return mobileVRouteFrame{}, err
	}
	if len(control) > mobileVRouteFrameMaxControlBytes {
		return mobileVRouteFrame{}, fmt.Errorf("vroute frame control too large: %d", len(control))
	}
	return mobileVRouteFrame{
		MainType: mobileVRouteFrameMainTypeIP,
		SubType:  mobileVRouteIPSubTypeIPv4,
		Control:  control,
		Data:     append([]byte(nil), packet...),
	}, nil
}

func encodeMobileVRouteFrame(frame mobileVRouteFrame) ([]byte, error) {
	controlLen := len(frame.Control)
	dataLen := len(frame.Data)
	if controlLen > mobileVRouteFrameMaxControlBytes {
		return nil, fmt.Errorf("vroute frame control too large: %d", controlLen)
	}
	if dataLen > mobileVRouteFrameMaxDataBytes {
		return nil, fmt.Errorf("vroute frame data too large: %d", dataLen)
	}
	frameLen := mobileVRouteFrameEnvelopeHeaderSize + controlLen + dataLen
	out := make([]byte, frameLen)
	binary.BigEndian.PutUint16(out[0:2], mobileVRouteFrameEnvelopeMagic)
	binary.BigEndian.PutUint16(out[2:4], frame.MainType)
	binary.BigEndian.PutUint16(out[4:6], frame.SubType)
	binary.BigEndian.PutUint16(out[6:8], uint16(controlLen))
	binary.BigEndian.PutUint16(out[8:10], uint16(dataLen))
	copy(out[mobileVRouteFrameEnvelopeHeaderSize:], frame.Control)
	copy(out[mobileVRouteFrameEnvelopeHeaderSize+controlLen:], frame.Data)
	binary.BigEndian.PutUint16(out[10:12], mobileVRouteFrameChecksum(out[:10], frame.Control, frame.Data))
	return out, nil
}

func readMobileVRouteFrame(reader *bufio.Reader) (mobileVRouteFrame, error) {
	header := make([]byte, mobileVRouteFrameEnvelopeHeaderSize)
	if _, err := io.ReadFull(reader, header); err != nil {
		return mobileVRouteFrame{}, err
	}
	if binary.BigEndian.Uint16(header[0:2]) != mobileVRouteFrameEnvelopeMagic {
		return mobileVRouteFrame{}, errors.New("invalid vroute frame magic")
	}
	controlLen := int(binary.BigEndian.Uint16(header[6:8]))
	dataLen := int(binary.BigEndian.Uint16(header[8:10]))
	if controlLen > mobileVRouteFrameMaxControlBytes || dataLen > mobileVRouteFrameMaxDataBytes || mobileVRouteFrameEnvelopeHeaderSize+controlLen+dataLen > mobileVRouteFrameMaxBytes {
		return mobileVRouteFrame{}, errors.New("invalid vroute frame length")
	}
	payload := make([]byte, controlLen+dataLen)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return mobileVRouteFrame{}, err
	}
	control := payload[:controlLen]
	data := payload[controlLen:]
	if got, want := binary.BigEndian.Uint16(header[10:12]), mobileVRouteFrameChecksum(header[:10], control, data); got != want {
		return mobileVRouteFrame{}, errors.New("vroute frame checksum mismatch")
	}
	return mobileVRouteFrame{
		MainType: binary.BigEndian.Uint16(header[2:4]),
		SubType:  binary.BigEndian.Uint16(header[4:6]),
		Control:  append([]byte(nil), control...),
		Data:     append([]byte(nil), data...),
	}, nil
}

func mobileVRouteFrameChecksum(headerPrefix []byte, control []byte, data []byte) uint16 {
	sum := uint32(0)
	add := func(payload []byte) {
		for len(payload) > 1 {
			sum += uint32(binary.BigEndian.Uint16(payload[:2]))
			payload = payload[2:]
		}
		if len(payload) == 1 {
			sum += uint32(payload[0]) << 8
		}
		for (sum >> 16) != 0 {
			sum = (sum & 0xffff) + (sum >> 16)
		}
	}
	add(headerPrefix)
	add(control)
	add(data)
	return ^uint16(sum)
}

func writeMobileVRouteAll(writer io.Writer, payload []byte) error {
	for len(payload) > 0 {
		n, err := writer.Write(payload)
		if n > 0 {
			payload = payload[n:]
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func mobileVRouteShortestPath(config mobileVRouteConfig, from string, to string) ([]string, error) {
	from = normalizeMobileRouteNodeID(from)
	to = normalizeMobileRouteNodeID(to)
	if from == "" || to == "" {
		return nil, errors.New("vroute path endpoint is missing")
	}
	if from == to {
		return []string{from}, nil
	}
	neighbors := map[string][]string{}
	for _, rule := range config.TopologyRules {
		left := normalizeMobileRouteNodeID(rule.FromNodeID)
		right := normalizeMobileRouteNodeID(rule.ToNodeID)
		if left == "" || right == "" {
			continue
		}
		neighbors[left] = append(neighbors[left], right)
		neighbors[right] = append(neighbors[right], left)
	}
	queue := []string{from}
	prev := map[string]string{from: ""}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, next := range neighbors[current] {
			if _, seen := prev[next]; seen {
				continue
			}
			prev[next] = current
			if next == to {
				path := []string{to}
				for at := current; at != ""; at = prev[at] {
					path = append([]string{at}, path...)
				}
				return path, nil
			}
			queue = append(queue, next)
		}
	}
	return nil, fmt.Errorf("vroute path unavailable: %s>%s", from, to)
}

func mobileVRouteFindTopologyRule(config mobileVRouteConfig, local string, peer string) (mobileVRouteTopology, bool, bool) {
	local = normalizeMobileRouteNodeID(local)
	peer = normalizeMobileRouteNodeID(peer)
	for _, rule := range config.TopologyRules {
		from := normalizeMobileRouteNodeID(rule.FromNodeID)
		to := normalizeMobileRouteNodeID(rule.ToNodeID)
		if from == local && to == peer {
			return rule, false, true
		}
		if from == peer && to == local {
			return rule, true, true
		}
	}
	return mobileVRouteTopology{}, false, false
}

func mobileVRoutePeerEndpoint(config mobileVRouteConfig, rule mobileVRouteTopology, reverse bool) (string, int) {
	host := strings.TrimSpace(rule.ToServiceDomain)
	port := mobileVRouteServicePortForNode(config, rule.ToNodeID, rule.ToServicePort)
	if reverse {
		host = strings.TrimSpace(rule.FromServiceDomain)
		port = mobileVRouteServicePortForNode(config, rule.FromNodeID, rule.FromServicePort)
	}
	if mobileVRouteIsCloudflareCopilotDomain(host) {
		port = 443
	}
	return host, port
}

func mobileVRouteRuntimeRouteID(rule mobileVRouteTopology) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{"route", strings.TrimSpace(rule.ID)}, "|")))
	return "vrouter-" + hex.EncodeToString(sum[:])[:24]
}

func mobileVRouteCarrierKey(plan mobileVRouteForwardPlan) string {
	return strings.Join([]string{plan.RouteID, plan.BridgeRole, plan.RelayHost, strconv.Itoa(plan.RelayPort)}, "|")
}

func mobileVRouteCleanPath(path []string) []string {
	out := make([]string, 0, len(path))
	for _, item := range path {
		if nodeID := normalizeMobileRouteNodeID(item); nodeID != "" {
			out = append(out, nodeID)
		}
	}
	return out
}

func mobileVRouteIPv4PacketTarget(packet []byte) (string, string, bool) {
	if len(packet) < 20 || packet[0]>>4 != 4 {
		return "", "", false
	}
	ihl := int(packet[0]&0x0f) * 4
	if ihl < 20 || len(packet) < ihl {
		return "", "", false
	}
	totalLen := int(binary.BigEndian.Uint16(packet[2:4]))
	if totalLen < ihl || totalLen > len(packet) {
		totalLen = len(packet)
	}
	dst := net.IPv4(packet[16], packet[17], packet[18], packet[19]).String()
	proto := packet[9]
	if (proto == 6 || proto == 17) && totalLen >= ihl+4 {
		return dst, strconv.Itoa(int(binary.BigEndian.Uint16(packet[ihl+2 : ihl+4]))), true
	}
	return dst, "0", true
}

func normalizeMobileVRouteRelayLayer(layer string) string {
	switch strings.ToLower(strings.TrimSpace(layer)) {
	case "websocket", "ws", "http", "https", "http2", "h2":
		return "websocket"
	case "websocket-h3", "ws-h3", "h3-websocket", "h3-ws", "h3", "http3", "quic":
		return "websocket-h3"
	case "auto", "", "default":
		return "websocket"
	default:
		return strings.ToLower(strings.TrimSpace(layer))
	}
}

func mobileVRouteIsCloudflareCopilotDomain(domain string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(domain)), "api_copilot_")
}
