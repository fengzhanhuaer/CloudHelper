package mobilecore

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"
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

func TestParseLinuxMemInfo(t *testing.T) {
	total, used, swapTotal, swapUsed := parseLinuxMemInfo(strings.NewReader(strings.Join([]string{
		"MemTotal:        1000 kB",
		"MemAvailable:     250 kB",
		"SwapTotal:        400 kB",
		"SwapFree:         100 kB",
	}, "\n")))
	if total != 1000*1024 || used != 750*1024 || swapTotal != 400*1024 || swapUsed != 300*1024 {
		t.Fatalf("meminfo total=%d used=%d swapTotal=%d swapUsed=%d", total, used, swapTotal, swapUsed)
	}
}

func TestParseCPUSnapshot(t *testing.T) {
	got, ok := parseCPUSnapshot(strings.NewReader("cpu  10 20 30 40 50 60 70\n"))
	if !ok {
		t.Fatal("expected cpu snapshot")
	}
	if got.total != 280 || got.idle != 90 {
		t.Fatalf("snapshot=%+v", got)
	}
}

func TestResolveLinkEndpointUsesProjectedRelayChain(t *testing.T) {
	item := linkChainServerItem{
		ChainID:        "client-chain",
		RelayChainID:   "relay-chain",
		Name:           "Android Link",
		Secret:         "link-secret",
		EntryNodeID:    "node-1",
		ExitNodeID:     "node-2",
		LinkLayer:      "ws",
		CascadeNodeIDs: []string{"node-9"},
		HopConfigs: []linkChainHopItem{
			{NodeNo: 1, RelayHost: "relay.example.com", ExternalPort: 443, ListenPort: 8443, LinkLayer: "websocket"},
		},
	}
	endpoint, err := resolveLinkEndpoint(item)
	if err != nil {
		t.Fatalf("resolveLinkEndpoint returned error: %v", err)
	}
	if endpoint.ChainID != "relay-chain" || endpoint.EntryHost != "relay.example.com" || endpoint.EntryPort != 443 || endpoint.LinkLayer != "websocket" {
		t.Fatalf("unexpected endpoint: %+v", endpoint)
	}
}

func TestLinkStatusReadsProxyChainCache(t *testing.T) {
	dir := t.TempDir()
	writeTestJSON(t, filepath.Join(dir, "proxy_chain.json"), map[string]any{
		"items": []map[string]any{
			{
				"chain_id":       "client-chain",
				"relay_chain_id": "relay-chain",
				"name":           "Android Link",
				"secret":         "link-secret",
				"entry_node_id":  "1",
				"exit_node_id":   "2",
				"link_layer":     "websocket",
				"hop_configs": []map[string]any{
					{"node_no": 1, "relay_host": "127.0.0.1", "external_port": 443},
				},
			},
		},
	})
	var payload struct {
		OK     bool             `json:"ok"`
		Chains []linkStatusItem `json:"chains"`
	}
	if err := json.Unmarshal([]byte(LinkStatus(dir)), &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.OK || len(payload.Chains) != 1 {
		t.Fatalf("unexpected status payload: %+v", payload)
	}
	if payload.Chains[0].Status != "configured" || payload.Chains[0].RelayChainID != "relay-chain" {
		t.Fatalf("unexpected chain status: %+v", payload.Chains[0])
	}
}

func TestLinkLatencyAndSpeedUseRelayProtocol(t *testing.T) {
	const chainID = "relay-chain"
	const secret = "link-secret"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != linkRelayAPIPath {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("chain_id") != chainID {
			t.Fatalf("chain_id query=%q", r.URL.Query().Get("chain_id"))
		}
		assertLinkAuth(t, r, chainID, secret)
		mode := r.Header.Get(linkCodexRelayModeHeader)
		upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("upgrade failed: %v", err)
		}
		conn := newWebSocketNetConn(ws)
		switch mode {
		case linkRelayModeBridge:
			serveTestPingPongRelay(t, conn)
		case linkRelayModeSpeedTest:
			byteCount, _ := strconv.ParseInt(r.Header.Get(linkCodexSpeedBytesHeader), 10, 64)
			if byteCount <= 0 {
				t.Fatalf("missing speed byte count")
			}
			writeTestSpeedBytes(t, conn, byteCount)
		default:
			t.Fatalf("unexpected relay mode %q", mode)
		}
	}))
	defer server.Close()

	host, port := testServerHostPort(t, server)
	dir := t.TempDir()
	writeTestJSON(t, filepath.Join(dir, "proxy_chain.json"), map[string]any{
		"items": []map[string]any{
			{
				"chain_id":       "client-chain",
				"relay_chain_id": chainID,
				"name":           "Android Link",
				"secret":         secret,
				"entry_node_id":  "1",
				"exit_node_id":   "2",
				"link_layer":     "websocket",
				"hop_configs": []map[string]any{
					{"node_no": 1, "relay_host": host, "external_port": port},
				},
			},
		},
	})

	var latency struct {
		OK           bool   `json:"ok"`
		Status       string `json:"status"`
		BestProtocol string `json:"best_protocol"`
	}
	if err := json.Unmarshal([]byte(LinkLatency(dir, "client-chain")), &latency); err != nil {
		t.Fatal(err)
	}
	if !latency.OK || latency.Status != "reachable" || latency.BestProtocol != "websocket" {
		t.Fatalf("unexpected latency result: %+v", latency)
	}

	_, endpoint, err := loadLinkEndpointByID(dir, "client-chain")
	if err != nil {
		t.Fatal(err)
	}
	speed := linkRelaySpeedTestWithLayer(endpoint, "websocket", 4096, 5*time.Second)
	if !speed.OK || speed.Bytes != 4096 || speed.RateBPS <= 0 {
		t.Fatalf("unexpected speed result: %+v", speed)
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

func writeTestJSON(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func testServerHostPort(t *testing.T, server *httptest.Server) (string, int) {
	t.Helper()
	host, portText, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "https://"))
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	return host, port
}

