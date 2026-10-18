package controllerlink

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/gorilla/websocket"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPinnedFallbackFreshNonceRangePostAndWebSocket(t *testing.T) {
	var mu sync.Mutex
	used := map[string]bool{}
	var sequence atomic.Int32
	var primaryPosts, directPosts, connected atomic.Int32
	auth := func(r *http.Request) bool {
		nonce := r.Header.Get("X-Probe-Rand")
		mac := hmac.New(sha256.New, []byte("private-secret"))
		fmt.Fprint(mac, "7\n"+nonce+"\n"+r.Method+"\n"+r.URL.RequestURI())
		mu.Lock()
		defer mu.Unlock()
		if nonce == "" || used[nonce] || r.Header.Get("X-Probe-Signature") != hex.EncodeToString(mac.Sum(nil)) {
			return false
		}
		used[nonce] = true
		return true
	}
	direct := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/probe/auth/challenge" {
			fmt.Fprintf(w, `{"challenge":"fresh-%d"}`, sequence.Add(1))
			return
		}
		if !auth(r) {
			http.Error(w, "auth", 401)
			return
		}
		if r.Method == "POST" {
			directPosts.Add(1)
		}
		if r.URL.Path == "/api/probe" {
			c, e := (&websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}).Upgrade(w, r, nil)
			if e == nil {
				connected.Add(1)
				c.Close()
			}
			return
		}
		if r.Header.Get("Range") != "bytes=4-7" || r.Header.Get("If-Range") != "\"stable\"" {
			t.Error("resume headers lost")
		}
		w.Header().Set("Content-Range", "bytes 4-7/8")
		w.WriteHeader(206)
		io.WriteString(w, "data")
	}))
	defer direct.Close()
	hash := sha256.Sum256(direct.Certificate().RawSubjectPublicKeyInfo)
	pin := hex.EncodeToString(hash[:])
	primary := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			primaryPosts.Add(1)
		}
		if r.URL.Path != "/api/probe/auth/challenge" {
			auth(r)
		}
		http.Error(w, "cloudflare unavailable", 503)
	}))
	defer primary.Close()
	m := &Manager{entries: map[string]*state{}}
	m.Configure(primary.URL, t.TempDir())
	m.Remember(primary.URL, "7", "private-secret")
	s := m.entry(primary.URL)
	s.data = Discovery{1, time.Now().Add(time.Hour).Unix(), []Endpoint{{"192.0.2.1", 15030, pin}}}
	s.lastDiscovery = time.Now()
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != "192.0.2.1:15030" {
			t.Error("unexpected direct endpoint", address)
		}
		return (&net.Dialer{}).DialContext(ctx, network, direct.Listener.Addr().String())
	}
	tr := m.Wrap(primary.URL, primary.Client().Transport.(*http.Transport).Clone(), dial)
	defer tr.CloseIdleConnections()
	req, _ := http.NewRequest("GET", primary.URL+"/api/probe/proxy/download", nil)
	req.Header.Set("Range", "bytes=4-7")
	req.Header.Set("If-Range", "\"stable\"")
	Sign(req, "7", "private-secret", "initial")
	resp, e := tr.RoundTrip(req)
	if e != nil {
		t.Fatal(e)
	}
	b, e := io.ReadAll(resp.Body)
	resp.Body.Close()
	if e != nil || resp.StatusCode != 206 || string(b) != "data" {
		t.Fatalf("fallback response %v %s", e, b)
	}
	s.preferUntil = time.Time{}
	post, _ := http.NewRequest("POST", primary.URL+"/api/probe/info_box", strings.NewReader("payload"))
	Sign(post, "7", "private-secret", "post-nonce")
	resp, e = tr.RoundTrip(post)
	if resp != nil {
		resp.Body.Close()
	}
	if e != nil || primaryPosts.Load() != 1 || directPosts.Load() != 0 {
		t.Fatal("POST replayed", e, primaryPosts.Load(), directPosts.Load())
	}
	wsURL := "wss" + strings.TrimPrefix(primary.URL, "https") + "/api/probe"
	h := http.Header{}
	signed, _ := http.NewRequest("GET", primary.URL+"/api/probe", nil)
	Sign(signed, "7", "private-secret", "ws-nonce")
	h = signed.Header
	conn, _, isDirect, e := m.DialWebSocket(t.Context(), wsURL, h, &websocket.Dialer{TLSClientConfig: primary.Client().Transport.(*http.Transport).TLSClientConfig}, dial)
	if e != nil {
		t.Fatal(e)
	}
	conn.Close()
	if !isDirect || connected.Load() != 1 {
		t.Fatal("duplicate/missing control session")
	}
	s.data.Endpoints[0].Pin = strings.Repeat("0", 64)
	req, _ = http.NewRequest("GET", primary.URL+"/api/probe/route/config", nil)
	Sign(req, "7", "private-secret", "bad-pin")
	resp, e = tr.RoundTrip(req)
	if resp != nil {
		resp.Body.Close()
	}
	if e == nil && resp != nil && resp.StatusCode == 200 {
		t.Fatal("wrong pin accepted")
	}
}

