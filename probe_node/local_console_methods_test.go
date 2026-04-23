package main

import (
	"net/http"
	"testing"
)

func TestProbeLocalAPIMethodGuards(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	cases := []struct {
		name   string
		method string
		path   string
		body   any
	}{
		{name: "auth session should only allow GET", method: http.MethodPost, path: "/local/api/auth/session", body: map[string]any{}},
		{name: "tun status should only allow GET", method: http.MethodPost, path: "/local/api/tun/status", body: map[string]any{}},
		{name: "tun install should only allow POST", method: http.MethodGet, path: "/local/api/tun/install", body: nil},
		{name: "proxy enable should only allow POST", method: http.MethodGet, path: "/local/api/proxy/enable", body: nil},
		{name: "proxy direct should only allow POST", method: http.MethodGet, path: "/local/api/proxy/direct", body: nil},
		{name: "proxy status should only allow GET", method: http.MethodPost, path: "/local/api/proxy/status", body: map[string]any{}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := doProbeLocalRequest(t, mux, tc.method, tc.path, tc.body, sessionCookie)
			if resp.Code != http.StatusMethodNotAllowed {
				t.Fatalf("%s %s status=%d body=%s", tc.method, tc.path, resp.Code, resp.Body.String())
			}
		})
	}
}
