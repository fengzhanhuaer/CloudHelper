//go:build mihomo_exit

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	probeMihomoRuntimeFileName   = "mihomo_runtime.json"
	probeMihomoSnapshotFileName  = "special_exit_snapshot.json"
	probeMihomoConfigFileName    = "mihomo.yaml"
	probeMihomoCandidateFileName = "mihomo.candidate.yaml"
	probeMihomoBinaryFileName    = "mihomo"
	probeMihomoSOCKSPort         = 17890
	probeMihomoAPIPort           = 17891
	probeMihomoMaxConfigBytes    = 16 << 20
	probeMihomoDelayTimeoutMS    = 5000
	probeMihomoConnectivityEvery = 60 * time.Second
	probeMihomoConnectivityURL   = "https://www.gstatic.com/generate_204"
)

type probeMihomoRuntimeSecrets struct {
	SOCKSUsername string `json:"socks_username"`
	SOCKSPassword string `json:"socks_password"`
	APISecret     string `json:"api_secret"`
}

type probeMihomoProcessState struct {
	mu                  sync.RWMutex
	applyMu             sync.Mutex
	process             *exec.Cmd
	processDone         chan error
	dataDir             string
	secrets             probeMihomoRuntimeSecrets
	runtime             probeMihomoExitRuntimeConfig
	report              probeSpecialExitRuntimeReport
	nodeID              string
	shuttingDown        bool
	healthErrors        int
	connectivityTargets []string
}

var activeProbeMihomoRuntime probeMihomoProcessState
var probeMihomoConnectivityAPIRequest = probeMihomoAPIRequest

func startProbeProductRuntime(nodeID string) error {
	dataDir, err := resolveDataDir()
	if err != nil {
		return err
	}
	secrets, err := loadOrCreateProbeMihomoRuntimeSecrets(dataDir)
	if err != nil {
		return err
	}
	activeProbeMihomoRuntime.mu.Lock()
	activeProbeMihomoRuntime.dataDir = dataDir
	activeProbeMihomoRuntime.secrets = secrets
	activeProbeMihomoRuntime.nodeID = normalizeProbeRouteNodeID(nodeID)
	activeProbeMihomoRuntime.shuttingDown = false
	activeProbeMihomoRuntime.mu.Unlock()
	snapshot, err := loadProbeMihomoSnapshot(dataDir)
	if err != nil {
		if os.IsNotExist(err) {
			return errors.New("special exit snapshot has not been received")
		}
		return err
	}
	return applyProbeMihomoSnapshot(snapshot, nodeID)
}

func stopProbeProductRuntime() {
	activeProbeMihomoRuntime.applyMu.Lock()
	defer activeProbeMihomoRuntime.applyMu.Unlock()
	activeProbeMihomoRuntime.mu.Lock()
	activeProbeMihomoRuntime.shuttingDown = true
	activeProbeMihomoRuntime.mu.Unlock()
	stopProbeMihomoProcessLocked()
}

func applyProbeProductRouteConfig(snapshot *probeSpecialExitSnapshot, nodeID string) error {
	if snapshot == nil {
		applyErr := errors.New("controller did not provide the special exit snapshot")
		activeProbeMihomoRuntime.applyMu.Lock()
		defer activeProbeMihomoRuntime.applyMu.Unlock()
		stopProbeMihomoProcessLocked()
		setProbeMihomoApplyError(probeSpecialExitSnapshot{}, applyErr)
		return applyErr
	}
	return applyProbeMihomoSnapshot(*snapshot, nodeID)
}

