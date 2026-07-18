package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	probeVirtualRouterFakeIPVerifyMinTimeout      = 5 * time.Second
	probeVirtualRouterFakeIPVerifyMaxTimeout      = 15 * time.Second
	probeVirtualRouterFakeIPVerifyRTTPadding      = 2 * time.Second
	probeVirtualRouterFakeIPVerifyCooldown        = 15 * time.Second
	probeVirtualRouterFakeIPVerifyMaxCooldown     = 5 * time.Minute
	probeVirtualRouterFakeIPVerifyMaxConcurrent   = 4
	probeVirtualRouterFakeIPVerifyLogPeriod       = 30 * time.Second
	probeVirtualRouterFakeIPVerifySYNWindow       = 5 * time.Second
	probeVirtualRouterFakeIPVerifySYNTriggerCount = 2
	probeVirtualRouterFakeIPVerifySYNFlowMaxAge   = 30 * time.Second
)

var probeVirtualRouterFakeIPVerifyState = struct {
	mu       sync.Mutex
	pending  map[string]chan probeVirtualRouterFakeIPVerifyPayload
	running  map[string]bool
	lastAt   map[string]time.Time
	failures map[string]int
	synFlows map[string]probeVirtualRouterFakeIPVerifySYNFlow
}{
	pending:  make(map[string]chan probeVirtualRouterFakeIPVerifyPayload),
	running:  make(map[string]bool),
	lastAt:   make(map[string]time.Time),
	failures: make(map[string]int),
	synFlows: make(map[string]probeVirtualRouterFakeIPVerifySYNFlow),
}

type probeVirtualRouterFakeIPVerifySYNFlow struct {
	FirstAt time.Time
	LastAt  time.Time
	Count   int
}

type probeVirtualRouterFakeIPVerifyPayload struct {
	RequestID         string   `json:"request_id"`
	SourceNodeID      string   `json:"source_node_id,omitempty"`
	ExitNodeID        string   `json:"exit_node_id,omitempty"`
	Path              []string `json:"path,omitempty"`
	Target            string   `json:"target,omitempty"`
	Domain            string   `json:"domain,omitempty"`
	TargetIP          string   `json:"target_ip,omitempty"`
	FakeIP            string   `json:"fake_ip,omitempty"`
	Port              int      `json:"port,omitempty"`
	Protocol          string   `json:"protocol,omitempty"`
	Reason            string   `json:"reason,omitempty"`
	ResolvedIPs       []string `json:"resolved_ips,omitempty"`
	CheckedAddress    string   `json:"checked_address,omitempty"`
	LatencyMS         int64    `json:"latency_ms,omitempty"`
	CreatedAtUnixNano int64    `json:"created_at_unix_nano,omitempty"`
	OK                bool     `json:"ok,omitempty"`
	Error             string   `json:"error,omitempty"`
	Responder         string   `json:"responder,omitempty"`
}

func maybeScheduleProbeVirtualRouterFakeIPVerifyForTCPRetransmit(packet []byte, path []string) bool {
	info, ok := probeVirtualRouterParseTCPUDPLogInfo(packet)
	if !ok || !strings.EqualFold(info.Protocol, "tcp") || info.DestinationPort == 0 {
		return false
	}
	flags := strings.ToUpper(strings.TrimSpace(info.TCPFlags))
	if !strings.Contains(flags, "SYN") || strings.Contains(flags, "ACK") {
		return false
	}
	if !probeVirtualRouterIPCanBeFakeIP(info.DestinationIP) || len(cleanProbeVirtualRouterPath(path)) < 2 {
		return false
	}
	now := time.Now()
	key := strings.Join([]string{
		info.SourceIP,
		strconv.Itoa(int(info.SourcePort)),
		info.DestinationIP,
		strconv.Itoa(int(info.DestinationPort)),
	}, "|")
	shouldVerify := false
	probeVirtualRouterFakeIPVerifyState.mu.Lock()
	for flowKey, flow := range probeVirtualRouterFakeIPVerifyState.synFlows {
		if !flow.LastAt.IsZero() && now.Sub(flow.LastAt) > probeVirtualRouterFakeIPVerifySYNFlowMaxAge {
			delete(probeVirtualRouterFakeIPVerifyState.synFlows, flowKey)
		}
	}
	flow := probeVirtualRouterFakeIPVerifyState.synFlows[key]
	if flow.FirstAt.IsZero() || now.Sub(flow.FirstAt) > probeVirtualRouterFakeIPVerifySYNWindow {
		flow = probeVirtualRouterFakeIPVerifySYNFlow{FirstAt: now}
	}
	flow.LastAt = now
	flow.Count++
	probeVirtualRouterFakeIPVerifyState.synFlows[key] = flow
	shouldVerify = flow.Count >= probeVirtualRouterFakeIPVerifySYNTriggerCount
	probeVirtualRouterFakeIPVerifyState.mu.Unlock()
	if !shouldVerify {
		return false
	}
	return scheduleProbeVirtualRouterFakeIPVerifyForPacket(packet, path, "tcp_syn_retransmit")
}

