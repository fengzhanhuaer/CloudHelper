package mobilecore

import (
	"bufio"
	"context"
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
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

const (
	mobileVRouteRelayAPIPath = "/api/node/route/relay"

	mobileVRouteLegacyRouteIDHeader   = "X-CH-Route-ID"
	mobileVRouteCodexRouteIDHeader    = "X-Codex-Route-Id"
	mobileVRouteCodexAuthModeHeader   = "X-Codex-Auth-Mode"
	mobileVRouteCodexMACHeader        = "X-Codex-Mac"
	mobileVRouteCodexAuthTicketHeader = "X-Codex-User-Auth-Ticket"
	mobileVRouteCodexVersionHeader    = "X-Codex-Api-Version"
	mobileVRouteCodexRelayModeHeader  = "X-Codex-Relay-Mode"
	mobileVRouteCodexRelayRoleHeader  = "X-Codex-Relay-Role"
	mobileVRouteCodexSourceNodeHeader = "X-Codex-Source-Node-Id"

	mobileVRouteAuthPacketVersion = "2025-03-22"
	mobileVRouteRelayModeBridge   = "bridge"
	mobileVRouteBridgeRoleToNext  = "to_next"
	mobileVRouteBridgeRoleToPrev  = "to_prev"

	mobileVRouteFrameEnvelopeMagic           uint16 = 0x5652
	mobileVRouteFrameEnvelopeHeaderSize             = 12
	mobileVRouteFrameMaxControlBytes                = 8096
	mobileVRouteFrameMaxDataBytes                   = 65535
	mobileVRouteFrameMaxBytes                       = mobileVRouteFrameEnvelopeHeaderSize + mobileVRouteFrameMaxControlBytes + mobileVRouteFrameMaxDataBytes
	mobileVRouteFrameMainTypeIP              uint16 = 1
	mobileVRouteIPSubTypeIPv4                uint16 = 1
	mobileVRouteFrameMainTypePingPong        uint16 = 2
	mobileVRoutePingPongSubTypePing          uint16 = 1
	mobileVRoutePingPongSubTypePong          uint16 = 2
	mobileVRouteFrameMainTypePathRTT         uint16 = 3
	mobileVRoutePathRTTSubTypeQuery          uint16 = 1
	mobileVRoutePathRTTSubTypeResponse       uint16 = 2
	mobileVRouteFrameMainTypeDebugLog        uint16 = 7
	mobileVRouteDebugLogSubTypeQuery         uint16 = 1
	mobileVRouteDebugLogSubTypeResponse      uint16 = 2
	mobileVRouteFrameWriteTimeout                   = 15 * time.Second
	mobileVRouteCarrierDialTimeout                  = 12 * time.Second
	mobileVRouteCarrierRetryMax                     = 30 * time.Second
	mobileVRouteCarrierTXBufferFrames               = 256
	mobileVRouteCarrierTXControlBufferFrames        = 32
	mobileVRouteCarrierTXBatchBytes                 = 64 * 1024
	mobileVRouteCarrierTXCoalesceWindow             = 200 * time.Microsecond
	mobileVRouteCarrierRXBufferFrames               = 512
	mobileVRouteMaxHops                             = 3
	mobileVRouteRelayResolveTimeout                 = 5 * time.Second
	mobileVRouteH3StreamOpenTimeout                 = 6 * time.Second
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
	Config     mobileVRouteConfig
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
	closeOne        sync.Once
	createdUnixNS   int64
	lastActivityNS  atomic.Int64
	txFrames        atomic.Int64
	txBytes         atomic.Int64
	txIPFrames      atomic.Int64
	txIPBytes       atomic.Int64
	txControlFrames atomic.Int64
	txDropped       atomic.Int64
	txLastWriteNS   atomic.Int64
	txWriteEMANS    atomic.Int64
	txBatchFrames   atomic.Int64
	txBatchBytes    atomic.Int64
	rxFrames        atomic.Int64
	rxBytes         atomic.Int64
	rxIPFrames      atomic.Int64
	rxIPBytes       atomic.Int64
	rxControlFrames atomic.Int64
	tunWriteFrames  atomic.Int64
	tunWriteBytes   atomic.Int64
	rxDropped       atomic.Int64
	lastErrorMu     sync.Mutex
	lastError       string
	lastErrorUnixNS int64
	writeBackMu     sync.RWMutex
	writeBack       func([]byte) error
	tx              chan mobileVRouteFrame
	txControl       chan mobileVRouteFrame
	rx              chan mobileVRouteFrame
	done            chan struct{}
}

type mobileVRouteCarrierWorker struct {
	plan     mobileVRouteForwardPlan
	stopCh   chan struct{}
	doneCh   chan struct{}
	stopOnce sync.Once
}

var mobileVRouteCarrierState = struct {
	mu              sync.Mutex
	items           map[string]*mobileVRouteCarrier
	workers         map[string]*mobileVRouteCarrierWorker
	lastError       string
	lastErrorUnixNS int64
}{
	items:   map[string]*mobileVRouteCarrier{},
	workers: map[string]*mobileVRouteCarrierWorker{},
}

var mobileVRouteCarrierDial = dialMobileVRouteCarrier
var mobileVRouteLookupIP = net.DefaultResolver.LookupIP
var mobileVRouteH3QUICDial = quic.DialAddr

type mobileVRouteRelayDialCandidate struct {
	URLHost  string
	DialHost string
	Network  string
}

type mobileVRouteRelayLookupResult struct {
	Network string
	IPs     []net.IP
	Err     error
}

type mobileVRouteNetAddr struct {
	label string
}

func (a mobileVRouteNetAddr) Network() string {
	return "mobile-vroute"
}

func (a mobileVRouteNetAddr) String() string {
	return strings.TrimSpace(a.label)
}

type mobileVRouteH3StreamNetConn struct {
	stream  mobileVRouteH3Stream
	local   net.Addr
	remote  net.Addr
	closeFn func() error
}

type mobileVRouteH3Stream interface {
	io.ReadWriteCloser
	SetDeadline(time.Time) error
	SetReadDeadline(time.Time) error
	SetWriteDeadline(time.Time) error
}

func (c *mobileVRouteH3StreamNetConn) Read(payload []byte) (int, error) {
	if c == nil || c.stream == nil {
		return 0, io.EOF
	}
	return c.stream.Read(payload)
}

func (c *mobileVRouteH3StreamNetConn) Write(payload []byte) (int, error) {
	if c == nil || c.stream == nil {
		return 0, io.ErrClosedPipe
	}
	return c.stream.Write(payload)
}

func (c *mobileVRouteH3StreamNetConn) Close() error {
	if c == nil {
		return nil
	}
	if c.closeFn != nil {
		return c.closeFn()
	}
	if c.stream != nil {
		return c.stream.Close()
	}
	return nil
}

func (c *mobileVRouteH3StreamNetConn) LocalAddr() net.Addr {
	if c != nil && c.local != nil {
		return c.local
	}
	return mobileVRouteNetAddr{label: "mobile-vroute-h3-local"}
}

func (c *mobileVRouteH3StreamNetConn) RemoteAddr() net.Addr {
	if c != nil && c.remote != nil {
		return c.remote
	}
	return mobileVRouteNetAddr{label: "mobile-vroute-h3-remote"}
}

func (c *mobileVRouteH3StreamNetConn) SetDeadline(t time.Time) error {
	if c == nil || c.stream == nil {
		return nil
	}
	return c.stream.SetDeadline(t)
}

func (c *mobileVRouteH3StreamNetConn) SetReadDeadline(t time.Time) error {
	if c == nil || c.stream == nil {
		return nil
	}
	return c.stream.SetReadDeadline(t)
}

func (c *mobileVRouteH3StreamNetConn) SetWriteDeadline(t time.Time) error {
	if c == nil || c.stream == nil {
		return nil
	}
	return c.stream.SetWriteDeadline(t)
}

var mobileVRouteCarrierRetryMin = time.Second

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
		logAndroidVPNDiagnostic("takeover_decision_error", "error", "vroute takeover decision failed: target="+targetAddr+" err="+err.Error(), 2*time.Second)
		return true, err
	}
	if route.Reject {
		logAndroidVPNDiagnostic("takeover_reject_"+route.Group, "warning", "vroute takeover rejected: target="+targetAddr+" group="+route.Group, 5*time.Second)
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
		recordMobileVRouteConnectionFailure("route_plan_failed", targetAddr, route.SelectedRouteID, route.Group, "", err)
		logAndroidVPNDiagnostic("takeover_plan_error_"+route.SelectedRouteID, "error", "vroute takeover plan failed: target="+targetAddr+" route="+route.SelectedRouteID+" err="+err.Error(), 2*time.Second)
		return true, err
	}
	forwardPacket, tunSourceIP, err := mobileVRouteRewriteTUNPacketForForward(packet, plan)
	if err != nil {
		recordMobileVRouteConnectionFailure("source_route_rewrite_failed", targetAddr, plan.RouteID, route.Group, "", err)
		logAndroidVPNDiagnostic("takeover_source_route_error_"+plan.RouteID, "error", "vroute takeover source-route rewrite failed: route="+plan.RouteID+" err="+err.Error(), 2*time.Second)
		return true, err
	}
	carrierWriteBack := func(reply []byte) error {
		restored, restoreErr := mobileVRouteRestoreTUNPacketFromReply(reply, tunSourceIP)
		if restoreErr != nil {
			return restoreErr
		}
		if writeBack == nil {
			return nil
		}
		return writeBack(restored)
	}
	carrier, err := ensureMobileVRouteCarrier(plan, carrierWriteBack)
	if err != nil {
		recordMobileVRouteConnectionFailure("carrier_open_failed", targetAddr, plan.RouteID, route.Group, "", err)
		logAndroidVPNDiagnostic("takeover_carrier_error_"+plan.RouteID, "error", "vroute takeover carrier unavailable: target="+targetAddr+" route="+plan.RouteID+" next="+plan.NextNode+" relay="+net.JoinHostPort(plan.RelayHost, strconv.Itoa(plan.RelayPort))+" err="+err.Error(), 2*time.Second)
		return true, err
	}
	if err := carrier.writeIPPacket(forwardPacket, plan.Path); err != nil {
		recordMobileVRouteConnectionFailure("enqueue_failed", targetAddr, plan.RouteID, route.Group, "", err)
		logAndroidVPNDiagnostic("takeover_enqueue_error_"+plan.RouteID, "error", "vroute takeover enqueue failed: route="+plan.RouteID+" err="+err.Error(), 2*time.Second)
		return true, err
	}
	trackMobileVRouteOutbound(forwardPacket, route, plan)
	return true, nil
}

