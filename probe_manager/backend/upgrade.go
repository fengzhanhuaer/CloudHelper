package backend

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"encoding/base64"
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
	"sync"
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

type ManagerUpgradeProgress struct {
	Active  bool   `json:"active"`
	Mode    string `json:"mode"`
	Phase   string `json:"phase"`
	Percent int    `json:"percent"`
	Message string `json:"message"`
}

var managerUpgradeProgressState = struct {
	mu   sync.RWMutex
	data ManagerUpgradeProgress
}{}

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

	out, err := fetchProxyLatestReleaseViaAdminWS(base, token, strings.TrimSpace(project))
	if err != nil {
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

func fetchProxyLatestReleaseViaAdminWS(baseURL, token, project string) (proxyLatestResponse, error) {
	wsURL, err := buildAdminWSURL(baseURL)
	if err != nil {
		return proxyLatestResponse{}, err
	}

	dialer := buildControllerWSDialer(baseURL)
	headers := http.Header{}
	headers.Set("X-Forwarded-Proto", "https")
	conn, resp, err := dialer.Dial(wsURL, headers)
	if err != nil {
		if resp != nil {
			defer resp.Body.Close()
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
			return proxyLatestResponse{}, fmt.Errorf("admin ws handshake failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		return proxyLatestResponse{}, err
	}
	defer conn.Close()

	deadline := time.Now().Add(20 * time.Second)
	if err := conn.SetReadDeadline(deadline); err != nil {
		return proxyLatestResponse{}, err
	}
	if err := conn.SetWriteDeadline(deadline); err != nil {
		return proxyLatestResponse{}, err
	}

	authID := fmt.Sprintf("upg-auth-%d", time.Now().UnixNano())
	authReq := adminWSRequest{ID: authID, Action: "auth.session", Payload: map[string]string{"token": token}}
	if err := conn.WriteJSON(authReq); err != nil {
		return proxyLatestResponse{}, err
	}
	authResp, err := readAdminWSResponseByID(conn, authID)
	if err != nil {
		return proxyLatestResponse{}, err
	}
	if !authResp.OK {
		return proxyLatestResponse{}, fmt.Errorf("admin ws auth failed: %s", strings.TrimSpace(authResp.Error))
	}

	queryID := fmt.Sprintf("upg-latest-%d", time.Now().UnixNano())
	queryReq := adminWSRequest{ID: queryID, Action: "admin.proxy.github.latest", Payload: map[string]string{"project": project}}
	if err := conn.WriteJSON(queryReq); err != nil {
		return proxyLatestResponse{}, err
	}
	queryResp, err := readAdminWSResponseByID(conn, queryID)
	if err != nil {
		return proxyLatestResponse{}, err
	}
	if !queryResp.OK {
		return proxyLatestResponse{}, fmt.Errorf("proxy latest release failed: %s", strings.TrimSpace(queryResp.Error))
	}

	var out proxyLatestResponse
	if len(queryResp.Data) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(queryResp.Data, &out); err != nil {
		return proxyLatestResponse{}, err
	}
	return out, nil
}

func (a *App) UpgradeManagerDirect(project string) (ManagerUpgradeResult, error) {
	result := ManagerUpgradeResult{
		CurrentVersion: a.GetManagerVersion(),
		Mode:           "direct",
	}
	setManagerUpgradeProgress(ManagerUpgradeProgress{Active: true, Mode: result.Mode, Phase: "prepare", Percent: 2, Message: "准备升级"})
	defer func() {
		p := getManagerUpgradeProgress()
		if p.Active {
			setManagerUpgradeProgress(ManagerUpgradeProgress{Active: false, Mode: result.Mode, Phase: "idle", Percent: p.Percent, Message: p.Message})
		}
	}()

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
	setManagerUpgradeProgress(ManagerUpgradeProgress{Active: true, Mode: result.Mode, Phase: "prepare", Percent: 2, Message: "准备升级"})
	defer func() {
		p := getManagerUpgradeProgress()
		if p.Active {
			setManagerUpgradeProgress(ManagerUpgradeProgress{Active: false, Mode: result.Mode, Phase: "idle", Percent: p.Percent, Message: p.Message})
		}
	}()

	info, err := a.GetLatestGitHubReleaseViaProxy(controllerBaseURL, sessionToken, project)
	if err != nil {
		return result, err
	}
	return a.performManagerUpgrade(result, info, controllerBaseURL, sessionToken)
}

func (a *App) performManagerUpgrade(result ManagerUpgradeResult, info ReleaseInfo, controllerBaseURL, sessionToken string) (ManagerUpgradeResult, error) {
	setManagerUpgradeProgress(ManagerUpgradeProgress{Active: true, Mode: result.Mode, Phase: "check", Percent: 10, Message: "检查版本"})
	latest := strings.TrimSpace(info.TagName)
	if latest == "" {
		return result, errors.New("latest release tag is empty")
	}
	result.LatestVersion = latest

	if normalizeVersion(result.CurrentVersion) == normalizeVersion(latest) {
		result.Updated = false
		result.Message = "already on latest version"
		setManagerUpgradeProgress(ManagerUpgradeProgress{Active: false, Mode: result.Mode, Phase: "done", Percent: 100, Message: "已是最新版本"})
		return result, nil
	}

	setManagerUpgradeProgress(ManagerUpgradeProgress{Active: true, Mode: result.Mode, Phase: "select_asset", Percent: 15, Message: "选择升级包"})
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

	setManagerUpgradeProgress(ManagerUpgradeProgress{Active: true, Mode: result.Mode, Phase: "download", Percent: 20, Message: "下载升级包"})

	if result.Mode == "proxy" {
		if err := downloadAssetViaProxy(ctx, controllerBaseURL, sessionToken, asset.DownloadURL, assetFile, func(downloaded, total int64) {
			setManagerDownloadProgress(result.Mode, downloaded, total)
		}); err != nil {
			setManagerUpgradeProgress(ManagerUpgradeProgress{Active: false, Mode: result.Mode, Phase: "failed", Percent: 20, Message: "下载失败"})
			return result, err
		}
	} else {
		if err := downloadReleaseAsset(ctx, asset.DownloadURL, assetFile, func(downloaded, total int64) {
			setManagerDownloadProgress(result.Mode, downloaded, total)
		}); err != nil {
			setManagerUpgradeProgress(ManagerUpgradeProgress{Active: false, Mode: result.Mode, Phase: "failed", Percent: 20, Message: "下载失败"})
			return result, err
		}
	}

	setManagerUpgradeProgress(ManagerUpgradeProgress{Active: true, Mode: result.Mode, Phase: "extract", Percent: 80, Message: "解压升级包"})
	binaryPath, err := extractManagerBinary(assetFile, asset.Name, tmpDir)
	if err != nil {
		setManagerUpgradeProgress(ManagerUpgradeProgress{Active: false, Mode: result.Mode, Phase: "failed", Percent: 80, Message: "解压失败"})
		return result, err
	}

	if runtime.GOOS == "windows" {
		setManagerUpgradeProgress(ManagerUpgradeProgress{Active: true, Mode: result.Mode, Phase: "replace", Percent: 92, Message: "写入新版本"})
		if err := stageWindowsSelfReplace(binaryPath); err != nil {
			setManagerUpgradeProgress(ManagerUpgradeProgress{Active: false, Mode: result.Mode, Phase: "failed", Percent: 92, Message: "写入失败"})
			return result, err
		}
		result.Updated = true
		result.Message = "upgrade staged, application will restart"
		setManagerUpgradeProgress(ManagerUpgradeProgress{Active: false, Mode: result.Mode, Phase: "done", Percent: 100, Message: "升级完成，程序即将重启"})
		go func() {
			time.Sleep(1200 * time.Millisecond)
			os.Exit(0)
		}()
		return result, nil
	}

	setManagerUpgradeProgress(ManagerUpgradeProgress{Active: true, Mode: result.Mode, Phase: "replace", Percent: 92, Message: "写入新版本"})
	if err := replaceCurrentExecutable(binaryPath); err != nil {
		setManagerUpgradeProgress(ManagerUpgradeProgress{Active: false, Mode: result.Mode, Phase: "failed", Percent: 92, Message: "写入失败"})
		return result, err
	}
	result.Updated = true
	result.Message = "upgrade completed, please restart manager"
	setManagerUpgradeProgress(ManagerUpgradeProgress{Active: false, Mode: result.Mode, Phase: "done", Percent: 100, Message: "升级完成"})
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

func downloadReleaseAsset(ctx context.Context, rawURL, outputPath string, onProgress func(downloaded, total int64)) error {
	partPath := outputPath + ".part"
	resumeOffset := int64(0)
	if st, err := os.Stat(partPath); err == nil && st.Mode().IsRegular() {
		resumeOffset = st.Size()
	}

	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSpace(rawURL), nil)
		if err != nil {
			return err
		}
		req.Header.Set("Accept", "application/octet-stream")
		req.Header.Set("User-Agent", "cloudhelper-probe-manager")
		if resumeOffset > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", resumeOffset))
		}
		if token := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}

		if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
			resp.Body.Close()
			if err := os.Rename(partPath, outputPath); err == nil {
				return nil
			}
			resumeOffset = 0
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			return fmt.Errorf("asset download failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
		}

		truncate := resumeOffset == 0 || resp.StatusCode == http.StatusOK
		flags := os.O_CREATE | os.O_WRONLY
		if truncate {
			flags |= os.O_TRUNC
		} else {
			flags |= os.O_APPEND
		}
		f, err := os.OpenFile(partPath, flags, 0o644)
		if err != nil {
			resp.Body.Close()
			return err
		}

		total := resp.ContentLength
		if total >= 0 && resp.StatusCode == http.StatusPartialContent && resumeOffset > 0 {
			total += resumeOffset
		}
		baseWritten := resumeOffset
		if truncate {
			baseWritten = 0
			if resumeOffset > 0 && resp.StatusCode == http.StatusOK {
				resumeOffset = 0
			}
		}
		wrappedProgress := onProgress
		if onProgress != nil {
			wrappedProgress = func(downloaded, total int64) {
				onProgress(baseWritten+downloaded, total)
			}
		}
		written, copyErr := copyWithProgress(f, resp.Body, total, wrappedProgress)
		closeErr := f.Close()
		resp.Body.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		finalSize := baseWritten + written
		if total >= 0 && finalSize < total {
			resumeOffset = finalSize
			continue
		}
		if err := os.Rename(partPath, outputPath); err != nil {
			return err
		}
		return nil
	}
}

