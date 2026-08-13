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
)

type probeMihomoRuntimeSecrets struct {
	SOCKSUsername string `json:"socks_username"`
	SOCKSPassword string `json:"socks_password"`
	APISecret     string `json:"api_secret"`
}

type probeMihomoProcessState struct {
	mu           sync.RWMutex
	applyMu      sync.Mutex
	process      *exec.Cmd
	processDone  chan error
	dataDir      string
	secrets      probeMihomoRuntimeSecrets
	runtime      probeMihomoExitRuntimeConfig
	report       probeSpecialExitRuntimeReport
	nodeID       string
	shuttingDown bool
	healthErrors int
}

var activeProbeMihomoRuntime probeMihomoProcessState

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
	if !snapshot.Enabled {
		stopProbeMihomoProcessLocked()
		if err := persistProbeMihomoSnapshot(dataDir, snapshot); err != nil {
			setProbeMihomoApplyError(snapshot, err)
			return err
		}
		activeProbeMihomoRuntime.mu.Lock()
		activeProbeMihomoRuntime.runtime = probeMihomoExitRuntimeConfig{DesiredRevision: snapshot.Revision, AppliedRevision: snapshot.Revision, DesiredSHA256: strings.ToLower(snapshot.SHA256), AppliedSHA256: strings.ToLower(snapshot.SHA256)}
		activeProbeMihomoRuntime.report = probeSpecialExitRuntimeReport{AppliedRevision: snapshot.Revision, AppliedSHA256: strings.ToLower(snapshot.SHA256), ExitReady: false, Healthy: false, UpdatedAt: time.Now().UTC().Format(time.RFC3339)}
		activeProbeMihomoRuntime.mu.Unlock()
		return nil
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
	activeProbeMihomoRuntime.report = probeSpecialExitRuntimeReport{AppliedRevision: snapshot.Revision, AppliedSHA256: strings.ToLower(snapshot.SHA256), ExitReady: true, Healthy: true, MihomoVersion: version, UpdatedAt: time.Now().UTC().Format(time.RFC3339)}
	activeProbeMihomoRuntime.mu.Unlock()
	return nil
}