// The Android VPN TUN address is local to the handset.  Frames entering the
// shared virtual-router network must instead use this node's configured
// virtual-router address, so normal reverse path lookup resolves back to it.
func mobileVRouteRewriteTUNPacketForForward(packet []byte, plan mobileVRouteForwardPlan) ([]byte, string, error) {
	info, ok := parseAndroidVPNIPv4TransportPacket(packet)
	if !ok {
		return nil, "", errors.New("vroute packet is not IPv4 TCP/UDP")
	}
	localNodeID := normalizeMobileRouteNodeID(plan.LocalNode)
	virtualIP := strings.TrimSpace(mobileVRouteProbeIPForNode(plan.Config, localNodeID))
	if net.ParseIP(virtualIP).To4() == nil {
		return nil, "", fmt.Errorf("vroute local virtual IP is unavailable: node=%s", localNodeID)
	}
	forwardPacket, err := rewriteAndroidVPNIPv4Packet(packet, virtualIP, "")
	if err != nil {
		return nil, "", err
	}
	return forwardPacket, info.SourceIP, nil
}

func mobileVRouteRestoreTUNPacketFromReply(packet []byte, tunSourceIP string) ([]byte, error) {
	if net.ParseIP(strings.TrimSpace(tunSourceIP)).To4() == nil {
		return nil, fmt.Errorf("invalid Android VPN TUN source IP: %s", tunSourceIP)
	}
	return rewriteAndroidVPNIPv4Packet(packet, "", tunSourceIP)
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
	if err := validateMobileVRoutePath(path); err != nil {
		return mobileVRouteForwardPlan{}, err
	}
	nextNode := path[1]
	rule, reverse, ok := mobileVRouteFindTopologyRule(config, localNode, nextNode)
	if !ok {
		return mobileVRouteForwardPlan{}, fmt.Errorf("vroute adjacent topology unavailable: %s>%s", localNode, nextNode)
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
		Config:     config,
		Rule:       rule,
		RouteID:    mobileVRouteRuntimeRouteID(rule),
		RelayHost:  host,
		RelayPort:  port,
		BridgeRole: bridgeRole,
		Layer:      normalizeMobileVRouteRelayLayer(rule.RouteLayer),
	}, nil
}

func buildMobileVRouteAdjacentPlan(config mobileVRouteConfig, path []string, localNode string, nextNode string) (mobileVRouteForwardPlan, error) {
	path = mobileVRouteCleanPath(path)
	if err := validateMobileVRoutePath(path); err != nil {
		return mobileVRouteForwardPlan{}, err
	}
	localNode = normalizeMobileRouteNodeID(localNode)
	nextNode = normalizeMobileRouteNodeID(nextNode)
	if localNode == "" || nextNode == "" {
		return mobileVRouteForwardPlan{}, errors.New("vroute adjacent nodes are required")
	}
	rule, reverse, ok := mobileVRouteFindTopologyRule(config, localNode, nextNode)
	if !ok {
		return mobileVRouteForwardPlan{}, fmt.Errorf("vroute adjacent topology unavailable: %s>%s", localNode, nextNode)
	}
	host, port := mobileVRoutePeerEndpoint(config, rule, reverse)
	if host == "" || port <= 0 || strings.TrimSpace(rule.Secret) == "" || strings.TrimSpace(rule.AuthTicket) == "" {
		return mobileVRouteForwardPlan{}, fmt.Errorf("vroute adjacent endpoint or auth unavailable: %s>%s", localNode, nextNode)
	}
	bridgeRole := mobileVRouteBridgeRoleToNext
	if reverse {
		bridgeRole = mobileVRouteBridgeRoleToPrev
	}
	return mobileVRouteForwardPlan{
		LocalNode:  localNode,
		ExitNode:   path[len(path)-1],
		Path:       append([]string(nil), path...),
		NextNode:   nextNode,
		Config:     config,
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
		existing.setWriteBack(writeBack)
		return existing, nil
	}
	mobileVRouteCarrierState.mu.Unlock()

	conn, err := mobileVRouteCarrierDial(plan)
	if err != nil {
		return nil, err
	}
	carrier := newMobileVRouteCarrier(key, plan, conn)
	carrier.setWriteBack(writeBack)
	carrier.markActivity()
	mobileVRouteCarrierState.mu.Lock()
	if existing := mobileVRouteCarrierState.items[key]; existing != nil {
		mobileVRouteCarrierState.mu.Unlock()
		_ = conn.Close()
		existing.setWriteBack(writeBack)
		return existing, nil
	}
	mobileVRouteCarrierState.items[key] = carrier
	mobileVRouteCarrierState.mu.Unlock()
	carrier.start()
	return carrier, nil
}

