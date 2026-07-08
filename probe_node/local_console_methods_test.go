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
		{name: "virtual router settings should reject PUT", method: http.MethodPut, path: "/local/api/virtual_router/settings", body: map[string]any{}},
		{name: "virtual router packets should only allow GET", method: http.MethodPost, path: "/local/api/virtual_router/packets", body: map[string]any{}},
		{name: "virtual router route test should reject PUT", method: http.MethodPut, path: "/local/api/virtual_router/route_test", body: map[string]any{}},
		{name: "virtual router route curl test should reject PUT", method: http.MethodPut, path: "/local/api/virtual_router/route_test/curl", body: map[string]any{}},
		{name: "virtual router route speed test should reject PUT", method: http.MethodPut, path: "/local/api/virtual_router/route_test/speed", body: map[string]any{}},
		{name: "virtual router debug logs should reject PUT", method: http.MethodPut, path: "/local/api/virtual_router/debug/logs", body: map[string]any{}},
		{name: "tun install should only allow POST", method: http.MethodGet, path: "/local/api/tun/install", body: nil},
		{name: "tun reset should only allow POST", method: http.MethodGet, path: "/local/api/tun/reset", body: nil},
		{name: "tun uninstall should only allow POST", method: http.MethodGet, path: "/local/api/tun/uninstall", body: nil},
		{name: "system upgrade should only allow POST", method: http.MethodGet, path: "/local/api/system/upgrade", body: nil},
		{name: "system upgrade check should only allow POST", method: http.MethodGet, path: "/local/api/system/upgrade/check", body: nil},
		{name: "system upgrade status should only allow GET", method: http.MethodPost, path: "/local/api/system/upgrade/status", body: map[string]any{}},
		{name: "system restart should only allow POST", method: http.MethodGet, path: "/local/api/system/restart", body: nil},
		{name: "system ip report settings should reject put", method: http.MethodPut, path: "/local/api/system/ip_report_settings", body: map[string]any{}},
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
