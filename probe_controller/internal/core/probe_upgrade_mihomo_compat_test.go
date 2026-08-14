package core

import "testing"

func TestProbeUpgradeModeUsesProxyToRescueLegacyMihomoExit(t *testing.T) {
	probeRuntimeStore.mu.Lock()
	oldRuntimeData := probeRuntimeStore.data
	probeRuntimeStore.data = map[string]probeRuntimeStatus{
		"19": {NodeID: "19", Version: "v0.3.315", BuildKind: probeNodeKindMihomoExit},
		"20": {NodeID: "20", Version: "v0.3.317", BuildKind: probeNodeKindMihomoExit},
	}
	probeRuntimeStore.mu.Unlock()
	t.Cleanup(func() {
		probeRuntimeStore.mu.Lock()
		probeRuntimeStore.data = oldRuntimeData
		probeRuntimeStore.mu.Unlock()
	})

	legacy := probeNodeRecord{NodeNo: 19, NodeKind: probeNodeKindMihomoExit, DirectConnect: true}
	if mode, compat := probeUpgradeModeForNode(legacy, "19"); mode != "proxy" || !compat {
		t.Fatalf("legacy mode=%q compat=%t", mode, compat)
	}
	fixed := probeNodeRecord{NodeNo: 20, NodeKind: probeNodeKindMihomoExit, DirectConnect: true}
	if mode, compat := probeUpgradeModeForNode(fixed, "20"); mode != "direct" || compat {
		t.Fatalf("fixed mode=%q compat=%t", mode, compat)
	}
	normal := probeNodeRecord{NodeNo: 21, NodeKind: probeNodeKindNormal, DirectConnect: true}
	if mode, compat := probeUpgradeModeForNode(normal, "21"); mode != "direct" || compat {
		t.Fatalf("normal mode=%q compat=%t", mode, compat)
	}
}

func TestProbeUpgradeAssetNameAliasesOnlyLegacyMihomoExit(t *testing.T) {
	oldProbeStore := ProbeStore
	ProbeStore = &probeConfigStore{data: probeConfigData{ProbeNodes: []probeNodeRecord{
		{NodeNo: 19, NodeKind: probeNodeKindMihomoExit},
		{NodeNo: 20, NodeKind: probeNodeKindMihomoExit},
		{NodeNo: 21, NodeKind: probeNodeKindNormal},
	}}}
	probeRuntimeStore.mu.Lock()
	oldRuntimeData := probeRuntimeStore.data
	probeRuntimeStore.data = map[string]probeRuntimeStatus{
		"19": {NodeID: "19", Version: "0.3.315"},
		"20": {NodeID: "20", Version: "0.3.317"},
	}
	probeRuntimeStore.mu.Unlock()
	t.Cleanup(func() {
		ProbeStore = oldProbeStore
		probeRuntimeStore.mu.Lock()
		probeRuntimeStore.data = oldRuntimeData
		probeRuntimeStore.mu.Unlock()
	})

	if got := probeUpgradeAssetNameForNode("19", probeMihomoExitUpgradeAssetName); got != probeMihomoExitLegacyUpgradeAssetAlias {
		t.Fatalf("legacy asset=%q", got)
	}
	if got := probeUpgradeAssetNameForNode("20", probeMihomoExitUpgradeAssetName); got != probeMihomoExitUpgradeAssetName {
		t.Fatalf("fixed asset=%q", got)
	}
	if got := probeUpgradeAssetNameForNode("21", probeMihomoExitUpgradeAssetName); got != probeMihomoExitUpgradeAssetName {
		t.Fatalf("normal asset=%q", got)
	}
}

func TestProbeVersionAtLeast(t *testing.T) {
	for _, tc := range []struct {
		version string
		want    bool
	}{
		{version: "v0.3.316", want: false},
		{version: "0.3.317", want: true},
		{version: "v0.3.318-dev", want: true},
		{version: "dev", want: false},
	} {
		if got := probeVersionAtLeast(tc.version, probeMihomoExitUpgradeExtractorFixedVersion); got != tc.want {
			t.Fatalf("version=%q got=%t want=%t", tc.version, got, tc.want)
		}
	}
}
