package core

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
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"time"
)

const (
	controllerVersionStoreField = "controller_version"
	defaultReleaseRepo          = "fengzhanhuaer/CloudHelper"
)

var BuildVersion = "dev"

type adminVersionResponse struct {
	CurrentVersion   string `json:"current_version"`
	LatestVersion    string `json:"latest_version,omitempty"`
	ReleaseRepo      string `json:"release_repo"`
	UpgradeAvailable bool   `json:"upgrade_available"`
	Message          string `json:"message,omitempty"`
}

type adminUpgradeResponse struct {
	CurrentVersion string `json:"current_version"`
	LatestVersion  string `json:"latest_version"`
	Updated        bool   `json:"updated"`
	AssetName      string `json:"asset_name,omitempty"`
	Message        string `json:"message"`
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

func AdminVersionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	token, err := extractBearerToken(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	if !IsTokenValid(token) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid or expired session token"})
		return
	}

	repo := releaseRepo()
	current := currentControllerVersion()
	resp := adminVersionResponse{
		CurrentVersion: current,
		ReleaseRepo:    repo,
	}

	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	release, err := fetchLatestRelease(ctx, repo)
	if err != nil {
		resp.Message = fmt.Sprintf("failed to query latest release: %v", err)
		writeJSON(w, http.StatusOK, resp)
		return
	}

	resp.LatestVersion = strings.TrimSpace(release.TagName)
	resp.UpgradeAvailable = normalizeVersion(current) != normalizeVersion(resp.LatestVersion)
	writeJSON(w, http.StatusOK, resp)
}

func AdminUpgradeHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	token, err := extractBearerToken(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	if !IsTokenValid(token) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid or expired session token"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()

	result, err := performControllerUpgrade(ctx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{
			"error":           err.Error(),
			"current_version": result.CurrentVersion,
			"latest_version":  result.LatestVersion,
		})
		return
	}

	writeJSON(w, http.StatusOK, result)

	if result.Updated && shouldAutoRestartAfterUpgrade() {
		go func() {
			time.Sleep(1200 * time.Millisecond)
			log.Printf("upgrade complete, restarting process to activate %s", result.LatestVersion)
			os.Exit(0)
		}()
	}
}

func shouldAutoRestartAfterUpgrade() bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv("CLOUDHELPER_UPGRADE_AUTO_RESTART")))
	if v == "0" || v == "false" || v == "no" {
		return false
	}
	return true
}

func performControllerUpgrade(ctx context.Context) (adminUpgradeResponse, error) {
	repo := releaseRepo()
	current := currentControllerVersion()
	result := adminUpgradeResponse{CurrentVersion: current}

	release, err := fetchLatestRelease(ctx, repo)
	if err != nil {
		return result, fmt.Errorf("fetch latest release failed: %w", err)
	}

	latest := strings.TrimSpace(release.TagName)
	if latest == "" {
		return result, errors.New("latest release tag is empty")
	}
	result.LatestVersion = latest

	if normalizeVersion(current) == normalizeVersion(latest) {
		result.Updated = false
		result.Message = "already on latest version"
		return result, nil
	}

	asset, err := pickControllerAsset(release.Assets)
	if err != nil {
		return result, err
	}
	result.AssetName = asset.Name

	tmpDir, err := os.MkdirTemp("", "cloudhelper-upgrade-*")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(tmpDir)

	assetFile := filepath.Join(tmpDir, asset.Name)
	if err := downloadReleaseAsset(ctx, asset.BrowserDownloadURL, assetFile); err != nil {
		return result, err
	}

	binaryPath, err := extractControllerBinary(assetFile, asset.Name, tmpDir)
	if err != nil {
		return result, err
	}

	if err := replaceCurrentExecutable(binaryPath); err != nil {
		return result, err
	}
	if err := persistControllerVersion(latest); err != nil {
		log.Printf("warning: failed to persist upgraded controller version %s: %v", latest, err)
	}

	result.Updated = true
	result.Message = "upgrade completed; service will restart"
	return result, nil
}