func TestDiscoveryVerifiedPrimaryAndOfflineCache(t *testing.T) {
	var discovery Discovery
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "challenge") {
			time.Sleep(4 * time.Second)
			io.WriteString(w, `{"challenge":"once"}`)
			return
		}
		json.NewEncoder(w).Encode(discovery)
	}))
	defer server.Close()
	hash := sha256.Sum256(server.Certificate().RawSubjectPublicKeyInfo)
	discovery = Discovery{1, time.Now().Add(time.Hour).Unix(), []Endpoint{{"192.0.2.2", 15030, hex.EncodeToString(hash[:])}}}
	dir := t.TempDir()
	m := &Manager{entries: map[string]*state{}}
	m.Configure(server.URL, dir)
	m.Remember(server.URL, "7", "secret")
	start := time.Now()
	m.prepare(t.Context(), server.URL, server.Client().Transport)
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("initial weak-network discovery blocked the caller")
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		m.mu.Lock()
		refreshing := m.entry(server.URL).refreshing
		m.mu.Unlock()
		if !refreshing || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	loaded := &Manager{entries: map[string]*state{}}
	loaded.Configure(server.URL, dir)
	d, _ := loaded.snapshot(server.URL)
	if len(d.Endpoints) != 1 {
		t.Fatal("cache not restored")
	}
	loaded.Configure("https://other-controller.invalid", dir)
	d, _ = loaded.snapshot("https://other-controller.invalid")
	if len(d.Endpoints) != 0 {
		t.Fatal("cross-origin pin reuse")
	}
	m.Feedback(server.URL, false, time.Second, false)
	_, prefer := m.snapshot(server.URL)
	if !prefer {
		t.Fatal("did not prefer direct")
	}
	m.Feedback(server.URL, false, time.Millisecond, true)
	_, prefer = m.snapshot(server.URL)
	if !prefer {
		t.Fatal("single probe caused oscillation")
	}
	m.Feedback(server.URL, false, time.Millisecond, true)
	_, prefer = m.snapshot(server.URL)
	if prefer {
		t.Fatal("healthy primary did not recover")
	}
}

func TestPinAndCacheRejectExpiredIdentities(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	cert := *server.Certificate()
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	pin := hex.EncodeToString(sum[:])
	cert.NotAfter = time.Now().Add(-time.Minute)
	if e := PinTLS(pin).VerifyConnection(tls.ConnectionState{PeerCertificates: []*x509.Certificate{&cert}}); e == nil {
		t.Fatal("expired pinned certificate accepted")
	}
	d := Discovery{1, time.Now().Add(-time.Minute).Unix(), []Endpoint{{"192.0.2.1", 15030, pin}}}
	if valid(d) {
		t.Fatal("expired endpoint cache accepted")
	}
}
