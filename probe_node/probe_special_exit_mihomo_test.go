//go:build mihomo_exit

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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
		Version: 1, NodeID: "19", Enabled: true, Revision: 7,
		DefaultAction: "direct",
		Rules: []probeSpecialExitRule{
			{ID: "proxy", Name: "proxy", Enabled: true, Action: "node", Target: "node-a", Entries: []string{"domain_suffix:example.com"}, Ports: []string{"443"}, Network: "tcp"},
			{ID: "prefix", Name: "prefix", Enabled: true, Action: "reject", Entries: []string{"domain_prefix:api."}},
			{ID: "keyword", Name: "keyword", Enabled: true, Action: "direct", Entries: []string{"domain_keyword:.media."}},
			{ID: "cidr", Name: "cidr", Enabled: true, Action: "direct", Entries: []string{"cidr:203.0.113.0/24"}},
		},
		Proxies: []map[string]interface{}{{"name": "node-a", "type": "socks5", "server": "proxy.example", "port": 1080, "username": "user", "password": "secret", "udp": true}},
	}
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
	tampered.DefaultAction = "reject"
	if err := validateProbeMihomoSnapshot(tampered, "19"); err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("tampered snapshot accepted: %v", err)
	}
	if err := validateProbeMihomoSnapshot(snapshot, "20"); err == nil || !strings.Contains(err.Error(), "node_id") {
		t.Fatalf("wrong node snapshot accepted: %v", err)
	}
}

func TestProbeMihomoPolicyNamesRejectReservedAndDelimitedValues(t *testing.T) {
	proxies := map[string]struct{}{"node-a": {}}
	for _, target := range []string{"DIRECT", "bad,group", "node-a"} {
		if err := validateProbeMihomoPolicy("group", target, proxies); err == nil {
			t.Fatalf("invalid group target %q accepted", target)
		}
	}
	if err := validateProbeMihomoPolicy("node", "missing", proxies); err == nil {
		t.Fatal("missing proxy node accepted")
	}
}

func TestCompileProbeMihomoConfigIsLoopbackAuthenticatedAndPreservesRuleSemantics(t *testing.T) {
	snapshot := signedProbeMihomoTestSnapshot(t)
	raw, err := compileProbeMihomoConfig(snapshot, probeMihomoRuntimeSecrets{SOCKSUsername: "runtime-user", SOCKSPassword: "runtime-password", APISecret: "api-secret"})
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{
		"127.0.0.1", "runtime-user", "runtime-password", "api-secret",
		"AND,((DOMAIN-SUFFIX,example.com),(DST-PORT,443),(NETWORK,TCP)),node-a",
		`DOMAIN-REGEX,^api\.,REJECT`, `DOMAIN-REGEX,\.media\.,DIRECT`,
		"IP-CIDR,203.0.113.0/24,DIRECT,no-resolve", "MATCH,DIRECT",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("compiled config missing %q:\n%s", want, text)
		}
	}
	for _, forbidden := range []string{"tun:", "0.0.0.0", "allow-lan: true"} {
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
	manifest := probeMihomoUpgradeManifest{SchemaVersion: 1, Version: "v2.3.4", BuildKind: probeBuildKindMihomoExit, OS: "linux", Arch: "amd64"}
	manifest.CompatibleProgramVersions.Min = "v2.3.4"
	manifest.CompatibleProgramVersions.Max = "v2.3.4"
	manifest.Program.Asset = "cloudhelper-probe-exit-node-linux-amd64"
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