func applyProbeMihomoSnapshot(snapshot probeSpecialExitSnapshot, nodeID string) error {
	activeProbeMihomoRuntime.applyMu.Lock()
	defer activeProbeMihomoRuntime.applyMu.Unlock()
	if err := validateProbeMihomoSnapshot(snapshot, nodeID); err != nil {
		stopProbeMihomoProcessLocked()
		setProbeMihomoApplyError(snapshot, err)
		return err
	}
	activeProbeMihomoRuntime.mu.RLock()
	dataDir := activeProbeMihomoRuntime.dataDir
	secrets := activeProbeMihomoRuntime.secrets
	activeProbeMihomoRuntime.mu.RUnlock()
	if dataDir == "" {
		var err error
		dataDir, err = resolveDataDir()
		if err != nil {
			setProbeMihomoApplyError(snapshot, err)
			return err
		}
		secrets, err = loadOrCreateProbeMihomoRuntimeSecrets(dataDir)
		if err != nil {
			setProbeMihomoApplyError(snapshot, err)
			return err
		}
		activeProbeMihomoRuntime.mu.Lock()
		activeProbeMihomoRuntime.dataDir, activeProbeMihomoRuntime.secrets = dataDir, secrets
		activeProbeMihomoRuntime.mu.Unlock()
	}
	config, err := compileProbeMihomoConfig(snapshot, secrets)
	if err != nil {
		stopProbeMihomoProcessLocked()
		setProbeMihomoApplyError(snapshot, err)
		return err
	}
	if len(config) > probeMihomoMaxConfigBytes {
		err = errors.New("compiled mihomo config is too large")
		stopProbeMihomoProcessLocked()
		setProbeMihomoApplyError(snapshot, err)
		return err
	}
	candidatePath := filepath.Join(dataDir, probeMihomoCandidateFileName)
	if err = writeProbeMihomoAtomic(candidatePath, config, 0o600); err != nil {
		stopProbeMihomoProcessLocked()
		setProbeMihomoApplyError(snapshot, err)
		return err
	}
	binaryPath := resolveProbeMihomoBinaryPath(dataDir)
	if err = validateProbeMihomoCandidate(binaryPath, dataDir, candidatePath); err != nil {
		stopProbeMihomoProcessLocked()
		setProbeMihomoApplyError(snapshot, err)
		return err
	}
	configPath := filepath.Join(dataDir, probeMihomoConfigFileName)
	previousConfig, previousConfigErr := os.ReadFile(configPath)
	previousConfigExists := previousConfigErr == nil
	if previousConfigErr != nil && !os.IsNotExist(previousConfigErr) {
		setProbeMihomoApplyError(snapshot, previousConfigErr)
		return previousConfigErr
	}
	if err = ensureProbeMihomoProcessLocked(binaryPath, dataDir, candidatePath, secrets); err != nil {
		_ = rollbackProbeMihomoActiveConfig(binaryPath, dataDir, configPath, previousConfig, previousConfigExists, secrets)
		setProbeMihomoApplyError(snapshot, err)
		return err
	}
	if err = writeProbeMihomoAtomic(configPath, config, 0o600); err != nil {
		_ = rollbackProbeMihomoActiveConfig(binaryPath, dataDir, configPath, previousConfig, previousConfigExists, secrets)
		setProbeMihomoApplyError(snapshot, err)
		return err
	}
	if err = persistProbeMihomoSnapshot(dataDir, snapshot); err != nil {
		_ = rollbackProbeMihomoActiveConfig(binaryPath, dataDir, configPath, previousConfig, previousConfigExists, secrets)
		setProbeMihomoApplyError(snapshot, err)
		return err
	}
	version := queryProbeMihomoVersion(secrets)
	runtimeConfig := probeMihomoExitRuntimeConfig{
		SOCKSAddress:  net.JoinHostPort("127.0.0.1", strconv.Itoa(probeMihomoSOCKSPort)),
		SOCKSUsername: secrets.SOCKSUsername, SOCKSPassword: secrets.SOCKSPassword,
		DesiredRevision: snapshot.Revision, AppliedRevision: snapshot.Revision,
		DesiredSHA256: strings.ToLower(snapshot.SHA256), AppliedSHA256: strings.ToLower(snapshot.SHA256), Healthy: true,
	}
	activeProbeMihomoRuntime.mu.Lock()
	activeProbeMihomoRuntime.runtime = runtimeConfig
	targets := selectedProbeMihomoConnectivityTargets(snapshot.Rules)
	activeProbeMihomoRuntime.connectivityTargets = append([]string(nil), targets...)
	activeProbeMihomoRuntime.report = probeSpecialExitRuntimeReport{AppliedRevision: snapshot.Revision, AppliedSHA256: strings.ToLower(snapshot.SHA256), ExitReady: true, Healthy: true, MihomoVersion: version, Connectivity: pendingProbeMihomoConnectivity(targets), UpdatedAt: time.Now().UTC().Format(time.RFC3339)}
	activeProbeMihomoRuntime.mu.Unlock()
	go refreshProbeMihomoConnectivity(snapshot.Revision, strings.ToLower(snapshot.SHA256), targets, secrets)
	return nil
}

