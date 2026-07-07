package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	probeVirtualRouterRouteTestDefaultPort = 443
	probeVirtualRouterRouteTestTimeout     = 12 * time.Second
)

type probeVirtualRouterRouteTestPayload struct {
	RequestID         string                             `json:"request_id"`
	SourceNodeID      string                             `json:"source_node_id,omitempty"`
	ExitNodeID        string                             `json:"exit_node_id,omitempty"`
	Target            string                             `json:"target,omitempty"`
	Domain            string                             `json:"domain,omitempty"`
	TargetIP          string                             `json:"target_ip,omitempty"`
	FakeIP            string                             `json:"fake_ip,omitempty"`
	Port              int                                `json:"port,omitempty"`
	Protocol          string                             `json:"protocol,omitempty"`
	RouteRuleID       string                             `json:"route_rule_id,omitempty"`
	RouteRuleName     string                             `json:"route_rule_name,omitempty"`
	RouteRuleAction   string                             `json:"route_rule_action,omitempty"`
	Path              []string                           `json:"path,omitempty"`
	Result            *probeVirtualRouterRouteTestResult `json:"result,omitempty"`
	Final             bool                               `json:"final,omitempty"`
	CreatedAtUnixNano int64                              `json:"created_at_unix_nano,omitempty"`
}

type probeVirtualRouterRouteTestResult struct {
	NodeID         string   `json:"node_id,omitempty"`
	Stage          string   `json:"stage,omitempty"`
	OK             bool     `json:"ok"`
	Message        string   `json:"message,omitempty"`
	Error          string   `json:"error,omitempty"`
	RuntimeRouteID string   `json:"runtime_route_id,omitempty"`
	PeerNodeID     string   `json:"peer_node_id,omitempty"`
	Path           []string `json:"path,omitempty"`
	ResolvedIPs    []string `json:"resolved_ips,omitempty"`
	CheckedAddress string   `json:"checked_address,omitempty"`
	CurlURL        string   `json:"curl_url,omitempty"`
	HTTPStatus     int      `json:"http_status,omitempty"`
	Output         string   `json:"output,omitempty"`
	LatencyMS      int64    `json:"latency_ms,omitempty"`
	Final          bool     `json:"final,omitempty"`
	UnixNano       int64    `json:"unix_nano,omitempty"`
}

type probeVirtualRouterRouteTestRunResult struct {
	RequestID       string                              `json:"request_id"`
	Target          string                              `json:"target,omitempty"`
	Domain          string                              `json:"domain,omitempty"`
	TargetIP        string                              `json:"target_ip,omitempty"`
	FakeIP          string                              `json:"fake_ip,omitempty"`
	Port            int                                 `json:"port,omitempty"`
	Protocol        string                              `json:"protocol,omitempty"`
	SourceNodeID    string                              `json:"source_node_id,omitempty"`
	ExitNodeID      string                              `json:"exit_node_id,omitempty"`
	RouteRuleID     string                              `json:"route_rule_id,omitempty"`
	RouteRuleName   string                              `json:"route_rule_name,omitempty"`
	RouteRuleAction string                              `json:"route_rule_action,omitempty"`
	Path            []string                            `json:"path,omitempty"`
	OK              bool                                `json:"ok"`
	Error           string                              `json:"error,omitempty"`
	Final           bool                                `json:"final"`
	Results         []probeVirtualRouterRouteTestResult `json:"results,omitempty"`
	StartedAt       string                              `json:"started_at,omitempty"`
	FinishedAt      string                              `json:"finished_at,omitempty"`
}

var probeVirtualRouterRouteTestResponseState = struct {
	mu      sync.Mutex
	pending map[string]chan probeVirtualRouterRouteTestPayload
}{pending: make(map[string]chan probeVirtualRouterRouteTestPayload)}

var probeVirtualRouterRouteTestRunState = struct {
	mu   sync.Mutex
	runs map[string]probeVirtualRouterRouteTestRunResult
}{runs: make(map[string]probeVirtualRouterRouteTestRunResult)}

type probeVirtualRouterRouteTestCurlRunner func(ctx context.Context, curlPath string, args []string) ([]byte, error)

var (
	probeVirtualRouterRouteTestLookPath       = exec.LookPath
	probeVirtualRouterRouteTestRunCurlCommand = runProbeVirtualRouterRouteTestCurlCommand
)

