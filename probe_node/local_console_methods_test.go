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
		{name: "logs should only allow GET", method: http.MethodPost, path: "/local/api/logs", body: map[string]any{}},
		{name: "dns status should only allow GET", method: http.MethodPost, path: "/local/api/dns/status", body: map[string]any{}},
		{name: "dns real ip list should only allow GET", method: http.MethodPost, path: "/local/api/dns/real_ip/list", body: map[string]any{}},
		{name: "dns real ip lookup should only allow GET", method: http.MethodPost, path: "/local/api/dns/real_ip/lookup", body: map[string]any{}},
		{name: "dns fake ip list should only allow GET", method: http.MethodPost, path: "/local/api/dns/fake_ip/list", body: map[string]any{}},
		{name: "dns fake ip lookup should only allow GET", method: http.MethodPost, path: "/local/api/dns/fake_ip/lookup", body: map[string]any{}},
		{name: "tun install should only allow POST", method: http.MethodGet, path: "/local/api/tun/install", body: nil},
		{name: "proxy enable should only allow POST", method: http.MethodGet, path: "/local/api/proxy/enable", body: nil},
		{name: "proxy direct should only allow POST", method: http.MethodGet, path: "/local/api/proxy/direct", body: nil},
		{name: "proxy reject should only allow POST", method: http.MethodGet, path: "/local/api/proxy/reject", body: nil},
		{name: "system upgrade should only allow POST", method: http.MethodGet, path: "/local/api/system/upgrade", body: nil},
		{name: "system upgrade status should only allow GET", method: http.MethodPost, path: "/local/api/system/upgrade/status", body: map[string]any{}},
		{name: "system restart should only allow POST", method: http.MethodGet, path: "/local/api/system/restart", body: nil},
		{name: "proxy status should only allow GET", method: http.MethodPost, path: "/local/api/proxy/status", body: map[string]any{}},
		{name: "proxy select should only allow POST", method: http.MethodGet, path: "/local/api/proxy/select", body: nil},
		{name: "proxy chains should only allow GET", method: http.MethodPost, path: "/local/api/proxy/chains", body: map[string]any{}},
		{name: "proxy groups should only allow GET", method: http.MethodPost, path: "/local/api/proxy/groups", body: map[string]any{}},
		{name: "proxy groups save should only allow POST", method: http.MethodGet, path: "/local/api/proxy/groups/save", body: nil},
		{name: "proxy state should only allow GET", method: http.MethodPost, path: "/local/api/proxy/state", body: map[string]any{}},
		{name: "proxy hosts should only allow GET", method: http.MethodPost, path: "/local/api/proxy/hosts", body: map[string]any{}},
		{name: "proxy hosts save should only allow POST", method: http.MethodGet, path: "/local/api/proxy/hosts/save", body: nil},
		{name: "proxy groups backup should only allow POST", method: http.MethodGet, path: "/local/api/proxy/groups/backup", body: nil},
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
