// Package upgrade implements GitHub release querying and manager upgrade capability.
// Migrated from probe_manager/backend/upgrade.go (read-only metadata path only).
// Actual binary replacement is deferred to probe_manager for W2;
// this package exposes the release check and triggers upgrade via IPC in W3.
// PKG-W2-04 / RQ-004
package upgrade

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"time"
)

const (
	defaultRepo   = "fengzhanhuaer/CloudHelper"
	userAgent     = "cloudhelper-manager-service"
	githubTimeout = 20 * time.Second
)

// ReleaseAsset represents a GitHub release asset.
type ReleaseAsset struct {
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	DownloadURL string `json:"download_url"`
}

// ReleaseInfo is the normalised release descriptor returned to callers.
type ReleaseInfo struct {
	Repo        string         `json:"repo"`
	TagName     string         `json:"tag_name"`
	ReleaseName string         `json:"release_name,omitempty"`
	HTMLURL     string         `json:"html_url,omitempty"`
	PublishedAt string         `json:"published_at,omitempty"`
	Assets      []ReleaseAsset `json:"assets"`
}

type githubReleaseAsset struct {
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type githubRelease struct {
	TagName     string               `json:"tag_name"`
	Name        string               `json:"name"`
	HTMLURL     string               `json:"html_url"`
	PublishedAt string               `json:"published_at"`
	Assets      []githubReleaseAsset `json:"assets"`
}

// GetLatestRelease fetches the latest release info for the given GitHub project.
func GetLatestRelease(ctx context.Context, project string) (ReleaseInfo, error) {
	repo, err := normalizeRepo(project)
	if err != nil {
		return ReleaseInfo{}, err
	}
	release, err := fetchLatest(ctx, repo)
	if err != nil {
		return ReleaseInfo{}, err
	}
	return toReleaseInfo(repo, release), nil
}

// PickAsset selects the most suitable release asset for the current OS/arch.
// Env CLOUDHELPER_MANAGER_ASSET_NAME overrides auto-selection.
func PickAsset(assets []ReleaseAsset) (ReleaseAsset, error) {
	if len(assets) == 0 {
		return ReleaseAsset{}, fmt.Errorf("no release assets found")
	}
	if exact := strings.TrimSpace(os.Getenv("CLOUDHELPER_MANAGER_ASSET_NAME")); exact != "" {
		for _, a := range assets {
			if strings.EqualFold(strings.TrimSpace(a.Name), exact) {
				return a, nil
			}
		}
		return ReleaseAsset{}, fmt.Errorf("asset %q not found", exact)
	}
	goos := strings.ToLower(runtime.GOOS)
	archKeys := archCandidates()
	isManager := func(name string) bool {
		n := strings.ToLower(name)
		return strings.Contains(n, "probe-manager") || strings.Contains(n, "probe_manager")
	}
	containsAny := func(s string, keys []string) bool {
		for _, k := range keys {
			if strings.Contains(s, k) {
				return true
			}
		}
		return false
	}
	// Best match: manager asset + goos + arch.
	for _, a := range assets {
		n := strings.ToLower(a.Name)
		if isManager(n) && strings.Contains(n, goos) && containsAny(n, archKeys) {
			return a, nil
		}
	}
	for _, a := range assets {
		n := strings.ToLower(a.Name)
		if isManager(n) && strings.Contains(n, goos) {
			return a, nil
		}
	}
	for _, a := range assets {
		if isManager(a.Name) {
			return a, nil
		}
	}
	for _, a := range assets {
		n := strings.ToLower(a.Name)
		if strings.Contains(n, goos) && containsAny(n, archKeys) {
			return a, nil
		}
	}
	if len(assets) == 1 {
		return assets[0], nil
	}
	return ReleaseAsset{}, fmt.Errorf("no matching asset found")
}

func archCandidates() []string {
	keys := []string{strings.ToLower(runtime.GOARCH)}
	switch runtime.GOARCH {
	case "amd64":
		keys = append(keys, "x86_64")
	case "386":
		keys = append(keys, "x86", "i386")
	case "arm64":
		keys = append(keys, "aarch64")
	}
	return keys
}

func fetchLatest(ctx context.Context, repo string) (githubRelease, error) {
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return githubRelease{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", userAgent)
	if token := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: githubTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return githubRelease{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return githubRelease{}, fmt.Errorf("github api status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var release githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return githubRelease{}, err
	}
	return release, nil
}

func toReleaseInfo(repo string, r githubRelease) ReleaseInfo {
	assets := make([]ReleaseAsset, 0, len(r.Assets))
	for _, a := range r.Assets {
		assets = append(assets, ReleaseAsset{
			Name:        a.Name,
			Size:        a.Size,
			DownloadURL: a.BrowserDownloadURL,
		})
	}
	return ReleaseInfo{
		Repo:        repo,
		TagName:     strings.TrimSpace(r.TagName),
		ReleaseName: strings.TrimSpace(r.Name),
		HTMLURL:     strings.TrimSpace(r.HTMLURL),
		PublishedAt: strings.TrimSpace(r.PublishedAt),
		Assets:      assets,
	}
}

func normalizeRepo(project string) (string, error) {
	p := strings.TrimSpace(project)
	if p == "" {
		if env := strings.TrimSpace(os.Getenv("CLOUDHELPER_MANAGER_RELEASE_REPO")); env != "" {
			p = env
		} else {
			p = defaultRepo
		}
	}
	if strings.Contains(p, "github.com") {
		u, err := url.Parse(p)
		if err != nil {
			return "", fmt.Errorf("invalid github project url")
		}
		pathPart := strings.Trim(strings.TrimSuffix(u.Path, ".git"), "/")
		parts := strings.SplitN(pathPart, "/", 3)
		if len(parts) < 2 {
			return "", fmt.Errorf("project url must include owner/repo")
		}
		return parts[0] + "/" + parts[1], nil
	}
	parts := strings.SplitN(strings.Trim(p, "/"), "/", 3)
	if len(parts) < 2 {
		return "", fmt.Errorf("project must be owner/repo or github url")
	}
	return parts[0] + "/" + parts[1], nil
}