func validateProbeMihomoSnapshot(snapshot probeSpecialExitSnapshot, nodeID string) error {
	if snapshot.Version != 3 {
		return fmt.Errorf("unsupported special exit snapshot version %d", snapshot.Version)
	}
	if normalizeProbeRouteNodeID(snapshot.NodeID) != normalizeProbeRouteNodeID(nodeID) {
		return errors.New("special exit snapshot node_id mismatch")
	}
	if snapshot.Revision <= 0 || !validProbeMihomoExitSHA256(snapshot.SHA256) {
		return errors.New("special exit snapshot revision or sha256 is invalid")
	}
	encoded := snapshot
	encoded.SHA256 = ""
	raw, err := json.Marshal(encoded)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != strings.ToLower(strings.TrimSpace(snapshot.SHA256)) {
		return errors.New("special exit snapshot sha256 mismatch")
	}
	proxyNames := make(map[string]struct{}, len(snapshot.Proxies))
	for index, proxy := range snapshot.Proxies {
		name := strings.TrimSpace(fmt.Sprint(proxy["name"]))
		if name == "" {
			return fmt.Errorf("proxies[%d] name is required", index)
		}
		if len(name) > 256 || strings.ContainsAny(name, ",\r\n") || probeMihomoReservedPolicyName(name) {
			return fmt.Errorf("proxies[%d] name %q is invalid or reserved", index, name)
		}
		if _, exists := proxyNames[name]; exists {
			return fmt.Errorf("duplicate proxy name %q", name)
		}
		proxyNames[name] = struct{}{}
	}
	routeRuleIDs := make(map[string]struct{}, len(snapshot.Rules))
	for index, rule := range snapshot.Rules {
		if strings.TrimSpace(rule.RouteRuleID) == "" {
			return fmt.Errorf("rules[%d].route_rule_id is required", index)
		}
		if _, exists := routeRuleIDs[strings.TrimSpace(rule.RouteRuleID)]; exists {
			return fmt.Errorf("rules[%d].route_rule_id is duplicated", index)
		}
		routeRuleIDs[strings.TrimSpace(rule.RouteRuleID)] = struct{}{}
		target := normalizeProbeMihomoRuleTarget(rule.Target)
		for _, entry := range rule.Entries {
			if _, err := compileProbeMihomoRule(entry, nil, "", target); err != nil {
				return fmt.Errorf("rules[%d] entry %q is invalid: %w", index, entry, err)
			}
		}
		if target != "DIRECT" {
			if _, ok := proxyNames[target]; !ok {
				return fmt.Errorf("rules[%d]: proxy node %q does not exist", index, target)
			}
		}
	}
	return nil
}

func normalizeProbeMihomoRuleTarget(value string) string {
	if strings.EqualFold(strings.TrimSpace(value), "DIRECT") {
		return "DIRECT"
	}
	return strings.TrimSpace(value)
}

func probeMihomoReservedPolicyName(value string) bool {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "DIRECT", "REJECT", "REJECT-DROP", "PASS", "COMPATIBLE", "GLOBAL", "MATCH":
		return true
	default:
		return false
	}
}

func compileProbeMihomoConfig(snapshot probeSpecialExitSnapshot, secrets probeMihomoRuntimeSecrets) ([]byte, error) {
	rules := make([]string, 0)
	for _, rule := range snapshot.Rules {
		for _, entry := range rule.Entries {
			line, err := compileProbeMihomoRule(entry, nil, "", normalizeProbeMihomoRuleTarget(rule.Target))
			if err != nil {
				return nil, fmt.Errorf("route rule %q: %w", rule.RouteRuleID, err)
			}
			rules = append(rules, line)
		}
	}
	rules = append(rules, "MATCH,DIRECT")
	config := map[string]interface{}{
		"mode": "rule", "log-level": "warning", "ipv6": false, "allow-lan": false,
		"external-controller": net.JoinHostPort("127.0.0.1", strconv.Itoa(probeMihomoAPIPort)), "secret": secrets.APISecret,
		"listeners": []map[string]interface{}{{"name": "cloudhelper-exit", "type": "socks", "port": probeMihomoSOCKSPort, "listen": "127.0.0.1", "udp": true, "users": []map[string]string{{"username": secrets.SOCKSUsername, "password": secrets.SOCKSPassword}}}},
		"dns":       map[string]interface{}{"enable": true, "ipv6": false, "enhanced-mode": "redir-host", "default-nameserver": []string{"1.1.1.1", "8.8.8.8"}, "nameserver": []string{"https://1.1.1.1/dns-query", "https://8.8.8.8/dns-query"}, "proxy-server-nameserver": []string{"1.1.1.1", "8.8.8.8"}, "direct-nameserver": []string{"system"}},
		"proxies":   snapshot.Proxies, "proxy-groups": []map[string]interface{}{}, "rules": rules,
	}
	return yaml.Marshal(config)
}