func newMobileVRouteCarrier(key string, plan mobileVRouteForwardPlan, conn net.Conn) *mobileVRouteCarrier {
	if conn == nil {
		return nil
	}
	return &mobileVRouteCarrier{
		key:           key,
		plan:          plan,
		conn:          conn,
		reader:        bufio.NewReaderSize(conn, 128*1024),
		createdUnixNS: time.Now().UnixNano(),
		tx:            make(chan mobileVRouteFrame, mobileVRouteCarrierTXBufferFrames),
		txControl:     make(chan mobileVRouteFrame, mobileVRouteCarrierTXControlBufferFrames),
		rx:            make(chan mobileVRouteFrame, mobileVRouteCarrierRXBufferFrames),
		done:          make(chan struct{}),
	}
}

func (c *mobileVRouteCarrier) start() {
	if c == nil || c.conn == nil || c.tx == nil || c.txControl == nil || c.rx == nil || c.done == nil {
		return
	}
	go c.runTXWorker()
	go c.readLoop()
	go c.runRXWorker()
}

func (c *mobileVRouteCarrier) writeIPPacket(packet []byte, path []string) error {
	if c == nil || c.conn == nil {
		return io.ErrClosedPipe
	}
	frame, err := buildMobileVRouteIPFrame(packet, path)
	if err != nil {
		return err
	}
	return c.enqueueFrame(frame)
}

func (c *mobileVRouteCarrier) enqueueFrame(frame mobileVRouteFrame) error {
	queue, queueName := c.txQueueForFrame(frame)
	if queue == nil || c.done == nil {
		return io.ErrClosedPipe
	}
	select {
	case queue <- frame:
		return nil
	case <-c.done:
		return io.ErrClosedPipe
	default:
		c.txDropped.Add(1)
		return fmt.Errorf("mobile vroute tx queue full: route=%s queue=%s depth=%d capacity=%d", strings.TrimSpace(c.plan.RouteID), queueName, len(queue), cap(queue))
	}
}

func (c *mobileVRouteCarrier) runTXWorker() {
	if c == nil || c.tx == nil || c.txControl == nil || c.done == nil {
		return
	}
	for {
		frame, ok := c.nextTXFrame()
		if !ok {
			return
		}
		frames := []mobileVRouteFrame{frame}
		batchBytes := mobileVRouteFrameEnvelopeHeaderSize + len(frame.Control) + len(frame.Data)
		coalesceDeadline := time.Time{}
		if mobileVRouteFrameIsIP(frame) {
			coalesceDeadline = time.Now().Add(mobileVRouteCarrierTXCoalesceWindow)
		}
		for batchBytes < mobileVRouteCarrierTXBatchBytes {
			next, available := c.tryNextTXFrame()
			if !available && !coalesceDeadline.IsZero() {
				next, available = c.waitNextTXFrameUntil(coalesceDeadline)
			}
			if !available {
				break
			}
			frames = append(frames, next)
			batchBytes += mobileVRouteFrameEnvelopeHeaderSize + len(next.Control) + len(next.Data)
		}
		payload, err := encodeMobileVRouteFrames(frames)
		if err == nil && mobileVRouteFrameWriteTimeout > 0 {
			_ = c.conn.SetWriteDeadline(time.Now().Add(mobileVRouteFrameWriteTimeout))
		}
		if err == nil {
			writeStartedAt := time.Now()
			err = writeMobileVRouteAll(c.conn, payload)
			c.recordTXWriteBatch(time.Since(writeStartedAt), len(frames), len(payload))
		}
		if mobileVRouteFrameWriteTimeout > 0 {
			_ = c.conn.SetWriteDeadline(time.Time{})
		}
		if err != nil {
			c.markError(err)
			recordMobileVRouteCarrierStateError(err)
			failMobileVRouteTrackedFlowsForCarrier(c.plan, "carrier_write_failed", err)
			androidLogStore.add("vpn", "warn", "vroute carrier write failed: "+err.Error())
			c.close()
			return
		}
		for _, sentFrame := range frames {
			c.txFrames.Add(1)
			c.txBytes.Add(int64(len(sentFrame.Data)))
			if mobileVRouteFrameIsIP(sentFrame) {
				c.txIPFrames.Add(1)
				c.txIPBytes.Add(int64(len(sentFrame.Data)))
			} else {
				c.txControlFrames.Add(1)
			}
		}
		c.markActivity()
	}
}

func (c *mobileVRouteCarrier) txQueueForFrame(frame mobileVRouteFrame) (chan mobileVRouteFrame, string) {
	if c == nil {
		return nil, ""
	}
	if mobileVRouteFrameIsIP(frame) {
		return c.tx, "ip"
	}
	return c.txControl, "control"
}

func (c *mobileVRouteCarrier) nextTXFrame() (mobileVRouteFrame, bool) {
	if c == nil || c.done == nil {
		return mobileVRouteFrame{}, false
	}
	select {
	case <-c.done:
		return mobileVRouteFrame{}, false
	default:
	}
	select {
	case frame := <-c.txControl:
		return frame, true
	default:
	}
	select {
	case frame := <-c.txControl:
		return frame, true
	case frame := <-c.tx:
		return frame, true
	case <-c.done:
		return mobileVRouteFrame{}, false
	}
}

func (c *mobileVRouteCarrier) tryNextTXFrame() (mobileVRouteFrame, bool) {
	if c == nil {
		return mobileVRouteFrame{}, false
	}
	select {
	case frame := <-c.txControl:
		return frame, true
	default:
	}
	select {
	case frame := <-c.tx:
		return frame, true
	default:
		return mobileVRouteFrame{}, false
	}
}

func (c *mobileVRouteCarrier) waitNextTXFrameUntil(deadline time.Time) (mobileVRouteFrame, bool) {
	if c == nil || c.done == nil {
		return mobileVRouteFrame{}, false
	}
	if frame, ok := c.tryNextTXFrame(); ok {
		return frame, true
	}
	wait := time.Until(deadline)
	if wait <= 0 {
		return mobileVRouteFrame{}, false
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case frame := <-c.txControl:
		return frame, true
	case frame := <-c.tx:
		return frame, true
	case <-c.done:
		return mobileVRouteFrame{}, false
	case <-timer.C:
		return mobileVRouteFrame{}, false
	}
}

