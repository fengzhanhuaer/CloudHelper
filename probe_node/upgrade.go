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

const (
	probeUpgradeVerifyOutputLimit     = 2048
	probeUpgradeVerifyTimeoutGraceSec = 15
	probeUpgradeWorkspaceDirName      = ".cloudhelper-upgrade"
	probeUpgradeWorkspacePrefix       = "cloudhelper-probe-node-upgrade-"
	probeUpgradeWorkspaceStaleTTL     = 24 * time.Hour
)

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
	reportProbeLocalUpgradeProgress(probeLocalUpgradeRuntimeState{
		Status:      "running",
		Step:        "prepare",
		Progress:    5,
		Message:     "准备升级环境",
		Mode:        mode,
		ReleaseRepo: repo,
	})
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

	reportProbeLocalUpgradeProgress(probeLocalUpgradeRuntimeState{
		Status:      "running",
		Step:        "fetch_release",
		Progress:    12,
		Message:     "获取最新版本信息",
		Mode:        mode,
		ReleaseRepo: repo,
	})
	release, err := fetchProbeRelease(ctx, mode, repo, controllerBase, identity)
	if err != nil {
		log.Printf("probe upgrade failed: fetch release: %v", err)
		reportProbeLocalUpgradeFailed("fetch_release", err, mode, repo, 12)
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
		reportProbeLocalUpgradeSuccess("当前已是最新版本", mode, repo)
		return
	}

	reportProbeLocalUpgradeProgress(probeLocalUpgradeRuntimeState{
		Status:      "running",
		Step:        "pick_asset",
		Progress:    20,
		Message:     "选择平台安装包",
		Mode:        mode,
		ReleaseRepo: repo,
	})
	asset, err := pickProbeNodeAsset(release.Assets, platform)
	if err != nil {
		log.Printf("probe upgrade failed: pick asset: %v", err)
		reportProbeLocalUpgradeFailed("pick_asset", err, mode, repo, 20)
		return
	}
	log.Printf("probe upgrade asset selected: name=%s", strings.TrimSpace(asset.Name))

	reportProbeLocalUpgradeProgress(probeLocalUpgradeRuntimeState{
		Status:      "running",
		Step:        "prepare_workspace",
		Progress:    28,
		Message:     "创建升级工作目录",
		Mode:        mode,
		ReleaseRepo: repo,
	})
	tmpDir, err := createProbeUpgradeWorkspace()
	if err != nil {
		log.Printf("probe upgrade failed: temp dir: %v", err)
		reportProbeLocalUpgradeFailed("prepare_workspace", err, mode, repo, 28)
		return
	}
	defer cleanupProbeUpgradeWorkspace(tmpDir)

	assetFile := filepath.Join(tmpDir, filepath.Base(asset.Name))
	reportProbeLocalUpgradeProgress(probeLocalUpgradeRuntimeState{
		Status:      "running",
		Step:        "download",
		Progress:    42,
		Message:     "下载升级包",
		Mode:        mode,
		ReleaseRepo: repo,
	})
	log.Printf("probe upgrade download: target=%s mode=%s", assetFile, mode)
	if err := downloadProbeAsset(ctx, mode, asset.DownloadURL, controllerBase, identity, assetFile); err != nil {
		log.Printf("probe upgrade failed: download asset: %v", err)
		reportProbeLocalUpgradeFailed("download", err, mode, repo, 42)
		return
	}
	if st, err := os.Stat(assetFile); err == nil {
		log.Printf("probe upgrade download complete: file=%s size=%d", assetFile, st.Size())
	}

	reportProbeLocalUpgradeProgress(probeLocalUpgradeRuntimeState{
		Status:      "running",
		Step:        "extract",
		Progress:    58,
		Message:     "解压升级包",
		Mode:        mode,
		ReleaseRepo: repo,
	})
	binaryPath, err := extractProbeBinary(assetFile, asset.Name, tmpDir)
	if err != nil {
		log.Printf("probe upgrade failed: extract binary: %v", err)
		reportProbeLocalUpgradeFailed("extract", err, mode, repo, 58)
		return
	}
	log.Printf("probe upgrade extract complete: binary=%s", binaryPath)

	reportProbeLocalUpgradeProgress(probeLocalUpgradeRuntimeState{
		Status:      "running",
		Step:        "verify",
		Progress:    72,
		Message:     "校验安装包兼容性",
		Mode:        mode,
		ReleaseRepo: repo,
	})
	if err := verifyBinaryCompatibility(binaryPath, platform); err != nil {
		log.Printf("probe upgrade failed: compatibility check: %v", err)
		reportProbeLocalUpgradeFailed("verify_compatibility", err, mode, repo, 72)
		return
	}
	if err := verifyProbeCandidateRuntime(binaryPath); err != nil {
		log.Printf("probe upgrade failed: candidate runtime verify: %v", err)
		reportProbeLocalUpgradeFailed("verify_runtime", err, mode, repo, 76)
		return
	}
	log.Printf("probe upgrade candidate runtime verify complete: binary=%s", binaryPath)

	reportProbeLocalUpgradeProgress(probeLocalUpgradeRuntimeState{
		Status:      "running",
		Step:        "replace",
		Progress:    86,
		Message:     "替换当前可执行文件",
		Mode:        mode,
		ReleaseRepo: repo,
	})
	exePath, backupPath, err := replaceCurrentExecutable(binaryPath)
	if err != nil {
		log.Printf("probe upgrade failed: replace executable: %v", err)
		reportProbeLocalUpgradeFailed("replace", err, mode, repo, 86)
		return
	}
	log.Printf("probe upgrade replace complete: exe=%s backup=%s", exePath, backupPath)

	reportProbeLocalUpgradeProgress(probeLocalUpgradeRuntimeState{
		Status:      "running",
		Step:        "restart",
		Progress:    95,
		Message:     "重启服务应用新版本",
		Mode:        mode,
		ReleaseRepo: repo,
	})
	log.Printf("probe upgrade complete: %s -> %s, restarting", BuildVersion, release.TagName)
	if err := restartCurrentProcess(exePath); err != nil {
		log.Printf("probe upgrade restart failed: %v", err)
		if rollbackErr := rollbackExecutable(exePath, backupPath); rollbackErr != nil {
			log.Printf("probe upgrade rollback failed: %v", rollbackErr)
			reportProbeLocalUpgradeFailed("rollback", rollbackErr, mode, repo, 96)
			return
		}
		log.Printf("probe upgrade rollback complete, old binary restored")
		reportProbeLocalUpgradeFailed("restart", err, mode, repo, 96)
		return
	}
	reportProbeLocalUpgradeSuccess("升级完成，服务重启中", mode, repo)
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
	partPath := output + ".part"
	resumeOffset := int64(0)
	if st, err := os.Stat(partPath); err == nil && st.Mode().IsRegular() {
		resumeOffset = st.Size()
	}

	openPartFile := func(truncate bool) (*os.File, int64, error) {
		flags := os.O_CREATE | os.O_WRONLY
		if truncate {
			flags |= os.O_TRUNC
		} else {
			flags |= os.O_APPEND
		}
		f, err := os.OpenFile(partPath, flags, 0o644)
		if err != nil {
			return nil, 0, err
		}
		st, err := f.Stat()
		if err != nil {
			f.Close()
			return nil, 0, err
		}
		return f, st.Size(), nil
	}

	downloadOnce := func(offset int64) (int64, error) {
		start := time.Now()
		var (
			reader     io.ReadCloser
			statusCode int
			total      int64 = -1
		)
		if mode == "proxy" {
			requestURL, err := buildProbeUpgradeProxyDownloadURL(controllerBase, assetURL)
			if err != nil {
				return 0, err
			}
			log.Printf("probe upgrade asset download via controller proxy: %s offset=%d", safeURLForLog(requestURL), offset)
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
			if err != nil {
				return 0, err
			}
			for key, value := range buildProbeAuthHeaders(identity) {
				req.Header.Set(key, value)
			}
			req.Header.Set("Accept", "application/octet-stream")
			req.Header.Set("User-Agent", "cloudhelper-probe-node")
			if offset > 0 {
				req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
			}
			client, closeClient, err := newProbeResolvedHTTPClientForURL(requestURL, probeResolvedDialDefaultTimeout)
			if err != nil {
				return 0, err
			}
			resp, err := client.Do(req)
			if err != nil {
				closeClient()
				log.Printf("warning: probe upgrade proxy download request failed: elapsed=%s offset=%d err=%v", time.Since(start).String(), offset, err)
				return 0, err
			}
			defer closeClient()
			if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
				resp.Body.Close()
				if err := os.Rename(partPath, output); err == nil {
					return 0, nil
				}
				return 0, fmt.Errorf("download failed: %d", resp.StatusCode)
			}
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
				resp.Body.Close()
				return 0, fmt.Errorf("download failed: %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
			}
			reader = resp.Body
			statusCode = resp.StatusCode
			total = resp.ContentLength
			if total >= 0 && statusCode == http.StatusPartialContent && offset > 0 {
				total += offset
			}
		} else {
			log.Printf("probe upgrade asset download direct: %s offset=%d", safeURLForLog(assetURL), offset)
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, assetURL, nil)
			if err != nil {
				return 0, err
			}
			req.Header.Set("Accept", "application/octet-stream")
			req.Header.Set("User-Agent", "cloudhelper-probe-node")
			if offset > 0 {
				req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				log.Printf("warning: probe upgrade direct download request failed: elapsed=%s offset=%d err=%v", time.Since(start).String(), offset, err)
				return 0, err
			}
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
				resp.Body.Close()
				return 0, fmt.Errorf("download failed: %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
			}
			reader = resp.Body
			statusCode = resp.StatusCode
			total = resp.ContentLength
			if total >= 0 && statusCode == http.StatusPartialContent && offset > 0 {
				total += offset
			}
		}
		defer reader.Close()

		if statusCode == http.StatusRequestedRangeNotSatisfiable {
			if err := os.Rename(partPath, output); err == nil {
				return 0, nil
			}
		}

		truncate := offset == 0 || statusCode == http.StatusOK
		if offset > 0 && statusCode == http.StatusOK {
			log.Printf("probe upgrade resume unsupported, restarting full download: url=%s", safeURLForLog(assetURL))
		}
		f, currentSize, err := openPartFile(truncate)
		if err != nil {
			return 0, err
		}
		written, copyErr := io.Copy(f, reader)
		closeErr := f.Close()
		if copyErr != nil {
			if closeErr != nil {
				return 0, errors.Join(copyErr, closeErr)
			}
			return 0, copyErr
		}
		if closeErr != nil {
			return 0, closeErr
		}
		finalSize := currentSize + written
		if truncate {
			finalSize = written
		}
		if total >= 0 && finalSize < total {
			return finalSize, nil
		}
		if err := os.Rename(partPath, output); err != nil {
			return 0, err
		}
		log.Printf("probe upgrade download chunk complete: mode=%s status=%d offset=%d total=%d elapsed=%s", mode, statusCode, offset, finalSize, time.Since(start).String())
		return 0, nil
	}

	retryCount := 0
	for {
		nextOffset, err := downloadOnce(resumeOffset)
		if err != nil {
			if retryCount < 3 && isProbeTransientHTTPError(err) && ctx.Err() == nil {
				retryCount++
				if st, statErr := os.Stat(partPath); statErr == nil && st.Mode().IsRegular() {
					resumeOffset = st.Size()
				}
				log.Printf("probe upgrade download transient error, retry=%d offset=%d err=%v", retryCount, resumeOffset, err)
				time.Sleep(time.Duration(retryCount) * time.Second)
				continue
			}
			return err
		}
		retryCount = 0
		if nextOffset == 0 {
			return nil
		}
		resumeOffset = nextOffset
	}
}

