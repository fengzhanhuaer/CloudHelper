package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
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

type runtimePlatformInfo struct {
	GOOS     string
	GOARCH   string
	IsAlpine bool
	IsMusl   bool
	Libc     string
}

type scoredProbeAsset struct {
	Asset releaseAsset
	Score int
}

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
	platform := detectRuntimePlatformInfo()
	log.Printf(
		"probe upgrade started: current=%s mode=%s repo=%s goos=%s goarch=%s libc=%s alpine=%t controller=%s",
		BuildVersion,
		mode,
		repo,
		platform.GOOS,
		platform.GOARCH,
		platform.Libc,
		platform.IsAlpine,
		controllerBase,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	release, err := fetchProbeRelease(ctx, mode, repo, controllerBase, identity)
	if err != nil {
		log.Printf("probe upgrade failed: fetch release: %v", err)
		return
	}
	log.Printf(
		"probe upgrade release fetched: latest=%s assets=%d names=[%s]",
		strings.TrimSpace(release.TagName),
		len(release.Assets),
		summarizeAssetNames(release.Assets, 12),
	)
	if normalizeVersionTag(release.TagName) == normalizeVersionTag(BuildVersion) {
		log.Printf("probe already latest: %s", release.TagName)
		return
	}

	asset, err := pickProbeNodeAsset(release.Assets, platform)
	if err != nil {
		log.Printf("probe upgrade failed: pick asset: %v", err)
		return
	}
	log.Printf("probe upgrade asset selected: name=%s", strings.TrimSpace(asset.Name))

	tmpDir, err := os.MkdirTemp("", "cloudhelper-probe-node-upgrade-*")
	if err != nil {
		log.Printf("probe upgrade failed: temp dir: %v", err)
		return
	}
	defer os.RemoveAll(tmpDir)

	assetFile := filepath.Join(tmpDir, filepath.Base(asset.Name))
	log.Printf("probe upgrade download: target=%s mode=%s", assetFile, mode)
	if err := downloadProbeAsset(ctx, mode, asset.DownloadURL, controllerBase, identity, assetFile); err != nil {
		log.Printf("probe upgrade failed: download asset: %v", err)
		return
	}
	if st, err := os.Stat(assetFile); err == nil {
		log.Printf("probe upgrade download complete: file=%s size=%d", assetFile, st.Size())
	}

	binaryPath, err := extractProbeBinary(assetFile, asset.Name, tmpDir)
	if err != nil {
		log.Printf("probe upgrade failed: extract binary: %v", err)
		return
	}
	log.Printf("probe upgrade extract complete: binary=%s", binaryPath)
	if err := verifyBinaryCompatibility(binaryPath, platform); err != nil {
		log.Printf("probe upgrade failed: compatibility check: %v", err)
		return
	}

	exePath, backupPath, err := replaceCurrentExecutable(binaryPath)
	if err != nil {
		log.Printf("probe upgrade failed: replace executable: %v", err)
		return
	}
	log.Printf("probe upgrade replace complete: exe=%s backup=%s", exePath, backupPath)

	log.Printf("probe upgrade complete: %s -> %s, restarting", BuildVersion, release.TagName)
	if err := restartCurrentProcess(exePath); err != nil {
		log.Printf("probe upgrade restart failed: %v", err)
		if rollbackErr := rollbackExecutable(exePath, backupPath); rollbackErr != nil {
			log.Printf("probe upgrade rollback failed: %v", rollbackErr)
			return
		}
		log.Printf("probe upgrade rollback complete, old binary restored")
	}
}