func runProbeVirtualRouterRouteTest(target string, port int, timeout time.Duration) probeVirtualRouterRouteTestRunResult {
	return runProbeVirtualRouterRouteTestWithProgress(target, port, timeout, "", nil, false)
}

func startProbeVirtualRouterRouteTest(target string, port int, timeout time.Duration) probeVirtualRouterRouteTestRunResult {
	return startProbeVirtualRouterRouteTestWithCurl(target, port, timeout, false)
}

func runProbeVirtualRouterRouteTestWithCurl(target string, port int, timeout time.Duration, withCurl bool) probeVirtualRouterRouteTestRunResult {
	return runProbeVirtualRouterRouteTestWithProgress(target, port, timeout, "", nil, withCurl)
}

func startProbeVirtualRouterRouteTestWithCurl(target string, port int, timeout time.Duration, withCurl bool) probeVirtualRouterRouteTestRunResult {
	requestID := newProbeTCPDebugFlowID("vrouter_route_test", target)
	if timeout <= 0 || timeout > 60*time.Second {
		timeout = probeVirtualRouterRouteTestTimeout
	}
	initial := probeVirtualRouterRouteTestRunResult{
		RequestID:    requestID,
		Target:       strings.TrimSpace(target),
		Port:         normalizeProbeVirtualRouterRouteTestPort(port),
		Protocol:     "tcp",
		SourceNodeID: currentProbeVirtualRouterLocalNodeID(),
		StartedAt:    time.Now().UTC().Format(time.RFC3339Nano),
	}
	storeProbeVirtualRouterRouteTestRun(initial)
	go runProbeVirtualRouterRouteTestWithProgress(target, port, timeout, requestID, storeProbeVirtualRouterRouteTestRun, withCurl)
	return initial
}

