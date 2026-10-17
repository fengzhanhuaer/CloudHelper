package core

import (
	"encoding/json"
	"net"
	"testing"
	"time"
)

func TestAsusUpgradeCapabilityAndTypedDispatch(t *testing.T) {
	const nodeID = "9821"
	probeRuntimeStore.mu.Lock()
	previous, existed := probeRuntimeStore.data[nodeID]
	probeRuntimeStore.data[nodeID] = probeRuntimeStatus{BuildKind: probeNodeKindAsus56UV2}
	probeRuntimeStore.mu.Unlock()
	t.Cleanup(func() {
		probeRuntimeStore.mu.Lock()
		if existed {
			probeRuntimeStore.data[nodeID] = previous
		} else {
			delete(probeRuntimeStore.data, nodeID)
		}
		probeRuntimeStore.mu.Unlock()
	})
	node := probeNodeRecord{NodeNo: 9821, NodeKind: probeNodeKindAsus56UV2, TargetSystem: "linux"}
	if _, err := dispatchUpgradeToProbe(node, "https://controller.test"); err == nil {
		t.Fatal("old plugin accepted upgrade")
	}
	updateProbeRuntimeRouterPluginUpgrade(nodeID, true)
	local, remote := net.Pipe()
	defer local.Close()
	defer remote.Close()
	_ = remote.SetDeadline(time.Now().Add(3 * time.Second))
	session := &probeSession{nodeID: nodeID, stream: local, enc: json.NewEncoder(local)}
	probeSessions.mu.Lock()
	oldSession := probeSessions.data[nodeID]
	probeSessions.data[nodeID] = session
	probeSessions.mu.Unlock()
	t.Cleanup(func() {
		probeSessions.mu.Lock()
		if oldSession != nil {
			probeSessions.data[nodeID] = oldSession
		} else {
			delete(probeSessions.data, nodeID)
		}
		probeSessions.mu.Unlock()
	})
	received := make(chan probeUpgradeCommand, 1)
	go func() { var cmd probeUpgradeCommand; _ = json.NewDecoder(remote).Decode(&cmd); received <- cmd }()
	result, err := dispatchUpgradeToProbe(node, "https://controller.test")
	if err != nil || !result.OK {
		t.Fatal("dispatch failed", err)
	}
	cmd := <-received
	if cmd.Type != "upgrade" || cmd.BuildKind != "asus_jffs" || cmd.Mode != "proxy" || cmd.ReleaseRepo != releaseRepo() {
		t.Fatalf("wrong plugin upgrade command: %+v", cmd)
	}
	updateProbeRuntimeRouterPluginUpgrade(nodeID, false)
	if _, err = dispatchUpgradeToProbe(node, "https://controller.test"); err == nil {
		t.Fatal("stale capability survived old plugin report")
	}
}

func TestGitHubProxyAssetPreservesDigest(t *testing.T) {
	const digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	var asset githubReleaseAsset
	if err := json.Unmarshal([]byte(`{"name":"cloudhelper-asusbe86u-jffs.tar.gz","digest":"`+digest+`"}`), &asset); err != nil || asset.Digest != digest {
		t.Fatal("release SHA256 lost", err)
	}
	b, err := json.Marshal(proxyAsset{Digest: asset.Digest})
	if err != nil {
		t.Fatal(err)
	}
	var proxy map[string]interface{}
	if err = json.Unmarshal(b, &proxy); err != nil || proxy["digest"] != digest {
		t.Fatal("proxy SHA256 lost", err)
	}
}
