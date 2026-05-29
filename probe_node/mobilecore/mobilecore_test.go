package mobilecore

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveWebSocketURL(t *testing.T) {
	got, err := resolveWebSocketURL("https://controller.example.com:15030/admin")
	if err != nil {
		t.Fatal(err)
	}
	if got != "wss://controller.example.com:15030/api/probe" {
		t.Fatalf("ws url=%q", got)
	}
}

func TestSignConnect(t *testing.T) {
	got := signConnect("secret-1", "node-1", "100", "abc")
	mac := hmac.New(sha256.New, []byte("secret-1"))
	_, _ = mac.Write([]byte("node-1\n100\nabc"))
	want := hex.EncodeToString(mac.Sum(nil))
	if got != want {
		t.Fatalf("signature=%q want %q", got, want)
	}
}

func TestRefreshConfigFilesWritesProxyAndChainCaches(t *testing.T) {
	proxyGroup := `{"groups":[{"group":"default","rules":[],"fallback":"direct"}]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Probe-Node-Id") != "7" {
			t.Fatalf("missing auth node id for %s", r.URL.Path)
		}
		switch r.URL.Path {
		case "/api/probe/proxy_group/backup":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":             true,
				"node_id":        "7",
				"file_name":      "proxy_group.json",
				"content_base64": base64.StdEncoding.EncodeToString([]byte(proxyGroup)),
			})
		case "/api/probe/link/config/grouped":
			if r.URL.Query().Get("node_id") != "7" || r.URL.Query().Get("secret") != "secret-7" {
				t.Fatalf("missing query identity: %s", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"node_id": "7",
				"self_chains": []map[string]any{
					{"chain_id": "self-1", "chain_type": "port_forward"},
				},
				"global_proxy_forward_chains": []map[string]any{
					{"chain_id": "proxy-1", "chain_type": "proxy_chain"},
					{"chain_id": "proxy-2", "chain_type": "proxy_chain"},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	summary, err := refreshConfigFiles(server.URL, "7", "secret-7", dir)
	if err != nil {
		t.Fatalf("refreshConfigFiles returned error: %v", err)
	}
	if !summary.ProxyGroupUpdated || summary.SelfChains != 1 || summary.ProxyEntries != 2 {
		t.Fatalf("unexpected summary: %+v", summary)
	}
	if got := readTestFile(t, filepath.Join(dir, "proxy_group.json")); !strings.Contains(got, `"groups"`) {
		t.Fatalf("proxy_group.json not written: %s", got)
	}
	if got := readTestFile(t, filepath.Join(dir, "probe_link_chain_config.json")); !strings.Contains(got, `"self-1"`) {
		t.Fatalf("probe_link_chain_config.json not written: %s", got)
	}
	if got := readTestFile(t, filepath.Join(dir, "proxy_chain.json")); !strings.Contains(got, `"proxy-2"`) {
		t.Fatalf("proxy_chain.json not written: %s", got)
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