func runProbeVirtualRouterRouteTestWithProgress(target string, port int, timeout time.Duration, requestID string, onUpdate func(probeVirtualRouterRouteTestRunResult), withCurl bool) probeVirtualRouterRouteTestRunResult {
	started := time.Now()
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		requestID = newProbeTCPDebugFlowID("vrouter_route_test", target)
	}
	localNodeID := currentProbeVirtualRouterLocalNodeID()
	result := probeVirtualRouterRouteTestRunResult{
		RequestID:    requestID,
		Target:       strings.TrimSpace(target),
		Port:         normalizeProbeVirtualRouterRouteTestPort(port),
		Protocol:     "tcp",
		SourceNodeID: localNodeID,
		StartedAt:    started.UTC().Format(time.RFC3339Nano),
	}
	add := func(item probeVirtualRouterRouteTestResult) {
		item.NodeID = normalizeProbeRouteNodeID(item.NodeID)
		if item.NodeID == "" {
			item.NodeID = localNodeID
		}
		if item.UnixNano <= 0 {
			item.UnixNano = time.Now().UnixNano()
		}
		result.Results = append(result.Results, item)
		if onUpdate != nil {
			onUpdate(cloneProbeVirtualRouterRouteTestRunResult(result))
		}
	}
	finish := func(ok bool, errText string) probeVirtualRouterRouteTestRunResult {
		result.OK = ok
		result.Error = strings.TrimSpace(errText)
		result.Final = true
		result.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
		if onUpdate != nil {
			onUpdate(cloneProbeVirtualRouterRouteTestRunResult(result))
		}
		return result
	}
	if onUpdate != nil {
		onUpdate(cloneProbeVirtualRouterRouteTestRunResult(result))
	}
	if localNodeID == "" {
		add(probeVirtualRouterRouteTestResult{Stage: "source", OK: false, Error: "本机虚拟路由节点 ID 为空", Final: true})
		return finish(false, "local virtual router node id is empty")
	}
	plan, err := buildProbeVirtualRouterRouteTestPlan(target, result.Port)
	if err != nil {
		add(probeVirtualRouterRouteTestResult{Stage: "plan", OK: false, Error: err.Error(), Final: true})
		return finish(false, err.Error())
	}
	result.Target = plan.Target
	result.Domain = plan.Domain
	result.TargetIP = plan.TargetIP
	result.FakeIP = plan.FakeIP
	result.Port = plan.Port
	result.Protocol = plan.Protocol
	result.ExitNodeID = plan.ExitNodeID
	result.RouteRuleID = plan.RouteRuleID
	result.RouteRuleName = plan.RouteRuleName
	result.RouteRuleAction = plan.RouteRuleAction
	result.Path = append([]string(nil), plan.Path...)
	add(probeVirtualRouterRouteTestResult{
		Stage:   "source",
		OK:      true,
		Message: "源节点已生成诊断帧",
		Path:    append([]string(nil), plan.Path...),
	})
	if len(plan.Path) == 1 && plan.Path[0] == localNodeID {
		exit := probeVirtualRouterRouteTestExitCheck(probeVirtualRouterRouteTestPayload{
			RequestID:       requestID,
			SourceNodeID:    localNodeID,
			ExitNodeID:      plan.ExitNodeID,
			Target:          plan.Target,
			Domain:          plan.Domain,
			TargetIP:        plan.TargetIP,
			FakeIP:          plan.FakeIP,
			Port:            plan.Port,
			Protocol:        plan.Protocol,
			RouteRuleID:     plan.RouteRuleID,
			RouteRuleName:   plan.RouteRuleName,
			RouteRuleAction: plan.RouteRuleAction,
			Path:            plan.Path,
		}, nil)
		exit.Final = true
		add(exit)
		if withCurl {
			curl := probeVirtualRouterRouteTestCurlCheck(plan, timeout)
			curl.Final = true
			add(curl)
			return finish(exit.OK && curl.OK, firstProbeVirtualRouterRouteTestError(exit.Error, curl.Error))
		}
		return finish(exit.OK, exit.Error)
	}
	waiter := registerProbeVirtualRouterRouteTestResponse(requestID)
	defer unregisterProbeVirtualRouterRouteTestResponse(requestID)
	msg := probeVirtualRouterRouteTestPayload{
		RequestID:         requestID,
		SourceNodeID:      localNodeID,
		ExitNodeID:        plan.ExitNodeID,
		Target:            plan.Target,
		Domain:            plan.Domain,
		TargetIP:          plan.TargetIP,
		FakeIP:            plan.FakeIP,
		Port:              plan.Port,
		Protocol:          plan.Protocol,
		RouteRuleID:       plan.RouteRuleID,
		RouteRuleName:     plan.RouteRuleName,
		RouteRuleAction:   plan.RouteRuleAction,
		Path:              plan.Path,
		CreatedAtUnixNano: started.UnixNano(),
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		add(probeVirtualRouterRouteTestResult{Stage: "source", OK: false, Error: err.Error(), Final: true})
		return finish(false, err.Error())
	}
	if err := forwardProbeVirtualRouterBusinessAlongPath(probeVirtualRouterFrameMainTypeRouteTest, probeVirtualRouterRouteTestSubTypeProbe, payload, msg.Path); err != nil {
		add(probeVirtualRouterRouteTestResult{Stage: "source_forward", OK: false, Error: err.Error(), Path: plan.Path, Final: true})
		return finish(false, err.Error())
	}
	if timeout <= 0 || timeout > 60*time.Second {
		timeout = probeVirtualRouterRouteTestTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case response := <-waiter:
			if response.Result != nil {
				item := *response.Result
				item.Final = response.Final || item.Final
				add(item)
				if response.Final {
					if withCurl {
						curl := probeVirtualRouterRouteTestCurlCheck(plan, timeout)
						curl.Final = true
						add(curl)
						return finish(item.OK && curl.OK, firstProbeVirtualRouterRouteTestError(item.Error, curl.Error))
					}
					return finish(item.OK, item.Error)
				}
			}
		case <-timer.C:
			add(probeVirtualRouterRouteTestResult{Stage: "timeout", OK: false, Error: "等待沿途节点回执超时", Final: true})
			return finish(false, "virtual router route test timeout")
		}
	}
}

func cloneProbeVirtualRouterRouteTestRunResult(in probeVirtualRouterRouteTestRunResult) probeVirtualRouterRouteTestRunResult {
	out := in
	out.Path = append([]string(nil), in.Path...)
	out.Results = append([]probeVirtualRouterRouteTestResult(nil), in.Results...)
	for index := range out.Results {
		out.Results[index].Path = append([]string(nil), in.Results[index].Path...)
		out.Results[index].ResolvedIPs = append([]string(nil), in.Results[index].ResolvedIPs...)
	}
	return out
}

func firstProbeVirtualRouterRouteTestError(items ...string) string {
	for _, item := range items {
		if text := strings.TrimSpace(item); text != "" {
			return text
		}
	}
	return ""
}

