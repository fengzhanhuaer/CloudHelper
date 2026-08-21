package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	probeVirtualRouterDirectionForward    = "forward"
	probeVirtualRouterDefaultServicePort  = 12040
	probeVirtualRouterRecentPacketLimit   = 256
	probeVirtualRouterRecentPacketQueue   = 4096
	probeVirtualRouterRecentConnectionTTL = 5 * time.Minute
	probeVirtualRouterRecentPruneInterval = time.Second
	probeVirtualRouterRecentFlushTimeout  = 2 * time.Second
	probeVirtualRouterRecentDropLogPeriod = time.Second

	// vRouter frame header is a stable 12-byte wire protocol boundary:
	// magic 2 bytes, maintype 2 bytes, subtype 2 bytes,
	// control_len 2 bytes, data_len 2 bytes, checksum 2 bytes.
	// 未经用户明确许可，不得修改此帧定义、字段顺序、字段宽度或 checksum 范围。
	probeVirtualRouterFrameEnvelopeMagic                   uint16 = 0x5652
	probeVirtualRouterFrameEnvelopeHeaderSize                     = 12
	probeVirtualRouterFrameMaxControlBytes                        = 8096
	probeVirtualRouterFrameMaxDataBytes                           = 65535
	probeVirtualRouterFrameMaxBytes                               = probeVirtualRouterFrameEnvelopeHeaderSize + probeVirtualRouterFrameMaxControlBytes + probeVirtualRouterFrameMaxDataBytes
	probeVirtualRouterFrameReadBufferBytes                        = 256 * 1024
	probeVirtualRouterFrameMainTypeIP                      uint16 = 1
	probeVirtualRouterFrameMainTypePingPong                uint16 = 2
	probeVirtualRouterFrameMainTypePathRTT                 uint16 = 3
	probeVirtualRouterFrameMainTypeSpeed                   uint16 = 4
	probeVirtualRouterFrameMainTypeRouteTest               uint16 = 5
	probeVirtualRouterFrameMainTypeFakeIPVerify            uint16 = 6
	probeVirtualRouterFrameMainTypeDebugLog                uint16 = 7
	probeVirtualRouterFrameMainTypeProxy                   uint16 = 8
	probeVirtualRouterFrameSubTypeUnknown                  uint16 = 0
	probeVirtualRouterIPSubTypeIPv4                        uint16 = 1
	probeVirtualRouterPingPongSubTypePing                  uint16 = 1
	probeVirtualRouterPingPongSubTypePong                  uint16 = 2
	probeVirtualRouterPathRTTSubTypeQuery                  uint16 = 1
	probeVirtualRouterPathRTTSubTypeResp                   uint16 = 2
	probeVirtualRouterSpeedSubTypeStart                    uint16 = 1
	probeVirtualRouterSpeedSubTypeChunk                    uint16 = 2
	probeVirtualRouterSpeedSubTypeFinish                   uint16 = 3
	probeVirtualRouterSpeedSubTypeResult                   uint16 = 4
	probeVirtualRouterSpeedSubTypeSend                     uint16 = 5
	probeVirtualRouterRouteTestSubTypeProbe                uint16 = 1
	probeVirtualRouterRouteTestSubTypeReport               uint16 = 2
	probeVirtualRouterFakeIPVerifySubTypeQuery             uint16 = 1
	probeVirtualRouterFakeIPVerifySubTypeResponse          uint16 = 2
	probeVirtualRouterDebugLogSubTypeQuery                 uint16 = 1
	probeVirtualRouterDebugLogSubTypeResponse              uint16 = 2
	probeVirtualRouterProxySubTypeTCPOpen                  uint16 = 1
	probeVirtualRouterProxySubTypeTCPOpenResult            uint16 = 2
	probeVirtualRouterProxySubTypeTCPData                  uint16 = 3
	probeVirtualRouterProxySubTypeTCPClose                 uint16 = 4
	probeVirtualRouterProxySubTypeUDPRequest               uint16 = 5
	probeVirtualRouterProxySubTypeUDPResponse              uint16 = 6
	probeVirtualRouterProxySubTypeUDPClose                 uint16 = 7
	probeVirtualRouterFrameLinkIdleTTL                            = 45 * time.Second
	probeVirtualRouterPingPongInterval                            = 30 * time.Second
	probeVirtualRouterPingPongTimeout                             = 5 * time.Second
	probeVirtualRouterFrameWriteTimeout                           = 15 * time.Second
	probeVirtualRouterPingPongBytes                               = 64
	probeVirtualRouterSpeedTestMaxBytes                           = 128 * 1024 * 1024
	probeVirtualRouterSpeedTestMaxDuration                        = 10 * time.Second
	probeVirtualRouterSpeedTestChunkBytes                         = 1024
	probeVirtualRouterSpeedTestTXHighWatermarkPercent             = 75
	probeVirtualRouterSpeedTestTXLowWatermarkPercent              = 25
	probeVirtualRouterSpeedTestTXHighWatermarkBytes               = 192 * 1024
	probeVirtualRouterSpeedTestTXLowWatermarkBytes                = 64 * 1024
	probeVirtualRouterSpeedReceiveCompletedTTL                    = 2 * time.Minute
	probeVirtualRouterCarrierStalePingFailures                    = 4
	probeVirtualRouterCarrierStaleRXGrace                         = 2 * probeVirtualRouterPingPongInterval
	probeVirtualRouterPathRTTFailureThreshold                     = 5
	probeVirtualRouterPathRecoveryMaxCandidates                   = 64
	probeVirtualRouterPathRecoveryMaxHops                         = 3
	probeVirtualRouterNonDirectPathGuardInterval                  = time.Minute
	probeVirtualRouterDiagnosticLogPeriod                         = 5 * time.Minute
	probeVirtualRouterRouteConfigRefreshHotPathMinInterval        = 60 * time.Second
	probeVirtualRouterProbeIPPoolSize                             = 1024
	probeVirtualRouterFakeIPMemoryTTL                             = 48 * time.Hour
)

var probeVirtualRouterEnsureDirectBypass = ensureProbeRouteDirectBypass

var probeVirtualRouterState = struct {
	mu                sync.RWMutex
	config            probeVirtualRouterConfig
	localNodeID       string
	localIP           string
	nodeToIP          map[string]string
	ipToNode          map[string]string
	neighbors         map[string]map[string]struct{}
	rulesByID         map[string]probeVirtualRouterTopologyRule
	topologySignature string
}{}

var probeVirtualRouterControllerState = struct {
	mu                sync.RWMutex
	identity          nodeIdentity
	controllerBaseURL string
}{}

var probeVirtualRouterRouteConfigRefreshState = struct {
	mu      sync.Mutex
	running map[string]bool
	lastAt  map[string]time.Time
}{
	running: make(map[string]bool),
	lastAt:  make(map[string]time.Time),
}

var probeVirtualRouterFakeIPItemRefreshState = struct {
	mu      sync.Mutex
	running map[string]bool
	lastAt  map[string]time.Time
}{
	running: make(map[string]bool),
	lastAt:  make(map[string]time.Time),
}

type probeVirtualRouterTopologyIndex struct {
	nodeToIP  map[string]string
	ipToNode  map[string]string
	neighbors map[string]map[string]struct{}
	rulesByID map[string]probeVirtualRouterTopologyRule
}

var probeVirtualRouterRouteCacheState = struct {
	mu     sync.RWMutex
	routes map[string][]string
}{routes: make(map[string][]string)}

var probeVirtualRouterNonDirectPathGuardState = struct {
	mu          sync.Mutex
	stopCh      chan struct{}
	failedPaths map[string]struct{}
}{failedPaths: make(map[string]struct{})}

var probeVirtualRouterDisconnectedCarrierState = struct {
	mu       sync.RWMutex
	routeIDs map[string]struct{}
}{routeIDs: make(map[string]struct{})}

var probeVirtualRouterPathRTTState = struct {
	mu    sync.RWMutex
	items map[string]probeVirtualRouterPathRTTRecord
}{items: make(map[string]probeVirtualRouterPathRTTRecord)}

var probeVirtualRouterPathRecoveryState = struct {
	mu       sync.Mutex
	inflight map[string]struct{}
}{inflight: make(map[string]struct{})}

var errProbeVirtualRouterAdjacentRTTUnavailable = errors.New("adjacent virtual router ping-pong latency is unavailable")

var probeVirtualRouterRuntimeStatsState = struct {
	mu    sync.Mutex
	items map[string]*probeVirtualRouterRuntimeStats
}{items: make(map[string]*probeVirtualRouterRuntimeStats)}

var probeVirtualRouterRecentPacketState = struct {
	mu         sync.Mutex
	nextID     uint64
	writeIndex int
	items      []probeVirtualRouterRecentPacket
}{}

var probeVirtualRouterRecentPacketRecorder = struct {
	once          sync.Once
	queue         chan probeVirtualRouterRecentPacketEvent
	mu            sync.Mutex
	dropped       uint64
	droppedTotal  uint64
	lastDropLogAt time.Time
}{}

var probeVirtualRouterRecentConnectionState = struct {
	mu          sync.Mutex
	items       map[string]probeVirtualRouterRecentConnection
	lastPruneAt time.Time
}{items: make(map[string]probeVirtualRouterRecentConnection)}

var probeVirtualRouterICMPPingState = struct {
	mu      sync.Mutex
	pending map[string]probeVirtualRouterICMPPingPending
}{pending: make(map[string]probeVirtualRouterICMPPingPending)}

var probeVirtualRouterControlResponseState = struct {
	mu      sync.Mutex
	pending map[string]chan probeVirtualRouterControlProbePayload
}{pending: make(map[string]chan probeVirtualRouterControlProbePayload)}

var probeVirtualRouterSpeedReceiveState = struct {
	mu        sync.Mutex
	sessions  map[string]*probeVirtualRouterSpeedReceiveSession
	completed map[string]time.Time
}{sessions: make(map[string]*probeVirtualRouterSpeedReceiveSession), completed: make(map[string]time.Time)}

var probeVirtualRouterSpeedResponseState = struct {
	mu      sync.Mutex
	pending map[string]chan probeVirtualRouterSpeedTestResultPayload
}{pending: make(map[string]chan probeVirtualRouterSpeedTestResultPayload)}

var probeVirtualRouterLocalInterfaceRetryState = struct {
	mu         sync.Mutex
	running    bool
	generation uint64
	ensuredIP  string
	ensuredAt  time.Time
	cancel     chan struct{}
	done       chan struct{}
}{}

var probeVirtualRouterLocalInterfaceRetryDelays = []time.Duration{
	1 * time.Second,
	2 * time.Second,
	5 * time.Second,
	10 * time.Second,
	20 * time.Second,
	30 * time.Second,
}

var probeVirtualRouterLocalInterfaceEnsureState = struct {
	mu      sync.Mutex
	running bool
	done    chan struct{}
}{}

type probeVirtualRouterRuntimeStats struct {
	PacketsForwarded            int64   `json:"packets_forwarded,omitempty"`
	BytesForwarded              int64   `json:"bytes_forwarded,omitempty"`
	PacketsReceived             int64   `json:"packets_received,omitempty"`
	BytesReceived               int64   `json:"bytes_received,omitempty"`
	PacketsDelivered            int64   `json:"packets_delivered,omitempty"`
	BytesDelivered              int64   `json:"bytes_delivered,omitempty"`
	FramesSent                  int64   `json:"frames_sent,omitempty"`
	FrameBytesSent              int64   `json:"frame_bytes_sent,omitempty"`
	FramesReceived              int64   `json:"frames_received,omitempty"`
	FrameBytesReceived          int64   `json:"frame_bytes_received,omitempty"`
	LinkOpenCount               int64   `json:"link_open_count,omitempty"`
	LastOpenLatencyMS           int64   `json:"last_open_latency_ms,omitempty"`
	LastOpenError               string  `json:"last_open_error,omitempty"`
	LastOpenAt                  string  `json:"last_open_at,omitempty"`
	PingCount                   int64   `json:"ping_count,omitempty"`
	LastPingLatencyMS           int64   `json:"last_ping_latency_ms,omitempty"`
	LastPingError               string  `json:"last_ping_error,omitempty"`
	LastPingAt                  string  `json:"last_ping_at,omitempty"`
	LastPingFailureCount        int     `json:"last_ping_failure_count,omitempty"`
	LastPingDirection           string  `json:"last_ping_direction,omitempty"`
	LastPingBridgeConnections   int     `json:"last_ping_bridge_connections,omitempty"`
	LastPingBridgeSessionID     string  `json:"last_ping_bridge_session_id,omitempty"`
	LastPingBridgeRemote        string  `json:"last_ping_bridge_remote,omitempty"`
	LastPingBridgeConnectedAt   string  `json:"last_ping_bridge_connected_at,omitempty"`
	VirtualPingCount            int64   `json:"virtual_ping_count,omitempty"`
	LastVirtualPingLatencyMS    int64   `json:"last_virtual_ping_latency_ms,omitempty"`
	LastVirtualPingAt           string  `json:"last_virtual_ping_at,omitempty"`
	LastVirtualPingSourceIP     string  `json:"last_virtual_ping_source_ip,omitempty"`
	LastVirtualPingDestIP       string  `json:"last_virtual_ping_dest_ip,omitempty"`
	LastVirtualPingID           uint16  `json:"last_virtual_ping_id,omitempty"`
	LastVirtualPingSequence     uint16  `json:"last_virtual_ping_sequence,omitempty"`
	LastVirtualPingPath         string  `json:"last_virtual_ping_path,omitempty"`
	LastRemoteRTTMS             int64   `json:"last_remote_rtt_ms,omitempty"`
	LastRemoteRTTAt             string  `json:"last_remote_rtt_at,omitempty"`
	LastRemoteRTTError          string  `json:"last_remote_rtt_error,omitempty"`
	LastRemoteRTTResponder      string  `json:"last_remote_rtt_responder,omitempty"`
	LastRemotePongsReceived     int64   `json:"last_remote_pongs_received,omitempty"`
	LastSpeedTestAt             string  `json:"last_speed_test_at,omitempty"`
	LastSpeedTestSourceNodeID   string  `json:"last_speed_test_source_node_id,omitempty"`
	LastSpeedTestTargetNodeID   string  `json:"last_speed_test_target_node_id,omitempty"`
	LastSpeedTestPath           string  `json:"last_speed_test_path,omitempty"`
	LastSpeedTestError          string  `json:"last_speed_test_error,omitempty"`
	LastSpeedTestUpBytes        int64   `json:"last_speed_test_up_bytes,omitempty"`
	LastSpeedTestUpFrames       int64   `json:"last_speed_test_up_frames,omitempty"`
	LastSpeedTestUpDurationMS   int64   `json:"last_speed_test_up_duration_ms,omitempty"`
	LastSpeedTestUpMbps         float64 `json:"last_speed_test_up_mbps,omitempty"`
	LastSpeedTestDownBytes      int64   `json:"last_speed_test_down_bytes,omitempty"`
	LastSpeedTestDownFrames     int64   `json:"last_speed_test_down_frames,omitempty"`
	LastSpeedTestDownDurationMS int64   `json:"last_speed_test_down_duration_ms,omitempty"`
	LastSpeedTestDownMbps       float64 `json:"last_speed_test_down_mbps,omitempty"`
	LastPacketAt                string  `json:"last_packet_at,omitempty"`
	LastFrameAt                 string  `json:"last_frame_at,omitempty"`
	LastFrameSourceIP           string  `json:"last_frame_source_ip,omitempty"`
	LastFrameDestinationIP      string  `json:"last_frame_destination_ip,omitempty"`
	LastFrameLocalIP            string  `json:"last_frame_local_ip,omitempty"`
	LastFrameLocalMatch         string  `json:"last_frame_local_match,omitempty"`
	LastFramePath               string  `json:"last_frame_path,omitempty"`
	LastFrameRuntimeNodeID      string  `json:"last_frame_runtime_node_id,omitempty"`
	LastDeliveryError           string  `json:"last_delivery_error,omitempty"`
	TUNDataPlane                bool    `json:"tun_data_plane,omitempty"`
	TUNRXPackets                uint64  `json:"tun_rx_packets,omitempty"`
	TUNRXBytes                  uint64  `json:"tun_rx_bytes,omitempty"`
	TUNTXPackets                uint64  `json:"tun_tx_packets,omitempty"`
	TUNTXBytes                  uint64  `json:"tun_tx_bytes,omitempty"`
}

type probeVirtualRouterRecentPacket struct {
	ID              uint64   `json:"id"`
	CapturedAt      string   `json:"captured_at"`
	Source          string   `json:"source"`
	Action          string   `json:"action"`
	RouteID         string   `json:"route_id,omitempty"`
	LocalNodeID     string   `json:"local_node_id,omitempty"`
	PeerNodeID      string   `json:"peer_node_id,omitempty"`
	Protocol        string   `json:"protocol,omitempty"`
	SourceIP        string   `json:"source_ip,omitempty"`
	DestinationIP   string   `json:"destination_ip,omitempty"`
	SourcePort      uint16   `json:"source_port,omitempty"`
	DestinationPort uint16   `json:"destination_port,omitempty"`
	TCPFlags        string   `json:"tcp_flags,omitempty"`
	Length          int      `json:"length"`
	Path            []string `json:"path,omitempty"`
	PathText        string   `json:"path_text,omitempty"`
	LocalMatch      bool     `json:"local_match,omitempty"`
	FakeIP          bool     `json:"fake_ip,omitempty"`
	FakeIPSide      string   `json:"fake_ip_side,omitempty"`
	FakeIPDomain    string   `json:"fake_ip_domain,omitempty"`
	FakeIPExitNode  string   `json:"fake_ip_exit_node,omitempty"`
	Detail          string   `json:"detail,omitempty"`
	Error           string   `json:"error,omitempty"`
}

type probeVirtualRouterRecentPacketEvent struct {
	source     string
	action     string
	routeID    string
	localNode  string
	peerNode   string
	packet     []byte
	path       []string
	localMatch bool
	errorText  string
	barrier    chan struct{}
}

type probeVirtualRouterRecentConnection struct {
	Kind            string   `json:"kind,omitempty"`
	TrafficType     string   `json:"traffic_type,omitempty"`
	FirstSeen       string   `json:"first_seen"`
	LastSeen        string   `json:"last_seen"`
	Protocol        string   `json:"protocol,omitempty"`
	Domain          string   `json:"domain,omitempty"`
	EndpointA       string   `json:"endpoint_a"`
	EndpointB       string   `json:"endpoint_b"`
	EndpointADomain string   `json:"endpoint_a_domain,omitempty"`
	EndpointBDomain string   `json:"endpoint_b_domain,omitempty"`
	RouteID         string   `json:"route_id,omitempty"`
	LocalNodeID     string   `json:"local_node_id,omitempty"`
	PeerNodeID      string   `json:"peer_node_id,omitempty"`
	PathText        string   `json:"path_text,omitempty"`
	Events          int      `json:"events"`
	Bytes           int64    `json:"bytes"`
	TUNEvents       int      `json:"tun_events"`
	FrameEvents     int      `json:"frame_events"`
	Forwarded       int      `json:"forwarded"`
	Delivered       int      `json:"delivered"`
	Dropped         int      `json:"dropped"`
	Errors          int      `json:"errors"`
	DNSQueries      int      `json:"dns_queries"`
	Connected       bool     `json:"connected,omitempty"`
	Status          string   `json:"status"`
	LastSource      string   `json:"last_source,omitempty"`
	LastAction      string   `json:"last_action,omitempty"`
	LastTCPFlags    string   `json:"last_tcp_flags,omitempty"`
	LastDetail      string   `json:"last_detail,omitempty"`
	LastError       string   `json:"last_error,omitempty"`
	FakeIPDomain    string   `json:"fake_ip_domain,omitempty"`
	FakeIPExitNode  string   `json:"fake_ip_exit_node,omitempty"`
	ResolvedIPs     []string `json:"resolved_ips,omitempty"`

	closed bool
	lastAt time.Time
}

type probeVirtualRouterICMPEchoLogInfo struct {
	Kind          string
	SourceIP      string
	DestinationIP string
	ID            uint16
	Sequence      uint16
}

type probeVirtualRouterICMPPingPending struct {
	StartedAt     time.Time
	RouteID       string
	SourceIP      string
	DestinationIP string
	ID            uint16
	Sequence      uint16
	Path          string
}

type probeVirtualRouterICMPPingCompleteSummary struct {
	SourceIP      string
	DestinationIP string
	ID            uint16
	Sequence      uint16
	Path          string
	LatencyMS     int64
}

type probeVirtualRouterSpeedReceiveSession struct {
	RequestID     string
	Direction     string
	SourceNodeID  string
	TargetNodeID  string
	ResultNodeID  string
	Path          []string
	RouteID       string
	MaxDurationMS int64
	LocalNodeID   string
	TimerStarted  bool
	StartedAt     time.Time
	LastAt        time.Time
	Bytes         int64
	Frames        int64
}

type probeVirtualRouterTransportLogInfo struct {
	Protocol        string
	SourceIP        string
	DestinationIP   string
	SourcePort      uint16
	DestinationPort uint16
	TCPFlags        string
}

// probeVirtualRouterFrame is the vRouter wire frame. Its binary header layout
// is fixed by the constants above; control_len and data_len are 2 bytes.
// 未经用户明确许可不得修改。
type probeVirtualRouterFrame struct {
	MainType uint16
	SubType  uint16
	Control  []byte
	Data     []byte
}

type probeVirtualRouterFrameControlEnvelope struct {
	Path  []string                          `json:"path,omitempty"`
	Trace []probeVirtualRouterFrameTraceHop `json:"trace,omitempty"`
}

type probeVirtualRouterFrameTraceHop struct {
	ID         string `json:"id"`
	NodeID     string `json:"node_id"`
	RouteID    string `json:"route_id,omitempty"`
	Event      string `json:"event"`
	Direction  string `json:"direction,omitempty"`
	RemoteNode string `json:"remote_node,omitempty"`
	UnixNano   int64  `json:"unix_nano"`
}

type probeVirtualRouterPathRTTRecord struct {
	RTTMS                   int64
	LastAt                  time.Time
	LastError               string
	ConsecutiveFailureCount int
	TargetNode              string
	Responder               string
}

type probeVirtualRouterPathRefreshResult struct {
	Queried          int
	Explored         int
	RecoveredTargets int
}

type probeVirtualRouterPathRTTQueryRequest struct {
	RequestID string `json:"request_id,omitempty"`
}

type probeVirtualRouterPathRTTQueryResponse struct {
	OK        bool   `json:"ok"`
	LatencyMS int64  `json:"latency_ms,omitempty"`
	Error     string `json:"error,omitempty"`
	Responder string `json:"responder,omitempty"`
}

type probeVirtualRouterControlProbePayload struct {
	RequestID         string   `json:"request_id"`
	SourceNodeID      string   `json:"source_node_id,omitempty"`
	TargetNodeID      string   `json:"target_node_id,omitempty"`
	Path              []string `json:"path,omitempty"`
	CreatedAtUnixNano int64    `json:"created_at_unix_nano,omitempty"`
	LatencyMS         int64    `json:"latency_ms,omitempty"`
	PingBytes         int      `json:"ping_bytes,omitempty"`
	OK                bool     `json:"ok,omitempty"`
	Error             string   `json:"error,omitempty"`
	Responder         string   `json:"responder,omitempty"`
}

type probeVirtualRouterSpeedTestResultPayload struct {
	RequestID         string   `json:"request_id"`
	Direction         string   `json:"direction,omitempty"`
	SourceNodeID      string   `json:"source_node_id,omitempty"`
	TargetNodeID      string   `json:"target_node_id,omitempty"`
	ResultNodeID      string   `json:"result_node_id,omitempty"`
	Path              []string `json:"path,omitempty"`
	MaxBytes          int64    `json:"max_bytes,omitempty"`
	MaxDurationMS     int64    `json:"max_duration_ms,omitempty"`
	CreatedAtUnixNano int64    `json:"created_at_unix_nano,omitempty"`
	Bytes             int64    `json:"bytes,omitempty"`
	Frames            int64    `json:"frames,omitempty"`
	DurationMS        int64    `json:"duration_ms,omitempty"`
	Mbps              float64  `json:"mbps,omitempty"`
	OK                bool     `json:"ok,omitempty"`
	Error             string   `json:"error,omitempty"`
	Responder         string   `json:"responder,omitempty"`
	RuntimeRouteID    string   `json:"-"`
}

type probeVirtualRouterSpeedTestResult struct {
	OK         bool
	Error      string
	Bytes      int64
	Frames     int64
	DurationMS int64
	Mbps       float64
}

func sanitizeProbeVirtualRouterConfigForCache(input probeVirtualRouterConfig) probeVirtualRouterConfig {
	out := probeVirtualRouterConfig{
		Enabled:       input.Enabled,
		FakeIPCIDR:    strings.TrimSpace(input.FakeIPCIDR),
		ProbeIPs:      sanitizeProbeVirtualRouterProbeIPs(input.ProbeIPs),
		TopologyRules: sanitizeProbeVirtualRouterTopologyRules(input.TopologyRules),
		RouteRules:    sanitizeProbeVirtualRouterRouteRules(input.RouteRules),
		FakeIPLibrary: sanitizeProbeVirtualRouterFakeIPLibrary(input.FakeIPLibrary),
		UpdatedAt:     strings.TrimSpace(input.UpdatedAt),
	}
	return out
}

func sanitizeProbeVirtualRouterFakeIPLibrary(input probeVirtualRouterFakeIPLibrary) probeVirtualRouterFakeIPLibrary {
	version := input.Version
	if version < 0 {
		version = 0
	}
	out := probeVirtualRouterFakeIPLibrary{
		Version:   version,
		UpdatedAt: strings.TrimSpace(input.UpdatedAt),
		Items:     []probeVirtualRouterFakeIPEntry{},
	}
	seenDomain := map[string]struct{}{}
	seenIP := map[string]struct{}{}
	for _, item := range input.Items {
		domain := normalizeProbeVirtualRouterDomain(item.Domain)
		ip := strings.TrimSpace(item.FakeIP)
		if domain == "" || net.ParseIP(ip).To4() == nil {
			continue
		}
		if _, ok := seenDomain[domain]; ok {
			continue
		}
		if _, ok := seenIP[ip]; ok {
			continue
		}
		seenDomain[domain] = struct{}{}
		seenIP[ip] = struct{}{}
		out.Items = append(out.Items, probeVirtualRouterFakeIPEntry{
			Domain:     domain,
			FakeIP:     ip,
			RuleID:     strings.TrimSpace(item.RuleID),
			Action:     sanitizeProbeVirtualRouterRouteRuleAction(item.Action, item.ExitNodeID),
			ExitNodeID: normalizeProbeRouteNodeID(item.ExitNodeID),
			ExpiresAt:  strings.TrimSpace(item.ExpiresAt),
			UpdatedAt:  strings.TrimSpace(item.UpdatedAt),
		})
	}
	sort.SliceStable(out.Items, func(i, j int) bool {
		return out.Items[i].Domain < out.Items[j].Domain
	})
	return out
}

func normalizeProbeVirtualRouterDomain(raw string) string {
	domain := strings.ToLower(strings.TrimSpace(strings.Trim(raw, ".")))
	if domain == "" || strings.ContainsAny(domain, " \t\r\n:/") {
		return ""
	}
	return domain
}

func sanitizeProbeVirtualRouterProbeIPs(items []probeVirtualRouterProbeIP) []probeVirtualRouterProbeIP {
	if len(items) == 0 {
		return []probeVirtualRouterProbeIP{}
	}
	out := make([]probeVirtualRouterProbeIP, 0, len(items))
	seenNode := map[string]struct{}{}
	seenIP := map[string]struct{}{}
	for _, item := range items {
		nodeID := normalizeProbeRouteNodeID(item.NodeID)
		ip := net.ParseIP(strings.TrimSpace(item.IP)).To4()
		if nodeID == "" || ip == nil {
			continue
		}
		ipText := ip.String()
		if _, exists := seenNode[nodeID]; exists {
			continue
		}
		if _, exists := seenIP[ipText]; exists {
			continue
		}
		seenNode[nodeID] = struct{}{}
		seenIP[ipText] = struct{}{}
		out = append(out, probeVirtualRouterProbeIP{
			NodeID:      nodeID,
			DisplayName: strings.TrimSpace(item.DisplayName),
			IP:          ipText,
			ServicePort: normalizeProbeVirtualRouterServicePort(item.ServicePort),
			Note:        strings.TrimSpace(item.Note),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].NodeID < out[j].NodeID
	})
	return out
}

func sanitizeProbeVirtualRouterTopologyRules(items []probeVirtualRouterTopologyRule) []probeVirtualRouterTopologyRule {
	if len(items) == 0 {
		return []probeVirtualRouterTopologyRule{}
	}
	out := make([]probeVirtualRouterTopologyRule, 0, len(items))
	seen := map[string]struct{}{}
	reserved := collectProbeVirtualRouterReservedRuleIDs(items)
	nextRuleSeq := 1
	for _, item := range items {
		fromNodeID := normalizeProbeRouteNodeID(item.FromNodeID)
		toNodeID := normalizeProbeRouteNodeID(item.ToNodeID)
		direction := normalizeProbeVirtualRouterDirection(item.Direction)
		if fromNodeID == "" || toNodeID == "" || fromNodeID == toNodeID {
			continue
		}
		fromServiceDomain := strings.TrimSpace(item.FromServiceDomain)
		fromServicePort := sanitizeProbeVirtualRouterOptionalServicePort(item.FromServicePort)
		toServiceDomain := strings.TrimSpace(item.ToServiceDomain)
		toServicePort := sanitizeProbeVirtualRouterOptionalServicePort(item.ToServicePort)
		ruleID := strings.TrimSpace(item.ID)
		if ruleID == "" {
			ruleID, nextRuleSeq = allocateProbeVirtualRouterRuleID(seen, reserved, nextRuleSeq)
		}
		key := ruleID
		if key == "" {
			key = fmt.Sprintf("%s|%s|%s", fromNodeID, toNodeID, strings.ToLower(toServiceDomain))
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, probeVirtualRouterTopologyRule{
			ID:                ruleID,
			Name:              strings.TrimSpace(item.Name),
			FromNodeID:        fromNodeID,
			ToNodeID:          toNodeID,
			Direction:         direction,
			FromServiceDomain: fromServiceDomain,
			FromServicePort:   fromServicePort,
			FromTLSSPKISHA256: normalizeProbeRouteTLSSPKI(item.FromTLSSPKISHA256),
			ToServiceDomain:   toServiceDomain,
			ToServicePort:     toServicePort,
			ToTLSSPKISHA256:   normalizeProbeRouteTLSSPKI(item.ToTLSSPKISHA256),
			RouteLayer:        normalizeProbeRouteRouteLayer(item.RouteLayer),
			UserID:            strings.TrimSpace(item.UserID),
			UserPublicKey:     strings.TrimSpace(item.UserPublicKey),
			Secret:            strings.TrimSpace(item.Secret),
			AuthTicket:        strings.TrimSpace(item.AuthTicket),
			Enabled:           item.Enabled,
			Note:              strings.TrimSpace(item.Note),
			UpdatedAt:         strings.TrimSpace(item.UpdatedAt),
		})
	}
	return out
}

func sanitizeProbeVirtualRouterRouteRules(items []probeVirtualRouterRouteRule) []probeVirtualRouterRouteRule {
	if len(items) == 0 {
		return []probeVirtualRouterRouteRule{}
	}
	out := make([]probeVirtualRouterRouteRule, 0, len(items))
	seen := map[string]struct{}{}
	for _, item := range items {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		ruleID := strings.TrimSpace(item.ID)
		key := firstNonEmpty(ruleID, strings.ToLower(name))
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		entries := make([]string, 0, len(item.Entries))
		entrySeen := map[string]struct{}{}
		for _, raw := range item.Entries {
			entry := strings.TrimSpace(raw)
			if entry == "" {
				continue
			}
			if _, exists := entrySeen[entry]; exists {
				continue
			}
			entrySeen[entry] = struct{}{}
			entries = append(entries, entry)
		}
		sort.Strings(entries)
		action := sanitizeProbeVirtualRouterRouteRuleAction(item.Action, item.ExitNodeID)
		exitNodeID := ""
		if action == "probe_exit" {
			exitNodeID = normalizeProbeRouteNodeID(item.ExitNodeID)
		}
		out = append(out, probeVirtualRouterRouteRule{
			ID:         ruleID,
			Name:       name,
			Action:     action,
			ExitNodeID: exitNodeID,
			Entries:    entries,
			Note:       strings.TrimSpace(item.Note),
			UpdatedAt:  strings.TrimSpace(item.UpdatedAt),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		left := strings.ToLower(firstNonEmpty(out[i].Name, out[i].ID))
		right := strings.ToLower(firstNonEmpty(out[j].Name, out[j].ID))
		if left != right {
			return left < right
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func sanitizeProbeVirtualRouterRouteRuleAction(raw string, exitNodeID string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	switch value {
	case "", "direct":
		if normalizeProbeRouteNodeID(exitNodeID) != "" && value == "" {
			return "probe_exit"
		}
		return "direct"
	case "probe_exit", "exit", "probe":
		return "probe_exit"
	case "reject", "block", "deny":
		return "reject"
	default:
		return "direct"
	}
}

func collectProbeVirtualRouterReservedRuleIDs(items []probeVirtualRouterTopologyRule) map[string]struct{} {
	reserved := map[string]struct{}{}
	for _, item := range items {
		ruleID := strings.TrimSpace(item.ID)
		if ruleID != "" {
			reserved[ruleID] = struct{}{}
		}
	}
	return reserved
}

func allocateProbeVirtualRouterRuleID(seen map[string]struct{}, reserved map[string]struct{}, nextSeq int) (string, int) {
	if nextSeq <= 0 {
		nextSeq = 1
	}
	for {
		ruleID := fmt.Sprintf("vr-%d", nextSeq)
		nextSeq++
		if _, exists := seen[ruleID]; !exists {
			if _, exists := reserved[ruleID]; !exists {
				return ruleID, nextSeq
			}
		}
	}
}

func normalizeProbeVirtualRouterServicePort(port int) int {
	if port <= 0 || port > 65535 {
		return probeVirtualRouterDefaultServicePort
	}
	return port
}

func sanitizeProbeVirtualRouterOptionalServicePort(port int) int {
	if port < 0 || port > 65535 {
		return 0
	}
	return port
}

func normalizeProbeVirtualRouterDirection(raw string) string {
	return probeVirtualRouterDirectionForward
}

func persistProbeRouteConfigCache(config probeVirtualRouterConfig) error {
	cachePath, err := resolveProbeRouteConfigCachePath()
	if err != nil {
		return err
	}
	cacheItem := sanitizeProbeVirtualRouterConfigForCache(config)
	cacheItem.FakeIPLibrary = probeVirtualRouterFakeIPLibrary{}
	payload := probeRouteConfigCacheFile{
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Item:      cacheItem,
	}
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(cachePath, append(encoded, '\n'), 0o600); err != nil {
		return err
	}
	return os.Chmod(cachePath, 0o600)
}

func loadProbeRouteConfigCache() (probeVirtualRouterConfig, error) {
	cachePath, err := resolveProbeRouteConfigCachePath()
	if err != nil {
		return probeVirtualRouterConfig{}, err
	}
	raw, err := os.ReadFile(cachePath)
	if err != nil {
		if os.IsNotExist(err) {
			return probeVirtualRouterConfig{}, nil
		}
		return probeVirtualRouterConfig{}, err
	}
	if strings.TrimSpace(string(raw)) == "" {
		return probeVirtualRouterConfig{}, nil
	}
	var payload probeRouteConfigCacheFile
	if err := json.Unmarshal(raw, &payload); err != nil {
		return probeVirtualRouterConfig{}, err
	}
	config := sanitizeProbeVirtualRouterConfigForCache(payload.Item)
	config.FakeIPLibrary = probeVirtualRouterFakeIPLibrary{}
	rememberProbeVirtualRouterAuthTickets(config)
	return config, nil
}

func resolveProbeRouteConfigCachePath() (string, error) {
	dataPath, err := resolveDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataPath, probeRouteConfigCacheFileName), nil
}

func applyProbeVirtualRouterConfig(config probeVirtualRouterConfig) {
	applyProbeVirtualRouterConfigForNode(config, "")
}

func applyProbeVirtualRouterConfigForNode(config probeVirtualRouterConfig, nodeID string) {
	sanitized := sanitizeProbeVirtualRouterConfigForCache(config)
	index := buildProbeVirtualRouterTopologyIndex(sanitized)
	now := time.Now().UTC()
	fakeIPRouteChanged := false
	probeVirtualRouterState.mu.Lock()
	if sanitized.FakeIPLibrary.Version <= 0 && len(sanitized.FakeIPLibrary.Items) == 0 {
		sanitized.FakeIPLibrary = sanitizeProbeVirtualRouterFakeIPLibrary(probeVirtualRouterState.config.FakeIPLibrary)
	}
	sanitized.FakeIPLibrary = probeVirtualRouterFakeIPLibraryWithMemoryTTL(sanitized.FakeIPLibrary, now)
	sanitized.FakeIPLibrary, fakeIPRouteChanged = reconcileProbeVirtualRouterFakeIPLibraryWithRouteRules(sanitized.FakeIPLibrary, sanitized.RouteRules, now)
	effectiveNodeID := strings.TrimSpace(probeVirtualRouterState.localNodeID)
	if cleanNodeID := normalizeProbeRouteNodeID(nodeID); cleanNodeID != "" {
		effectiveNodeID = cleanNodeID
	}
	signature := probeVirtualRouterTopologySignature(sanitized, index, effectiveNodeID)
	topologyChanged := probeVirtualRouterState.topologySignature != signature
	probeVirtualRouterState.config = sanitized
	probeVirtualRouterState.localNodeID = effectiveNodeID
	probeVirtualRouterState.nodeToIP = index.nodeToIP
	probeVirtualRouterState.ipToNode = index.ipToNode
	probeVirtualRouterState.neighbors = index.neighbors
	probeVirtualRouterState.rulesByID = index.rulesByID
	probeVirtualRouterState.localIP = index.nodeToIP[effectiveNodeID]
	probeVirtualRouterState.topologySignature = signature
	ensureLocalInterface := activeProbeProductProfile.EnableVRoutePlatformInterface && sanitized.Enabled && strings.TrimSpace(probeVirtualRouterState.localIP) != ""
	probeVirtualRouterState.mu.Unlock()
	if topologyChanged {
		clearProbeVirtualRouterRouteCache("config updated")
	} else if fakeIPRouteChanged {
		clearProbeVirtualRouterRouteCache("fake ip route rules updated")
	}
	setProbeVirtualRouterNonDirectPathGuardEnabled(sanitized.Enabled)
	if sanitized.Enabled {
		cleanupProbeRouteDirectBypassForVirtualRouterRules(sanitized)
	}
	if activeProbeProductProfile.EnableVRoutePlatformInterface {
		if ensureLocalInterface {
			scheduleProbeVirtualRouterLocalInterfaceIPEnsure("config_updated")
		} else if err := cleanupProbeVirtualRouterPlatformRoutes(); err != nil {
			log.Printf("warning: cleanup probe virtual router platform routes failed: %v", err)
		}
	}
}

func buildProbeVirtualRouterTopologyIndex(config probeVirtualRouterConfig) probeVirtualRouterTopologyIndex {
	index := probeVirtualRouterTopologyIndex{
		nodeToIP:  make(map[string]string),
		ipToNode:  make(map[string]string),
		neighbors: make(map[string]map[string]struct{}),
		rulesByID: make(map[string]probeVirtualRouterTopologyRule),
	}
	for _, item := range config.ProbeIPs {
		nodeID := normalizeProbeRouteNodeID(item.NodeID)
		ip := net.ParseIP(strings.TrimSpace(item.IP)).To4()
		if nodeID == "" || ip == nil {
			continue
		}
		ipText := ip.String()
		index.nodeToIP[nodeID] = ipText
		index.ipToNode[ipText] = nodeID
	}
	addNeighbor := func(a string, b string) {
		a = normalizeProbeRouteNodeID(a)
		b = normalizeProbeRouteNodeID(b)
		if a == "" || b == "" {
			return
		}
		if index.neighbors[a] == nil {
			index.neighbors[a] = map[string]struct{}{}
		}
		index.neighbors[a][b] = struct{}{}
	}
	for _, rule := range config.TopologyRules {
		if !rule.Enabled {
			continue
		}
		if ruleID := strings.TrimSpace(rule.ID); ruleID != "" {
			index.rulesByID[ruleID] = rule
		}
		addNeighbor(rule.FromNodeID, rule.ToNodeID)
		addNeighbor(rule.ToNodeID, rule.FromNodeID)
	}
	return index
}

func probeVirtualRouterTopologySignature(config probeVirtualRouterConfig, index probeVirtualRouterTopologyIndex, localNodeID string) string {
	var b strings.Builder
	localNodeID = normalizeProbeRouteNodeID(localNodeID)
	fmt.Fprintf(&b, "enabled=%t|cidr=%s|local=%s|local_ip=%s\n", config.Enabled, strings.TrimSpace(config.FakeIPCIDR), localNodeID, strings.TrimSpace(index.nodeToIP[localNodeID]))
	nodeIDs := make([]string, 0, len(index.nodeToIP))
	for nodeID := range index.nodeToIP {
		nodeIDs = append(nodeIDs, nodeID)
	}
	sort.Strings(nodeIDs)
	for _, nodeID := range nodeIDs {
		fmt.Fprintf(&b, "ip|%s|%s|%d\n", nodeID, strings.TrimSpace(index.nodeToIP[nodeID]), probeVirtualRouterServicePortForNode(config, nodeID, probeVirtualRouterDefaultServicePort))
	}
	neighborIDs := make([]string, 0, len(index.neighbors))
	for nodeID := range index.neighbors {
		neighborIDs = append(neighborIDs, nodeID)
	}
	sort.Strings(neighborIDs)
	for _, nodeID := range neighborIDs {
		peers := make([]string, 0, len(index.neighbors[nodeID]))
		for peerID := range index.neighbors[nodeID] {
			peers = append(peers, peerID)
		}
		sort.Strings(peers)
		fmt.Fprintf(&b, "adj|%s|%s\n", nodeID, strings.Join(peers, ","))
	}
	ruleIDs := make([]string, 0, len(index.rulesByID))
	for ruleID := range index.rulesByID {
		ruleIDs = append(ruleIDs, ruleID)
	}
	sort.Strings(ruleIDs)
	for _, ruleID := range ruleIDs {
		rule := index.rulesByID[ruleID]
		fmt.Fprintf(&b, "rule|%s|%s|%s|%s|%s|%d|%s|%d\n",
			strings.TrimSpace(rule.ID),
			normalizeProbeRouteNodeID(rule.FromNodeID),
			normalizeProbeRouteNodeID(rule.ToNodeID),
			normalizeProbeVirtualRouterDirection(rule.Direction),
			strings.TrimSpace(rule.FromServiceDomain),
			sanitizeProbeVirtualRouterOptionalServicePort(rule.FromServicePort),
			strings.TrimSpace(rule.ToServiceDomain),
			sanitizeProbeVirtualRouterOptionalServicePort(rule.ToServicePort),
		)
	}
	for _, rule := range config.RouteRules {
		fmt.Fprintf(&b, "route_rule|%s|%s|%s|%s\n",
			strings.TrimSpace(rule.ID),
			strings.TrimSpace(rule.Name),
			strings.TrimSpace(rule.Action),
			normalizeProbeRouteNodeID(rule.ExitNodeID),
		)
		for _, entry := range rule.Entries {
			fmt.Fprintf(&b, "route_rule_entry|%s|%s\n", strings.TrimSpace(rule.ID), strings.TrimSpace(entry))
		}
	}
	return b.String()
}

func reconcileProbeVirtualRouterFakeIPLibraryWithRouteRules(library probeVirtualRouterFakeIPLibrary, rules []probeVirtualRouterRouteRule, now time.Time) (probeVirtualRouterFakeIPLibrary, bool) {
	library = sanitizeProbeVirtualRouterFakeIPLibrary(library)
	if len(library.Items) == 0 {
		return library, false
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	updatedAt := now.Format(time.RFC3339)
	kept := library.Items[:0]
	changed := false
	for _, item := range library.Items {
		domain := normalizeProbeVirtualRouterDomain(item.Domain)
		rule, ok := probeVirtualRouterRouteRuleForFakeIPDomain(rules, domain)
		if !ok {
			kept = append(kept, item)
			continue
		}
		if sanitizeProbeVirtualRouterRouteRuleAction(rule.Action, rule.ExitNodeID) != "probe_exit" {
			changed = true
			continue
		}
		exitNodeID := normalizeProbeRouteNodeID(rule.ExitNodeID)
		if exitNodeID == "" {
			changed = true
			continue
		}
		next := item
		next.Domain = domain
		next.RuleID = strings.TrimSpace(rule.ID)
		next.Action = "probe_exit"
		next.ExitNodeID = exitNodeID
		if next.Domain != item.Domain ||
			next.RuleID != strings.TrimSpace(item.RuleID) ||
			sanitizeProbeVirtualRouterRouteRuleAction(item.Action, item.ExitNodeID) != next.Action ||
			normalizeProbeRouteNodeID(item.ExitNodeID) != next.ExitNodeID {
			next.UpdatedAt = updatedAt
			changed = true
		}
		kept = append(kept, next)
	}
	if !changed {
		return library, false
	}
	library.Items = kept
	if library.Version <= 0 {
		library.Version = 1
	}
	library.Version++
	library.UpdatedAt = updatedAt
	return library, true
}

func probeVirtualRouterRouteRuleForFakeIPDomain(rules []probeVirtualRouterRouteRule, domain string) (probeVirtualRouterRouteRule, bool) {
	cleanDomain := normalizeProbeVirtualRouterDomain(domain)
	if cleanDomain == "" {
		return probeVirtualRouterRouteRule{}, false
	}
	for _, rule := range rules {
		action := sanitizeProbeVirtualRouterRouteRuleAction(rule.Action, rule.ExitNodeID)
		if action == "" {
			continue
		}
		for _, entry := range rule.Entries {
			if probeVirtualRouterRouteRuleEntryMatchesDomain(cleanDomain, entry) {
				rule.Action = action
				rule.ExitNodeID = normalizeProbeRouteNodeID(rule.ExitNodeID)
				return rule, true
			}
		}
	}
	return probeVirtualRouterRouteRule{}, false
}

func currentProbeVirtualRouterConfig() probeVirtualRouterConfig {
	probeVirtualRouterState.mu.RLock()
	defer probeVirtualRouterState.mu.RUnlock()
	return sanitizeProbeVirtualRouterConfigForCache(probeVirtualRouterState.config)
}

func probeVirtualRouterServicePortForNode(config probeVirtualRouterConfig, nodeID string, fallback int) int {
	nodeID = normalizeProbeRouteNodeID(nodeID)
	if nodeID != "" {
		for _, item := range config.ProbeIPs {
			if normalizeProbeRouteNodeID(item.NodeID) == nodeID {
				return normalizeProbeVirtualRouterServicePort(item.ServicePort)
			}
		}
	}
	return normalizeProbeVirtualRouterServicePort(fallback)
}

func currentProbeVirtualRouterFakeIPCIDR() string {
	config := currentProbeVirtualRouterConfig()
	if cidr := strings.TrimSpace(config.FakeIPCIDR); cidr != "" {
		return cidr
	}
	return probeLocalFakeIPDefaultCIDR
}

func currentProbeVirtualRouterFakeIPLibrary() probeVirtualRouterFakeIPLibrary {
	config := currentProbeVirtualRouterConfig()
	return sanitizeProbeVirtualRouterFakeIPLibrary(config.FakeIPLibrary)
}

func currentProbeVirtualRouterFakeIPEntryByDomain(domain string) (probeVirtualRouterFakeIPEntry, bool) {
	cleanDomain := normalizeProbeVirtualRouterDomain(domain)
	if cleanDomain == "" {
		return probeVirtualRouterFakeIPEntry{}, false
	}
	now := time.Now().UTC()
	probeVirtualRouterState.mu.RLock()
	defer probeVirtualRouterState.mu.RUnlock()
	for _, item := range probeVirtualRouterState.config.FakeIPLibrary.Items {
		if item.Domain != cleanDomain {
			continue
		}
		if probeVirtualRouterFakeIPEntryExpired(item, now) {
			return probeVirtualRouterFakeIPEntry{}, false
		}
		return item, true
	}
	return probeVirtualRouterFakeIPEntry{}, false
}

func currentProbeVirtualRouterRouteRuleForDomain(domain string) (probeVirtualRouterRouteRule, bool) {
	cleanDomain := normalizeProbeVirtualRouterDomain(domain)
	if cleanDomain == "" {
		return probeVirtualRouterRouteRule{}, false
	}
	config := currentProbeVirtualRouterConfig()
	for _, rule := range config.RouteRules {
		action := sanitizeProbeVirtualRouterRouteRuleAction(rule.Action, rule.ExitNodeID)
		if action == "" {
			continue
		}
		for _, entry := range rule.Entries {
			if probeVirtualRouterRouteRuleEntryMatchesDomain(cleanDomain, entry) {
				rule.Action = action
				rule.ExitNodeID = normalizeProbeRouteNodeID(rule.ExitNodeID)
				return rule, true
			}
		}
	}
	return probeVirtualRouterRouteRule{}, false
}

func currentProbeVirtualRouterRouteRuleForIP(ipText string) (probeVirtualRouterRouteRule, bool) {
	target := net.ParseIP(strings.TrimSpace(ipText)).To4()
	if target == nil {
		return probeVirtualRouterRouteRule{}, false
	}
	config := currentProbeVirtualRouterConfig()
	for _, rule := range config.RouteRules {
		action := sanitizeProbeVirtualRouterRouteRuleAction(rule.Action, rule.ExitNodeID)
		if action == "" {
			continue
		}
		for _, entry := range rule.Entries {
			if probeVirtualRouterRouteRuleEntryMatchesIP(target, entry) {
				rule.Action = action
				rule.ExitNodeID = normalizeProbeRouteNodeID(rule.ExitNodeID)
				return rule, true
			}
		}
	}
	return probeVirtualRouterRouteRule{}, false
}

func resolveProbeVirtualRouterFakeIPForDNS(domain string, rule probeVirtualRouterRouteRule) (probeVirtualRouterFakeIPEntry, error) {
	cleanDomain := normalizeProbeVirtualRouterDomain(domain)
	if cleanDomain == "" {
		return probeVirtualRouterFakeIPEntry{}, errors.New("virtual router dns domain is empty")
	}
	if item, exists := currentProbeVirtualRouterFakeIPEntryByDomain(cleanDomain); exists {
		return item, nil
	}
	identity, controllerBaseURL, ok := currentProbeVirtualRouterController()
	if ok {
		ctx, cancel := context.WithTimeout(context.Background(), probeRouteConfigSyncFetchTimeout)
		item, _, err := probeRequestRouteFakeIP(ctx, controllerBaseURL, identity, cleanDomain, rule)
		cancel()
		if err != nil {
			return probeVirtualRouterFakeIPEntry{}, err
		}
		if !applyProbeVirtualRouterFakeIPEntry(item) {
			return probeVirtualRouterFakeIPEntry{}, errors.New("virtual router fake ip response is invalid")
		}
		if item, exists := currentProbeVirtualRouterFakeIPEntryByDomain(cleanDomain); exists {
			return item, nil
		}
		return probeVirtualRouterFakeIPEntry{}, errors.New("virtual router fake ip response was not cached")
	}
	return probeVirtualRouterFakeIPEntry{}, errors.New("virtual router controller is unavailable")
}

func probeVirtualRouterRouteRuleEntryMatchesDomain(domain string, entry string) bool {
	key, value, ok := strings.Cut(strings.TrimSpace(entry), ":")
	if !ok {
		return false
	}
	key = strings.ToLower(strings.TrimSpace(key))
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return false
	}
	switch key {
	case "domain_suffix":
		return domain == value || strings.HasSuffix(domain, "."+value)
	case "domain_prefix":
		return strings.HasPrefix(domain, value)
	case "domain_keyword":
		return strings.Contains(domain, value)
	default:
		return false
	}
}

func probeVirtualRouterRouteRuleEntryMatchesIP(ip net.IP, entry string) bool {
	target := ip.To4()
	if target == nil {
		return false
	}
	key, value, ok := strings.Cut(strings.TrimSpace(entry), ":")
	if !ok {
		return false
	}
	key = strings.ToLower(strings.TrimSpace(key))
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	switch key {
	case "cidr", "ip_cidr", "ip-cidr":
		_, network, err := net.ParseCIDR(value)
		return err == nil && network != nil && network.Contains(target)
	case "ip":
		return target.Equal(net.ParseIP(value).To4())
	default:
		return false
	}
}

func currentProbeVirtualRouterFakeIPEntryByIP(ip string) (probeVirtualRouterFakeIPEntry, bool) {
	item, ok := currentProbeVirtualRouterStoredFakeIPEntryByIP(ip)
	if !ok {
		return probeVirtualRouterFakeIPEntry{}, false
	}
	return effectiveProbeVirtualRouterFakeIPEntryForCurrentRules(item)
}

func currentProbeVirtualRouterStoredFakeIPEntryByIP(ip string) (probeVirtualRouterFakeIPEntry, bool) {
	target := net.ParseIP(strings.TrimSpace(ip)).To4()
	if target == nil {
		return probeVirtualRouterFakeIPEntry{}, false
	}
	now := time.Now().UTC()
	probeVirtualRouterState.mu.RLock()
	defer probeVirtualRouterState.mu.RUnlock()
	return probeVirtualRouterFakeIPEntryByIPLocked(target.String(), now)
}

func probeVirtualRouterFakeIPEntryByIPLocked(ip string, now time.Time) (probeVirtualRouterFakeIPEntry, bool) {
	target := net.ParseIP(strings.TrimSpace(ip)).To4()
	if target == nil {
		return probeVirtualRouterFakeIPEntry{}, false
	}
	targetText := target.String()
	for _, item := range probeVirtualRouterState.config.FakeIPLibrary.Items {
		if strings.TrimSpace(item.FakeIP) != targetText {
			continue
		}
		if probeVirtualRouterFakeIPEntryExpired(item, now) {
			return probeVirtualRouterFakeIPEntry{}, false
		}
		return item, true
	}
	return probeVirtualRouterFakeIPEntry{}, false
}

func effectiveProbeVirtualRouterFakeIPEntryForCurrentRules(item probeVirtualRouterFakeIPEntry) (probeVirtualRouterFakeIPEntry, bool) {
	item = sanitizeProbeVirtualRouterFakeIPEntry(item)
	if strings.TrimSpace(item.Domain) == "" || strings.TrimSpace(item.FakeIP) == "" {
		return probeVirtualRouterFakeIPEntry{}, false
	}
	rule, ok := currentProbeVirtualRouterRouteRuleForDomain(item.Domain)
	if !ok {
		return item, true
	}
	action := sanitizeProbeVirtualRouterRouteRuleAction(rule.Action, rule.ExitNodeID)
	if action != "probe_exit" {
		return probeVirtualRouterFakeIPEntry{}, false
	}
	exitNodeID := normalizeProbeRouteNodeID(rule.ExitNodeID)
	if exitNodeID == "" {
		return probeVirtualRouterFakeIPEntry{}, false
	}
	item.RuleID = strings.TrimSpace(rule.ID)
	item.Action = "probe_exit"
	item.ExitNodeID = exitNodeID
	return item, true
}

func probeVirtualRouterFakeIPEntryExpired(item probeVirtualRouterFakeIPEntry, now time.Time) bool {
	expiresAt, err := time.Parse(time.RFC3339, strings.TrimSpace(item.ExpiresAt))
	return err == nil && !expiresAt.IsZero() && !now.Before(expiresAt)
}

func recordProbeVirtualRouterRecentPacket(source string, action string, runtime *probeVirtualRouterRuntime, packet []byte, path []string, localMatch bool, err error) {
	queue := ensureProbeVirtualRouterRecentPacketRecorder()
	if len(queue) >= cap(queue) {
		recordProbeVirtualRouterRecentPacketMonitorDrop(len(queue), cap(queue))
		return
	}
	event := probeVirtualRouterRecentPacketEvent{
		source:     strings.TrimSpace(source),
		action:     strings.TrimSpace(action),
		packet:     append([]byte(nil), packet...),
		path:       append([]string(nil), path...),
		localMatch: localMatch,
	}
	if runtime != nil {
		event.routeID = probeVirtualRouterRuntimeLogRouteID(runtime)
		event.localNode = currentProbeVirtualRouterLocalNodeIDForRuntime(runtime)
		event.peerNode = normalizeProbeRouteNodeID(runtime.cfg.peerNodeID)
	}
	if event.localNode == "" {
		event.localNode = currentProbeVirtualRouterLocalNodeID()
	}
	if err != nil {
		event.errorText = strings.TrimSpace(err.Error())
	}
	select {
	case queue <- event:
	default:
		recordProbeVirtualRouterRecentPacketMonitorDrop(len(queue), cap(queue))
	}
}

func ensureProbeVirtualRouterRecentPacketRecorder() chan probeVirtualRouterRecentPacketEvent {
	probeVirtualRouterRecentPacketRecorder.once.Do(func() {
		probeVirtualRouterRecentPacketRecorder.queue = make(chan probeVirtualRouterRecentPacketEvent, probeVirtualRouterRecentPacketQueue)
		go runProbeVirtualRouterRecentPacketRecorder(probeVirtualRouterRecentPacketRecorder.queue)
	})
	return probeVirtualRouterRecentPacketRecorder.queue
}

func runProbeVirtualRouterRecentPacketRecorder(queue <-chan probeVirtualRouterRecentPacketEvent) {
	for event := range queue {
		if event.barrier != nil {
			close(event.barrier)
			continue
		}
		storeProbeVirtualRouterRecentPacket(event)
	}
}

func storeProbeVirtualRouterRecentPacket(event probeVirtualRouterRecentPacketEvent) {
	var eventErr error
	if event.errorText != "" {
		eventErr = errors.New(event.errorText)
	}
	item := buildProbeVirtualRouterRecentPacket(event.source, event.action, nil, event.packet, event.path, event.localMatch, eventErr)
	item.RouteID = event.routeID
	item.LocalNodeID = event.localNode
	item.PeerNodeID = event.peerNode
	if strings.TrimSpace(item.SourceIP) == "" && strings.TrimSpace(item.DestinationIP) == "" {
		return
	}
	probeVirtualRouterRecentPacketState.mu.Lock()
	probeVirtualRouterRecentPacketState.nextID++
	item.ID = probeVirtualRouterRecentPacketState.nextID
	item.CapturedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if len(probeVirtualRouterRecentPacketState.items) < probeVirtualRouterRecentPacketLimit {
		probeVirtualRouterRecentPacketState.items = append(probeVirtualRouterRecentPacketState.items, item)
	} else {
		probeVirtualRouterRecentPacketState.items[probeVirtualRouterRecentPacketState.writeIndex] = item
		probeVirtualRouterRecentPacketState.writeIndex = (probeVirtualRouterRecentPacketState.writeIndex + 1) % probeVirtualRouterRecentPacketLimit
	}
	probeVirtualRouterRecentPacketState.mu.Unlock()
	recordProbeVirtualRouterRecentConnection(item)
}

func recordProbeVirtualRouterRecentPacketMonitorDrop(depth int, capacity int) {
	now := time.Now()
	shouldLog := false
	dropped := uint64(0)
	probeVirtualRouterRecentPacketRecorder.mu.Lock()
	probeVirtualRouterRecentPacketRecorder.dropped++
	probeVirtualRouterRecentPacketRecorder.droppedTotal++
	if probeVirtualRouterRecentPacketRecorder.lastDropLogAt.IsZero() || now.Sub(probeVirtualRouterRecentPacketRecorder.lastDropLogAt) >= probeVirtualRouterRecentDropLogPeriod {
		dropped = probeVirtualRouterRecentPacketRecorder.dropped
		probeVirtualRouterRecentPacketRecorder.dropped = 0
		probeVirtualRouterRecentPacketRecorder.lastDropLogAt = now
		shouldLog = true
	}
	probeVirtualRouterRecentPacketRecorder.mu.Unlock()
	if shouldLog {
		log.Printf("probe virtual router recent packet monitor queue full: dropped=%d depth=%d capacity=%d", dropped, depth, capacity)
	}
}

func probeVirtualRouterRecentPacketMonitorDroppedTotal() uint64 {
	probeVirtualRouterRecentPacketRecorder.mu.Lock()
	dropped := probeVirtualRouterRecentPacketRecorder.droppedTotal
	probeVirtualRouterRecentPacketRecorder.mu.Unlock()
	return dropped
}

func flushProbeVirtualRouterRecentPacketEvents() {
	queue := ensureProbeVirtualRouterRecentPacketRecorder()
	done := make(chan struct{})
	timer := time.NewTimer(probeVirtualRouterRecentFlushTimeout)
	defer timer.Stop()
	select {
	case queue <- probeVirtualRouterRecentPacketEvent{barrier: done}:
	case <-timer.C:
		return
	}
	select {
	case <-done:
	case <-timer.C:
	}
}

func buildProbeVirtualRouterRecentPacket(source string, action string, runtime *probeVirtualRouterRuntime, packet []byte, path []string, localMatch bool, err error) probeVirtualRouterRecentPacket {
	cleanPath := cleanProbeVirtualRouterPath(path)
	item := probeVirtualRouterRecentPacket{
		Source:     strings.TrimSpace(source),
		Action:     strings.TrimSpace(action),
		RouteID:    probeVirtualRouterRuntimeLogRouteID(runtime),
		LocalMatch: localMatch,
		Length:     len(packet),
		Path:       cleanPath,
		PathText:   strings.Join(cleanPath, ">"),
	}
	if runtime != nil {
		item.LocalNodeID = currentProbeVirtualRouterLocalNodeIDForRuntime(runtime)
		item.PeerNodeID = normalizeProbeRouteNodeID(runtime.cfg.peerNodeID)
	}
	if item.LocalNodeID == "" {
		item.LocalNodeID = currentProbeVirtualRouterLocalNodeID()
	}
	if err != nil {
		item.Error = strings.TrimSpace(err.Error())
	}
	if info, ok := probeVirtualRouterParseTCPUDPLogInfo(packet); ok {
		item.Protocol = strings.ToUpper(strings.TrimSpace(info.Protocol))
		item.SourceIP = info.SourceIP
		item.DestinationIP = info.DestinationIP
		item.SourcePort = info.SourcePort
		item.DestinationPort = info.DestinationPort
		item.TCPFlags = info.TCPFlags
		item.Detail = probeVirtualRouterPacketChecksumSummary(packet)
	} else if info, ok := probeVirtualRouterParseICMPEchoLogInfo(packet); ok {
		item.Protocol = "ICMP"
		item.SourceIP = info.SourceIP
		item.DestinationIP = info.DestinationIP
		item.Detail = fmt.Sprintf("%s id=%d seq=%d", strings.TrimSpace(info.Kind), info.ID, info.Sequence)
	} else {
		item.Protocol = probeVirtualRouterIPv4ProtocolName(packet)
		item.SourceIP = probeVirtualRouterIPv4Source(packet)
		item.DestinationIP = probeVirtualRouterIPv4Destination(packet)
	}
	applyProbeVirtualRouterRecentPacketFakeIP(&item, item.DestinationIP, "dst")
	if !item.FakeIP {
		applyProbeVirtualRouterRecentPacketFakeIP(&item, item.SourceIP, "src")
	}
	return item
}

func applyProbeVirtualRouterRecentPacketFakeIP(item *probeVirtualRouterRecentPacket, ip string, side string) {
	if item == nil {
		return
	}
	entry, ok := currentProbeVirtualRouterFakeIPEntryByIP(ip)
	if !ok {
		return
	}
	item.FakeIP = true
	item.FakeIPSide = strings.TrimSpace(side)
	item.FakeIPDomain = strings.TrimSpace(entry.Domain)
	item.FakeIPExitNode = normalizeProbeRouteNodeID(entry.ExitNodeID)
}

func probeVirtualRouterIPv4ProtocolName(packet []byte) string {
	if len(packet) < 20 || packet[0]>>4 != 4 {
		return ""
	}
	switch packet[9] {
	case 1:
		return "ICMP"
	case 6:
		return "TCP"
	case 17:
		return "UDP"
	default:
		return fmt.Sprintf("IP-%d", packet[9])
	}
}

func snapshotProbeVirtualRouterRecentPackets() []probeVirtualRouterRecentPacket {
	flushProbeVirtualRouterRecentPacketEvents()
	probeVirtualRouterRecentPacketState.mu.Lock()
	out := make([]probeVirtualRouterRecentPacket, 0, len(probeVirtualRouterRecentPacketState.items))
	if len(probeVirtualRouterRecentPacketState.items) < probeVirtualRouterRecentPacketLimit || probeVirtualRouterRecentPacketState.writeIndex == 0 {
		out = append(out, probeVirtualRouterRecentPacketState.items...)
	} else {
		out = append(out, probeVirtualRouterRecentPacketState.items[probeVirtualRouterRecentPacketState.writeIndex:]...)
		out = append(out, probeVirtualRouterRecentPacketState.items[:probeVirtualRouterRecentPacketState.writeIndex]...)
	}
	probeVirtualRouterRecentPacketState.mu.Unlock()
	for i := 0; i < len(out)/2; i++ {
		j := len(out) - 1 - i
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func snapshotProbeVirtualRouterRecentConnections() []probeVirtualRouterRecentConnection {
	flushProbeVirtualRouterRecentPacketEvents()
	now := time.Now()
	probeVirtualRouterRecentConnectionState.mu.Lock()
	pruneProbeVirtualRouterRecentConnectionsLocked(now)
	probeVirtualRouterRecentConnectionState.lastPruneAt = now
	connections := make([]probeVirtualRouterRecentConnection, 0, len(probeVirtualRouterRecentConnectionState.items))
	for _, item := range probeVirtualRouterRecentConnectionState.items {
		connections = append(connections, finalizeProbeVirtualRouterRecentConnection(item))
	}
	probeVirtualRouterRecentConnectionState.mu.Unlock()
	sort.Slice(connections, func(i, j int) bool {
		return connections[i].lastAt.After(connections[j].lastAt)
	})
	return connections
}

func recordProbeVirtualRouterRecentConnection(packet probeVirtualRouterRecentPacket) {
	if strings.TrimSpace(packet.Source) == "fake_verify" || probeVirtualRouterRecentPacketIsProbeP2P(packet) {
		return
	}
	key, endpointA, endpointB := probeVirtualRouterRecentConnectionKey(packet)
	if key == "" {
		return
	}
	now := time.Now()
	probeVirtualRouterRecentConnectionState.mu.Lock()
	maybePruneProbeVirtualRouterRecentConnectionsLocked(now)
	connection, ok := probeVirtualRouterRecentConnectionState.items[key]
	flags := strings.ToUpper(strings.TrimSpace(packet.TCPFlags))
	if !ok || (connection.closed && strings.Contains(flags, "SYN") && !strings.Contains(flags, "ACK")) {
		connection = probeVirtualRouterRecentConnection{
			Kind:        "connection",
			TrafficType: probeVirtualRouterRecentPacketTrafficType(packet),
			FirstSeen:   packet.CapturedAt,
			Protocol:    strings.ToUpper(strings.TrimSpace(packet.Protocol)),
			EndpointA:   endpointA,
			EndpointB:   endpointB,
		}
	}
	if connection.TrafficType == "" {
		connection.TrafficType = probeVirtualRouterRecentPacketTrafficType(packet)
	}
	connection.Events++
	connection.Bytes += int64(packet.Length)
	if packet.Source == "tun_rx" {
		connection.TUNEvents++
	} else if packet.Source == "frame_rx" || packet.Source == "frame_tx" {
		connection.FrameEvents++
	}
	switch strings.TrimSpace(packet.Action) {
	case "forward":
		connection.Forwarded++
	case "deliver", "fake_exit", "exit", "local_icmp":
		connection.Delivered++
	case "drop":
		connection.Dropped++
	}
	eventFailed := strings.TrimSpace(packet.Error) != "" || strings.Contains(strings.TrimSpace(packet.Action), "error")
	if eventFailed {
		connection.Errors++
	}
	connection.LastSeen = packet.CapturedAt
	connection.lastAt = now
	connection.RouteID = strings.TrimSpace(packet.RouteID)
	connection.LocalNodeID = strings.TrimSpace(packet.LocalNodeID)
	connection.PeerNodeID = strings.TrimSpace(packet.PeerNodeID)
	connection.PathText = strings.TrimSpace(packet.PathText)
	if eventFailed || connection.Errors == 0 {
		connection.LastSource = strings.TrimSpace(packet.Source)
		connection.LastAction = strings.TrimSpace(packet.Action)
		connection.LastTCPFlags = strings.TrimSpace(packet.TCPFlags)
		connection.LastDetail = strings.TrimSpace(packet.Detail)
		connection.LastError = strings.TrimSpace(packet.Error)
	}
	if strings.TrimSpace(packet.FakeIPDomain) != "" {
		connection.Domain = strings.TrimSpace(packet.FakeIPDomain)
		connection.FakeIPDomain = strings.TrimSpace(packet.FakeIPDomain)
		connection.FakeIPExitNode = strings.TrimSpace(packet.FakeIPExitNode)
		applyProbeVirtualRouterRecentConnectionEndpointDomain(&connection, packet, connection.Domain, packet.FakeIPSide)
	} else if connection.Domain == "" && connection.TrafficType == "direct" {
		domain, side := probeVirtualRouterRecentDNSDomainForPacketLocked(packet)
		connection.Domain = domain
		applyProbeVirtualRouterRecentConnectionEndpointDomain(&connection, packet, domain, side)
	}
	if strings.Contains(flags, "FIN") || strings.Contains(flags, "RST") {
		connection.closed = true
	}
	probeVirtualRouterRecentConnectionState.items[key] = connection
	probeVirtualRouterRecentConnectionState.mu.Unlock()
	if domain := strings.TrimSpace(packet.FakeIPDomain); domain != "" {
		markProbeVirtualRouterRecentDNSConnection(domain, packet)
	}
}

func pruneProbeVirtualRouterRecentConnectionsLocked(now time.Time) {
	for key, item := range probeVirtualRouterRecentConnectionState.items {
		if item.lastAt.IsZero() || now.Sub(item.lastAt) > probeVirtualRouterRecentConnectionTTL {
			delete(probeVirtualRouterRecentConnectionState.items, key)
		}
	}
}

func maybePruneProbeVirtualRouterRecentConnectionsLocked(now time.Time) {
	if !probeVirtualRouterRecentConnectionState.lastPruneAt.IsZero() && now.Sub(probeVirtualRouterRecentConnectionState.lastPruneAt) < probeVirtualRouterRecentPruneInterval {
		return
	}
	pruneProbeVirtualRouterRecentConnectionsLocked(now)
	probeVirtualRouterRecentConnectionState.lastPruneAt = now
}

func finalizeProbeVirtualRouterRecentConnection(item probeVirtualRouterRecentConnection) probeVirtualRouterRecentConnection {
	if item.Kind == "dns" {
		if item.Connected {
			item.Status = "connected"
		} else if strings.TrimSpace(item.LastError) != "" {
			item.Status = "error"
		} else {
			item.Status = "unconnected"
		}
		return item
	}
	if item.Errors > 0 || item.Dropped > 0 {
		item.Status = "error"
	} else if item.closed {
		item.Status = "closed"
	} else {
		item.Status = "active"
	}
	return item
}

func recordProbeVirtualRouterRecentDNSQuery(domain string, action string, exitNodeID string, fakeIP string, realIPs []string, err error) {
	domain = normalizeProbeVirtualRouterDomain(domain)
	if domain == "" {
		return
	}
	now := time.Now()
	key := "DNS|" + domain
	probeVirtualRouterRecentConnectionState.mu.Lock()
	defer probeVirtualRouterRecentConnectionState.mu.Unlock()
	maybePruneProbeVirtualRouterRecentConnectionsLocked(now)
	connection, ok := probeVirtualRouterRecentConnectionState.items[key]
	if !ok || connection.Kind != "dns" {
		connection = probeVirtualRouterRecentConnection{
			Kind:            "dns",
			TrafficType:     "dns",
			FirstSeen:       now.UTC().Format(time.RFC3339Nano),
			Protocol:        "DNS",
			Domain:          domain,
			EndpointA:       "DNS",
			EndpointB:       domain,
			EndpointBDomain: domain,
		}
	}
	connection.Events++
	connection.DNSQueries++
	connection.LastSeen = now.UTC().Format(time.RFC3339Nano)
	connection.lastAt = now
	connection.LastSource = "dns"
	connection.LastAction = strings.TrimSpace(action)
	connection.PeerNodeID = normalizeProbeRouteNodeID(exitNodeID)
	if strings.TrimSpace(fakeIP) != "" {
		connection.FakeIPDomain = domain
		connection.FakeIPExitNode = normalizeProbeRouteNodeID(exitNodeID)
		connection.LastDetail = "Fake IP " + strings.TrimSpace(fakeIP)
	} else if len(realIPs) > 0 {
		connection.ResolvedIPs = sanitizeProbeVirtualRouterRecentResolvedIPs(realIPs)
		connection.LastDetail = "A " + strings.Join(connection.ResolvedIPs, ", ")
	}
	if err != nil {
		connection.Errors++
		connection.LastError = strings.TrimSpace(err.Error())
	} else {
		connection.LastError = ""
	}
	probeVirtualRouterRecentConnectionState.items[key] = connection
}

func recordProbeVirtualRouterRecentDialFailure(targetAddr string, decision probeVRouteProxyTargetDecision, err error) {
	if err == nil {
		return
	}
	now := time.Now()
	target := firstNonEmpty(strings.TrimSpace(decision.OriginalTarget), strings.TrimSpace(decision.TargetAddr), strings.TrimSpace(targetAddr))
	if target == "" {
		return
	}
	trafficType := "direct"
	if decision.Action == "probe_exit" && !decision.Direct() {
		trafficType = "proxy"
	}
	domain := normalizeProbeVirtualRouterDomain(decision.Domain)
	if domain == "" {
		host, _, splitErr := net.SplitHostPort(target)
		if splitErr == nil && net.ParseIP(strings.TrimSpace(strings.Trim(host, "[]"))) == nil {
			domain = normalizeProbeVirtualRouterDomain(host)
		}
	}
	key := strings.Join([]string{"DIAL", "TCP", trafficType, strings.ToLower(target)}, "|")
	probeVirtualRouterRecentConnectionState.mu.Lock()
	defer probeVirtualRouterRecentConnectionState.mu.Unlock()
	maybePruneProbeVirtualRouterRecentConnectionsLocked(now)
	connection, ok := probeVirtualRouterRecentConnectionState.items[key]
	if !ok {
		connection = probeVirtualRouterRecentConnection{
			Kind:            "connection",
			TrafficType:     trafficType,
			FirstSeen:       now.UTC().Format(time.RFC3339Nano),
			Protocol:        "TCP",
			Domain:          domain,
			EndpointA:       "local-proxy",
			EndpointB:       target,
			EndpointBDomain: domain,
		}
	}
	connection.Events++
	connection.Errors++
	connection.LastSeen = now.UTC().Format(time.RFC3339Nano)
	connection.lastAt = now
	connection.RouteID = strings.TrimSpace(decision.RuleID)
	connection.PeerNodeID = normalizeProbeRouteNodeID(decision.ExitNodeID)
	connection.PathText = strings.Join(cleanProbeVirtualRouterPath(decision.Path), ">")
	connection.LastSource = "proxy"
	connection.LastAction = trafficType + "_error"
	connection.LastDetail = strings.TrimSpace(decision.TargetAddr)
	connection.LastError = strings.TrimSpace(err.Error())
	if trafficType == "proxy" {
		connection.FakeIPDomain = domain
		connection.FakeIPExitNode = normalizeProbeRouteNodeID(decision.ExitNodeID)
	}
	probeVirtualRouterRecentConnectionState.items[key] = connection
}

func probeVirtualRouterRecentPacketIsProbeP2P(packet probeVirtualRouterRecentPacket) bool {
	sourceIP := net.ParseIP(strings.TrimSpace(packet.SourceIP)).To4()
	destinationIP := net.ParseIP(strings.TrimSpace(packet.DestinationIP)).To4()
	if sourceIP == nil || destinationIP == nil {
		return false
	}
	probeVirtualRouterState.mu.RLock()
	_, sourceIsProbe := probeVirtualRouterState.ipToNode[sourceIP.String()]
	_, destinationIsProbe := probeVirtualRouterState.ipToNode[destinationIP.String()]
	probeVirtualRouterState.mu.RUnlock()
	return sourceIsProbe && destinationIsProbe
}

func probeVirtualRouterRecentPacketTrafficType(packet probeVirtualRouterRecentPacket) string {
	if packet.FakeIP || strings.TrimSpace(packet.FakeIPDomain) != "" || len(cleanProbeVirtualRouterPath(packet.Path)) >= 2 || strings.TrimSpace(packet.Source) == "frame_rx" {
		return "proxy"
	}
	return "direct"
}

func sanitizeProbeVirtualRouterRecentResolvedIPs(items []string) []string {
	seen := make(map[string]struct{}, len(items))
	out := make([]string, 0, len(items))
	for _, item := range items {
		ip := net.ParseIP(strings.TrimSpace(item))
		if ip == nil {
			continue
		}
		value := ip.String()
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func probeVirtualRouterRecentDNSDomainForPacketLocked(packet probeVirtualRouterRecentPacket) (string, string) {
	bestDomain := ""
	bestSide := ""
	bestAt := time.Time{}
	for _, candidate := range []struct {
		ip   string
		side string
	}{
		{ip: packet.DestinationIP, side: "dst"},
		{ip: packet.SourceIP, side: "src"},
	} {
		ip := net.ParseIP(strings.TrimSpace(candidate.ip))
		if ip == nil {
			continue
		}
		value := ip.String()
		for _, item := range probeVirtualRouterRecentConnectionState.items {
			if item.Kind != "dns" || item.Domain == "" || item.lastAt.Before(bestAt) {
				continue
			}
			for _, resolvedIP := range item.ResolvedIPs {
				if resolvedIP == value {
					bestDomain = item.Domain
					bestSide = candidate.side
					bestAt = item.lastAt
					break
				}
			}
		}
	}
	return bestDomain, bestSide
}

func applyProbeVirtualRouterRecentConnectionEndpointDomain(connection *probeVirtualRouterRecentConnection, packet probeVirtualRouterRecentPacket, domain string, side string) {
	if connection == nil {
		return
	}
	domain = normalizeProbeVirtualRouterDomain(domain)
	if domain == "" {
		return
	}
	var endpoint string
	switch strings.ToLower(strings.TrimSpace(side)) {
	case "src", "source":
		endpoint = probeVirtualRouterRecentConnectionEndpoint(packet.SourceIP, packet.SourcePort)
	case "dst", "destination":
		endpoint = probeVirtualRouterRecentConnectionEndpoint(packet.DestinationIP, packet.DestinationPort)
	default:
		return
	}
	if endpoint == connection.EndpointA {
		connection.EndpointADomain = domain
	}
	if endpoint == connection.EndpointB {
		connection.EndpointBDomain = domain
	}
}

func markProbeVirtualRouterRecentDNSConnection(domain string, packet probeVirtualRouterRecentPacket) {
	domain = normalizeProbeVirtualRouterDomain(domain)
	if domain == "" {
		return
	}
	now := time.Now()
	key := "DNS|" + domain
	probeVirtualRouterRecentConnectionState.mu.Lock()
	defer probeVirtualRouterRecentConnectionState.mu.Unlock()
	connection, ok := probeVirtualRouterRecentConnectionState.items[key]
	if !ok || connection.Kind != "dns" {
		return
	}
	connection.Connected = true
	connection.LastSeen = packet.CapturedAt
	connection.lastAt = now
	connection.LastSource = strings.TrimSpace(packet.Source)
	connection.LastAction = "connected"
	connection.LastDetail = strings.TrimSpace(packet.Protocol) + " " + probeVirtualRouterRecentConnectionEndpoint(packet.DestinationIP, packet.DestinationPort)
	probeVirtualRouterRecentConnectionState.items[key] = connection
}

func probeVirtualRouterRecentConnectionKey(packet probeVirtualRouterRecentPacket) (string, string, string) {
	protocol := strings.ToUpper(strings.TrimSpace(packet.Protocol))
	left := probeVirtualRouterRecentConnectionEndpoint(packet.SourceIP, packet.SourcePort)
	right := probeVirtualRouterRecentConnectionEndpoint(packet.DestinationIP, packet.DestinationPort)
	if protocol == "" || left == "" || right == "" {
		return "", "", ""
	}
	endpointA, endpointB := left, right
	if endpointB < endpointA {
		endpointA, endpointB = endpointB, endpointA
	}
	return strings.Join([]string{protocol, endpointA, endpointB}, "|"), endpointA, endpointB
}

func probeVirtualRouterRecentConnectionEndpoint(ip string, port uint16) string {
	address := strings.TrimSpace(ip)
	if address == "" {
		return ""
	}
	if port == 0 {
		return address
	}
	return net.JoinHostPort(address, strconv.Itoa(int(port)))
}

func applyProbeVirtualRouterFakeIPLibrary(library probeVirtualRouterFakeIPLibrary) {
	nextLibrary := probeVirtualRouterFakeIPLibraryWithMemoryTTL(library, time.Now().UTC())
	probeVirtualRouterState.mu.Lock()
	currentVersion := probeVirtualRouterState.config.FakeIPLibrary.Version
	if nextLibrary.Version >= currentVersion {
		probeVirtualRouterState.config.FakeIPLibrary = nextLibrary
	}
	probeVirtualRouterState.mu.Unlock()
}

func applyProbeVirtualRouterFakeIPEntry(item probeVirtualRouterFakeIPEntry) bool {
	item = sanitizeProbeVirtualRouterFakeIPEntry(item)
	if strings.TrimSpace(item.Domain) == "" || strings.TrimSpace(item.FakeIP) == "" {
		return false
	}
	now := time.Now().UTC()
	if probeVirtualRouterFakeIPEntryExpired(item, now) {
		return false
	}
	item = probeVirtualRouterFakeIPEntryWithMemoryTTL(item, now)
	probeVirtualRouterState.mu.Lock()
	library := sanitizeProbeVirtualRouterFakeIPLibrary(probeVirtualRouterState.config.FakeIPLibrary)
	next := library.Items[:0]
	for _, existing := range library.Items {
		if existing.Domain == item.Domain || strings.TrimSpace(existing.FakeIP) == strings.TrimSpace(item.FakeIP) {
			continue
		}
		next = append(next, existing)
	}
	next = append(next, item)
	library.Items = next
	if library.Version <= 0 {
		library.Version = 1
	}
	library.Version++
	library.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	sort.SliceStable(library.Items, func(i, j int) bool {
		return library.Items[i].Domain < library.Items[j].Domain
	})
	probeVirtualRouterState.config.FakeIPLibrary = library
	probeVirtualRouterState.mu.Unlock()
	return true
}

func probeVirtualRouterFakeIPLibraryWithMemoryTTL(library probeVirtualRouterFakeIPLibrary, now time.Time) probeVirtualRouterFakeIPLibrary {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	library = sanitizeProbeVirtualRouterFakeIPLibrary(library)
	next := library.Items[:0]
	for _, item := range library.Items {
		if probeVirtualRouterFakeIPEntryExpired(item, now) {
			continue
		}
		next = append(next, probeVirtualRouterFakeIPEntryWithMemoryTTL(item, now))
	}
	library.Items = next
	return library
}

func probeVirtualRouterFakeIPEntryWithMemoryTTL(item probeVirtualRouterFakeIPEntry, now time.Time) probeVirtualRouterFakeIPEntry {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	maxExpiresAt := now.Add(probeVirtualRouterFakeIPMemoryTTL)
	expiresAt, err := time.Parse(time.RFC3339, strings.TrimSpace(item.ExpiresAt))
	if err != nil || expiresAt.IsZero() || expiresAt.After(maxExpiresAt) {
		item.ExpiresAt = maxExpiresAt.Format(time.RFC3339)
		item.UpdatedAt = now.Format(time.RFC3339)
	}
	return item
}

func sanitizeProbeVirtualRouterFakeIPEntry(item probeVirtualRouterFakeIPEntry) probeVirtualRouterFakeIPEntry {
	domain := normalizeProbeVirtualRouterDomain(item.Domain)
	ip := net.ParseIP(strings.TrimSpace(item.FakeIP)).To4()
	if domain == "" || ip == nil {
		return probeVirtualRouterFakeIPEntry{}
	}
	return probeVirtualRouterFakeIPEntry{
		Domain:     domain,
		FakeIP:     ip.String(),
		RuleID:     strings.TrimSpace(item.RuleID),
		Action:     sanitizeProbeVirtualRouterRouteRuleAction(item.Action, item.ExitNodeID),
		ExitNodeID: normalizeProbeRouteNodeID(item.ExitNodeID),
		ExpiresAt:  strings.TrimSpace(item.ExpiresAt),
		UpdatedAt:  strings.TrimSpace(item.UpdatedAt),
	}
}

func probeVirtualRouterCloneNodeToIPLocked() map[string]string {
	out := make(map[string]string, len(probeVirtualRouterState.nodeToIP))
	for key, value := range probeVirtualRouterState.nodeToIP {
		out[key] = value
	}
	return out
}

func probeVirtualRouterCloneNeighborsLocked() map[string]map[string]struct{} {
	out := make(map[string]map[string]struct{}, len(probeVirtualRouterState.neighbors))
	for nodeID, peers := range probeVirtualRouterState.neighbors {
		nextPeers := make(map[string]struct{}, len(peers))
		for peerID := range peers {
			nextPeers[peerID] = struct{}{}
		}
		out[nodeID] = nextPeers
	}
	return out
}

func currentProbeVirtualRouterLocalNodeID() string {
	probeVirtualRouterState.mu.RLock()
	defer probeVirtualRouterState.mu.RUnlock()
	return strings.TrimSpace(probeVirtualRouterState.localNodeID)
}

func currentProbeVirtualRouterLocalNodeIDForRuntime(runtime *probeVirtualRouterRuntime) string {
	if runtime != nil {
		if nodeID := normalizeProbeRouteNodeID(runtime.cfg.identity.NodeID); nodeID != "" {
			return nodeID
		}
	}
	return currentProbeVirtualRouterLocalNodeID()
}

func currentProbeVirtualRouterLocalIP() string {
	probeVirtualRouterState.mu.RLock()
	localIP := strings.TrimSpace(probeVirtualRouterState.localIP)
	nodeID := strings.TrimSpace(probeVirtualRouterState.localNodeID)
	nodeToIP := probeVirtualRouterCloneNodeToIPLocked()
	probeVirtualRouterState.mu.RUnlock()
	if localIP != "" {
		return localIP
	}
	return nodeToIP[nodeID]
}

func currentProbeVirtualRouterLocalIPForRuntime(runtime *probeVirtualRouterRuntime) string {
	nodeID := currentProbeVirtualRouterLocalNodeIDForRuntime(runtime)
	if nodeID != "" {
		if ip := currentProbeVirtualRouterIPForNode(nodeID); ip != "" {
			return ip
		}
	}
	return currentProbeVirtualRouterLocalIP()
}

func ensureProbeVirtualRouterLocalInterfaceIP() {
	if !probeVirtualRouterBaseTransportEnabled() {
		return
	}
	localIP, err := ensureProbeVirtualRouterLocalInterfaceIPOnce()
	if localIP == "" {
		return
	}
	if err != nil {
		log.Printf("warning: ensure probe virtual router local ip failed: ip=%s err=%v", localIP, err)
		scheduleProbeVirtualRouterLocalInterfaceIPRetry(localIP, err)
		return
	}
	log.Printf("probe virtual router local ip ensured: ip=%s", localIP)
	markProbeVirtualRouterLocalInterfaceEnsured(localIP)
	markProbeLocalTUNInterfaceReady()
}

func scheduleProbeVirtualRouterLocalInterfaceIPEnsure(reason string) {
	probeVirtualRouterLocalInterfaceEnsureState.mu.Lock()
	if probeVirtualRouterLocalInterfaceEnsureState.running {
		probeVirtualRouterLocalInterfaceEnsureState.mu.Unlock()
		return
	}
	done := make(chan struct{})
	probeVirtualRouterLocalInterfaceEnsureState.running = true
	probeVirtualRouterLocalInterfaceEnsureState.done = done
	probeVirtualRouterLocalInterfaceEnsureState.mu.Unlock()
	go func() {
		defer func() {
			probeVirtualRouterLocalInterfaceEnsureState.mu.Lock()
			probeVirtualRouterLocalInterfaceEnsureState.running = false
			probeVirtualRouterLocalInterfaceEnsureState.done = nil
			close(done)
			probeVirtualRouterLocalInterfaceEnsureState.mu.Unlock()
		}()
		if cleanReason := strings.TrimSpace(reason); cleanReason != "" {
			log.Printf("probe virtual router local ip ensure scheduled: reason=%s", cleanReason)
		}
		ensureProbeVirtualRouterLocalInterfaceIP()
	}()
}

func waitProbeVirtualRouterLocalInterfaceIPEnsure() {
	probeVirtualRouterLocalInterfaceEnsureState.mu.Lock()
	done := probeVirtualRouterLocalInterfaceEnsureState.done
	probeVirtualRouterLocalInterfaceEnsureState.mu.Unlock()
	if done != nil {
		<-done
	}
}

func ensureProbeVirtualRouterLocalInterfaceIPOnce() (string, error) {
	if !probeVirtualRouterBaseTransportEnabled() {
		return "", nil
	}
	localIP := currentProbeVirtualRouterLocalIP()
	if localIP == "" {
		return "", nil
	}
	if err := ensureProbeVirtualRouterPlatformInterfaceIP(localIP); err != nil {
		return localIP, err
	}
	return localIP, nil
}

func scheduleProbeVirtualRouterLocalInterfaceIPRetry(localIP string, cause error) {
	if strings.TrimSpace(localIP) == "" || cause == nil {
		return
	}
	if !probeVirtualRouterBaseTransportEnabled() {
		return
	}
	cleanIP := strings.TrimSpace(localIP)
	probeVirtualRouterLocalInterfaceRetryState.mu.Lock()
	if probeVirtualRouterLocalInterfaceRetryState.running {
		probeVirtualRouterLocalInterfaceRetryState.mu.Unlock()
		return
	}
	probeVirtualRouterLocalInterfaceRetryState.running = true
	probeVirtualRouterLocalInterfaceRetryState.generation++
	retryGeneration := probeVirtualRouterLocalInterfaceRetryState.generation
	cancel := make(chan struct{})
	done := make(chan struct{})
	probeVirtualRouterLocalInterfaceRetryState.cancel = cancel
	probeVirtualRouterLocalInterfaceRetryState.done = done
	probeVirtualRouterLocalInterfaceRetryState.mu.Unlock()

	go func() {
		defer func() {
			probeVirtualRouterLocalInterfaceRetryState.mu.Lock()
			probeVirtualRouterLocalInterfaceRetryState.running = false
			if probeVirtualRouterLocalInterfaceRetryState.cancel == cancel {
				probeVirtualRouterLocalInterfaceRetryState.cancel = nil
			}
			if probeVirtualRouterLocalInterfaceRetryState.done == done {
				probeVirtualRouterLocalInterfaceRetryState.done = nil
			}
			close(done)
			probeVirtualRouterLocalInterfaceRetryState.mu.Unlock()
		}()
		delays := append([]time.Duration(nil), probeVirtualRouterLocalInterfaceRetryDelays...)
		log.Printf("probe virtual router local ip retry scheduled: ip=%s reason=%v", cleanIP, cause)
		for attempt, delay := range delays {
			timer := time.NewTimer(delay)
			select {
			case <-timer.C:
			case <-cancel:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				log.Printf("probe virtual router local ip retry stopped: ip=%s reason=canceled attempt=%d", cleanIP, attempt+1)
				return
			}
			if probeVirtualRouterLocalInterfaceRetryObsolete(cleanIP, retryGeneration) {
				log.Printf("probe virtual router local ip retry stopped: ip=%s reason=already_ensured attempt=%d", cleanIP, attempt+1)
				return
			}
			nextIP, err := ensureProbeVirtualRouterLocalInterfaceIPOnce()
			if nextIP == "" {
				log.Printf("probe virtual router local ip retry stopped: reason=local_ip_empty attempt=%d", attempt+1)
				return
			}
			if err != nil {
				log.Printf("warning: probe virtual router local ip retry failed: ip=%s attempt=%d err=%v", nextIP, attempt+1, err)
				continue
			}
			if probeVirtualRouterLocalInterfaceRetryObsolete(cleanIP, retryGeneration) {
				log.Printf("probe virtual router local ip retry stopped: ip=%s reason=already_ensured attempt=%d", cleanIP, attempt+1)
				return
			}
			log.Printf("probe virtual router local ip retry succeeded: ip=%s attempt=%d", nextIP, attempt+1)
			markProbeVirtualRouterLocalInterfaceEnsured(nextIP)
			markProbeLocalTUNInterfaceReady()
			return
		}
		log.Printf("warning: probe virtual router local ip retry exhausted: ip=%s attempts=%d", cleanIP, len(delays))
	}()
}

func probeVirtualRouterBaseTransportEnabled() bool {
	probeVirtualRouterState.mu.RLock()
	enabled := probeVirtualRouterState.config.Enabled && strings.TrimSpace(probeVirtualRouterState.localIP) != ""
	probeVirtualRouterState.mu.RUnlock()
	return enabled
}

func cancelAndWaitProbeVirtualRouterLocalInterfaceIPRetry() {
	probeVirtualRouterLocalInterfaceRetryState.mu.Lock()
	probeVirtualRouterLocalInterfaceRetryState.generation++
	cancel := probeVirtualRouterLocalInterfaceRetryState.cancel
	done := probeVirtualRouterLocalInterfaceRetryState.done
	if cancel != nil {
		close(cancel)
		probeVirtualRouterLocalInterfaceRetryState.cancel = nil
	}
	probeVirtualRouterLocalInterfaceRetryState.mu.Unlock()
	if done != nil {
		<-done
	}
}

func markProbeVirtualRouterLocalInterfaceEnsured(localIP string) {
	cleanIP := strings.TrimSpace(localIP)
	if cleanIP == "" {
		return
	}
	probeVirtualRouterLocalInterfaceRetryState.mu.Lock()
	probeVirtualRouterLocalInterfaceRetryState.ensuredIP = cleanIP
	probeVirtualRouterLocalInterfaceRetryState.ensuredAt = time.Now()
	probeVirtualRouterLocalInterfaceRetryState.generation++
	probeVirtualRouterLocalInterfaceRetryState.mu.Unlock()
}

func probeVirtualRouterLocalInterfaceRetryObsolete(localIP string, generation uint64) bool {
	cleanIP := strings.TrimSpace(localIP)
	if cleanIP == "" {
		return true
	}
	probeVirtualRouterLocalInterfaceRetryState.mu.Lock()
	defer probeVirtualRouterLocalInterfaceRetryState.mu.Unlock()
	if probeVirtualRouterLocalInterfaceRetryState.generation != generation {
		return true
	}
	return probeVirtualRouterLocalInterfaceRetryState.ensuredIP == cleanIP && !probeVirtualRouterLocalInterfaceRetryState.ensuredAt.IsZero()
}

func probeVirtualRouterIPForNode(config probeVirtualRouterConfig, nodeID string) string {
	target := normalizeProbeRouteNodeID(nodeID)
	if target == "" {
		return ""
	}
	for _, item := range config.ProbeIPs {
		if normalizeProbeRouteNodeID(item.NodeID) == target {
			return strings.TrimSpace(item.IP)
		}
	}
	return ""
}

func probeVirtualRouterDisplayNameForNode(config probeVirtualRouterConfig, nodeID string) string {
	target := normalizeProbeRouteNodeID(nodeID)
	if target == "" {
		return ""
	}
	for _, item := range config.ProbeIPs {
		if normalizeProbeRouteNodeID(item.NodeID) == target {
			return strings.TrimSpace(item.DisplayName)
		}
	}
	return ""
}

func probeVirtualRouterReachable(config probeVirtualRouterConfig, fromNodeID string, toNodeID string) bool {
	return len(probeVirtualRouterPath(config, fromNodeID, toNodeID)) > 0
}

func probeVirtualRouterPath(config probeVirtualRouterConfig, fromNodeID string, toNodeID string) []string {
	from := normalizeProbeRouteNodeID(fromNodeID)
	to := normalizeProbeRouteNodeID(toNodeID)
	if !config.Enabled || from == "" || to == "" {
		return nil
	}
	if from == to {
		return []string{from}
	}
	graph := map[string]map[string]struct{}{}
	addEdge := func(a string, b string) {
		a = normalizeProbeRouteNodeID(a)
		b = normalizeProbeRouteNodeID(b)
		if a == "" || b == "" {
			return
		}
		if graph[a] == nil {
			graph[a] = map[string]struct{}{}
		}
		graph[a][b] = struct{}{}
	}
	for _, rule := range config.TopologyRules {
		if !rule.Enabled {
			continue
		}
		addEdge(rule.FromNodeID, rule.ToNodeID)
		addEdge(rule.ToNodeID, rule.FromNodeID)
	}
	return selectProbeVirtualRouterBestPath(probeVirtualRouterShortestPathsFromNeighbors(graph, from, to), false)
}

func probeVirtualRouterPathFromNeighbors(neighbors map[string]map[string]struct{}, fromNodeID string, toNodeID string) []string {
	from := normalizeProbeRouteNodeID(fromNodeID)
	to := normalizeProbeRouteNodeID(toNodeID)
	if from == "" || to == "" {
		return nil
	}
	if from == to {
		return []string{from}
	}
	return selectProbeVirtualRouterBestPath(probeVirtualRouterCandidatePathsFromNeighbors(neighbors, from, to), true)
}

func probeVirtualRouterShortestPathsFromNeighbors(neighbors map[string]map[string]struct{}, from string, to string) [][]string {
	if from == "" || to == "" {
		return nil
	}
	if from == to {
		return [][]string{{from}}
	}
	distance := map[string]int{from: 0}
	parents := map[string][]string{}
	queue := []string{from}
	foundDistance := -1
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		currentDistance := distance[current]
		if foundDistance >= 0 && currentDistance >= foundDistance {
			continue
		}
		for _, next := range sortedProbeVirtualRouterNeighborIDs(neighbors, current) {
			nextDistance := currentDistance + 1
			if foundDistance >= 0 && nextDistance > foundDistance {
				continue
			}
			oldDistance, seen := distance[next]
			if !seen {
				distance[next] = nextDistance
				parents[next] = []string{current}
				if next == to {
					foundDistance = nextDistance
				} else {
					queue = append(queue, next)
				}
				continue
			}
			if oldDistance == nextDistance {
				parents[next] = append(parents[next], current)
			}
		}
	}
	if foundDistance < 0 {
		return nil
	}
	return buildProbeVirtualRouterShortestPaths(parents, from, to)
}

func probeVirtualRouterCandidatePathsFromNeighbors(neighbors map[string]map[string]struct{}, from string, to string) [][]string {
	from = normalizeProbeRouteNodeID(from)
	to = normalizeProbeRouteNodeID(to)
	if from == "" || to == "" {
		return nil
	}
	if from == to {
		return [][]string{{from}}
	}
	out := make([][]string, 0)
	visited := map[string]bool{from: true}
	var walk func(string, []string)
	walk = func(current string, path []string) {
		if len(out) >= probeVirtualRouterPathRecoveryMaxCandidates {
			return
		}
		if current == to {
			out = append(out, append([]string(nil), path...))
			return
		}
		if len(path)-1 >= probeVirtualRouterPathRecoveryMaxHops {
			return
		}
		for _, next := range sortedProbeVirtualRouterNeighborIDs(neighbors, current) {
			if visited[next] {
				continue
			}
			visited[next] = true
			walk(next, append(path, next))
			delete(visited, next)
		}
	}
	walk(from, []string{from})
	filtered := out[:0]
	for _, path := range out {
		if probeVirtualRouterPathHasAvailableSourceShortcut(neighbors, path) {
			continue
		}
		filtered = append(filtered, path)
	}
	out = filtered
	sort.SliceStable(out, func(i, j int) bool {
		if len(out[i]) != len(out[j]) {
			return len(out[i]) < len(out[j])
		}
		return compareProbeVirtualRouterPathLexicographic(out[i], out[j]) < 0
	})
	return out
}

func probeVirtualRouterPathHasAvailableSourceShortcut(neighbors map[string]map[string]struct{}, path []string) bool {
	cleanPath := cleanProbeVirtualRouterPath(path)
	if len(cleanPath) < 3 {
		return false
	}
	source := cleanPath[0]
	for index := 2; index < len(cleanPath); index++ {
		candidate := cleanPath[index]
		if _, adjacent := neighbors[source][candidate]; !adjacent {
			continue
		}
		directPath := []string{source, candidate}
		if probeVirtualRouterPathShouldAvoid(directPath) {
			continue
		}
		if _, ok := currentProbeVirtualRouterPathLatencyMS(directPath); ok {
			return true
		}
		if rt, _ := probeVirtualRouterRuntimeForAdjacentNode(candidate); rt != nil && probeVirtualRouterRuntimeHasPhysicalBridgeSession(rt) {
			return true
		}
	}
	return false
}

func sortedProbeVirtualRouterNeighborIDs(neighbors map[string]map[string]struct{}, nodeID string) []string {
	items := make([]string, 0, len(neighbors[nodeID]))
	for item := range neighbors[nodeID] {
		if clean := normalizeProbeRouteNodeID(item); clean != "" {
			items = append(items, clean)
		}
	}
	sort.Strings(items)
	return items
}

func buildProbeVirtualRouterPath(parent map[string]string, from string, to string) []string {
	if from == "" || to == "" {
		return nil
	}
	path := []string{to}
	for current := to; current != from; {
		prev := parent[current]
		if prev == "" {
			return nil
		}
		path = append(path, prev)
		current = prev
	}
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}
	return path
}

func buildProbeVirtualRouterShortestPaths(parents map[string][]string, from string, to string) [][]string {
	var out [][]string
	var walk func(current string, suffix []string)
	walk = func(current string, suffix []string) {
		if current == "" {
			return
		}
		nextSuffix := append([]string{current}, suffix...)
		if current == from {
			out = append(out, nextSuffix)
			return
		}
		parentItems := append([]string(nil), parents[current]...)
		sort.Strings(parentItems)
		for _, parent := range parentItems {
			walk(parent, nextSuffix)
		}
	}
	walk(to, nil)
	sort.SliceStable(out, func(i, j int) bool {
		return compareProbeVirtualRouterPathLexicographic(out[i], out[j]) < 0
	})
	return out
}

func selectProbeVirtualRouterBestPath(paths [][]string, useRTT bool) []string {
	if len(paths) == 0 {
		return nil
	}
	eligible := make([][]string, 0, len(paths))
	for _, path := range paths {
		if !probeVirtualRouterPathShouldAvoid(path) {
			eligible = append(eligible, path)
		}
	}
	if len(eligible) == 0 {
		return nil
	}
	paths = eligible
	best := append([]string(nil), paths[0]...)
	for _, path := range paths[1:] {
		if probeVirtualRouterPathLess(path, best, useRTT) {
			best = append([]string(nil), path...)
		}
	}
	return best
}

func probeVirtualRouterPathShouldAvoid(path []string) bool {
	cleanPath := cleanProbeVirtualRouterPath(path)
	localNodeID := currentProbeVirtualRouterLocalNodeID()
	if len(cleanPath) < 2 || localNodeID == "" || cleanPath[0] != localNodeID {
		return false
	}
	// End-to-end probe failures are diagnostic. Only a disconnected local carrier
	// can prove that the selected first hop is unavailable.
	rt, _ := probeVirtualRouterRuntimeForAdjacentNode(cleanPath[1])
	return rt != nil && rt.cfg.dialer && probeVirtualRouterPhysicalCarrierIsDisconnected(rt.cfg.routeID)
}

func probeVirtualRouterPhysicalCarrierIsDisconnected(routeID string) bool {
	key := strings.TrimSpace(routeID)
	if key == "" {
		return false
	}
	probeVirtualRouterDisconnectedCarrierState.mu.RLock()
	_, disconnected := probeVirtualRouterDisconnectedCarrierState.routeIDs[key]
	probeVirtualRouterDisconnectedCarrierState.mu.RUnlock()
	return disconnected
}

func markProbeVirtualRouterPhysicalCarrierDisconnected(rt *probeVirtualRouterRuntime, reason string) {
	if rt == nil {
		return
	}
	routeID := strings.TrimSpace(rt.cfg.routeID)
	if routeID == "" {
		return
	}
	probeVirtualRouterDisconnectedCarrierState.mu.Lock()
	_, alreadyDisconnected := probeVirtualRouterDisconnectedCarrierState.routeIDs[routeID]
	if probeVirtualRouterDisconnectedCarrierState.routeIDs == nil {
		probeVirtualRouterDisconnectedCarrierState.routeIDs = make(map[string]struct{})
	}
	probeVirtualRouterDisconnectedCarrierState.routeIDs[routeID] = struct{}{}
	probeVirtualRouterDisconnectedCarrierState.mu.Unlock()
	if !alreadyDisconnected {
		clearProbeVirtualRouterRouteCacheForRuntime(rt, "physical carrier disconnected")
		closeProbeVRouteProxyTCPSessionsForEdge(rt.cfg.localNodeID, rt.cfg.peerNodeID, reason)
		closeProbeVRouteProxyUDPSessionsForEdge(rt.cfg.localNodeID, rt.cfg.peerNodeID)
		log.Printf("probe virtual router physical carrier marked unavailable: route=%s reason=%s", routeID, strings.TrimSpace(reason))
	}
}

func clearProbeVirtualRouterPhysicalCarrierDisconnected(rt *probeVirtualRouterRuntime) {
	if rt == nil {
		return
	}
	routeID := strings.TrimSpace(rt.cfg.routeID)
	if routeID == "" {
		return
	}
	probeVirtualRouterDisconnectedCarrierState.mu.Lock()
	_, wasDisconnected := probeVirtualRouterDisconnectedCarrierState.routeIDs[routeID]
	delete(probeVirtualRouterDisconnectedCarrierState.routeIDs, routeID)
	probeVirtualRouterDisconnectedCarrierState.mu.Unlock()
	if wasDisconnected {
		clearProbeVirtualRouterRouteCache("physical carrier recovered")
		log.Printf("probe virtual router physical carrier recovered: route=%s", routeID)
	}
}

func probeVirtualRouterPathLess(left []string, right []string, useRTT bool) bool {
	if len(left) != len(right) {
		return len(left) < len(right)
	}
	if useRTT {
		leftRTT, leftMissing := probeVirtualRouterPathRTTScore(left)
		rightRTT, rightMissing := probeVirtualRouterPathRTTScore(right)
		if leftMissing != rightMissing {
			return leftMissing < rightMissing
		}
		if leftRTT != rightRTT {
			return leftRTT < rightRTT
		}
	}
	return compareProbeVirtualRouterPathLexicographic(left, right) < 0
}

func probeVirtualRouterPathRTTScore(path []string) (int64, int) {
	if latencyMS, ok := currentProbeVirtualRouterPathLatencyMS(path); ok {
		return latencyMS, 0
	}
	var total int64
	missing := 0
	for i := 0; i+1 < len(path); i++ {
		latencyMS, ok := currentProbeVirtualRouterAdjacentLatencyMS(path[i], path[i+1])
		if !ok {
			missing++
			continue
		}
		total += latencyMS
	}
	return total, missing
}

func currentProbeVirtualRouterPathLatencyMS(path []string) (int64, bool) {
	key := probeVirtualRouterPathKey(path)
	if key == "" {
		return 0, false
	}
	probeVirtualRouterPathRTTState.mu.RLock()
	item, ok := probeVirtualRouterPathRTTState.items[key]
	probeVirtualRouterPathRTTState.mu.RUnlock()
	if !ok || item.RTTMS <= 0 || strings.TrimSpace(item.LastError) != "" {
		return 0, false
	}
	return item.RTTMS, true
}

func recordProbeVirtualRouterPathRTTSuccess(path []string, latency time.Duration, responder string) {
	key := probeVirtualRouterPathKey(path)
	if key == "" {
		return
	}
	nextRTTMS := probeDurationMilliseconds(latency)
	target := ""
	if len(path) > 0 {
		target = normalizeProbeRouteNodeID(path[len(path)-1])
	}
	shouldClearRouteCache := false
	probeVirtualRouterPathRTTState.mu.Lock()
	if probeVirtualRouterPathRTTState.items == nil {
		probeVirtualRouterPathRTTState.items = make(map[string]probeVirtualRouterPathRTTRecord)
	}
	previous := probeVirtualRouterPathRTTState.items[key]
	shouldClearRouteCache = previous.RTTMS != nextRTTMS || strings.TrimSpace(previous.LastError) != "" || previous.ConsecutiveFailureCount != 0
	probeVirtualRouterPathRTTState.items[key] = probeVirtualRouterPathRTTRecord{
		RTTMS:                   nextRTTMS,
		LastAt:                  time.Now().UTC(),
		ConsecutiveFailureCount: 0,
		TargetNode:              target,
		Responder:               strings.TrimSpace(responder),
	}
	probeVirtualRouterPathRTTState.mu.Unlock()
	if shouldClearRouteCache {
		clearProbeVirtualRouterRouteCache("")
	}
}

func recordProbeVirtualRouterPathRTTError(path []string, err error) bool {
	if err == nil {
		return false
	}
	key := probeVirtualRouterPathKey(path)
	if key == "" {
		return false
	}
	target := ""
	if len(path) > 0 {
		target = normalizeProbeRouteNodeID(path[len(path)-1])
	}
	nextError := strings.TrimSpace(err.Error())
	shouldClearRouteCache := false
	probeVirtualRouterPathRTTState.mu.Lock()
	if probeVirtualRouterPathRTTState.items == nil {
		probeVirtualRouterPathRTTState.items = make(map[string]probeVirtualRouterPathRTTRecord)
	}
	item := probeVirtualRouterPathRTTState.items[key]
	shouldClearRouteCache = strings.TrimSpace(item.LastError) != nextError
	item.LastAt = time.Now().UTC()
	item.LastError = nextError
	item.ConsecutiveFailureCount++
	item.TargetNode = target
	probeVirtualRouterPathRTTState.items[key] = item
	thresholdReached := item.ConsecutiveFailureCount == probeVirtualRouterPathRTTFailureThreshold
	probeVirtualRouterPathRTTState.mu.Unlock()
	if shouldClearRouteCache {
		clearProbeVirtualRouterRouteCache("path rtt query error")
	}
	return thresholdReached
}

func scheduleProbeVirtualRouterPathRecovery(path []string) {
	cleanPath := cleanProbeVirtualRouterPath(path)
	if len(cleanPath) < 2 {
		return
	}
	sourceNodeID := cleanPath[0]
	targetNodeID := cleanPath[len(cleanPath)-1]
	key := probeVirtualRouterRouteCacheKey(sourceNodeID, targetNodeID)
	if key == "" {
		return
	}
	probeVirtualRouterPathRecoveryState.mu.Lock()
	if _, running := probeVirtualRouterPathRecoveryState.inflight[key]; running {
		probeVirtualRouterPathRecoveryState.mu.Unlock()
		return
	}
	probeVirtualRouterPathRecoveryState.inflight[key] = struct{}{}
	probeVirtualRouterPathRecoveryState.mu.Unlock()
	go func() {
		defer func() {
			probeVirtualRouterPathRecoveryState.mu.Lock()
			delete(probeVirtualRouterPathRecoveryState.inflight, key)
			probeVirtualRouterPathRecoveryState.mu.Unlock()
		}()
		probeVirtualRouterState.mu.RLock()
		neighbors := probeVirtualRouterCloneNeighborsLocked()
		probeVirtualRouterState.mu.RUnlock()
		paths := probeVirtualRouterCandidatePathsFromNeighbors(neighbors, sourceNodeID, targetNodeID)
		reachablePaths := make([][]string, 0, len(paths))
		for _, candidate := range paths {
			if _, err := probeVirtualRouterQueryPathRTT(candidate); err == nil {
				reachablePaths = append(reachablePaths, candidate)
			}
		}
		clearProbeVirtualRouterRouteCacheForPath(cleanPath, "destination unreachable path recovery")
		if selected := selectProbeVirtualRouterBestPath(reachablePaths, true); len(selected) > 0 {
			storeProbeVirtualRouterRoutePath(sourceNodeID, targetNodeID, selected)
			log.Printf("probe virtual router path recovery selected: target=%s path=%s", targetNodeID, strings.Join(selected, ">"))
			return
		}
		log.Printf("probe virtual router path recovery found no reachable path: target=%s candidates=%d", targetNodeID, len(paths))
	}()
}

func probeVirtualRouterPathKey(path []string) string {
	clean := make([]string, 0, len(path))
	for _, item := range path {
		if nodeID := normalizeProbeRouteNodeID(item); nodeID != "" {
			clean = append(clean, nodeID)
		}
	}
	if len(clean) < 2 {
		return ""
	}
	return strings.Join(clean, ">")
}

func currentProbeVirtualRouterAdjacentLatencyMS(fromNodeID string, toNodeID string) (int64, bool) {
	localNodeID := currentProbeVirtualRouterLocalNodeID()
	from := normalizeProbeRouteNodeID(fromNodeID)
	to := normalizeProbeRouteNodeID(toNodeID)
	if localNodeID == "" || from == "" || to == "" {
		return 0, false
	}
	target := ""
	switch localNodeID {
	case from:
		target = to
	case to:
		target = from
	default:
		return 0, false
	}
	rt, _ := probeVirtualRouterRuntimeForAdjacentNode(target)
	if rt == nil {
		return 0, false
	}
	stats := snapshotProbeVirtualRouterRuntimeStats(rt.cfg.routeID)
	if stats == nil {
		return 0, false
	}
	if stats.LastPingLatencyMS > 0 && strings.TrimSpace(stats.LastPingError) == "" {
		return stats.LastPingLatencyMS, true
	}
	if stats.LastRemoteRTTMS > 0 && strings.TrimSpace(stats.LastRemoteRTTError) == "" {
		return stats.LastRemoteRTTMS, true
	}
	return 0, false
}

func compareProbeVirtualRouterPathLexicographic(left []string, right []string) int {
	limit := len(left)
	if len(right) < limit {
		limit = len(right)
	}
	for i := 0; i < limit; i++ {
		l := normalizeProbeRouteNodeID(left[i])
		r := normalizeProbeRouteNodeID(right[i])
		if l < r {
			return -1
		}
		if l > r {
			return 1
		}
	}
	if len(left) < len(right) {
		return -1
	}
	if len(left) > len(right) {
		return 1
	}
	return 0
}

func probeVirtualRouterNodeIDForIP(config probeVirtualRouterConfig, ip string) string {
	target := net.ParseIP(strings.TrimSpace(ip)).To4()
	if target == nil {
		return ""
	}
	targetText := target.String()
	for _, item := range config.ProbeIPs {
		itemIP := net.ParseIP(strings.TrimSpace(item.IP)).To4()
		if itemIP != nil && itemIP.String() == targetText {
			return normalizeProbeRouteNodeID(item.NodeID)
		}
	}
	return ""
}

func currentProbeVirtualRouterIPForNode(nodeID string) string {
	target := normalizeProbeRouteNodeID(nodeID)
	if target == "" {
		return ""
	}
	probeVirtualRouterState.mu.RLock()
	defer probeVirtualRouterState.mu.RUnlock()
	return probeVirtualRouterState.nodeToIP[target]
}

func currentProbeVirtualRouterNodeIDForIP(ip string) string {
	target := net.ParseIP(strings.TrimSpace(ip)).To4()
	if target == nil {
		return ""
	}
	probeVirtualRouterState.mu.RLock()
	defer probeVirtualRouterState.mu.RUnlock()
	return probeVirtualRouterState.ipToNode[target.String()]
}

func currentProbeVirtualRouterPathBetweenNodes(fromNodeID string, toNodeID string) []string {
	from := normalizeProbeRouteNodeID(fromNodeID)
	to := normalizeProbeRouteNodeID(toNodeID)
	if from == "" || to == "" {
		return nil
	}
	if from == to {
		return []string{from}
	}
	probeVirtualRouterState.mu.RLock()
	neighbors := probeVirtualRouterCloneNeighborsLocked()
	probeVirtualRouterState.mu.RUnlock()
	if path := cachedProbeVirtualRouterRoutePath(from, to); len(path) > 0 {
		if !probeVirtualRouterPathShouldAvoid(path) && !probeVirtualRouterPathHasAvailableSourceShortcut(neighbors, path) {
			return path
		}
		clearProbeVirtualRouterRouteCacheForPath(path, "cached path is unavailable")
	}
	path := probeVirtualRouterPathFromNeighbors(neighbors, from, to)
	if len(path) > 0 {
		storeProbeVirtualRouterRoutePath(from, to, path)
	}
	return path
}

func probeVirtualRouterRouteCacheKey(fromNodeID string, toNodeID string) string {
	from := normalizeProbeRouteNodeID(fromNodeID)
	to := normalizeProbeRouteNodeID(toNodeID)
	if from == "" || to == "" {
		return ""
	}
	return from + ">" + to
}

func cachedProbeVirtualRouterRoutePath(fromNodeID string, toNodeID string) []string {
	key := probeVirtualRouterRouteCacheKey(fromNodeID, toNodeID)
	if key == "" {
		return nil
	}
	probeVirtualRouterRouteCacheState.mu.RLock()
	path := append([]string(nil), probeVirtualRouterRouteCacheState.routes[key]...)
	probeVirtualRouterRouteCacheState.mu.RUnlock()
	return path
}

func storeProbeVirtualRouterRoutePath(fromNodeID string, toNodeID string, path []string) {
	key := probeVirtualRouterRouteCacheKey(fromNodeID, toNodeID)
	if key == "" || len(path) == 0 {
		return
	}
	probeVirtualRouterRouteCacheState.mu.Lock()
	if probeVirtualRouterRouteCacheState.routes == nil {
		probeVirtualRouterRouteCacheState.routes = make(map[string][]string)
	}
	probeVirtualRouterRouteCacheState.routes[key] = append([]string(nil), path...)
	probeVirtualRouterRouteCacheState.mu.Unlock()
}

func clearProbeVirtualRouterRouteCache(reason string) {
	probeVirtualRouterRouteCacheState.mu.Lock()
	if len(probeVirtualRouterRouteCacheState.routes) == 0 {
		probeVirtualRouterRouteCacheState.mu.Unlock()
		return
	}
	probeVirtualRouterRouteCacheState.routes = make(map[string][]string)
	probeVirtualRouterRouteCacheState.mu.Unlock()
	if strings.TrimSpace(reason) != "" {
		log.Printf("probe virtual router route cache cleared: reason=%s", strings.TrimSpace(reason))
	}
}

func clearProbeVirtualRouterRouteCacheForRuntime(rt *probeVirtualRouterRuntime, reason string) {
	if rt == nil {
		clearProbeVirtualRouterRouteCache(reason)
		return
	}
	fromNodeID := normalizeProbeRouteNodeID(rt.cfg.fromNodeID)
	toNodeID := normalizeProbeRouteNodeID(rt.cfg.toNodeID)
	if fromNodeID == "" || toNodeID == "" || fromNodeID == toNodeID {
		clearProbeVirtualRouterRouteCache(reason)
		return
	}
	clearProbeVirtualRouterRouteCacheForEdge(fromNodeID, toNodeID, reason)
}

func clearProbeVirtualRouterRouteCacheForEdge(fromNodeID string, toNodeID string, reason string) {
	from := normalizeProbeRouteNodeID(fromNodeID)
	to := normalizeProbeRouteNodeID(toNodeID)
	if from == "" || to == "" || from == to {
		clearProbeVirtualRouterRouteCache(reason)
		return
	}
	removed := 0
	probeVirtualRouterRouteCacheState.mu.Lock()
	for key, path := range probeVirtualRouterRouteCacheState.routes {
		if probeVirtualRouterPathContainsAdjacentEdge(path, from, to) {
			delete(probeVirtualRouterRouteCacheState.routes, key)
			removed++
		}
	}
	probeVirtualRouterRouteCacheState.mu.Unlock()
	if removed > 0 && strings.TrimSpace(reason) != "" {
		log.Printf("probe virtual router route cache entries cleared: reason=%s edge=%s>%s count=%d", strings.TrimSpace(reason), from, to, removed)
	}
}

func clearProbeVirtualRouterRouteCacheForPath(path []string, reason string) {
	cleanPath := cleanProbeVirtualRouterPath(path)
	if len(cleanPath) < 2 {
		return
	}
	key := probeVirtualRouterRouteCacheKey(cleanPath[0], cleanPath[len(cleanPath)-1])
	if key == "" {
		return
	}
	probeVirtualRouterRouteCacheState.mu.Lock()
	_, removed := probeVirtualRouterRouteCacheState.routes[key]
	delete(probeVirtualRouterRouteCacheState.routes, key)
	probeVirtualRouterRouteCacheState.mu.Unlock()
	if removed && strings.TrimSpace(reason) != "" {
		log.Printf("probe virtual router route cache cleared: path=%s reason=%s", strings.Join(cleanPath, ">"), strings.TrimSpace(reason))
	}
}

func probeVirtualRouterPathContainsAdjacentEdge(path []string, fromNodeID string, toNodeID string) bool {
	from := normalizeProbeRouteNodeID(fromNodeID)
	to := normalizeProbeRouteNodeID(toNodeID)
	if from == "" || to == "" || from == to || len(path) < 2 {
		return false
	}
	for index := 0; index+1 < len(path); index++ {
		left := normalizeProbeRouteNodeID(path[index])
		right := normalizeProbeRouteNodeID(path[index+1])
		if (left == from && right == to) || (left == to && right == from) {
			return true
		}
	}
	return false
}

func currentProbeVirtualRouterPathToIP(ip string) []string {
	targetIP := net.ParseIP(strings.TrimSpace(ip)).To4()
	if targetIP == nil {
		return nil
	}
	probeVirtualRouterState.mu.RLock()
	nodeID := strings.TrimSpace(probeVirtualRouterState.localNodeID)
	targetNodeID := probeVirtualRouterState.ipToNode[targetIP.String()]
	probeVirtualRouterState.mu.RUnlock()
	if targetNodeID == "" {
		if entry, ok := currentProbeVirtualRouterFakeIPEntryByIP(targetIP.String()); ok {
			targetNodeID = normalizeProbeRouteNodeID(entry.ExitNodeID)
		}
	}
	if targetNodeID == "" {
		if rule, ok := currentProbeVirtualRouterRouteRuleForIP(targetIP.String()); ok && sanitizeProbeVirtualRouterRouteRuleAction(rule.Action, rule.ExitNodeID) == "probe_exit" {
			targetNodeID = normalizeProbeRouteNodeID(rule.ExitNodeID)
		}
	}
	return currentProbeVirtualRouterPathBetweenNodes(nodeID, targetNodeID)
}

func currentProbeVirtualRouterPathForPacket(packet []byte, dstIP string) []string {
	sourceIP := net.ParseIP(probeVirtualRouterIPv4Source(packet)).To4()
	targetIP := net.ParseIP(strings.TrimSpace(dstIP)).To4()
	if sourceIP == nil || targetIP == nil {
		return nil
	}
	probeVirtualRouterState.mu.RLock()
	nodeID := strings.TrimSpace(probeVirtualRouterState.localNodeID)
	sourceNodeID := probeVirtualRouterState.ipToNode[sourceIP.String()]
	targetNodeID := probeVirtualRouterState.ipToNode[targetIP.String()]
	probeVirtualRouterState.mu.RUnlock()
	if targetNodeID == "" {
		if entry, ok := currentProbeVirtualRouterFakeIPEntryByIP(targetIP.String()); ok {
			targetNodeID = normalizeProbeRouteNodeID(entry.ExitNodeID)
		}
	}
	if targetNodeID == "" {
		if rule, ok := currentProbeVirtualRouterRouteRuleForIP(targetIP.String()); ok && sanitizeProbeVirtualRouterRouteRuleAction(rule.Action, rule.ExitNodeID) == "probe_exit" {
			targetNodeID = normalizeProbeRouteNodeID(rule.ExitNodeID)
		}
	}
	if nodeID == "" {
		nodeID = sourceNodeID
	}
	return currentProbeVirtualRouterPathBetweenNodes(nodeID, targetNodeID)
}

func probeVirtualRouterPacketTargetsLocalIP(runtime *probeVirtualRouterRuntime, dstIP string) bool {
	return probeVirtualRouterIPMatches(dstIP, currentProbeVirtualRouterLocalIPForRuntime(runtime))
}

func probeVirtualRouterPacketTargetsLocalDelivery(runtime *probeVirtualRouterRuntime, dstIP string, path []string) bool {
	if probeVirtualRouterPacketTargetsLocalIP(runtime, dstIP) {
		return true
	}
	if probeProductTargetsLocalDelivery(dstIP) {
		localNodeID := currentProbeVirtualRouterLocalNodeIDForRuntime(runtime)
		return localNodeID != "" && (len(path) == 0 || normalizeProbeRouteNodeID(path[len(path)-1]) == localNodeID)
	}
	entry, ok := currentProbeVirtualRouterFakeIPEntryByIP(dstIP)
	if !ok {
		return false
	}
	localNodeID := currentProbeVirtualRouterLocalNodeIDForRuntime(runtime)
	if normalizeProbeRouteNodeID(entry.ExitNodeID) != localNodeID || localNodeID == "" {
		return false
	}
	return len(path) == 0 || normalizeProbeRouteNodeID(path[len(path)-1]) == localNodeID
}

func probeVirtualRouterIPMatches(target string, local string) bool {
	localIP := net.ParseIP(strings.TrimSpace(local)).To4()
	targetIP := net.ParseIP(strings.TrimSpace(target)).To4()
	if localIP == nil || targetIP == nil {
		return false
	}
	return targetIP.Equal(localIP)
}

func marshalProbeVirtualRouterFrameEnvelope(frame probeVirtualRouterFrame) ([]byte, error) {
	return encodeProbeVirtualRouterFrame(frame)
}

func buildProbeVirtualRouterBusinessFrame(mainType uint16, subType uint16, payload []byte, path []string, trace []probeVirtualRouterFrameTraceHop) (probeVirtualRouterFrame, error) {
	controlPayload, err := marshalProbeVirtualRouterFrameControl(path, trace)
	if err != nil {
		return probeVirtualRouterFrame{}, err
	}
	return probeVirtualRouterFrame{
		MainType: mainType,
		SubType:  subType,
		Control:  controlPayload,
		Data:     append([]byte(nil), payload...),
	}, nil
}

func buildProbeVirtualRouterIPFrame(packet []byte, path []string, trace []probeVirtualRouterFrameTraceHop) (probeVirtualRouterFrame, error) {
	if len(packet) == 0 {
		return probeVirtualRouterFrame{}, errors.New("virtual router ip frame payload is empty")
	}
	return buildProbeVirtualRouterBusinessFrame(probeVirtualRouterFrameMainTypeIP, probeVirtualRouterIPSubTypeIPv4, packet, path, trace)
}

func marshalProbeVirtualRouterFrameControl(path []string, trace []probeVirtualRouterFrameTraceHop) ([]byte, error) {
	cleanPath := cleanProbeVirtualRouterPath(path)
	cleanTrace := make([]probeVirtualRouterFrameTraceHop, 0, len(trace))
	for _, hop := range trace {
		clean := probeVirtualRouterCleanFrameTraceHop(hop)
		if clean.ID == "" || clean.NodeID == "" || clean.Event == "" || clean.UnixNano <= 0 {
			continue
		}
		cleanTrace = append(cleanTrace, clean)
	}
	payload, err := json.Marshal(probeVirtualRouterFrameControlEnvelope{
		Path:  cleanPath,
		Trace: cleanTrace,
	})
	if err != nil {
		return nil, err
	}
	if len(payload) > probeVirtualRouterFrameMaxControlBytes {
		return nil, fmt.Errorf("virtual router frame control is too large: %d", len(payload))
	}
	return payload, nil
}

func encodeProbeVirtualRouterFrame(frame probeVirtualRouterFrame) ([]byte, error) {
	controlLen := len(frame.Control)
	dataLen := len(frame.Data)
	if controlLen > probeVirtualRouterFrameMaxControlBytes {
		return nil, fmt.Errorf("virtual router frame control is too large: %d", controlLen)
	}
	if dataLen > probeVirtualRouterFrameMaxDataBytes {
		return nil, fmt.Errorf("virtual router frame data is too large: %d", dataLen)
	}
	frameLen := probeVirtualRouterFrameEnvelopeHeaderSize + controlLen + dataLen

	out := make([]byte, frameLen)
	binary.BigEndian.PutUint16(out[0:2], probeVirtualRouterFrameEnvelopeMagic)
	binary.BigEndian.PutUint16(out[2:4], frame.MainType)
	binary.BigEndian.PutUint16(out[4:6], frame.SubType)
	binary.BigEndian.PutUint16(out[6:8], uint16(controlLen))
	binary.BigEndian.PutUint16(out[8:10], uint16(dataLen))
	offset := probeVirtualRouterFrameEnvelopeHeaderSize
	copy(out[offset:offset+controlLen], frame.Control)
	copy(out[offset+controlLen:], frame.Data)
	checksum := probeVirtualRouterFrameChecksum(out[:10], frame.Control, frame.Data)
	binary.BigEndian.PutUint16(out[10:12], checksum)
	return out, nil
}

func unmarshalProbeVirtualRouterFrameEnvelope(payload []byte) (probeVirtualRouterFrame, error) {
	return decodeProbeVirtualRouterFrame(payload)
}

func decodeProbeVirtualRouterFrame(payload []byte) (probeVirtualRouterFrame, error) {
	if len(payload) < probeVirtualRouterFrameEnvelopeHeaderSize {
		return probeVirtualRouterFrame{}, errors.New("invalid virtual router frame envelope")
	}
	frame, controlLen, dataLen, payloadLen, err := decodeProbeVirtualRouterFrameHeader(payload[:probeVirtualRouterFrameEnvelopeHeaderSize])
	if err != nil {
		return probeVirtualRouterFrame{}, err
	}
	if len(payload) != probeVirtualRouterFrameEnvelopeHeaderSize+payloadLen {
		return probeVirtualRouterFrame{}, errors.New("invalid virtual router frame envelope")
	}
	controlPayload := payload[probeVirtualRouterFrameEnvelopeHeaderSize : probeVirtualRouterFrameEnvelopeHeaderSize+controlLen]
	dataPayload := payload[probeVirtualRouterFrameEnvelopeHeaderSize+controlLen:]
	if err := verifyProbeVirtualRouterFrameChecksum(payload[:probeVirtualRouterFrameEnvelopeHeaderSize], controlPayload, dataPayload); err != nil {
		return probeVirtualRouterFrame{}, err
	}
	frame.Control = controlPayload
	frame.Data = dataPayload
	if len(dataPayload) != dataLen {
		return probeVirtualRouterFrame{}, errors.New("invalid virtual router frame envelope")
	}
	return frame, nil
}

func decodeProbeVirtualRouterFrameHeader(header []byte) (probeVirtualRouterFrame, int, int, int, error) {
	if len(header) != probeVirtualRouterFrameEnvelopeHeaderSize {
		return probeVirtualRouterFrame{}, 0, 0, 0, errors.New("invalid virtual router frame header")
	}
	if binary.BigEndian.Uint16(header[0:2]) != probeVirtualRouterFrameEnvelopeMagic {
		return probeVirtualRouterFrame{}, 0, 0, 0, errors.New("invalid virtual router frame magic")
	}
	controlLen := int(binary.BigEndian.Uint16(header[6:8]))
	dataLen := int(binary.BigEndian.Uint16(header[8:10]))
	if controlLen > probeVirtualRouterFrameMaxControlBytes {
		return probeVirtualRouterFrame{}, 0, 0, 0, fmt.Errorf("virtual router frame control is too large: %d", controlLen)
	}
	if dataLen > probeVirtualRouterFrameMaxDataBytes {
		return probeVirtualRouterFrame{}, 0, 0, 0, fmt.Errorf("virtual router frame data is too large: %d", dataLen)
	}
	if probeVirtualRouterFrameEnvelopeHeaderSize+controlLen+dataLen > probeVirtualRouterFrameMaxBytes {
		return probeVirtualRouterFrame{}, 0, 0, 0, errors.New("invalid virtual router frame envelope")
	}
	return probeVirtualRouterFrame{
		MainType: binary.BigEndian.Uint16(header[2:4]),
		SubType:  binary.BigEndian.Uint16(header[4:6]),
	}, controlLen, dataLen, controlLen + dataLen, nil
}

func verifyProbeVirtualRouterFrameChecksum(header []byte, controlPayload []byte, dataPayload []byte) error {
	if len(header) != probeVirtualRouterFrameEnvelopeHeaderSize {
		return errors.New("invalid virtual router frame header")
	}
	checksum := binary.BigEndian.Uint16(header[10:12])
	expected := probeVirtualRouterFrameChecksum(header[:10], controlPayload, dataPayload)
	if checksum != expected {
		return errors.New("virtual router frame checksum mismatch")
	}
	return nil
}

func probeVirtualRouterFrameChecksum(headerPrefix []byte, controlPayload []byte, dataPayload []byte) uint16 {
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
	add(controlPayload)
	add(dataPayload)
	if hasPending {
		sum += uint32(pending) << 8
	}
	for sum > 0xffff {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return ^uint16(sum)
}

func probeVirtualRouterFrameControl(frame probeVirtualRouterFrame, fallbackPath []string) (probeVirtualRouterFrameControlEnvelope, error) {
	control := probeVirtualRouterFrameControlEnvelope{}
	if len(frame.Control) > 0 {
		if err := json.Unmarshal(frame.Control, &control); err != nil {
			return probeVirtualRouterFrameControlEnvelope{}, fmt.Errorf("invalid virtual router frame control: %w", err)
		}
	}
	if len(control.Path) == 0 {
		control.Path = append([]string(nil), fallbackPath...)
	}
	return control, nil
}

func setProbeVirtualRouterFrameControl(frame *probeVirtualRouterFrame, control probeVirtualRouterFrameControlEnvelope) error {
	if frame == nil {
		return errors.New("virtual router frame is nil")
	}
	payload, err := json.Marshal(control)
	if err != nil {
		return err
	}
	if len(payload) > probeVirtualRouterFrameMaxControlBytes {
		return fmt.Errorf("virtual router frame control is too large: %d", len(payload))
	}
	frame.Control = payload
	return nil
}

func appendProbeVirtualRouterWireFrameICMPTrace(frame probeVirtualRouterFrame, runtime *probeVirtualRouterRuntime, fallbackPath []string, event string) probeVirtualRouterFrame {
	if _, ok := probeVirtualRouterParseICMPEchoLogInfo(frame.Data); !ok {
		return frame
	}
	control, err := probeVirtualRouterFrameControl(frame, fallbackPath)
	if err != nil {
		return frame
	}
	control.Trace = appendProbeVirtualRouterICMPTrace(control.Trace, runtime, event, "", "")
	if err := setProbeVirtualRouterFrameControl(&frame, control); err != nil {
		return frame
	}
	return frame
}

func probeVirtualRouterWireFramePathString(frame probeVirtualRouterFrame, fallbackPath []string) string {
	control, err := probeVirtualRouterFrameControl(frame, fallbackPath)
	if err != nil {
		return ""
	}
	return strings.Join(control.Path, ">")
}

func marshalProbeVirtualRouterFrameTrace(trace []probeVirtualRouterFrameTraceHop) ([]byte, error) {
	if len(trace) == 0 {
		return nil, nil
	}
	out := make([]probeVirtualRouterFrameTraceHop, 0, len(trace))
	for _, hop := range trace {
		clean := probeVirtualRouterCleanFrameTraceHop(hop)
		if clean.ID == "" || clean.NodeID == "" || clean.Event == "" || clean.UnixNano <= 0 {
			continue
		}
		out = append(out, clean)
	}
	if len(out) == 0 {
		return nil, nil
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	if len(raw) > 0xffff {
		return nil, errors.New("virtual router frame trace is too large")
	}
	return raw, nil
}

func unmarshalProbeVirtualRouterFrameTrace(raw []byte) ([]probeVirtualRouterFrameTraceHop, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var items []probeVirtualRouterFrameTraceHop
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("invalid virtual router frame trace: %w", err)
	}
	out := make([]probeVirtualRouterFrameTraceHop, 0, len(items))
	for _, item := range items {
		clean := probeVirtualRouterCleanFrameTraceHop(item)
		if clean.ID == "" || clean.NodeID == "" || clean.Event == "" || clean.UnixNano <= 0 {
			continue
		}
		out = append(out, clean)
	}
	return out, nil
}

func probeVirtualRouterCleanFrameTraceHop(input probeVirtualRouterFrameTraceHop) probeVirtualRouterFrameTraceHop {
	return probeVirtualRouterFrameTraceHop{
		ID:         strings.TrimSpace(input.ID),
		NodeID:     normalizeProbeRouteNodeID(input.NodeID),
		RouteID:    strings.TrimSpace(input.RouteID),
		Event:      strings.TrimSpace(input.Event),
		Direction:  strings.TrimSpace(input.Direction),
		RemoteNode: normalizeProbeRouteNodeID(input.RemoteNode),
		UnixNano:   input.UnixNano,
	}
}

func appendProbeVirtualRouterICMPTrace(trace []probeVirtualRouterFrameTraceHop, runtime *probeVirtualRouterRuntime, event string, direction string, remoteNodeID string) []probeVirtualRouterFrameTraceHop {
	cleanEvent := strings.TrimSpace(event)
	nodeID := currentProbeVirtualRouterLocalNodeIDForRuntime(runtime)
	if cleanEvent == "" || nodeID == "" {
		return trace
	}
	routeID := ""
	if runtime != nil {
		routeID = strings.TrimSpace(runtime.cfg.routeID)
	}
	item := probeVirtualRouterFrameTraceHop{
		ID:         newProbeTCPDebugFlowID("vrouter_icmp_trace", nodeID),
		NodeID:     nodeID,
		RouteID:    routeID,
		Event:      cleanEvent,
		Direction:  strings.TrimSpace(direction),
		RemoteNode: normalizeProbeRouteNodeID(remoteNodeID),
		UnixNano:   time.Now().UnixNano(),
	}
	return append(append([]probeVirtualRouterFrameTraceHop(nil), trace...), item)
}

func probeVirtualRouterICMPTraceString(trace []probeVirtualRouterFrameTraceHop) string {
	if len(trace) == 0 {
		return ""
	}
	parts := make([]string, 0, len(trace))
	var firstUnixNano int64
	for i, hop := range trace {
		clean := probeVirtualRouterCleanFrameTraceHop(hop)
		if clean.ID == "" || clean.NodeID == "" || clean.Event == "" || clean.UnixNano <= 0 {
			continue
		}
		at := time.Unix(0, clean.UnixNano).UTC().Format(time.RFC3339Nano)
		if firstUnixNano == 0 {
			firstUnixNano = clean.UnixNano
		}
		sinceStart := "0"
		if clean.UnixNano >= firstUnixNano {
			sinceStart = fmt.Sprintf("%d", (clean.UnixNano-firstUnixNano)/int64(time.Millisecond))
		} else {
			sinceStart = "clock_skew"
		}
		parts = append(parts, fmt.Sprintf("%02d node=%s event=%s direction=%s remote=%s route=%s at=%s since_start_ref_ms=%s id=%s", i, clean.NodeID, clean.Event, clean.Direction, clean.RemoteNode, clean.RouteID, at, sinceStart, clean.ID))
	}
	return strings.Join(parts, " | ")
}

func parseProbeVirtualRouterPathText(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ">")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		nodeID := normalizeProbeRouteNodeID(part)
		if nodeID != "" {
			out = append(out, nodeID)
		}
	}
	return out
}

func writeProbeVirtualRouterIPFrame(link *probeVirtualRouterFrameLink, packet []byte, path []string, trace []probeVirtualRouterFrameTraceHop) error {
	if link == nil {
		return errors.New("virtual router frame link is nil")
	}
	frame, err := buildProbeVirtualRouterIPFrame(packet, path, trace)
	if err != nil {
		return err
	}
	return link.EnqueueProbeVirtualRouterFrame(frame)
}

func writeProbeVirtualRouterIPFrameUntil(link *probeVirtualRouterFrameLink, packet []byte, path []string, trace []probeVirtualRouterFrameTraceHop, deadline time.Time) error {
	if link == nil {
		return errors.New("virtual router frame link is nil")
	}
	frame, err := buildProbeVirtualRouterIPFrame(packet, path, trace)
	if err != nil {
		return err
	}
	return link.EnqueueProbeVirtualRouterFrameUntil(frame, deadline)
}

func writeProbeVirtualRouterWireFrameRaw(writer io.Writer, frame probeVirtualRouterFrame) error {
	return writeProbeVirtualRouterWireFramesRaw(writer, []probeVirtualRouterFrame{frame})
}

func writeProbeVirtualRouterWireFramesRaw(writer io.Writer, frames []probeVirtualRouterFrame) error {
	if len(frames) == 0 {
		return nil
	}
	totalBytes := 0
	encoded := make([][]byte, 0, len(frames))
	for _, frame := range frames {
		payload, err := encodeProbeVirtualRouterFrame(frame)
		if err != nil {
			return err
		}
		encoded = append(encoded, payload)
		totalBytes += len(payload)
	}
	payload := make([]byte, 0, totalBytes)
	for _, framePayload := range encoded {
		payload = append(payload, framePayload...)
	}
	if deadlineWriter, ok := writer.(interface{ SetWriteDeadline(time.Time) error }); ok && probeVirtualRouterFrameWriteTimeout > 0 {
		defer func() {
			_ = deadlineWriter.SetWriteDeadline(time.Time{})
		}()
		return writeProbeVirtualRouterAllWithWriteIdleTimeout(writer, deadlineWriter, payload, probeVirtualRouterFrameWriteTimeout)
	}
	return writeProbeVirtualRouterAll(writer, payload)
}

func readProbeVirtualRouterWireFrame(reader *bufio.Reader) (probeVirtualRouterFrame, error) {
	header := make([]byte, probeVirtualRouterFrameEnvelopeHeaderSize)
	if _, err := io.ReadFull(reader, header); err != nil {
		return probeVirtualRouterFrame{}, err
	}
	frame, controlLen, dataLen, payloadLen, err := decodeProbeVirtualRouterFrameHeader(header)
	if err != nil {
		return probeVirtualRouterFrame{}, err
	}
	payload := make([]byte, payloadLen)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return probeVirtualRouterFrame{}, err
	}
	controlPayload := payload[:controlLen]
	dataPayload := payload[controlLen : controlLen+dataLen]
	if err := verifyProbeVirtualRouterFrameChecksum(header, controlPayload, dataPayload); err != nil {
		return probeVirtualRouterFrame{}, err
	}
	frame.Control = controlPayload
	frame.Data = dataPayload
	return frame, nil
}

func writeProbeVirtualRouterAll(writer io.Writer, payload []byte) error {
	written := 0
	for written < len(payload) {
		n, err := writer.Write(payload[written:])
		if n > 0 {
			written += n
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

func writeProbeVirtualRouterAllWithWriteIdleTimeout(writer io.Writer, deadlineWriter interface{ SetWriteDeadline(time.Time) error }, payload []byte, idleTimeout time.Duration) error {
	written := 0
	for written < len(payload) {
		if idleTimeout > 0 {
			if err := deadlineWriter.SetWriteDeadline(time.Now().Add(idleTimeout)); err != nil {
				return err
			}
		}
		n, err := writer.Write(payload[written:])
		if n > 0 {
			written += n
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

func handleProbeVirtualRouterTUNPacket(packet []byte) bool {
	dstIP := probeVirtualRouterIPv4Destination(packet)
	if dstIP == "" {
		return false
	}
	if probeVirtualRouterShouldDropNonUnicastDestination(dstIP) {
		return false
	}
	if handleProbeVirtualRouterLocalSelfTUNPacket(packet, dstIP) {
		return true
	}
	path := currentProbeVirtualRouterPathForPacket(packet, dstIP)
	if !probeVirtualRouterLocalEntryEnabled() && !probeVirtualRouterTUNPacketAllowedWhenEntryDisabled(dstIP, path) && !probeProductAllowsForwardedTUNPacket(packet, dstIP, path) {
		return false
	}
	if probeProductRejectsTUNPacket(packet, dstIP, path) {
		recordProbeVirtualRouterRecentPacket("tun_rx", "reject_sni", nil, packet, path, false, nil)
		return true
	}
	if len(path) < 2 && probeVirtualRouterIPInCurrentFakeCIDR(dstIP) {
		scheduleProbeVirtualRouterFakeIPItemRefreshByIP(dstIP)
	}
	maybeScheduleProbeVirtualRouterFakeIPVerifyForTCPRetransmit(packet, path)
	if info, ok := probeVirtualRouterParseICMPEchoLogInfo(packet); ok {
		log.Printf("probe virtual router icmp tun rx: trace_code=icmp-trace-v2 kind=%s src=%s dst=%s id=%d seq=%d local_node=%s path=%s bytes=%d", info.Kind, info.SourceIP, info.DestinationIP, info.ID, info.Sequence, currentProbeVirtualRouterLocalNodeID(), strings.Join(path, ">"), len(packet))
	}
	if len(path) < 2 {
		if probeProductHandleDirectTUNPacket(packet, dstIP) {
			recordProbeVirtualRouterRecentPacket("tun_rx", "direct_reinject", nil, packet, path, false, nil)
			return true
		}
		if handled, directErr := probeVirtualRouterEnsureDirectBypassForOrdinaryTarget(packet, dstIP); handled {
			action := "direct"
			if directErr != nil {
				action = "direct_error"
			}
			recordProbeVirtualRouterRecentPacket("tun_rx", action, nil, packet, path, false, directErr)
			return false
		}
		if info, ok := probeVirtualRouterParseICMPEchoLogInfo(packet); ok {
			log.Printf("probe virtual router icmp tun drop: kind=%s src=%s dst=%s id=%d seq=%d reason=path_unavailable local_node=%s", info.Kind, info.SourceIP, info.DestinationIP, info.ID, info.Sequence, currentProbeVirtualRouterLocalNodeID())
		}
		if info, ok := probeVirtualRouterParseTCPUDPLogInfo(packet); ok {
			if shouldLog, suppressed := takeProbeVirtualRouterTransportTUNDropLogThrottle(info, "path_unavailable", time.Now()); shouldLog {
				log.Printf("probe virtual router transport tun drop: proto=%s src=%s:%d dst=%s:%d reason=path_unavailable local_node=%s suppressed=%d", info.Protocol, info.SourceIP, info.SourcePort, info.DestinationIP, info.DestinationPort, currentProbeVirtualRouterLocalNodeID(), suppressed)
			}
		}
		recordProbeVirtualRouterRecentPacket("tun_rx", "drop", nil, packet, path, false, errors.New("path unavailable"))
		return false
	}
	if err := probeVirtualRouterFakeIPForwardUnavailableError(dstIP, path, currentProbeVirtualRouterLocalNodeID()); err != nil {
		if info, ok := probeVirtualRouterParseICMPEchoLogInfo(packet); ok {
			log.Printf("probe virtual router icmp tun drop: kind=%s src=%s dst=%s id=%d seq=%d reason=fake_ip_exit_unreachable local_node=%s path=%s err=%v", info.Kind, info.SourceIP, info.DestinationIP, info.ID, info.Sequence, currentProbeVirtualRouterLocalNodeID(), strings.Join(path, ">"), err)
		}
		if info, ok := probeVirtualRouterParseTCPUDPLogInfo(packet); ok {
			if shouldLog, suppressed := takeProbeVirtualRouterTransportTUNDropLogThrottle(info, "fake_ip_exit_unreachable", time.Now()); shouldLog {
				log.Printf("probe virtual router transport tun drop: proto=%s src=%s:%d dst=%s:%d reason=fake_ip_exit_unreachable local_node=%s path=%s suppressed=%d err=%v", info.Protocol, info.SourceIP, info.SourcePort, info.DestinationIP, info.DestinationPort, currentProbeVirtualRouterLocalNodeID(), strings.Join(path, ">"), suppressed, err)
			}
		}
		recordProbeVirtualRouterRecentPacket("tun_rx", "drop", nil, packet, path, false, err)
		scheduleProbeVirtualRouterFakeIPVerifyForPacket(packet, path, "exit_unreachable")
		return false
	}
	if info, ok := probeVirtualRouterParseICMPEchoLogInfo(packet); ok && info.Kind == "echo_request" && probeVirtualRouterIPMatches(info.SourceIP, currentProbeVirtualRouterLocalIP()) {
		recordProbeVirtualRouterICMPPingStart(info, path)
	}
	trace := []probeVirtualRouterFrameTraceHop(nil)
	if _, ok := probeVirtualRouterParseICMPEchoLogInfo(packet); ok {
		trace = appendProbeVirtualRouterICMPTrace(trace, nil, "tun_rx", "", "")
	}
	if err := forwardProbeVirtualRouterPacketAlongPath(packet, dstIP, path, trace); err != nil {
		log.Printf("probe virtual router frame forward failed: dst=%s path=%s err=%v", dstIP, strings.Join(path, ">"), err)
		recordProbeVirtualRouterRecentPacket("tun_rx", "forward_error", nil, packet, path, false, err)
		scheduleProbeVirtualRouterFakeIPVerifyForPacket(packet, path, "forward_error")
		return true
	}
	recordProbeVirtualRouterRecentPacket("tun_rx", "forward", nil, packet, path, false, nil)
	return true
}

func takeProbeVirtualRouterTransportTUNDropLogThrottle(info probeVirtualRouterTransportLogInfo, reason string, now time.Time) (bool, int) {
	key := fmt.Sprintf(
		"transport_tun_drop|%s|%s:%d|%s:%d|%s",
		strings.ToLower(strings.TrimSpace(info.Protocol)),
		strings.TrimSpace(info.SourceIP),
		info.SourcePort,
		strings.TrimSpace(info.DestinationIP),
		info.DestinationPort,
		strings.ToLower(strings.TrimSpace(reason)),
	)
	return takeProbeVirtualRouterLogThrottle(key, probeVirtualRouterDiagnosticLogPeriod, now)
}

func probeVirtualRouterEnsureDirectBypassForOrdinaryTarget(packet []byte, dstIP string) (bool, error) {
	if !probeVirtualRouterLocalEntryEnabled() || probeVirtualRouterIPInCurrentFakeCIDR(dstIP) {
		return false, nil
	}
	targetIP := net.ParseIP(strings.TrimSpace(dstIP)).To4()
	if targetIP == nil {
		return false, nil
	}
	probeVirtualRouterState.mu.RLock()
	_, isVirtualNodeIP := probeVirtualRouterState.ipToNode[targetIP.String()]
	probeVirtualRouterState.mu.RUnlock()
	if isVirtualNodeIP {
		return false, nil
	}
	if rule, ok := currentProbeVirtualRouterRouteRuleForIP(targetIP.String()); ok && sanitizeProbeVirtualRouterRouteRuleAction(rule.Action, rule.ExitNodeID) == "probe_exit" {
		return false, nil
	}
	targetAddr := probeVirtualRouterDirectBypassTargetAddr(packet, targetIP.String())
	if targetAddr == "" {
		return false, nil
	}
	if err := probeVirtualRouterEnsureDirectBypass(targetAddr); err != nil {
		log.Printf("probe virtual router direct bypass route failed: dst=%s target=%s err=%v", targetIP.String(), targetAddr, err)
		return true, err
	}
	return true, nil
}

func probeVirtualRouterDirectBypassTargetAddr(packet []byte, dstIP string) string {
	if info, ok := probeVirtualRouterParseTCPUDPLogInfo(packet); ok && info.DestinationPort > 0 {
		return net.JoinHostPort(strings.TrimSpace(dstIP), strconv.Itoa(int(info.DestinationPort)))
	}
	return net.JoinHostPort(strings.TrimSpace(dstIP), "0")
}

func probeVirtualRouterFakeIPForwardUnavailableError(dstIP string, path []string, localNodeID string) error {
	if len(path) < 2 {
		return nil
	}
	if _, ok := currentProbeVirtualRouterFakeIPEntryByIP(dstIP); !ok {
		return nil
	}
	localNodeID = normalizeProbeRouteNodeID(localNodeID)
	if localNodeID == "" {
		localNodeID = currentProbeVirtualRouterLocalNodeID()
	}
	nextNodeID := probeVirtualRouterNextHopInPath(path, localNodeID)
	if nextNodeID == "" {
		return fmt.Errorf("fake ip exit unreachable: next hop is unavailable path=%s", strings.Join(path, ">"))
	}
	rt, direction := probeVirtualRouterRuntimeForAdjacentNode(nextNodeID)
	if rt == nil {
		return fmt.Errorf("fake ip exit unreachable: adjacent runtime is unavailable next=%s path=%s", nextNodeID, strings.Join(path, ">"))
	}
	if !probeVirtualRouterRuntimeHasBridgeSession(rt, direction) {
		return fmt.Errorf("fake ip exit unreachable: physical carrier unavailable route=%s next=%s direction=%s path=%s", probeVirtualRouterRuntimeLogRouteID(rt), nextNodeID, normalizeProbeRouteBridgeRole(direction), strings.Join(path, ">"))
	}
	return nil
}

func probeVirtualRouterTUNPacketAllowedWhenEntryDisabled(dstIP string, path []string) bool {
	if probeVirtualRouterIPInCurrentFakeCIDR(dstIP) {
		return true
	}
	if len(path) >= 2 {
		return true
	}
	ip := net.ParseIP(strings.TrimSpace(dstIP)).To4()
	if ip == nil {
		return false
	}
	probeVirtualRouterState.mu.RLock()
	_, ok := probeVirtualRouterState.ipToNode[ip.String()]
	probeVirtualRouterState.mu.RUnlock()
	return ok
}

func handleProbeVirtualRouterLocalSelfTUNPacket(packet []byte, dstIP string) bool {
	localIP := currentProbeVirtualRouterLocalIP()
	if !probeVirtualRouterIPMatches(dstIP, localIP) {
		return false
	}
	reply, _, ok := buildProbeVirtualRouterICMPEchoReply(packet, localIP)
	if !ok {
		return false
	}
	writer := probeVirtualRouterLocalTUNPacketWriter
	if writer == nil {
		writer = writeProbeVirtualRouterLocalTUNPacket
	}
	if err := writer(reply); err != nil {
		log.Printf("probe virtual router local self icmp reply failed: local_ip=%s err=%v", localIP, err)
		return true
	}
	if info, ok := probeVirtualRouterParseICMPEchoLogInfo(reply); ok {
		log.Printf("probe virtual router local self icmp reply ok: kind=%s src=%s dst=%s id=%d seq=%d local_ip=%s bytes=%d", info.Kind, info.SourceIP, info.DestinationIP, info.ID, info.Sequence, localIP, len(reply))
	}
	return true
}

func probeVirtualRouterIPInCurrentFakeCIDR(ipText string) bool {
	ip := net.ParseIP(strings.TrimSpace(ipText)).To4()
	if ip == nil {
		return false
	}
	_, network, err := net.ParseCIDR(currentProbeVirtualRouterFakeIPCIDR())
	return err == nil && network != nil && network.Contains(ip)
}

func probeVirtualRouterIPInReservedProbePool(ipText string) bool {
	ip := net.ParseIP(strings.TrimSpace(ipText)).To4()
	if ip == nil {
		return false
	}
	_, network, err := net.ParseCIDR(currentProbeVirtualRouterFakeIPCIDR())
	if err != nil || network == nil || network.IP.To4() == nil || !network.Contains(ip) {
		return false
	}
	base := binary.BigEndian.Uint32(network.IP.To4())
	value := binary.BigEndian.Uint32(ip)
	if value < base {
		return false
	}
	return uint64(value-base) <= uint64(probeVirtualRouterProbeIPPoolSize)
}

func refreshProbeVirtualRouterRouteConfigFromController(reason string) bool {
	identity, controllerBaseURL, ok := currentProbeVirtualRouterController()
	if !ok {
		return false
	}
	if cleanReason := strings.TrimSpace(reason); cleanReason != "" {
		log.Printf("probe virtual router route config sync start: reason=%s", cleanReason)
	}
	if err := syncProbeRouteConfig(identity, controllerBaseURL); err != nil {
		log.Printf("probe virtual router route config sync failed: reason=%s err=%v", strings.TrimSpace(reason), err)
		return false
	}
	return true
}

func scheduleProbeVirtualRouterRouteConfigRefreshFromController(reason string, minInterval time.Duration) bool {
	cleanReason := strings.TrimSpace(reason)
	if cleanReason == "" {
		cleanReason = "scheduled"
	}
	if minInterval <= 0 {
		minInterval = probeVirtualRouterRouteConfigRefreshHotPathMinInterval
	}
	now := time.Now()
	probeVirtualRouterRouteConfigRefreshState.mu.Lock()
	if probeVirtualRouterRouteConfigRefreshState.running[cleanReason] {
		probeVirtualRouterRouteConfigRefreshState.mu.Unlock()
		return false
	}
	if lastAt := probeVirtualRouterRouteConfigRefreshState.lastAt[cleanReason]; !lastAt.IsZero() && now.Sub(lastAt) < minInterval {
		probeVirtualRouterRouteConfigRefreshState.mu.Unlock()
		return false
	}
	probeVirtualRouterRouteConfigRefreshState.running[cleanReason] = true
	probeVirtualRouterRouteConfigRefreshState.lastAt[cleanReason] = now
	probeVirtualRouterRouteConfigRefreshState.mu.Unlock()

	go func() {
		defer func() {
			probeVirtualRouterRouteConfigRefreshState.mu.Lock()
			delete(probeVirtualRouterRouteConfigRefreshState.running, cleanReason)
			probeVirtualRouterRouteConfigRefreshState.mu.Unlock()
		}()
		refreshProbeVirtualRouterRouteConfigFromController(cleanReason)
	}()
	return true
}

func scheduleProbeVirtualRouterFakeIPItemRefreshByIP(fakeIP string) bool {
	cleanIP := ""
	if ip := net.ParseIP(strings.TrimSpace(fakeIP)).To4(); ip != nil {
		cleanIP = ip.String()
	}
	if cleanIP == "" {
		return false
	}
	if !probeVirtualRouterIPCanBeFakeIP(cleanIP) {
		return false
	}
	identity, controllerBaseURL, ok := currentProbeVirtualRouterController()
	if !ok {
		return false
	}
	now := time.Now()
	key := "ip|" + cleanIP
	probeVirtualRouterFakeIPItemRefreshState.mu.Lock()
	if probeVirtualRouterFakeIPItemRefreshState.running[key] {
		probeVirtualRouterFakeIPItemRefreshState.mu.Unlock()
		return false
	}
	if lastAt := probeVirtualRouterFakeIPItemRefreshState.lastAt[key]; !lastAt.IsZero() && now.Sub(lastAt) < time.Second {
		probeVirtualRouterFakeIPItemRefreshState.mu.Unlock()
		return false
	}
	probeVirtualRouterFakeIPItemRefreshState.running[key] = true
	probeVirtualRouterFakeIPItemRefreshState.lastAt[key] = now
	probeVirtualRouterFakeIPItemRefreshState.mu.Unlock()

	go func() {
		defer func() {
			probeVirtualRouterFakeIPItemRefreshState.mu.Lock()
			delete(probeVirtualRouterFakeIPItemRefreshState.running, key)
			probeVirtualRouterFakeIPItemRefreshState.mu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), probeRouteConfigSyncFetchTimeout)
		item, err := probeRequestRouteFakeIPByIP(ctx, controllerBaseURL, identity, cleanIP)
		cancel()
		if err != nil {
			logProbeWarnf("probe virtual router fake ip item refresh failed: fake_ip=%s err=%v", cleanIP, err)
			return
		}
		applyProbeVirtualRouterFakeIPEntry(item)
	}()
	return true
}

func probeVirtualRouterIPCanBeFakeIP(ip string) bool {
	cleanIP := ""
	if parsed := net.ParseIP(strings.TrimSpace(ip)).To4(); parsed != nil {
		cleanIP = parsed.String()
	}
	if cleanIP == "" || !probeVirtualRouterIPInCurrentFakeCIDR(cleanIP) {
		return false
	}
	if probeVirtualRouterIPInReservedProbePool(cleanIP) {
		return false
	}
	return currentProbeVirtualRouterNodeIDForIP(cleanIP) == ""
}

func scheduleProbeVirtualRouterFakeIPItemRefreshByDomain(identity nodeIdentity, controllerBaseURL string, domain string, rule probeVirtualRouterRouteRule) bool {
	cleanDomain := normalizeProbeVirtualRouterDomain(domain)
	if cleanDomain == "" {
		return false
	}
	if strings.TrimSpace(identity.NodeID) == "" || strings.TrimSpace(identity.Secret) == "" || strings.TrimSpace(controllerBaseURL) == "" {
		return false
	}
	now := time.Now()
	key := "domain|" + cleanDomain
	probeVirtualRouterFakeIPItemRefreshState.mu.Lock()
	if probeVirtualRouterFakeIPItemRefreshState.running[key] {
		probeVirtualRouterFakeIPItemRefreshState.mu.Unlock()
		return false
	}
	if lastAt := probeVirtualRouterFakeIPItemRefreshState.lastAt[key]; !lastAt.IsZero() && now.Sub(lastAt) < time.Second {
		probeVirtualRouterFakeIPItemRefreshState.mu.Unlock()
		return false
	}
	probeVirtualRouterFakeIPItemRefreshState.running[key] = true
	probeVirtualRouterFakeIPItemRefreshState.lastAt[key] = now
	probeVirtualRouterFakeIPItemRefreshState.mu.Unlock()

	go func() {
		defer func() {
			probeVirtualRouterFakeIPItemRefreshState.mu.Lock()
			delete(probeVirtualRouterFakeIPItemRefreshState.running, key)
			probeVirtualRouterFakeIPItemRefreshState.mu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), probeRouteConfigSyncFetchTimeout)
		item, _, err := probeRequestRouteFakeIP(ctx, controllerBaseURL, identity, cleanDomain, rule)
		cancel()
		if err != nil {
			logProbeWarnf("probe virtual router fake ip item allocate failed: domain=%s exit_node=%s err=%v", cleanDomain, strings.TrimSpace(rule.ExitNodeID), err)
			return
		}
		applyProbeVirtualRouterFakeIPEntry(item)
	}()
	return true
}

func recordProbeVirtualRouterFakeIPExitHit(domain string) {
	_ = domain
}

func probeVirtualRouterShouldDropNonUnicastDestination(dstIP string) bool {
	ip := net.ParseIP(strings.TrimSpace(dstIP)).To4()
	if ip == nil {
		return false
	}
	if ip.IsMulticast() || ip.Equal(net.IPv4bcast) {
		return true
	}
	// vRouter uses 198.18.0.0/15 for virtual addresses; 198.19.255.255 is the
	// subnet broadcast and must not enter point-to-point route selection.
	return ip[0] == 198 && ip[1] == 19 && ip[2] == 255 && ip[3] == 255
}

func startProbeVirtualRouterKeepAliveWorker(rt *probeVirtualRouterRuntime) {
	if rt == nil || !isProbeVirtualRouterRuntimeRouteID(rt.cfg.routeID) {
		return
	}
	go func() {
		timer := time.NewTimer(2 * time.Second)
		defer timer.Stop()
		for {
			select {
			case <-rt.stopCh:
				return
			case <-timer.C:
				probeVirtualRouterKeepAliveRuntime(rt)
				timer.Reset(probeVirtualRouterPingPongInterval)
			}
		}
	}()
}

func probeVirtualRouterKeepAliveRuntime(rt *probeVirtualRouterRuntime) {
	if rt == nil {
		return
	}
	if normalizeProbeRouteNodeID(rt.cfg.peerNodeID) == "" {
		clearProbeVirtualRouterRuntimePingError(rt.cfg.routeID)
		return
	}
	if !probeVirtualRouterRuntimeHasPhysicalBridgeSession(rt) {
		if rt.cfg.dialer {
			signalProbeVirtualRouterBridgeDialer(rt)
		}
		clearProbeVirtualRouterRuntimePingError(rt.cfg.routeID)
		return
	}
	if !rt.cfg.dialer {
		clearProbeVirtualRouterRuntimePingError(rt.cfg.routeID)
		return
	}
	probeVirtualRouterPingPongDirection(rt, probeRouteBridgeRoleToNext)
}

func probeVirtualRouterPingPongAllRuntimes() int {
	probeVirtualRouterRuntimeState.mu.RLock()
	runtimes := make([]*probeVirtualRouterRuntime, 0, len(probeVirtualRouterRuntimeState.runtimes))
	for _, rt := range probeVirtualRouterRuntimeState.runtimes {
		if rt == nil {
			continue
		}
		runtimes = append(runtimes, rt)
	}
	probeVirtualRouterRuntimeState.mu.RUnlock()

	var wg sync.WaitGroup
	for _, rt := range runtimes {
		wg.Add(1)
		go func(runtime *probeVirtualRouterRuntime) {
			defer wg.Done()
			probeVirtualRouterKeepAliveRuntime(runtime)
		}(rt)
	}
	wg.Wait()
	probeVirtualRouterQueryAllPathRTTs()
	return len(runtimes)
}

func probeVirtualRouterQueryAdjacentRTTRuntime(rt *probeVirtualRouterRuntime) {
	if rt == nil {
		return
	}
	targetNodeID := ""
	direction := ""
	if normalizeProbeRouteNodeID(rt.cfg.peerNodeID) != "" && rt.cfg.dialer {
		targetNodeID = normalizeProbeRouteNodeID(rt.cfg.peerNodeID)
		direction = probeRouteBridgeRoleToNext
	}
	if targetNodeID == "" {
		recordProbeVirtualRouterRuntimeRemoteRTTError(rt.cfg.routeID, errors.New("adjacent virtual router node is unavailable"))
		return
	}
	result, err := queryProbeVirtualRouterAdjacentPing(rt, direction, targetNodeID)
	if err != nil {
		recordProbeVirtualRouterRuntimeRemoteRTTError(rt.cfg.routeID, err)
		return
	}
	recordProbeVirtualRouterRuntimeRemoteRTTControlSuccess(rt.cfg.routeID, result.LatencyMS, result.Responder)
}

func probeVirtualRouterQueryPathRTT(path []string) (time.Duration, error) {
	localNodeID := currentProbeVirtualRouterLocalNodeID()
	if localNodeID == "" {
		return 0, errors.New("local virtual router node id is empty")
	}
	cleanPath := cleanProbeVirtualRouterPath(path)
	if len(cleanPath) < 2 || normalizeProbeRouteNodeID(cleanPath[0]) != localNodeID {
		return 0, errors.New("virtual router rtt query path must start at local node")
	}
	if err := validateProbeVirtualRouterForwardPath(cleanPath); err != nil {
		return 0, err
	}
	response, err := queryProbeVirtualRouterPathRTTControl(cleanPath)
	if err != nil {
		if recordProbeVirtualRouterPathRTTError(cleanPath, err) {
			scheduleProbeVirtualRouterPathRecovery(cleanPath)
		}
		return 0, err
	}
	if !response.OK {
		err = errors.New(strings.TrimSpace(response.Error))
		if err.Error() == "" {
			err = errors.New("virtual router rtt query failed")
		}
		if recordProbeVirtualRouterPathRTTError(cleanPath, err) {
			scheduleProbeVirtualRouterPathRecovery(cleanPath)
		}
		return 0, err
	}
	latency := time.Duration(response.LatencyMS) * time.Millisecond
	recordProbeVirtualRouterPathRTTSuccess(cleanPath, latency, response.Responder)
	return latency, nil
}

func probeVirtualRouterQueryAllPathRTTs() probeVirtualRouterPathRefreshResult {
	return probeVirtualRouterRefreshPathRTTs(probeVirtualRouterQueryPathRTT, false)
}

func probeVirtualRouterExploreAllPathRTTs() probeVirtualRouterPathRefreshResult {
	return probeVirtualRouterRefreshPathRTTs(probeVirtualRouterQueryPathRTT, true)
}

func probeVirtualRouterRefreshPathRTTs(query func([]string) (time.Duration, error), exploreFailures bool) probeVirtualRouterPathRefreshResult {
	result := probeVirtualRouterPathRefreshResult{}
	if query == nil {
		return result
	}
	localNodeID := currentProbeVirtualRouterLocalNodeID()
	if localNodeID == "" {
		return result
	}
	probeVirtualRouterState.mu.RLock()
	neighbors := probeVirtualRouterCloneNeighborsLocked()
	nodeToIP := probeVirtualRouterCloneNodeToIPLocked()
	probeVirtualRouterState.mu.RUnlock()
	nodeIDs := make([]string, 0, len(nodeToIP))
	for nodeID := range nodeToIP {
		clean := normalizeProbeRouteNodeID(nodeID)
		if clean != "" && clean != localNodeID {
			nodeIDs = append(nodeIDs, clean)
		}
	}
	sort.Strings(nodeIDs)
	shortestByTarget := make(map[string][][]string, len(nodeIDs))
	var shortestPaths [][]string
	for _, nodeID := range nodeIDs {
		paths := probeVirtualRouterShortestPathsFromNeighbors(neighbors, localNodeID, nodeID)
		shortestByTarget[nodeID] = paths
		shortestPaths = append(shortestPaths, paths...)
	}
	shortestReachable, queried := probeVirtualRouterQueryPathSet(shortestPaths, query)
	result.Queried += queried
	for _, nodeID := range nodeIDs {
		reachable := probeVirtualRouterReachablePathSubset(shortestByTarget[nodeID], shortestReachable)
		if selected := selectProbeVirtualRouterBestPath(reachable, true); len(selected) > 0 {
			clearProbeVirtualRouterRouteCacheForPath(selected, "manual path refresh")
			storeProbeVirtualRouterRoutePath(localNodeID, nodeID, selected)
			continue
		}
		if !exploreFailures {
			continue
		}

		shortestKeys := probeVirtualRouterPathKeySet(shortestByTarget[nodeID])
		candidates := probeVirtualRouterCandidatePathsFromNeighbors(neighbors, localNodeID, nodeID)
		exploration := make([][]string, 0, len(candidates))
		for _, candidate := range candidates {
			if _, alreadyQueried := shortestKeys[probeVirtualRouterPathKey(candidate)]; !alreadyQueried {
				exploration = append(exploration, candidate)
			}
		}
		reachable, explored := probeVirtualRouterQueryPathSet(exploration, query)
		result.Queried += explored
		result.Explored += explored
		selected := selectProbeVirtualRouterBestPath(reachable, true)
		if len(selected) == 0 {
			clearProbeVirtualRouterRouteCacheForPath([]string{localNodeID, nodeID}, "manual path exploration failed")
			continue
		}
		clearProbeVirtualRouterRouteCacheForPath(selected, "manual path exploration recovered")
		storeProbeVirtualRouterRoutePath(localNodeID, nodeID, selected)
		result.RecoveredTargets++
		log.Printf("probe virtual router manual path exploration recovered: target=%s path=%s", nodeID, strings.Join(selected, ">"))
	}
	return result
}

func probeVirtualRouterQueryPathSet(paths [][]string, query func([]string) (time.Duration, error)) ([][]string, int) {
	unique := make(map[string][]string, len(paths))
	for _, path := range paths {
		if key := probeVirtualRouterPathKey(path); key != "" {
			unique[key] = append([]string(nil), path...)
		}
	}
	if len(unique) == 0 {
		return nil, 0
	}
	var mu sync.Mutex
	reachable := make([][]string, 0, len(unique))
	semaphore := make(chan struct{}, 16)
	var wg sync.WaitGroup
	for _, path := range unique {
		pathCopy := append([]string(nil), path...)
		wg.Add(1)
		go func() {
			defer wg.Done()
			semaphore <- struct{}{}
			_, err := query(pathCopy)
			<-semaphore
			if err != nil {
				if !errors.Is(err, errProbeVirtualRouterAdjacentRTTUnavailable) {
					log.Printf("probe virtual router path rtt query failed: path=%s err=%v", strings.Join(pathCopy, ">"), err)
				}
				return
			}
			mu.Lock()
			reachable = append(reachable, pathCopy)
			mu.Unlock()
		}()
	}
	wg.Wait()
	return reachable, len(unique)
}

func probeVirtualRouterPathKeySet(paths [][]string) map[string]struct{} {
	out := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		if key := probeVirtualRouterPathKey(path); key != "" {
			out[key] = struct{}{}
		}
	}
	return out
}

func probeVirtualRouterReachablePathSubset(paths [][]string, reachable [][]string) [][]string {
	wanted := probeVirtualRouterPathKeySet(paths)
	out := make([][]string, 0, len(reachable))
	for _, path := range reachable {
		if _, ok := wanted[probeVirtualRouterPathKey(path)]; ok {
			out = append(out, path)
		}
	}
	return out
}

func setProbeVirtualRouterNonDirectPathGuardEnabled(enabled bool) {
	probeVirtualRouterNonDirectPathGuardState.mu.Lock()
	if !enabled {
		stopCh := probeVirtualRouterNonDirectPathGuardState.stopCh
		probeVirtualRouterNonDirectPathGuardState.stopCh = nil
		probeVirtualRouterNonDirectPathGuardState.failedPaths = make(map[string]struct{})
		probeVirtualRouterNonDirectPathGuardState.mu.Unlock()
		if stopCh != nil {
			close(stopCh)
		}
		return
	}
	if probeVirtualRouterNonDirectPathGuardState.stopCh != nil {
		probeVirtualRouterNonDirectPathGuardState.mu.Unlock()
		return
	}
	if probeVirtualRouterNonDirectPathGuardState.failedPaths == nil {
		probeVirtualRouterNonDirectPathGuardState.failedPaths = make(map[string]struct{})
	}
	stopCh := make(chan struct{})
	probeVirtualRouterNonDirectPathGuardState.stopCh = stopCh
	probeVirtualRouterNonDirectPathGuardState.mu.Unlock()
	go runProbeVirtualRouterNonDirectPathGuard(stopCh)
}

func runProbeVirtualRouterNonDirectPathGuard(stopCh <-chan struct{}) {
	ticker := time.NewTicker(probeVirtualRouterNonDirectPathGuardInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C:
			probeVirtualRouterGuardNonDirectPaths()
		}
	}
}

func probeVirtualRouterGuardNonDirectPaths() int {
	localNodeID := currentProbeVirtualRouterLocalNodeID()
	if localNodeID == "" {
		return 0
	}
	probeVirtualRouterState.mu.RLock()
	nodeToIP := probeVirtualRouterCloneNodeToIPLocked()
	neighbors := probeVirtualRouterCloneNeighborsLocked()
	probeVirtualRouterState.mu.RUnlock()
	nodeIDs := make([]string, 0, len(nodeToIP))
	for nodeID := range nodeToIP {
		cleanNodeID := normalizeProbeRouteNodeID(nodeID)
		if cleanNodeID != "" && cleanNodeID != localNodeID {
			nodeIDs = append(nodeIDs, cleanNodeID)
		}
	}
	sort.Strings(nodeIDs)
	pathsByKey := make(map[string][]string)
	for _, nodeID := range nodeIDs {
		for _, path := range probeVirtualRouterShortestPathsFromNeighbors(neighbors, localNodeID, nodeID) {
			if len(path) <= 2 {
				continue
			}
			if key := probeVirtualRouterPathKey(path); key != "" {
				pathsByKey[key] = path
			}
		}
	}
	pathKeys := make([]string, 0, len(pathsByKey))
	for key := range pathsByKey {
		pathKeys = append(pathKeys, key)
	}
	sort.Strings(pathKeys)
	guarded := 0
	activePathKeys := make(map[string]struct{}, len(pathKeys))
	for _, key := range pathKeys {
		path := pathsByKey[key]
		activePathKeys[key] = struct{}{}
		guarded++
		if _, err := probeVirtualRouterQueryPathRTT(path); err != nil {
			if errors.Is(err, errProbeVirtualRouterAdjacentRTTUnavailable) {
				recordProbeVirtualRouterPathRTTError(path, err)
			}
			if markProbeVirtualRouterNonDirectPathGuardianFailure(key, true) {
				log.Printf("probe virtual router non-direct path guardian failed: path=%s err=%v", strings.Join(path, ">"), err)
			}
			if probeVirtualRouterPathShouldAvoid(path) {
				clearProbeVirtualRouterRouteCacheForPath(path, "non-direct path first hop is unavailable")
				if replacement := currentProbeVirtualRouterPathBetweenNodes(localNodeID, path[len(path)-1]); len(replacement) > 0 && !sameProbeVirtualRouterPath(path, replacement) {
					log.Printf("probe virtual router non-direct path guardian reselected path: old=%s new=%s", strings.Join(path, ">"), strings.Join(replacement, ">"))
				}
			}
		} else {
			markProbeVirtualRouterNonDirectPathGuardianFailure(key, false)
		}
	}
	pruneProbeVirtualRouterNonDirectPathGuardianFailures(activePathKeys)
	return guarded
}

func markProbeVirtualRouterNonDirectPathGuardianFailure(pathKey string, failed bool) bool {
	pathKey = strings.TrimSpace(pathKey)
	if pathKey == "" {
		return false
	}
	probeVirtualRouterNonDirectPathGuardState.mu.Lock()
	defer probeVirtualRouterNonDirectPathGuardState.mu.Unlock()
	if probeVirtualRouterNonDirectPathGuardState.failedPaths == nil {
		probeVirtualRouterNonDirectPathGuardState.failedPaths = make(map[string]struct{})
	}
	_, wasFailed := probeVirtualRouterNonDirectPathGuardState.failedPaths[pathKey]
	if !failed {
		delete(probeVirtualRouterNonDirectPathGuardState.failedPaths, pathKey)
		return false
	}
	probeVirtualRouterNonDirectPathGuardState.failedPaths[pathKey] = struct{}{}
	return !wasFailed
}

func pruneProbeVirtualRouterNonDirectPathGuardianFailures(activePathKeys map[string]struct{}) {
	probeVirtualRouterNonDirectPathGuardState.mu.Lock()
	defer probeVirtualRouterNonDirectPathGuardState.mu.Unlock()
	for pathKey := range probeVirtualRouterNonDirectPathGuardState.failedPaths {
		if _, active := activePathKeys[pathKey]; !active {
			delete(probeVirtualRouterNonDirectPathGuardState.failedPaths, pathKey)
		}
	}
}

func sameProbeVirtualRouterPath(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if normalizeProbeRouteNodeID(left[index]) != normalizeProbeRouteNodeID(right[index]) {
			return false
		}
	}
	return true
}

func probeVirtualRouterPingPongDirection(rt *probeVirtualRouterRuntime, direction string) {
	if rt == nil {
		return
	}
	targetNodeID := ""
	switch normalizeProbeRouteBridgeRole(direction) {
	case probeRouteBridgeRoleToPrev:
		targetNodeID = normalizeProbeRouteNodeID(rt.cfg.peerNodeID)
	default:
		targetNodeID = normalizeProbeRouteNodeID(rt.cfg.peerNodeID)
	}
	result, err := queryProbeVirtualRouterAdjacentPing(rt, direction, targetNodeID)
	if err != nil {
		recordProbeVirtualRouterRuntimePingError(rt, direction, err)
		return
	}
	recordProbeVirtualRouterRuntimePingSuccess(rt, direction, time.Duration(result.LatencyMS)*time.Millisecond)
}

func makeProbeVirtualRouterFrameLinkRXDispatchShards() []chan probeVirtualRouterFrame {
	shards := make([]chan probeVirtualRouterFrame, 0, probeVirtualRouterFrameLinkRXDispatchShards)
	for i := 0; i < probeVirtualRouterFrameLinkRXDispatchShards; i++ {
		shards = append(shards, make(chan probeVirtualRouterFrame, probeVirtualRouterFrameLinkRXDispatchShardBufferFrames))
	}
	return shards
}

func newProbeVirtualRouterFrameLink(key string, runtime *probeVirtualRouterRuntime, carrier net.Conn, requestPath []string) *probeVirtualRouterFrameLink {
	now := time.Now()
	link := &probeVirtualRouterFrameLink{
		key:              strings.TrimSpace(key),
		runtime:          runtime,
		requestPath:      append([]string(nil), requestPath...),
		openedAt:         now,
		lastUsed:         now,
		tx:               make(chan probeVirtualRouterFrame, probeVirtualRouterFrameLinkTXBufferFrames),
		txControl:        make(chan probeVirtualRouterFrame, probeVirtualRouterFrameLinkTXControlBufferFrames),
		txBulk:           make(chan probeVirtualRouterFrame, probeVirtualRouterFrameLinkTXBulkBufferFrames),
		rx:               make(chan probeVirtualRouterFrame, probeVirtualRouterFrameLinkRXBufferFrames),
		rxDispatchShards: makeProbeVirtualRouterFrameLinkRXDispatchShards(),
		done:             make(chan struct{}),
		carrierNotify:    make(chan struct{}, 1),
	}
	if carrier != nil {
		link.carrier = newProbeVirtualRouterPhysicalCarrier(carrier, "", "")
	}
	return link
}

func newProbeVirtualRouterPhysicalCarrier(conn net.Conn, sessionID string, remoteAddr string) *probeVirtualRouterPhysicalCarrier {
	now := time.Now()
	return &probeVirtualRouterPhysicalCarrier{
		conn:        conn,
		sessionID:   strings.TrimSpace(sessionID),
		remoteAddr:  strings.TrimSpace(remoteAddr),
		connectedAt: now,
		lastReadAt:  now,
		lastWriteAt: now,
		done:        make(chan struct{}),
	}
}

func (s *probeVirtualRouterFrameLink) AttachCarrier(conn net.Conn, sessionID string, remoteAddr string) *probeVirtualRouterPhysicalCarrier {
	if s == nil || conn == nil {
		return nil
	}
	token := newProbeVirtualRouterPhysicalCarrier(conn, sessionID, remoteAddr)
	var old *probeVirtualRouterPhysicalCarrier
	s.mu.Lock()
	select {
	case <-s.done:
		s.mu.Unlock()
		token.close()
		return nil
	default:
	}
	old = s.carrier
	s.carrier = token
	s.openedAt = token.connectedAt
	s.lastUsed = token.connectedAt
	droppedTX, droppedRX := s.clearBuffersLocked()
	s.signalCarrierChangedLocked()
	s.mu.Unlock()
	if old != nil {
		old.close()
	}
	clearProbeVirtualRouterPhysicalCarrierDisconnected(s.runtime)
	if droppedTX > 0 || droppedRX > 0 {
		log.Printf("probe virtual router frame buffers cleared: reason=carrier_attached route=%s key=%s tx=%d rx=%d session_id=%s remote=%s", probeVirtualRouterRuntimeLogRouteID(s.runtime), strings.TrimSpace(s.key), droppedTX, droppedRX, strings.TrimSpace(sessionID), strings.TrimSpace(remoteAddr))
	}
	return token
}

func (s *probeVirtualRouterFrameLink) signalCarrierChangedLocked() {
	if s == nil || s.carrierNotify == nil {
		return
	}
	select {
	case s.carrierNotify <- struct{}{}:
	default:
	}
}

func (s *probeVirtualRouterFrameLink) clearBuffersLocked() (int, int) {
	if s == nil {
		return 0, 0
	}
	txDropped := drainProbeVirtualRouterFrameChannel(s.tx)
	txDropped += drainProbeVirtualRouterFrameChannel(s.txControl)
	txDropped += drainProbeVirtualRouterFrameChannel(s.txBulk)
	rxDropped := drainProbeVirtualRouterFrameChannel(s.rx)
	for _, shard := range s.rxDispatchShards {
		rxDropped += drainProbeVirtualRouterFrameChannel(shard)
	}
	return txDropped, rxDropped
}

func drainProbeVirtualRouterFrameChannel(ch chan probeVirtualRouterFrame) int {
	if ch == nil {
		return 0
	}
	count := 0
	for {
		select {
		case <-ch:
			count++
		default:
			return count
		}
	}
}

func runProbeVirtualRouterPhysicalCarrier(runtime *probeVirtualRouterRuntime, carrier net.Conn, sessionID string, remoteAddr string) {
	if runtime == nil || carrier == nil {
		return
	}
	key := probeVirtualRouterFrameLinkKey(runtime, "", "", nil)
	probeVirtualRouterFrameLinkState.mu.Lock()
	link := probeVirtualRouterFrameLinkState.links[key]
	if link == nil {
		link = newProbeVirtualRouterFrameLink(key, runtime, nil, nil)
		probeVirtualRouterFrameLinkState.links[key] = link
	} else {
		link.runtime = runtime
	}
	probeVirtualRouterFrameLinkState.mu.Unlock()
	link.Start()
	token := link.AttachCarrier(carrier, sessionID, remoteAddr)
	if token == nil {
		_ = carrier.Close()
		return
	}
	recordProbeVirtualRouterRuntimeOpenSuccess(runtime.cfg.routeID, 0)
	log.Printf("probe virtual router physical carrier connected: route=%s role=%s session_id=%s remote=%s", strings.TrimSpace(runtime.cfg.routeID), probeVirtualRouterRuntimeRole, strings.TrimSpace(sessionID), strings.TrimSpace(remoteAddr))
	<-token.done
	log.Printf("probe virtual router physical carrier disconnected: route=%s role=%s session_id=%s remote=%s %s", strings.TrimSpace(runtime.cfg.routeID), probeVirtualRouterRuntimeRole, strings.TrimSpace(sessionID), strings.TrimSpace(remoteAddr), probeVirtualRouterFrameLinkCarrierStateString(link, token))
}

func (s *probeVirtualRouterFrameLink) Start() {
	if s == nil || s.done == nil || s.tx == nil || s.txControl == nil || s.txBulk == nil || s.rx == nil {
		return
	}
	s.startOnce.Do(func() {
		shards := s.ensureRXDispatchShards()
		go s.runTXWorker()
		go s.runRXWorker()
		go s.runRXDispatchWorker()
		for shardID, shard := range shards {
			go s.runRXDispatchShardWorker(shardID, shard)
		}
	})
}

func (s *probeVirtualRouterFrameLink) ensureRXDispatchShards() []chan probeVirtualRouterFrame {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if len(s.rxDispatchShards) == 0 {
		s.rxDispatchShards = makeProbeVirtualRouterFrameLinkRXDispatchShards()
	}
	shards := append([]chan probeVirtualRouterFrame(nil), s.rxDispatchShards...)
	s.mu.Unlock()
	return shards
}

func (s *probeVirtualRouterFrameLink) rxQueueSnapshot() (int, int, int, int, int) {
	if s == nil {
		return 0, 0, 0, 0, 0
	}
	entryDepth, entryCap := 0, 0
	if s.rx != nil {
		entryDepth = len(s.rx)
		entryCap = cap(s.rx)
	}
	s.mu.Lock()
	shards := append([]chan probeVirtualRouterFrame(nil), s.rxDispatchShards...)
	s.mu.Unlock()
	dispatchDepth, dispatchCap := 0, 0
	for _, shard := range shards {
		if shard == nil {
			continue
		}
		dispatchDepth += len(shard)
		dispatchCap += cap(shard)
	}
	return entryDepth, entryCap, dispatchDepth, dispatchCap, len(shards)
}

func (s *probeVirtualRouterFrameLink) Wait() {
	if s == nil || s.done == nil {
		return
	}
	<-s.done
}

func (s *probeVirtualRouterFrameLink) EnqueueProbeVirtualRouterFrame(input probeVirtualRouterFrame) error {
	return s.enqueueProbeVirtualRouterFrame(input, time.Time{})
}

func (s *probeVirtualRouterFrameLink) EnqueueProbeVirtualRouterFrameUntil(input probeVirtualRouterFrame, deadline time.Time) error {
	return s.enqueueProbeVirtualRouterFrame(input, deadline)
}

func (s *probeVirtualRouterFrameLink) enqueueProbeVirtualRouterFrame(input probeVirtualRouterFrame, deadline time.Time) error {
	if s == nil {
		return io.ErrClosedPipe
	}
	frame := probeVirtualRouterFrame{
		MainType: input.MainType,
		SubType:  input.SubType,
		Control:  append([]byte(nil), input.Control...),
		Data:     append([]byte(nil), input.Data...),
	}
	queue, queueName := s.txQueueForFrame(frame)
	if queue == nil || s.done == nil {
		token, err := s.currentCarrier()
		if err != nil {
			return err
		}
		frame = appendProbeVirtualRouterWireFrameICMPTrace(frame, s.runtime, s.requestPath, "carrier_tx")
		if err := writeProbeVirtualRouterWireFrameRaw(token.conn, frame); err != nil {
			s.detachCarrierWithReason(token, "immediate_write_error", err)
			return err
		}
		token.markWrite()
		s.touch()
		return nil
	}
	select {
	case <-s.done:
		return io.ErrClosedPipe
	default:
	}
	if !deadline.IsZero() {
		wait := time.Until(deadline)
		if wait <= 0 {
			depth, capacity, _, _, _, _, _, _ := s.txQueueSnapshot()
			return fmt.Errorf("virtual router tx queue wait timeout: key=%s queue=%s depth=%d capacity=%d total_depth=%d total_capacity=%d: %w", strings.TrimSpace(s.key), queueName, len(queue), cap(queue), depth, capacity, os.ErrDeadlineExceeded)
		}
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case queue <- frame:
			s.touch()
			return nil
		case <-s.done:
			return io.ErrClosedPipe
		case <-timer.C:
			depth, capacity, _, _, _, _, _, _ := s.txQueueSnapshot()
			return fmt.Errorf("virtual router tx queue wait timeout: key=%s queue=%s depth=%d capacity=%d total_depth=%d total_capacity=%d: %w", strings.TrimSpace(s.key), queueName, len(queue), cap(queue), depth, capacity, os.ErrDeadlineExceeded)
		}
	}
	select {
	case queue <- frame:
		s.touch()
		return nil
	case <-s.done:
		return io.ErrClosedPipe
	default:
		depth, capacity, _, _, _, _, _, _ := s.txQueueSnapshot()
		return fmt.Errorf("virtual router tx queue full: key=%s queue=%s depth=%d capacity=%d total_depth=%d total_capacity=%d", strings.TrimSpace(s.key), queueName, len(queue), cap(queue), depth, capacity)
	}
}

func (s *probeVirtualRouterFrameLink) runTXWorker() {
	businessSinceBulk := 0
	for {
		frame, ok := s.nextTXFrame(&businessSinceBulk)
		if !ok {
			return
		}
		if len(frame.Data) == 0 {
			continue
		}
		token, err := s.currentCarrier()
		if err != nil {
			log.Printf("probe virtual router frame tx drop: route=%s key=%s frame=%s err=%v", probeVirtualRouterRuntimeLogRouteID(s.runtime), s.key, probeVirtualRouterFrameDebugLabel(frame, s.requestPath), err)
			recordProbeVirtualRouterTXFrameFailure(s, frame, err)
			continue
		}
		frame = appendProbeVirtualRouterWireFrameICMPTrace(frame, s.runtime, s.requestPath, "carrier_tx")
		frames := []probeVirtualRouterFrame{frame}
		batchBytes := probeVirtualRouterFrameEnvelopeHeaderSize + len(frame.Control) + len(frame.Data)
		coalesceDeadline := time.Time{}
		_, queueName := s.txQueueForFrame(frame)
		allowBatch := queueName != "control"
		if allowBatch {
			coalesceDeadline = time.Now().Add(probeVirtualRouterFrameLinkTXCoalesceWindow)
		}
		for allowBatch && batchBytes < probeVirtualRouterFrameLinkTXBatchBytes {
			next, available := s.tryNextTXFrame(&businessSinceBulk)
			if !available && !coalesceDeadline.IsZero() {
				next, available = s.waitNextTXFrameUntil(&businessSinceBulk, coalesceDeadline)
			}
			if !available {
				break
			}
			if len(next.Data) == 0 {
				continue
			}
			next = appendProbeVirtualRouterWireFrameICMPTrace(next, s.runtime, s.requestPath, "carrier_tx")
			frames = append(frames, next)
			batchBytes += probeVirtualRouterFrameEnvelopeHeaderSize + len(next.Control) + len(next.Data)
		}
		writeStartedAt := time.Now()
		err = writeProbeVirtualRouterWireFramesRaw(token.conn, frames)
		s.recordTXWriteBatch(time.Since(writeStartedAt), len(frames), batchBytes)
		if err == nil {
			token.markWrite()
			recordProbeVirtualRouterRuntimeCarrierTXSuccess(s.runtime)
			s.touch()
			continue
		}
		log.Printf("probe virtual router frame tx carrier failed: route=%s key=%s frames=%d bytes=%d first_frame=%s err=%v %s", probeVirtualRouterRuntimeLogRouteID(s.runtime), s.key, len(frames), batchBytes, probeVirtualRouterFrameDebugLabel(frame, s.requestPath), err, probeVirtualRouterFrameLinkCarrierStateString(s, token))
		for _, failedFrame := range frames {
			recordProbeVirtualRouterTXFrameFailure(s, failedFrame, err)
		}
		s.detachCarrierWithReason(token, "tx_write_error", err)
	}
}

func recordProbeVirtualRouterTXFrameFailure(link *probeVirtualRouterFrameLink, frame probeVirtualRouterFrame, err error) {
	if link == nil || frame.MainType != probeVirtualRouterFrameMainTypeIP || len(frame.Data) == 0 || err == nil {
		return
	}
	recordProbeVirtualRouterRecentPacket("frame_tx", "proxy_error", link.runtime, frame.Data, link.requestPath, false, err)
}

func (s *probeVirtualRouterFrameLink) txQueueForFrame(frame probeVirtualRouterFrame) (chan probeVirtualRouterFrame, string) {
	if s == nil {
		return nil, ""
	}
	switch frame.MainType {
	case probeVirtualRouterFrameMainTypePingPong,
		probeVirtualRouterFrameMainTypePathRTT,
		probeVirtualRouterFrameMainTypeRouteTest,
		probeVirtualRouterFrameMainTypeFakeIPVerify:
		return s.txControl, "control"
	case probeVirtualRouterFrameMainTypeSpeed:
		return s.txBulk, "bulk"
	case probeVirtualRouterFrameMainTypeDebugLog:
		if frame.SubType == probeVirtualRouterDebugLogSubTypeQuery {
			return s.txControl, "control"
		}
		return s.txBulk, "bulk"
	case probeVirtualRouterFrameMainTypeProxy:
		switch frame.SubType {
		case probeVirtualRouterProxySubTypeTCPData,
			probeVirtualRouterProxySubTypeUDPRequest,
			probeVirtualRouterProxySubTypeUDPResponse:
			return s.tx, "business"
		default:
			return s.txControl, "control"
		}
	default:
		return s.tx, "business"
	}
}

func (s *probeVirtualRouterFrameLink) txQueueSnapshot() (depth int, capacity int, controlDepth int, controlCapacity int, businessDepth int, businessCapacity int, bulkDepth int, bulkCapacity int) {
	if s == nil {
		return
	}
	if s.txControl != nil {
		controlDepth = len(s.txControl)
		controlCapacity = cap(s.txControl)
	}
	if s.tx != nil {
		businessDepth = len(s.tx)
		businessCapacity = cap(s.tx)
	}
	if s.txBulk != nil {
		bulkDepth = len(s.txBulk)
		bulkCapacity = cap(s.txBulk)
	}
	depth = controlDepth + businessDepth + bulkDepth
	capacity = controlCapacity + businessCapacity + bulkCapacity
	return
}

func (s *probeVirtualRouterFrameLink) nextTXFrame(businessSinceBulk *int) (probeVirtualRouterFrame, bool) {
	if s == nil || s.done == nil {
		return probeVirtualRouterFrame{}, false
	}
	if businessSinceBulk == nil {
		value := 0
		businessSinceBulk = &value
	}
	for {
		select {
		case <-s.done:
			return probeVirtualRouterFrame{}, false
		default:
		}
		if frame, ok := s.tryNextTXFrame(businessSinceBulk); ok {
			return frame, true
		}
		select {
		case frame := <-s.txControl:
			return frame, true
		case frame := <-s.tx:
			if *businessSinceBulk < probeVirtualRouterFrameLinkTXBusinessQuantum {
				(*businessSinceBulk)++
			}
			return frame, true
		case frame := <-s.txBulk:
			*businessSinceBulk = 0
			return frame, true
		case <-s.done:
			return probeVirtualRouterFrame{}, false
		}
	}
}

func (s *probeVirtualRouterFrameLink) tryNextTXFrame(businessSinceBulk *int) (probeVirtualRouterFrame, bool) {
	if s == nil || businessSinceBulk == nil {
		return probeVirtualRouterFrame{}, false
	}
	select {
	case frame := <-s.txControl:
		return frame, true
	default:
	}
	if *businessSinceBulk >= probeVirtualRouterFrameLinkTXBusinessQuantum {
		select {
		case frame := <-s.txBulk:
			*businessSinceBulk = 0
			return frame, true
		default:
		}
	}
	select {
	case frame := <-s.tx:
		if *businessSinceBulk < probeVirtualRouterFrameLinkTXBusinessQuantum {
			(*businessSinceBulk)++
		}
		return frame, true
	default:
	}
	select {
	case frame := <-s.txBulk:
		*businessSinceBulk = 0
		return frame, true
	default:
		return probeVirtualRouterFrame{}, false
	}
}

func (s *probeVirtualRouterFrameLink) waitNextTXFrameUntil(businessSinceBulk *int, deadline time.Time) (probeVirtualRouterFrame, bool) {
	if s == nil || businessSinceBulk == nil || s.done == nil {
		return probeVirtualRouterFrame{}, false
	}
	if frame, ok := s.tryNextTXFrame(businessSinceBulk); ok {
		return frame, true
	}
	wait := time.Until(deadline)
	if wait <= 0 {
		return probeVirtualRouterFrame{}, false
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case frame := <-s.txControl:
		return frame, true
	case frame := <-s.tx:
		if *businessSinceBulk < probeVirtualRouterFrameLinkTXBusinessQuantum {
			(*businessSinceBulk)++
		}
		return frame, true
	case frame := <-s.txBulk:
		*businessSinceBulk = 0
		return frame, true
	case <-s.done:
		return probeVirtualRouterFrame{}, false
	case <-timer.C:
		return probeVirtualRouterFrame{}, false
	}
}

func (s *probeVirtualRouterFrameLink) recordTXWriteBatch(value time.Duration, frames int, bytes int) {
	if s == nil || value < 0 {
		return
	}
	s.mu.Lock()
	s.txLastWriteTime = value
	s.txLastBatchFrames = frames
	s.txLastBatchBytes = bytes
	if s.txWriteTimeEMA <= 0 {
		s.txWriteTimeEMA = value
	} else {
		s.txWriteTimeEMA = (s.txWriteTimeEMA*7 + value) / 8
	}
	s.mu.Unlock()
}

func (s *probeVirtualRouterFrameLink) txWriteSnapshot() (time.Duration, time.Duration, int, int) {
	if s == nil {
		return 0, 0, 0, 0
	}
	s.mu.Lock()
	last := s.txLastWriteTime
	ema := s.txWriteTimeEMA
	batchFrames := s.txLastBatchFrames
	batchBytes := s.txLastBatchBytes
	s.mu.Unlock()
	return last, ema, batchFrames, batchBytes
}

func (s *probeVirtualRouterFrameLink) runRXWorker() {
	for {
		token, err := s.waitCarrier()
		if err != nil {
			return
		}
		reader := bufio.NewReaderSize(token.conn, probeVirtualRouterFrameReadBufferBytes)
		for {
			frame, err := readProbeVirtualRouterWireFrame(reader)
			if err != nil {
				closedLike := errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) || isProbeVirtualRouterClosedLinkError(err)
				log.Printf("probe virtual router frame rx carrier ended: route=%s key=%s closed_like=%v err=%v %s", probeVirtualRouterRuntimeLogRouteID(s.runtime), s.key, closedLike, err, probeVirtualRouterFrameLinkCarrierStateString(s, token))
				s.detachCarrierWithReason(token, "rx_read_error", err)
				break
			}
			frame = appendProbeVirtualRouterWireFrameICMPTrace(frame, s.runtime, s.requestPath, "carrier_rx")
			token.markRead()
			if shouldHandleProbeVirtualRouterFrameInRXWorker(s.runtime, frame, s.requestPath) {
				if err := handleProbeVirtualRouterFrame(s.runtime, s, frame, s.requestPath); err != nil {
					log.Printf("probe virtual router frame rx inline failed: route=%s key=%s path=%s err=%v", probeVirtualRouterRuntimeLogRouteID(s.runtime), s.key, probeVirtualRouterWireFramePathString(frame, s.requestPath), err)
				}
				s.touch()
				continue
			}
			if err := s.enqueueRXFrame(frame); err != nil {
				if errors.Is(err, io.ErrClosedPipe) {
					return
				}
				log.Printf("probe virtual router frame rx enqueue failed: route=%s key=%s path=%s err=%v", probeVirtualRouterRuntimeLogRouteID(s.runtime), s.key, probeVirtualRouterWireFramePathString(frame, s.requestPath), err)
			}
		}
	}
}

func (s *probeVirtualRouterFrameLink) enqueueRXFrame(frame probeVirtualRouterFrame) error {
	if s == nil {
		return io.ErrClosedPipe
	}
	if s.rx == nil || s.done == nil {
		return io.ErrClosedPipe
	}
	select {
	case s.rx <- frame:
		s.touch()
		return nil
	case <-s.done:
		return io.ErrClosedPipe
	default:
		return fmt.Errorf("virtual router rx queue full: key=%s depth=%d capacity=%d", strings.TrimSpace(s.key), len(s.rx), cap(s.rx))
	}
}

func (s *probeVirtualRouterFrameLink) runRXDispatchWorker() {
	shards := s.ensureRXDispatchShards()
	for {
		select {
		case frame := <-s.rx:
			if len(frame.Data) == 0 {
				continue
			}
			if err := s.enqueueRXDispatchFrame(frame, shards); err != nil {
				if errors.Is(err, io.ErrClosedPipe) {
					return
				}
				if shouldLog, dropped := s.recordRXDispatchDrop(); shouldLog {
					log.Printf("probe virtual router frame rx dispatch enqueue failed: route=%s key=%s path=%s dropped_since_last=%d err=%v", probeVirtualRouterRuntimeLogRouteID(s.runtime), s.key, probeVirtualRouterWireFramePathString(frame, s.requestPath), dropped, err)
				}
				if frame.MainType == probeVirtualRouterFrameMainTypeIP {
					recordProbeVirtualRouterRecentPacket("frame_rx", "drop", s.runtime, frame.Data, s.requestPath, false, err)
				}
				continue
			}
		case <-s.done:
			return
		}
	}
}

func (s *probeVirtualRouterFrameLink) recordRXDispatchDrop() (bool, uint64) {
	if s == nil {
		return false, 0
	}
	now := time.Now()
	s.mu.Lock()
	s.rxDispatchDrops++
	if !s.rxDropLastLogAt.IsZero() && now.Sub(s.rxDropLastLogAt) < probeVirtualRouterRXDispatchDropLogPeriod {
		s.mu.Unlock()
		return false, 0
	}
	dropped := s.rxDispatchDrops
	s.rxDispatchDrops = 0
	s.rxDropLastLogAt = now
	s.mu.Unlock()
	return true, dropped
}

func (s *probeVirtualRouterFrameLink) enqueueRXDispatchFrame(frame probeVirtualRouterFrame, shards []chan probeVirtualRouterFrame) error {
	if s == nil {
		return io.ErrClosedPipe
	}
	if len(shards) == 0 {
		return s.handleRXDispatchFrame(frame)
	}
	shardID := probeVirtualRouterFrameRXDispatchShard(frame, len(shards))
	shard := shards[shardID]
	select {
	case shard <- frame:
		return nil
	case <-s.done:
		return io.ErrClosedPipe
	default:
		return fmt.Errorf("virtual router rx dispatch queue full: key=%s shard=%d depth=%d capacity=%d", strings.TrimSpace(s.key), shardID, len(shard), cap(shard))
	}
}

func (s *probeVirtualRouterFrameLink) runRXDispatchShardWorker(shardID int, shard chan probeVirtualRouterFrame) {
	if s == nil || shard == nil {
		return
	}
	for {
		select {
		case frame := <-shard:
			if len(frame.Data) == 0 {
				continue
			}
			if err := s.handleRXDispatchFrame(frame); err != nil {
				log.Printf("probe virtual router frame rx dispatch failed: route=%s key=%s shard=%d path=%s err=%v", probeVirtualRouterRuntimeLogRouteID(s.runtime), s.key, shardID, probeVirtualRouterWireFramePathString(frame, s.requestPath), err)
				continue
			}
		case <-s.done:
			return
		}
	}
}

func (s *probeVirtualRouterFrameLink) handleRXDispatchFrame(frame probeVirtualRouterFrame) error {
	if s == nil {
		return io.ErrClosedPipe
	}
	if len(frame.Data) == 0 {
		return nil
	}
	return handleProbeVirtualRouterFrame(s.runtime, s, frame, s.requestPath)
}

func probeVirtualRouterFrameRXDispatchShard(frame probeVirtualRouterFrame, shardCount int) int {
	if shardCount <= 1 {
		return 0
	}
	return int(probeVirtualRouterFrameRXDispatchHash(frame) % uint32(shardCount))
}

func probeVirtualRouterFrameRXDispatchHash(frame probeVirtualRouterFrame) uint32 {
	const (
		fnvOffset uint32 = 2166136261
		fnvPrime  uint32 = 16777619
	)
	hashByte := func(h uint32, value byte) uint32 {
		h ^= uint32(value)
		return h * fnvPrime
	}
	hashUint16 := func(h uint32, value uint16) uint32 {
		h = hashByte(h, byte(value>>8))
		return hashByte(h, byte(value))
	}
	h := fnvOffset
	h = hashUint16(h, frame.MainType)
	h = hashUint16(h, frame.SubType)
	if frame.MainType == probeVirtualRouterFrameMainTypeProxy {
		return probeVRouteProxyFrameDispatchHash(frame.SubType, frame.Data, h)
	}
	if frame.MainType != probeVirtualRouterFrameMainTypeIP {
		return h
	}
	return probeVirtualRouterPacketFlowHash(frame.Data, h)
}

func probeVirtualRouterPacketDispatchShard(packet []byte, shardCount int) int {
	if shardCount <= 1 {
		return 0
	}
	return int(probeVirtualRouterPacketFlowHash(packet, 2166136261) % uint32(shardCount))
}

func probeVirtualRouterPacketFlowHash(packet []byte, seed uint32) uint32 {
	const fnvPrime uint32 = 16777619
	hashByte := func(h uint32, value byte) uint32 {
		h ^= uint32(value)
		return h * fnvPrime
	}
	hashUint16 := func(h uint32, value uint16) uint32 {
		h = hashByte(h, byte(value>>8))
		return hashByte(h, byte(value))
	}
	hashUint32 := func(h uint32, value uint32) uint32 {
		h = hashByte(h, byte(value>>24))
		h = hashByte(h, byte(value>>16))
		h = hashByte(h, byte(value>>8))
		return hashByte(h, byte(value))
	}

	h := seed
	if len(packet) < 20 || packet[0]>>4 != 4 {
		return h
	}
	ihl := int(packet[0]&0x0F) * 4
	if ihl < 20 || len(packet) < ihl {
		return h
	}
	totalLen := int(binary.BigEndian.Uint16(packet[2:4]))
	if totalLen <= 0 || totalLen > len(packet) || totalLen < ihl {
		return h
	}
	proto := packet[9]
	srcIP := binary.BigEndian.Uint32(packet[12:16])
	dstIP := binary.BigEndian.Uint32(packet[16:20])
	srcPort, dstPort := uint16(0), uint16(0)
	if (proto == 6 || proto == 17) && totalLen >= ihl+4 {
		transport := packet[ihl:totalLen]
		srcPort = binary.BigEndian.Uint16(transport[0:2])
		dstPort = binary.BigEndian.Uint16(transport[2:4])
	}
	if probeVirtualRouterEndpointGreater(srcIP, srcPort, dstIP, dstPort) {
		srcIP, dstIP = dstIP, srcIP
		srcPort, dstPort = dstPort, srcPort
	}
	h = hashByte(h, proto)
	h = hashUint32(h, srcIP)
	h = hashUint16(h, srcPort)
	h = hashUint32(h, dstIP)
	h = hashUint16(h, dstPort)
	if proto == 1 && totalLen >= ihl+8 {
		icmp := packet[ihl:totalLen]
		h = hashUint16(h, binary.BigEndian.Uint16(icmp[4:6]))
	}
	return h
}

func probeVirtualRouterEndpointGreater(leftIP uint32, leftPort uint16, rightIP uint32, rightPort uint16) bool {
	if leftIP != rightIP {
		return leftIP > rightIP
	}
	return leftPort > rightPort
}

func (s *probeVirtualRouterFrameLink) touch() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.lastUsed = time.Now()
	s.mu.Unlock()
}

func shouldHandleProbeVirtualRouterFrameInRXWorker(runtime *probeVirtualRouterRuntime, frame probeVirtualRouterFrame, fallbackPath []string) bool {
	if len(frame.Data) == 0 {
		return false
	}
	control, err := probeVirtualRouterFrameControl(frame, fallbackPath)
	if err != nil {
		return false
	}
	localNodeID := currentProbeVirtualRouterLocalNodeIDForRuntime(runtime)
	if localNodeID == "" {
		return false
	}
	switch frame.MainType {
	case probeVirtualRouterFrameMainTypeIP:
		return false
	case probeVirtualRouterFrameMainTypePingPong:
		return shouldHandleProbeVirtualRouterPingPongFrameInRXWorker(frame.SubType, frame.Data)
	case probeVirtualRouterFrameMainTypePathRTT:
		return shouldHandleProbeVirtualRouterPathRTTFrameInRXWorker(frame.SubType, frame.Data, localNodeID)
	case probeVirtualRouterFrameMainTypeSpeed:
		return shouldHandleProbeVirtualRouterSpeedFrameInRXWorker(frame.SubType, frame.Data, control.Path, localNodeID)
	case probeVirtualRouterFrameMainTypeRouteTest:
		return false
	case probeVirtualRouterFrameMainTypeFakeIPVerify:
		return frame.SubType == probeVirtualRouterFakeIPVerifySubTypeResponse
	default:
		return false
	}
}

func shouldHandleProbeVirtualRouterIPFrameInRXWorker(runtime *probeVirtualRouterRuntime, packet []byte, path []string, localNodeID string) bool {
	dstIP := probeVirtualRouterIPv4Destination(packet)
	if dstIP == "" {
		return false
	}
	if probeVirtualRouterIPMatches(dstIP, currentProbeVirtualRouterLocalIPForRuntime(runtime)) {
		return false
	}
	cleanPath := cleanProbeVirtualRouterPath(path)
	if len(cleanPath) == 0 {
		cleanPath = currentProbeVirtualRouterPathToIP(dstIP)
	}
	return probeVirtualRouterNextHopInPath(cleanPath, localNodeID) != ""
}

func shouldHandleProbeVirtualRouterPingPongFrameInRXWorker(subType uint16, payload []byte) bool {
	switch subType {
	case probeVirtualRouterPingPongSubTypePing, probeVirtualRouterPingPongSubTypePong:
		msg := probeVirtualRouterControlProbePayload{}
		return json.Unmarshal(payload, &msg) == nil
	default:
		return false
	}
}

func shouldHandleProbeVirtualRouterPathRTTFrameInRXWorker(subType uint16, payload []byte, localNodeID string) bool {
	msg := probeVirtualRouterControlProbePayload{}
	if err := json.Unmarshal(payload, &msg); err != nil {
		return false
	}
	msg.Path = cleanProbeVirtualRouterPath(msg.Path)
	switch subType {
	case probeVirtualRouterPathRTTSubTypeQuery:
		if normalizeProbeRouteNodeID(msg.TargetNodeID) == localNodeID {
			return true
		}
		return probeVirtualRouterNextHopInPath(msg.Path, localNodeID) != ""
	case probeVirtualRouterPathRTTSubTypeResp:
		if normalizeProbeRouteNodeID(msg.SourceNodeID) == localNodeID {
			return true
		}
		return probeVirtualRouterNextHopInPath(probeVirtualRouterReversePath(msg.Path), localNodeID) != ""
	default:
		return false
	}
}

func shouldHandleProbeVirtualRouterSpeedFrameInRXWorker(subType uint16, payload []byte, framePath []string, localNodeID string) bool {
	if subType == probeVirtualRouterSpeedSubTypeChunk {
		path := cleanProbeVirtualRouterPath(framePath)
		if len(path) < 2 {
			return false
		}
		if localNodeID == path[len(path)-1] {
			return true
		}
		return probeVirtualRouterNextHopInPath(path, localNodeID) != ""
	}
	msg := probeVirtualRouterSpeedTestResultPayload{}
	if err := json.Unmarshal(payload, &msg); err != nil {
		return false
	}
	msg.Path = cleanProbeVirtualRouterPath(msg.Path)
	switch subType {
	case probeVirtualRouterSpeedSubTypeStart, probeVirtualRouterSpeedSubTypeFinish:
		if len(msg.Path) < 2 {
			return false
		}
		if localNodeID == msg.Path[len(msg.Path)-1] {
			return true
		}
		return probeVirtualRouterNextHopInPath(msg.Path, localNodeID) != ""
	case probeVirtualRouterSpeedSubTypeSend:
		return len(msg.Path) >= 2 && localNodeID != msg.Path[len(msg.Path)-1] && probeVirtualRouterNextHopInPath(msg.Path, localNodeID) != ""
	case probeVirtualRouterSpeedSubTypeResult:
		if normalizeProbeRouteNodeID(msg.ResultNodeID) == localNodeID {
			return true
		}
		return probeVirtualRouterNextHopInPath(msg.Path, localNodeID) != ""
	default:
		return false
	}
}

func shouldHandleProbeVirtualRouterRouteTestFrameInRXWorker(subType uint16, payload []byte, framePath []string, localNodeID string) bool {
	msg := probeVirtualRouterRouteTestPayload{}
	if err := json.Unmarshal(payload, &msg); err != nil {
		return false
	}
	if len(msg.Path) == 0 {
		msg.Path = append([]string(nil), framePath...)
	}
	msg.Path = cleanProbeVirtualRouterPath(msg.Path)
	switch subType {
	case probeVirtualRouterRouteTestSubTypeProbe:
		if len(msg.Path) < 1 {
			return false
		}
		if normalizeProbeRouteNodeID(msg.ExitNodeID) == localNodeID || localNodeID == msg.Path[len(msg.Path)-1] {
			return true
		}
		return probeVirtualRouterNextHopInPath(msg.Path, localNodeID) != ""
	case probeVirtualRouterRouteTestSubTypeReport:
		if normalizeProbeRouteNodeID(msg.SourceNodeID) == localNodeID {
			return true
		}
		return probeVirtualRouterNextHopInPath(msg.Path, localNodeID) != ""
	default:
		return false
	}
}

func (s *probeVirtualRouterFrameLink) currentCarrier() (*probeVirtualRouterPhysicalCarrier, error) {
	if s == nil {
		return nil, io.ErrClosedPipe
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done == nil {
		return nil, io.ErrClosedPipe
	}
	select {
	case <-s.done:
		return nil, io.ErrClosedPipe
	default:
	}
	token := s.carrier
	if token == nil || token.conn == nil {
		return nil, errors.New("virtual router physical carrier is unavailable")
	}
	return token, nil
}

func (s *probeVirtualRouterFrameLink) waitCarrier() (*probeVirtualRouterPhysicalCarrier, error) {
	if s == nil {
		return nil, io.ErrClosedPipe
	}
	for {
		s.mu.Lock()
		if s.done == nil {
			s.mu.Unlock()
			return nil, io.ErrClosedPipe
		}
		select {
		case <-s.done:
			s.mu.Unlock()
			return nil, io.ErrClosedPipe
		default:
		}
		token := s.carrier
		notify := s.carrierNotify
		if token != nil && token.conn != nil {
			s.mu.Unlock()
			return token, nil
		}
		s.mu.Unlock()
		select {
		case <-s.done:
			return nil, io.ErrClosedPipe
		case <-notify:
		}
	}
}

func probeVirtualRouterFrameLinkDebugState(link *probeVirtualRouterFrameLink) string {
	if link == nil {
		return "link=nil"
	}
	now := time.Now()
	txDepth, _, txControlDepth, _, txBusinessDepth, _, txBulkDepth, _ := link.txQueueSnapshot()
	txLastWrite, txWriteEMA, txLastBatchFrames, txLastBatchBytes := link.txWriteSnapshot()
	rxEntryDepth, _, rxDispatchDepth, _, _ := link.rxQueueSnapshot()
	rxDepth := rxEntryDepth + rxDispatchDepth
	link.mu.Lock()
	key := strings.TrimSpace(link.key)
	lastUsed := link.lastUsed
	token := link.carrier
	link.mu.Unlock()
	if token == nil {
		return fmt.Sprintf("link_key=%s carrier=none last_used_ms=%d tx_queue=%d tx_control=%d tx_business=%d tx_bulk=%d tx_last_write_ms=%d tx_write_ema_ms=%d tx_last_batch_frames=%d tx_last_batch_bytes=%d rx_queue=%d rx_entry_queue=%d rx_dispatch_queue=%d", key, probeDurationMilliseconds(now.Sub(lastUsed)), txDepth, txControlDepth, txBusinessDepth, txBulkDepth, probeDurationMilliseconds(txLastWrite), probeDurationMilliseconds(txWriteEMA), txLastBatchFrames, txLastBatchBytes, rxDepth, rxEntryDepth, rxDispatchDepth)
	}
	token.mu.Lock()
	sessionID := strings.TrimSpace(token.sessionID)
	remoteAddr := strings.TrimSpace(token.remoteAddr)
	connectedAt := token.connectedAt
	lastReadAt := token.lastReadAt
	lastWriteAt := token.lastWriteAt
	token.mu.Unlock()
	return fmt.Sprintf(
		"link_key=%s carrier_session=%s remote=%s connected_ms=%d rx_idle_ms=%d tx_idle_ms=%d tx_queue=%d tx_control=%d tx_business=%d tx_bulk=%d tx_last_write_ms=%d tx_write_ema_ms=%d tx_last_batch_frames=%d tx_last_batch_bytes=%d rx_queue=%d rx_entry_queue=%d rx_dispatch_queue=%d",
		key,
		sessionID,
		remoteAddr,
		probeDurationMilliseconds(now.Sub(connectedAt)),
		probeDurationMilliseconds(now.Sub(lastReadAt)),
		probeDurationMilliseconds(now.Sub(lastWriteAt)),
		txDepth,
		txControlDepth,
		txBusinessDepth,
		txBulkDepth,
		probeDurationMilliseconds(txLastWrite),
		probeDurationMilliseconds(txWriteEMA),
		txLastBatchFrames,
		txLastBatchBytes,
		rxDepth,
		rxEntryDepth,
		rxDispatchDepth,
	)
}

func probeVirtualRouterFrameLinkCarrierStateString(link *probeVirtualRouterFrameLink, token *probeVirtualRouterPhysicalCarrier) string {
	if link == nil {
		return "link=nil"
	}
	if token == nil {
		return probeVirtualRouterFrameLinkDebugState(link)
	}
	now := time.Now()
	txDepth, txCap, txControlDepth, txControlCap, txBusinessDepth, txBusinessCap, txBulkDepth, txBulkCap := link.txQueueSnapshot()
	txLastWrite, txWriteEMA, txLastBatchFrames, txLastBatchBytes := link.txWriteSnapshot()
	rxDepth, rxCap := 0, 0
	if link.rx != nil {
		rxDepth = len(link.rx)
		rxCap = cap(link.rx)
	}
	rxEntryDepth, rxEntryCap, rxDispatchDepth, rxDispatchCap, rxDispatchWorkers := link.rxQueueSnapshot()
	token.mu.Lock()
	sessionID := strings.TrimSpace(token.sessionID)
	remoteAddr := strings.TrimSpace(token.remoteAddr)
	connectedAt := token.connectedAt
	lastReadAt := token.lastReadAt
	lastWriteAt := token.lastWriteAt
	token.mu.Unlock()
	return fmt.Sprintf(
		"link_key=%s carrier_session=%s remote=%s connected_ms=%d rx_idle_ms=%d tx_idle_ms=%d tx_queue=%d/%d tx_control=%d/%d tx_business=%d/%d tx_bulk=%d/%d tx_last_write_ms=%d tx_write_ema_ms=%d tx_last_batch_frames=%d tx_last_batch_bytes=%d rx_queue=%d/%d rx_entry_queue=%d/%d rx_dispatch_queue=%d/%d rx_dispatch_workers=%d",
		strings.TrimSpace(link.key),
		sessionID,
		remoteAddr,
		probeDurationMilliseconds(now.Sub(connectedAt)),
		probeDurationMilliseconds(now.Sub(lastReadAt)),
		probeDurationMilliseconds(now.Sub(lastWriteAt)),
		txDepth,
		txCap,
		txControlDepth,
		txControlCap,
		txBusinessDepth,
		txBusinessCap,
		txBulkDepth,
		txBulkCap,
		probeDurationMilliseconds(txLastWrite),
		probeDurationMilliseconds(txWriteEMA),
		txLastBatchFrames,
		txLastBatchBytes,
		rxDepth,
		rxCap,
		rxEntryDepth,
		rxEntryCap,
		rxDispatchDepth,
		rxDispatchCap,
		rxDispatchWorkers,
	)
}

func probeVirtualRouterFrameDebugLabel(frame probeVirtualRouterFrame, fallbackPath []string) string {
	label := fmt.Sprintf("type=%d/%d data=%d path=%s", frame.MainType, frame.SubType, len(frame.Data), probeVirtualRouterWireFramePathString(frame, fallbackPath))
	if frame.MainType != probeVirtualRouterFrameMainTypeSpeed {
		return label
	}
	if frame.SubType == probeVirtualRouterSpeedSubTypeChunk {
		if requestID, ok := parseProbeVirtualRouterSpeedChunkRequestID(frame.Data); ok {
			label += " speed_request_id=" + requestID
		}
		return label
	}
	msg := probeVirtualRouterSpeedTestResultPayload{}
	if err := json.Unmarshal(frame.Data, &msg); err == nil && strings.TrimSpace(msg.RequestID) != "" {
		label += fmt.Sprintf(" speed_request_id=%s speed_direction=%s speed_bytes=%d speed_frames=%d", strings.TrimSpace(msg.RequestID), strings.TrimSpace(msg.Direction), msg.Bytes, msg.Frames)
	}
	return label
}

func (s *probeVirtualRouterFrameLink) detachCarrier(token *probeVirtualRouterPhysicalCarrier) {
	s.detachCarrierWithReason(token, "unspecified", nil)
}

func (s *probeVirtualRouterFrameLink) detachCarrierWithReason(token *probeVirtualRouterPhysicalCarrier, reason string, err error) {
	if s == nil || token == nil {
		return
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "unspecified"
	}
	state := probeVirtualRouterFrameLinkCarrierStateString(s, token)
	detached := false
	droppedTX := 0
	droppedRX := 0
	s.mu.Lock()
	if s.carrier == token {
		s.carrier = nil
		droppedTX, droppedRX = s.clearBuffersLocked()
		s.signalCarrierChangedLocked()
		detached = true
	}
	s.mu.Unlock()
	token.close()
	if detached {
		if err != nil {
			log.Printf("probe virtual router physical carrier detached: reason=%s route=%s key=%s err=%v tx_dropped=%d rx_dropped=%d %s", reason, probeVirtualRouterRuntimeLogRouteID(s.runtime), strings.TrimSpace(s.key), err, droppedTX, droppedRX, state)
		} else {
			log.Printf("probe virtual router physical carrier detached: reason=%s route=%s key=%s tx_dropped=%d rx_dropped=%d %s", reason, probeVirtualRouterRuntimeLogRouteID(s.runtime), strings.TrimSpace(s.key), droppedTX, droppedRX, state)
		}
	}
	if detached && (droppedTX > 0 || droppedRX > 0) {
		log.Printf("probe virtual router frame buffers cleared: reason=carrier_detached_%s route=%s key=%s tx=%d rx=%d session_id=%s remote=%s", reason, probeVirtualRouterRuntimeLogRouteID(s.runtime), strings.TrimSpace(s.key), droppedTX, droppedRX, strings.TrimSpace(token.sessionID), strings.TrimSpace(token.remoteAddr))
	}
	if detached && s.runtime != nil {
		clearProbeVirtualRouterRuntimePingError(s.runtime.cfg.routeID)
		markProbeVirtualRouterPhysicalCarrierDisconnected(s.runtime, reason)
	}
}

func (c *probeVirtualRouterPhysicalCarrier) markRead() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.lastReadAt = time.Now()
	c.mu.Unlock()
}

func (c *probeVirtualRouterPhysicalCarrier) markWrite() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.lastWriteAt = time.Now()
	c.mu.Unlock()
}

func (c *probeVirtualRouterPhysicalCarrier) lastRead() time.Time {
	if c == nil {
		return time.Time{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastReadAt
}

func (c *probeVirtualRouterPhysicalCarrier) close() {
	if c == nil {
		return
	}
	c.closeOnce.Do(func() {
		if c.conn != nil {
			_ = c.conn.Close()
		}
		if c.done != nil {
			close(c.done)
		}
	})
}

func stopProbeVirtualRouterFrameLink(s *probeVirtualRouterFrameLink) {
	if s == nil {
		return
	}
	var token *probeVirtualRouterPhysicalCarrier
	droppedTX := 0
	droppedRX := 0
	s.closeOnce.Do(func() {
		s.mu.Lock()
		if s.done != nil {
			select {
			case <-s.done:
			default:
				close(s.done)
			}
		}
		token = s.carrier
		s.carrier = nil
		droppedTX, droppedRX = s.clearBuffersLocked()
		s.signalCarrierChangedLocked()
		s.mu.Unlock()
		if droppedTX > 0 || droppedRX > 0 {
			log.Printf("probe virtual router frame buffers cleared: reason=link_stopped route=%s key=%s tx=%d rx=%d", probeVirtualRouterRuntimeLogRouteID(s.runtime), strings.TrimSpace(s.key), droppedTX, droppedRX)
		}
		if token != nil {
			token.close()
		}
	})
}

func handleProbeVirtualRouterFrame(runtime *probeVirtualRouterRuntime, link *probeVirtualRouterFrameLink, frame probeVirtualRouterFrame, fallbackPath []string) error {
	control, err := probeVirtualRouterFrameControl(frame, fallbackPath)
	if err != nil {
		return err
	}
	if runtime != nil {
		if err := validateProbeVirtualRouterIngressPath(runtime, control.Path); err != nil {
			return err
		}
	}
	switch frame.MainType {
	case probeVirtualRouterFrameMainTypeIP:
		if frame.SubType != probeVirtualRouterIPSubTypeIPv4 {
			return fmt.Errorf("unsupported virtual router ip subtype=%d", frame.SubType)
		}
		return handleProbeVirtualRouterIPFrame(runtime, link, frame.Data, control.Path, control.Trace)
	case probeVirtualRouterFrameMainTypePingPong, probeVirtualRouterFrameMainTypePathRTT, probeVirtualRouterFrameMainTypeSpeed, probeVirtualRouterFrameMainTypeRouteTest, probeVirtualRouterFrameMainTypeFakeIPVerify, probeVirtualRouterFrameMainTypeDebugLog, probeVirtualRouterFrameMainTypeProxy:
		return handleProbeVirtualRouterBusinessFrame(runtime, link, frame.MainType, frame.SubType, frame.Data, control.Path)
	default:
		return fmt.Errorf("unsupported virtual router business type=%d subtype=%d", frame.MainType, frame.SubType)
	}
}

func handleProbeVirtualRouterBusinessFrame(runtime *probeVirtualRouterRuntime, link *probeVirtualRouterFrameLink, mainType uint16, subType uint16, payload []byte, framePath []string) error {
	if mainType == probeVirtualRouterFrameMainTypeProxy {
		return handleProbeVRouteProxyFrame(runtime, subType, payload, framePath)
	}
	if mainType == probeVirtualRouterFrameMainTypeSpeed && subType == probeVirtualRouterSpeedSubTypeChunk {
		return handleProbeVirtualRouterSpeedChunk(runtime, payload, framePath)
	}
	msg := probeVirtualRouterControlProbePayload{}
	if err := json.Unmarshal(payload, &msg); err != nil {
		return err
	}
	if len(msg.Path) == 0 {
		msg.Path = append([]string(nil), framePath...)
	}
	switch {
	case mainType == probeVirtualRouterFrameMainTypePingPong && subType == probeVirtualRouterPingPongSubTypePing:
		return handleProbeVirtualRouterControlPing(runtime, link, msg)
	case mainType == probeVirtualRouterFrameMainTypePingPong && subType == probeVirtualRouterPingPongSubTypePong:
		completeProbeVirtualRouterControlResponse(msg)
		return nil
	case mainType == probeVirtualRouterFrameMainTypePathRTT && subType == probeVirtualRouterPathRTTSubTypeQuery:
		return handleProbeVirtualRouterControlPathRTTQuery(runtime, msg)
	case mainType == probeVirtualRouterFrameMainTypePathRTT && subType == probeVirtualRouterPathRTTSubTypeResp:
		return handleProbeVirtualRouterControlPathRTTResponse(runtime, msg)
	case mainType == probeVirtualRouterFrameMainTypeSpeed:
		speedMsg := probeVirtualRouterSpeedTestResultPayload{}
		if err := json.Unmarshal(payload, &speedMsg); err != nil {
			return err
		}
		if len(speedMsg.Path) == 0 {
			speedMsg.Path = append([]string(nil), framePath...)
		}
		return handleProbeVirtualRouterSpeedFrame(runtime, subType, speedMsg)
	case mainType == probeVirtualRouterFrameMainTypeRouteTest:
		routeTestMsg := probeVirtualRouterRouteTestPayload{}
		if err := json.Unmarshal(payload, &routeTestMsg); err != nil {
			return err
		}
		if len(routeTestMsg.Path) == 0 {
			routeTestMsg.Path = append([]string(nil), framePath...)
		}
		return handleProbeVirtualRouterRouteTestFrame(runtime, subType, routeTestMsg)
	case mainType == probeVirtualRouterFrameMainTypeFakeIPVerify:
		verifyMsg := probeVirtualRouterFakeIPVerifyPayload{}
		if err := json.Unmarshal(payload, &verifyMsg); err != nil {
			return err
		}
		if len(verifyMsg.Path) == 0 {
			verifyMsg.Path = append([]string(nil), framePath...)
		}
		return handleProbeVirtualRouterFakeIPVerifyFrame(runtime, subType, verifyMsg)
	case mainType == probeVirtualRouterFrameMainTypeDebugLog:
		debugLogMsg := probeVirtualRouterDebugLogPayload{}
		if err := json.Unmarshal(payload, &debugLogMsg); err != nil {
			return err
		}
		if len(debugLogMsg.Path) == 0 {
			debugLogMsg.Path = append([]string(nil), framePath...)
		}
		return handleProbeVirtualRouterDebugLogFrame(runtime, subType, debugLogMsg)
	default:
		return fmt.Errorf("unsupported virtual router business type=%d subtype=%d", mainType, subType)
	}
}

func handleProbeVirtualRouterControlPing(runtime *probeVirtualRouterRuntime, link *probeVirtualRouterFrameLink, msg probeVirtualRouterControlProbePayload) error {
	localNodeID := currentProbeVirtualRouterLocalNodeIDForRuntime(runtime)
	if msg.RequestID == "" {
		return errors.New("virtual router control ping request id is empty")
	}
	response := msg
	response.OK = true
	response.Error = ""
	response.Responder = localNodeID
	response.CreatedAtUnixNano = time.Now().UnixNano()
	response.LatencyMS = 0
	payload, err := json.Marshal(response)
	if err != nil {
		return err
	}
	if link == nil {
		return errors.New("virtual router control ping carrier is unavailable")
	}
	frame, err := buildProbeVirtualRouterBusinessFrame(probeVirtualRouterFrameMainTypePingPong, probeVirtualRouterPingPongSubTypePong, payload, probeVirtualRouterReversePath(msg.Path), nil)
	if err != nil {
		return err
	}
	return link.EnqueueProbeVirtualRouterFrame(frame)
}

func handleProbeVirtualRouterControlPathRTTQuery(runtime *probeVirtualRouterRuntime, msg probeVirtualRouterControlProbePayload) error {
	localNodeID := currentProbeVirtualRouterLocalNodeIDForRuntime(runtime)
	if localNodeID == "" {
		return errors.New("local virtual router node id is empty")
	}
	msg.Path = cleanProbeVirtualRouterPath(msg.Path)
	if msg.RequestID == "" || len(msg.Path) < 2 {
		return errors.New("virtual router path rtt query is incomplete")
	}
	if err := validateProbeVirtualRouterForwardPath(msg.Path); err != nil {
		return err
	}
	if normalizeProbeRouteNodeID(msg.TargetNodeID) == localNodeID || probeVirtualRouterNextHopInPath(msg.Path, localNodeID) == "" {
		return sendProbeVirtualRouterPathRTTResponse(msg, true, 0, localNodeID, "")
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return forwardProbeVirtualRouterBusinessAlongPath(probeVirtualRouterFrameMainTypePathRTT, probeVirtualRouterPathRTTSubTypeQuery, payload, msg.Path)
}

func handleProbeVirtualRouterControlPathRTTResponse(runtime *probeVirtualRouterRuntime, msg probeVirtualRouterControlProbePayload) error {
	localNodeID := currentProbeVirtualRouterLocalNodeIDForRuntime(runtime)
	if normalizeProbeRouteNodeID(msg.SourceNodeID) == localNodeID {
		completeProbeVirtualRouterControlResponse(msg)
		return nil
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return forwardProbeVirtualRouterBusinessAlongPath(probeVirtualRouterFrameMainTypePathRTT, probeVirtualRouterPathRTTSubTypeResp, payload, probeVirtualRouterReversePath(msg.Path))
}

func sendProbeVirtualRouterPathRTTResponse(msg probeVirtualRouterControlProbePayload, ok bool, latencyMS int64, responder string, message string) error {
	msg.OK = ok
	msg.LatencyMS = latencyMS
	msg.Responder = normalizeProbeRouteNodeID(responder)
	msg.Error = strings.TrimSpace(message)
	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return forwardProbeVirtualRouterBusinessAlongPath(probeVirtualRouterFrameMainTypePathRTT, probeVirtualRouterPathRTTSubTypeResp, payload, probeVirtualRouterReversePath(msg.Path))
}

func handleProbeVirtualRouterSpeedFrame(runtime *probeVirtualRouterRuntime, subType uint16, msg probeVirtualRouterSpeedTestResultPayload) error {
	localNodeID := currentProbeVirtualRouterLocalNodeIDForRuntime(runtime)
	msg.Path = cleanProbeVirtualRouterPath(msg.Path)
	if localNodeID == "" || msg.RequestID == "" || len(msg.Path) < 2 {
		return errors.New("virtual router speed control frame is incomplete")
	}
	switch subType {
	case probeVirtualRouterSpeedSubTypeStart:
		if localNodeID != msg.Path[len(msg.Path)-1] {
			return forwardProbeVirtualRouterSpeedAlongPath(subType, msg, msg.Path)
		}
		startProbeVirtualRouterSpeedReceive(msg, localNodeID, probeVirtualRouterRuntimeLogRouteID(runtime))
		return nil
	case probeVirtualRouterSpeedSubTypeFinish:
		if localNodeID != msg.Path[len(msg.Path)-1] {
			return forwardProbeVirtualRouterSpeedAlongPath(subType, msg, msg.Path)
		}
		result, ok := finishProbeVirtualRouterSpeedReceive(msg, localNodeID)
		if !ok {
			return nil
		}
		if runtime != nil {
			recordProbeVirtualRouterRuntimeSpeedTestReceive(strings.TrimSpace(runtime.cfg.routeID), result)
		}
		if normalizeProbeRouteNodeID(result.ResultNodeID) == localNodeID {
			completeProbeVirtualRouterSpeedResponse(result)
			return nil
		}
		result.Path = probeVirtualRouterReversePath(msg.Path)
		return forwardProbeVirtualRouterSpeedAlongPath(probeVirtualRouterSpeedSubTypeResult, result, result.Path)
	case probeVirtualRouterSpeedSubTypeResult:
		if normalizeProbeRouteNodeID(msg.ResultNodeID) == localNodeID {
			completeProbeVirtualRouterSpeedResponse(msg)
			return nil
		}
		return forwardProbeVirtualRouterSpeedAlongPath(subType, msg, msg.Path)
	case probeVirtualRouterSpeedSubTypeSend:
		if localNodeID != msg.Path[len(msg.Path)-1] {
			return forwardProbeVirtualRouterSpeedAlongPath(subType, msg, msg.Path)
		}
		go func() {
			if err := runProbeVirtualRouterOneWaySpeedSender(probeVirtualRouterReversePath(msg.Path), msg, normalizeProbeVirtualRouterSpeedDuration(msg.MaxDurationMS)); err != nil {
				response := msg
				response.OK = false
				response.Error = strings.TrimSpace(err.Error())
				response.Responder = localNodeID
				response.Path = probeVirtualRouterReversePath(msg.Path)
				log.Printf("probe virtual router speed remote sender failed: request_id=%s direction=%s local=%s path=%s err=%v", strings.TrimSpace(msg.RequestID), strings.TrimSpace(msg.Direction), localNodeID, strings.Join(response.Path, ">"), err)
				if forwardErr := forwardProbeVirtualRouterSpeedAlongPath(probeVirtualRouterSpeedSubTypeResult, response, response.Path); forwardErr != nil {
					log.Printf("probe virtual router speed remote sender failure result forward failed: request_id=%s direction=%s local=%s path=%s err=%v", strings.TrimSpace(msg.RequestID), strings.TrimSpace(msg.Direction), localNodeID, strings.Join(response.Path, ">"), forwardErr)
				}
			}
		}()
		return nil
	default:
		return fmt.Errorf("unsupported virtual router speed subtype=%d", subType)
	}
}

func handleProbeVirtualRouterSpeedChunk(runtime *probeVirtualRouterRuntime, payload []byte, framePath []string) error {
	localNodeID := currentProbeVirtualRouterLocalNodeIDForRuntime(runtime)
	path := cleanProbeVirtualRouterPath(framePath)
	if localNodeID == "" || len(path) < 2 {
		return errors.New("virtual router speed chunk path is incomplete")
	}
	if localNodeID != path[len(path)-1] {
		return forwardProbeVirtualRouterBusinessAlongPath(probeVirtualRouterFrameMainTypeSpeed, probeVirtualRouterSpeedSubTypeChunk, payload, path)
	}
	requestID, ok := parseProbeVirtualRouterSpeedChunkRequestID(payload)
	if !ok {
		return errors.New("invalid virtual router speed chunk payload")
	}
	recordProbeVirtualRouterSpeedChunk(requestID, int64(len(payload)))
	return nil
}

func startProbeVirtualRouterSpeedReceive(msg probeVirtualRouterSpeedTestResultPayload, localNodeID string, routeID string) {
	now := time.Now()
	session := &probeVirtualRouterSpeedReceiveSession{
		RequestID:     strings.TrimSpace(msg.RequestID),
		Direction:     strings.TrimSpace(msg.Direction),
		SourceNodeID:  normalizeProbeRouteNodeID(msg.SourceNodeID),
		TargetNodeID:  normalizeProbeRouteNodeID(msg.TargetNodeID),
		ResultNodeID:  normalizeProbeRouteNodeID(firstNonEmpty(msg.ResultNodeID, msg.SourceNodeID)),
		Path:          append([]string(nil), msg.Path...),
		RouteID:       strings.TrimSpace(routeID),
		MaxDurationMS: msg.MaxDurationMS,
		LocalNodeID:   normalizeProbeRouteNodeID(localNodeID),
		LastAt:        now,
	}
	if session.ResultNodeID == "" {
		session.ResultNodeID = normalizeProbeRouteNodeID(localNodeID)
	}
	probeVirtualRouterSpeedReceiveState.mu.Lock()
	if probeVirtualRouterSpeedReceiveState.sessions == nil {
		probeVirtualRouterSpeedReceiveState.sessions = make(map[string]*probeVirtualRouterSpeedReceiveSession)
	}
	if probeVirtualRouterSpeedReceiveState.completed == nil {
		probeVirtualRouterSpeedReceiveState.completed = make(map[string]time.Time)
	}
	for key, item := range probeVirtualRouterSpeedReceiveState.sessions {
		if item == nil || (!item.LastAt.IsZero() && now.Sub(item.LastAt) > time.Minute) {
			delete(probeVirtualRouterSpeedReceiveState.sessions, key)
		}
	}
	cleanupProbeVirtualRouterSpeedReceiveCompletedLocked(now)
	delete(probeVirtualRouterSpeedReceiveState.completed, session.RequestID)
	probeVirtualRouterSpeedReceiveState.sessions[session.RequestID] = session
	probeVirtualRouterSpeedReceiveState.mu.Unlock()
}

func recordProbeVirtualRouterSpeedChunk(requestID string, bytes int64) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" || bytes <= 0 {
		return
	}
	now := time.Now()
	probeVirtualRouterSpeedReceiveState.mu.Lock()
	cleanupProbeVirtualRouterSpeedReceiveCompletedLocked(now)
	if _, done := probeVirtualRouterSpeedReceiveState.completed[requestID]; done {
		probeVirtualRouterSpeedReceiveState.mu.Unlock()
		return
	}
	session := probeVirtualRouterSpeedReceiveState.sessions[requestID]
	if session == nil {
		probeVirtualRouterSpeedReceiveState.mu.Unlock()
		return
	}
	shouldStartTimer := false
	if session.Frames == 0 || session.StartedAt.IsZero() {
		session.StartedAt = now
		shouldStartTimer = true
	}
	session.LastAt = now
	session.Bytes += bytes
	session.Frames++
	maxDuration := normalizeProbeVirtualRouterSpeedDuration(session.MaxDurationMS)
	localNodeID := session.LocalNodeID
	if localNodeID == "" {
		localNodeID = currentProbeVirtualRouterLocalNodeID()
	}
	if shouldStartTimer && !session.TimerStarted {
		session.TimerStarted = true
		go completeProbeVirtualRouterSpeedReceiveAfter(requestID, localNodeID, maxDuration+500*time.Millisecond)
	}
	probeVirtualRouterSpeedReceiveState.mu.Unlock()
}

func finishProbeVirtualRouterSpeedReceive(msg probeVirtualRouterSpeedTestResultPayload, localNodeID string) (probeVirtualRouterSpeedTestResultPayload, bool) {
	result, ok := finalizeProbeVirtualRouterSpeedReceive(strings.TrimSpace(msg.RequestID), localNodeID, msg)
	return result, ok
}

func finalizeProbeVirtualRouterSpeedReceive(requestID string, localNodeID string, fallback probeVirtualRouterSpeedTestResultPayload) (probeVirtualRouterSpeedTestResultPayload, bool) {
	now := time.Now()
	requestID = strings.TrimSpace(requestID)
	probeVirtualRouterSpeedReceiveState.mu.Lock()
	cleanupProbeVirtualRouterSpeedReceiveCompletedLocked(now)
	_, alreadyCompleted := probeVirtualRouterSpeedReceiveState.completed[requestID]
	session := probeVirtualRouterSpeedReceiveState.sessions[requestID]
	if session != nil {
		delete(probeVirtualRouterSpeedReceiveState.sessions, requestID)
	}
	markProbeVirtualRouterSpeedReceiveCompletedLocked(requestID, now)
	probeVirtualRouterSpeedReceiveState.mu.Unlock()
	if session == nil {
		if !alreadyCompleted {
			log.Printf("probe virtual router speed receiver finish without start: request_id=%s local=%s fallback_direction=%s fallback_path=%s", requestID, normalizeProbeRouteNodeID(localNodeID), strings.TrimSpace(fallback.Direction), strings.Join(cleanProbeVirtualRouterPath(fallback.Path), ">"))
		}
		return probeVirtualRouterSpeedTestResultPayload{}, false
	}
	result := fallback
	if result.RequestID == "" {
		result.RequestID = session.RequestID
	}
	if result.Direction == "" {
		result.Direction = session.Direction
	}
	if result.SourceNodeID == "" {
		result.SourceNodeID = session.SourceNodeID
	}
	if result.TargetNodeID == "" {
		result.TargetNodeID = session.TargetNodeID
	}
	if result.ResultNodeID == "" {
		result.ResultNodeID = session.ResultNodeID
	}
	if len(result.Path) == 0 {
		result.Path = append([]string(nil), session.Path...)
	}
	result.OK = true
	result.Error = ""
	result.Responder = normalizeProbeRouteNodeID(localNodeID)
	result.RuntimeRouteID = strings.TrimSpace(session.RouteID)
	result.Bytes = session.Bytes
	result.Frames = session.Frames
	if !session.StartedAt.IsZero() && session.Frames > 0 {
		result.DurationMS = probeDurationMilliseconds(now.Sub(session.StartedAt))
		if result.DurationMS <= 0 {
			result.DurationMS = 1
		}
	}
	result.Mbps = probeVirtualRouterSpeedMbps(result.Bytes, result.DurationMS)
	if result.ResultNodeID == "" {
		result.ResultNodeID = normalizeProbeRouteNodeID(firstNonEmpty(fallback.ResultNodeID, fallback.SourceNodeID))
	}
	return result, true
}

func cleanupProbeVirtualRouterSpeedReceiveCompletedLocked(now time.Time) {
	if probeVirtualRouterSpeedReceiveState.completed == nil {
		probeVirtualRouterSpeedReceiveState.completed = make(map[string]time.Time)
		return
	}
	for requestID, completedAt := range probeVirtualRouterSpeedReceiveState.completed {
		if requestID == "" || completedAt.IsZero() || now.Sub(completedAt) > probeVirtualRouterSpeedReceiveCompletedTTL {
			delete(probeVirtualRouterSpeedReceiveState.completed, requestID)
		}
	}
}

func markProbeVirtualRouterSpeedReceiveCompletedLocked(requestID string, completedAt time.Time) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return
	}
	if probeVirtualRouterSpeedReceiveState.completed == nil {
		probeVirtualRouterSpeedReceiveState.completed = make(map[string]time.Time)
	}
	probeVirtualRouterSpeedReceiveState.completed[requestID] = completedAt
	cleanupProbeVirtualRouterSpeedReceiveCompletedLocked(completedAt)
}

func completeProbeVirtualRouterSpeedReceiveAfter(requestID string, localNodeID string, delay time.Duration) {
	if delay <= 0 {
		delay = probeVirtualRouterSpeedTestMaxDuration
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	<-timer.C
	result, ok := finalizeProbeVirtualRouterSpeedReceive(requestID, localNodeID, probeVirtualRouterSpeedTestResultPayload{})
	if !ok {
		return
	}
	if normalizeProbeRouteNodeID(result.ResultNodeID) == normalizeProbeRouteNodeID(localNodeID) {
		recordProbeVirtualRouterRuntimeSpeedTestReceive(result.RuntimeRouteID, result)
		completeProbeVirtualRouterSpeedResponse(result)
		return
	}
	recordProbeVirtualRouterRuntimeSpeedTestReceive(result.RuntimeRouteID, result)
	result.Path = probeVirtualRouterReversePath(result.Path)
	if err := forwardProbeVirtualRouterSpeedAlongPath(probeVirtualRouterSpeedSubTypeResult, result, result.Path); err != nil {
		log.Printf("probe virtual router speed timed result forward failed: request_id=%s path=%s err=%v", strings.TrimSpace(result.RequestID), strings.Join(result.Path, ">"), err)
	}
}

func forwardProbeVirtualRouterSpeedAlongPath(subType uint16, msg probeVirtualRouterSpeedTestResultPayload, path []string) error {
	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return forwardProbeVirtualRouterBusinessAlongPath(probeVirtualRouterFrameMainTypeSpeed, subType, payload, path)
}

func queryProbeVirtualRouterAdjacentPing(rt *probeVirtualRouterRuntime, direction string, targetNodeID string) (probeVirtualRouterControlProbePayload, error) {
	if rt == nil {
		return probeVirtualRouterControlProbePayload{}, errors.New("runtime is nil")
	}
	targetNodeID = normalizeProbeRouteNodeID(targetNodeID)
	if targetNodeID == "" {
		return probeVirtualRouterControlProbePayload{}, errors.New("adjacent virtual router node is unavailable")
	}
	localNodeID := currentProbeVirtualRouterLocalNodeIDForRuntime(rt)
	path := []string{localNodeID, targetNodeID}
	link, err := ensureProbeVirtualRouterFrameLink(rt, direction, "", path)
	if err != nil {
		return probeVirtualRouterControlProbePayload{}, err
	}
	requestID := newProbeTCPDebugFlowID("vrouter_control_ping", rt.cfg.routeID)
	waiter := registerProbeVirtualRouterControlResponse(requestID)
	defer unregisterProbeVirtualRouterControlResponse(requestID)
	startedAt := time.Now()
	msg := probeVirtualRouterControlProbePayload{
		RequestID:         requestID,
		SourceNodeID:      localNodeID,
		TargetNodeID:      targetNodeID,
		Path:              path,
		CreatedAtUnixNano: startedAt.UnixNano(),
		PingBytes:         probeVirtualRouterPingPongBytes,
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		return probeVirtualRouterControlProbePayload{}, err
	}
	if err := enqueueProbeVirtualRouterBusinessFrame(link, probeVirtualRouterFrameMainTypePingPong, probeVirtualRouterPingPongSubTypePing, payload, path); err != nil {
		return probeVirtualRouterControlProbePayload{}, err
	}
	response, err := waitProbeVirtualRouterControlResponse(waiter, probeVirtualRouterPingPongTimeout)
	if err != nil {
		return probeVirtualRouterControlProbePayload{}, fmt.Errorf("%w: request_id=%s target=%s direction=%s path=%s %s", err, requestID, targetNodeID, normalizeProbeRouteBridgeRole(direction), strings.Join(path, ">"), probeVirtualRouterFrameLinkDebugState(link))
	}
	response.LatencyMS = probeVirtualRouterAdjacentLatencyMilliseconds(time.Since(startedAt))
	return response, nil
}

func probeVirtualRouterAdjacentLatencyMilliseconds(elapsed time.Duration) int64 {
	return probeDurationMilliseconds(elapsed)
}

func probeVirtualRouterPathLatencyMilliseconds(sentAtUnixNano int64, receivedAtUnixNano int64) (int64, error) {
	if sentAtUnixNano <= 0 || receivedAtUnixNano < sentAtUnixNano {
		return 0, errors.New("invalid virtual router path rtt source timestamp")
	}
	return probeDurationMilliseconds(time.Duration(receivedAtUnixNano - sentAtUnixNano)), nil
}

func queryProbeVirtualRouterPathRTTControl(path []string) (probeVirtualRouterPathRTTQueryResponse, error) {
	cleanPath := cleanProbeVirtualRouterPath(path)
	localNodeID := currentProbeVirtualRouterLocalNodeID()
	if len(cleanPath) < 2 || localNodeID == "" || cleanPath[0] != localNodeID {
		return probeVirtualRouterPathRTTQueryResponse{}, errors.New("virtual router rtt query path must start at local node")
	}
	if err := validateProbeVirtualRouterForwardPath(cleanPath); err != nil {
		return probeVirtualRouterPathRTTQueryResponse{}, err
	}
	targetNodeID := cleanPath[len(cleanPath)-1]
	if len(cleanPath) == 2 {
		rt, direction := probeVirtualRouterRuntimeForAdjacentNode(targetNodeID)
		if rt == nil {
			return probeVirtualRouterPathRTTQueryResponse{}, errors.New("adjacent virtual router rtt runtime is unavailable")
		}
		response, err := queryProbeVirtualRouterAdjacentPing(rt, direction, targetNodeID)
		if err != nil {
			return probeVirtualRouterPathRTTQueryResponse{}, err
		}
		return probeVirtualRouterPathRTTQueryResponse{
			OK:        response.OK,
			LatencyMS: response.LatencyMS,
			Error:     response.Error,
			Responder: response.Responder,
		}, nil
	}
	nextNodeID := probeVirtualRouterNextHopInPath(cleanPath, localNodeID)
	if nextNodeID == "" {
		return probeVirtualRouterPathRTTQueryResponse{}, errors.New("next virtual router rtt hop is unavailable")
	}
	rt, direction := probeVirtualRouterRuntimeForAdjacentNode(nextNodeID)
	if rt == nil {
		return probeVirtualRouterPathRTTQueryResponse{}, errors.New("adjacent virtual router rtt runtime is unavailable")
	}
	link, err := ensureProbeVirtualRouterFrameLink(rt, direction, "", cleanPath)
	if err != nil {
		return probeVirtualRouterPathRTTQueryResponse{}, err
	}
	requestID := newProbeTCPDebugFlowID("vrouter_path_rtt", strings.Join(cleanPath, ">"))
	waiter := registerProbeVirtualRouterControlResponse(requestID)
	defer unregisterProbeVirtualRouterControlResponse(requestID)
	sentAtUnixNano := time.Now().UnixNano()
	msg := probeVirtualRouterControlProbePayload{
		RequestID:         requestID,
		SourceNodeID:      localNodeID,
		TargetNodeID:      targetNodeID,
		Path:              cleanPath,
		CreatedAtUnixNano: sentAtUnixNano,
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		return probeVirtualRouterPathRTTQueryResponse{}, err
	}
	if err := enqueueProbeVirtualRouterBusinessFrame(link, probeVirtualRouterFrameMainTypePathRTT, probeVirtualRouterPathRTTSubTypeQuery, payload, cleanPath); err != nil {
		return probeVirtualRouterPathRTTQueryResponse{}, err
	}
	response, err := waitProbeVirtualRouterControlResponse(waiter, probeVirtualRouterPingPongTimeout)
	if err != nil {
		return probeVirtualRouterPathRTTQueryResponse{}, err
	}
	latencyMS, err := probeVirtualRouterPathLatencyMilliseconds(sentAtUnixNano, time.Now().UnixNano())
	if err != nil {
		return probeVirtualRouterPathRTTQueryResponse{}, err
	}
	return probeVirtualRouterPathRTTQueryResponse{
		OK:        response.OK,
		LatencyMS: latencyMS,
		Error:     response.Error,
		Responder: response.Responder,
	}, nil
}

func runProbeVirtualRouterSpeedTest(sourceNodeID string, targetNodeID string, maxBytes int64, maxDuration time.Duration) (probeVirtualRouterSpeedTestResult, probeVirtualRouterSpeedTestResult, string, error) {
	sourceNodeID = normalizeProbeRouteNodeID(sourceNodeID)
	targetNodeID = normalizeProbeRouteNodeID(targetNodeID)
	localNodeID := currentProbeVirtualRouterLocalNodeID()
	if sourceNodeID == "" {
		sourceNodeID = localNodeID
	}
	if sourceNodeID == "" || targetNodeID == "" {
		return probeVirtualRouterSpeedTestResult{}, probeVirtualRouterSpeedTestResult{}, "", errors.New("source and target virtual router nodes are required")
	}
	if sourceNodeID != localNodeID {
		return probeVirtualRouterSpeedTestResult{}, probeVirtualRouterSpeedTestResult{}, "", errors.New("virtual router speed test must run on the selected source node")
	}
	if sourceNodeID == targetNodeID {
		return probeVirtualRouterSpeedTestResult{}, probeVirtualRouterSpeedTestResult{}, "", errors.New("source and target virtual router nodes must be different")
	}
	if maxBytes <= 0 || maxBytes > probeVirtualRouterSpeedTestMaxBytes {
		maxBytes = probeVirtualRouterSpeedTestMaxBytes
	}
	if maxDuration <= 0 || maxDuration > probeVirtualRouterSpeedTestMaxDuration {
		maxDuration = probeVirtualRouterSpeedTestMaxDuration
	}
	path := currentProbeVirtualRouterPathBetweenNodes(sourceNodeID, targetNodeID)
	if len(path) < 2 {
		return probeVirtualRouterSpeedTestResult{}, probeVirtualRouterSpeedTestResult{}, "", errors.New("virtual router speed test path is unavailable")
	}
	up, upErr := runProbeVirtualRouterOneWaySpeedTest(path, "up", sourceNodeID, targetNodeID, maxBytes, maxDuration)
	down, downErr := runProbeVirtualRouterReverseSpeedTest(path, sourceNodeID, targetNodeID, maxBytes, maxDuration)
	pathText := strings.Join(path, ">")
	var err error
	if upErr != nil && downErr != nil {
		err = fmt.Errorf("up: %v; down: %v", upErr, downErr)
	} else if upErr != nil {
		err = fmt.Errorf("up: %v", upErr)
	} else if downErr != nil {
		err = fmt.Errorf("down: %v", downErr)
	}
	routeID := ""
	if nextNodeID := probeVirtualRouterNextHopInPath(path, localNodeID); nextNodeID != "" {
		if rt, _ := probeVirtualRouterRuntimeForAdjacentNode(nextNodeID); rt != nil {
			routeID = strings.TrimSpace(rt.cfg.routeID)
		}
	}
	recordProbeVirtualRouterRuntimeSpeedTest(routeID, sourceNodeID, targetNodeID, pathText, up, down, err)
	errText := ""
	if err != nil {
		errText = strings.TrimSpace(err.Error())
	}
	log.Printf("probe virtual router speed test summary: source=%s target=%s path=%s max_bytes=%d max_duration_ms=%d up_ok=%v up_bytes=%d up_duration_ms=%d up_mbps=%.2f up_error=%q down_ok=%v down_bytes=%d down_duration_ms=%d down_mbps=%.2f down_error=%q err=%q", sourceNodeID, targetNodeID, pathText, maxBytes, probeDurationMilliseconds(maxDuration), up.OK, up.Bytes, up.DurationMS, up.Mbps, strings.TrimSpace(up.Error), down.OK, down.Bytes, down.DurationMS, down.Mbps, strings.TrimSpace(down.Error), errText)
	return up, down, pathText, err
}

func runProbeVirtualRouterOneWaySpeedTest(path []string, directionName string, sourceNodeID string, targetNodeID string, maxBytes int64, maxDuration time.Duration) (probeVirtualRouterSpeedTestResult, error) {
	requestID := newProbeTCPDebugFlowID("vrouter_speed_"+directionName, strings.Join(path, ">"))
	waiter := registerProbeVirtualRouterSpeedResponse(requestID)
	defer unregisterProbeVirtualRouterSpeedResponse(requestID)
	msg := probeVirtualRouterSpeedTestResultPayload{
		RequestID:         requestID,
		Direction:         directionName,
		SourceNodeID:      sourceNodeID,
		TargetNodeID:      targetNodeID,
		ResultNodeID:      sourceNodeID,
		Path:              append([]string(nil), path...),
		MaxBytes:          maxBytes,
		MaxDurationMS:     probeDurationMilliseconds(maxDuration),
		CreatedAtUnixNano: time.Now().UnixNano(),
	}
	if err := runProbeVirtualRouterOneWaySpeedSender(path, msg, maxDuration); err != nil {
		log.Printf("probe virtual router speed test send failed: request_id=%s direction=%s source=%s target=%s path=%s err=%v", requestID, directionName, sourceNodeID, targetNodeID, strings.Join(path, ">"), err)
		return probeVirtualRouterSpeedTestResult{OK: false, Error: err.Error()}, err
	}
	response, err := waitProbeVirtualRouterSpeedResponse(waiter, maxDuration+10*time.Second)
	if err != nil {
		log.Printf("probe virtual router speed test response failed: request_id=%s direction=%s source=%s target=%s path=%s err=%v", requestID, directionName, sourceNodeID, targetNodeID, strings.Join(path, ">"), err)
		return probeVirtualRouterSpeedTestResult{OK: false, Error: err.Error()}, err
	}
	result := probeVirtualRouterSpeedTestResult{
		OK:         response.OK,
		Error:      strings.TrimSpace(response.Error),
		Bytes:      response.Bytes,
		Frames:     response.Frames,
		DurationMS: response.DurationMS,
		Mbps:       response.Mbps,
	}
	if !response.OK && result.Error == "" {
		result.Error = "virtual router speed test failed"
	}
	return result, nil
}

func runProbeVirtualRouterReverseSpeedTest(path []string, sourceNodeID string, targetNodeID string, maxBytes int64, maxDuration time.Duration) (probeVirtualRouterSpeedTestResult, error) {
	requestID := newProbeTCPDebugFlowID("vrouter_speed_down", strings.Join(path, ">"))
	waiter := registerProbeVirtualRouterSpeedResponse(requestID)
	defer unregisterProbeVirtualRouterSpeedResponse(requestID)
	msg := probeVirtualRouterSpeedTestResultPayload{
		RequestID:         requestID,
		Direction:         "down",
		SourceNodeID:      sourceNodeID,
		TargetNodeID:      targetNodeID,
		ResultNodeID:      sourceNodeID,
		Path:              append([]string(nil), path...),
		MaxBytes:          maxBytes,
		MaxDurationMS:     probeDurationMilliseconds(maxDuration),
		CreatedAtUnixNano: time.Now().UnixNano(),
	}
	if err := forwardProbeVirtualRouterSpeedAlongPath(probeVirtualRouterSpeedSubTypeSend, msg, path); err != nil {
		log.Printf("probe virtual router speed test remote sender request failed: request_id=%s direction=down source=%s target=%s path=%s err=%v", requestID, sourceNodeID, targetNodeID, strings.Join(path, ">"), err)
		return probeVirtualRouterSpeedTestResult{OK: false, Error: err.Error()}, err
	}
	response, err := waitProbeVirtualRouterSpeedResponse(waiter, maxDuration+10*time.Second)
	if err != nil {
		log.Printf("probe virtual router speed test remote sender response failed: request_id=%s direction=down source=%s target=%s path=%s err=%v", requestID, sourceNodeID, targetNodeID, strings.Join(path, ">"), err)
		return probeVirtualRouterSpeedTestResult{OK: false, Error: err.Error()}, err
	}
	result := probeVirtualRouterSpeedTestResult{
		OK:         response.OK,
		Error:      strings.TrimSpace(response.Error),
		Bytes:      response.Bytes,
		Frames:     response.Frames,
		DurationMS: response.DurationMS,
		Mbps:       response.Mbps,
	}
	if !response.OK && result.Error == "" {
		result.Error = "virtual router speed test failed"
	}
	return result, nil
}

func normalizeProbeVirtualRouterSpeedDuration(maxDurationMS int64) time.Duration {
	if maxDurationMS <= 0 || maxDurationMS > probeDurationMilliseconds(probeVirtualRouterSpeedTestMaxDuration) {
		return probeVirtualRouterSpeedTestMaxDuration
	}
	return time.Duration(maxDurationMS) * time.Millisecond
}

func runProbeVirtualRouterOneWaySpeedSender(path []string, msg probeVirtualRouterSpeedTestResultPayload, maxDuration time.Duration) error {
	cleanPath := cleanProbeVirtualRouterPath(path)
	localNodeID := currentProbeVirtualRouterLocalNodeID()
	if localNodeID == "" || len(cleanPath) < 2 || cleanPath[0] != localNodeID {
		return errors.New("virtual router speed sender path must start at local node")
	}
	nextNodeID := probeVirtualRouterNextHopInPath(cleanPath, localNodeID)
	if nextNodeID == "" {
		return errors.New("next virtual router speed hop is unavailable")
	}
	rt, direction := probeVirtualRouterRuntimeForAdjacentNode(nextNodeID)
	if rt == nil {
		return errors.New("adjacent virtual router speed runtime is unavailable")
	}
	link, err := ensureProbeVirtualRouterFrameLink(rt, direction, "", cleanPath)
	if err != nil {
		return err
	}
	msg.Path = cleanPath
	msg.MaxBytes = normalizeProbeVirtualRouterSpeedMaxBytes(msg.MaxBytes)
	if maxDuration <= 0 || maxDuration > probeVirtualRouterSpeedTestMaxDuration {
		maxDuration = probeVirtualRouterSpeedTestMaxDuration
	}
	if msg.MaxDurationMS <= 0 {
		msg.MaxDurationMS = probeDurationMilliseconds(maxDuration)
	}
	startPayload, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if err := enqueueProbeVirtualRouterBusinessFrameUntil(link, probeVirtualRouterFrameMainTypeSpeed, probeVirtualRouterSpeedSubTypeStart, startPayload, cleanPath, time.Now().Add(2*time.Second)); err != nil {
		log.Printf("probe virtual router speed sender start frame failed: request_id=%s direction=%s route=%s path=%s err=%v %s", strings.TrimSpace(msg.RequestID), strings.TrimSpace(msg.Direction), probeVirtualRouterRuntimeLogRouteID(rt), strings.Join(cleanPath, ">"), err, probeVirtualRouterFrameLinkDebugState(link))
		return err
	}
	deadline := time.Now().Add(maxDuration)
	var sentBytes int64
	var frames int64
	for sentBytes < msg.MaxBytes && time.Now().Before(deadline) {
		if err := probeVirtualRouterSpeedCarrierAvailable(link); err != nil {
			log.Printf("probe virtual router speed sender carrier unavailable: request_id=%s direction=%s route=%s bytes=%d frames=%d path=%s err=%v %s", strings.TrimSpace(msg.RequestID), strings.TrimSpace(msg.Direction), probeVirtualRouterRuntimeLogRouteID(rt), sentBytes, frames, strings.Join(cleanPath, ">"), err, probeVirtualRouterFrameLinkDebugState(link))
			return err
		}
		if err := waitProbeVirtualRouterSpeedTXBackpressure(link, deadline); err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				break
			}
			log.Printf("probe virtual router speed sender tx wait failed: request_id=%s direction=%s route=%s bytes=%d frames=%d path=%s err=%v %s", strings.TrimSpace(msg.RequestID), strings.TrimSpace(msg.Direction), probeVirtualRouterRuntimeLogRouteID(rt), sentBytes, frames, strings.Join(cleanPath, ">"), err, probeVirtualRouterFrameLinkDebugState(link))
			return err
		}
		if err := probeVirtualRouterSpeedCarrierAvailable(link); err != nil {
			log.Printf("probe virtual router speed sender carrier unavailable after wait: request_id=%s direction=%s route=%s bytes=%d frames=%d path=%s err=%v %s", strings.TrimSpace(msg.RequestID), strings.TrimSpace(msg.Direction), probeVirtualRouterRuntimeLogRouteID(rt), sentBytes, frames, strings.Join(cleanPath, ">"), err, probeVirtualRouterFrameLinkDebugState(link))
			return err
		}
		size := int64(probeVirtualRouterSpeedTestChunkBytes)
		if remain := msg.MaxBytes - sentBytes; remain < size {
			size = remain
		}
		payload := buildProbeVirtualRouterSpeedChunkPayload(msg.RequestID, int(size))
		if err := enqueueProbeVirtualRouterBusinessFrameUntil(link, probeVirtualRouterFrameMainTypeSpeed, probeVirtualRouterSpeedSubTypeChunk, payload, cleanPath, deadline); err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				break
			}
			log.Printf("probe virtual router speed sender chunk enqueue failed: request_id=%s direction=%s route=%s bytes=%d frames=%d path=%s err=%v %s", strings.TrimSpace(msg.RequestID), strings.TrimSpace(msg.Direction), probeVirtualRouterRuntimeLogRouteID(rt), sentBytes, frames, strings.Join(cleanPath, ">"), err, probeVirtualRouterFrameLinkDebugState(link))
			return err
		}
		sentBytes += int64(len(payload))
		frames++
	}
	msg.Bytes = sentBytes
	msg.Frames = frames
	finishPayload, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if err := probeVirtualRouterSpeedCarrierAvailable(link); err != nil {
		log.Printf("probe virtual router speed sender carrier unavailable before finish: request_id=%s direction=%s route=%s bytes=%d frames=%d path=%s err=%v %s", strings.TrimSpace(msg.RequestID), strings.TrimSpace(msg.Direction), probeVirtualRouterRuntimeLogRouteID(rt), sentBytes, frames, strings.Join(cleanPath, ">"), err, probeVirtualRouterFrameLinkDebugState(link))
		return err
	}
	err = enqueueProbeVirtualRouterBusinessFrameUntil(link, probeVirtualRouterFrameMainTypeSpeed, probeVirtualRouterSpeedSubTypeFinish, finishPayload, cleanPath, time.Now().Add(2*time.Second))
	if err != nil {
		log.Printf("probe virtual router speed sender finish frame failed: request_id=%s direction=%s route=%s bytes=%d frames=%d path=%s err=%v %s", strings.TrimSpace(msg.RequestID), strings.TrimSpace(msg.Direction), probeVirtualRouterRuntimeLogRouteID(rt), sentBytes, frames, strings.Join(cleanPath, ">"), err, probeVirtualRouterFrameLinkDebugState(link))
		return err
	}
	return nil
}

func probeVirtualRouterSpeedCarrierAvailable(link *probeVirtualRouterFrameLink) error {
	if link == nil {
		return errors.New("virtual router speed carrier link is nil")
	}
	_, err := link.currentCarrier()
	return err
}

func waitProbeVirtualRouterSpeedTXBackpressure(link *probeVirtualRouterFrameLink, deadline time.Time) error {
	if link == nil || link.txBulk == nil || link.done == nil {
		return nil
	}
	capacity := cap(link.txBulk)
	if capacity <= 0 {
		return nil
	}
	high, low := probeVirtualRouterSpeedTXWatermarks(capacity)
	if len(link.txBulk) < high {
		return nil
	}
	ticker := time.NewTicker(2 * time.Millisecond)
	defer ticker.Stop()
	for {
		if len(link.txBulk) <= low {
			return nil
		}
		wait := time.Until(deadline)
		if wait <= 0 {
			return os.ErrDeadlineExceeded
		}
		timer := time.NewTimer(wait)
		select {
		case <-link.done:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return io.ErrClosedPipe
		case <-ticker.C:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
			return os.ErrDeadlineExceeded
		}
	}
}

func probeVirtualRouterSpeedTXWatermarks(capacity int) (high int, low int) {
	if capacity <= 0 {
		return 0, 0
	}
	high = capacity * probeVirtualRouterSpeedTestTXHighWatermarkPercent / 100
	if high <= 0 {
		high = capacity
	}
	low = capacity * probeVirtualRouterSpeedTestTXLowWatermarkPercent / 100
	if low < 0 {
		low = 0
	}
	framesForBytes := func(bytes int) int {
		return (bytes + probeVirtualRouterSpeedTestChunkBytes - 1) / probeVirtualRouterSpeedTestChunkBytes
	}
	if byteHigh := framesForBytes(probeVirtualRouterSpeedTestTXHighWatermarkBytes); byteHigh < high {
		high = byteHigh
	}
	if byteLow := framesForBytes(probeVirtualRouterSpeedTestTXLowWatermarkBytes); byteLow < low {
		low = byteLow
	}
	if high < 1 {
		high = 1
	}
	if low >= high {
		low = high - 1
	}
	return high, low
}

func normalizeProbeVirtualRouterSpeedMaxBytes(value int64) int64 {
	if value <= 0 || value > probeVirtualRouterSpeedTestMaxBytes {
		return probeVirtualRouterSpeedTestMaxBytes
	}
	return value
}

func buildProbeVirtualRouterSpeedChunkPayload(requestID string, size int) []byte {
	requestID = strings.TrimSpace(requestID)
	headerSize := 6 + len(requestID)
	if size < headerSize {
		size = headerSize
	}
	payload := make([]byte, size)
	copy(payload[0:4], []byte("VRS1"))
	binary.BigEndian.PutUint16(payload[4:6], uint16(len(requestID)))
	copy(payload[6:6+len(requestID)], []byte(requestID))
	return payload
}

func parseProbeVirtualRouterSpeedChunkRequestID(payload []byte) (string, bool) {
	if len(payload) < 6 || string(payload[0:4]) != "VRS1" {
		return "", false
	}
	size := int(binary.BigEndian.Uint16(payload[4:6]))
	if size <= 0 || 6+size > len(payload) {
		return "", false
	}
	requestID := strings.TrimSpace(string(payload[6 : 6+size]))
	return requestID, requestID != ""
}

func probeVirtualRouterSpeedMbps(bytes int64, durationMS int64) float64 {
	if bytes <= 0 || durationMS <= 0 {
		return 0
	}
	return float64(bytes*8) / (float64(durationMS) / 1000) / 1000 / 1000
}

func forwardProbeVirtualRouterBusinessAlongPath(mainType uint16, subType uint16, payload []byte, path []string) error {
	cleanPath := cleanProbeVirtualRouterPath(path)
	if err := validateProbeVirtualRouterForwardPath(cleanPath); err != nil {
		return err
	}
	localNodeID := currentProbeVirtualRouterLocalNodeID()
	if localNodeID == "" {
		return errors.New("local virtual router node id is empty")
	}
	nextNodeID := probeVirtualRouterNextHopInPath(cleanPath, localNodeID)
	if nextNodeID == "" {
		return errors.New("next virtual router business frame hop is unavailable")
	}
	rt, direction := probeVirtualRouterRuntimeForAdjacentNode(nextNodeID)
	if rt == nil {
		return errors.New("adjacent virtual router business frame runtime is unavailable")
	}
	link, err := ensureProbeVirtualRouterFrameLink(rt, direction, "", cleanPath)
	if err != nil {
		return err
	}
	return enqueueProbeVirtualRouterBusinessFrame(link, mainType, subType, payload, cleanPath)
}

func enqueueProbeVirtualRouterBusinessFrame(link *probeVirtualRouterFrameLink, mainType uint16, subType uint16, payload []byte, path []string) error {
	if link == nil {
		return errors.New("virtual router physical carrier is unavailable")
	}
	frame, err := buildProbeVirtualRouterBusinessFrame(mainType, subType, payload, path, nil)
	if err != nil {
		return err
	}
	return link.EnqueueProbeVirtualRouterFrame(frame)
}

func enqueueProbeVirtualRouterBusinessFrameUntil(link *probeVirtualRouterFrameLink, mainType uint16, subType uint16, payload []byte, path []string, deadline time.Time) error {
	if link == nil {
		return errors.New("virtual router physical carrier is unavailable")
	}
	frame, err := buildProbeVirtualRouterBusinessFrame(mainType, subType, payload, path, nil)
	if err != nil {
		return err
	}
	queue, _ := link.txQueueForFrame(frame)
	if queue == nil || link.done == nil {
		return link.EnqueueProbeVirtualRouterFrame(frame)
	}
	wait := time.Until(deadline)
	if wait <= 0 {
		return os.ErrDeadlineExceeded
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case queue <- frame:
		link.touch()
		return nil
	case <-link.done:
		return io.ErrClosedPipe
	case <-timer.C:
		return os.ErrDeadlineExceeded
	}
}

func registerProbeVirtualRouterControlResponse(requestID string) chan probeVirtualRouterControlProbePayload {
	requestID = strings.TrimSpace(requestID)
	ch := make(chan probeVirtualRouterControlProbePayload, 1)
	if requestID == "" {
		return ch
	}
	probeVirtualRouterControlResponseState.mu.Lock()
	if probeVirtualRouterControlResponseState.pending == nil {
		probeVirtualRouterControlResponseState.pending = make(map[string]chan probeVirtualRouterControlProbePayload)
	}
	probeVirtualRouterControlResponseState.pending[requestID] = ch
	probeVirtualRouterControlResponseState.mu.Unlock()
	return ch
}

func unregisterProbeVirtualRouterControlResponse(requestID string) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return
	}
	probeVirtualRouterControlResponseState.mu.Lock()
	delete(probeVirtualRouterControlResponseState.pending, requestID)
	probeVirtualRouterControlResponseState.mu.Unlock()
}

func waitProbeVirtualRouterControlResponse(ch chan probeVirtualRouterControlProbePayload, timeout time.Duration) (probeVirtualRouterControlProbePayload, error) {
	if ch == nil {
		return probeVirtualRouterControlProbePayload{}, errors.New("virtual router control response waiter is nil")
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case response := <-ch:
		return response, nil
	case <-timer.C:
		return probeVirtualRouterControlProbePayload{}, errors.New("virtual router control response timeout")
	}
}

func completeProbeVirtualRouterControlResponse(msg probeVirtualRouterControlProbePayload) {
	requestID := strings.TrimSpace(msg.RequestID)
	if requestID == "" {
		return
	}
	probeVirtualRouterControlResponseState.mu.Lock()
	ch := probeVirtualRouterControlResponseState.pending[requestID]
	probeVirtualRouterControlResponseState.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- msg:
	default:
	}
}

func registerProbeVirtualRouterSpeedResponse(requestID string) chan probeVirtualRouterSpeedTestResultPayload {
	requestID = strings.TrimSpace(requestID)
	ch := make(chan probeVirtualRouterSpeedTestResultPayload, 1)
	if requestID == "" {
		return ch
	}
	probeVirtualRouterSpeedResponseState.mu.Lock()
	if probeVirtualRouterSpeedResponseState.pending == nil {
		probeVirtualRouterSpeedResponseState.pending = make(map[string]chan probeVirtualRouterSpeedTestResultPayload)
	}
	probeVirtualRouterSpeedResponseState.pending[requestID] = ch
	probeVirtualRouterSpeedResponseState.mu.Unlock()
	return ch
}

func unregisterProbeVirtualRouterSpeedResponse(requestID string) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return
	}
	probeVirtualRouterSpeedResponseState.mu.Lock()
	delete(probeVirtualRouterSpeedResponseState.pending, requestID)
	probeVirtualRouterSpeedResponseState.mu.Unlock()
}

func waitProbeVirtualRouterSpeedResponse(ch chan probeVirtualRouterSpeedTestResultPayload, timeout time.Duration) (probeVirtualRouterSpeedTestResultPayload, error) {
	if ch == nil {
		return probeVirtualRouterSpeedTestResultPayload{}, errors.New("virtual router speed response waiter is nil")
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case response := <-ch:
		return response, nil
	case <-timer.C:
		return probeVirtualRouterSpeedTestResultPayload{}, errors.New("virtual router speed response timeout")
	}
}

func completeProbeVirtualRouterSpeedResponse(msg probeVirtualRouterSpeedTestResultPayload) {
	requestID := strings.TrimSpace(msg.RequestID)
	if requestID == "" {
		return
	}
	probeVirtualRouterSpeedResponseState.mu.Lock()
	ch := probeVirtualRouterSpeedResponseState.pending[requestID]
	probeVirtualRouterSpeedResponseState.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- msg:
	default:
	}
}

func cleanProbeVirtualRouterPath(path []string) []string {
	out := make([]string, 0, len(path))
	for _, item := range path {
		if nodeID := normalizeProbeRouteNodeID(item); nodeID != "" {
			out = append(out, nodeID)
		}
	}
	return out
}

func validateProbeVirtualRouterForwardPath(path []string) error {
	cleanPath := cleanProbeVirtualRouterPath(path)
	if len(cleanPath) < 2 {
		return errors.New("virtual router business frame path is incomplete")
	}
	if len(cleanPath)-1 > probeVirtualRouterPathRecoveryMaxHops {
		return fmt.Errorf("virtual router path exceeds maximum hops: hops=%d max=%d", len(cleanPath)-1, probeVirtualRouterPathRecoveryMaxHops)
	}
	seen := make(map[string]struct{}, len(cleanPath))
	for _, nodeID := range cleanPath {
		if _, exists := seen[nodeID]; exists {
			return fmt.Errorf("virtual router path contains a loop at node=%s", nodeID)
		}
		seen[nodeID] = struct{}{}
	}
	return nil
}

func validateProbeVirtualRouterIngressPath(runtime *probeVirtualRouterRuntime, path []string) error {
	if runtime == nil {
		return errors.New("virtual router runtime is required")
	}
	cleanPath := cleanProbeVirtualRouterPath(path)
	if err := validateProbeVirtualRouterForwardPath(cleanPath); err != nil {
		return err
	}
	localNodeID := normalizeProbeRouteNodeID(runtime.cfg.localNodeID)
	peerNodeID := normalizeProbeRouteNodeID(runtime.cfg.peerNodeID)
	if localNodeID == "" || peerNodeID == "" {
		return errors.New("virtual router ingress identity is incomplete")
	}
	localIndex := -1
	for index, nodeID := range cleanPath {
		if nodeID == localNodeID {
			localIndex = index
			break
		}
	}
	if localIndex <= 0 || cleanPath[localIndex-1] != peerNodeID {
		return fmt.Errorf("virtual router ingress peer mismatch: local=%s peer=%s path=%s", localNodeID, peerNodeID, strings.Join(cleanPath, ">"))
	}

	probeVirtualRouterState.mu.RLock()
	defer probeVirtualRouterState.mu.RUnlock()
	for index := 0; index+1 < len(cleanPath); index++ {
		if !probeVirtualRouterPathEdgeAuthorizedLocked(cleanPath[index], cleanPath[index+1], runtime.cfg.userID) {
			return fmt.Errorf("virtual router path edge is unauthorized: %s>%s", cleanPath[index], cleanPath[index+1])
		}
	}
	return nil
}

func probeVirtualRouterPathEdgeAuthorizedLocked(left string, right string, userID string) bool {
	left = normalizeProbeRouteNodeID(left)
	right = normalizeProbeRouteNodeID(right)
	userID = strings.TrimSpace(userID)
	for _, rule := range probeVirtualRouterState.config.TopologyRules {
		if !rule.Enabled {
			continue
		}
		fromNodeID := normalizeProbeRouteNodeID(rule.FromNodeID)
		toNodeID := normalizeProbeRouteNodeID(rule.ToNodeID)
		if !((fromNodeID == left && toNodeID == right) || (fromNodeID == right && toNodeID == left)) {
			continue
		}
		if userID == "" || strings.TrimSpace(rule.UserID) == userID {
			return true
		}
	}
	return false
}

func probeVirtualRouterPreviousHopInPath(path []string, localNodeID string) string {
	local := normalizeProbeRouteNodeID(localNodeID)
	if local == "" || len(path) < 2 {
		return ""
	}
	for i, item := range path {
		if normalizeProbeRouteNodeID(item) != local {
			continue
		}
		if i > 0 {
			return normalizeProbeRouteNodeID(path[i-1])
		}
		return ""
	}
	return ""
}

func handleProbeVirtualRouterIPFrame(runtime *probeVirtualRouterRuntime, link *probeVirtualRouterFrameLink, packet []byte, path []string, trace []probeVirtualRouterFrameTraceHop) error {
	recordProbeVirtualRouterRuntimeFrameReceived(runtime, len(packet))
	dstIP := probeVirtualRouterIPv4Destination(packet)
	if dstIP == "" {
		return errors.New("virtual router frame destination is invalid")
	}
	recordProbeVirtualRouterRuntimePacketReceived(runtime, len(packet))
	if len(path) == 0 {
		path = currentProbeVirtualRouterPathToIP(dstIP)
	}
	srcIP := probeVirtualRouterIPv4Source(packet)
	localIP := currentProbeVirtualRouterLocalIPForRuntime(runtime)
	localMatch := probeVirtualRouterPacketTargetsLocalDelivery(runtime, dstIP, path)
	if !localMatch && probeVirtualRouterFrameTargetsLocalFakeIP(dstIP, path, currentProbeVirtualRouterLocalNodeIDForRuntime(runtime)) {
		if scheduleProbeVirtualRouterFakeIPFirstPacketRecovery(runtime, packet, path) {
			recordProbeVirtualRouterRecentPacket("frame_rx", "wait_fake_mapping", runtime, packet, path, false, nil)
			return nil
		}
		err := fmt.Errorf("fake ip final-hop mapping unavailable: fake_ip=%s path=%s", dstIP, strings.Join(cleanProbeVirtualRouterPath(path), ">"))
		scheduleProbeVirtualRouterFakeIPItemRefreshByIP(dstIP)
		recordProbeVirtualRouterRuntimeDeliveryError(runtime, err)
		recordProbeVirtualRouterRecentPacket("frame_rx", "drop", runtime, packet, path, false, err)
		log.Printf("probe virtual router fake ip final-hop drop: route=%s runtime_node=%s dst=%s path=%s err=%v", probeVirtualRouterRuntimeLogRouteID(runtime), currentProbeVirtualRouterLocalNodeIDForRuntime(runtime), dstIP, strings.Join(cleanProbeVirtualRouterPath(path), ">"), err)
		return nil
	}
	recordProbeVirtualRouterRuntimeFrameDecision(runtime, srcIP, dstIP, localIP, path, localMatch)
	if info, ok := probeVirtualRouterParseICMPEchoLogInfo(packet); ok {
		trace = appendProbeVirtualRouterICMPTrace(trace, runtime, "frame_rx", "", "")
		log.Printf("probe virtual router icmp frame rx: trace_code=icmp-trace-v2 route=%s runtime_node=%s kind=%s src=%s dst=%s id=%d seq=%d local_ip=%s local_match=%v path=%s bytes=%d trace_hops=%d", probeVirtualRouterRuntimeLogRouteID(runtime), currentProbeVirtualRouterLocalNodeIDForRuntime(runtime), info.Kind, info.SourceIP, info.DestinationIP, info.ID, info.Sequence, localIP, localMatch, strings.Join(path, ">"), len(packet), len(trace))
	}
	if !localMatch && probeVirtualRouterFrameTargetsLocalPathEnd(path, currentProbeVirtualRouterLocalNodeIDForRuntime(runtime)) {
		if handleProbeVirtualRouterFakeIPExitPacket(runtime, link, packet, path) {
			recordProbeVirtualRouterRuntimePacketDelivered(runtime, len(packet))
			recordProbeVirtualRouterRecentPacket("frame_rx", "exit", runtime, packet, path, true, nil)
			return nil
		}
	}
	if localMatch {
		if handleProbeVirtualRouterLocalICMPEchoRequest(runtime, link, packet, path, trace) {
			recordProbeVirtualRouterRuntimePacketDelivered(runtime, len(packet))
			recordProbeVirtualRouterRecentPacket("frame_rx", "local_icmp", runtime, packet, path, true, nil)
			return nil
		}
		if handleProbeVirtualRouterFakeIPExitPacket(runtime, link, packet, path) {
			recordProbeVirtualRouterRuntimePacketDelivered(runtime, len(packet))
			recordProbeVirtualRouterRecentPacket("frame_rx", "fake_exit", runtime, packet, path, true, nil)
			return nil
		}
		deliverStartedAt := time.Now()
		if err := writeProbeVirtualRouterLocalTUNPacket(packet); err != nil {
			recordProbeVirtualRouterRuntimeDeliveryError(runtime, err)
			recordProbeVirtualRouterRecentPacket("frame_rx", "deliver_error", runtime, packet, path, true, err)
			if info, ok := probeVirtualRouterParseICMPEchoLogInfo(packet); ok {
				log.Printf("probe virtual router icmp local deliver failed: route=%s runtime_node=%s kind=%s src=%s dst=%s id=%d seq=%d local_ip=%s err=%v", probeVirtualRouterRuntimeLogRouteID(runtime), currentProbeVirtualRouterLocalNodeIDForRuntime(runtime), info.Kind, info.SourceIP, info.DestinationIP, info.ID, info.Sequence, localIP, err)
			}
			if info, ok := probeVirtualRouterParseTCPUDPLogInfo(packet); ok {
				log.Printf("probe virtual router transport local deliver failed: route=%s runtime_node=%s proto=%s src=%s:%d dst=%s:%d local_ip=%s err=%v", probeVirtualRouterRuntimeLogRouteID(runtime), currentProbeVirtualRouterLocalNodeIDForRuntime(runtime), info.Protocol, info.SourceIP, info.SourcePort, info.DestinationIP, info.DestinationPort, localIP, err)
			}
			return err
		}
		if info, ok := probeVirtualRouterParseICMPEchoLogInfo(packet); ok {
			if info.Kind == "echo_reply" {
				trace = appendProbeVirtualRouterICMPTrace(trace, runtime, "echo_reply_source", "", "")
				trace = appendProbeVirtualRouterICMPTrace(trace, runtime, "local_deliver", "", "")
				log.Printf("probe virtual router icmp local deliver ok: trace_code=icmp-trace-v2 route=%s runtime_node=%s kind=%s src=%s dst=%s id=%d seq=%d local_ip=%s write_ms=%d bytes=%d trace_hops=%d", probeVirtualRouterRuntimeLogRouteID(runtime), currentProbeVirtualRouterLocalNodeIDForRuntime(runtime), info.Kind, info.SourceIP, info.DestinationIP, info.ID, info.Sequence, localIP, probeDurationMilliseconds(time.Since(deliverStartedAt)), len(packet), len(trace))
				summary, completed := recordProbeVirtualRouterICMPPingReply(runtime, info)
				if completed {
					logProbeVirtualRouterICMPPingSummary(runtime, info, trace, summary)
				} else {
					logProbeVirtualRouterICMPTraceComplete(runtime, info, trace)
				}
			} else {
				log.Printf("probe virtual router icmp local deliver ok: route=%s runtime_node=%s kind=%s src=%s dst=%s id=%d seq=%d local_ip=%s write_ms=%d bytes=%d", probeVirtualRouterRuntimeLogRouteID(runtime), currentProbeVirtualRouterLocalNodeIDForRuntime(runtime), info.Kind, info.SourceIP, info.DestinationIP, info.ID, info.Sequence, localIP, probeDurationMilliseconds(time.Since(deliverStartedAt)), len(packet))
			}
		}
		recordProbeVirtualRouterRuntimePacketDelivered(runtime, len(packet))
		recordProbeVirtualRouterRecentPacket("frame_rx", "deliver", runtime, packet, path, true, nil)
		return nil
	}
	if len(path) < 2 {
		if info, ok := probeVirtualRouterParseICMPEchoLogInfo(packet); ok {
			log.Printf("probe virtual router icmp frame drop: route=%s runtime_node=%s kind=%s src=%s dst=%s id=%d seq=%d reason=path_incomplete local_ip=%s local_match=%v path=%s", probeVirtualRouterRuntimeLogRouteID(runtime), currentProbeVirtualRouterLocalNodeIDForRuntime(runtime), info.Kind, info.SourceIP, info.DestinationIP, info.ID, info.Sequence, localIP, localMatch, strings.Join(path, ">"))
		}
		if info, ok := probeVirtualRouterParseTCPUDPLogInfo(packet); ok {
			log.Printf("probe virtual router transport frame drop: route=%s runtime_node=%s proto=%s src=%s:%d dst=%s:%d reason=path_incomplete local_ip=%s local_match=%v path=%s", probeVirtualRouterRuntimeLogRouteID(runtime), currentProbeVirtualRouterLocalNodeIDForRuntime(runtime), info.Protocol, info.SourceIP, info.SourcePort, info.DestinationIP, info.DestinationPort, localIP, localMatch, strings.Join(path, ">"))
		}
		recordProbeVirtualRouterRecentPacket("frame_rx", "drop", runtime, packet, path, false, errors.New("path incomplete"))
		return errors.New("virtual router path is incomplete")
	}
	if err := probeVirtualRouterFakeIPForwardUnavailableError(dstIP, path, currentProbeVirtualRouterLocalNodeIDForRuntime(runtime)); err != nil {
		if info, ok := probeVirtualRouterParseICMPEchoLogInfo(packet); ok {
			log.Printf("probe virtual router icmp frame drop: route=%s runtime_node=%s kind=%s src=%s dst=%s id=%d seq=%d reason=fake_ip_exit_unreachable local_ip=%s local_match=%v path=%s err=%v", probeVirtualRouterRuntimeLogRouteID(runtime), currentProbeVirtualRouterLocalNodeIDForRuntime(runtime), info.Kind, info.SourceIP, info.DestinationIP, info.ID, info.Sequence, localIP, localMatch, strings.Join(path, ">"), err)
		}
		if info, ok := probeVirtualRouterParseTCPUDPLogInfo(packet); ok {
			log.Printf("probe virtual router transport frame drop: route=%s runtime_node=%s proto=%s src=%s:%d dst=%s:%d reason=fake_ip_exit_unreachable local_ip=%s local_match=%v path=%s err=%v", probeVirtualRouterRuntimeLogRouteID(runtime), currentProbeVirtualRouterLocalNodeIDForRuntime(runtime), info.Protocol, info.SourceIP, info.SourcePort, info.DestinationIP, info.DestinationPort, localIP, localMatch, strings.Join(path, ">"), err)
		}
		recordProbeVirtualRouterRecentPacket("frame_rx", "drop", runtime, packet, path, false, err)
		return err
	}
	if err := forwardProbeVirtualRouterPacketAlongPath(packet, dstIP, path, trace); err != nil {
		if info, ok := probeVirtualRouterParseICMPEchoLogInfo(packet); ok {
			log.Printf("probe virtual router icmp frame forward failed: route=%s runtime_node=%s kind=%s src=%s dst=%s id=%d seq=%d path=%s err=%v", probeVirtualRouterRuntimeLogRouteID(runtime), currentProbeVirtualRouterLocalNodeIDForRuntime(runtime), info.Kind, info.SourceIP, info.DestinationIP, info.ID, info.Sequence, strings.Join(path, ">"), err)
		}
		if info, ok := probeVirtualRouterParseTCPUDPLogInfo(packet); ok {
			log.Printf("probe virtual router transport frame forward failed: route=%s runtime_node=%s proto=%s src=%s:%d dst=%s:%d path=%s err=%v", probeVirtualRouterRuntimeLogRouteID(runtime), currentProbeVirtualRouterLocalNodeIDForRuntime(runtime), info.Protocol, info.SourceIP, info.SourcePort, info.DestinationIP, info.DestinationPort, strings.Join(path, ">"), err)
		}
		recordProbeVirtualRouterRecentPacket("frame_rx", "forward_error", runtime, packet, path, false, err)
		return err
	}
	recordProbeVirtualRouterRecentPacket("frame_rx", "forward", runtime, packet, path, false, nil)
	return nil
}

func probeVirtualRouterFrameTargetsLocalPathEnd(path []string, localNodeID string) bool {
	local := normalizeProbeRouteNodeID(localNodeID)
	cleanPath := cleanProbeVirtualRouterPath(path)
	return local != "" && len(cleanPath) > 0 && normalizeProbeRouteNodeID(cleanPath[len(cleanPath)-1]) == local
}

func probeVirtualRouterFrameTargetsLocalFakeIP(dstIP string, path []string, localNodeID string) bool {
	if !probeVirtualRouterIPInCurrentFakeCIDR(dstIP) {
		return false
	}
	local := normalizeProbeRouteNodeID(localNodeID)
	if local == "" || len(path) == 0 {
		return false
	}
	return normalizeProbeRouteNodeID(path[len(path)-1]) == local
}

func writeProbeVirtualRouterLocalTUNPacket(packet []byte) error {
	normalizeProbeVirtualRouterLocalTUNPacketChecksums(packet)
	if err := writeProbeVirtualRouterTUNPacket(packet); err == nil {
		return nil
	} else {
		firstErr := err
		if startErr := startProbeVirtualRouterTUNDataPlane(); startErr != nil {
			return fmt.Errorf("write local tun packet failed: %w; restart data plane failed: %v", firstErr, startErr)
		}
		if retryErr := writeProbeVirtualRouterTUNPacket(packet); retryErr != nil {
			return fmt.Errorf("write local tun packet failed after data plane restart: %w (initial: %v)", retryErr, firstErr)
		}
		return nil
	}
}

var probeVirtualRouterLocalTUNPacketWriter func([]byte) error

func normalizeProbeVirtualRouterLocalTUNPacketChecksums(packet []byte) {
	if len(packet) < 20 || packet[0]>>4 != 4 {
		return
	}
	ihl := int(packet[0]&0x0F) * 4
	if ihl < 20 || len(packet) < ihl {
		return
	}
	totalLen := int(binary.BigEndian.Uint16(packet[2:4]))
	if totalLen <= 0 || totalLen > len(packet) || totalLen < ihl {
		return
	}
	packet[10], packet[11] = 0, 0
	binary.BigEndian.PutUint16(packet[10:12], probeVirtualRouterChecksum(packet[:ihl]))
	transport := packet[ihl:totalLen]
	switch packet[9] {
	case 6:
		if len(transport) < 20 {
			return
		}
		transport[16], transport[17] = 0, 0
		binary.BigEndian.PutUint16(transport[16:18], probeVirtualRouterTransportChecksum(packet, transport))
	case 17:
		if len(transport) < 8 {
			return
		}
		transport[6], transport[7] = 0, 0
		binary.BigEndian.PutUint16(transport[6:8], probeVirtualRouterTransportChecksum(packet, transport))
	}
}

func forwardProbeVirtualRouterPacketAlongPath(packet []byte, dstIP string, path []string, trace []probeVirtualRouterFrameTraceHop) error {
	localNodeID := currentProbeVirtualRouterLocalNodeID()
	if localNodeID == "" && len(path) > 0 {
		localNodeID = normalizeProbeRouteNodeID(path[0])
	}
	if localNodeID == "" {
		return errors.New("local virtual router node id is empty")
	}
	nextNodeID := probeVirtualRouterNextHopInPath(path, localNodeID)
	if nextNodeID == "" {
		return errors.New("next virtual router hop is unavailable")
	}
	rt, direction := probeVirtualRouterRuntimeForAdjacentNode(nextNodeID)
	if rt == nil {
		return errors.New("adjacent virtual router runtime is unavailable")
	}
	if _, ok := probeVirtualRouterParseICMPEchoLogInfo(packet); ok {
		trace = appendProbeVirtualRouterICMPTrace(trace, rt, "forward_tx", direction, nextNodeID)
	}
	if info, ok := probeVirtualRouterParseICMPEchoLogInfo(packet); ok {
		log.Printf("probe virtual router icmp forward enqueue: trace_code=icmp-trace-v2 route=%s local_node=%s next_node=%s direction=%s kind=%s src=%s dst=%s id=%d seq=%d path=%s bytes=%d", probeVirtualRouterRuntimeLogRouteID(rt), localNodeID, nextNodeID, direction, info.Kind, info.SourceIP, info.DestinationIP, info.ID, info.Sequence, strings.Join(path, ">"), len(packet))
	}
	link, err := ensureProbeVirtualRouterFrameLink(rt, direction, dstIP, path)
	if err != nil {
		return err
	}
	if err := writeProbeVirtualRouterIPFrameUntil(link, packet, path, trace, time.Now().Add(probeVirtualRouterFrameLinkTXEnqueueWait)); err != nil {
		recordProbeVirtualRouterRuntimeOpenError(rt.cfg.routeID, err)
		if !isProbeVirtualRouterClosedLinkError(err) {
			return err
		}
		link, err = ensureProbeVirtualRouterFrameLink(rt, direction, dstIP, path)
		if err != nil {
			recordProbeVirtualRouterRuntimeOpenError(rt.cfg.routeID, err)
			return err
		}
		if err := writeProbeVirtualRouterIPFrameUntil(link, packet, path, trace, time.Now().Add(probeVirtualRouterFrameLinkTXEnqueueWait)); err != nil {
			recordProbeVirtualRouterRuntimeOpenError(rt.cfg.routeID, err)
			return err
		}
	}
	recordProbeVirtualRouterRuntimeFrameSent(rt, len(packet))
	recordProbeVirtualRouterRuntimePacketForwarded(rt, len(packet))
	if info, ok := probeVirtualRouterParseICMPEchoLogInfo(packet); ok {
		log.Printf("probe virtual router icmp forward queued: trace_code=icmp-trace-v2 route=%s local_node=%s next_node=%s direction=%s kind=%s src=%s dst=%s id=%d seq=%d path=%s bytes=%d", probeVirtualRouterRuntimeLogRouteID(rt), localNodeID, nextNodeID, direction, info.Kind, info.SourceIP, info.DestinationIP, info.ID, info.Sequence, strings.Join(path, ">"), len(packet))
	}
	return nil
}

func handleProbeVirtualRouterLocalICMPEchoRequest(runtime *probeVirtualRouterRuntime, stream *probeVirtualRouterFrameLink, packet []byte, ingressPath []string, trace []probeVirtualRouterFrameTraceHop) bool {
	localIP := currentProbeVirtualRouterLocalIPForRuntime(runtime)
	reply, dstIP, ok := buildProbeVirtualRouterICMPEchoReply(packet, localIP)
	if !ok {
		return false
	}
	trace = appendProbeVirtualRouterICMPTrace(trace, runtime, "echo_request_final", "", "")
	trace = appendProbeVirtualRouterICMPTrace(trace, runtime, "echo_reply_build", "", "")
	path := probeVirtualRouterReversePath(ingressPath)
	if len(path) < 2 {
		path = currentProbeVirtualRouterPathForPacket(reply, dstIP)
	}
	if len(path) < 2 {
		log.Printf("probe virtual router icmp echo reply path unavailable: dst=%s", dstIP)
		return false
	}
	if reqInfo, ok := probeVirtualRouterParseICMPEchoLogInfo(packet); ok {
		if replyInfo, replyOK := probeVirtualRouterParseICMPEchoLogInfo(reply); replyOK {
			log.Printf("probe virtual router icmp echo reply build: route=%s runtime_node=%s request_src=%s request_dst=%s reply_src=%s reply_dst=%s id=%d seq=%d local_ip=%s path=%s", probeVirtualRouterRuntimeLogRouteID(runtime), currentProbeVirtualRouterLocalNodeIDForRuntime(runtime), reqInfo.SourceIP, reqInfo.DestinationIP, replyInfo.SourceIP, replyInfo.DestinationIP, reqInfo.ID, reqInfo.Sequence, localIP, strings.Join(path, ">"))
		}
	}
	if err := writeProbeVirtualRouterIPFrame(stream, reply, path, trace); err != nil {
		if runtime != nil {
			recordProbeVirtualRouterRuntimeOpenError(runtime.cfg.routeID, err)
		}
		log.Printf("probe virtual router icmp echo reply write failed: dst=%s path=%s err=%v", dstIP, strings.Join(path, ">"), err)
		return false
	}
	recordProbeVirtualRouterRuntimeFrameSent(runtime, len(reply))
	recordProbeVirtualRouterRuntimePacketForwarded(runtime, len(reply))
	return true
}

func probeVirtualRouterReversePath(path []string) []string {
	if len(path) == 0 {
		return nil
	}
	out := make([]string, 0, len(path))
	for i := len(path) - 1; i >= 0; i-- {
		if nodeID := normalizeProbeRouteNodeID(path[i]); nodeID != "" {
			out = append(out, nodeID)
		}
	}
	if len(out) < 2 {
		return nil
	}
	return out
}

func ensureProbeVirtualRouterFrameLink(rt *probeVirtualRouterRuntime, direction string, dstIP string, path []string) (*probeVirtualRouterFrameLink, error) {
	if rt == nil {
		return nil, errors.New("runtime is nil")
	}
	key := probeVirtualRouterFrameLinkKey(rt, direction, dstIP, path)
	if key == "" {
		return nil, errors.New("frame link key is empty")
	}
	now := time.Now()
	if stream := reusableProbeVirtualRouterFrameLink(key, now); stream != nil {
		return stream, nil
	}
	link := newProbeVirtualRouterFrameLink(key, rt, nil, path)
	link.Start()
	probeVirtualRouterFrameLinkState.mu.Lock()
	if existing := probeVirtualRouterFrameLinkState.links[key]; existing != nil && !isProbeVirtualRouterFrameLinkClosed(existing) {
		probeVirtualRouterFrameLinkState.mu.Unlock()
		stopProbeVirtualRouterFrameLink(link)
		return existing, nil
	}
	probeVirtualRouterFrameLinkState.links[key] = link
	probeVirtualRouterFrameLinkState.mu.Unlock()
	return link, nil
}

func reusableProbeVirtualRouterFrameLink(key string, now time.Time) *probeVirtualRouterFrameLink {
	probeVirtualRouterFrameLinkState.mu.Lock()
	item := probeVirtualRouterFrameLinkState.links[key]
	if item == nil {
		probeVirtualRouterFrameLinkState.mu.Unlock()
		return nil
	}
	if isProbeVirtualRouterFrameLinkClosed(item) {
		delete(probeVirtualRouterFrameLinkState.links, key)
		probeVirtualRouterFrameLinkState.mu.Unlock()
		stopProbeVirtualRouterFrameLink(item)
		return nil
	}
	item.lastUsed = now
	probeVirtualRouterFrameLinkState.mu.Unlock()
	return item
}

func isProbeVirtualRouterFrameLinkClosed(item *probeVirtualRouterFrameLink) bool {
	if item == nil {
		return true
	}
	if item.done != nil {
		select {
		case <-item.done:
			return true
		default:
		}
	}
	return false
}

func isProbeVirtualRouterClosedLinkError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, net.ErrClosed) || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, io.EOF) {
		return true
	}
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(text, "use of closed network connection") ||
		strings.Contains(text, "closed pipe") ||
		strings.Contains(text, "connection reset by peer") ||
		strings.Contains(text, "broken pipe") ||
		strings.Contains(text, "websocket: close") ||
		strings.Contains(text, "abnormal closure") ||
		strings.Contains(text, "unexpected eof")
}

func dropProbeVirtualRouterFrameLink(link *probeVirtualRouterFrameLink) {
	if link == nil {
		return
	}
	dropped := false
	probeVirtualRouterFrameLinkState.mu.Lock()
	for key, item := range probeVirtualRouterFrameLinkState.links {
		if item != nil && item == link {
			delete(probeVirtualRouterFrameLinkState.links, key)
			dropped = true
			break
		}
	}
	probeVirtualRouterFrameLinkState.mu.Unlock()
	if dropped {
		stopProbeVirtualRouterFrameLink(link)
	}
}

func closeProbeVirtualRouterFrameLinks(reason string) {
	probeVirtualRouterFrameLinkState.mu.Lock()
	links := probeVirtualRouterFrameLinkState.links
	probeVirtualRouterFrameLinkState.links = make(map[string]*probeVirtualRouterFrameLink)
	probeVirtualRouterFrameLinkState.mu.Unlock()
	for _, item := range links {
		if item != nil {
			stopProbeVirtualRouterFrameLink(item)
		}
	}
	if len(links) > 0 {
		log.Printf("probe virtual router frame links closed: count=%d reason=%s", len(links), strings.TrimSpace(reason))
	}
}

func closeProbeVirtualRouterRuntimeFrameLink(rt *probeVirtualRouterRuntime) {
	if rt == nil {
		return
	}
	key := probeVirtualRouterFrameLinkKey(rt, "", "", nil)
	if key == "" {
		return
	}
	probeVirtualRouterFrameLinkState.mu.Lock()
	item := probeVirtualRouterFrameLinkState.links[key]
	if item != nil {
		delete(probeVirtualRouterFrameLinkState.links, key)
	}
	probeVirtualRouterFrameLinkState.mu.Unlock()
	if item != nil {
		stopProbeVirtualRouterFrameLink(item)
	}
}

func probeVirtualRouterFrameLinkKey(rt *probeVirtualRouterRuntime, direction string, dstIP string, path []string) string {
	if rt == nil {
		return ""
	}
	return strings.Join([]string{
		"packet",
		strings.TrimSpace(rt.cfg.routeID),
	}, "|")
}

func probeVirtualRouterRTTQueryLinkKey(rt *probeVirtualRouterRuntime, direction string, path []string) string {
	if rt == nil {
		return ""
	}
	cleanPath := make([]string, 0, len(path))
	for _, item := range path {
		if nodeID := normalizeProbeRouteNodeID(item); nodeID != "" {
			cleanPath = append(cleanPath, nodeID)
		}
	}
	if len(cleanPath) < 2 {
		return ""
	}
	return strings.Join([]string{
		"rtt",
		strings.TrimSpace(rt.cfg.routeID),
		strings.TrimSpace(direction),
		strings.Join(cleanPath, ">"),
	}, "|")
}

func probeVirtualRouterRuntimeStatsForUpdateLocked(routeID string) *probeVirtualRouterRuntimeStats {
	routeID = strings.TrimSpace(routeID)
	if routeID == "" {
		return nil
	}
	item := probeVirtualRouterRuntimeStatsState.items[routeID]
	if item == nil {
		item = &probeVirtualRouterRuntimeStats{}
		probeVirtualRouterRuntimeStatsState.items[routeID] = item
	}
	return item
}

func recordProbeVirtualRouterRuntimePacketForwarded(rt *probeVirtualRouterRuntime, packetBytes int) {
	if rt == nil {
		return
	}
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	item := probeVirtualRouterRuntimeStatsForUpdateLocked(rt.cfg.routeID)
	if item != nil {
		item.PacketsForwarded++
		item.BytesForwarded += int64(packetBytes)
		item.LastPacketAt = time.Now().UTC().Format(time.RFC3339)
	}
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
}

func recordProbeVirtualRouterRuntimePacketReceived(rt *probeVirtualRouterRuntime, packetBytes int) {
	if rt == nil {
		return
	}
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	item := probeVirtualRouterRuntimeStatsForUpdateLocked(rt.cfg.routeID)
	if item != nil {
		item.PacketsReceived++
		item.BytesReceived += int64(packetBytes)
		item.LastPacketAt = time.Now().UTC().Format(time.RFC3339)
	}
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
}

func recordProbeVirtualRouterRuntimePacketDelivered(rt *probeVirtualRouterRuntime, packetBytes int) {
	if rt == nil {
		return
	}
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	item := probeVirtualRouterRuntimeStatsForUpdateLocked(rt.cfg.routeID)
	if item != nil {
		item.PacketsDelivered++
		item.BytesDelivered += int64(packetBytes)
		item.LastPacketAt = time.Now().UTC().Format(time.RFC3339)
		item.LastDeliveryError = ""
	}
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
}

func recordProbeVirtualRouterRuntimeFrameSent(rt *probeVirtualRouterRuntime, frameBytes int) {
	if rt == nil {
		return
	}
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	item := probeVirtualRouterRuntimeStatsForUpdateLocked(rt.cfg.routeID)
	if item != nil {
		item.FramesSent++
		item.FrameBytesSent += int64(frameBytes)
		item.LastFrameAt = time.Now().UTC().Format(time.RFC3339)
	}
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
}

// A frame that reached the physical carrier proves that a prior transient
// enqueue or carrier-send failure has recovered without requiring a reconnect.
func recordProbeVirtualRouterRuntimeCarrierTXSuccess(rt *probeVirtualRouterRuntime) {
	if rt == nil {
		return
	}
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	item := probeVirtualRouterRuntimeStatsForUpdateLocked(rt.cfg.routeID)
	if item != nil {
		item.LastOpenError = ""
	}
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
}

func recordProbeVirtualRouterRuntimeFrameReceived(rt *probeVirtualRouterRuntime, frameBytes int) {
	if rt == nil {
		return
	}
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	item := probeVirtualRouterRuntimeStatsForUpdateLocked(rt.cfg.routeID)
	if item != nil {
		item.FramesReceived++
		item.FrameBytesReceived += int64(frameBytes)
		item.LastFrameAt = time.Now().UTC().Format(time.RFC3339)
	}
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
}

func recordProbeVirtualRouterRuntimeFrameDecision(rt *probeVirtualRouterRuntime, srcIP string, dstIP string, localIP string, path []string, localMatch bool) {
	if rt == nil {
		return
	}
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	item := probeVirtualRouterRuntimeStatsForUpdateLocked(rt.cfg.routeID)
	if item != nil {
		item.LastFrameSourceIP = strings.TrimSpace(srcIP)
		item.LastFrameDestinationIP = strings.TrimSpace(dstIP)
		item.LastFrameLocalIP = strings.TrimSpace(localIP)
		if localMatch {
			item.LastFrameLocalMatch = "yes"
		} else {
			item.LastFrameLocalMatch = "no"
		}
		item.LastFramePath = strings.Join(path, ">")
		item.LastFrameRuntimeNodeID = currentProbeVirtualRouterLocalNodeIDForRuntime(rt)
	}
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
}

func probeVirtualRouterICMPPingKey(sourceIP string, destinationIP string, id uint16, sequence uint16) string {
	return strings.Join([]string{
		strings.TrimSpace(sourceIP),
		strings.TrimSpace(destinationIP),
		fmt.Sprintf("%d", id),
		fmt.Sprintf("%d", sequence),
	}, "|")
}

func recordProbeVirtualRouterICMPPingStart(info probeVirtualRouterICMPEchoLogInfo, path []string) {
	key := probeVirtualRouterICMPPingKey(info.SourceIP, info.DestinationIP, info.ID, info.Sequence)
	if key == "" {
		return
	}
	now := time.Now()
	pending := probeVirtualRouterICMPPingPending{
		StartedAt:     now,
		SourceIP:      strings.TrimSpace(info.SourceIP),
		DestinationIP: strings.TrimSpace(info.DestinationIP),
		ID:            info.ID,
		Sequence:      info.Sequence,
		Path:          strings.Join(path, ">"),
	}
	probeVirtualRouterICMPPingState.mu.Lock()
	if probeVirtualRouterICMPPingState.pending == nil {
		probeVirtualRouterICMPPingState.pending = make(map[string]probeVirtualRouterICMPPingPending)
	}
	for itemKey, item := range probeVirtualRouterICMPPingState.pending {
		if now.Sub(item.StartedAt) > 60*time.Second {
			delete(probeVirtualRouterICMPPingState.pending, itemKey)
		}
	}
	probeVirtualRouterICMPPingState.pending[key] = pending
	probeVirtualRouterICMPPingState.mu.Unlock()
	log.Printf("probe virtual router icmp echo start: trace_code=icmp-trace-v2 src=%s dst=%s id=%d seq=%d path=%s", info.SourceIP, info.DestinationIP, info.ID, info.Sequence, strings.Join(path, ">"))
}

func recordProbeVirtualRouterICMPPingReply(rt *probeVirtualRouterRuntime, info probeVirtualRouterICMPEchoLogInfo) (probeVirtualRouterICMPPingCompleteSummary, bool) {
	key := probeVirtualRouterICMPPingKey(info.DestinationIP, info.SourceIP, info.ID, info.Sequence)
	if key == "" {
		return probeVirtualRouterICMPPingCompleteSummary{}, false
	}
	probeVirtualRouterICMPPingState.mu.Lock()
	pending, ok := probeVirtualRouterICMPPingState.pending[key]
	if ok {
		delete(probeVirtualRouterICMPPingState.pending, key)
	}
	probeVirtualRouterICMPPingState.mu.Unlock()
	if !ok || pending.StartedAt.IsZero() {
		return probeVirtualRouterICMPPingCompleteSummary{}, false
	}
	latency := time.Since(pending.StartedAt)
	latencyMS := probeDurationMilliseconds(latency)
	routeID := ""
	if rt != nil {
		routeID = strings.TrimSpace(rt.cfg.routeID)
	}
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	item := probeVirtualRouterRuntimeStatsForUpdateLocked(routeID)
	if item != nil {
		item.VirtualPingCount++
		item.LastVirtualPingLatencyMS = latencyMS
		item.LastVirtualPingAt = time.Now().UTC().Format(time.RFC3339)
		item.LastVirtualPingSourceIP = pending.SourceIP
		item.LastVirtualPingDestIP = pending.DestinationIP
		item.LastVirtualPingID = pending.ID
		item.LastVirtualPingSequence = pending.Sequence
		item.LastVirtualPingPath = pending.Path
	}
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
	log.Printf("probe virtual router icmp echo complete: trace_code=icmp-trace-v2 complete_site=record_reply route=%s src=%s dst=%s id=%d seq=%d latency_ms=%d path=%s", routeID, pending.SourceIP, pending.DestinationIP, pending.ID, pending.Sequence, latencyMS, pending.Path)
	return probeVirtualRouterICMPPingCompleteSummary{
		SourceIP:      pending.SourceIP,
		DestinationIP: pending.DestinationIP,
		ID:            pending.ID,
		Sequence:      pending.Sequence,
		Path:          pending.Path,
		LatencyMS:     latencyMS,
	}, true
}

func logProbeVirtualRouterICMPTraceComplete(rt *probeVirtualRouterRuntime, info probeVirtualRouterICMPEchoLogInfo, trace []probeVirtualRouterFrameTraceHop) {
	if len(trace) == 0 {
		return
	}
	routeID := ""
	if rt != nil {
		routeID = strings.TrimSpace(rt.cfg.routeID)
	}
	log.Printf("probe virtual router icmp trace complete: trace_code=icmp-trace-v2 route=%s kind=%s src=%s dst=%s id=%d seq=%d hops=%d trace_clock=node_local_absolute trace=%s", routeID, info.Kind, info.SourceIP, info.DestinationIP, info.ID, info.Sequence, len(trace), probeVirtualRouterICMPTraceString(trace))
}

func logProbeVirtualRouterICMPPingSummary(rt *probeVirtualRouterRuntime, info probeVirtualRouterICMPEchoLogInfo, trace []probeVirtualRouterFrameTraceHop, summary probeVirtualRouterICMPPingCompleteSummary) {
	routeID := ""
	if rt != nil {
		routeID = strings.TrimSpace(rt.cfg.routeID)
	}
	traceText := probeVirtualRouterICMPTraceString(trace)
	if strings.TrimSpace(traceText) == "" {
		traceText = "-"
	}
	sourceIP := firstNonEmpty(summary.SourceIP, info.DestinationIP)
	destinationIP := firstNonEmpty(summary.DestinationIP, info.SourceIP)
	path := strings.TrimSpace(summary.Path)
	if path == "" {
		path = "-"
	}
	log.Printf("probe virtual router icmp echo summary: trace_code=icmp-trace-v2 route=%s kind=%s src=%s dst=%s id=%d seq=%d latency_ms=%d path=%s trace_hops=%d trace_clock=node_local_absolute trace=%s", routeID, info.Kind, sourceIP, destinationIP, summary.ID, summary.Sequence, summary.LatencyMS, path, len(trace), traceText)
}

func recordProbeVirtualRouterRuntimeDeliveryError(rt *probeVirtualRouterRuntime, err error) {
	if rt == nil || err == nil {
		return
	}
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	item := probeVirtualRouterRuntimeStatsForUpdateLocked(rt.cfg.routeID)
	if item != nil {
		item.LastDeliveryError = strings.TrimSpace(err.Error())
		item.LastPacketAt = time.Now().UTC().Format(time.RFC3339)
	}
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
}

func recordProbeVirtualRouterRuntimeOpenSuccess(routeID string, latency time.Duration) {
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	item := probeVirtualRouterRuntimeStatsForUpdateLocked(routeID)
	if item != nil {
		item.LinkOpenCount++
		item.LastOpenLatencyMS = probeDurationMilliseconds(latency)
		item.LastOpenError = ""
		item.LastOpenAt = time.Now().UTC().Format(time.RFC3339)
		resetProbeVirtualRouterRuntimePingStateLocked(item)
	}
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
}

func recordProbeVirtualRouterRuntimeOpenError(routeID string, err error) {
	if err == nil {
		return
	}
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	item := probeVirtualRouterRuntimeStatsForUpdateLocked(routeID)
	if item != nil {
		item.LastOpenError = strings.TrimSpace(err.Error())
		item.LastOpenAt = time.Now().UTC().Format(time.RFC3339)
	}
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
}

func recordProbeVirtualRouterRuntimePingSuccess(rt *probeVirtualRouterRuntime, direction string, latency time.Duration) {
	routeID, bridgeStatus, bridgeSession := snapshotProbeVirtualRouterPingContext(rt, direction)
	shouldClearRouteCache := false
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	item := probeVirtualRouterRuntimeStatsForUpdateLocked(routeID)
	if item != nil {
		shouldClearRouteCache = strings.TrimSpace(item.LastPingError) != ""
		item.PingCount++
		item.LastPingLatencyMS = probeDurationMilliseconds(latency)
		item.LastPingError = ""
		item.LastPingFailureCount = 0
		item.LastPingAt = time.Now().UTC().Format(time.RFC3339)
		applyProbeVirtualRouterPingContext(item, direction, bridgeStatus, bridgeSession)
	}
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
	if shouldClearRouteCache {
		clearProbeVirtualRouterRouteCacheForRuntime(rt, "bridge ping recovered")
	}
}

func recordProbeVirtualRouterRuntimePingError(rt *probeVirtualRouterRuntime, direction string, err error) {
	if rt == nil || err == nil {
		return
	}
	routeID, bridgeStatus, bridgeSession := snapshotProbeVirtualRouterPingContext(rt, direction)
	normalizedErr := normalizeProbeVirtualRouterBridgeError(err.Error())
	if probeVirtualRouterRuntimeCarrierRecentlyReceived(rt, probeVirtualRouterPingPongInterval) {
		probeVirtualRouterRuntimeStatsState.mu.Lock()
		item := probeVirtualRouterRuntimeStatsForUpdateLocked(routeID)
		if item != nil {
			item.LastPingError = ""
			item.LastPingFailureCount = 0
			item.LastPingAt = time.Now().UTC().Format(time.RFC3339)
			applyProbeVirtualRouterPingContext(item, direction, bridgeStatus, bridgeSession)
		}
		probeVirtualRouterRuntimeStatsState.mu.Unlock()
		return
	}
	failureCount := 0
	shouldClearRouteCache := false
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	item := probeVirtualRouterRuntimeStatsForUpdateLocked(routeID)
	if item != nil {
		item.LastPingError = normalizedErr
		item.LastPingFailureCount++
		failureCount = item.LastPingFailureCount
		shouldClearRouteCache = failureCount == probeVirtualRouterCarrierStalePingFailures
		item.LastPingAt = time.Now().UTC().Format(time.RFC3339)
		applyProbeVirtualRouterPingContext(item, direction, bridgeStatus, bridgeSession)
	}
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
	if shouldClearRouteCache {
		clearProbeVirtualRouterRouteCacheForRuntime(rt, "bridge ping error threshold")
	}
	log.Printf("probe virtual router bridge ping error retained carrier: route=%s direction=%s failures=%d err=%s", routeID, normalizeProbeRouteBridgeRole(direction), failureCount, normalizedErr)
	detachProbeVirtualRouterStalePhysicalCarrier(rt, failureCount, normalizedErr)
}

func probeVirtualRouterRuntimeCarrierRecentlyReceived(rt *probeVirtualRouterRuntime, maxIdle time.Duration) bool {
	if rt == nil {
		return false
	}
	if maxIdle <= 0 {
		maxIdle = probeVirtualRouterPingPongInterval
	}
	key := probeVirtualRouterFrameLinkKey(rt, "", "", nil)
	if key == "" {
		return false
	}
	probeVirtualRouterFrameLinkState.mu.Lock()
	link := probeVirtualRouterFrameLinkState.links[key]
	probeVirtualRouterFrameLinkState.mu.Unlock()
	if link == nil || isProbeVirtualRouterFrameLinkClosed(link) {
		return false
	}
	link.mu.Lock()
	token := link.carrier
	link.mu.Unlock()
	if token == nil {
		return false
	}
	lastReadAt := token.lastRead()
	return !lastReadAt.IsZero() && time.Since(lastReadAt) < maxIdle
}

func recordProbeVirtualRouterRuntimeRemoteRTTControlSuccess(routeID string, latencyMS int64, responder string) {
	routeID = strings.TrimSpace(routeID)
	if routeID == "" {
		return
	}
	shouldClearRouteCache := false
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	item := probeVirtualRouterRuntimeStatsForUpdateLocked(routeID)
	if item != nil {
		shouldClearRouteCache = strings.TrimSpace(item.LastRemoteRTTError) != ""
		item.LastRemoteRTTMS = latencyMS
		item.LastRemoteRTTAt = time.Now().UTC().Format(time.RFC3339)
		item.LastRemoteRTTError = ""
		item.LastRemoteRTTResponder = strings.TrimSpace(responder)
		item.LastRemotePongsReceived++
	}
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
	if shouldClearRouteCache {
		clearProbeVirtualRouterRouteCache("remote rtt control query recovered")
	}
}

func recordProbeVirtualRouterRuntimeRemoteRTTError(routeID string, err error) {
	if err == nil {
		return
	}
	routeID = strings.TrimSpace(routeID)
	if routeID == "" {
		return
	}
	shouldClearRouteCache := false
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	item := probeVirtualRouterRuntimeStatsForUpdateLocked(routeID)
	if item != nil {
		shouldClearRouteCache = strings.TrimSpace(item.LastRemoteRTTError) == ""
		item.LastRemoteRTTError = strings.TrimSpace(err.Error())
		item.LastRemoteRTTAt = time.Now().UTC().Format(time.RFC3339)
	}
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
	if shouldClearRouteCache {
		clearProbeVirtualRouterRouteCache("remote rtt query error")
	}
}

func recordProbeVirtualRouterRuntimeSpeedTest(routeID string, sourceNodeID string, targetNodeID string, pathText string, up probeVirtualRouterSpeedTestResult, down probeVirtualRouterSpeedTestResult, resultErr error) {
	routeID = strings.TrimSpace(routeID)
	if routeID == "" {
		return
	}
	errText := ""
	if resultErr != nil {
		errText = strings.TrimSpace(resultErr.Error())
	}
	if up.Error != "" {
		errText = firstNonEmpty(errText, "up: "+strings.TrimSpace(up.Error))
	}
	if down.Error != "" {
		errText = firstNonEmpty(errText, "down: "+strings.TrimSpace(down.Error))
	}
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	item := probeVirtualRouterRuntimeStatsForUpdateLocked(routeID)
	if item != nil {
		item.LastSpeedTestAt = time.Now().UTC().Format(time.RFC3339)
		item.LastSpeedTestSourceNodeID = normalizeProbeRouteNodeID(sourceNodeID)
		item.LastSpeedTestTargetNodeID = normalizeProbeRouteNodeID(targetNodeID)
		item.LastSpeedTestPath = strings.TrimSpace(pathText)
		item.LastSpeedTestError = errText
		item.LastSpeedTestUpBytes = up.Bytes
		item.LastSpeedTestUpFrames = up.Frames
		item.LastSpeedTestUpDurationMS = up.DurationMS
		item.LastSpeedTestUpMbps = up.Mbps
		item.LastSpeedTestDownBytes = down.Bytes
		item.LastSpeedTestDownFrames = down.Frames
		item.LastSpeedTestDownDurationMS = down.DurationMS
		item.LastSpeedTestDownMbps = down.Mbps
	}
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
}

func recordProbeVirtualRouterRuntimeSpeedTestReceive(routeID string, result probeVirtualRouterSpeedTestResultPayload) {
	routeID = strings.TrimSpace(routeID)
	if routeID == "" {
		return
	}
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	item := probeVirtualRouterRuntimeStatsForUpdateLocked(routeID)
	if item != nil {
		item.LastSpeedTestAt = time.Now().UTC().Format(time.RFC3339)
		item.LastSpeedTestSourceNodeID = normalizeProbeRouteNodeID(result.SourceNodeID)
		item.LastSpeedTestTargetNodeID = normalizeProbeRouteNodeID(result.TargetNodeID)
		item.LastSpeedTestPath = strings.Join(cleanProbeVirtualRouterPath(result.Path), ">")
		item.LastSpeedTestError = strings.TrimSpace(result.Error)
		switch strings.TrimSpace(result.Direction) {
		case "down":
			item.LastSpeedTestDownBytes = result.Bytes
			item.LastSpeedTestDownFrames = result.Frames
			item.LastSpeedTestDownDurationMS = result.DurationMS
			item.LastSpeedTestDownMbps = result.Mbps
		default:
			item.LastSpeedTestUpBytes = result.Bytes
			item.LastSpeedTestUpFrames = result.Frames
			item.LastSpeedTestUpDurationMS = result.DurationMS
			item.LastSpeedTestUpMbps = result.Mbps
		}
	}
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
}

func normalizeProbeVirtualRouterBridgeError(value string) string {
	text := strings.TrimSpace(value)
	text = strings.ReplaceAll(text, "upstream bridge", "bridge")
	text = strings.ReplaceAll(text, "downstream bridge", "bridge")
	return text
}

func snapshotProbeVirtualRouterPingContext(rt *probeVirtualRouterRuntime, direction string) (string, probeRouteBridgeRuntimeStatus, probeRouteBridgeSessionSnapshot) {
	if rt == nil {
		return "", probeRouteBridgeRuntimeStatus{}, probeRouteBridgeSessionSnapshot{}
	}
	snapshot, ok := snapshotProbeVirtualRouterPhysicalCarrier(rt)
	status := probeRouteBridgeRuntimeStatus{UpdatedAt: time.Now().UTC().Format(time.RFC3339)}
	if ok {
		status.DownstreamActive = 1
		status.Sessions = []probeRouteBridgeSessionSnapshot{snapshot}
		return strings.TrimSpace(rt.cfg.routeID), status, snapshot
	}
	return strings.TrimSpace(rt.cfg.routeID), status, probeRouteBridgeSessionSnapshot{}
}

func applyProbeVirtualRouterPingContext(item *probeVirtualRouterRuntimeStats, direction string, bridgeStatus probeRouteBridgeRuntimeStatus, bridgeSession probeRouteBridgeSessionSnapshot) {
	if item == nil {
		return
	}
	item.LastPingDirection = normalizeProbeRouteBridgeRole(direction)
	item.LastPingBridgeConnections = probeVirtualRouterBridgeConnectionCount(bridgeStatus)
	item.LastPingBridgeSessionID = strings.TrimSpace(bridgeSession.SessionID)
	item.LastPingBridgeRemote = strings.TrimSpace(bridgeSession.RemoteAddr)
	item.LastPingBridgeConnectedAt = strings.TrimSpace(bridgeSession.ConnectedAt)
}

func probeVirtualRouterBridgeConnectionCount(bridgeStatus probeRouteBridgeRuntimeStatus) int {
	count := 0
	for _, session := range bridgeStatus.Sessions {
		if !session.Closed {
			count++
		}
	}
	return count
}

func snapshotProbeVirtualRouterPhysicalCarrier(rt *probeVirtualRouterRuntime) (probeRouteBridgeSessionSnapshot, bool) {
	link := currentProbeVirtualRouterPhysicalCarrier(rt)
	if link == nil {
		return probeRouteBridgeSessionSnapshot{}, false
	}
	link.mu.Lock()
	defer link.mu.Unlock()
	carrier := link.carrier
	if carrier == nil {
		return probeRouteBridgeSessionSnapshot{}, false
	}
	connectedAt := ""
	connectedMS := int64(0)
	if !carrier.connectedAt.IsZero() {
		connectedAt = carrier.connectedAt.UTC().Format(time.RFC3339)
		connectedMS = time.Since(carrier.connectedAt).Milliseconds()
	}
	return probeRouteBridgeSessionSnapshot{
		RouteID:        strings.TrimSpace(rt.cfg.routeID),
		RuntimeRole:    probeVirtualRouterRuntimeRole,
		Direction:      "physical",
		SessionID:      firstNonEmpty(strings.TrimSpace(carrier.sessionID), "vrouter-carrier"),
		BridgeRole:     "physical",
		RemoteAddr:     strings.TrimSpace(carrier.remoteAddr),
		ConnectedAt:    connectedAt,
		ConnectedMS:    connectedMS,
		StreamsCurrent: 0,
		Closed:         false,
	}, true
}

func clearProbeVirtualRouterRuntimePingError(routeID string) {
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	item := probeVirtualRouterRuntimeStatsForUpdateLocked(routeID)
	if item != nil {
		resetProbeVirtualRouterRuntimePingStateLocked(item)
	}
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
}

func resetProbeVirtualRouterRuntimePingStateLocked(item *probeVirtualRouterRuntimeStats) {
	if item == nil {
		return
	}
	item.LastPingLatencyMS = 0
	item.LastPingError = ""
	item.LastPingAt = ""
	item.LastPingFailureCount = 0
	item.LastPingDirection = ""
	item.LastPingBridgeConnections = 0
	item.LastPingBridgeSessionID = ""
	item.LastPingBridgeRemote = ""
	item.LastPingBridgeConnectedAt = ""
}

func snapshotProbeVirtualRouterRuntimeStats(routeID string) *probeVirtualRouterRuntimeStats {
	routeID = strings.TrimSpace(routeID)
	if routeID == "" {
		return nil
	}
	probeVirtualRouterRuntimeStatsState.mu.Lock()
	item := probeVirtualRouterRuntimeStatsState.items[routeID]
	if item == nil {
		probeVirtualRouterRuntimeStatsState.mu.Unlock()
		return nil
	}
	out := *item
	tunStats := probeVirtualRouterTUNDataPlaneStatsSnapshot()
	out.TUNDataPlane = tunStats.Running
	out.TUNRXPackets = tunStats.RXPackets
	out.TUNRXBytes = tunStats.RXBytes
	out.TUNTXPackets = tunStats.TXPackets
	out.TUNTXBytes = tunStats.TXBytes
	probeVirtualRouterRuntimeStatsState.mu.Unlock()
	return &out
}

func probeVirtualRouterRuntimeForAdjacentNode(nodeID string) (*probeVirtualRouterRuntime, string) {
	target := normalizeProbeRouteNodeID(nodeID)
	if target == "" {
		return nil, ""
	}
	probeVirtualRouterRuntimeState.mu.RLock()
	defer probeVirtualRouterRuntimeState.mu.RUnlock()
	return findProbeVirtualRouterRuntimeForAdjacentNodeLocked(target)
}

func findProbeVirtualRouterRuntimeForAdjacentNodeLocked(target string) (*probeVirtualRouterRuntime, string) {
	var fallbackRT *probeVirtualRouterRuntime
	var fallbackDirection string
	for _, rt := range probeVirtualRouterRuntimeState.runtimes {
		if rt == nil {
			continue
		}
		if normalizeProbeRouteNodeID(rt.cfg.peerNodeID) != target {
			continue
		}
		direction := probeRouteBridgeRoleToPrev
		if rt.cfg.dialer {
			direction = probeRouteBridgeRoleToNext
		}
		if probeVirtualRouterRuntimeHasBridgeSession(rt, direction) {
			return rt, direction
		}
		if fallbackRT == nil {
			fallbackRT = rt
			fallbackDirection = direction
		}
	}
	if fallbackRT != nil {
		return fallbackRT, fallbackDirection
	}
	return nil, ""
}

func selectProbeVirtualRouterBridgeDirection(rt *probeVirtualRouterRuntime, preferred string) string {
	return normalizeProbeRouteBridgeRole(preferred)
}

func probeVirtualRouterRuntimeLogRouteID(runtime *probeVirtualRouterRuntime) string {
	if runtime == nil {
		return ""
	}
	return strings.TrimSpace(runtime.cfg.routeID)
}

func probeVirtualRouterRuntimeHasBridgeSession(rt *probeVirtualRouterRuntime, direction string) bool {
	if rt == nil {
		return false
	}
	return probeVirtualRouterRuntimeHasPhysicalBridgeSession(rt)
}

func probeVirtualRouterRuntimeHasPhysicalBridgeSession(rt *probeVirtualRouterRuntime) bool {
	if rt == nil {
		return false
	}
	return currentProbeVirtualRouterPhysicalCarrier(rt) != nil
}

func currentProbeVirtualRouterPhysicalCarrier(rt *probeVirtualRouterRuntime) *probeVirtualRouterFrameLink {
	if rt == nil {
		return nil
	}
	key := probeVirtualRouterFrameLinkKey(rt, "", "", nil)
	probeVirtualRouterFrameLinkState.mu.Lock()
	item := probeVirtualRouterFrameLinkState.links[key]
	if item == nil {
		probeVirtualRouterFrameLinkState.mu.Unlock()
		return nil
	}
	if isProbeVirtualRouterFrameLinkClosed(item) {
		delete(probeVirtualRouterFrameLinkState.links, key)
		probeVirtualRouterFrameLinkState.mu.Unlock()
		stopProbeVirtualRouterFrameLink(item)
		return nil
	}
	item.mu.Lock()
	hasCarrier := item.carrier != nil
	item.mu.Unlock()
	if !hasCarrier {
		probeVirtualRouterFrameLinkState.mu.Unlock()
		return nil
	}
	probeVirtualRouterFrameLinkState.mu.Unlock()
	return item
}

func detachProbeVirtualRouterStalePhysicalCarrier(rt *probeVirtualRouterRuntime, failureCount int, reason string) {
	if rt == nil || failureCount < probeVirtualRouterCarrierStalePingFailures {
		return
	}
	key := probeVirtualRouterFrameLinkKey(rt, "", "", nil)
	if key == "" {
		return
	}
	probeVirtualRouterFrameLinkState.mu.Lock()
	item := probeVirtualRouterFrameLinkState.links[key]
	probeVirtualRouterFrameLinkState.mu.Unlock()
	if item == nil || isProbeVirtualRouterFrameLinkClosed(item) {
		return
	}
	item.mu.Lock()
	token := item.carrier
	item.mu.Unlock()
	if token == nil {
		return
	}
	lastReadAt := token.lastRead()
	if lastReadAt.IsZero() {
		lastReadAt = token.connectedAt
	}
	idleFor := time.Since(lastReadAt)
	if idleFor < probeVirtualRouterCarrierStaleRXGrace {
		return
	}
	log.Printf("probe virtual router physical carrier stale, detach for reconnect: route=%s role=%s session_id=%s remote=%s failures=%d rx_idle_ms=%d reason=%s", strings.TrimSpace(rt.cfg.routeID), probeVirtualRouterRuntimeRole, strings.TrimSpace(token.sessionID), strings.TrimSpace(token.remoteAddr), failureCount, probeDurationMilliseconds(idleFor), strings.TrimSpace(reason))
	item.detachCarrierWithReason(token, "stale_ping", errors.New(strings.TrimSpace(reason)))
}

func probeVirtualRouterNextHopInPath(path []string, localNodeID string) string {
	local := normalizeProbeRouteNodeID(localNodeID)
	if local == "" || len(path) < 2 {
		return ""
	}
	for i, item := range path {
		if normalizeProbeRouteNodeID(item) != local {
			continue
		}
		if i+1 < len(path) {
			return normalizeProbeRouteNodeID(path[i+1])
		}
		return ""
	}
	return ""
}

func probeVirtualRouterPathFromAssociation(association *probeRouteAssociationV2Meta) []string {
	if association == nil {
		return nil
	}
	return parseProbeVirtualRouterPathText(association.RouteTarget)
}

func probeVirtualRouterIPv4Destination(packet []byte) string {
	if len(packet) < 20 || packet[0]>>4 != 4 {
		return ""
	}
	ihl := int(packet[0]&0x0F) * 4
	if ihl < 20 || len(packet) < ihl {
		return ""
	}
	totalLen := int(packet[2])<<8 | int(packet[3])
	if totalLen > 0 && totalLen > len(packet) {
		return ""
	}
	ip := net.IPv4(packet[16], packet[17], packet[18], packet[19]).To4()
	if ip == nil {
		return ""
	}
	return ip.String()
}

func probeVirtualRouterIPv4Source(packet []byte) string {
	if len(packet) < 20 || packet[0]>>4 != 4 {
		return ""
	}
	ihl := int(packet[0]&0x0F) * 4
	if ihl < 20 || len(packet) < ihl {
		return ""
	}
	totalLen := int(packet[2])<<8 | int(packet[3])
	if totalLen > 0 && totalLen > len(packet) {
		return ""
	}
	ip := net.IPv4(packet[12], packet[13], packet[14], packet[15]).To4()
	if ip == nil {
		return ""
	}
	return ip.String()
}

func probeVirtualRouterParseICMPEchoLogInfo(packet []byte) (probeVirtualRouterICMPEchoLogInfo, bool) {
	if len(packet) < 28 || packet[0]>>4 != 4 {
		return probeVirtualRouterICMPEchoLogInfo{}, false
	}
	ihl := int(packet[0]&0x0F) * 4
	if ihl < 20 || len(packet) < ihl+8 {
		return probeVirtualRouterICMPEchoLogInfo{}, false
	}
	totalLen := int(binary.BigEndian.Uint16(packet[2:4]))
	if totalLen <= 0 || totalLen > len(packet) || totalLen < ihl+8 {
		return probeVirtualRouterICMPEchoLogInfo{}, false
	}
	if packet[9] != 1 {
		return probeVirtualRouterICMPEchoLogInfo{}, false
	}
	icmp := packet[ihl:totalLen]
	kind := ""
	switch {
	case icmp[0] == 8 && icmp[1] == 0:
		kind = "echo_request"
	case icmp[0] == 0 && icmp[1] == 0:
		kind = "echo_reply"
	default:
		return probeVirtualRouterICMPEchoLogInfo{}, false
	}
	return probeVirtualRouterICMPEchoLogInfo{
		Kind:          kind,
		SourceIP:      net.IPv4(packet[12], packet[13], packet[14], packet[15]).String(),
		DestinationIP: net.IPv4(packet[16], packet[17], packet[18], packet[19]).String(),
		ID:            binary.BigEndian.Uint16(icmp[4:6]),
		Sequence:      binary.BigEndian.Uint16(icmp[6:8]),
	}, true
}

func probeVirtualRouterParseTCPUDPLogInfo(packet []byte) (probeVirtualRouterTransportLogInfo, bool) {
	if len(packet) < 28 || packet[0]>>4 != 4 {
		return probeVirtualRouterTransportLogInfo{}, false
	}
	ihl := int(packet[0]&0x0F) * 4
	if ihl < 20 || len(packet) < ihl+8 {
		return probeVirtualRouterTransportLogInfo{}, false
	}
	totalLen := int(binary.BigEndian.Uint16(packet[2:4]))
	if totalLen <= 0 || totalLen > len(packet) || totalLen < ihl+8 {
		return probeVirtualRouterTransportLogInfo{}, false
	}
	protocol := ""
	switch packet[9] {
	case 6:
		protocol = "tcp"
	case 17:
		protocol = "udp"
	default:
		return probeVirtualRouterTransportLogInfo{}, false
	}
	transport := packet[ihl:totalLen]
	info := probeVirtualRouterTransportLogInfo{
		Protocol:        protocol,
		SourceIP:        net.IPv4(packet[12], packet[13], packet[14], packet[15]).String(),
		DestinationIP:   net.IPv4(packet[16], packet[17], packet[18], packet[19]).String(),
		SourcePort:      binary.BigEndian.Uint16(transport[0:2]),
		DestinationPort: binary.BigEndian.Uint16(transport[2:4]),
	}
	if protocol == "tcp" && len(transport) >= 14 {
		info.TCPFlags = formatProbeVirtualRouterTCPFlags(transport[13])
	}
	return info, true
}

func formatProbeVirtualRouterTCPFlags(flags byte) string {
	if flags == 0 {
		return ""
	}
	names := make([]string, 0, 8)
	if flags&0x80 != 0 {
		names = append(names, "CWR")
	}
	if flags&0x40 != 0 {
		names = append(names, "ECE")
	}
	if flags&0x20 != 0 {
		names = append(names, "URG")
	}
	if flags&0x10 != 0 {
		names = append(names, "ACK")
	}
	if flags&0x08 != 0 {
		names = append(names, "PSH")
	}
	if flags&0x04 != 0 {
		names = append(names, "RST")
	}
	if flags&0x02 != 0 {
		names = append(names, "SYN")
	}
	if flags&0x01 != 0 {
		names = append(names, "FIN")
	}
	return strings.Join(names, ",")
}

func probeVirtualRouterPacketChecksumSummary(packet []byte) string {
	if len(packet) < 20 || packet[0]>>4 != 4 {
		return ""
	}
	ihl := int(packet[0]&0x0F) * 4
	if ihl < 20 || len(packet) < ihl {
		return ""
	}
	totalLen := int(binary.BigEndian.Uint16(packet[2:4]))
	if totalLen <= 0 || totalLen > len(packet) || totalLen < ihl {
		return ""
	}
	parts := make([]string, 0, 2)
	ipChecksum := "bad"
	if probeVirtualRouterChecksum(packet[:ihl]) == 0 {
		ipChecksum = "ok"
	}
	parts = append(parts, "ip_checksum="+ipChecksum)
	transport := packet[ihl:totalLen]
	switch packet[9] {
	case 6:
		if len(transport) < 20 {
			parts = append(parts, "tcp_checksum=short")
			break
		}
		checksum := binary.BigEndian.Uint16(transport[16:18])
		tcpChecksum := "bad"
		if checksum == 0 {
			tcpChecksum = "zero"
		} else if probeVirtualRouterTransportChecksum(packet, transport) == 0 {
			tcpChecksum = "ok"
		}
		parts = append(parts, "tcp_checksum="+tcpChecksum)
	case 17:
		if len(transport) < 8 {
			parts = append(parts, "udp_checksum=short")
			break
		}
		checksum := binary.BigEndian.Uint16(transport[6:8])
		udpChecksum := "ok"
		if checksum != 0 && probeVirtualRouterTransportChecksum(packet, transport) != 0 {
			udpChecksum = "bad"
		}
		parts = append(parts, "udp_checksum="+udpChecksum)
	}
	return strings.Join(parts, " ")
}

func probeVirtualRouterTransportChecksum(packet []byte, transport []byte) uint16 {
	pseudo := make([]byte, 12+len(transport))
	copy(pseudo[0:4], packet[12:16])
	copy(pseudo[4:8], packet[16:20])
	pseudo[9] = packet[9]
	binary.BigEndian.PutUint16(pseudo[10:12], uint16(len(transport)))
	copy(pseudo[12:], transport)
	return probeVirtualRouterChecksum(pseudo)
}

func buildProbeVirtualRouterICMPEchoReply(packet []byte, localIP string) ([]byte, string, bool) {
	local := net.ParseIP(strings.TrimSpace(localIP)).To4()
	if local == nil || len(packet) < 28 || packet[0]>>4 != 4 {
		return nil, "", false
	}
	ihl := int(packet[0]&0x0F) * 4
	if ihl < 20 || len(packet) < ihl+8 {
		return nil, "", false
	}
	totalLen := int(binary.BigEndian.Uint16(packet[2:4]))
	if totalLen <= 0 || totalLen > len(packet) || totalLen < ihl+8 {
		return nil, "", false
	}
	if packet[9] != 1 {
		return nil, "", false
	}
	dst := net.IPv4(packet[16], packet[17], packet[18], packet[19]).To4()
	if dst == nil || !dst.Equal(local) {
		return nil, "", false
	}
	icmp := packet[ihl:totalLen]
	if len(icmp) < 8 || icmp[0] != 8 || icmp[1] != 0 {
		return nil, "", false
	}
	reply := append([]byte(nil), packet[:totalLen]...)
	copy(reply[12:16], packet[16:20])
	copy(reply[16:20], packet[12:16])
	reply[8] = 64
	reply[10], reply[11] = 0, 0
	binary.BigEndian.PutUint16(reply[10:12], probeVirtualRouterChecksum(reply[:ihl]))
	reply[ihl] = 0
	reply[ihl+1] = 0
	reply[ihl+2], reply[ihl+3] = 0, 0
	binary.BigEndian.PutUint16(reply[ihl+2:ihl+4], probeVirtualRouterChecksum(reply[ihl:totalLen]))
	return reply, net.IPv4(packet[12], packet[13], packet[14], packet[15]).String(), true
}

func probeVirtualRouterChecksum(payload []byte) uint16 {
	var sum uint32
	for len(payload) >= 2 {
		sum += uint32(binary.BigEndian.Uint16(payload[:2]))
		payload = payload[2:]
	}
	if len(payload) > 0 {
		sum += uint32(payload[0]) << 8
	}
	for sum>>16 != 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return ^uint16(sum)
}
