package core

import (
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestDirectProbeIsolationAndNonce(t *testing.T) {
	withProbeStoreSecretForTest(t, "1", "test-secret")
	reached := 0
	handler := probeOnlyDirectHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached++
		if r.URL.Path != "/api/probe/auth/challenge" {
			if _, err := authenticateProbeRequest(r); err != nil {
				t.Error("inner handler consumed nonce twice", err)
			}
		}
		w.WriteHeader(204)
	}))
	for _, path := range []string{"/", "/mng", "/api/login", "/api/probe/controller/endpoints", "/api/probe/../login", "/api/probe/unknown"} {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("CF-Connecting-IP", "127.0.0.1")
		req.Header.Set("X-Forwarded-Proto", "https")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != 404 {
			t.Fatalf("%s status %d", path, w.Code)
		}
	}
	req := httptest.NewRequest("GET", "/api/probe/route/config", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
	applyProbeChallengeAuthForTest(t, req, "1", "test-secret")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != 204 || reached != 1 {
		t.Fatalf("authenticated request failed %d reached=%d", w.Code, reached)
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatal("nonce replay accepted", w.Code)
	}
}
func TestDirectIdentityPersistsAndFiltersIPs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.pem")
	a, pin, err := controllerDirectCertificate(path)
	if err != nil {
		t.Fatal(err)
	}
	b, pin2, err := controllerDirectCertificate(path)
	if err != nil || pin != pin2 || string(a.Certificate[0]) != string(b.Certificate[0]) {
		t.Fatal("identity rotated", err)
	}
	for _, raw := range []string{"127.0.0.1", "192.168.50.1", "198.18.0.1", "100.64.0.1", "::1", "2001:db8::1"} {
		if controllerDirectPublicIP(net.ParseIP(raw)) {
			t.Fatal("non-public address", raw)
		}
	}
	if !controllerDirectPublicIP(net.ParseIP("94.16.111.150")) {
		t.Fatal("public origin rejected")
	}
}

func TestControllerEndpointDiscoveryRequiresPrivateAuthentication(t *testing.T) {
	withProbeStoreSecretForTest(t, "1", "secret")
	req := httptest.NewRequest("GET", "/api/probe/controller/endpoints", nil)
	w := httptest.NewRecorder()
	ProbeControllerEndpointsHandler(w, req)
	if w.Code != 401 {
		t.Fatal("unauthed discovery", w.Code)
	}
	applyProbeChallengeAuthForTest(t, req, "1", "secret")
	w = httptest.NewRecorder()
	ProbeControllerEndpointsHandler(w, req)
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Code, w.Body.String())
	}
}