func storeProbeVirtualRouterRouteTestRun(result probeVirtualRouterRouteTestRunResult) {
	requestID := strings.TrimSpace(result.RequestID)
	if requestID == "" {
		return
	}
	result = cloneProbeVirtualRouterRouteTestRunResult(result)
	probeVirtualRouterRouteTestRunState.mu.Lock()
	if probeVirtualRouterRouteTestRunState.runs == nil {
		probeVirtualRouterRouteTestRunState.runs = make(map[string]probeVirtualRouterRouteTestRunResult)
	}
	probeVirtualRouterRouteTestRunState.runs[requestID] = result
	pruneProbeVirtualRouterRouteTestRunsLocked(32, 10*time.Minute)
	probeVirtualRouterRouteTestRunState.mu.Unlock()
}

func getProbeVirtualRouterRouteTestRun(requestID string) (probeVirtualRouterRouteTestRunResult, bool) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return probeVirtualRouterRouteTestRunResult{}, false
	}
	probeVirtualRouterRouteTestRunState.mu.Lock()
	result, ok := probeVirtualRouterRouteTestRunState.runs[requestID]
	probeVirtualRouterRouteTestRunState.mu.Unlock()
	if !ok {
		return probeVirtualRouterRouteTestRunResult{}, false
	}
	return cloneProbeVirtualRouterRouteTestRunResult(result), true
}

func pruneProbeVirtualRouterRouteTestRunsLocked(maxRuns int, maxAge time.Duration) {
	if maxRuns <= 0 || len(probeVirtualRouterRouteTestRunState.runs) <= maxRuns {
		return
	}
	type runRef struct {
		id string
		at time.Time
	}
	refs := make([]runRef, 0, len(probeVirtualRouterRouteTestRunState.runs))
	cutoff := time.Now().Add(-maxAge)
	for id, item := range probeVirtualRouterRouteTestRunState.runs {
		at := parseProbeVirtualRouterRouteTestRunTime(item.FinishedAt)
		if at.IsZero() {
			at = parseProbeVirtualRouterRouteTestRunTime(item.StartedAt)
		}
		if !at.IsZero() && at.Before(cutoff) {
			delete(probeVirtualRouterRouteTestRunState.runs, id)
			continue
		}
		refs = append(refs, runRef{id: id, at: at})
	}
	if len(refs) <= maxRuns {
		return
	}
	sort.SliceStable(refs, func(i, j int) bool {
		if refs[i].at.IsZero() {
			return true
		}
		if refs[j].at.IsZero() {
			return false
		}
		return refs[i].at.Before(refs[j].at)
	})
	for len(refs) > maxRuns {
		delete(probeVirtualRouterRouteTestRunState.runs, refs[0].id)
		refs = refs[1:]
	}
}

func parseProbeVirtualRouterRouteTestRunTime(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}
	}
	return t
}

type probeVirtualRouterRouteTestPlan struct {
	Target          string
	Domain          string
	TargetIP        string
	FakeIP          string
	Port            int
	Protocol        string
	ExitNodeID      string
	RouteRuleID     string
	RouteRuleName   string
	RouteRuleAction string
	Path            []string
}

