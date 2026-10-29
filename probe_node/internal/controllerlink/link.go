// Package controllerlink adds an authenticated, pinned origin fallback to probe
// requests. It never changes the configured controller identity or retries POST.
package controllerlink

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const DiscoveryPath = "/api/probe/controller/endpoints"

type Endpoint struct {
	IP   string `json:"ip"`
	Port int    `json:"port"`
	Pin  string `json:"tls_spki_sha256"`
}
type Discovery struct {
	Version   int        `json:"version"`
	ExpiresAt int64      `json:"expires_at"`
	Endpoints []Endpoint `json:"endpoints"`
}
type cacheFile struct {
	Origin string `json:"origin"`
	Discovery
}
type state struct {
	path                            string
	data                            Discovery
	loaded                          bool
	refreshing                      bool
	lastDiscovery, lastProbe        time.Time
	preferUntil                     time.Time
	primaryFailures, directFailures int
	primarySuccesses                int
	primaryLatency, directLatency   time.Duration
	endpoints                       map[Endpoint]endpointHealth
	node, secret                    string
}
type Manager struct {
	mu      sync.Mutex
	entries map[string]*state
}

var Default = &Manager{entries: map[string]*state{}}

type DialFunc func(context.Context, string, string) (net.Conn, error)

func Origin(raw string) string {
	u, e := url.Parse(raw)
	if e != nil || u.Host == "" || u.User != nil {
		return ""
	}
	if u.Scheme == "wss" {
		u.Scheme = "https"
	}
	if u.Scheme == "ws" {
		u.Scheme = "http"
	}
	return strings.ToLower(u.Scheme + "://" + u.Host)
}
func IsProbeURL(raw string) bool {
	u, e := url.Parse(raw)
	return e == nil && (u.Path == "/api/probe" || strings.HasPrefix(u.Path, "/api/probe/"))
}
func (m *Manager) entry(origin string) *state {
	s := m.entries[origin]
	if s == nil {
		s = &state{}
		m.entries[origin] = s
	}
	return s
}
func (m *Manager) Configure(raw, dir string) {
	origin := Origin(raw)
	if origin == "" || dir == "" {
		return
	}
	sum := sha256.Sum256([]byte(origin))
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.entry(origin)
	path := filepath.Join(dir, fmt.Sprintf("controller-direct-%x.json", sum[:8]))
	if s.path != path {
		s.path = path
		s.loaded = false
	}
	m.load(origin, s)
}
func (m *Manager) Remember(raw, node, secret string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.entry(Origin(raw))
	s.node = node
	s.secret = secret
}
func valid(d Discovery) bool {
	now := time.Now().Unix()
	if d.Version != 1 || d.ExpiresAt <= now || d.ExpiresAt > now+31*86400 || len(d.Endpoints) > 4 {
		return false
	}
	for _, e := range d.Endpoints {
		ip := net.ParseIP(e.IP)
		pin, err := hex.DecodeString(e.Pin)
		if ip == nil || !ip.IsGlobalUnicast() || ip.IsLoopback() || ip.IsUnspecified() || e.Port != 15030 || err != nil || len(pin) != 32 {
			return false
		}
	}
	return true
}
func (m *Manager) load(origin string, s *state) {
	if s.loaded {
		return
	}
	s.loaded = true
	if s.path == "" {
		return
	}
	f, e := os.Open(s.path)
	if e != nil {
		return
	}
	defer f.Close()
	var c cacheFile
	if json.NewDecoder(io.LimitReader(f, 8192)).Decode(&c) == nil && c.Origin == origin && valid(c.Discovery) {
		s.data = c.Discovery
	}
}
func (m *Manager) snapshot(raw string) (Discovery, bool) {
	origin := Origin(raw)
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.entry(origin)
	m.load(origin, s)
	if !valid(s.data) {
		return Discovery{}, false
	}
	data := s.data
	data.Endpoints = availableEndpoints(s, time.Now())
	return data, time.Now().Before(s.preferUntil)
}
func (m *Manager) Feedback(raw string, direct bool, elapsed time.Duration, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.entry(Origin(raw))
	if direct {
		if ok {
			s.directFailures = 0
			if !s.preferUntil.IsZero() {
				s.preferUntil = time.Now().Add(60 * time.Second)
			}
			s.directLatency = (s.directLatency*3 + elapsed) / 4
		} else {
			s.directFailures++
			s.preferUntil = time.Time{}
		}
	} else {
		if ok {
			s.primaryFailures = 0
			s.primarySuccesses++
			s.primaryLatency = (s.primaryLatency*3 + elapsed) / 4
			if elapsed < 2*time.Second && s.primarySuccesses >= 2 {
				s.preferUntil = time.Time{}
			}
		} else {
			s.primaryFailures++
			s.primarySuccesses = 0
		}
		if !ok || elapsed > 2500*time.Millisecond {
			s.primarySuccesses = 0
			s.preferUntil = time.Now().Add(60 * time.Second)
		}
	}
}
func (m *Manager) SessionEnded(raw string, direct bool, duration time.Duration) {
	// A short-lived control stream is evidence of instability even when TLS succeeded.
	if duration < 90*time.Second {
		m.Feedback(raw, direct, duration, false)
	}
}