func compileProbeMihomoRule(entry string, ports []string, network, policy string) (string, error) {
	kind, value, ok := strings.Cut(strings.TrimSpace(entry), ":")
	if !ok || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("invalid entry %q", entry)
	}
	value = strings.TrimSpace(value)
	if strings.ContainsAny(value, ",\r\n") {
		return "", fmt.Errorf("entry %q contains an unsupported delimiter", entry)
	}
	var base string
	switch kind {
	case "domain_suffix":
		base = "DOMAIN-SUFFIX," + value
	case "domain_keyword":
		base = "DOMAIN-REGEX," + regexp.QuoteMeta(value)
	case "domain_prefix":
		base = "DOMAIN-REGEX,^" + regexp.QuoteMeta(value)
	case "cidr":
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			return "", err
		}
		if prefix.Addr().Is6() {
			base = "IP-CIDR6," + prefix.Masked().String()
		} else {
			base = "IP-CIDR," + prefix.Masked().String()
		}
	default:
		return "", fmt.Errorf("unsupported entry kind %q", kind)
	}
	conditions := []string{base}
	if len(ports) > 0 {
		conditions = append(conditions, "DST-PORT,"+strings.Join(ports, "/"))
	}
	if network = strings.ToUpper(strings.TrimSpace(network)); network != "" {
		conditions = append(conditions, "NETWORK,"+network)
	}
	if len(conditions) == 1 {
		if kind == "cidr" {
			return base + "," + policy + ",no-resolve", nil
		}
		return base + "," + policy, nil
	}
	parts := make([]string, len(conditions))
	for index := range conditions {
		parts[index] = "(" + conditions[index] + ")"
	}
	return "AND,(" + strings.Join(parts, ",") + ")," + policy, nil
}

func loadOrCreateProbeMihomoRuntimeSecrets(dataDir string) (probeMihomoRuntimeSecrets, error) {
	path := filepath.Join(dataDir, probeMihomoRuntimeFileName)
	raw, err := os.ReadFile(path)
	if err == nil {
		var value probeMihomoRuntimeSecrets
		if json.Unmarshal(raw, &value) == nil && value.SOCKSUsername != "" && value.SOCKSPassword != "" && value.APISecret != "" {
			return value, nil
		}
	} else if !os.IsNotExist(err) {
		return probeMihomoRuntimeSecrets{}, err
	}
	value := probeMihomoRuntimeSecrets{SOCKSUsername: "cloudhelper", SOCKSPassword: randomProbeMihomoSecret(32), APISecret: randomProbeMihomoSecret(32)}
	raw, _ = json.MarshalIndent(value, "", "  ")
	raw = append(raw, '\n')
	if err := writeProbeMihomoAtomic(path, raw, 0o600); err != nil {
		return probeMihomoRuntimeSecrets{}, err
	}
	return value, nil
}

