package core

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
)

const probeProxyDownloadMaxRedirects = 10

var probeProxyDownloadAllowedHosts = map[string]struct{}{
	"api.github.com":                        {},
	"github.com":                            {},
	"github-releases.githubusercontent.com": {},
	"objects.githubusercontent.com":         {},
	"release-assets.githubusercontent.com":  {},
}

func parseProbeProxyDownloadURL(raw string) (*url.URL, error) {
	target, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || target == nil {
		return nil, fmt.Errorf("invalid download url")
	}
	if !strings.EqualFold(strings.TrimSpace(target.Scheme), "https") || target.User != nil {
		return nil, fmt.Errorf("invalid download url")
	}
	host := strings.ToLower(strings.TrimSpace(target.Hostname()))
	if _, ok := probeProxyDownloadAllowedHosts[host]; !ok {
		return nil, fmt.Errorf("download host is not allowed")
	}
	if port := strings.TrimSpace(target.Port()); port != "" && port != "443" {
		return nil, fmt.Errorf("download port is not allowed")
	}
	return target, nil
}

func newProbeProxyDownloadRequest(ctx context.Context, rawURL string) (*http.Request, error) {
	target, err := parseProbeProxyDownloadURL(rawURL)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, err
	}
	applyProbeProxyDownloadAuthorization(req)
	return req, nil
}

func applyProbeProxyDownloadAuthorization(req *http.Request) {
	if req == nil || req.URL == nil {
		return
	}
	req.Header.Del("Authorization")
	if !strings.EqualFold(strings.TrimSpace(req.URL.Hostname()), "api.github.com") {
		return
	}
	if token := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
}

func doProbeProxyDownload(req *http.Request) (*http.Response, error) {
	if req == nil || req.URL == nil {
		return nil, fmt.Errorf("download request is required")
	}
	if _, err := parseProbeProxyDownloadURL(req.URL.String()); err != nil {
		return nil, err
	}
	applyProbeProxyDownloadAuthorization(req)
	base := http.DefaultClient
	if base == nil {
		base = &http.Client{}
	}
	client := *base
	client.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if len(via) >= probeProxyDownloadMaxRedirects {
			return fmt.Errorf("too many download redirects")
		}
		if next == nil || next.URL == nil {
			return fmt.Errorf("invalid download redirect")
		}
		if _, err := parseProbeProxyDownloadURL(next.URL.String()); err != nil {
			return err
		}
		applyProbeProxyDownloadAuthorization(next)
		return nil
	}
	return client.Do(req)
}