func assertLinkAuth(t *testing.T, r *http.Request, chainID string, secret string) {
	t.Helper()
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	nonce := strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
	if nonce == "" || nonce == auth {
		t.Fatalf("missing bearer nonce: %q", auth)
	}
	want := buildLinkHMAC(secret, chainID, nonce)
	if got := r.Header.Get(linkCodexMACHeader); got != want {
		t.Fatalf("mac=%q want %q", got, want)
	}
	if r.Header.Get(linkCodexAuthModeHeader) != "secret_hmac" {
		t.Fatalf("auth mode=%q", r.Header.Get(linkCodexAuthModeHeader))
	}
}

func serveTestPingPongRelay(t *testing.T, conn net.Conn) {
	t.Helper()
	defer conn.Close()
	session, err := yamux.Server(conn, newLinkYamuxConfig())
	if err != nil {
		t.Fatalf("yamux server: %v", err)
	}
	defer session.Close()
	stream, err := session.Accept()
	if err != nil {
		t.Fatalf("yamux accept: %v", err)
	}
	defer stream.Close()
	var req linkTunnelOpenRequest
	if err := json.NewDecoder(stream).Decode(&req); err != nil {
		t.Fatalf("decode ping request: %v", err)
	}
	if req.Type != linkRelayModePingPong || req.PingBytes != 64 {
		t.Fatalf("unexpected ping request: %+v", req)
	}
	if err := json.NewEncoder(stream).Encode(linkTunnelOpenResponse{OK: true}); err != nil {
		t.Fatalf("encode ping response: %v", err)
	}
	buf := make([]byte, req.PingBytes)
	if _, err := io.ReadFull(stream, buf); err != nil {
		t.Fatalf("read ping payload: %v", err)
	}
	if _, err := stream.Write(buf); err != nil {
		t.Fatalf("write ping echo: %v", err)
	}
}

func writeTestSpeedBytes(t *testing.T, conn net.Conn, byteCount int64) {
	t.Helper()
	defer conn.Close()
	chunk := []byte(strings.Repeat("a", 1024))
	remaining := byteCount
	for remaining > 0 {
		n := int64(len(chunk))
		if remaining < n {
			n = remaining
		}
		if _, err := conn.Write(chunk[:n]); err != nil {
			t.Fatalf("write speed bytes: %v", err)
		}
		remaining -= n
	}
}