func downloadAssetViaProxy(ctx context.Context, controllerBaseURL, sessionToken, assetURL, outputPath string, onProgress func(downloaded, total int64)) error {
	base, err := normalizeControllerBaseURL(controllerBaseURL)
	if err != nil {
		return err
	}
	token := strings.TrimSpace(sessionToken)
	if token == "" {
		return errors.New("session token is required")
	}
	partPath := outputPath + ".part"
	resumeOffset := int64(0)
	if st, err := os.Stat(partPath); err == nil && st.Mode().IsRegular() {
		resumeOffset = st.Size()
	}

	for {
		wsURL, err := buildAdminWSURL(base)
		if err != nil {
			return err
		}

		dialer := buildControllerWSDialer(base)
		dialer.HandshakeTimeout = 12 * time.Second
		headers := http.Header{}
		headers.Set("X-Forwarded-Proto", "https")
		conn, resp, err := dialer.Dial(wsURL, headers)
		if err != nil {
			if resp != nil {
				defer resp.Body.Close()
				body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
				return fmt.Errorf("admin ws handshake failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
			}
			return err
		}

		readDeadline := time.Now().Add(10 * time.Minute)
		if deadline, ok := ctx.Deadline(); ok {
			readDeadline = deadline
		}
		if err := conn.SetReadDeadline(readDeadline); err != nil {
			conn.Close()
			return err
		}
		if err := conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
			conn.Close()
			return err
		}

		authID := fmt.Sprintf("upg-dl-auth-%d", time.Now().UnixNano())
		authReq := adminWSRequest{ID: authID, Action: "auth.session", Payload: map[string]string{"token": token}}
		if err := conn.WriteJSON(authReq); err != nil {
			conn.Close()
			return err
		}
		authResp, err := readAdminWSResponseByID(conn, authID)
		if err != nil {
			conn.Close()
			return err
		}
		if !authResp.OK {
			conn.Close()
			return fmt.Errorf("admin ws auth failed: %s", strings.TrimSpace(authResp.Error))
		}

		streamID := fmt.Sprintf("upg-dl-%d", time.Now().UnixNano())
		streamReq := adminWSRequest{
			ID:     streamID,
			Action: "admin.proxy.download.stream",
			Payload: map[string]interface{}{
				"url":        strings.TrimSpace(assetURL),
				"chunk_size": 64 * 1024,
				"offset":     resumeOffset,
			},
		}
		if err := conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
			conn.Close()
			return err
		}
		if err := conn.WriteJSON(streamReq); err != nil {
			conn.Close()
			return err
		}

		total := int64(0)
		written := resumeOffset
		statusCode := 0
		truncate := resumeOffset == 0
		var f *os.File
		for {
			if deadline, ok := ctx.Deadline(); ok {
				_ = conn.SetReadDeadline(deadline)
			}

			var msg adminWSResponse
			if err := conn.ReadJSON(&msg); err != nil {
				if f != nil {
					_ = f.Close()
				}
				conn.Close()
				return err
			}

			if strings.TrimSpace(msg.Type) != "" {
				if strings.TrimSpace(msg.Type) != "proxy.download.chunk" {
					continue
				}
				var chunk struct {
					RequestID   string `json:"request_id"`
					ChunkBase64 string `json:"chunk_base64"`
					Downloaded  int64  `json:"downloaded"`
					Total       int64  `json:"total"`
					Status      int    `json:"status"`
				}
				if err := json.Unmarshal(msg.Data, &chunk); err != nil {
					if f != nil {
						_ = f.Close()
					}
					conn.Close()
					return err
				}
				if strings.TrimSpace(chunk.RequestID) != streamID {
					continue
				}
				if statusCode == 0 {
					statusCode = chunk.Status
					truncate = resumeOffset == 0 || statusCode == http.StatusOK
					flags := os.O_CREATE | os.O_WRONLY
					if truncate {
						flags |= os.O_TRUNC
						written = 0
					} else {
						flags |= os.O_APPEND
					}
					f, err = os.OpenFile(partPath, flags, 0o644)
					if err != nil {
						conn.Close()
						return err
					}
				}
				payload, err := base64.StdEncoding.DecodeString(strings.TrimSpace(chunk.ChunkBase64))
				if err != nil {
					if f != nil {
						_ = f.Close()
					}
					conn.Close()
					return err
				}
				if len(payload) > 0 {
					n, writeErr := f.Write(payload)
					written += int64(n)
					if writeErr != nil {
						_ = f.Close()
						conn.Close()
						return writeErr
					}
					if n != len(payload) {
						_ = f.Close()
						conn.Close()
						return io.ErrShortWrite
					}
				}
				if chunk.Total > 0 {
					total = chunk.Total
				}
				if onProgress != nil {
					onProgress(written, total)
				}
				continue
			}

			if strings.TrimSpace(msg.ID) != streamID {
				continue
			}
			if !msg.OK {
				if f != nil {
					_ = f.Close()
				}
				conn.Close()
				return fmt.Errorf("proxy download failed: %s", strings.TrimSpace(msg.Error))
			}

			var done struct {
				Downloaded int64 `json:"downloaded"`
				Total      int64 `json:"total"`
				Status     int   `json:"status"`
			}
			if len(msg.Data) > 0 {
				_ = json.Unmarshal(msg.Data, &done)
				if done.Total > 0 {
					total = done.Total
				}
				if done.Downloaded > written {
					written = done.Downloaded
				}
				if done.Status != 0 {
					statusCode = done.Status
				}
			}
			if f != nil {
				if err := f.Close(); err != nil {
					conn.Close()
					return err
				}
			}
			conn.Close()
			if statusCode == http.StatusRequestedRangeNotSatisfiable {
				if err := os.Rename(partPath, outputPath); err == nil {
					if onProgress != nil {
						onProgress(written, total)
					}
					return nil
				}
				resumeOffset = 0
				break
			}
			if total > 0 && written < total {
				resumeOffset = written
				break
			}
			if err := os.Rename(partPath, outputPath); err != nil {
				return err
			}
			if onProgress != nil {
				onProgress(written, total)
			}
			return nil
		}
	}
}