func (c *mobileVRouteCarrier) recordTXWriteBatch(value time.Duration, frames int, bytes int) {
	if c == nil || value < 0 {
		return
	}
	nanoseconds := int64(value)
	c.txLastWriteNS.Store(nanoseconds)
	c.txBatchFrames.Store(int64(frames))
	c.txBatchBytes.Store(int64(bytes))
	for {
		current := c.txWriteEMANS.Load()
		next := nanoseconds
		if current > 0 {
			next = (current*7 + nanoseconds) / 8
		}
		if c.txWriteEMANS.CompareAndSwap(current, next) {
			return
		}
	}
}

func (c *mobileVRouteCarrier) readLoop() {
	defer c.close()
	for {
		frame, err := readMobileVRouteFrame(c.reader)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				c.markError(err)
				failMobileVRouteTrackedFlowsForCarrier(c.plan, "carrier_read_failed", err)
				androidLogStore.add("vpn", "warn", "vroute carrier read failed: "+err.Error())
			}
			return
		}
		if err := c.handleIncomingFrame(frame); err != nil {
			c.markError(err)
			recordMobileVRouteCarrierStateError(err)
			androidLogStore.add("vpn", "warn", "vroute frame handling failed: "+err.Error())
		}
	}
}

func (c *mobileVRouteCarrier) handleIncomingFrame(frame mobileVRouteFrame) error {
	if c == nil {
		return io.ErrClosedPipe
	}
	control := mobileVRouteFrameControlEnvelope{}
	if err := json.Unmarshal(frame.Control, &control); err != nil {
		return err
	}
	path := mobileVRouteCleanPath(control.Path)
	if err := validateMobileVRoutePath(path); err != nil {
		return err
	}
	localNode := normalizeMobileRouteNodeID(c.plan.LocalNode)
	position := -1
	for index, nodeID := range path {
		if nodeID == localNode {
			position = index
			break
		}
	}
	if position < 0 {
		return fmt.Errorf("vroute frame path does not include local node=%s", localNode)
	}
	c.rxFrames.Add(1)
	c.rxBytes.Add(int64(len(frame.Data)))
	if mobileVRouteFrameIsIP(frame) {
		c.rxIPFrames.Add(1)
		c.rxIPBytes.Add(int64(len(frame.Data)))
	} else {
		c.rxControlFrames.Add(1)
	}
	c.markActivity()
	if position < len(path)-1 {
		nextNode := path[position+1]
		plan, err := buildMobileVRouteAdjacentPlan(c.plan.Config, path, localNode, nextNode)
		if err != nil {
			return err
		}
		carrier, err := ensureMobileVRouteCarrier(plan, c.currentWriteBack())
		if err != nil {
			return err
		}
		return carrier.enqueueFrame(frame)
	}
	if frame.MainType == mobileVRouteFrameMainTypePingPong && frame.SubType == mobileVRoutePingPongSubTypePong {
		return completeMobileVRouteRTTResponse(frame)
	}
	if frame.MainType == mobileVRouteFrameMainTypePathRTT && frame.SubType == mobileVRoutePathRTTSubTypeResponse {
		return completeMobileVRouteRTTResponse(frame)
	}
	if frame.MainType == mobileVRouteFrameMainTypePingPong && frame.SubType == mobileVRoutePingPongSubTypePing {
		return c.respondToRTTFrame(frame, path, mobileVRouteFrameMainTypePingPong, mobileVRoutePingPongSubTypePong)
	}
	if frame.MainType == mobileVRouteFrameMainTypePathRTT && frame.SubType == mobileVRoutePathRTTSubTypeQuery {
		return c.respondToRTTFrame(frame, path, mobileVRouteFrameMainTypePathRTT, mobileVRoutePathRTTSubTypeResponse)
	}
	if frame.MainType == mobileVRouteFrameMainTypeDebugLog && frame.SubType == mobileVRouteDebugLogSubTypeQuery {
		return c.respondToDebugLogFrame(frame, path)
	}
	if frame.MainType == mobileVRouteFrameMainTypeIP && frame.SubType == mobileVRouteIPSubTypeIPv4 && len(frame.Data) > 0 {
		select {
		case c.rx <- frame:
		case <-c.done:
			return io.ErrClosedPipe
		default:
			c.rxDropped.Add(1)
			androidLogStore.add("vpn", "warn", "vroute carrier rx queue full: route="+c.plan.RouteID+" depth="+strconv.Itoa(len(c.rx))+" capacity="+strconv.Itoa(cap(c.rx)))
		}
	}
	return nil
}

func (c *mobileVRouteCarrier) runRXWorker() {
	if c == nil || c.rx == nil || c.done == nil {
		return
	}
	for {
		select {
		case frame := <-c.rx:
			trackMobileVRouteInbound(frame.Data)
			if writeBack := c.currentWriteBack(); writeBack != nil {
				if err := writeBack(append([]byte(nil), frame.Data...)); err != nil {
					c.markError(err)
					androidLogStore.add("vpn", "warn", "vroute packet writeback failed: "+err.Error())
				} else {
					c.tunWriteFrames.Add(1)
					c.tunWriteBytes.Add(int64(len(frame.Data)))
				}
			}
		case <-c.done:
			return
		}
	}
}

func mobileVRouteFrameIsIP(frame mobileVRouteFrame) bool {
	return frame.MainType == mobileVRouteFrameMainTypeIP && frame.SubType == mobileVRouteIPSubTypeIPv4
}

func mobileVRouteFrameKind(frame mobileVRouteFrame) string {
	if mobileVRouteFrameIsIP(frame) {
		return "ip"
	}
	return "control"
}

func (c *mobileVRouteCarrier) setWriteBack(writeBack func([]byte) error) {
	if c == nil || writeBack == nil {
		return
	}
	c.writeBackMu.Lock()
	c.writeBack = writeBack
	c.writeBackMu.Unlock()
}

func (c *mobileVRouteCarrier) currentWriteBack() func([]byte) error {
	if c == nil {
		return nil
	}
	c.writeBackMu.RLock()
	defer c.writeBackMu.RUnlock()
	return c.writeBack
}

func startMobileVRouteCarrierWorkers(config mobileVRouteConfig) {
	for _, plan := range mobileVRouteOutboundCarrierPlans(config) {
		worker := &mobileVRouteCarrierWorker{
			plan:   plan,
			stopCh: make(chan struct{}),
			doneCh: make(chan struct{}),
		}
		key := mobileVRouteCarrierKey(plan)
		mobileVRouteCarrierState.mu.Lock()
		if existing := mobileVRouteCarrierState.workers[key]; existing != nil {
			mobileVRouteCarrierState.mu.Unlock()
			continue
		}
		mobileVRouteCarrierState.workers[key] = worker
		mobileVRouteCarrierState.mu.Unlock()
		go worker.run()
	}
}