func scheduleProbeVirtualRouterFakeIPVerifyForPacket(packet []byte, path []string, reason string) bool {
	dstIP := probeVirtualRouterIPv4Destination(packet)
	if !probeVirtualRouterIPCanBeFakeIP(dstIP) {
		return false
	}
	cleanPath := cleanProbeVirtualRouterPath(path)
	if len(cleanPath) < 2 {
		return false
	}
	entry, ok := currentProbeVirtualRouterFakeIPEntryByIP(dstIP)
	if !ok {
		scheduleProbeVirtualRouterFakeIPItemRefreshByIP(dstIP)
		return false
	}
	exitNodeID := normalizeProbeRouteNodeID(entry.ExitNodeID)
	if exitNodeID == "" {
		exitNodeID = normalizeProbeRouteNodeID(cleanPath[len(cleanPath)-1])
	}
	if exitNodeID == "" {
		return false
	}
	if !probeVirtualRouterPathContainsNode(cleanPath, exitNodeID) {
		exitPath := currentProbeVirtualRouterPathBetweenNodes(currentProbeVirtualRouterLocalNodeID(), exitNodeID)
		if len(exitPath) >= 2 {
			cleanPath = exitPath
		}
	}
	if len(cleanPath) < 2 {
		return false
	}
	port, protocol := probeVirtualRouterFakeIPVerifyPortProtocol(packet)
	if port <= 0 {
		port = probeVirtualRouterRouteTestDefaultPort
	}
	msg := probeVirtualRouterFakeIPVerifyPayload{
		SourceNodeID: currentProbeVirtualRouterLocalNodeID(),
		ExitNodeID:   exitNodeID,
		Path:         cleanPath,
		Target:       strings.TrimSpace(entry.Domain),
		Domain:       normalizeProbeVirtualRouterDomain(entry.Domain),
		FakeIP:       strings.TrimSpace(entry.FakeIP),
		Port:         port,
		Protocol:     protocol,
		Reason:       strings.TrimSpace(reason),
	}
	return scheduleProbeVirtualRouterFakeIPVerify(msg, packet)
}