func persistControllerVersion(version string) error {
	if Store == nil {
		return nil
	}
	Store.mu.Lock()
	Store.Data[controllerVersionStoreField] = strings.TrimSpace(version)
	Store.mu.Unlock()
	return Store.Save()
}

func currentControllerVersion() string {
	if v := strings.TrimSpace(os.Getenv("CLOUDHELPER_VERSION")); v != "" {
		return v
	}

	if Store != nil {
		Store.mu.RLock()
		raw, _ := Store.Data[controllerVersionStoreField].(string)
		Store.mu.RUnlock()
		if strings.TrimSpace(raw) != "" {
			return strings.TrimSpace(raw)
		}
	}

	for _, p := range versionFileCandidates() {
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if v := strings.TrimSpace(string(raw)); v != "" {
			return v
		}
	}

	if bi, ok := debug.ReadBuildInfo(); ok {
		if v := strings.TrimSpace(bi.Main.Version); v != "" && v != "(devel)" {
			return v
		}
	}

	if v := strings.TrimSpace(BuildVersion); v != "" && v != "(devel)" && !strings.EqualFold(v, "dev") {
		return v
	}
	return "dev"
}

func versionFileCandidates() []string {
	candidates := []string{
		filepath.Join(".", "version"),
		filepath.Join("..", "version"),
		filepath.Join("..", "..", "version"),
	}

	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(dir, "version"),
			filepath.Join(dir, "..", "version"),
		)
	}
	return candidates
}

func releaseRepo() string {
	if v := strings.TrimSpace(os.Getenv("CLOUDHELPER_RELEASE_REPO")); v != "" {
		return v
	}
	return defaultReleaseRepo
}

func normalizeVersion(v string) string {
	v = strings.TrimSpace(strings.ToLower(v))
	v = strings.TrimPrefix(v, "v")
	return v
}

func fetchLatestRelease(ctx context.Context, repo string) (githubRelease, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return githubRelease{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "cloudhelper-probe-controller")
	if token := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(req)
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

func pickControllerAsset(assets []githubReleaseAsset) (githubReleaseAsset, error) {
	if len(assets) == 0 {
		return githubReleaseAsset{}, errors.New("no release assets found")
	}

	if exact := strings.TrimSpace(os.Getenv("CLOUDHELPER_CONTROLLER_ASSET_NAME")); exact != "" {
		for _, a := range assets {
			if a.Name == exact {
				return a, nil
			}
		}
		return githubReleaseAsset{}, fmt.Errorf("asset %q not found in release", exact)
	}

	goos := strings.ToLower(runtime.GOOS)
	archKeys := []string{strings.ToLower(runtime.GOARCH)}
	if runtime.GOARCH == "amd64" {
		archKeys = append(archKeys, "x86_64")
	}
	if runtime.GOARCH == "arm64" {
		archKeys = append(archKeys, "aarch64")
	}

	isControllerAsset := func(name string) bool {
		n := strings.ToLower(name)
		return strings.Contains(n, "probe-controller") || strings.Contains(n, "probe_controller")
	}

	containsAny := func(s string, keys []string) bool {
		for _, k := range keys {
			if strings.Contains(s, k) {
				return true
			}
		}
		return false
	}

	for _, a := range assets {
		n := strings.ToLower(a.Name)
		if isControllerAsset(n) && strings.Contains(n, goos) && containsAny(n, archKeys) {
			return a, nil
		}
	}
	for _, a := range assets {
		n := strings.ToLower(a.Name)
		if isControllerAsset(n) && strings.Contains(n, goos) {
			return a, nil
		}
	}
	for _, a := range assets {
		n := strings.ToLower(a.Name)
		if isControllerAsset(n) {
			return a, nil
		}
	}

	return githubReleaseAsset{}, errors.New("no matching probe_controller asset in release")
}

func downloadReleaseAsset(ctx context.Context, url, outputPath string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("User-Agent", "cloudhelper-probe-controller")
	if token := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("asset download failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	f, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := io.Copy(f, resp.Body); err != nil {
		return err
	}
	return nil
}

func extractControllerBinary(assetFile, assetName, workDir string) (string, error) {
	extractDir := filepath.Join(workDir, "extract")
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		return "", err
	}

	lower := strings.ToLower(assetName)
	switch {
	case strings.HasSuffix(lower, ".tar.gz") || strings.HasSuffix(lower, ".tgz"):
		if err := extractTarGz(assetFile, extractDir); err != nil {
			return "", err
		}
	case strings.HasSuffix(lower, ".zip"):
		if err := extractZip(assetFile, extractDir); err != nil {
			return "", err
		}
	default:
		dst := filepath.Join(extractDir, filepath.Base(assetName))
		if err := copyFile(assetFile, dst, 0o755); err != nil {
			return "", err
		}
	}

	candidate, err := findControllerBinaryCandidate(extractDir)
	if err != nil {
		return "", err
	}
	if err := os.Chmod(candidate, 0o755); err != nil {
		return "", err
	}
	return candidate, nil
}