func randomProbeMihomoSecret(size int) string {
	raw := make([]byte, size)
	if _, err := rand.Read(raw); err != nil {
		panic(err)
	}
	return hex.EncodeToString(raw)
}
func resolveProbeMihomoBinaryPath(dataDir string) string {
	if value := strings.TrimSpace(os.Getenv("PROBE_MIHOMO_BINARY")); value != "" {
		return value
	}
	return filepath.Join(dataDir, probeMihomoBinaryFileName)
}
func validateProbeMihomoCandidate(binaryPath, dataDir, configPath string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, binaryPath, "-t", "-d", dataDir, "-f", configPath).CombinedOutput()
	if err != nil {
		return fmt.Errorf("mihomo config validation failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func ensureProbeMihomoProcessLocked(binaryPath, dataDir, configPath string, secrets probeMihomoRuntimeSecrets) error {
	activeProbeMihomoRuntime.mu.RLock()
	running := activeProbeMihomoRuntime.process != nil
	activeProbeMihomoRuntime.mu.RUnlock()
	if running {
		if err := reloadProbeMihomoConfig(configPath, secrets); err == nil {
			return waitProbeMihomoHealthy(secrets, 8*time.Second)
		}
		return errors.New("mihomo rejected the candidate config reload")
	}
	logDir, err := resolveProbeProductWorkingPath(activeProbeProductProfile.RuntimeLogDir)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(logDir, 0o755); err != nil {
		return err
	}
	logPath := filepath.Join(logDir, "mihomo.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	logWriter := newProbeRotatingLogFileWriter(logPath, logFile, probeLogMaxBytes)
	cmd := exec.Command(binaryPath, "-d", dataDir, "-f", configPath)
	cmd.Stdout = logWriter
	cmd.Stderr = logWriter
	if err = cmd.Start(); err != nil {
		_ = logWriter.Close()
		return err
	}
	done := make(chan error, 1)
	go func() {
		waitErr := cmd.Wait()
		_ = logWriter.Close()
		activeProbeMihomoRuntime.mu.Lock()
		if activeProbeMihomoRuntime.process == cmd {
			activeProbeMihomoRuntime.process = nil
			activeProbeMihomoRuntime.processDone = nil
			activeProbeMihomoRuntime.runtime.Healthy = false
			activeProbeMihomoRuntime.report.ExitReady = false
			activeProbeMihomoRuntime.report.Healthy = false
			checkedAt := time.Now().UTC().Format(time.RFC3339)
			activeProbeMihomoRuntime.report.LastApplyError = fmt.Sprintf("mihomo process exited: %v", waitErr)
			for index := range activeProbeMihomoRuntime.report.Connectivity {
				activeProbeMihomoRuntime.report.Connectivity[index].Reachable = false
				activeProbeMihomoRuntime.report.Connectivity[index].LatencyMS = 0
				activeProbeMihomoRuntime.report.Connectivity[index].Error = "mihomo process is unavailable"
				activeProbeMihomoRuntime.report.Connectivity[index].CheckedAt = checkedAt
			}
			activeProbeMihomoRuntime.report.UpdatedAt = checkedAt
			dataDir := activeProbeMihomoRuntime.dataDir
			nodeID := activeProbeMihomoRuntime.nodeID
			shuttingDown := activeProbeMihomoRuntime.shuttingDown
			activeProbeMihomoRuntime.mu.Unlock()
			_, _ = triggerProbeImmediateReport()
			if !shuttingDown {
				go restartProbeMihomoAfterUnexpectedExit(dataDir, nodeID)
			}
			done <- waitErr
			return
		}
		activeProbeMihomoRuntime.mu.Unlock()
		done <- waitErr
	}()
	activeProbeMihomoRuntime.mu.Lock()
	activeProbeMihomoRuntime.process = cmd
	activeProbeMihomoRuntime.processDone = done
	activeProbeMihomoRuntime.healthErrors = 0
	activeProbeMihomoRuntime.mu.Unlock()
	if err = waitProbeMihomoHealthy(secrets, 10*time.Second); err != nil {
		stopProbeMihomoProcessLocked()
		return err
	}
	go monitorProbeMihomoHealth(cmd, secrets)
	go monitorProbeMihomoConnectivity(cmd)
	return nil
}

func rollbackProbeMihomoActiveConfig(binaryPath, dataDir, configPath string, previous []byte, existed bool, secrets probeMihomoRuntimeSecrets) error {
	if !existed {
		stopProbeMihomoProcessLocked()
		_ = os.Remove(configPath)
		return nil
	}
	if err := writeProbeMihomoAtomic(configPath, previous, 0o600); err != nil {
		stopProbeMihomoProcessLocked()
		return err
	}
	if err := reloadProbeMihomoConfig(configPath, secrets); err == nil {
		if err = waitProbeMihomoHealthy(secrets, 8*time.Second); err == nil {
			return nil
		}
	}
	stopProbeMihomoProcessLocked()
	return ensureProbeMihomoProcessLocked(binaryPath, dataDir, configPath, secrets)
}

func restartProbeMihomoAfterUnexpectedExit(dataDir, nodeID string) {
	delay := 5 * time.Second
	for {
		time.Sleep(delay)
		activeProbeMihomoRuntime.mu.RLock()
		stop := activeProbeMihomoRuntime.shuttingDown || activeProbeMihomoRuntime.process != nil
		activeProbeMihomoRuntime.mu.RUnlock()
		if stop {
			return
		}
		snapshot, err := loadProbeMihomoSnapshot(dataDir)
		if err == nil {
			err = applyProbeMihomoSnapshot(snapshot, nodeID)
		}
		if err == nil {
			return
		}
		logProbeWarnf("mihomo supervisor restart failed: %v", err)
		if delay < 60*time.Second {
			delay *= 2
			if delay > 60*time.Second {
				delay = 60 * time.Second
			}
		}
	}
}

func monitorProbeMihomoHealth(cmd *exec.Cmd, secrets probeMihomoRuntimeSecrets) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		activeProbeMihomoRuntime.mu.RLock()
		current := activeProbeMihomoRuntime.process == cmd
		activeProbeMihomoRuntime.mu.RUnlock()
		if !current {
			return
		}
		_, healthErr := probeMihomoAPIRequest(http.MethodGet, "/version", nil, secrets)
		activeProbeMihomoRuntime.mu.Lock()
		if activeProbeMihomoRuntime.process != cmd {
			activeProbeMihomoRuntime.mu.Unlock()
			return
		}
		restart := updateProbeMihomoHealthLocked(healthErr)
		dataDir := activeProbeMihomoRuntime.dataDir
		nodeID := activeProbeMihomoRuntime.nodeID
		shuttingDown := activeProbeMihomoRuntime.shuttingDown
		activeProbeMihomoRuntime.mu.Unlock()
		if restart {
			logProbeWarnf("mihomo health check failed repeatedly; restarting managed process")
			stopProbeMihomoProcessLocked()
			if !shuttingDown {
				go restartProbeMihomoAfterUnexpectedExit(dataDir, nodeID)
			}
			return
		}
	}
}