func scheduleProbeVirtualRouterFakeIPVerify(msg probeVirtualRouterFakeIPVerifyPayload, packet []byte) bool {
	msg.Path = cleanProbeVirtualRouterPath(msg.Path)
	msg.SourceNodeID = normalizeProbeRouteNodeID(msg.SourceNodeID)
	msg.ExitNodeID = normalizeProbeRouteNodeID(msg.ExitNodeID)
	msg.Domain = normalizeProbeVirtualRouterDomain(msg.Domain)
	if msg.SourceNodeID == "" || msg.ExitNodeID == "" || len(msg.Path) < 2 {
		return false
	}
	if msg.Path[0] != msg.SourceNodeID {
		return false
	}
	if msg.Path[len(msg.Path)-1] != msg.ExitNodeID {
		return false
	}
	if msg.Domain == "" && net.ParseIP(strings.TrimSpace(msg.TargetIP)).To4() == nil {
		return false
	}
	if msg.Port <= 0 || msg.Port > 65535 {
		msg.Port = probeVirtualRouterRouteTestDefaultPort
	}
	if strings.TrimSpace(msg.Protocol) == "" {
		msg.Protocol = "tcp"
	}
	key := probeVirtualRouterFakeIPVerifyKey(msg)
	now := time.Now()
	probeVirtualRouterFakeIPVerifyState.mu.Lock()
	if probeVirtualRouterFakeIPVerifyState.running[key] {
		probeVirtualRouterFakeIPVerifyState.mu.Unlock()
		return false
	}
	if len(probeVirtualRouterFakeIPVerifyState.running) >= probeVirtualRouterFakeIPVerifyMaxConcurrent {
		probeVirtualRouterFakeIPVerifyState.mu.Unlock()
		return false
	}
	cooldown := probeVirtualRouterFakeIPVerifyCooldownForFailures(probeVirtualRouterFakeIPVerifyState.failures[key])
	if lastAt := probeVirtualRouterFakeIPVerifyState.lastAt[key]; !lastAt.IsZero() && now.Sub(lastAt) < cooldown {
		probeVirtualRouterFakeIPVerifyState.mu.Unlock()
		return false
	}
	probeVirtualRouterFakeIPVerifyState.running[key] = true
	probeVirtualRouterFakeIPVerifyState.lastAt[key] = now
	probeVirtualRouterFakeIPVerifyState.mu.Unlock()
	packetCopy := append([]byte(nil), packet...)
	go func() {
		timeout := probeVirtualRouterFakeIPVerifyTimeoutForPath(msg.Path)
		response, err := queryProbeVirtualRouterFakeIPVerify(msg, timeout)
		failureCount := completeProbeVirtualRouterFakeIPVerifySchedule(key, err == nil && response.OK)
		if err != nil {
			logKey := strings.Join([]string{"fake_ip_verify", strings.Join(msg.Path, ">"), strings.TrimSpace(msg.Reason), "query_error"}, "|")
			shouldLog, suppressed := takeProbeVirtualRouterLogThrottle(logKey, probeVirtualRouterFakeIPVerifyLogPeriod, time.Now())
			if shouldLog {
				log.Printf("probe virtual router fake ip verify failed: domain=%s fake_ip=%s port=%d path=%s reason=%s timeout_ms=%d consecutive_failures=%d suppressed=%d err=%v", msg.Domain, strings.TrimSpace(msg.FakeIP), msg.Port, strings.Join(msg.Path, ">"), strings.TrimSpace(msg.Reason), probeDurationMilliseconds(timeout), failureCount, suppressed, err)
			}
			if shouldLog && len(packetCopy) > 0 {
				recordProbeVirtualRouterRecentPacket("fake_verify", "verify_error", nil, packetCopy, msg.Path, false, err)
			}
			return
		}
		outcome := "result_ok"
		if !response.OK {
			outcome = "result_error"
		}
		logKey := strings.Join([]string{"fake_ip_verify", strings.Join(msg.Path, ">"), strings.TrimSpace(msg.Reason), outcome}, "|")
		shouldLog, suppressed := takeProbeVirtualRouterLogThrottle(logKey, probeVirtualRouterFakeIPVerifyLogPeriod, time.Now())
		if shouldLog && !response.OK {
			log.Printf("probe virtual router fake ip verify result: ok=%v domain=%s fake_ip=%s port=%d resolved=%s checked=%s latency_ms=%d path=%s reason=%s consecutive_failures=%d suppressed=%d err=%s", response.OK, msg.Domain, strings.TrimSpace(msg.FakeIP), msg.Port, strings.Join(response.ResolvedIPs, ","), strings.TrimSpace(response.CheckedAddress), response.LatencyMS, strings.Join(msg.Path, ">"), strings.TrimSpace(msg.Reason), failureCount, suppressed, strings.TrimSpace(response.Error))
		}
		if shouldLog && len(packetCopy) > 0 {
			action := "verify_ok"
			if !response.OK {
				action = "verify_error"
			}
			err := error(nil)
			if strings.TrimSpace(response.Error) != "" {
				err = errors.New(strings.TrimSpace(response.Error))
			}
			recordProbeVirtualRouterRecentPacket("fake_verify", action, nil, packetCopy, msg.Path, false, err)
		}
	}()
	return true
}

