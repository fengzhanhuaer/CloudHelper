package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const defaultManagerUpgradeRepo = "fengzhanhuaer/CloudHelper"

type ReleaseAsset struct {
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	DownloadURL string `json:"download_url"`
}

type ReleaseInfo struct {
	Repo        string         `json:"repo"`
	TagName     string         `json:"tag_name"`
	ReleaseName string         `json:"release_name,omitempty"`
	HTMLURL     string         `json:"html_url,omitempty"`
	PublishedAt string         `json:"published_at,omitempty"`
	Assets      []ReleaseAsset `json:"assets"`
}

type ManagerUpgradeResult struct {
	CurrentVersion string `json:"current_version"`
	LatestVersion  string `json:"latest_version"`
	AssetName      string `json:"asset_name,omitempty"`
	Mode           string `json:"mode"`
	Updated        bool   `json:"updated"`
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

type proxyLatestResponse struct {
	Repo        string         `json:"repo"`
	TagName     string         `json:"tag_name"`
	ReleaseName string         `json:"release_name,omitempty"`
	HTMLURL     string         `json:"html_url,omitempty"`
	PublishedAt string         `json:"published_at,omitempty"`
	Assets      []ReleaseAsset `json:"assets"`
}

func (a *App) GetLatestGitHubRelease(project string) (ReleaseInfo, error) {
	repo, err := normalizeGitHubRepo(project)
	if err != nil {
		return ReleaseInfo{}, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	release, err := fetchLatestRelease(ctx, repo)
	if err != nil {
		return ReleaseInfo{}, err
	}

	return releaseInfoFromGitHub(repo, release), nil
}

func (a *App) GetLatestGitHubReleaseViaProxy(controllerBaseURL, sessionToken, project string) (ReleaseInfo, error) {
	base, err := normalizeControllerBaseURL(controllerBaseURL)
	if err != nil {
		return ReleaseInfo{}, err
	}
	token := strings.TrimSpace(sessionToken)
	if token == "" {
		return ReleaseInfo{}, errors.New("session token is required")
	}

	payload := map[string]string{"project": strings.TrimSpace(project)}
	body, err := json.Marshal(payload)
	if err != nil {
		return ReleaseInfo{}, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/admin/proxy/github/latest", bytes.NewReader(body))
	if err != nil {
		return ReleaseInfo{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ReleaseInfo{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return ReleaseInfo{}, fmt.Errorf("proxy latest release failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var out proxyLatestResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return ReleaseInfo{}, err
	}

	return ReleaseInfo{
		Repo:        strings.TrimSpace(out.Repo),
		TagName:     strings.TrimSpace(out.TagName),
		ReleaseName: strings.TrimSpace(out.ReleaseName),
		HTMLURL:     strings.TrimSpace(out.HTMLURL),
		PublishedAt: strings.TrimSpace(out.PublishedAt),
		Assets:      out.Assets,
	}, nil
}

func (a *App) UpgradeManagerDirect(project string) (ManagerUpgradeResult, error) {
	result := ManagerUpgradeResult{
		CurrentVersion: a.GetManagerVersion(),
		Mode:           "direct",
	}

	repo, err := normalizeGitHubRepo(project)
	if err != nil {
		return result, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	release, err := fetchLatestRelease(ctx, repo)
	if err != nil {
		return result, err
	}

	info := releaseInfoFromGitHub(repo, release)
	return a.performManagerUpgrade(result, info, "", "")
}

func (a *App) UpgradeManagerViaProxy(controllerBaseURL, sessionToken, project string) (ManagerUpgradeResult, error) {
	result := ManagerUpgradeResult{
		CurrentVersion: a.GetManagerVersion(),
		Mode:           "proxy",
	}

	info, err := a.GetLatestGitHubReleaseViaProxy(controllerBaseURL, sessionToken, project)
	if err != nil {
		return result, err
	}
	return a.performManagerUpgrade(result, info, controllerBaseURL, sessionToken)
}

func (a *App) performManagerUpgrade(result ManagerUpgradeResult, info ReleaseInfo, controllerBaseURL, sessionToken string) (ManagerUpgradeResult, error) {
	latest := strings.TrimSpace(info.TagName)
	if latest == "" {
		return result, errors.New("latest release tag is empty")
	}
	result.LatestVersion = latest

	if normalizeVersion(result.CurrentVersion) == normalizeVersion(latest) {
		result.Updated = false
		result.Message = "already on latest version"
		return result, nil
	}

	asset, err := pickManagerAsset(info.Assets)
	if err != nil {
		return result, err
	}
	result.AssetName = asset.Name

	tmpDir, err := os.MkdirTemp("", "cloudhelper-manager-upgrade-*")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(tmpDir)

	assetFile := filepath.Join(tmpDir, sanitizeFilename(asset.Name))
	if assetFile == tmpDir || strings.TrimSpace(asset.DownloadURL) == "" {
		return result, errors.New("invalid release asset")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if result.Mode == "proxy" {
		if err := downloadAssetViaProxy(ctx, controllerBaseURL, sessionToken, asset.DownloadURL, assetFile); err != nil {
			return result, err
		}
	} else {
		if err := downloadReleaseAsset(ctx, asset.DownloadURL, assetFile); err != nil {
			return result, err
		}
	}

	binaryPath, err := extractManagerBinary(assetFile, asset.Name, tmpDir)
	if err != nil {
		return result, err
	}

	if runtime.GOOS == "windows" {
		if err := stageWindowsSelfReplace(binaryPath); err != nil {
			return result, err
		}
		result.Updated = true
		result.Message = "upgrade staged, application will restart"
		go func() {
			time.Sleep(1200 * time.Millisecond)
			os.Exit(0)
		}()
		return result, nil
	}

	if err := replaceCurrentExecutable(binaryPath); err != nil {
		return result, err
	}
	result.Updated = true
	result.Message = "upgrade completed, please restart manager"
	return result, nil
}

func releaseInfoFromGitHub(repo string, release githubRelease) ReleaseInfo {
	assets := make([]ReleaseAsset, 0, len(release.Assets))
	for _, a := range release.Assets {
		assets = append(assets, ReleaseAsset{
			Name:        a.Name,
			Size:        a.Size,
			DownloadURL: a.BrowserDownloadURL,
		})
	}

	return ReleaseInfo{
		Repo:        strings.TrimSpace(repo),
		TagName:     strings.TrimSpace(release.TagName),
		ReleaseName: strings.TrimSpace(release.Name),
		HTMLURL:     strings.TrimSpace(release.HTMLURL),
		PublishedAt: strings.TrimSpace(release.PublishedAt),
		Assets:      assets,
	}
}

func normalizeGitHubRepo(project string) (string, error) {
	p := strings.TrimSpace(project)
	if p == "" {
		if env := strings.TrimSpace(os.Getenv("CLOUDHELPER_MANAGER_RELEASE_REPO")); env != "" {
			p = env
		} else {
			p = defaultManagerUpgradeRepo
		}
	}

	if strings.Contains(p, "github.com") {
		u, err := url.Parse(p)
		if err != nil {
			return "", errors.New("invalid github project url")
		}
		pathPart := strings.Trim(strings.TrimSuffix(u.Path, ".git"), "/")
		parts := strings.Split(pathPart, "/")
		if len(parts) < 2 {
			return "", errors.New("project url must include owner/repo")
		}
		return parts[0] + "/" + parts[1], nil
	}

	parts := strings.Split(strings.Trim(p, "/"), "/")
	if len(parts) < 2 {
		return "", errors.New("project must be owner/repo or github url")
	}
	return parts[0] + "/" + parts[1], nil
}

func normalizeControllerBaseURL(base string) (string, error) {
	trimmed := strings.TrimSpace(base)
	if trimmed == "" {
		return "", errors.New("controller base url is required")
	}
	if !strings.Contains(trimmed, "://") {
		trimmed = "http://" + trimmed
	}
	u, err := url.Parse(trimmed)
	if err != nil || strings.TrimSpace(u.Host) == "" {
		return "", errors.New("invalid controller base url")
	}
	u.Path = ""
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimRight(u.String(), "/"), nil
}

func fetchLatestRelease(ctx context.Context, repo string) (githubRelease, error) {
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return githubRelease{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "cloudhelper-probe-manager")
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

func pickManagerAsset(assets []ReleaseAsset) (ReleaseAsset, error) {
	if len(assets) == 0 {
		return ReleaseAsset{}, errors.New("no release assets found")
	}

	if exact := strings.TrimSpace(os.Getenv("CLOUDHELPER_MANAGER_ASSET_NAME")); exact != "" {
		for _, a := range assets {
			if strings.EqualFold(strings.TrimSpace(a.Name), exact) {
				return a, nil
			}
		}
		return ReleaseAsset{}, fmt.Errorf("asset %q not found in release", exact)
	}

	goos := strings.ToLower(runtime.GOOS)
	archKeys := []string{strings.ToLower(runtime.GOARCH)}
	if runtime.GOARCH == "amd64" {
		archKeys = append(archKeys, "x86_64")
	}
	if runtime.GOARCH == "386" {
		archKeys = append(archKeys, "x86", "i386")
	}
	if runtime.GOARCH == "arm64" {
		archKeys = append(archKeys, "aarch64")
	}

	containsAny := func(s string, keys []string) bool {
		for _, k := range keys {
			if strings.Contains(s, k) {
				return true
			}
		}
		return false
	}
	isManagerAsset := func(name string) bool {
		n := strings.ToLower(name)
		return strings.Contains(n, "probe-manager") || strings.Contains(n, "probe_manager")
	}

	for _, a := range assets {
		n := strings.ToLower(a.Name)
		if isManagerAsset(n) && strings.Contains(n, goos) && containsAny(n, archKeys) {
			return a, nil
		}
	}
	for _, a := range assets {
		n := strings.ToLower(a.Name)
		if isManagerAsset(n) && strings.Contains(n, goos) {
			return a, nil
		}
	}
	for _, a := range assets {
		if isManagerAsset(a.Name) {
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
	return ReleaseAsset{}, errors.New("no matching manager asset found in release")
}

func downloadReleaseAsset(ctx context.Context, rawURL, outputPath string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSpace(rawURL), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("User-Agent", "cloudhelper-probe-manager")
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

	_, err = io.Copy(f, resp.Body)
	return err
}

func downloadAssetViaProxy(ctx context.Context, controllerBaseURL, sessionToken, assetURL, outputPath string) error {
	base, err := normalizeControllerBaseURL(controllerBaseURL)
	if err != nil {
		return err
	}
	token := strings.TrimSpace(sessionToken)
	if token == "" {
		return errors.New("session token is required")
	}

	downloadURL := base + "/api/admin/proxy/download?url=" + url.QueryEscape(strings.TrimSpace(assetURL))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/octet-stream")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("proxy download failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	f, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = io.Copy(f, resp.Body)
	return err
}

func extractManagerBinary(assetFile, assetName, workDir string) (string, error) {
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
		dst := filepath.Join(extractDir, sanitizeFilename(filepath.Base(assetName)))
		if err := copyFile(assetFile, dst, 0o755); err != nil {
			return "", err
		}
	}

	candidate, err := findManagerBinaryCandidate(extractDir)
	if err != nil {
		return "", err
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(candidate, 0o755); err != nil {
			return "", err
		}
	}
	return candidate, nil
}

func findManagerBinaryCandidate(root string) (string, error) {
	type candidate struct {
		path  string
		score int
	}
	best := candidate{}

	archKeys := []string{strings.ToLower(runtime.GOARCH)}
	if runtime.GOARCH == "amd64" {
		archKeys = append(archKeys, "x86_64")
	}
	if runtime.GOARCH == "386" {
		archKeys = append(archKeys, "x86", "i386")
	}
	if runtime.GOARCH == "arm64" {
		archKeys = append(archKeys, "aarch64")
	}

	containsAny := func(s string, keys []string) bool {
		for _, k := range keys {
			if strings.Contains(s, k) {
				return true
			}
		}
		return false
	}

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}

		name := strings.ToLower(d.Name())
		if runtime.GOOS == "windows" && !strings.HasSuffix(name, ".exe") {
			return nil
		}

		score := 1
		if strings.Contains(name, "probe-manager") || strings.Contains(name, "probe_manager") {
			score += 100
		}
		if strings.Contains(name, strings.ToLower(runtime.GOOS)) {
			score += 50
		}
		if containsAny(name, archKeys) {
			score += 25
		}

		if runtime.GOOS != "windows" {
			if info, infoErr := d.Info(); infoErr == nil && (info.Mode()&0o111) != 0 {
				score += 10
			}
		}

		if score > best.score {
			best = candidate{path: path, score: score}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if best.path == "" {
		return "", errors.New("failed to locate manager binary in release asset")
	}
	return best.path, nil
}

func replaceCurrentExecutable(newBinaryPath string) error {
	currentExe, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, resolveErr := filepath.EvalSymlinks(currentExe); resolveErr == nil {
		currentExe = resolved
	}

	dir := filepath.Dir(currentExe)
	staged := filepath.Join(dir, filepath.Base(currentExe)+".new")
	backup := filepath.Join(dir, filepath.Base(currentExe)+".bak")

	if err := copyFile(newBinaryPath, staged, 0o755); err != nil {
		return err
	}

	_ = os.Remove(backup)
	if err := os.Rename(currentExe, backup); err != nil {
		_ = os.Remove(staged)
		return fmt.Errorf("backup current executable failed: %w", err)
	}
	if err := os.Rename(staged, currentExe); err != nil {
		_ = os.Rename(backup, currentExe)
		return fmt.Errorf("replace executable failed: %w", err)
	}
	return nil
}

func stageWindowsSelfReplace(newBinaryPath string) error {
	currentExe, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, resolveErr := filepath.EvalSymlinks(currentExe); resolveErr == nil {
		currentExe = resolved
	}

	staged := currentExe + ".new"
	if err := copyFile(newBinaryPath, staged, 0o755); err != nil {
		return err
	}

	tmpDir, err := os.MkdirTemp("", "cloudhelper-manager-restart-*")
	if err != nil {
		return err
	}

	escape := func(v string) string {
		return strings.ReplaceAll(v, "'", "''")
	}

	script := fmt.Sprintf(`$pidToWait = %d
$target = '%s'
$source = '%s'
$backup = '%s'
for ($i = 0; $i -lt 180; $i++) {
    if (-not (Get-Process -Id $pidToWait -ErrorAction SilentlyContinue)) { break }
    Start-Sleep -Seconds 1
}
if (Test-Path $backup) {
    Remove-Item -Force $backup -ErrorAction SilentlyContinue
}
if (Test-Path $target) {
    Move-Item -Force $target $backup -ErrorAction SilentlyContinue
}
Move-Item -Force $source $target
Start-Process -FilePath $target
`, os.Getpid(), escape(currentExe), escape(staged), escape(currentExe+".bak"))

	scriptPath := filepath.Join(tmpDir, "apply_manager_upgrade.ps1")
	if err := os.WriteFile(scriptPath, []byte(script), 0o600); err != nil {
		return err
	}

	cmd := exec.Command("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-WindowStyle", "Hidden", "-File", scriptPath)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return err
	}
	return nil
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
		if errors.Is(err, io.EOF) {
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
			perm := os.FileMode(hdr.Mode)
			if perm == 0 {
				perm = 0o644
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
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
		perm := f.Mode().Perm()
		if perm == 0 {
			perm = 0o644
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
		if err != nil {
			rc.Close()
			return err
		}

		if _, err := io.Copy(out, rc); err != nil {
			rc.Close()
			out.Close()
			return err
		}
		if err := rc.Close(); err != nil {
			out.Close()
			return err
		}
		if err := out.Close(); err != nil {
			return err
		}
	}
	return nil
}

func safeJoin(baseDir, name string) (string, error) {
	cleanName := filepath.Clean(name)
	if cleanName == "." || cleanName == string(filepath.Separator) {
		return "", errors.New("invalid archive entry")
	}
	if filepath.IsAbs(cleanName) {
		return "", errors.New("archive entry must be relative")
	}
	target := filepath.Join(baseDir, cleanName)
	base := filepath.Clean(baseDir) + string(filepath.Separator)
	if !strings.HasPrefix(filepath.Clean(target)+string(filepath.Separator), base) {
		return "", errors.New("archive entry escapes destination")
	}
	return target, nil
}

func copyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
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

func sanitizeFilename(name string) string {
	name = strings.TrimSpace(strings.ReplaceAll(name, `"`, ""))
	if name == "" {
		return strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	name = filepath.Base(name)
	if name == "." || name == string(filepath.Separator) {
		return strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	return name
}

func normalizeVersion(v string) string {
	v = strings.TrimSpace(strings.ToLower(v))
	return strings.TrimPrefix(v, "v")
}