func validateProbeMihomoSnapshot(snapshot probeSpecialExitSnapshot, nodeID string) error {
	if snapshot.Version != 1 {
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
	if err := validateProbeMihomoPolicy(snapshot.DefaultAction, snapshot.DefaultTarget, proxyNames); err != nil {
		return fmt.Errorf("default action: %w", err)
	}
	for index, rule := range snapshot.Rules {
		if !rule.Enabled {
			continue
		}
		if len(rule.Entries) == 0 {
			return fmt.Errorf("rules[%d] has no entries", index)
		}
		if err := validateProbeMihomoPolicy(rule.Action, rule.Target, proxyNames); err != nil {
			return fmt.Errorf("rules[%d]: %w", index, err)
		}
	}
	return nil
}

func validateProbeMihomoPolicy(action, target string, proxyNames map[string]struct{}) error {
	action = strings.ToLower(strings.TrimSpace(action))
	target = strings.TrimSpace(target)
	switch action {
	case "", "direct", "reject":
		return nil
	case "proxy", "group":
		if target == "" {
			return errors.New("policy target is required")
		}
		if len(proxyNames) == 0 {
			return errors.New("policy requires at least one proxy")
		}
		if strings.ContainsAny(target, ",\r\n") || probeMihomoReservedPolicyName(target) {
			return fmt.Errorf("policy target %q is invalid or reserved", target)
		}
		if _, exists := proxyNames[target]; exists {
			return fmt.Errorf("policy group %q conflicts with a proxy name", target)
		}
		return nil
	case "node":
		if _, ok := proxyNames[target]; !ok {
			return fmt.Errorf("proxy node %q does not exist", target)
		}
		return nil
	default:
		return fmt.Errorf("unsupported policy action %q", action)
	}
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
	proxyNames := make([]string, 0, len(snapshot.Proxies))
	for _, proxy := range snapshot.Proxies {
		proxyNames = append(proxyNames, strings.TrimSpace(fmt.Sprint(proxy["name"])))
	}
	groups := []map[string]interface{}{}
	groupSeen := map[string]struct{}{}
	ensureGroup := func(name string) error {
		name = strings.TrimSpace(name)
		if name == "" {
			return errors.New("proxy group name is empty")
		}
		if _, exists := groupSeen[name]; exists {
			return nil
		}
		if len(proxyNames) == 0 {
			return fmt.Errorf("proxy group %q has no proxies", name)
		}
		groupSeen[name] = struct{}{}
		groups = append(groups, map[string]interface{}{"name": name, "type": "select", "proxies": append([]string(nil), proxyNames...)})
		return nil
	}
	policies := []struct{ action, target string }{{snapshot.DefaultAction, snapshot.DefaultTarget}}
	for _, rule := range snapshot.Rules {
		if rule.Enabled {
			policies = append(policies, struct{ action, target string }{rule.Action, rule.Target})
		}
	}
	for _, item := range policies {
		action := strings.ToLower(strings.TrimSpace(item.action))
		if action == "proxy" || action == "group" {
			if err := ensureGroup(item.target); err != nil {
				return nil, err
			}
		}
	}
	rules := make([]string, 0)
	for _, rule := range snapshot.Rules {
		if !rule.Enabled {
			continue
		}
		policy := probeMihomoPolicyName(rule.Action, rule.Target)
		for _, entry := range rule.Entries {
			line, err := compileProbeMihomoRule(entry, rule.Ports, rule.Network, policy)
			if err != nil {
				return nil, fmt.Errorf("rule %q: %w", rule.ID, err)
			}
			rules = append(rules, line)
		}
	}
	rules = append(rules, "MATCH,"+probeMihomoPolicyName(snapshot.DefaultAction, snapshot.DefaultTarget))
	config := map[string]interface{}{
		"mode": "rule", "log-level": "warning", "ipv6": false, "allow-lan": false,
		"external-controller": net.JoinHostPort("127.0.0.1", strconv.Itoa(probeMihomoAPIPort)), "secret": secrets.APISecret,
		"listeners": []map[string]interface{}{{"name": "cloudhelper-exit", "type": "socks", "port": probeMihomoSOCKSPort, "listen": "127.0.0.1", "udp": true, "users": []map[string]string{{"username": secrets.SOCKSUsername, "password": secrets.SOCKSPassword}}}},
		"dns":       map[string]interface{}{"enable": true, "ipv6": false, "enhanced-mode": "redir-host", "default-nameserver": []string{"1.1.1.1", "8.8.8.8"}, "nameserver": []string{"https://1.1.1.1/dns-query", "https://8.8.8.8/dns-query"}, "proxy-server-nameserver": []string{"1.1.1.1", "8.8.8.8"}, "direct-nameserver": []string{"system"}},
		"proxies":   snapshot.Proxies, "proxy-groups": groups, "rules": rules,
	}
	return yaml.Marshal(config)
}

func probeMihomoPolicyName(action, target string) string {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "reject":
		return "REJECT"
	case "proxy", "group", "node":
		return strings.TrimSpace(target)
	default:
		return "DIRECT"
	}
}

func compileProbeMihomoRule(entry string, ports []string, network, policy string) (string, error) {
	kind, value, ok := strings.Cut(strings.TrimSpace(entry), ":")
	if !ok || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("invalid entry %q", entry)
	}
	value = strings.TrimSpace(value)
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
			activeProbeMihomoRuntime.report.LastApplyError = fmt.Sprintf("mihomo process exited: %v", waitErr)
			activeProbeMihomoRuntime.report.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
			dataDir := activeProbeMihomoRuntime.dataDir
			nodeID := activeProbeMihomoRuntime.nodeID
			shuttingDown := activeProbeMihomoRuntime.shuttingDown
			activeProbeMihomoRuntime.mu.Unlock()
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
		if err == nil && !snapshot.Enabled {
			return
		}
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
	activeProbeMihomoRuntime.report = probeSpecialExitRuntimeReport{AppliedRevision: previous.AppliedRevision, AppliedSHA256: previous.AppliedSHA256, ExitReady: false, Healthy: false, MihomoVersion: previous.MihomoVersion, LastApplyError: applyErr.Error(), UpdatedAt: time.Now().UTC().Format(time.RFC3339)}
	activeProbeMihomoRuntime.mu.Unlock()
}
func probeProductSpecialExitReport() probeSpecialExitRuntimeReport {
	activeProbeMihomoRuntime.mu.RLock()
	defer activeProbeMihomoRuntime.mu.RUnlock()
	return activeProbeMihomoRuntime.report
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
