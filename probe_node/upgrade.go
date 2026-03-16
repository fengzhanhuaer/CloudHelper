package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

type releaseAsset struct {
	Name        string `json:"name"`
	DownloadURL string `json:"download_url"`
}

type releaseInfo struct {
	Repo    string         `json:"repo"`
	TagName string         `json:"tag_name"`
	Assets  []releaseAsset `json:"assets"`
}

var probeUpgradeState = struct {
	mu      sync.Mutex
	running bool
}{}

func runProbeUpgrade(cmd probeControlMessage, identity nodeIdentity) {
	probeUpgradeState.mu.Lock()
	if probeUpgradeState.running {
		probeUpgradeState.mu.Unlock()
		log.Printf("probe upgrade ignored: another upgrade in progress")
		return
	}
	probeUpgradeState.running = true
	probeUpgradeState.mu.Unlock()
	defer func() {
		probeUpgradeState.mu.Lock()
		probeUpgradeState.running = false
		probeUpgradeState.mu.Unlock()
	}()

	mode := strings.ToLower(strings.TrimSpace(cmd.Mode))
	if mode == "" {
		mode = "direct"
	}
	repo := strings.TrimSpace(cmd.ReleaseRepo)
	if repo == "" {
		repo = "fengzhanhuaer/CloudHelper"
	}
	controllerBase := strings.TrimSpace(cmd.ControllerBaseURL)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	release, err := fetchProbeRelease(ctx, mode, repo, controllerBase, identity)
	if err != nil {
		log.Printf("probe upgrade failed: fetch release: %v", err)
		return
	}
	if normalizeVersionTag(release.TagName) == normalizeVersionTag(BuildVersion) {
		log.Printf("probe already latest: %s", release.TagName)
		return
	}

	asset, err := pickProbeNodeAsset(release.Assets)
	if err != nil {
		log.Printf("probe upgrade failed: pick asset: %v", err)
		return
	}

	tmpDir, err := os.MkdirTemp("", "cloudhelper-probe-node-upgrade-*")
	if err != nil {
		log.Printf("probe upgrade failed: temp dir: %v", err)
		return
	}
	defer os.RemoveAll(tmpDir)

	assetFile := filepath.Join(tmpDir, filepath.Base(asset.Name))
	if err := downloadProbeAsset(ctx, mode, asset.DownloadURL, controllerBase, identity, assetFile); err != nil {
		log.Printf("probe upgrade failed: download asset: %v", err)
		return
	}

	binaryPath, err := extractProbeBinary(assetFile, asset.Name, tmpDir)
	if err != nil {
		log.Printf("probe upgrade failed: extract binary: %v", err)
		return
	}

	if err := replaceCurrentExecutable(binaryPath); err != nil {
		log.Printf("probe upgrade failed: replace executable: %v", err)
		return
	}

	log.Printf("probe upgrade complete: %s -> %s, restarting", BuildVersion, release.TagName)
	os.Exit(0)
}

func fetchProbeRelease(ctx context.Context, mode, repo, controllerBase string, identity nodeIdentity) (releaseInfo, error) {
	if mode == "proxy" {
		// Security boundary: probe can only use /api/probe/* endpoints.
		u := strings.TrimRight(controllerBase, "/") + "/api/probe/proxy/github/latest?project=" + url.QueryEscape(repo)
		body, err := probeAuthedGet(ctx, u, identity)
		if err != nil {
			return releaseInfo{}, err
		}
		var out releaseInfo
		if err := json.Unmarshal(body, &out); err != nil {
			return releaseInfo{}, err
		}
		return out, nil
	}

	apiURL := "https://api.github.com/repos/" + repo + "/releases/latest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return releaseInfo{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "cloudhelper-probe-node")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return releaseInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return releaseInfo{}, fmt.Errorf("github latest failed: %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var raw struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return releaseInfo{}, err
	}
	out := releaseInfo{Repo: repo, TagName: raw.TagName, Assets: make([]releaseAsset, 0, len(raw.Assets))}
	for _, a := range raw.Assets {
		out.Assets = append(out.Assets, releaseAsset{Name: a.Name, DownloadURL: a.URL})
	}
	return out, nil
}