func extractTarGz(src, dst string) error {
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
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		target, err := safeJoin(dst, hdr.Name)
		if err != nil {
			return err
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			mode := fs.FileMode(hdr.Mode)
			if mode == 0 {
				mode = 0o755
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
		}
	}
	return nil
}

func extractZip(src, dst string) error {
	zr, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer zr.Close()

	for _, f := range zr.File {
		target, err := safeJoin(dst, f.Name)
		if err != nil {
			return err
		}
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
		mode := f.Mode()
		if mode == 0 {
			mode = 0o755
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
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
		if err := out.Close(); err != nil {
			return err
		}
	}
	return nil
}

func safeJoin(base, unsafePath string) (string, error) {
	clean := filepath.Clean(unsafePath)
	if strings.HasPrefix(clean, "..") {
		return "", fmt.Errorf("invalid archive path: %s", unsafePath)
	}
	joined := filepath.Join(base, clean)
	if !strings.HasPrefix(joined, filepath.Clean(base)+string(os.PathSeparator)) && filepath.Clean(joined) != filepath.Clean(base) {
		return "", fmt.Errorf("invalid archive path: %s", unsafePath)
	}
	return joined, nil
}

func findControllerBinaryCandidate(root string) (string, error) {
	candidates := []string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		name := strings.ToLower(d.Name())
		if strings.HasSuffix(name, ".exe") {
			return nil
		}
		if strings.Contains(name, "probe_controller") || strings.Contains(name, "probe-controller") {
			candidates = append(candidates, path)
			return nil
		}

		if info, statErr := d.Info(); statErr == nil && info.Mode()&0o111 != 0 {
			candidates = append(candidates, path)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(candidates) == 0 {
		return "", errors.New("no executable binary found in downloaded asset")
	}

	// Prefer canonical binary names first.
	for _, p := range candidates {
		n := strings.ToLower(filepath.Base(p))
		if n == "probe_controller" || n == "probe-controller" {
			return p, nil
		}
	}
	return candidates[0], nil
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
		if mode == 0 {
			mode = 0o755
		}
	}

	tmpPath := exePath + ".new"
	if err := copyFile(newBinary, tmpPath, mode); err != nil {
		return err
	}

	backupPath := fmt.Sprintf("%s.bak.%d", exePath, time.Now().Unix())
	if err := os.Rename(exePath, backupPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("backup current binary failed: %w", err)
	}
	if err := os.Rename(tmpPath, exePath); err != nil {
		_ = os.Rename(backupPath, exePath)
		_ = os.Remove(tmpPath)
		return fmt.Errorf("replace binary failed: %w", err)
	}
	return nil
}

func copyFile(src, dst string, mode fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return nil
}
