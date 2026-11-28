package main

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/cloudhelper/probe_node/internal/externalspeed"
	"io"
	"strings"

	"net"
	"net/http"
	"slices"
	"strconv"

	"sync"
	"sync/atomic"
	"time"
)

const probeExternalSpeedMaxBytes = externalspeed.MaxBytes
const probeExternalSpeedDuration = externalspeed.Duration

type probeExternalSpeedResult = externalspeed.Result

var runProbeExternalSpeed = externalspeed.Run
var probeExternalSpeedState externalspeed.Job
var probeExternalActive atomic.Bool
var probeExternalPacket struct {
	sync.Mutex
	packet *externalspeed.PacketConn
	path   []string
}

func feedProbeExternalPacket(data []byte, path []string) bool {
	if !probeExternalActive.Load() {
		return false
	}
	probeExternalPacket.Lock()
	defer probeExternalPacket.Unlock()
	return probeExternalPacket.packet != nil && slices.Equal(path, probeExternalPacket.path) && probeExternalPacket.packet.Feed(data)
}
func probeExternalSpeedFactory(ctx context.Context, node string, targetPort uint16) (*http.Client, func(), []string, error) {
	if node == "" {
		return probeExternalSpeedClient(targetPort), nil, nil, nil
	}
	config := currentProbeVirtualRouterConfig()
	local := currentProbeVirtualRouterLocalNodeID()
	path := probeVirtualRouterPath(config, local, node)
	if len(path) < 2 || node == local {
		return nil, nil, path, errors.New("所选出口没有可用路径")
	}
	ips, err := resolveProbeLocalDNSRealIPv4sFromUpstreamsWithContext(ctx, "speed.cloudflare.com", probeLocalDNSRouteDecision{})
	if err != nil {
		return nil, nil, path, err
	}
	var target string
	for _, ip := range ips {
		if externalspeed.PublicIPv4(ip) {
			target = ip
			break
		}
	}
	if target == "" {
		return nil, nil, path, errors.New("测速源没有可用公网 IPv4")
	}
	packet, err := externalspeed.NewPacketConnWithPort(ctx, probeVirtualRouterIPForNode(config, local), target, targetPort, func(data []byte) error { return forwardProbeVirtualRouterPacketAlongPath(data, target, path, nil) })
	if err != nil {
		return nil, nil, path, err
	}
	probeExternalPacket.Lock()
	probeExternalPacket.packet = packet
	probeExternalPacket.path = probeVirtualRouterReversePath(path)
	probeExternalActive.Store(true)
	probeExternalPacket.Unlock()
	cleanup := func() {
		probeExternalPacket.Lock()
		if probeExternalPacket.packet == packet {
			probeExternalActive.Store(false)
			probeExternalPacket.packet = nil
			probeExternalPacket.path = nil
		}
		probeExternalPacket.Unlock()
		packet.Close()
	}
	return externalspeed.Client(packet.DialContext), cleanup, path, nil
}

func probeExternalSpeedClient(targetPort uint16) *http.Client {
	transport := &http.Transport{Proxy: nil, DisableCompression: true, DisableKeepAlives: true, ForceAttemptHTTP2: false, TLSHandshakeTimeout: 6 * time.Second, ResponseHeaderTimeout: 8 * time.Second}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		if host != "speed.cloudflare.com" || port != strconv.Itoa(int(targetPort)) {
			return nil, errors.New("测速目标不受支持")
		}
		ips, err := resolveProbeLocalDNSRealIPv4sFromUpstreamsWithContext(ctx, host, probeLocalDNSRouteDecision{})
		if err != nil {
			return nil, err
		}
		var last error
		for _, raw := range ips {
			if !externalspeed.PublicIPv4(raw) {
				continue
			}
			target := net.JoinHostPort(raw, port)
			// Fail closed if the physical bypass cannot be installed. A direct test
			// must never silently traverse the virtual-router policy it is comparing.
			if probeVirtualRouterLocalEntryEnabled() {
				if err = ensureProbeRouteDirectBypass(target); err != nil {
					last = err
					continue
				}
			}
			dialer := applyProbeRouteEgressDialer(&net.Dialer{Timeout: 5 * time.Second})
			conn, dialErr := dialer.DialContext(ctx, "tcp4", target)
			if dialErr == nil {
				return conn, nil
			}
			last = dialErr
		}
		if last == nil {
			last = errors.New("测速源没有可用公网 IPv4 地址")
		}
		return nil, last
	}
	return &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func probeLocalExternalSpeedHandler(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	if r.Method == http.MethodPost {
		var input struct {
			Action   string `json:"action"`
			NodeID   string `json:"node_id"`
			Protocol string `json:"protocol"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeJSON(w, 400, map[string]string{"error": "无效测速请求"})
			return
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			writeJSON(w, 400, map[string]string{"error": "无效测速请求"})
			return
		}
		input.NodeID = strings.TrimSpace(input.NodeID)
		if len(input.NodeID) > 128 {
			writeJSON(w, 400, map[string]string{"error": "无效节点"})
			return
		}
		switch input.Action {
		case "start":
			var targetPort uint16 = 443
			switch input.Protocol {
			case "http":
				targetPort = 80
			case "", "https":
				input.Protocol = "https"
			default:
				writeJSON(w, 400, map[string]string{"error": "无效测速协议"})
				return
			}
			factory := func(ctx context.Context, node string) (*http.Client, func(), []string, error) {
				return probeExternalSpeedFactory(ctx, node, targetPort)
			}
			if err := probeExternalSpeedState.StartProtocol(input.NodeID, input.Protocol, factory); err != nil {
				writeJSON(w, 409, map[string]string{"error": err.Error()})
				return
			}
		case "stop":
			probeExternalSpeedState.Stop()
		default:
			writeJSON(w, 400, map[string]string{"error": "action 必须是 start 或 stop"})
			return
		}
	} else if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	nodes := []map[string]string{}
	config := currentProbeVirtualRouterConfig()
	local := currentProbeVirtualRouterLocalNodeID()
	for _, n := range config.ProbeIPs {
		if n.NodeID != local {
			nodes = append(nodes, map[string]string{"node_id": n.NodeID, "display_name": probeVirtualRouterDisplayNameForNode(config, n.NodeID)})
		}
	}
	writeJSON(w, 200, map[string]any{"ok": true, "result": probeExternalSpeedState.Snapshot(), "nodes": nodes})
}