func buildProbeVirtualRouterRouteTestPlan(rawTarget string, port int) (probeVirtualRouterRouteTestPlan, error) {
	target, parsedPort := normalizeProbeVirtualRouterRouteTestTarget(rawTarget)
	if target == "" {
		return probeVirtualRouterRouteTestPlan{}, errors.New("target is required")
	}
	if parsedPort > 0 && port <= 0 {
		port = parsedPort
	}
	port = normalizeProbeVirtualRouterRouteTestPort(port)
	localNodeID := currentProbeVirtualRouterLocalNodeID()
	if localNodeID == "" {
		return probeVirtualRouterRouteTestPlan{}, errors.New("local virtual router node id is empty")
	}
	plan := probeVirtualRouterRouteTestPlan{Target: target, Port: port, Protocol: "tcp"}
	if ip := net.ParseIP(target).To4(); ip != nil {
		plan.TargetIP = ip.String()
		if entry, ok := currentProbeVirtualRouterFakeIPEntryByIPWithControllerRefresh(plan.TargetIP); ok {
			plan.Domain = normalizeProbeVirtualRouterDomain(entry.Domain)
			plan.FakeIP = strings.TrimSpace(entry.FakeIP)
			plan.ExitNodeID = normalizeProbeRouteNodeID(entry.ExitNodeID)
			plan.RouteRuleAction = sanitizeProbeVirtualRouterRouteRuleAction(entry.Action, entry.ExitNodeID)
		} else if nodeID := currentProbeVirtualRouterNodeIDForIP(plan.TargetIP); nodeID != "" {
			plan.ExitNodeID = nodeID
			plan.RouteRuleAction = "virtual_ip"
		} else {
			return probeVirtualRouterRouteTestPlan{}, fmt.Errorf("ip %s 没有 Fake IP 映射，也不是已知虚拟节点 IP", plan.TargetIP)
		}
	} else {
		domain := normalizeProbeVirtualRouterDomain(target)
		if domain == "" {
			return probeVirtualRouterRouteTestPlan{}, errors.New("target domain is invalid")
		}
		plan.Domain = domain
		rule, ok := currentProbeVirtualRouterRouteRuleForDomain(domain)
		if !ok {
			return probeVirtualRouterRouteTestPlan{}, fmt.Errorf("domain %s 未命中虚拟路由规则", domain)
		}
		plan.RouteRuleID = strings.TrimSpace(rule.ID)
		plan.RouteRuleName = strings.TrimSpace(rule.Name)
		plan.RouteRuleAction = sanitizeProbeVirtualRouterRouteRuleAction(rule.Action, rule.ExitNodeID)
		plan.ExitNodeID = normalizeProbeRouteNodeID(rule.ExitNodeID)
		if entry, ok := currentProbeVirtualRouterFakeIPEntryByDomain(domain); ok {
			plan.FakeIP = strings.TrimSpace(entry.FakeIP)
		}
	}
	switch plan.RouteRuleAction {
	case "probe_exit", "virtual_ip":
	case "reject":
		return probeVirtualRouterRouteTestPlan{}, fmt.Errorf("target %s 命中 reject 规则", target)
	case "direct":
		return probeVirtualRouterRouteTestPlan{}, fmt.Errorf("target %s 命中 direct 规则，不会进入虚拟路由出口", target)
	default:
		return probeVirtualRouterRouteTestPlan{}, fmt.Errorf("target %s 没有可用探针出口", target)
	}
	if plan.ExitNodeID == "" {
		return probeVirtualRouterRouteTestPlan{}, errors.New("exit node is empty")
	}
	if plan.ExitNodeID == localNodeID {
		plan.Path = []string{localNodeID}
		return plan, nil
	}
	plan.Path = currentProbeVirtualRouterPathBetweenNodes(localNodeID, plan.ExitNodeID)
	if len(plan.Path) < 2 {
		return probeVirtualRouterRouteTestPlan{}, fmt.Errorf("本节点 %s 到出口节点 %s 没有可用虚拟路径", localNodeID, plan.ExitNodeID)
	}
	return plan, nil
}

func normalizeProbeVirtualRouterRouteTestTarget(raw string) (string, int) {
	target := strings.TrimSpace(raw)
	if target == "" {
		return "", 0
	}
	if strings.Contains(target, "://") {
		if parsed, err := url.Parse(target); err == nil {
			target = strings.TrimSpace(parsed.Host)
		}
	}
	if host, portText, err := net.SplitHostPort(target); err == nil {
		port, _ := strconv.Atoi(strings.TrimSpace(portText))
		return strings.Trim(strings.TrimSpace(host), "[]"), port
	}
	target = strings.Trim(strings.TrimSpace(target), "[]")
	if strings.Contains(target, "/") {
		target = strings.SplitN(target, "/", 2)[0]
	}
	return target, 0
}

func normalizeProbeVirtualRouterRouteTestPort(port int) int {
	if port <= 0 || port > 65535 {
		return probeVirtualRouterRouteTestDefaultPort
	}
	return port
}

