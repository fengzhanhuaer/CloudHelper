package core

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestProbeProxyGroupBackupHandlerStoresAndRestoresGlobally(t *testing.T) {
	oldStore := ProbeStore
	ProbeStore = &probeConfigStore{
		path: filepath.Join(t.TempDir(), "probe_config.json"),
		data: probeConfigData{
			ProbeNodes:             []probeNodeRecord{{NodeNo: 7, NodeName: "node-7", NodeSecret: "secret-7"}, {NodeNo: 8, NodeName: "node-8", NodeSecret: "secret-8"}},
			ProbeSecrets:           map[string]string{"7": "secret-7", "8": "secret-8"},
			ProbeShellShortcuts:    []probeShellShortcutRecord{},
			DeletedProbeNodeNos:    []int{},
			ProbeProxyGroupBackups: map[string]probeProxyGroupBackupRecord{},
		},
	}
	probeAuthReplayStore.mu.Lock()
	probeAuthReplayStore.seen = map[string]time.Time{}
	probeAuthReplayStore.mu.Unlock()
	t.Cleanup(func() {
		ProbeStore = oldStore
		probeAuthReplayStore.mu.Lock()
		probeAuthReplayStore.seen = map[string]time.Time{}
		probeAuthReplayStore.mu.Unlock()
	})

	content := []byte(`{"version":1,"groups":[{"group":"media","rules":["domain_suffix:example.com"]}]}`)
	body, err := json.Marshal(map[string]any{
		"node_id":        "7",
		"file_name":      probeProxyGroupBackupFileName,
		"content_base64": base64.StdEncoding.EncodeToString(content),
	})
	if err != nil {
		t.Fatalf("marshal upload body: %v", err)
	}

	uploadReq := httptest.NewRequest(http.MethodPost, "/api/probe/proxy_group/backup", bytes.NewReader(body))
	uploadReq.Header.Set("Content-Type", "application/json")
	setProbeBackupAuthHeaders(uploadReq, "7", "secret-7", "upload-rand")
	uploadRR := httptest.NewRecorder()
	ProbeProxyGroupBackupHandler(uploadRR, uploadReq)
	if uploadRR.Code != http.StatusOK {
		t.Fatalf("upload status=%d body=%s", uploadRR.Code, uploadRR.Body.String())
	}

	downloadReq := httptest.NewRequest(http.MethodGet, "/api/probe/proxy_group/backup", nil)
	setProbeBackupAuthHeaders(downloadReq, "8", "secret-8", "download-rand")
	downloadRR := httptest.NewRecorder()
	ProbeProxyGroupBackupHandler(downloadRR, downloadReq)
	if downloadRR.Code != http.StatusOK {
		t.Fatalf("download status=%d body=%s", downloadRR.Code, downloadRR.Body.String())
	}

	payload := map[string]any{}
	if err := json.Unmarshal(downloadRR.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode download body: %v", err)
	}
	if payload["node_id"] != "7" {
		t.Fatalf("node_id=%v", payload["node_id"])
	}
	if payload["file_name"] != probeProxyGroupBackupFileName {
		t.Fatalf("file_name=%v", payload["file_name"])
	}
	gotRaw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(payload["content_base64"].(string)))
	if err != nil {
		t.Fatalf("decode content_base64: %v", err)
	}
	if string(gotRaw) != string(content) {
		t.Fatalf("content=%s want=%s", string(gotRaw), string(content))
	}

	ProbeStore.mu.RLock()
	defer ProbeStore.mu.RUnlock()
	if len(ProbeStore.data.ProbeProxyGroupBackups) != 1 {
		t.Fatalf("backup bucket count=%d", len(ProbeStore.data.ProbeProxyGroupBackups))
	}
	if _, ok := ProbeStore.data.ProbeProxyGroupBackups[probeProxyGroupBackupGlobalKey]; !ok {
		t.Fatalf("expected global backup key to exist")
	}
}