func monitorProbeMihomoConnectivity(cmd *exec.Cmd) {
	ticker := time.NewTicker(probeMihomoConnectivityEvery)
	defer ticker.Stop()
	for range ticker.C {
		activeProbeMihomoRuntime.mu.RLock()
		if activeProbeMihomoRuntime.process != cmd {
			activeProbeMihomoRuntime.mu.RUnlock()
			return
		}
		revision := activeProbeMihomoRuntime.report.AppliedRevision
		sha256Value := activeProbeMihomoRuntime.report.AppliedSHA256
		targets := append([]string(nil), activeProbeMihomoRuntime.connectivityTargets...)
		secrets := activeProbeMihomoRuntime.secrets
		activeProbeMihomoRuntime.mu.RUnlock()
		refreshProbeMihomoConnectivity(revision, sha256Value, targets, secrets)
	}
}

func selectedProbeMihomoConnectivityTargets(rules []probeSpecialExitRule) []string {
	seen := make(map[string]struct{}, len(rules))
	targets := make([]string, 0, len(rules))
	for _, rule := range rules {
		target := strings.TrimSpace(rule.Target)
		if target == "" || strings.EqualFold(target, "DIRECT") {
			continue
		}
		if _, ok := seen[target]; ok {
			continue
		}
		seen[target] = struct{}{}
		targets = append(targets, target)
	}
	return targets
}

func pendingProbeMihomoConnectivity(targets []string) []probeSpecialExitConnectivityReport {
	items := make([]probeSpecialExitConnectivityReport, 0, len(targets))
	for _, target := range targets {
		items = append(items, probeSpecialExitConnectivityReport{Target: target})
	}
	return items
}

func refreshProbeMihomoConnectivity(revision int64, sha256Value string, targets []string, secrets probeMihomoRuntimeSecrets) {
	if revision <= 0 || len(targets) == 0 {
		return
	}
	results := make([]probeSpecialExitConnectivityReport, len(targets))
	semaphore := make(chan struct{}, 8)
	var group sync.WaitGroup
	for index, target := range targets {
		index, target := index, target
		group.Add(1)
		go func() {
			defer group.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()
			results[index] = measureProbeMihomoConnectivity(target, secrets)
		}()
	}
	group.Wait()
	activeProbeMihomoRuntime.mu.Lock()
	if activeProbeMihomoRuntime.report.AppliedRevision != revision || !strings.EqualFold(activeProbeMihomoRuntime.report.AppliedSHA256, sha256Value) {
		activeProbeMihomoRuntime.mu.Unlock()
		return
	}
	activeProbeMihomoRuntime.report.Connectivity = results
	activeProbeMihomoRuntime.report.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	activeProbeMihomoRuntime.mu.Unlock()
	_, _ = triggerProbeImmediateReport()
}

