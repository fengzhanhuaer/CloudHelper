package controllerlink

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDownloadPrefersPinnedIPAndFallsBackWithFreshAuth(t *testing.T) {
	for _, failDirect := range []bool{false, true} {
		t.Run(fmt.Sprint("direct-failure=", failDirect), func(t *testing.T) {
			var mu sync.Mutex
			var calls []string
			used := map[string]bool{}
			sequence := 0
			handler := func(endpoint string) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					mu.Lock()
					defer mu.Unlock()
					if r.URL.Path == "/api/probe/auth/challenge" {
						sequence++
						fmt.Fprintf(w, `{"challenge":"fresh-%d"}`, sequence)
						return
					}
					calls = append(calls, endpoint+":"+r.URL.Path)
					nonce := r.Header.Get("X-Probe-Rand")
					mac := hmac.New(sha256.New, []byte("secret"))
					fmt.Fprint(mac, "7\n"+nonce+"\nGET\n"+r.URL.RequestURI())
					if nonce == "" || used[nonce] || r.Header.Get("X-Probe-Signature") != hex.EncodeToString(mac.Sum(nil)) {
						t.Error("missing private authentication or replayed nonce")
						w.WriteHeader(401)
						return
					}
					used[nonce] = true
					if r.URL.Path == "/api/probe/proxy/download" {
						if r.Header.Get("Range") != "bytes=4-7" || r.Header.Get("If-Range") != `"stable"` {
							t.Error("range/validator lost on IP or fallback")
						}
						if failDirect && endpoint == "ip" {
							w.WriteHeader(503)
							return
						}
						w.WriteHeader(206)
					}
					fmt.Fprint(w, "data")
				}
			}
			direct := httptest.NewTLSServer(handler("ip"))
			defer direct.Close()
			primary := httptest.NewTLSServer(handler("domain"))
			defer primary.Close()
			pin := sha256.Sum256(direct.Certificate().RawSubjectPublicKeyInfo)
			m := &Manager{entries: map[string]*state{}}
			m.Remember(primary.URL, "7", "secret")
			s := m.entry(primary.URL)
			s.loaded = true
			s.data = Discovery{1, time.Now().Add(time.Hour).Unix(), []Endpoint{{"192.0.2.1", 15030, hex.EncodeToString(pin[:])}}}
			s.lastDiscovery = time.Now()
			tr := m.Wrap(primary.URL, primary.Client().Transport.(*http.Transport).Clone(), func(ctx context.Context, network, address string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, direct.Listener.Addr().String())
			})
			defer tr.CloseIdleConnections()
			get := func(path, nonce string) *http.Response {
				t.Helper()
				req, _ := http.NewRequestWithContext(t.Context(), "GET", primary.URL+path, nil)
				req.Header.Set("Range", "bytes=4-7")
				req.Header.Set("If-Range", `"stable"`)
				Sign(req, "7", "secret", nonce)
				resp, err := tr.RoundTrip(req)
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(resp.Body)
				resp.Body.Close()
				if err != nil || string(body) != "data" {
					t.Fatal("bad response", err, string(body))
				}
				return resp
			}
			get("/api/probe/route/config", "ordinary")
			resp := get("/api/probe/proxy/download", "download")
			mu.Lock()
			defer mu.Unlock()
			want := []string{"domain:/api/probe/route/config", "ip:/api/probe/proxy/download"}
			if failDirect {
				want = append(want, "domain:/api/probe/proxy/download")
				if sequence != 1 {
					t.Fatal("fallback did not obtain a fresh nonce", sequence)
				}
			} else {
				ReportDownloadFailure(resp)
				m.mu.Lock()
				available := availableEndpoints(s, time.Now())
				m.mu.Unlock()
				if len(available) != 0 {
					t.Fatal("slow download did not cool down its IP")
				}
			}
			if fmt.Sprint(calls) != fmt.Sprint(want) {
				t.Fatal("incorrect endpoint priority", calls, want)
			}
		})
	}
}

func TestUpgradeChallengeAndMetadataReuseTrustedIPs(t *testing.T) {
	var mu sync.Mutex
	var dials atomic.Int32
	var allowPrimary atomic.Bool
	var primaryCalls atomic.Int32
	var paths []string
	direct := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		if r.URL.Path == "/api/probe/auth/challenge" {
			fmt.Fprint(w, `{"challenge":"fresh"}`)
			return
		}
		if r.Header.Get("X-Probe-Signature") == "" {
			t.Error("private auth missing")
		}
		fmt.Fprint(w, "data")
	}))
	defer direct.Close()
	primary := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryCalls.Add(1)
		if !allowPrimary.Load() {
			t.Errorf("upgrade contacted primary despite valid IP cache: %s", r.URL.Path)
		}
		w.WriteHeader(503)
	}))
	defer primary.Close()
	pin := sha256.Sum256(direct.Certificate().RawSubjectPublicKeyInfo)
	m := &Manager{entries: map[string]*state{}, reuseDirect: true}
	m.Remember(primary.URL, "7", "secret")
	s := m.entry(primary.URL)
	s.data = Discovery{1, time.Now().Add(7 * 24 * time.Hour).Unix(), []Endpoint{{"192.0.2.1", 15030, hex.EncodeToString(pin[:])}}}
	tr := m.Wrap(primary.URL, primary.Client().Transport.(*http.Transport).Clone(), func(ctx context.Context, n, a string) (net.Conn, error) {
		dials.Add(1)
		return (&net.Dialer{}).DialContext(ctx, n, direct.Listener.Addr().String())
	})
	defer tr.CloseIdleConnections()
	for _, path := range []string{"/api/probe/auth/challenge", "/api/probe/proxy/github/latest", "/api/probe/auth/challenge"} {
		req, _ := http.NewRequestWithContext(WithUpgradePriority(t.Context()), "GET", primary.URL+path, nil)
		if path != "/api/probe/auth/challenge" {
			Sign(req, "7", "secret", "fresh")
		}
		resp, err := tr.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	mu.Lock()
	if len(paths) != 3 || !s.lastDiscovery.IsZero() || dials.Load() != 1 {
		t.Fatal("cached IP/TLS connection not reused or requests duplicated", paths, dials.Load())
	}
	mu.Unlock()
	// One slow worker can cool the IP while others are still healthy. If the
	// CDN also fails, the cached IP must remain available as the final fallback.
	allowPrimary.Store(true)
	m.endpointFeedback(t.Context(), primary.URL, s.data.Endpoints[0], false)
	req, _ := http.NewRequestWithContext(WithUpgradePriority(t.Context()), "GET", primary.URL+"/api/probe/auth/challenge", nil)
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || primaryCalls.Load() != 1 || dials.Load() != 1 {
		t.Fatal("cooldown stranded upgrade on failed primary", resp.StatusCode, primaryCalls.Load(), dials.Load())
	}
}