func probeVirtualRouterRouteTestCurlCheck(plan probeVirtualRouterRouteTestPlan, timeout time.Duration) probeVirtualRouterRouteTestResult {
	started := time.Now()
	result := probeVirtualRouterRouteTestResult{
		NodeID:   currentProbeVirtualRouterLocalNodeID(),
		Stage:    "curl",
		OK:       false,
		UnixNano: time.Now().UnixNano(),
		Path:     append([]string(nil), plan.Path...),
	}
	curlURL, err := buildProbeVirtualRouterRouteTestCurlURL(plan)
	result.CurlURL = curlURL
	if err != nil {
		result.Error = err.Error()
		result.LatencyMS = probeDurationMilliseconds(time.Since(started))
		return result
	}
	curlPath, err := probeVirtualRouterRouteTestLookPath("curl")
	if err != nil {
		result.Error = "curl 不可用: " + err.Error()
		result.LatencyMS = probeDurationMilliseconds(time.Since(started))
		return result
	}
	if timeout <= 0 || timeout > 60*time.Second {
		timeout = probeVirtualRouterRouteTestTimeout
	}
	timeoutSeconds := maxProbeVirtualRouterRouteTestSeconds(1, int(timeout/time.Second))
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	nullTarget := "/dev/null"
	if runtime.GOOS == "windows" {
		nullTarget = "NUL"
	}
	args := []string{
		"--silent",
		"--show-error",
		"--noproxy", "*",
		"--connect-timeout", strconv.Itoa(timeoutSeconds),
		"--max-time", strconv.Itoa(timeoutSeconds),
		"--output", nullTarget,
		"--write-out", "\nhttp_code=%{http_code}\nremote_ip=%{remote_ip}\nremote_port=%{remote_port}\ntime_namelookup=%{time_namelookup}\ntime_connect=%{time_connect}\ntime_appconnect=%{time_appconnect}\ntime_starttransfer=%{time_starttransfer}\ntime_total=%{time_total}\n",
		curlURL,
	}
	out, err := probeVirtualRouterRouteTestRunCurlCommand(ctx, curlPath, args)
	result.LatencyMS = probeDurationMilliseconds(time.Since(started))
	parsed := parseProbeVirtualRouterRouteTestCurlOutput(string(out))
	if status := strings.TrimSpace(parsed["http_code"]); status != "" {
		if code, parseErr := strconv.Atoi(status); parseErr == nil {
			result.HTTPStatus = code
		}
	}
	if remoteIP := strings.TrimSpace(parsed["remote_ip"]); remoteIP != "" {
		remotePort := strings.TrimSpace(parsed["remote_port"])
		if remotePort != "" && remotePort != "0" {
			result.CheckedAddress = net.JoinHostPort(remoteIP, remotePort)
		} else {
			result.CheckedAddress = remoteIP
		}
	}
	result.Output = compactProbeVirtualRouterRouteTestCurlOutput(parsed, string(out))
	if err != nil {
		result.Error = trimProbeVirtualRouterRouteTestOutput(err.Error()+" "+string(out), 800)
		return result
	}
	if result.HTTPStatus <= 0 {
		result.Error = "curl 未返回 HTTP 状态"
		return result
	}
	result.OK = true
	result.Message = fmt.Sprintf("curl HTTP %d", result.HTTPStatus)
	return result
}

func runProbeVirtualRouterRouteTestCurlCommand(ctx context.Context, curlPath string, args []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, curlPath, args...)
	hideWindowSysProcAttr(cmd)
	return cmd.CombinedOutput()
}

func maxProbeVirtualRouterRouteTestSeconds(minValue int, value int) int {
	if value < minValue {
		return minValue
	}
	return value
}

func buildProbeVirtualRouterRouteTestCurlURL(plan probeVirtualRouterRouteTestPlan) (string, error) {
	host := strings.TrimSpace(plan.Domain)
	if host == "" {
		host = strings.TrimSpace(plan.TargetIP)
	}
	if host == "" {
		host = strings.TrimSpace(plan.Target)
	}
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if host == "" {
		return "", errors.New("curl target is empty")
	}
	port := normalizeProbeVirtualRouterRouteTestPort(plan.Port)
	scheme := "https"
	if port == 80 {
		scheme = "http"
	}
	hostPort := host
	if (scheme == "https" && port != 443) || (scheme == "http" && port != 80) {
		hostPort = net.JoinHostPort(host, strconv.Itoa(port))
	}
	return (&url.URL{Scheme: scheme, Host: hostPort, Path: "/"}).String(), nil
}

func parseProbeVirtualRouterRouteTestCurlOutput(raw string) map[string]string {
	out := make(map[string]string)
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, "=") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		out[key] = strings.TrimSpace(value)
	}
	return out
}

func compactProbeVirtualRouterRouteTestCurlOutput(values map[string]string, raw string) string {
	parts := make([]string, 0, 8)
	for _, key := range []string{"time_namelookup", "time_connect", "time_appconnect", "time_starttransfer", "time_total"} {
		if value := strings.TrimSpace(values[key]); value != "" {
			parts = append(parts, key+"="+value)
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, " ")
	}
	return trimProbeVirtualRouterRouteTestOutput(raw, 800)
}

func trimProbeVirtualRouterRouteTestOutput(raw string, limit int) string {
	text := strings.TrimSpace(raw)
	if limit <= 0 || len(text) <= limit {
		return text
	}
	return text[:limit] + "..."
}