func probeVirtualRouterFakeIPVerifyKey(msg probeVirtualRouterFakeIPVerifyPayload) string {
	return strings.Join([]string{msg.SourceNodeID, msg.ExitNodeID, msg.Domain, strings.TrimSpace(msg.TargetIP), strings.TrimSpace(msg.FakeIP), strconv.Itoa(msg.Port), strings.TrimSpace(msg.Protocol)}, "|")
}

func probeVirtualRouterFakeIPVerifyTimeoutForPath(path []string) time.Duration {
	latencyMS, _ := probeVirtualRouterPathRTTScore(cleanProbeVirtualRouterPath(path))
	if latencyMS <= 0 {
		return probeVirtualRouterFakeIPVerifyMinTimeout
	}
	timeout := 2*time.Duration(latencyMS)*time.Millisecond + probeVirtualRouterFakeIPVerifyRTTPadding
	if timeout < probeVirtualRouterFakeIPVerifyMinTimeout {
		return probeVirtualRouterFakeIPVerifyMinTimeout
	}
	if timeout > probeVirtualRouterFakeIPVerifyMaxTimeout {
		return probeVirtualRouterFakeIPVerifyMaxTimeout
	}
	return timeout
}

func probeVirtualRouterFakeIPVerifyCooldownForFailures(failures int) time.Duration {
	cooldown := probeVirtualRouterFakeIPVerifyCooldown
	for attempt := 0; attempt < failures && cooldown < probeVirtualRouterFakeIPVerifyMaxCooldown; attempt++ {
		cooldown *= 2
		if cooldown >= probeVirtualRouterFakeIPVerifyMaxCooldown {
			return probeVirtualRouterFakeIPVerifyMaxCooldown
		}
	}
	return cooldown
}

func completeProbeVirtualRouterFakeIPVerifySchedule(key string, success bool) int {
	probeVirtualRouterFakeIPVerifyState.mu.Lock()
	defer probeVirtualRouterFakeIPVerifyState.mu.Unlock()
	if !probeVirtualRouterFakeIPVerifyState.running[key] {
		return 0
	}
	delete(probeVirtualRouterFakeIPVerifyState.running, key)
	probeVirtualRouterFakeIPVerifyState.lastAt[key] = time.Now()
	if success {
		delete(probeVirtualRouterFakeIPVerifyState.failures, key)
		return 0
	}
	probeVirtualRouterFakeIPVerifyState.failures[key]++
	return probeVirtualRouterFakeIPVerifyState.failures[key]
}

func queryProbeVirtualRouterFakeIPVerify(msg probeVirtualRouterFakeIPVerifyPayload, timeout time.Duration) (probeVirtualRouterFakeIPVerifyPayload, error) {
	msg.Path = cleanProbeVirtualRouterPath(msg.Path)
	if len(msg.Path) < 2 {
		return probeVirtualRouterFakeIPVerifyPayload{}, errors.New("fake ip verify path is incomplete")
	}
	if timeout <= 0 {
		timeout = probeVirtualRouterFakeIPVerifyMinTimeout
	}
	msg.RequestID = newProbeTCPDebugFlowID("vrouter_fake_ip_verify", firstNonEmpty(msg.Domain, msg.FakeIP, msg.TargetIP))
	msg.CreatedAtUnixNano = time.Now().UnixNano()
	waiter := registerProbeVirtualRouterFakeIPVerifyResponse(msg.RequestID)
	defer unregisterProbeVirtualRouterFakeIPVerifyResponse(msg.RequestID)
	payload, err := json.Marshal(msg)
	if err != nil {
		return probeVirtualRouterFakeIPVerifyPayload{}, err
	}
	if err := forwardProbeVirtualRouterBusinessAlongPath(probeVirtualRouterFrameMainTypeFakeIPVerify, probeVirtualRouterFakeIPVerifySubTypeQuery, payload, msg.Path); err != nil {
		return probeVirtualRouterFakeIPVerifyPayload{}, err
	}
	response, err := waitProbeVirtualRouterFakeIPVerifyResponse(waiter, timeout)
	if err != nil {
		return probeVirtualRouterFakeIPVerifyPayload{}, fmt.Errorf("%w: request_id=%s exit=%s path=%s", err, msg.RequestID, msg.ExitNodeID, strings.Join(msg.Path, ">"))
	}
	return response, nil
}