func measureProbeMihomoConnectivity(target string, secrets probeMihomoRuntimeSecrets) probeSpecialExitConnectivityReport {
	checkedAt := time.Now().UTC().Format(time.RFC3339)
	query := url.Values{}
	query.Set("url", probeMihomoConnectivityURL)
	query.Set("timeout", strconv.Itoa(probeMihomoDelayTimeoutMS))
	query.Set("expected", "204")
	path := "/proxies/" + url.PathEscape(target) + "/delay?" + query.Encode()
	raw, err := probeMihomoConnectivityAPIRequest(http.MethodGet, path, nil, secrets)
	if err != nil {
		return probeSpecialExitConnectivityReport{Target: target, Error: sanitizeProbeMihomoConnectivityError(err), CheckedAt: checkedAt}
	}
	var response struct {
		Delay int64 `json:"delay"`
	}
	if err := json.Unmarshal(raw, &response); err != nil || response.Delay <= 0 {
		return probeSpecialExitConnectivityReport{Target: target, Error: "mihomo delay response is invalid", CheckedAt: checkedAt}
	}
	return probeSpecialExitConnectivityReport{Target: target, Reachable: true, LatencyMS: response.Delay, CheckedAt: checkedAt}
}

func sanitizeProbeMihomoConnectivityError(err error) string {
	value := strings.TrimSpace(err.Error())
	if len(value) > 240 {
		value = value[:240]
	}
	return value
}

func updateProbeMihomoHealthLocked(healthErr error) bool {
	healthy := healthErr == nil
	if healthy {
		activeProbeMihomoRuntime.healthErrors = 0
	} else {
		activeProbeMihomoRuntime.healthErrors++
	}
	activeProbeMihomoRuntime.runtime.Healthy = healthy
	activeProbeMihomoRuntime.report.Healthy = healthy
	activeProbeMihomoRuntime.report.ExitReady = healthy && activeProbeMihomoRuntime.runtime.AppliedRevision > 0 && activeProbeMihomoRuntime.runtime.AppliedRevision == activeProbeMihomoRuntime.runtime.DesiredRevision && activeProbeMihomoRuntime.runtime.AppliedSHA256 == activeProbeMihomoRuntime.runtime.DesiredSHA256
	if healthErr != nil {
		activeProbeMihomoRuntime.report.LastApplyError = "mihomo health check failed: " + healthErr.Error()
	} else if activeProbeMihomoRuntime.report.ExitReady {
		activeProbeMihomoRuntime.report.LastApplyError = ""
	}
	activeProbeMihomoRuntime.report.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return !healthy && activeProbeMihomoRuntime.healthErrors >= 3
}

func probeMihomoSessionOpened() {
	activeProbeMihomoRuntime.mu.Lock()
	activeProbeMihomoRuntime.report.ActiveSessions++
	activeProbeMihomoRuntime.mu.Unlock()
}

func probeMihomoSessionClosed() {
	activeProbeMihomoRuntime.mu.Lock()
	if activeProbeMihomoRuntime.report.ActiveSessions > 0 {
		activeProbeMihomoRuntime.report.ActiveSessions--
	}
	activeProbeMihomoRuntime.mu.Unlock()
}

func probeMihomoBytesTransferred(up bool, count int) {
	if count <= 0 {
		return
	}
	activeProbeMihomoRuntime.mu.Lock()
	if up {
		activeProbeMihomoRuntime.report.BytesUp += int64(count)
	} else {
		activeProbeMihomoRuntime.report.BytesDown += int64(count)
	}
	activeProbeMihomoRuntime.mu.Unlock()
}