func startMobileVRouteCarrierWorkersFromConfigDir(configDir string) {
	config, err := loadMobileVRouteConfig(configDir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			androidLogStore.add("route", "warning", "vroute carrier config load failed: "+err.Error())
		}
		return
	}
	startMobileVRouteCarrierWorkers(config)
}

func stopMobileVRouteCarrierWorkers() {
	mobileVRouteCarrierState.mu.Lock()
	workers := make([]*mobileVRouteCarrierWorker, 0, len(mobileVRouteCarrierState.workers))
	for _, worker := range mobileVRouteCarrierState.workers {
		workers = append(workers, worker)
	}
	mobileVRouteCarrierState.workers = map[string]*mobileVRouteCarrierWorker{}
	mobileVRouteCarrierState.mu.Unlock()
	for _, worker := range workers {
		worker.stop()
	}
}

func (w *mobileVRouteCarrierWorker) run() {
	if w == nil {
		return
	}
	defer close(w.doneCh)
	backoff := mobileVRouteCarrierRetryMin
	for {
		select {
		case <-w.stopCh:
			return
		default:
		}
		carrier, err := ensureMobileVRouteCarrier(w.plan, nil)
		if err != nil {
			recordMobileVRouteCarrierStateError(err)
			androidLogStore.add("route", "warning", "vroute carrier dial failed: route="+w.plan.RouteID+" relay="+net.JoinHostPort(w.plan.RelayHost, strconv.Itoa(w.plan.RelayPort))+" err="+err.Error())
			if !w.wait(backoff) {
				return
			}
			backoff = nextMobileVRouteCarrierRetry(backoff)
			continue
		}
		clearMobileVRouteCarrierStateError()
		backoff = mobileVRouteCarrierRetryMin
		if !w.waitCarrier(carrier) {
			if carrier != nil {
				carrier.close()
			}
			return
		}
	}
}

func (w *mobileVRouteCarrierWorker) wait(delay time.Duration) bool {
	if w == nil {
		return false
	}
	if delay <= 0 {
		delay = time.Second
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-w.stopCh:
		return false
	case <-timer.C:
		return true
	}
}

func (w *mobileVRouteCarrierWorker) waitCarrier(carrier *mobileVRouteCarrier) bool {
	if w == nil {
		return false
	}
	if carrier == nil || carrier.done == nil {
		return w.wait(mobileVRouteCarrierRetryMin)
	}
	select {
	case <-w.stopCh:
		return false
	case <-carrier.done:
		return true
	}
}

func (w *mobileVRouteCarrierWorker) stop() {
	if w == nil {
		return
	}
	w.stopOnce.Do(func() { close(w.stopCh) })
	if w.doneCh != nil {
		<-w.doneCh
	}
}

func nextMobileVRouteCarrierRetry(current time.Duration) time.Duration {
	if current <= 0 {
		return time.Second
	}
	next := current * 2
	if next > mobileVRouteCarrierRetryMax {
		return mobileVRouteCarrierRetryMax
	}
	return next
}

func recordMobileVRouteCarrierStateError(err error) {
	if err == nil {
		return
	}
	mobileVRouteCarrierState.mu.Lock()
	mobileVRouteCarrierState.lastError = strings.TrimSpace(err.Error())
	mobileVRouteCarrierState.lastErrorUnixNS = time.Now().UnixNano()
	mobileVRouteCarrierState.mu.Unlock()
}

func clearMobileVRouteCarrierStateError() {
	mobileVRouteCarrierState.mu.Lock()
	mobileVRouteCarrierState.lastError = ""
	mobileVRouteCarrierState.lastErrorUnixNS = 0
	mobileVRouteCarrierState.mu.Unlock()
}

func mobileVRouteOutboundCarrierPlans(config mobileVRouteConfig) []mobileVRouteForwardPlan {
	config = sanitizeMobileVRouteConfig(config)
	localNode := normalizeMobileRouteNodeID(config.LocalNodeID)
	if !config.Enabled || localNode == "" {
		return nil
	}
	plans := make([]mobileVRouteForwardPlan, 0, len(config.TopologyRules))
	for _, rule := range config.TopologyRules {
		if !rule.Enabled {
			continue
		}
		fromNode := normalizeMobileRouteNodeID(rule.FromNodeID)
		toNode := normalizeMobileRouteNodeID(rule.ToNodeID)
		reverse := toNode == localNode
		if fromNode != localNode && !reverse {
			continue
		}
		nextNode := toNode
		if reverse {
			nextNode = fromNode
		}
		host, port := mobileVRoutePeerEndpoint(config, rule, reverse)
		if nextNode == "" || host == "" || port <= 0 || strings.TrimSpace(rule.Secret) == "" || strings.TrimSpace(rule.AuthTicket) == "" {
			continue
		}
		bridgeRole := mobileVRouteBridgeRoleToNext
		if reverse {
			bridgeRole = mobileVRouteBridgeRoleToPrev
		}
		plans = append(plans, mobileVRouteForwardPlan{
			LocalNode:  localNode,
			ExitNode:   nextNode,
			Path:       []string{localNode, nextNode},
			NextNode:   nextNode,
			Config:     config,
			Rule:       rule,
			RouteID:    mobileVRouteRuntimeRouteID(rule),
			RelayHost:  host,
			RelayPort:  port,
			BridgeRole: bridgeRole,
			Layer:      normalizeMobileVRouteRelayLayer(rule.RouteLayer),
		})
	}
	sort.Slice(plans, func(i, j int) bool {
		return plans[i].RouteID < plans[j].RouteID
	})
	return plans
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
		if c.done != nil {
			close(c.done)
		}
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
		"links":        snapshotMobileVRouteRelayReports(configDir),
		"capabilities": mobileVRouteCapabilitiesPayload(),
	}
}