func handleProbeVirtualRouterRouteTestFrame(runtime *probeVirtualRouterRuntime, subType uint16, msg probeVirtualRouterRouteTestPayload) error {
	localNodeID := currentProbeVirtualRouterLocalNodeIDForRuntime(runtime)
	msg.Path = cleanProbeVirtualRouterPath(msg.Path)
	if localNodeID == "" || msg.RequestID == "" || normalizeProbeRouteNodeID(msg.SourceNodeID) == "" || len(msg.Path) == 0 {
		return errors.New("virtual router route test frame is incomplete")
	}
	switch subType {
	case probeVirtualRouterRouteTestSubTypeProbe:
		hop := probeVirtualRouterRouteTestHopResult(runtime, msg, "hop", true, "诊断帧已经过本节点", "")
		_ = sendProbeVirtualRouterRouteTestReport(msg, hop, false)
		if localNodeID == normalizeProbeRouteNodeID(msg.ExitNodeID) || localNodeID == msg.Path[len(msg.Path)-1] {
			exit := probeVirtualRouterRouteTestExitCheck(msg, runtime)
			exit.Final = true
			return sendProbeVirtualRouterRouteTestReport(msg, exit, true)
		}
		payloadMsg := msg
		payloadMsg.Result = nil
		payloadMsg.Final = false
		payload, err := json.Marshal(payloadMsg)
		if err != nil {
			return err
		}
		if err := forwardProbeVirtualRouterBusinessAlongPath(probeVirtualRouterFrameMainTypeRouteTest, probeVirtualRouterRouteTestSubTypeProbe, payload, msg.Path); err != nil {
			fail := probeVirtualRouterRouteTestHopResult(runtime, msg, "forward", false, "", err.Error())
			fail.Final = true
			_ = sendProbeVirtualRouterRouteTestReport(msg, fail, true)
			return err
		}
		return nil
	case probeVirtualRouterRouteTestSubTypeReport:
		if normalizeProbeRouteNodeID(msg.SourceNodeID) == localNodeID {
			completeProbeVirtualRouterRouteTestResponse(msg)
			return nil
		}
		payload, err := json.Marshal(msg)
		if err != nil {
			return err
		}
		return forwardProbeVirtualRouterBusinessAlongPath(probeVirtualRouterFrameMainTypeRouteTest, probeVirtualRouterRouteTestSubTypeReport, payload, msg.Path)
	default:
		return fmt.Errorf("unsupported virtual router route test subtype=%d", subType)
	}
}

func probeVirtualRouterRouteTestHopResult(runtime *probeVirtualRouterRuntime, msg probeVirtualRouterRouteTestPayload, stage string, ok bool, message string, errText string) probeVirtualRouterRouteTestResult {
	localNodeID := currentProbeVirtualRouterLocalNodeIDForRuntime(runtime)
	result := probeVirtualRouterRouteTestResult{
		NodeID:     localNodeID,
		Stage:      strings.TrimSpace(stage),
		OK:         ok,
		Message:    strings.TrimSpace(message),
		Error:      strings.TrimSpace(errText),
		Path:       append([]string(nil), msg.Path...),
		UnixNano:   time.Now().UnixNano(),
		PeerNodeID: "",
	}
	if runtime != nil {
		result.RuntimeRouteID = strings.TrimSpace(runtime.cfg.routeID)
		result.PeerNodeID = strings.TrimSpace(runtime.cfg.peerNodeID)
	}
	return result
}