func pickProbeNodeAsset(assets []releaseAsset) (releaseAsset, error) {
	goos := strings.ToLower(runtime.GOOS)
	arch := strings.ToLower(runtime.GOARCH)
	for _, a := range assets {
		n := strings.ToLower(a.Name)
		if (strings.Contains(n, "probe-node") || strings.Contains(n, "probe_node")) && strings.Contains(n, goos) && strings.Contains(n, arch) {
			return a, nil
		}
	}
	for _, a := range assets {
		n := strings.ToLower(a.Name)
		if strings.Contains(n, "probe-node") || strings.Contains(n, "probe_node") {
			return a, nil
		}
	}
	return releaseAsset{}, fmt.Errorf("matching probe_node asset not found")
}

func downloadProbeAsset(ctx context.Context, mode, assetURL, controllerBase string, identity nodeIdentity, output string) error {
	var reader io.ReadCloser
	if mode == "proxy" {
		// Security boundary: probe can only use /api/probe/* endpoints.
		u := strings.TrimRight(controllerBase, "/") + "/api/probe/proxy/download?url=" + url.QueryEscape(assetURL)
		body, err := probeAuthedGet(ctx, u, identity)
		if err != nil {
			return err
		}
		if err := os.WriteFile(output, body, 0o644); err != nil {
			return err
		}
		return nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, assetURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("User-Agent", "cloudhelper-probe-node")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		resp.Body.Close()
		return fmt.Errorf("download failed: %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	reader = resp.Body
	defer reader.Close()

	f, err := os.Create(output)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, reader)
	return err
}

func probeAuthedGet(ctx context.Context, requestURL string, identity nodeIdentity) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, err
	}
	for key, value := range buildProbeAuthHeaders(identity) {
		req.Header.Set(key, value)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("proxy request failed: %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return io.ReadAll(resp.Body)
}

func normalizeVersionTag(v string) string {
	v = strings.TrimSpace(strings.ToLower(v))
	return strings.TrimPrefix(v, "v")
}

func extractProbeBinary(assetFile, assetName, workDir string) (string, error) {
	extractDir := filepath.Join(workDir, "extract")
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		return "", err
	}
	lower := strings.ToLower(assetName)
	switch {
	case strings.HasSuffix(lower, ".tar.gz") || strings.HasSuffix(lower, ".tgz"):
		if err := extractTarGzFile(assetFile, extractDir); err != nil {
			return "", err
		}
	case strings.HasSuffix(lower, ".zip"):
		if err := extractZipFile(assetFile, extractDir); err != nil {
			return "", err
		}
	default:
		dst := filepath.Join(extractDir, filepath.Base(assetName))
		if err := copyFileWithMode(assetFile, dst, 0o755); err != nil {
			return "", err
		}
	}

	return findProbeBinary(extractDir)
}

func findProbeBinary(root string) (string, error) {
	var candidate string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		n := strings.ToLower(filepath.Base(path))
		if strings.HasSuffix(n, ".exe") {
			return nil
		}
		if strings.Contains(n, "probe_node") || strings.Contains(n, "probe-node") {
			candidate = path
			return io.EOF
		}
		return nil
	})
	if err != nil && err != io.EOF {
		return "", err
	}
	if strings.TrimSpace(candidate) == "" {
		return "", fmt.Errorf("probe binary not found")
	}
	_ = os.Chmod(candidate, 0o755)
	return candidate, nil
}

func replaceCurrentExecutable(newBinary string) error {
	exePath, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(exePath); err == nil && strings.TrimSpace(resolved) != "" {
		exePath = resolved
	}
	mode := fs.FileMode(0o755)
	if st, err := os.Stat(exePath); err == nil {
		mode = st.Mode().Perm()
	}
	tmp := exePath + ".new"
	if err := copyFileWithMode(newBinary, tmp, mode); err != nil {
		return err
	}
	backup := exePath + ".bak"
	_ = os.Remove(backup)
	if err := os.Rename(exePath, backup); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, exePath); err != nil {
		_ = os.Rename(backup, exePath)
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func copyFileWithMode(src, dst string, mode fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func extractTarGzFile(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		target := filepath.Join(dst, filepath.Clean(h.Name))
		if h.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, tr); err != nil {
			out.Close()
			return err
		}
		out.Close()
	}
}

func extractZipFile(src, dst string) error {
	zr, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, f := range zr.File {
		target := filepath.Join(dst, filepath.Clean(f.Name))
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			rc.Close()
			return err
		}
		if _, err := io.Copy(out, rc); err != nil {
			rc.Close()
			out.Close()
			return err
		}
		rc.Close()
		out.Close()
	}
	return nil
}
