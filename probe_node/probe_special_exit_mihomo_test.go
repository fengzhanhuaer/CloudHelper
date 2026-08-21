//go:build linux_router

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func signedProbeMihomoTestSnapshot(t *testing.T) probeSpecialExitSnapshot {
	t.Helper()
	snapshot := probeSpecialExitSnapshot{
		Version: 3, NodeID: "19", Revision: 7,
		Rules: []probeSpecialExitRule{
			{RouteRuleID: "rr-proxy", Target: "node-a", Entries: []string{"domain_suffix:example.com", "domain_keyword:api", "domain_prefix:cdn", "cidr:10.0.0.0/8"}},
			{RouteRuleID: "rr-direct", Target: "DIRECT", Entries: []string{"domain_suffix:direct.example"}},
		},
		Proxies: []map[string]interface{}{{"name": "node-a", "type": "socks5", "server": "proxy.example", "port": 1080, "username": "user", "password": "secret", "udp": true}},
	}
	return signProbeMihomoTestSnapshot(t, snapshot)
}

func signProbeMihomoTestSnapshot(t *testing.T, snapshot probeSpecialExitSnapshot) probeSpecialExitSnapshot {
	t.Helper()
	unsigned := snapshot
	unsigned.SHA256 = ""
	raw, err := json.Marshal(unsigned)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	snapshot.SHA256 = hex.EncodeToString(sum[:])
	return snapshot
}

func TestProbeMihomoSnapshotValidationAndTamperDetection(t *testing.T) {
	snapshot := signedProbeMihomoTestSnapshot(t)
	if err := validateProbeMihomoSnapshot(snapshot, "19"); err != nil {
		t.Fatalf("valid snapshot rejected: %v", err)
	}
	tampered := snapshot
	tampered.Revision++
	if err := validateProbeMihomoSnapshot(tampered, "19"); err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("tampered snapshot accepted: %v", err)
	}
	if err := validateProbeMihomoSnapshot(snapshot, "20"); err == nil || !strings.Contains(err.Error(), "node_id") {
		t.Fatalf("wrong node snapshot accepted: %v", err)
	}
}

func TestProbeMihomoSnapshotRejectsVersionTwoAndInvalidRules(t *testing.T) {
	legacy := signedProbeMihomoTestSnapshot(t)
	legacy.Version = 2
	legacy = signProbeMihomoTestSnapshot(t, legacy)
	if err := validateProbeMihomoSnapshot(legacy, "19"); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("version 2 snapshot accepted: %v", err)
	}
	invalidEntry := signedProbeMihomoTestSnapshot(t)
	invalidEntry.Rules = append([]probeSpecialExitRule(nil), invalidEntry.Rules...)
	invalidEntry.Rules[0].Entries = []string{"unsupported:example.com"}
	invalidEntry = signProbeMihomoTestSnapshot(t, invalidEntry)
	if err := validateProbeMihomoSnapshot(invalidEntry, "19"); err == nil || !strings.Contains(err.Error(), "unsupported entry kind") {
		t.Fatalf("unsupported route entry accepted: %v", err)
	}
	missing := signedProbeMihomoTestSnapshot(t)
	missing.Rules[0].Target = "missing"
	missing = signProbeMihomoTestSnapshot(t, missing)
	if err := validateProbeMihomoSnapshot(missing, "19"); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("missing node accepted: %v", err)
	}
	duplicate := signedProbeMihomoTestSnapshot(t)
	duplicate.Rules[1].RouteRuleID = duplicate.Rules[0].RouteRuleID
	duplicate = signProbeMihomoTestSnapshot(t, duplicate)
	if err := validateProbeMihomoSnapshot(duplicate, "19"); err == nil || !strings.Contains(err.Error(), "duplicated") {
		t.Fatalf("duplicate route rule accepted: %v", err)
	}
	injected := signedProbeMihomoTestSnapshot(t)
	injected.Rules[0].Entries = []string{"domain_suffix:example.com,DIRECT"}
	injected = signProbeMihomoTestSnapshot(t, injected)
	if err := validateProbeMihomoSnapshot(injected, "19"); err == nil || !strings.Contains(err.Error(), "delimiter") {
		t.Fatalf("delimiter injection accepted: %v", err)
	}
}

