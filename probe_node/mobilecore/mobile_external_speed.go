package mobilecore

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/cloudhelper/probe_node/internal/externalspeed"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var mobileExternalJob externalspeed.Job
var mobileExternalActive atomic.Bool
var mobileExternalPacket struct {
	sync.Mutex
	packet *externalspeed.PacketConn
	path   []string
}

// VRouteExternalSpeed starts, stops or polls one bounded external test. An empty
// node is physical direct access (the app itself is excluded from Android VPN).
func VRouteExternalSpeed(action, node string) string {
	node = strings.TrimSpace(node)
	var err error
	if len(node) > 128 {
		err = errors.New("无效出口节点")
	} else {
		switch action {
		case "start":
			err = mobileExternalJob.Start(node, mobileExternalFactory)
		case "stop":
			mobileExternalJob.Stop()
		case "status":
		default:
			err = errors.New("无效测速操作")
		}
	}
	if err != nil {
		return marshalRouteJSON(map[string]any{"ok": false, "error": err.Error(), "result": mobileExternalJob.Snapshot()})
	}
	return marshalRouteJSON(map[string]any{"ok": true, "result": mobileExternalJob.Snapshot()})
}
func feedMobileExternalPacket(data []byte, path []string) bool {
	if !mobileExternalActive.Load() {
		return false
	}
	mobileExternalPacket.Lock()
	defer mobileExternalPacket.Unlock()
	reverse := mobileExternalPacket.path
	return mobileExternalPacket.packet != nil && slices.Equal(path, reverse) && mobileExternalPacket.packet.Feed(data)
}
func mobileExternalFactory(ctx context.Context, node string) (*http.Client, func(), []string, error) {
	var plan mobileVRouteForwardPlan
	var err error
	if node != "" {
		plan, err = buildMobileVRouteForwardPlan(currentAndroidVPNConfigDir(), mobileVRouteProbeExitRouteID(node))
		if err != nil {
			return nil, nil, nil, err
		}
	}
	// Use real upstream DNS, never the shared fake-IP allocator.
	ips, err := resolveAndroidVPNDomainIPv4s("speed.cloudflare.com")
	if err != nil {
		return nil, nil, plan.Path, err
	}
	var target string
	for _, ip := range ips {
		if externalspeed.PublicIPv4(ip) {
			target = ip
			break
		}
	}
	if target == "" {
		return nil, nil, plan.Path, errors.New("测速源没有可用公网 IPv4")
	}
	if node == "" {
		dialer := &net.Dialer{Timeout: 5 * time.Second}
		return externalspeed.Client(func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "tcp4", net.JoinHostPort(target, "443"))
		}), nil, nil, nil
	}
	control, err := json.Marshal(mobileVRouteFrameControlEnvelope{Path: plan.Path})
	if err != nil {
		return nil, nil, plan.Path, err
	}
	packet, err := externalspeed.NewPacketConn(ctx, mobileVRouteProbeIPForNode(plan.Config, plan.LocalNode), target, func(data []byte) error {
		frame := mobileVRouteFrame{MainType: mobileVRouteFrameMainTypeIP, SubType: mobileVRouteIPSubTypeIPv4, Control: control, Data: data}
		carrier, err := selectMobileVRouteCarrier(plan, frame, nil)
		if err != nil {
			return err
		}
		return carrier.enqueueFrame(frame)
	})
	if err != nil {
		return nil, nil, plan.Path, err
	}
	mobileExternalPacket.Lock()
	mobileExternalPacket.packet = packet
	mobileExternalPacket.path = append([]string(nil), plan.Path...)
	slices.Reverse(mobileExternalPacket.path)
	mobileExternalActive.Store(true)
	mobileExternalPacket.Unlock()
	cleanup := func() {
		mobileExternalPacket.Lock()
		if mobileExternalPacket.packet == packet {
			mobileExternalActive.Store(false)
			mobileExternalPacket.packet = nil
			mobileExternalPacket.path = nil
		}
		mobileExternalPacket.Unlock()
		packet.Close()
	}
	return externalspeed.Client(packet.DialContext), cleanup, plan.Path, nil
}