func buildProbeUpgradeProxyDownloadURL(controllerBase string, assetURL string) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(controllerBase), "/")
	if base == "" {
		return "", errors.New("controller base url is empty")
	}
	if strings.TrimSpace(assetURL) == "" {
		return "", errors.New("asset url is empty")
	}
	query := url.Values{}
	query.Set("url", strings.TrimSpace(assetURL))
	return base + "/api/probe/proxy/download?" + query.Encode(), nil
}

func probeAuthedGet(ctx context.Context, requestURL string, identity nodeIdentity) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, err
	}
	for key, value := range buildProbeAuthHeaders(identity) {
		req.Header.Set(key, value)
	}
	client, closeClient, err := newProbeResolvedHTTPClientForURL(requestURL, probeResolvedDialDefaultTimeout)
	if err != nil {
		return nil, err
	}
	defer closeClient()
	resp, err := client.Do(req)
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

func resolveProbeUpgradeTargetPathForRuntime(exePath string, goos string) (string, string) {
	targetPath := normalizeExecutablePathForUpgradeTarget(exePath)
	if strings.TrimSpace(targetPath) == "" {
		return "", ""
	}
	if !strings.EqualFold(strings.TrimSpace(goos), "windows") {
		return targetPath, ""
	}
	if !strings.EqualFold(filepath.Base(targetPath), "probe_node.exe") {
		return targetPath, ""
	}
	parentDir := filepath.Dir(targetPath)
	if strings.EqualFold(filepath.Base(parentDir), "probe_node") {
		return targetPath, ""
	}
	runtimeDir := filepath.Join(parentDir, "probe_node")
	return filepath.Join(runtimeDir, "probe_node.exe"), parentDir
}