func mobileVRouteCapabilitiesPayload() map[string]any {
	return map[string]any{
		"ip_frame":           true,
		"ipv4":               true,
		"websocket_carrier":  true,
		"websocket_h3":       true,
		"outbound_dialer":    true,
		"inbound_listener":   false,
		"reverse_first_hop":  true,
		"relay_forwarding":   true,
		"control_ping":       true,
		"path_rtt":           true,
		"route_test":         false,
		"speed_test":         false,
		"debug_log_pull":     true,
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
		carrier := map[string]any{
			"route_id":            item.plan.RouteID,
			"path":                append([]string(nil), item.plan.Path...),
			"next_node":           item.plan.NextNode,
			"exit_node":           item.plan.ExitNode,
			"relay":               net.JoinHostPort(item.plan.RelayHost, strconv.Itoa(item.plan.RelayPort)),
			"bridge_role":         item.plan.BridgeRole,
			"layer":               item.plan.Layer,
			"tx_frames":           item.txFrames.Load(),
			"tx_bytes":            item.txBytes.Load(),
			"tx_ip_frames":        item.txIPFrames.Load(),
			"tx_ip_bytes":         item.txIPBytes.Load(),
			"tx_control_frames":   item.txControlFrames.Load(),
			"tx_queue":            len(item.tx) + len(item.txControl),
			"tx_capacity":         cap(item.tx) + cap(item.txControl),
			"tx_ip_queue":         len(item.tx),
			"tx_ip_capacity":      cap(item.tx),
			"tx_control_queue":    len(item.txControl),
			"tx_control_capacity": cap(item.txControl),
			"tx_last_write_ms":    time.Duration(item.txLastWriteNS.Load()).Milliseconds(),
			"tx_write_ema_ms":     time.Duration(item.txWriteEMANS.Load()).Milliseconds(),
			"rx_queue":            len(item.rx),
			"rx_capacity":         cap(item.rx),
			"rx_frames":           item.rxFrames.Load(),
			"rx_bytes":            item.rxBytes.Load(),
			"rx_ip_frames":        item.rxIPFrames.Load(),
			"rx_ip_bytes":         item.rxIPBytes.Load(),
			"rx_control_frames":   item.rxControlFrames.Load(),
			"tun_write_frames":    item.tunWriteFrames.Load(),
			"tun_write_bytes":     item.tunWriteBytes.Load(),
			"created_at":          mobileVRouteUnixNanoRFC3339(item.createdUnixNS),
			"last_activity_at":    mobileVRouteUnixNanoRFC3339(item.lastActivityNS.Load()),
			"last_error":          itemLastError,
			"last_error_at":       mobileVRouteUnixNanoRFC3339(itemLastErrorUnixNS),
		}
		carrier["tx_last_batch_frames"] = item.txBatchFrames.Load()
		carrier["tx_last_batch_bytes"] = item.txBatchBytes.Load()
		carriers = append(carriers, carrier)
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
		return dialMobileVRouteH3Carrier(plan)
	default:
		return nil, fmt.Errorf("unsupported vroute relay protocol: %s", strings.TrimSpace(plan.Layer))
	}
}

func dialMobileVRouteWebSocketCarrier(plan mobileVRouteForwardPlan) (net.Conn, error) {
	header := http.Header{}
	header.Set(mobileVRouteLegacyRouteIDHeader, strings.TrimSpace(plan.RouteID))
	header.Set(mobileVRouteCodexRouteIDHeader, strings.TrimSpace(plan.RouteID))
	header.Set(mobileVRouteCodexVersionHeader, mobileVRouteAuthPacketVersion)
	header.Set(mobileVRouteCodexRelayModeHeader, mobileVRouteRelayModeBridge)
	header.Set(mobileVRouteCodexRelayRoleHeader, plan.BridgeRole)
	if err := applyMobileVRouteSecretAuthHeaders(header, plan.RouteID, plan.Rule.Secret, plan.Rule.AuthTicket, plan.LocalNode, http.MethodGet, mobileVRouteRelayAPIPath, plan.BridgeRole); err != nil {
		return nil, err
	}
	candidates, err := mobileVRouteRelayDialCandidates(plan.RelayHost)
	if err != nil {
		return nil, err
	}
	var attempts []string
	for _, candidate := range candidates {
		conn, err := dialMobileVRouteWebSocketCarrierCandidate(plan, header, candidate)
		if err == nil {
			androidLogStore.add("vpn", "info", "vroute carrier connected: route="+plan.RouteID+" path="+strings.Join(plan.Path, ">")+" dial="+candidate.DialHost)
			return conn, nil
		}
		attempts = append(attempts, strings.TrimSpace(candidate.DialHost)+": "+err.Error())
		androidLogStore.add("vpn", "warn", "vroute carrier dial failed: route="+plan.RouteID+" dial="+candidate.DialHost+" err="+err.Error())
	}
	if len(attempts) == 0 {
		return nil, errors.New("vroute websocket dial failed: no relay candidate")
	}
	return nil, fmt.Errorf("vroute websocket dial failed: %s", strings.Join(attempts, "; "))
}