func TestProbeProxyGroupBackupHandlerRejectsBodyNodeMismatch(t *testing.T) {
	oldStore := ProbeStore
	ProbeStore = &probeConfigStore{
		path: filepath.Join(t.TempDir(), "probe_config.json"),
		data: probeConfigData{
			ProbeNodes:             []probeNodeRecord{{NodeNo: 7, NodeName: "node-7", NodeSecret: "secret-7"}},
			ProbeSecrets:           map[string]string{"7": "secret-7"},
			ProbeProxyGroupBackups: map[string]probeProxyGroupBackupRecord{},
		},
	}
	probeAuthReplayStore.mu.Lock()
	probeAuthReplayStore.seen = map[string]time.Time{}
	probeAuthReplayStore.mu.Unlock()
	t.Cleanup(func() {
		ProbeStore = oldStore
		probeAuthReplayStore.mu.Lock()
		probeAuthReplayStore.seen = map[string]time.Time{}
		probeAuthReplayStore.mu.Unlock()
	})

	content := []byte(`{"version":1,"groups":[{"group":"media","rules":["domain_suffix:example.com"]}]}`)
	body, _ := json.Marshal(map[string]any{
		"node_id":        "8",
		"file_name":      probeProxyGroupBackupFileName,
		"content_base64": base64.StdEncoding.EncodeToString(content),
	})
	req := httptest.NewRequest(http.MethodPost, "/api/probe/proxy_group/backup", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	setProbeBackupAuthHeaders(req, "7", "secret-7", "mismatch-rand")
	rr := httptest.NewRecorder()

	ProbeProxyGroupBackupHandler(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestNormalizeProbeProxyGroupBackupsCollapsesLegacyNodeBuckets(t *testing.T) {
	contentOld := base64.StdEncoding.EncodeToString([]byte(`{"version":1,"groups":[{"group":"old"}]}`))
	contentNew := base64.StdEncoding.EncodeToString([]byte(`{"version":1,"groups":[{"group":"new"}]}`))
	items := map[string]probeProxyGroupBackupRecord{
		"7": {
			NodeID:        "7",
			FileName:      probeProxyGroupBackupFileName,
			ContentBase64: contentOld,
			UpdatedAt:     "2026-05-16T01:02:03Z",
		},
		"8": {
			NodeID:        "8",
			FileName:      probeProxyGroupBackupFileName,
			ContentBase64: contentNew,
			UpdatedAt:     "2026-05-16T02:02:03Z",
		},
	}

	normalized := normalizeProbeProxyGroupBackups(items)
	if len(normalized) != 1 {
		t.Fatalf("normalized backup count=%d", len(normalized))
	}
	record, ok := normalized[probeProxyGroupBackupGlobalKey]
	if !ok {
		t.Fatalf("global backup key missing")
	}
	if record.NodeID != "8" {
		t.Fatalf("record node_id=%q", record.NodeID)
	}
	gotRaw, err := base64.StdEncoding.DecodeString(record.ContentBase64)
	if err != nil {
		t.Fatalf("decode normalized content: %v", err)
	}
	if string(gotRaw) != `{"version":1,"groups":[{"group":"new"}]}` {
		t.Fatalf("normalized content=%s", string(gotRaw))
	}
}

func setProbeBackupAuthHeaders(req *http.Request, nodeID, secret, randomToken string) {
	timestamp := time.Now().Unix()
	ts := strconv.FormatInt(timestamp, 10)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(strings.TrimSpace(nodeID)))
	_, _ = mac.Write([]byte("\n"))
	_, _ = mac.Write([]byte(ts))
	_, _ = mac.Write([]byte("\n"))
	_, _ = mac.Write([]byte(strings.TrimSpace(randomToken)))
	req.Header.Set("X-Probe-Node-Id", strings.TrimSpace(nodeID))
	req.Header.Set("X-Probe-Timestamp", ts)
	req.Header.Set("X-Probe-Rand", strings.TrimSpace(randomToken))
	req.Header.Set("X-Probe-Signature", hex.EncodeToString(mac.Sum(nil)))
}