func PinTLS(pin string) *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true, // SPKI and lifetime replace DNS/CA validation only on discovered IP sockets.
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("missing controller certificate")
			}
			leaf := cs.PeerCertificates[0]
			now := time.Now()
			if now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) {
				return errors.New("controller certificate outside validity period")
			}
			sum := sha256.Sum256(leaf.RawSubjectPublicKeyInfo)
			if !hmac.Equal([]byte(hex.EncodeToString(sum[:])), []byte(strings.ToLower(pin))) {
				return errors.New("controller public key mismatch")
			}
			if leaf.KeyUsage != 0 && leaf.KeyUsage&x509.KeyUsageDigitalSignature == 0 {
				return errors.New("invalid controller certificate usage")
			}
			return nil
		}}
}

func Sign(req *http.Request, node, secret, challenge string) {
	path := req.URL.EscapedPath()
	if req.URL.RawQuery != "" {
		path += "?" + req.URL.RawQuery
	}
	mac := hmac.New(sha256.New, []byte(secret))
	io.WriteString(mac, node+"\n"+challenge+"\n"+req.Method+"\n"+path)
	req.Header.Set("X-Probe-Node-Id", node)
	req.Header.Set("X-Probe-Rand", challenge)
	req.Header.Set("X-Probe-Signature", hex.EncodeToString(mac.Sum(nil)))
}
func challenge(ctx context.Context, origin, node string, rt http.RoundTripper) (string, error) {
	return challengeWithin(ctx, origin, node, rt, 3*time.Second)
}
func challengeWithin(ctx context.Context, origin, node string, rt http.RoundTripper, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", origin+"/api/probe/auth/challenge?node_id="+url.QueryEscape(node), nil)
	resp, e := rt.RoundTrip(req)
	if e != nil {
		return "", e
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("controller challenge status %d", resp.StatusCode)
	}
	var p struct {
		Challenge string `json:"challenge"`
	}
	e = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&p)
	if e == nil && p.Challenge == "" {
		e = errors.New("empty controller challenge")
	}
	return p.Challenge, e
}
func (m *Manager) prepare(ctx context.Context, raw string, primary http.RoundTripper) {
	origin := Origin(raw)
	if !strings.HasPrefix(origin, "https://") {
		return
	}
	m.mu.Lock()
	s := m.entry(origin)
	m.load(origin, s)
	if s.refreshing || time.Since(s.lastDiscovery) < 30*time.Second || s.node == "" {
		m.mu.Unlock()
		return
	}
	s.refreshing = true
	s.lastDiscovery = time.Now()
	node, secret := s.node, s.secret
	m.mu.Unlock()
	refresh := func() {
		defer func() { m.mu.Lock(); m.entry(origin).refreshing = false; m.mu.Unlock() }()
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		nonce, e := challengeWithin(ctx, origin, node, primary, 30*time.Second)
		if e != nil {
			return
		}
		req, _ := http.NewRequestWithContext(ctx, "GET", origin+DiscoveryPath, nil)
		Sign(req, node, secret, nonce)
		resp, e := primary.RoundTrip(req)
		if e != nil {
			return
		}
		defer resp.Body.Close()
		// Only the verified primary HTTPS identity may introduce or rotate a pin.
		if resp.StatusCode != 200 || resp.TLS == nil || len(resp.TLS.VerifiedChains) == 0 {
			return
		}
		var data Discovery
		if json.NewDecoder(io.LimitReader(resp.Body, 8192)).Decode(&data) != nil || !valid(data) {
			return
		}
		m.mu.Lock()
		s := m.entry(origin)
		s.data = data
		if len(data.Endpoints) > 0 {
			s.lastDiscovery = time.Now().Add(time.Hour - 30*time.Second)
		}
		path := s.path
		m.mu.Unlock()
		if path != "" {
			bytes, _ := json.Marshal(cacheFile{origin, data})
			if os.MkdirAll(filepath.Dir(path), 0700) == nil {
				tmp := path + ".tmp"
				if os.WriteFile(tmp, bytes, 0600) == nil {
					os.Rename(tmp, path)
				}
			}
		}
	}
	// Initial discovery must not delay the caller. Weak primary paths get a
	// separate budget; normal probe requests retain their short failover limits.
	go refresh()
}