func stopProbeMihomoProcessLocked() {
	activeProbeMihomoRuntime.mu.Lock()
	cmd := activeProbeMihomoRuntime.process
	done := activeProbeMihomoRuntime.processDone
	activeProbeMihomoRuntime.process = nil
	activeProbeMihomoRuntime.processDone = nil
	activeProbeMihomoRuntime.healthErrors = 0
	activeProbeMihomoRuntime.runtime.Healthy = false
	activeProbeMihomoRuntime.report.ExitReady = false
	activeProbeMihomoRuntime.report.Healthy = false
	activeProbeMihomoRuntime.connectivityTargets = nil
	activeProbeMihomoRuntime.report.Connectivity = nil
	activeProbeMihomoRuntime.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	if done != nil {
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
	}
}

func probeMihomoAPIRequest(method, path string, body []byte, secrets probeMihomoRuntimeSecrets) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, "http://"+net.JoinHostPort("127.0.0.1", strconv.Itoa(probeMihomoAPIPort))+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+secrets.APISecret)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("mihomo API status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return raw, nil
}
func reloadProbeMihomoConfig(path string, secrets probeMihomoRuntimeSecrets) error {
	body, _ := json.Marshal(map[string]interface{}{"path": path, "payload": ""})
	_, err := probeMihomoAPIRequest(http.MethodPut, "/configs?force=true", body, secrets)
	return err
}
func waitProbeMihomoHealthy(secrets probeMihomoRuntimeSecrets, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var last error
	for time.Now().Before(deadline) {
		_, last = probeMihomoAPIRequest(http.MethodGet, "/version", nil, secrets)
		if last == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("mihomo health timeout: %w", last)
}
func queryProbeMihomoVersion(secrets probeMihomoRuntimeSecrets) string {
	raw, err := probeMihomoAPIRequest(http.MethodGet, "/version", nil, secrets)
	if err != nil {
		return ""
	}
	var value map[string]interface{}
	_ = json.Unmarshal(raw, &value)
	return strings.TrimSpace(fmt.Sprint(value["version"]))
}

func setProbeMihomoApplyError(snapshot probeSpecialExitSnapshot, applyErr error) {
	activeProbeMihomoRuntime.mu.Lock()
	activeProbeMihomoRuntime.runtime = probeMihomoExitRuntimeConfig{DesiredRevision: snapshot.Revision, DesiredSHA256: strings.ToLower(snapshot.SHA256), Healthy: false}
	previous := activeProbeMihomoRuntime.report
	activeProbeMihomoRuntime.connectivityTargets = nil
	activeProbeMihomoRuntime.report = probeSpecialExitRuntimeReport{AppliedRevision: previous.AppliedRevision, AppliedSHA256: previous.AppliedSHA256, ExitReady: false, Healthy: false, MihomoVersion: previous.MihomoVersion, LastApplyError: applyErr.Error(), UpdatedAt: time.Now().UTC().Format(time.RFC3339)}
	activeProbeMihomoRuntime.mu.Unlock()
}
func probeProductSpecialExitReport() probeSpecialExitRuntimeReport {
	activeProbeMihomoRuntime.mu.RLock()
	defer activeProbeMihomoRuntime.mu.RUnlock()
	report := activeProbeMihomoRuntime.report
	report.Connectivity = append([]probeSpecialExitConnectivityReport(nil), report.Connectivity...)
	return report
}
func currentProbeMihomoRuntimeConfig() (probeMihomoExitRuntimeConfig, bool) {
	activeProbeMihomoRuntime.mu.RLock()
	defer activeProbeMihomoRuntime.mu.RUnlock()
	return activeProbeMihomoRuntime.runtime, activeProbeMihomoRuntime.runtime.AppliedRevision > 0
}
func writeProbeMihomoAtomic(path string, raw []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, mode); err != nil {
		return err
	}
	_ = os.Chmod(tmp, mode)
	return os.Rename(tmp, path)
}
func persistProbeMihomoSnapshot(dataDir string, snapshot probeSpecialExitSnapshot) error {
	raw, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	return writeProbeMihomoAtomic(filepath.Join(dataDir, probeMihomoSnapshotFileName), append(raw, '\n'), 0o600)
}
func loadProbeMihomoSnapshot(dataDir string) (probeSpecialExitSnapshot, error) {
	var value probeSpecialExitSnapshot
	raw, err := os.ReadFile(filepath.Join(dataDir, probeMihomoSnapshotFileName))
	if err != nil {
		return value, err
	}
	err = json.Unmarshal(raw, &value)
	return value, err
}
