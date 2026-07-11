package mobilecore

import (
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	mobileVRouteTCPFlowIdle = 90 * time.Second
	mobileVRouteUDPFlowIdle = 30 * time.Second
)

type mobileVRouteTrackedFlow struct {
	relay     *androidRouteConnectionRelay
	last      time.Time
	transport string
}

var mobileVRouteTrackedFlowState = struct {
	mu    sync.Mutex
	flows map[string]*mobileVRouteTrackedFlow
}{flows: make(map[string]*mobileVRouteTrackedFlow)}

func trackMobileVRouteOutbound(packet []byte, route vpnRouteDecision, plan mobileVRouteForwardPlan) {
	info, ok := parseAndroidVPNIPv4TransportPacket(packet)
	if !ok {
		return
	}
	key := mobileVRouteFlowKey(info.Protocol, info.SourceIP, info.SourcePort, info.DestinationIP, info.DestinationPort)
	now := time.Now().UTC()
	mobileVRouteTrackedFlowState.mu.Lock()
	flow := mobileVRouteTrackedFlowState.flows[key]
	if flow == nil {
		transport := "udp"
		if info.Protocol == 6 {
			transport = "tcp"
		}
		flow = &mobileVRouteTrackedFlow{
			relay: globalandroidRouteConnectionState.begin(androidRouteConnectionOptions{
				Scope:     "vpn_vroute",
				FlowID:    key,
				Side:      "local",
				Target:    route.TargetAddr,
				Transport: transport,
				Route: androidRouteConnectionRoute{
					Direct:          false,
					TargetAddr:      route.TargetAddr,
					Group:           route.Group,
					SelectedRouteID: plan.RouteID,
				},
			}),
			transport: transport,
		}
		mobileVRouteTrackedFlowState.flows[key] = flow
	}
	flow.last = now
	mobileVRouteTrackedFlowState.mu.Unlock()
	flow.relay.touch("up", info.TotalLength)
	if mobileVRouteTCPPacketClosesFlow(packet, info) {
		finishMobileVRouteTrackedFlow(key, "tcp_closed")
	}
}

func trackMobileVRouteInbound(packet []byte) {
	info, ok := parseAndroidVPNIPv4TransportPacket(packet)
	if !ok {
		return
	}
	key := mobileVRouteFlowKey(info.Protocol, info.DestinationIP, info.DestinationPort, info.SourceIP, info.SourcePort)
	now := time.Now().UTC()
	mobileVRouteTrackedFlowState.mu.Lock()
	flow := mobileVRouteTrackedFlowState.flows[key]
	if flow != nil {
		flow.last = now
	}
	mobileVRouteTrackedFlowState.mu.Unlock()
	if flow == nil {
		return
	}
	flow.relay.touch("down", info.TotalLength)
	if mobileVRouteTCPPacketClosesFlow(packet, info) {
		finishMobileVRouteTrackedFlow(key, "tcp_closed")
	}
}

func recordMobileVRouteConnectionFailure(kind string, target string, routeID string, group string, transport string, err error) {
	if err == nil {
		err = errors.New(kind)
	}
	globalandroidRouteConnectionState.recordFailure(kind, androidRouteConnectionOptions{
		Scope:     "vpn_vroute",
		FlowID:    newAndroidRouteFlowID("vpn_vroute", target),
		Side:      "local",
		Target:    target,
		Transport: transport,
		Route: androidRouteConnectionRoute{
			Direct:          false,
			TargetAddr:      target,
			Group:           group,
			SelectedRouteID: routeID,
		},
	}, err)
}

func cleanupMobileVRouteTrackedFlows() {
	now := time.Now().UTC()
	var expired []string
	mobileVRouteTrackedFlowState.mu.Lock()
	for key, flow := range mobileVRouteTrackedFlowState.flows {
		idle := mobileVRouteUDPFlowIdle
		if flow != nil && flow.transport == "tcp" {
			idle = mobileVRouteTCPFlowIdle
		}
		if flow == nil || flow.last.IsZero() || now.Sub(flow.last) >= idle {
			expired = append(expired, key)
		}
	}
	mobileVRouteTrackedFlowState.mu.Unlock()
	for _, key := range expired {
		finishMobileVRouteTrackedFlow(key, "idle_timeout")
	}
}

func closeMobileVRouteTrackedFlows(reason string) {
	mobileVRouteTrackedFlowState.mu.Lock()
	keys := make([]string, 0, len(mobileVRouteTrackedFlowState.flows))
	for key := range mobileVRouteTrackedFlowState.flows {
		keys = append(keys, key)
	}
	mobileVRouteTrackedFlowState.mu.Unlock()
	for _, key := range keys {
		finishMobileVRouteTrackedFlow(key, firstNonEmptyString(strings.TrimSpace(reason), "closed"))
	}
}

func failMobileVRouteTrackedFlowsForCarrier(plan mobileVRouteForwardPlan, kind string, err error) {
	routeID := strings.TrimSpace(plan.RouteID)
	recordMobileVRouteConnectionFailure(
		kind,
		netJoinMobileVRouteRelay(plan),
		routeID,
		strings.TrimSpace(plan.Rule.Name),
		"stream",
		err,
	)
	mobileVRouteTrackedFlowState.mu.Lock()
	keys := make([]string, 0)
	for key, flow := range mobileVRouteTrackedFlowState.flows {
		if flow != nil && flow.relay != nil && strings.TrimSpace(flow.relay.routeID) == routeID {
			keys = append(keys, key)
		}
	}
	mobileVRouteTrackedFlowState.mu.Unlock()
	for _, key := range keys {
		finishMobileVRouteTrackedFlow(key, kind)
	}
}

func netJoinMobileVRouteRelay(plan mobileVRouteForwardPlan) string {
	if strings.TrimSpace(plan.RelayHost) == "" || plan.RelayPort <= 0 {
		return strings.TrimSpace(plan.NextNode)
	}
	return net.JoinHostPort(strings.TrimSpace(plan.RelayHost), strconv.Itoa(plan.RelayPort))
}

func finishMobileVRouteTrackedFlow(key string, reason string) {
	mobileVRouteTrackedFlowState.mu.Lock()
	flow := mobileVRouteTrackedFlowState.flows[key]
	delete(mobileVRouteTrackedFlowState.flows, key)
	mobileVRouteTrackedFlowState.mu.Unlock()
	if flow == nil || flow.relay == nil {
		return
	}
	flow.relay.finish(reason)
	flow.relay.state.mu.Lock()
	delete(flow.relay.state.active, flow.relay.id)
	flow.relay.state.mu.Unlock()
}

func mobileVRouteFlowKey(protocol uint8, sourceIP string, sourcePort uint16, destinationIP string, destinationPort uint16) string {
	return strconv.Itoa(int(protocol)) + "|" + strings.TrimSpace(sourceIP) + "|" + strconv.Itoa(int(sourcePort)) + "|" + strings.TrimSpace(destinationIP) + "|" + strconv.Itoa(int(destinationPort))
}

func mobileVRouteTCPPacketClosesFlow(packet []byte, info androidVPNIPv4TransportInfo) bool {
	if info.Protocol != 6 || info.HeaderLength < 20 || len(packet) <= info.HeaderLength+13 {
		return false
	}
	flags := packet[info.HeaderLength+13]
	return flags&0x05 != 0
}