func copyWithProgress(dst io.Writer, src io.Reader, total int64, onProgress func(downloaded, total int64)) (int64, error) {
	buf := make([]byte, 32*1024)
	var written int64
	lastEmit := time.Now()
	for {
		n, err := src.Read(buf)
		if n > 0 {
			wn, werr := dst.Write(buf[:n])
			written += int64(wn)
			if onProgress != nil {
				now := time.Now()
				if now.Sub(lastEmit) > 200*time.Millisecond || err == io.EOF {
					onProgress(written, total)
					lastEmit = now
				}
			}
			if werr != nil {
				return written, werr
			}
			if wn != n {
				return written, io.ErrShortWrite
			}
		}
		if err != nil {
			if err == io.EOF {
				if onProgress != nil {
					onProgress(written, total)
				}
				return written, nil
			}
			return written, err
		}
	}
}

func setManagerDownloadProgress(mode string, downloaded, total int64) {
	percent := 20
	if total > 0 {
		span := int(float64(downloaded) / float64(total) * 55.0)
		if span < 0 {
			span = 0
		}
		if span > 55 {
			span = 55
		}
		percent = 20 + span
	}
	msg := "下载升级包中"
	if total > 0 {
		msg = fmt.Sprintf("下载升级包中 (%d%%)", int(float64(downloaded)*100.0/float64(total)))
	}
	setManagerUpgradeProgress(ManagerUpgradeProgress{Active: true, Mode: mode, Phase: "download", Percent: percent, Message: msg})
}

func (a *App) GetManagerUpgradeProgress() ManagerUpgradeProgress {
	return getManagerUpgradeProgress()
}

func setManagerUpgradeProgress(progress ManagerUpgradeProgress) {
	if progress.Percent < 0 {
		progress.Percent = 0
	}
	if progress.Percent > 100 {
		progress.Percent = 100
	}
	managerUpgradeProgressState.mu.Lock()
	managerUpgradeProgressState.data = progress
	managerUpgradeProgressState.mu.Unlock()
}

func getManagerUpgradeProgress() ManagerUpgradeProgress {
	managerUpgradeProgressState.mu.RLock()
	defer managerUpgradeProgressState.mu.RUnlock()
	return managerUpgradeProgressState.data
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

func cleanupManagerStaleExecutables() error {
	currentExe, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, resolveErr := filepath.EvalSymlinks(currentExe); resolveErr == nil {
		currentExe = resolved
	}

	dir := filepath.Dir(currentExe)
	base := filepath.Base(currentExe)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}

	var firstErr error
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if name != base+".new" && name != base+".bak" && !strings.HasPrefix(name, base+".bak.") {
			continue
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
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