func dialMobileVRouteWebSocketCarrierCandidate(plan mobileVRouteForwardPlan, header http.Header, candidate mobileVRouteRelayDialCandidate) (net.Conn, error) {
	relayURL, err := mobileVRouteRelayWebSocketURL(candidate.URLHost, plan.RelayPort, plan.RouteID)
	if err != nil {
		return nil, err
	}
	dialHostPort := net.JoinHostPort(candidate.DialHost, strconv.Itoa(plan.RelayPort))
	dialer := websocket.Dialer{
		HandshakeTimeout:  mobileVRouteCarrierDialTimeout,
		Proxy:             nil,
		ReadBufferSize:    64 * 1024,
		WriteBufferSize:   64 * 1024,
		EnableCompression: false,
		NetDialContext: func(ctx context.Context, network string, addr string) (net.Conn, error) {
			dialNetwork := strings.TrimSpace(candidate.Network)
			if dialNetwork == "" {
				dialNetwork = network
			}
			netDialer := &net.Dialer{Timeout: mobileVRouteCarrierDialTimeout}
			return netDialer.DialContext(ctx, dialNetwork, dialHostPort)
		},
	}
	tlsConfig, err := newMobileVRouteRelayTLSConfig(plan, candidate, tls.VersionTLS12, nil)
	if err != nil {
		return nil, err
	}
	dialer.TLSClientConfig = tlsConfig
	ws, resp, err := dialer.Dial(relayURL, header)
	if err != nil {
		if resp != nil && resp.Body != nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
			_ = resp.Body.Close()
			return nil, fmt.Errorf("vroute websocket failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		return nil, err
	}
	return newWebSocketNetConn(ws), nil
}

func dialMobileVRouteH3Carrier(plan mobileVRouteForwardPlan) (net.Conn, error) {
	candidates, err := mobileVRouteRelayDialCandidates(plan.RelayHost)
	if err != nil {
		return nil, err
	}
	var attempts []string
	for _, candidate := range candidates {
		conn, err := dialMobileVRouteH3CarrierCandidate(plan, candidate)
		if err == nil {
			androidLogStore.add("vpn", "info", "vroute h3 carrier connected: route="+plan.RouteID+" path="+strings.Join(plan.Path, ">")+" dial="+candidate.DialHost)
			return conn, nil
		}
		attempts = append(attempts, strings.TrimSpace(candidate.DialHost)+": "+err.Error())
		androidLogStore.add("vpn", "warn", "vroute h3 carrier dial failed: route="+plan.RouteID+" dial="+candidate.DialHost+" err="+err.Error())
	}
	if len(attempts) == 0 {
		return nil, errors.New("vroute h3 dial failed: no relay candidate")
	}
	return nil, fmt.Errorf("vroute h3 dial failed: %s", strings.Join(attempts, "; "))
}

func dialMobileVRouteH3CarrierCandidate(plan mobileVRouteForwardPlan, candidate mobileVRouteRelayDialCandidate) (net.Conn, error) {
	relayURL, err := mobileVRouteRelayWebSocketURL(candidate.URLHost, plan.RelayPort, plan.RouteID)
	if err != nil {
		return nil, err
	}
	dialHostPort := net.JoinHostPort(candidate.DialHost, strconv.Itoa(plan.RelayPort))
	tlsConf, err := newMobileVRouteRelayTLSConfig(plan, candidate, tls.VersionTLS13, []string{http3.NextProtoH3})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), mobileVRouteCarrierDialTimeout)
	defer cancel()
	quicConn, err := mobileVRouteH3QUICDial(ctx, dialHostPort, tlsConf, mobileVRouteQUICConfig())
	if err != nil {
		return nil, err
	}
	clientConn := (&http3.Transport{}).NewClientConn(quicConn)
	select {
	case <-clientConn.ReceivedSettings():
	case <-ctx.Done():
		_ = quicConn.CloseWithError(0, "mobile vroute h3 settings timeout")
		return nil, fmt.Errorf("vroute h3 settings timeout: relay=%s", dialHostPort)
	case <-clientConn.Context().Done():
		_ = quicConn.CloseWithError(0, "mobile vroute h3 connection closed")
		return nil, fmt.Errorf("vroute h3 connection failed: %w", context.Cause(clientConn.Context()))
	}
	if settings := clientConn.Settings(); settings == nil || !settings.EnableExtendedConnect {
		_ = quicConn.CloseWithError(0, "mobile vroute h3 extended connect disabled")
		return nil, errors.New("vroute h3 failed: server did not enable extended connect")
	}
	stream, err := clientConn.OpenRequestStream(ctx)
	if err != nil {
		_ = quicConn.CloseWithError(0, "mobile vroute h3 stream open failed")
		return nil, err
	}
	_ = stream.SetDeadline(time.Now().Add(mobileVRouteH3StreamOpenTimeout))
	request, err := http.NewRequestWithContext(ctx, http.MethodConnect, relayURL, nil)
	if err != nil {
		stream.CancelRead(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
		stream.CancelWrite(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
		_ = quicConn.CloseWithError(0, "mobile vroute h3 request build failed")
		return nil, err
	}
	request.Proto = "websocket"
	request.ProtoMajor = 3
	request.ProtoMinor = 0
	request.Header.Set(mobileVRouteLegacyRouteIDHeader, strings.TrimSpace(plan.RouteID))
	request.Header.Set(mobileVRouteCodexRouteIDHeader, strings.TrimSpace(plan.RouteID))
	request.Header.Set(mobileVRouteCodexVersionHeader, mobileVRouteAuthPacketVersion)
	request.Header.Set(mobileVRouteCodexRelayModeHeader, mobileVRouteRelayModeBridge)
	request.Header.Set(mobileVRouteCodexRelayRoleHeader, plan.BridgeRole)
	if err := applyMobileVRouteSecretAuthHeaders(request.Header, plan.RouteID, plan.Rule.Secret, plan.Rule.AuthTicket, plan.LocalNode, http.MethodConnect, mobileVRouteRelayAPIPath, plan.BridgeRole); err != nil {
		stream.CancelRead(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
		stream.CancelWrite(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
		_ = quicConn.CloseWithError(0, "mobile vroute h3 auth failed")
		return nil, err
	}
	if strings.TrimSpace(candidate.URLHost) != "" {
		request.Host = strings.TrimSpace(candidate.URLHost)
	}
	if err := stream.SendRequestHeader(request); err != nil {
		stream.CancelRead(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
		stream.CancelWrite(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
		_ = quicConn.CloseWithError(0, "mobile vroute h3 header send failed")
		return nil, err
	}
	response, err := stream.ReadResponse()
	if err != nil {
		stream.CancelRead(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
		stream.CancelWrite(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
		_ = quicConn.CloseWithError(0, "mobile vroute h3 response failed")
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		_ = response.Body.Close()
		stream.CancelRead(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
		stream.CancelWrite(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
		_ = quicConn.CloseWithError(0, "mobile vroute h3 status failed")
		return nil, fmt.Errorf("vroute h3 failed: status=%d body=%s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	_ = stream.SetDeadline(time.Time{})
	cancelOnce := sync.Once{}
	return &mobileVRouteH3StreamNetConn{
		stream: stream,
		local:  mobileVRouteNetAddr{label: "mobile-vroute-h3-local"},
		remote: mobileVRouteNetAddr{label: dialHostPort},
		closeFn: func() error {
			var closeErr error
			cancelOnce.Do(func() {
				stream.CancelRead(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
				stream.CancelWrite(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
				closeErr = quicConn.CloseWithError(0, "mobile vroute h3 closed")
			})
			return closeErr
		},
	}, nil
}

func mobileVRouteQUICConfig() *quic.Config {
	return &quic.Config{
		InitialStreamReceiveWindow:     512 * 1024,
		MaxStreamReceiveWindow:         4 * 1024 * 1024,
		InitialConnectionReceiveWindow: 1024 * 1024,
		MaxConnectionReceiveWindow:     8 * 1024 * 1024,
		EnableDatagrams:                true,
	}
}

func applyMobileVRouteSecretAuthHeaders(headers http.Header, routeID string, secret string, authTicket string, sourceNodeID string, method string, requestPath string, relayRole string) error {
	cleanRouteID := strings.TrimSpace(routeID)
	cleanSecret := strings.TrimSpace(secret)
	if cleanRouteID == "" {
		return errors.New("route_id is required")
	}
	if cleanSecret == "" {
		return errors.New("route_secret is required")
	}
	cleanSourceNodeID := normalizeMobileRouteNodeID(sourceNodeID)
	if cleanSourceNodeID == "" {
		return errors.New("source_node_id is required")
	}
	nonce := randomHexToken(16)
	headers.Set("Authorization", "Bearer "+nonce)
	headers.Set(mobileVRouteCodexAuthModeHeader, "secret_hmac")
	headers.Set(mobileVRouteCodexSourceNodeHeader, cleanSourceNodeID)
	headers.Set(mobileVRouteCodexMACHeader, buildMobileVRouteHMAC(cleanSecret, cleanRouteID, nonce, method, requestPath, cleanSourceNodeID, relayRole))
	if strings.TrimSpace(authTicket) != "" {
		headers.Set(mobileVRouteCodexAuthTicketHeader, strings.TrimSpace(authTicket))
	}
	return nil
}

func buildMobileVRouteHMAC(secret string, routeID string, nonce string, method string, requestPath string, sourceNodeID string, relayRole string) string {
	h := hmac.New(sha256.New, []byte(strings.TrimSpace(secret)))
	canonical := strings.Join([]string{
		strings.TrimSpace(routeID),
		strings.TrimSpace(nonce),
		strings.ToUpper(strings.TrimSpace(method)),
		strings.TrimSpace(requestPath),
		normalizeMobileRouteNodeID(sourceNodeID),
		strings.ToLower(strings.TrimSpace(relayRole)),
	}, "\n")
	_, _ = h.Write([]byte(canonical))
	return hex.EncodeToString(h.Sum(nil))
}

func mobileVRouteRelayDialCandidates(host string) ([]mobileVRouteRelayDialCandidate, error) {
	cleanHost := strings.TrimSpace(strings.Trim(host, "[]"))
	if cleanHost == "" {
		return nil, errors.New("relay host is required")
	}
	if parsed := net.ParseIP(cleanHost); parsed != nil {
		return []mobileVRouteRelayDialCandidate{{
			URLHost:  parsed.String(),
			DialHost: parsed.String(),
			Network:  mobileVRouteTCPNetworkForIP(parsed),
		}}, nil
	}
	if mobileVRouteIsCloudflareCopilotDomain(cleanHost) {
		return []mobileVRouteRelayDialCandidate{{
			URLHost:  cleanHost,
			DialHost: cleanHost,
			Network:  "tcp",
		}}, nil
	}

	var candidates []mobileVRouteRelayDialCandidate
	seen := map[string]struct{}{}
	ips, err := lookupMobileVRouteRelayIPs(cleanHost)
	if err != nil {
		androidLogStore.add("vpn", "warn", "vroute relay resolve failed: host="+cleanHost+" err="+err.Error())
		return nil, fmt.Errorf("resolve vroute relay host failed: %w", err)
	}
	appendIPCandidates := func(wantIPv4 bool) {
		for _, ip := range ips {
			if ip == nil {
				continue
			}
			v4 := ip.To4()
			if wantIPv4 != (v4 != nil) {
				continue
			}
			dialIP := ip
			if v4 != nil {
				dialIP = v4
			} else if dialIP = ip.To16(); dialIP == nil {
				continue
			}
			dialHost := dialIP.String()
			key := strings.ToLower(dialHost)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			candidates = append(candidates, mobileVRouteRelayDialCandidate{
				URLHost:  dialHost,
				DialHost: dialHost,
				Network:  mobileVRouteTCPNetworkForIP(dialIP),
			})
		}
	}
	appendIPCandidates(true)
	appendIPCandidates(false)
	if len(candidates) == 0 {
		return nil, errors.New("resolve vroute relay host failed: no ip")
	}
	return candidates, nil
}

func normalizeMobileVRouteTLSSPKI(raw string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	if len(value) != sha256.Size*2 {
		return ""
	}
	if _, err := hex.DecodeString(value); err != nil {
		return ""
	}
	return value
}

func newMobileVRouteRelayTLSConfig(_ mobileVRouteForwardPlan, candidate mobileVRouteRelayDialCandidate, minVersion uint16, nextProtos []string) (*tls.Config, error) {
	host := strings.TrimSpace(strings.Trim(candidate.URLHost, "[]"))
	if host != "" && net.ParseIP(host) == nil && mobileVRouteIsCloudflareCopilotDomain(host) {
		return &tls.Config{MinVersion: minVersion, NextProtos: append([]string(nil), nextProtos...), ServerName: host}, nil
	}
	return &tls.Config{
		MinVersion:         minVersion,
		NextProtos:         append([]string(nil), nextProtos...),
		InsecureSkipVerify: true,
	}, nil
}

func lookupMobileVRouteRelayIPs(host string) ([]net.IP, error) {
	cleanHost := strings.TrimSpace(strings.Trim(host, "[]"))
	if cleanHost == "" {
		return nil, errors.New("relay host is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), mobileVRouteRelayResolveTimeout)
	defer cancel()

	results := make(chan mobileVRouteRelayLookupResult, 2)
	for _, network := range []string{"ip4", "ip6"} {
		network := network
		go func() {
			ips, err := mobileVRouteLookupIP(ctx, network, cleanHost)
			results <- mobileVRouteRelayLookupResult{Network: network, IPs: ips, Err: err}
		}()
	}

	var ipv4 []net.IP
	var ipv6 []net.IP
	var ipv4Err error
	var ipv6Err error
	for i := 0; i < 2; i++ {
		select {
		case result := <-results:
			switch result.Network {
			case "ip4":
				ipv4 = append(ipv4, result.IPs...)
				ipv4Err = result.Err
			case "ip6":
				ipv6 = append(ipv6, result.IPs...)
				ipv6Err = result.Err
			}
		case <-ctx.Done():
			if len(ipv4) > 0 || len(ipv6) > 0 {
				out := make([]net.IP, 0, len(ipv4)+len(ipv6))
				out = append(out, ipv4...)
				out = append(out, ipv6...)
				return out, nil
			}
			return nil, ctx.Err()
		}
	}

	if len(ipv4) > 0 || len(ipv6) > 0 {
		out := make([]net.IP, 0, len(ipv4)+len(ipv6))
		out = append(out, ipv4...)
		out = append(out, ipv6...)
		return out, nil
	}
	if ipv4Err != nil && ipv6Err != nil {
		return nil, fmt.Errorf("ipv4: %v; ipv6: %v", ipv4Err, ipv6Err)
	}
	if ipv4Err != nil {
		return nil, ipv4Err
	}
	if ipv6Err != nil {
		return nil, ipv6Err
	}
	return nil, errors.New("no ip")
}

func mobileVRouteTCPNetworkForIP(ip net.IP) string {
	if ip == nil {
		return "tcp"
	}
	if ip.To4() != nil {
		return "tcp4"
	}
	if ip.To16() != nil {
		return "tcp6"
	}
	return "tcp"
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
	cleanPath := mobileVRouteCleanPath(path)
	if err := validateMobileVRoutePath(cleanPath); err != nil {
		return mobileVRouteFrame{}, err
	}
	control, err := json.Marshal(mobileVRouteFrameControlEnvelope{Path: cleanPath})
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

func encodeMobileVRouteFrames(frames []mobileVRouteFrame) ([]byte, error) {
	if len(frames) == 0 {
		return nil, nil
	}
	totalBytes := 0
	encoded := make([][]byte, 0, len(frames))
	for _, frame := range frames {
		payload, err := encodeMobileVRouteFrame(frame)
		if err != nil {
			return nil, err
		}
		encoded = append(encoded, payload)
		totalBytes += len(payload)
	}
	payload := make([]byte, 0, totalBytes)
	for _, framePayload := range encoded {
		payload = append(payload, framePayload...)
	}
	return payload, nil
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
	var sum uint32
	var pending byte
	hasPending := false
	add := func(payload []byte) {
		for _, item := range payload {
			if hasPending {
				sum += uint32(pending)<<8 | uint32(item)
				sum = (sum & 0xffff) + (sum >> 16)
				hasPending = false
				continue
			}
			pending = item
			hasPending = true
		}
	}
	add(headerPrefix)
	add(control)
	add(data)
	if hasPending {
		sum += uint32(pending) << 8
	}
	for sum > 0xffff {
		sum = (sum & 0xffff) + (sum >> 16)
	}
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
	hops := map[string]int{from: 0}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if hops[current] >= mobileVRouteMaxHops {
			continue
		}
		for _, next := range neighbors[current] {
			if _, seen := prev[next]; seen {
				continue
			}
			prev[next] = current
			hops[next] = hops[current] + 1
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

func validateMobileVRoutePath(path []string) error {
	cleanPath := mobileVRouteCleanPath(path)
	if len(cleanPath) < 2 {
		return errors.New("vroute path is incomplete")
	}
	if len(cleanPath)-1 > mobileVRouteMaxHops {
		return fmt.Errorf("vroute path exceeds maximum hops: hops=%d max=%d", len(cleanPath)-1, mobileVRouteMaxHops)
	}
	seen := make(map[string]struct{}, len(cleanPath))
	for _, nodeID := range cleanPath {
		if _, exists := seen[nodeID]; exists {
			return fmt.Errorf("vroute path contains a loop at node=%s", nodeID)
		}
		seen[nodeID] = struct{}{}
	}
	return nil
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