type Transport struct {
	Manager *Manager
	Primary *http.Transport
	Dial    DialFunc
	origin  string
}

func (m *Manager) Wrap(raw string, primary *http.Transport, dial DialFunc) *Transport {
	return &Transport{m, primary, dial, Origin(raw)}
}
func (t *Transport) CloseIdleConnections() { t.Primary.CloseIdleConnections() }
func (t *Transport) direct(e Endpoint) *http.Transport {
	dial := t.Dial
	if dial == nil {
		dial = (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	}
	return &http.Transport{Proxy: nil, DisableKeepAlives: true, DialTLSContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		conn, err := dial(ctx, "tcp", net.JoinHostPort(e.IP, fmt.Sprint(e.Port)))
		if err != nil {
			return nil, err
		}
		secured := tls.Client(conn, PinTLS(e.Pin))
		if err = secured.HandshakeContext(ctx); err != nil {
			conn.Close()
			return nil, err
		}
		return secured, nil
	}}
}

type cancelBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

type observedBody struct {
	io.ReadCloser
	failed func()
}

// ReportDownloadFailure lets a payload consumer reject a path whose body is
// making progress too slowly. Report before cancelling the request so the
// selected endpoint, rather than all direct endpoints, enters cooldown.
func ReportDownloadFailure(resp *http.Response) {
	if resp != nil {
		if body, ok := resp.Body.(observedBody); ok {
			body.failed()
		}
	}
}

func (b observedBody) Read(p []byte) (int, error) {
	n, e := b.ReadCloser.Read(p)
	if e != nil && e != io.EOF {
		b.failed()
	}
	return n, e
}

func (b cancelBody) Close() error { defer b.cancel(); return b.ReadCloser.Close() }
func attempt(req *http.Request, rt http.RoundTripper, budget time.Duration) (*http.Response, error) {
	// Bound time-to-headers, not the streaming download body.
	ctx, cancel := context.WithCancel(req.Context())
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline)/2 < budget {
		budget = time.Until(deadline) / 2
	}
	timer := time.AfterFunc(budget, cancel)
	resp, e := rt.RoundTrip(req.Clone(ctx))
	timer.Stop()
	if e != nil {
		cancel()
		return resp, e
	}
	resp.Body = cancelBody{resp.Body, cancel}
	return resp, nil
}
func retryable(resp *http.Response, e error) bool {
	if e == nil && resp.StatusCode == 503 && resp.Header.Get("X-CloudHelper-Cache") == "preparing" {
		return false
	}
	return e != nil || resp.StatusCode >= 500 || resp.StatusCode == 429 || resp.StatusCode == 403
}
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if Origin(req.URL.String()) != t.origin || !IsProbeURL(req.URL.String()) {
		return t.Primary.RoundTrip(req)
	}
	t.Manager.prepare(req.Context(), req.URL.String(), t.Primary)
	data, prefer := t.Manager.snapshot(req.URL.String())
	if len(data.Endpoints) == 0 {
		return t.Primary.RoundTrip(req)
	}
	// Prefer a stable direct path temporarily, but probe the primary without
	// registering a second control session or consuming a business nonce.
	if prefer {
		t.probePrimary(req)
	}
	var resp *http.Response
	var err error
	// Upgrade payloads should not linger on a responsive but slow CDN path.
	// Keep normal control traffic on the existing stability-based policy.
	firstDirect := prefer || ((req.Method == "GET" || req.Method == "HEAD") && req.URL.Path == "/api/probe/proxy/download")
	rounds := len(data.Endpoints) + 1
	for i := 0; i < rounds; i++ {
		direct := firstDirect || i > 0
		index := i - 1
		if firstDirect {
			direct = i < len(data.Endpoints)
			index = i
		}
		var rt http.RoundTripper = t.Primary
		if direct {
			rt = t.direct(data.Endpoints[index])
		}
		candidate := req.Clone(req.Context())
		if i > 0 {
			if req.Method != "GET" && req.Method != "HEAD" {
				break
			}
			if req.Header.Get("X-Probe-Signature") != "" {
				t.Manager.mu.Lock()
				s := t.Manager.entry(t.origin)
				node, secret := s.node, s.secret
				t.Manager.mu.Unlock()
				if node == "" || secret == "" {
					break
				}
				nonce, e := challenge(req.Context(), t.origin, node, rt)
				if e != nil {
					if direct {
						t.Manager.endpointFeedback(req.Context(), t.origin, data.Endpoints[index], false)
					}
					t.Manager.Feedback(t.origin, direct, 0, false)
					err = e
					continue
				}
				Sign(candidate, node, secret, nonce)
			}
		}
		start := time.Now()
		budget := 3 * time.Second
		if direct {
			budget = 8 * time.Second
		}
		if req.URL.Path == "/api/probe/certificate" {
			budget = 8 * time.Minute
		}
		resp, err = attempt(candidate, rt, budget)
		ok := !retryable(resp, err)
		if direct {
			t.Manager.endpointFeedback(req.Context(), t.origin, data.Endpoints[index], ok)
		}
		t.Manager.Feedback(req.URL.String(), direct, time.Since(start), ok)
		if ok {
			resp.Body = observedBody{resp.Body, func() {
				if req.Context().Err() == nil {
					if direct {
						t.Manager.endpointFeedback(req.Context(), t.origin, data.Endpoints[index], false)
					}
					t.Manager.Feedback(t.origin, direct, 0, false)
				}
			}}
			return resp, err
		}
		if i+1 == rounds || (req.Method != "GET" && req.Method != "HEAD") {
			return resp, err
		}
		if resp != nil {
			resp.Body.Close()
			resp = nil
		}
	}
	if err == nil {
		err = errors.New("controller fallback unavailable")
	}
	return nil, err
}
func (t *Transport) probePrimary(req *http.Request) {
	t.Manager.mu.Lock()
	s := t.Manager.entry(t.origin)
	if time.Since(s.lastProbe) < 60*time.Second {
		t.Manager.mu.Unlock()
		return
	}
	s.lastProbe = time.Now()
	node := s.node
	t.Manager.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		start := time.Now()
		_, e := challenge(ctx, t.origin, node, t.Primary)
		t.Manager.Feedback(t.origin, false, time.Since(start), e == nil)
	}()
}