func rewriteLegacyWinSWExecutablePathForRuntimeDir(legacyRoot string) error {
	root := strings.TrimSpace(legacyRoot)
	if root == "" {
		return nil
	}
	matches, err := filepath.Glob(filepath.Join(root, "*-service.xml"))
	if err != nil {
		return err
	}
	for _, path := range matches {
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		content := string(raw)
		updated := strings.ReplaceAll(content, `%BASE%\probe_node.exe`, `%BASE%\probe_node\probe_node.exe`)
		if updated == content {
			continue
		}
		if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
			return err
		}
		log.Printf("probe upgrade legacy WinSW xml updated: %s", path)
	}
	return nil
}

func replaceCurrentExecutable(newBinary string) (string, string, error) {
	exePath, err := os.Executable()
	if err != nil {
		return "", "", err
	}
	if resolved, err := filepath.EvalSymlinks(exePath); err == nil && strings.TrimSpace(resolved) != "" {
		exePath = resolved
	}
	resolvedCurrentPath := normalizeExecutablePathForUpgradeTarget(exePath)
	targetPath, legacyRoot := resolveProbeUpgradeTargetPathForRuntime(resolvedCurrentPath, runtime.GOOS)
	if strings.TrimSpace(targetPath) == "" {
		return "", "", fmt.Errorf("resolved executable path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return "", "", err
	}

	cleanedCount, cleanErr := cleanupLegacyUpgradeBackups(targetPath)
	if cleanErr != nil {
		log.Printf("probe upgrade cleanup warning: target=%s err=%v", targetPath, cleanErr)
	} else if cleanedCount > 0 {
		log.Printf("probe upgrade cleanup: removed %d legacy backup files for %s", cleanedCount, targetPath)
	}

	mode := fs.FileMode(0o755)
	if st, err := os.Stat(targetPath); err == nil {
		mode = st.Mode().Perm()
	} else if st, err := os.Stat(resolvedCurrentPath); err == nil {
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

	backupSource := targetPath
	if _, statErr := os.Stat(backupSource); statErr != nil {
		backupSource = resolvedCurrentPath
	}
	if err := os.Rename(backupSource, backup); err != nil {
		_ = os.Remove(tmp)
		return "", "", err
	}
	if err := os.Rename(tmp, targetPath); err != nil {
		_ = os.Rename(backup, targetPath)
		_ = os.Remove(tmp)
		return "", "", err
	}

	if strings.TrimSpace(legacyRoot) != "" {
		if err := rewriteLegacyWinSWExecutablePathForRuntimeDir(legacyRoot); err != nil {
			log.Printf("probe upgrade warning: update legacy WinSW xml failed: root=%s err=%v", legacyRoot, err)
		}
	}
	return targetPath, backup, nil
}

func normalizeExecutablePathForUpgradeTarget(exePath string) string {
	cleaned := strings.TrimSpace(exePath)
	if cleaned == "" {
		return ""
	}

	for {
		lowered := strings.ToLower(cleaned)
		switch {
		case strings.HasSuffix(lowered, ".bak"):
			cleaned = strings.TrimSpace(cleaned[:len(cleaned)-len(".bak")])
		case hasTimestampedBackupSuffix(lowered):
			idx := strings.LastIndex(lowered, ".bak.")
			if idx <= 0 {
				return strings.TrimSpace(cleaned)
			}
			cleaned = strings.TrimSpace(cleaned[:idx])
		default:
			return cleaned
		}
		if cleaned == "" {
			return ""
		}
	}
}

func hasTimestampedBackupSuffix(loweredPath string) bool {
	idx := strings.LastIndex(loweredPath, ".bak.")
	if idx <= 0 {
		return false
	}
	suffix := loweredPath[idx+len(".bak."):]
	if len(suffix) != 14 {
		return false
	}
	for _, ch := range suffix {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}

func cleanupLegacyUpgradeBackups(targetPath string) (int, error) {
	cleanTarget := strings.TrimSpace(targetPath)
	if cleanTarget == "" {
		return 0, nil
	}

	base := filepath.Base(cleanTarget)
	dir := filepath.Dir(cleanTarget)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}

	removed := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := strings.TrimSpace(entry.Name())
		if name == "" {
			continue
		}
		if !looksLikeLegacyUpgradeBackup(base, name) {
			continue
		}
		fullPath := filepath.Join(dir, name)
		if err := os.Remove(fullPath); err != nil && !os.IsNotExist(err) {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

func looksLikeLegacyUpgradeBackup(binaryBaseName string, fileName string) bool {
	base := strings.TrimSpace(binaryBaseName)
	name := strings.TrimSpace(fileName)
	if base == "" || name == "" {
		return false
	}

	lowerBase := strings.ToLower(base)
	lowerName := strings.ToLower(name)

	if lowerName == lowerBase || lowerName == lowerBase+".bak" {
		return false
	}
	if strings.HasPrefix(lowerName, lowerBase+".bak.") {
		return true
	}

	normalized := strings.ToLower(normalizeExecutablePathForUpgradeTarget(name))
	if normalized == lowerBase && lowerName != lowerBase+".bak" {
		return true
	}
	return false
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

func verifyProbeCandidateRuntime(binaryPath string) error {
	candidate := strings.TrimSpace(binaryPath)
	if candidate == "" {
		return fmt.Errorf("candidate binary path is empty")
	}

	verifySec := normalizeUpgradeVerifyDurationSec(defaultUpgradeVerifyDurationSec)
	timeout := time.Duration(verifySec+probeUpgradeVerifyTimeoutGraceSec) * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	args := []string{
		"--upgrade-verify",
		fmt.Sprintf("--upgrade-verify-duration=%d", verifySec),
	}
	cmd := exec.CommandContext(ctx, candidate, args...)
	hideWindowSysProcAttr(cmd)
	cmd.Env = os.Environ()
	output, err := cmd.CombinedOutput()
	outputText := trimUpgradeVerifyOutputForLog(output, probeUpgradeVerifyOutputLimit)

	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("candidate verify timeout after %s", timeout)
	}
	if err != nil {
		if outputText != "" {
			return fmt.Errorf("candidate verify failed: %w; output=%s", err, outputText)
		}
		return fmt.Errorf("candidate verify failed: %w", err)
	}
	if outputText != "" {
		log.Printf("probe upgrade candidate verify output: %s", strings.ReplaceAll(outputText, "\n", " | "))
	}
	return nil
}

func trimUpgradeVerifyOutputForLog(raw []byte, limit int) string {
	if limit <= 0 {
		limit = probeUpgradeVerifyOutputLimit
	}
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return ""
	}
	if len(text) <= limit {
		return text
	}
	if limit <= 3 {
		return text[:limit]
	}
	return text[:limit-3] + "..."
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

func createProbeUpgradeWorkspace() (string, error) {
	baseDir := os.TempDir()
	if preferred, err := probeUpgradeWorkspaceBaseDir(); err == nil && strings.TrimSpace(preferred) != "" {
		baseDir = preferred
	}
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return "", err
	}
	cleanupProbeStaleUpgradeWorkspaces(baseDir)
	return os.MkdirTemp(baseDir, probeUpgradeWorkspacePrefix+"*")
}

func probeUpgradeWorkspaceBaseDir() (string, error) {
	exePath, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exePath); err == nil && strings.TrimSpace(resolved) != "" {
		exePath = resolved
	}
	return filepath.Join(filepath.Dir(exePath), probeUpgradeWorkspaceDirName), nil
}

func cleanupProbeStaleUpgradeWorkspaces(baseDir string) {
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		return
	}
	now := time.Now()
	for _, entry := range entries {
		name := strings.TrimSpace(entry.Name())
		if !strings.HasPrefix(name, probeUpgradeWorkspacePrefix) {
			continue
		}
		fullPath := filepath.Join(baseDir, name)
		info, infoErr := entry.Info()
		if infoErr == nil {
			if now.Sub(info.ModTime()) < probeUpgradeWorkspaceStaleTTL {
				continue
			}
		}
		_ = os.RemoveAll(fullPath)
	}
}

func cleanupProbeUpgradeWorkspace(workDir string) {
	cleanPath := strings.TrimSpace(workDir)
	if cleanPath == "" {
		return
	}
	if err := os.RemoveAll(cleanPath); err != nil && !os.IsNotExist(err) {
		log.Printf("probe upgrade cleanup warning: workspace=%s err=%v", cleanPath, err)
	}
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