func TestProbeMihomoSnapshotAllowsRouteRuleWithoutEntries(t *testing.T) {
	snapshot := signedProbeMihomoTestSnapshot(t)
	snapshot.Rules[0].Entries = nil
	snapshot = signProbeMihomoTestSnapshot(t, snapshot)
	if err := validateProbeMihomoSnapshot(snapshot, "19"); err != nil {
		t.Fatalf("empty original route rule rejected: %v", err)
	}

	raw, err := compileProbeMihomoConfig(snapshot, probeMihomoRuntimeSecrets{SOCKSUsername: "runtime-user", SOCKSPassword: "runtime-password", APISecret: "api-secret"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "node-a,no-resolve") || !strings.Contains(string(raw), "MATCH,DIRECT") {
		t.Fatalf("empty route rule emitted traffic match or lost fallback:\n%s", raw)
	}
}

func TestCompileProbeMihomoConfigIsLoopbackAuthenticatedAndUsesSelectedNodes(t *testing.T) {
	snapshot := signedProbeMihomoTestSnapshot(t)
	raw, err := compileProbeMihomoConfig(snapshot, probeMihomoRuntimeSecrets{SOCKSUsername: "runtime-user", SOCKSPassword: "runtime-password", APISecret: "api-secret"})
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{
		"127.0.0.1", "runtime-user", "runtime-password", "api-secret",
		"DOMAIN-SUFFIX,example.com,node-a", "DOMAIN-REGEX,api,node-a", "DOMAIN-REGEX,^cdn,node-a",
		"IP-CIDR,10.0.0.0/8,node-a,no-resolve", "DOMAIN-SUFFIX,direct.example,DIRECT", "MATCH,DIRECT",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("compiled config missing %q:\n%s", want, text)
		}
	}
	for _, forbidden := range []string{"tun:", "listen: 0.0.0.0", "allow-lan: true"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("compiled config contains %q:\n%s", forbidden, text)
		}
	}
	var decoded map[string]interface{}
	if err := yaml.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("compiled YAML invalid: %v", err)
	}
}

func TestProbeMihomoRuntimeSecretsPersistWithStableValues(t *testing.T) {
	dir := t.TempDir()
	first, err := loadOrCreateProbeMihomoRuntimeSecrets(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := loadOrCreateProbeMihomoRuntimeSecrets(dir)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || first.SOCKSPassword == "" || first.APISecret == "" {
		t.Fatalf("secrets not stable: first=%+v second=%+v", first, second)
	}
	info, err := os.Stat(filepath.Join(dir, probeMihomoRuntimeFileName))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("secret file mode=%o", info.Mode().Perm())
	}
}

func TestProbeMihomoRepeatedHealthFailuresTriggerRestartThreshold(t *testing.T) {
	activeProbeMihomoRuntime.mu.Lock()
	previousRuntime := activeProbeMihomoRuntime.runtime
	previousReport := activeProbeMihomoRuntime.report
	previousErrors := activeProbeMihomoRuntime.healthErrors
	defer func() {
		activeProbeMihomoRuntime.runtime = previousRuntime
		activeProbeMihomoRuntime.report = previousReport
		activeProbeMihomoRuntime.healthErrors = previousErrors
		activeProbeMihomoRuntime.mu.Unlock()
	}()
	activeProbeMihomoRuntime.runtime = probeMihomoExitRuntimeConfig{DesiredRevision: 1, AppliedRevision: 1, DesiredSHA256: strings.Repeat("a", 64), AppliedSHA256: strings.Repeat("a", 64), Healthy: true}
	activeProbeMihomoRuntime.report = probeSpecialExitRuntimeReport{ExitReady: true, Healthy: true}
	activeProbeMihomoRuntime.healthErrors = 0
	if updateProbeMihomoHealthLocked(errors.New("health-1")) {
		t.Fatal("first health failure triggered restart")
	}
	if activeProbeMihomoRuntime.report.ExitReady || activeProbeMihomoRuntime.report.Healthy {
		t.Fatal("health failure did not fail closed")
	}
	if updateProbeMihomoHealthLocked(errors.New("health-2")) {
		t.Fatal("second health failure triggered restart")
	}
	if !updateProbeMihomoHealthLocked(errors.New("health-3")) {
		t.Fatal("third health failure did not trigger restart")
	}
	if updateProbeMihomoHealthLocked(nil) || activeProbeMihomoRuntime.healthErrors != 0 {
		t.Fatal("healthy check did not reset failure threshold")
	}
}

func TestSelectedProbeMihomoConnectivityTargetsDeduplicatesAndSkipsDirect(t *testing.T) {
	targets := selectedProbeMihomoConnectivityTargets([]probeSpecialExitRule{
		{Target: "DIRECT"}, {Target: "node-a"}, {Target: "node-a"}, {Target: "node-b"}, {Target: " direct "},
	})
	if len(targets) != 2 || targets[0] != "node-a" || targets[1] != "node-b" {
		t.Fatalf("unexpected connectivity targets: %#v", targets)
	}
}

