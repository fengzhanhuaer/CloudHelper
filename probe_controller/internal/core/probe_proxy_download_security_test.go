package core

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
)

func TestParseProbeProxyDownloadURLAllowlist(t *testing.T) {
	for _, raw := range []string{
		"https://github.com/org/repo/releases/download/v1/asset",
		"https://api.github.com/repos/org/repo/releases/assets/1",
		"https://release-assets.githubusercontent.com/asset",
	} {
		if _, err := parseProbeProxyDownloadURL(raw); err != nil {
			t.Fatalf("expected allowed URL %q: %v", raw, err)
		}
	}
	for _, raw := range []string{
		"http://github.com/asset",
		"https://github.com.evil.example/asset",
		"https://127.0.0.1/asset",
		"https://github.com:8443/asset",
		"https://user:pass@github.com/asset",
	} {
		if _, err := parseProbeProxyDownloadURL(raw); err == nil {
			t.Fatalf("expected URL rejection: %q", raw)
		}
	}
}

func TestProbeProxyDownloadAuthorizationOnlyTargetsGitHubAPI(t *testing.T) {
	old := os.Getenv("GITHUB_TOKEN")
	_ = os.Setenv("GITHUB_TOKEN", "top-secret")
	t.Cleanup(func() { _ = os.Setenv("GITHUB_TOKEN", old) })

	apiReq, err := newProbeProxyDownloadRequest(context.Background(), "https://api.github.com/repos/a/b/releases/assets/1")
	if err != nil {
		t.Fatal(err)
	}
	if got := apiReq.Header.Get("Authorization"); got != "Bearer top-secret" {
		t.Fatalf("expected API authorization, got %q", got)
	}
	assetReq, err := newProbeProxyDownloadRequest(context.Background(), "https://github.com/a/b/releases/download/v1/asset")
	if err != nil {
		t.Fatal(err)
	}
	if got := assetReq.Header.Get("Authorization"); got != "" {
		t.Fatalf("token leaked to non-API host: %q", got)
	}
}

func TestProbeProxyDownloadRejectsOffAllowlistRedirect(t *testing.T) {
	oldClient := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: probeProxyRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusFound,
			Header:     http.Header{"Location": {"https://attacker.example/steal"}},
			Body:       http.NoBody,
			Request:    req,
		}, nil
	})}
	t.Cleanup(func() { http.DefaultClient = oldClient })

	req, err := newProbeProxyDownloadRequest(context.Background(), "https://github.com/a/b/releases/download/v1/asset")
	if err != nil {
		t.Fatal(err)
	}
	_, err = doProbeProxyDownload(req)
	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("expected off-allowlist redirect rejection, got %v", err)
	}
}