func (m *Manager) DialWebSocket(ctx context.Context, raw string, headers http.Header, primary *websocket.Dialer, dial DialFunc) (*websocket.Conn, *http.Response, bool, error) {
	data, prefer := m.snapshot(raw)
	try := func(direct bool, e Endpoint) (*websocket.Conn, *http.Response, error) {
		d := *primary
		h := headers.Clone()
		if len(data.Endpoints) > 0 {
			d.HandshakeTimeout = 3 * time.Second
		}
		if direct {
			tr := (&Transport{Manager: m, Primary: &http.Transport{}, Dial: dial, origin: Origin(raw)}).direct(e)
			defer tr.CloseIdleConnections()
			d.Proxy = nil
			d.NetDialTLSContext = tr.DialTLSContext
			d.TLSClientConfig = nil
			d.HandshakeTimeout = 8 * time.Second
			m.mu.Lock()
			s := m.entry(Origin(raw))
			node, secret := s.node, s.secret
			m.mu.Unlock()
			nonce, err := challenge(ctx, Origin(raw), node, tr)
			if err != nil {
				return nil, nil, err
			}
			req, _ := http.NewRequest("GET", raw, nil)
			req.Header = h
			Sign(req, node, secret, nonce)
			h = req.Header
		}
		return d.DialContext(ctx, raw, h)
	}
	var conn *websocket.Conn
	var resp *http.Response
	var err error
	if !prefer {
		start := time.Now()
		conn, resp, err = try(false, Endpoint{})
		m.Feedback(raw, false, time.Since(start), err == nil)
		if err == nil {
			return conn, resp, false, nil
		}
		if resp != nil {
			resp.Body.Close()
		}
	}
	for _, e := range data.Endpoints {
		start := time.Now()
		conn, resp, err = try(true, e)
		m.endpointFeedback(ctx, raw, e, err == nil)
		m.Feedback(raw, true, time.Since(start), err == nil)
		if err == nil {
			return conn, resp, true, nil
		}
		if resp != nil {
			resp.Body.Close()
		}
	}
	if prefer {
		conn, resp, err = try(false, Endpoint{})
	}
	return conn, resp, false, err
}