func TestMeasureProbeMihomoConnectivityUsesDelayAPI(t *testing.T) {
	previous := probeMihomoConnectivityAPIRequest
	defer func() { probeMihomoConnectivityAPIRequest = previous }()
	var requestedPath string
	probeMihomoConnectivityAPIRequest = func(method, path string, _ []byte, _ probeMihomoRuntimeSecrets) ([]byte, error) {
		if method != http.MethodGet {
			t.Fatalf("method=%q", method)
		}
		requestedPath = path
		return []byte(`{"delay":137}`), nil
	}
	result := measureProbeMihomoConnectivity("PH / Manila", probeMihomoRuntimeSecrets{APISecret: "secret"})
	if !result.Reachable || result.LatencyMS != 137 || result.Error != "" || result.CheckedAt == "" {
		t.Fatalf("unexpected connectivity result: %+v", result)
	}
	parsed, err := url.Parse(requestedPath)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.EscapedPath() != "/proxies/PH%20%2F%20Manila/delay" || parsed.Query().Get("url") != probeMihomoConnectivityURL || parsed.Query().Get("timeout") != "5000" || parsed.Query().Get("expected") != "204" {
		t.Fatalf("unexpected delay API path: %s", requestedPath)
	}
	probeMihomoConnectivityAPIRequest = func(string, string, []byte, probeMihomoRuntimeSecrets) ([]byte, error) {
		return nil, errors.New(strings.Repeat("x", 300))
	}
	failed := measureProbeMihomoConnectivity("node-a", probeMihomoRuntimeSecrets{})
	if failed.Reachable || len(failed.Error) != 240 || failed.CheckedAt == "" {
		t.Fatalf("unexpected failed connectivity result: %+v", failed)
	}
}

func TestOfficialMihomoValidatesCompiledSpecialExitConfig(t *testing.T) {
	binary := strings.TrimSpace(os.Getenv("PROBE_MIHOMO_POC_BINARY"))
	if binary == "" {
		t.Skip("PROBE_MIHOMO_POC_BINARY is not configured")
	}
	dir := t.TempDir()
	raw, err := compileProbeMihomoConfig(signedProbeMihomoTestSnapshot(t), probeMihomoRuntimeSecrets{SOCKSUsername: "runtime-user", SOCKSPassword: "runtime-password", APISecret: "api-secret"})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateProbeMihomoCandidate(binary, dir, path); err != nil {
		t.Fatal(err)
	}
}

func TestProbeMihomoUpgradeManifestAndPairedRollback(t *testing.T) {
	manifest := probeMihomoUpgradeManifest{SchemaVersion: 1, Version: "v2.3.4", BuildKind: probeBuildKindLinuxRouter, OS: "linux", Arch: "amd64"}
	manifest.CompatibleProgramVersions.Min = "v2.3.4"
	manifest.CompatibleProgramVersions.Max = "v2.3.4"
	manifest.Program.Asset = "cloudhelper-probe-router-linux-amd64"
	manifest.Program.SHA256 = strings.Repeat("ab", 32)
	manifest.Mihomo.Version = "v1.19.29"
	manifest.Mihomo.Asset = "mihomo-linux-amd64-compatible-v1.19.29.gz"
	manifest.Mihomo.URL = "https://github.com/MetaCubeX/mihomo/releases/download/v1.19.29/" + manifest.Mihomo.Asset
	manifest.Mihomo.SHA256 = strings.Repeat("cd", 32)
	if err := validateProbeMihomoUpgradeManifest(manifest, "v2.3.4"); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
	wrong := manifest
	wrong.BuildKind = probeBuildKindNormal
	if err := validateProbeMihomoUpgradeManifest(wrong, "v2.3.4"); err == nil {
		t.Fatal("wrong build manifest accepted")
	}

	dir := t.TempDir()
	target := filepath.Join(dir, "mihomo")
	candidate := filepath.Join(dir, "candidate")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(candidate, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	value, err := replaceProbeProductUpgradeCompanion(probeProductUpgradeCompanion{CandidatePath: candidate, TargetPath: target})
	if err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(target); string(raw) != "new" {
		t.Fatalf("target=%q", raw)
	}
	if err := rollbackProbeProductUpgradeCompanion(value); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(target); string(raw) != "old" {
		t.Fatalf("rollback target=%q", raw)
	}
}