func fetchProbeRelease(ctx context.Context, mode, repo, controllerBase string, identity nodeIdentity) (releaseInfo, error) {
	if mode == "proxy" {
		// Security boundary: probe can only use /api/probe/* endpoints.
		u := strings.TrimRight(controllerBase, "/") + "/api/probe/proxy/github/latest?project=" + url.QueryEscape(repo)
		log.Printf("probe upgrade release fetch via proxy: %s", safeURLForLog(u))
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
	log.Printf("probe upgrade release fetch direct: %s", safeURLForLog(apiURL))
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

func pickProbeNodeAsset(assets []releaseAsset, platform runtimePlatformInfo) (releaseAsset, error) {
	probeAssets := make([]releaseAsset, 0, len(assets))
	for _, a := range assets {
		n := strings.ToLower(strings.TrimSpace(a.Name))
		if strings.Contains(n, "probe-node") || strings.Contains(n, "probe_node") {
			probeAssets = append(probeAssets, a)
		}
	}
	if len(probeAssets) == 0 {
		return releaseAsset{}, fmt.Errorf("matching probe_node asset not found, release assets=[%s]", summarizeAssetNames(assets, 20))
	}

	// Keep selection aligned with GitHub Action artifact naming:
	// cloudhelper-probe-node-<goos>-<goarch>
	preferredPrefix := "cloudhelper-probe-node-" + strings.ToLower(strings.TrimSpace(platform.GOOS)) + "-" + strings.ToLower(strings.TrimSpace(platform.GOARCH))
	prefixMatched := make([]releaseAsset, 0, len(probeAssets))
	for _, a := range probeAssets {
		name := strings.ToLower(strings.TrimSpace(a.Name))
		if strings.HasPrefix(name, preferredPrefix) {
			prefixMatched = append(prefixMatched, a)
		}
	}
	if len(prefixMatched) > 0 {
		sort.Slice(prefixMatched, func(i, j int) bool {
			ni := strings.ToLower(strings.TrimSpace(prefixMatched[i].Name))
			nj := strings.ToLower(strings.TrimSpace(prefixMatched[j].Name))
			if len(ni) == len(nj) {
				return ni < nj
			}
			return len(ni) < len(nj)
		})
		selected := prefixMatched[0]
		log.Printf("probe upgrade asset selected by workflow prefix: name=%s prefix=%s", strings.TrimSpace(selected.Name), preferredPrefix)
		return selected, nil
	}

	scored := make([]scoredProbeAsset, 0, len(probeAssets))
	for _, a := range probeAssets {
		n := strings.ToLower(strings.TrimSpace(a.Name))
		score := 0

		if assetMatchesOS(n, platform.GOOS) {
			score += 40
		} else if platform.GOOS == "linux" && !assetLooksWindows(n) {
			// Linux and Alpine share the same upgrade package in current release flow.
			// Keep a weak fallback for Linux-like assets that omit explicit "linux".
			score += 8
		}

		if assetMatchesArch(n, platform.GOARCH) {
			score += 40
		}

		if assetLooksWindows(n) {
			score -= 100
		}

		scored = append(scored, scoredProbeAsset{Asset: a, Score: score})
	}

	sort.Slice(scored, func(i, j int) bool {
		if scored[i].Score == scored[j].Score {
			return strings.ToLower(scored[i].Asset.Name) < strings.ToLower(scored[j].Asset.Name)
		}
		return scored[i].Score > scored[j].Score
	})

	if len(scored) == 0 || scored[0].Score < 0 {
		return releaseAsset{}, fmt.Errorf("matching probe_node asset not found for goos=%s goarch=%s libc=%s, probe assets=[%s]", platform.GOOS, platform.GOARCH, platform.Libc, summarizeAssetNames(probeAssets, 20))
	}

	top := scored[0]
	log.Printf(
		"probe upgrade asset scoring: selected=%s score=%d goos=%s goarch=%s libc=%s top=[%s]",
		strings.TrimSpace(top.Asset.Name),
		top.Score,
		platform.GOOS,
		platform.GOARCH,
		platform.Libc,
		summarizeScoredAssets(scored, 5),
	)
	return top.Asset, nil
}

func downloadProbeAsset(ctx context.Context, mode, assetURL, controllerBase string, identity nodeIdentity, output string) error {
	var reader io.ReadCloser
	if mode == "proxy" {
		// Security boundary: probe can only use /api/probe/* endpoints.
		u := strings.TrimRight(controllerBase, "/") + "/api/probe/proxy/download?url=" + url.QueryEscape(assetURL)
		log.Printf("probe upgrade asset download via proxy: %s", safeURLForLog(u))
		body, err := probeAuthedGet(ctx, u, identity)
		if err != nil {
			return err
		}
		if err := os.WriteFile(output, body, 0o644); err != nil {
			return err
		}
		return nil
	}

	log.Printf("probe upgrade asset download direct: %s", safeURLForLog(assetURL))
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
	allowExe := strings.EqualFold(runtime.GOOS, "windows")
	candidates := make([]string, 0, 8)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		n := strings.ToLower(filepath.Base(path))
		if strings.HasSuffix(n, ".exe") && !allowExe {
			return nil
		}
		if strings.Contains(n, "probe_node") || strings.Contains(n, "probe-node") {
			candidates = append(candidates, path)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(candidates) == 0 {
		return "", fmt.Errorf("probe binary not found")
	}

	sort.Slice(candidates, func(i, j int) bool {
		li := strings.ToLower(filepath.Base(candidates[i]))
		lj := strings.ToLower(filepath.Base(candidates[j]))
		iExe := strings.HasSuffix(li, ".exe")
		jExe := strings.HasSuffix(lj, ".exe")
		if allowExe {
			if iExe != jExe {
				return iExe
			}
		} else {
			if iExe != jExe {
				return !iExe
			}
		}
		if len(li) == len(lj) {
			return li < lj
		}
		return len(li) < len(lj)
	})

	candidate := candidates[0]
	_ = os.Chmod(candidate, 0o755)
	return candidate, nil
}

func replaceCurrentExecutable(newBinary string) (string, string, error) {
	exePath, err := os.Executable()
	if err != nil {
		return "", "", err
	}
	if resolved, err := filepath.EvalSymlinks(exePath); err == nil && strings.TrimSpace(resolved) != "" {
		exePath = resolved
	}
	targetPath := normalizeExecutablePathForUpgradeTarget(exePath)
	if strings.TrimSpace(targetPath) == "" {
		return "", "", fmt.Errorf("resolved executable path is empty")
	}

	mode := fs.FileMode(0o755)
	if st, err := os.Stat(targetPath); err == nil {
		mode = st.Mode().Perm()
	} else if st, err := os.Stat(exePath); err == nil {
		mode = st.Mode().Perm()
	}
	tmp := targetPath + ".new"
	if err := copyFileWithMode(newBinary, tmp, mode); err != nil {
		return "", "", err
	}
	backup := targetPath + ".bak"
	_ = os.Remove(backup)
	if err := os.Rename(targetPath, backup); err != nil {
		_ = os.Remove(tmp)
		return "", "", err
	}
	if err := os.Rename(tmp, targetPath); err != nil {
		_ = os.Rename(backup, targetPath)
		_ = os.Remove(tmp)
		return "", "", err
	}
	return targetPath, backup, nil
}

func normalizeExecutablePathForUpgradeTarget(exePath string) string {
	cleaned := strings.TrimSpace(exePath)
	if cleaned == "" {
		return ""
	}

	lowered := strings.ToLower(cleaned)
	for strings.HasSuffix(lowered, ".bak") {
		cleaned = cleaned[:len(cleaned)-len(".bak")]
		lowered = strings.ToLower(cleaned)
	}
	return cleaned
}

func rollbackExecutable(exePath, backupPath string) error {
	exePath = strings.TrimSpace(exePath)
	backupPath = strings.TrimSpace(backupPath)
	if exePath == "" || backupPath == "" {
		return fmt.Errorf("rollback path is empty")
	}

	if _, err := os.Stat(backupPath); err != nil {
		return fmt.Errorf("backup binary not found: %w", err)
	}

	failed := exePath + ".failed-" + time.Now().UTC().Format("20060102T150405")
	if _, err := os.Stat(exePath); err == nil {
		if renameErr := os.Rename(exePath, failed); renameErr != nil {
			return fmt.Errorf("rename current binary to failed file: %w", renameErr)
		}
	}
	if err := os.Rename(backupPath, exePath); err != nil {
		return fmt.Errorf("restore backup binary failed: %w", err)
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

func detectRuntimePlatformInfo() runtimePlatformInfo {
	goos := strings.ToLower(strings.TrimSpace(runtime.GOOS))
	goarch := strings.ToLower(strings.TrimSpace(runtime.GOARCH))
	info := runtimePlatformInfo{
		GOOS:   goos,
		GOARCH: goarch,
		Libc:   "unknown",
	}
	if goos != "linux" {
		return info
	}

	if _, err := os.Stat("/etc/alpine-release"); err == nil {
		info.IsAlpine = true
	}

	if isLinuxMuslRuntime() {
		info.IsMusl = true
		info.Libc = "musl"
		return info
	}
	info.Libc = "glibc-or-static"
	return info
}

func isLinuxMuslRuntime() bool {
	if _, err := os.Stat("/etc/alpine-release"); err == nil {
		return true
	}
	if matches, _ := filepath.Glob("/lib/ld-musl-*.so.1"); len(matches) > 0 {
		return true
	}
	lddPath, err := exec.LookPath("ldd")
	if err != nil {
		return false
	}
	out, err := exec.Command(lddPath, "--version").CombinedOutput()
	text := strings.ToLower(string(out))
	if strings.Contains(text, "musl") {
		return true
	}
	if err != nil && strings.Contains(text, "musl") {
		return true
	}
	return false
}

func verifyBinaryCompatibility(binaryPath string, platform runtimePlatformInfo) error {
	if platform.GOOS != "linux" {
		return nil
	}
	lddPath, err := exec.LookPath("ldd")
	if err != nil {
		log.Printf("probe upgrade compatibility check skipped: ldd not found")
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, lddPath, binaryPath)
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	lower := strings.ToLower(text)

	if text != "" {
		log.Printf("probe upgrade ldd output: %s", strings.ReplaceAll(text, "\n", " | "))
	}
	if err == nil {
		if strings.Contains(lower, "not found") || strings.Contains(lower, "error loading shared library") {
			return fmt.Errorf("ldd reports missing library: %s", text)
		}
		return nil
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("ldd check timeout")
	}
	if strings.Contains(lower, "not a dynamic executable") || strings.Contains(lower, "statically linked") {
		return nil
	}
	if strings.Contains(lower, "not found") || strings.Contains(lower, "error loading shared library") || strings.Contains(lower, "no such file or directory") {
		return fmt.Errorf("ldd compatibility check failed: %s", text)
	}
	log.Printf("probe upgrade compatibility check warning: ldd exited with error=%v", err)
	return nil
}

func summarizeAssetNames(assets []releaseAsset, limit int) string {
	if len(assets) == 0 {
		return ""
	}
	if limit <= 0 {
		limit = len(assets)
	}
	max := len(assets)
	if max > limit {
		max = limit
	}
	names := make([]string, 0, max+1)
	for i := 0; i < max; i++ {
		names = append(names, strings.TrimSpace(assets[i].Name))
	}
	if len(assets) > max {
		names = append(names, fmt.Sprintf("...+%d", len(assets)-max))
	}
	return strings.Join(names, ", ")
}

func summarizeScoredAssets(items []scoredProbeAsset, limit int) string {
	if len(items) == 0 {
		return ""
	}
	if limit <= 0 {
		limit = len(items)
	}
	max := len(items)
	if max > limit {
		max = limit
	}
	out := make([]string, 0, max+1)
	for i := 0; i < max; i++ {
		out = append(out, fmt.Sprintf("%s(%d)", strings.TrimSpace(items[i].Asset.Name), items[i].Score))
	}
	if len(items) > max {
		out = append(out, fmt.Sprintf("...+%d", len(items)-max))
	}
	return strings.Join(out, ", ")
}

func assetMatchesOS(name, goos string) bool {
	goos = strings.ToLower(strings.TrimSpace(goos))
	name = strings.ToLower(strings.TrimSpace(name))
	if goos == "" || name == "" {
		return false
	}
	return strings.Contains(name, goos)
}

func assetMatchesArch(name, goarch string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, token := range archTokens(goarch) {
		if strings.Contains(name, token) {
			return true
		}
	}
	return false
}

func archTokens(goarch string) []string {
	switch strings.ToLower(strings.TrimSpace(goarch)) {
	case "amd64":
		return []string{"amd64", "x86_64", "x64"}
	case "arm64":
		return []string{"arm64", "aarch64"}
	case "arm":
		return []string{"armv7", "armv7l", "arm"}
	case "386":
		return []string{"386", "i386", "x86"}
	default:
		v := strings.ToLower(strings.TrimSpace(goarch))
		if v == "" {
			return nil
		}
		return []string{v}
	}
}

func assetLooksWindows(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	return strings.Contains(n, "windows") || strings.HasSuffix(n, ".exe")
}

func safeURLForLog(raw string) string {
	v := strings.TrimSpace(raw)
	if v == "" {
		return ""
	}
	parsed, err := url.Parse(v)
	if err != nil {
		return v
	}
	out := parsed.Scheme + "://" + parsed.Host + parsed.Path
	if strings.TrimSpace(parsed.RawQuery) != "" {
		out += "?..."
	}
	return out
}