func probeVirtualRouterRouteTestExitCheck(msg probeVirtualRouterRouteTestPayload, runtime *probeVirtualRouterRuntime) probeVirtualRouterRouteTestResult {
	started := time.Now()
	result := probeVirtualRouterRouteTestHopResult(runtime, msg, "exit", true, "出口节点已收到诊断帧", "")
	if strings.TrimSpace(msg.RouteRuleAction) == "virtual_ip" {
		result.Message = "目的虚拟节点已收到诊断帧"
		result.LatencyMS = probeDurationMilliseconds(time.Since(started))
		return result
	}
	port := normalizeProbeVirtualRouterRouteTestPort(msg.Port)
	var ips []string
	var err error
	if domain := normalizeProbeVirtualRouterDomain(msg.Domain); domain != "" {
		ips, err = resolveProbeVirtualRouterFakeIPExitRealIPs(domain)
	} else if ip := net.ParseIP(strings.TrimSpace(msg.TargetIP)).To4(); ip != nil {
		ips = []string{ip.String()}
	} else if ip := net.ParseIP(strings.TrimSpace(msg.Target)).To4(); ip != nil {
		ips = []string{ip.String()}
	} else {
		err = errors.New("出口测试目标为空")
	}
	if err != nil {
		result.OK = false
		result.Error = err.Error()
		result.LatencyMS = probeDurationMilliseconds(time.Since(started))
		return result
	}
	ips = filterProbeLocalIPv4StringsFromList(ips)
	result.ResolvedIPs = append([]string(nil), ips...)
	if len(ips) == 0 {
		result.OK = false
		result.Error = "出口解析没有可用 IPv4"
		result.LatencyMS = probeDurationMilliseconds(time.Since(started))
		return result
	}
	targets := buildProbeLocalTunnelRouteTargetCandidates(ips, strconv.Itoa(port))
	if len(targets) == 0 {
		result.OK = false
		result.Error = "出口测试没有可用目标地址"
		result.LatencyMS = probeDurationMilliseconds(time.Since(started))
		return result
	}
	conn, err := dialProbeVirtualRouterExitTCP(targets)
	result.LatencyMS = probeDurationMilliseconds(time.Since(started))
	if err != nil {
		result.OK = false
		result.Error = err.Error()
		result.CheckedAddress = targets[len(targets)-1]
		return result
	}
	result.CheckedAddress = strings.TrimSpace(conn.RemoteAddr().String())
	_ = conn.Close()
	result.Message = "出口 TCP 可达"
	return result
}

func sendProbeVirtualRouterRouteTestReport(msg probeVirtualRouterRouteTestPayload, result probeVirtualRouterRouteTestResult, final bool) error {
	localNodeID := currentProbeVirtualRouterLocalNodeID()
	report := msg
	result.Final = final
	report.Result = &result
	report.Final = final
	report.Path = probeVirtualRouterRouteTestReturnPath(msg.Path, localNodeID)
	if normalizeProbeRouteNodeID(report.SourceNodeID) == localNodeID {
		completeProbeVirtualRouterRouteTestResponse(report)
		return nil
	}
	payload, err := json.Marshal(report)
	if err != nil {
		return err
	}
	return forwardProbeVirtualRouterBusinessAlongPath(probeVirtualRouterFrameMainTypeRouteTest, probeVirtualRouterRouteTestSubTypeReport, payload, report.Path)
}

func probeVirtualRouterRouteTestReturnPath(path []string, localNodeID string) []string {
	cleanPath := cleanProbeVirtualRouterPath(path)
	local := normalizeProbeRouteNodeID(localNodeID)
	if local == "" || len(cleanPath) == 0 {
		return probeVirtualRouterReversePath(cleanPath)
	}
	for index, nodeID := range cleanPath {
		if nodeID != local {
			continue
		}
		return probeVirtualRouterReversePath(cleanPath[:index+1])
	}
	return probeVirtualRouterReversePath(cleanPath)
}

func registerProbeVirtualRouterRouteTestResponse(requestID string) chan probeVirtualRouterRouteTestPayload {
	requestID = strings.TrimSpace(requestID)
	ch := make(chan probeVirtualRouterRouteTestPayload, 32)
	if requestID == "" {
		return ch
	}
	probeVirtualRouterRouteTestResponseState.mu.Lock()
	if probeVirtualRouterRouteTestResponseState.pending == nil {
		probeVirtualRouterRouteTestResponseState.pending = make(map[string]chan probeVirtualRouterRouteTestPayload)
	}
	probeVirtualRouterRouteTestResponseState.pending[requestID] = ch
	probeVirtualRouterRouteTestResponseState.mu.Unlock()
	return ch
}

func unregisterProbeVirtualRouterRouteTestResponse(requestID string) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return
	}
	probeVirtualRouterRouteTestResponseState.mu.Lock()
	delete(probeVirtualRouterRouteTestResponseState.pending, requestID)
	probeVirtualRouterRouteTestResponseState.mu.Unlock()
}

func completeProbeVirtualRouterRouteTestResponse(msg probeVirtualRouterRouteTestPayload) {
	requestID := strings.TrimSpace(msg.RequestID)
	if requestID == "" {
		return
	}
	probeVirtualRouterRouteTestResponseState.mu.Lock()
	ch := probeVirtualRouterRouteTestResponseState.pending[requestID]
	probeVirtualRouterRouteTestResponseState.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- msg:
	default:
	}
}