func handleProbeVirtualRouterFakeIPVerifyFrame(runtime *probeVirtualRouterRuntime, subType uint16, msg probeVirtualRouterFakeIPVerifyPayload) error {
	localNodeID := currentProbeVirtualRouterLocalNodeIDForRuntime(runtime)
	if localNodeID == "" {
		localNodeID = currentProbeVirtualRouterLocalNodeID()
	}
	localNodeID = normalizeProbeRouteNodeID(localNodeID)
	msg.Path = cleanProbeVirtualRouterPath(msg.Path)
	if localNodeID == "" || msg.RequestID == "" || len(msg.Path) < 2 {
		return errors.New("virtual router fake ip verify frame is incomplete")
	}
	switch subType {
	case probeVirtualRouterFakeIPVerifySubTypeQuery:
		exitNodeID := normalizeProbeRouteNodeID(msg.ExitNodeID)
		if exitNodeID == "" {
			exitNodeID = msg.Path[len(msg.Path)-1]
		}
		if exitNodeID == localNodeID || probeVirtualRouterNextHopInPath(msg.Path, localNodeID) == "" {
			return sendProbeVirtualRouterFakeIPVerifyResponse(msg, runtime)
		}
		payload, err := json.Marshal(msg)
		if err != nil {
			return err
		}
		return forwardProbeVirtualRouterBusinessAlongPath(probeVirtualRouterFrameMainTypeFakeIPVerify, probeVirtualRouterFakeIPVerifySubTypeQuery, payload, msg.Path)
	case probeVirtualRouterFakeIPVerifySubTypeResponse:
		if normalizeProbeRouteNodeID(msg.SourceNodeID) == localNodeID || msg.Path[len(msg.Path)-1] == localNodeID {
			completeProbeVirtualRouterFakeIPVerifyResponse(msg)
			return nil
		}
		payload, err := json.Marshal(msg)
		if err != nil {
			return err
		}
		return forwardProbeVirtualRouterBusinessAlongPath(probeVirtualRouterFrameMainTypeFakeIPVerify, probeVirtualRouterFakeIPVerifySubTypeResponse, payload, msg.Path)
	default:
		return fmt.Errorf("unsupported virtual router fake ip verify subtype=%d", subType)
	}
}

func sendProbeVirtualRouterFakeIPVerifyResponse(request probeVirtualRouterFakeIPVerifyPayload, runtime *probeVirtualRouterRuntime) error {
	response := runProbeVirtualRouterFakeIPVerifyAtExit(request, runtime)
	response.Path = probeVirtualRouterReversePath(request.Path)
	payload, err := json.Marshal(response)
	if err != nil {
		return err
	}
	return forwardProbeVirtualRouterBusinessAlongPath(probeVirtualRouterFrameMainTypeFakeIPVerify, probeVirtualRouterFakeIPVerifySubTypeResponse, payload, response.Path)
}

func runProbeVirtualRouterFakeIPVerifyAtExit(request probeVirtualRouterFakeIPVerifyPayload, runtime *probeVirtualRouterRuntime) probeVirtualRouterFakeIPVerifyPayload {
	started := time.Now()
	response := request
	response.OK = false
	response.Error = ""
	response.Responder = currentProbeVirtualRouterLocalNodeIDForRuntime(runtime)
	response.CreatedAtUnixNano = time.Now().UnixNano()
	port := normalizeProbeVirtualRouterRouteTestPort(request.Port)
	var ips []string
	var err error
	if domain := normalizeProbeVirtualRouterDomain(request.Domain); domain != "" {
		response.Domain = domain
		ips, err = resolveProbeVirtualRouterFakeIPExitRealIPs(domain)
	} else if ip := net.ParseIP(strings.TrimSpace(request.TargetIP)).To4(); ip != nil {
		response.TargetIP = ip.String()
		ips = []string{ip.String()}
	} else if ip := net.ParseIP(strings.TrimSpace(request.Target)).To4(); ip != nil {
		response.TargetIP = ip.String()
		ips = []string{ip.String()}
	} else {
		err = errors.New("verify target is empty")
	}
	if err != nil {
		response.Error = err.Error()
		response.LatencyMS = probeDurationMilliseconds(time.Since(started))
		return response
	}
	ips = filterProbeLocalIPv4StringsFromList(ips)
	response.ResolvedIPs = append([]string(nil), ips...)
	if len(ips) == 0 {
		response.Error = "exit resolved no usable ipv4"
		response.LatencyMS = probeDurationMilliseconds(time.Since(started))
		return response
	}
	targets := buildProbeLocalTunnelRouteTargetCandidates(ips, strconv.Itoa(port))
	if len(targets) == 0 {
		response.Error = "exit verify has no usable target address"
		response.LatencyMS = probeDurationMilliseconds(time.Since(started))
		return response
	}
	switch strings.ToLower(strings.TrimSpace(request.Protocol)) {
	case "", "tcp":
		conn, dialErr := dialProbeVirtualRouterExitTCP(targets)
		response.LatencyMS = probeDurationMilliseconds(time.Since(started))
		if dialErr != nil {
			response.Error = dialErr.Error()
			response.CheckedAddress = targets[len(targets)-1]
			return response
		}
		response.CheckedAddress = strings.TrimSpace(conn.RemoteAddr().String())
		_ = conn.Close()
		response.OK = true
		return response
	default:
		response.LatencyMS = probeDurationMilliseconds(time.Since(started))
		response.Error = fmt.Sprintf("unsupported verify protocol: %s", strings.TrimSpace(request.Protocol))
		return response
	}
}

func registerProbeVirtualRouterFakeIPVerifyResponse(requestID string) chan probeVirtualRouterFakeIPVerifyPayload {
	requestID = strings.TrimSpace(requestID)
	ch := make(chan probeVirtualRouterFakeIPVerifyPayload, 1)
	if requestID == "" {
		return ch
	}
	probeVirtualRouterFakeIPVerifyState.mu.Lock()
	if probeVirtualRouterFakeIPVerifyState.pending == nil {
		probeVirtualRouterFakeIPVerifyState.pending = make(map[string]chan probeVirtualRouterFakeIPVerifyPayload)
	}
	probeVirtualRouterFakeIPVerifyState.pending[requestID] = ch
	probeVirtualRouterFakeIPVerifyState.mu.Unlock()
	return ch
}

func unregisterProbeVirtualRouterFakeIPVerifyResponse(requestID string) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return
	}
	probeVirtualRouterFakeIPVerifyState.mu.Lock()
	delete(probeVirtualRouterFakeIPVerifyState.pending, requestID)
	probeVirtualRouterFakeIPVerifyState.mu.Unlock()
}

func waitProbeVirtualRouterFakeIPVerifyResponse(ch chan probeVirtualRouterFakeIPVerifyPayload, timeout time.Duration) (probeVirtualRouterFakeIPVerifyPayload, error) {
	if ch == nil {
		return probeVirtualRouterFakeIPVerifyPayload{}, errors.New("virtual router fake ip verify response waiter is nil")
	}
	if timeout <= 0 {
		timeout = probeVirtualRouterFakeIPVerifyMinTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case response := <-ch:
		return response, nil
	case <-timer.C:
		return probeVirtualRouterFakeIPVerifyPayload{}, errors.New("virtual router fake ip verify response timeout")
	}
}

func completeProbeVirtualRouterFakeIPVerifyResponse(msg probeVirtualRouterFakeIPVerifyPayload) {
	requestID := strings.TrimSpace(msg.RequestID)
	if requestID == "" {
		return
	}
	probeVirtualRouterFakeIPVerifyState.mu.Lock()
	ch := probeVirtualRouterFakeIPVerifyState.pending[requestID]
	probeVirtualRouterFakeIPVerifyState.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- msg:
	default:
	}
}

func probeVirtualRouterFakeIPVerifyPortProtocol(packet []byte) (int, string) {
	if info, ok := probeVirtualRouterParseTCPUDPLogInfo(packet); ok {
		return int(info.DestinationPort), strings.ToLower(strings.TrimSpace(info.Protocol))
	}
	return 0, "tcp"
}

func probeVirtualRouterPathContainsNode(path []string, nodeID string) bool {
	target := normalizeProbeRouteNodeID(nodeID)
	if target == "" {
		return false
	}
	for _, item := range path {
		if normalizeProbeRouteNodeID(item) == target {
			return true
		}
	}
	return false
}
