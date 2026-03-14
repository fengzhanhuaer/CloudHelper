package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"
)

type proxyLatestRequest struct {
	Project string `json:"project"`
	Repo    string `json:"repo"`
}

type proxyAsset struct {
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	DownloadURL string `json:"download_url"`
}

type proxyLatestResponse struct {
	Repo        string       `json:"repo"`
	TagName     string       `json:"tag_name"`
	ReleaseName string       `json:"release_name,omitempty"`
	HTMLURL     string       `json:"html_url,omitempty"`
	PublishedAt string       `json:"published_at,omitempty"`
	Assets      []proxyAsset `json:"assets"`
}

func AdminProxyGitHubLatestHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req proxyLatestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}

	repo, err := normalizeGitHubRepo(req.Repo, req.Project)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	release, err := fetchLatestRelease(ctx, repo)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": fmt.Sprintf("failed to fetch github latest release: %v", err)})
		return
	}

	assets := make([]proxyAsset, 0, len(release.Assets))
	for _, a := range release.Assets {
		assets = append(assets, proxyAsset{
			Name:        a.Name,
			Size:        a.Size,
			DownloadURL: a.BrowserDownloadURL,
		})
	}

	writeJSON(w, http.StatusOK, proxyLatestResponse{
		Repo:        repo,
		TagName:     strings.TrimSpace(release.TagName),
		ReleaseName: strings.TrimSpace(release.Name),
		HTMLURL:     strings.TrimSpace(release.HTMLURL),
		PublishedAt: strings.TrimSpace(release.PublishedAt),
		Assets:      assets,
	})
}

func AdminProxyDownloadHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	rawURL := strings.TrimSpace(r.URL.Query().Get("url"))
	if rawURL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "url query parameter is required"})
		return
	}

	targetURL, err := url.Parse(rawURL)
	if err != nil || targetURL == nil || targetURL.Scheme != "https" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid download url"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	proxyReq, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL.String(), nil)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	proxyReq.Header.Set("User-Agent", "cloudhelper-proxy-download")
	proxyReq.Header.Set("Accept", "application/octet-stream")
	if token := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); token != "" {
		proxyReq.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(proxyReq)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": fmt.Sprintf("proxy download failed: %v", err)})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": fmt.Sprintf("upstream status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))})
		return
	}

	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	fileName := sanitizeFilename(path.Base(strings.TrimSpace(targetURL.Path)))
	if fileName != "" {
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, fileName))
	}
	w.Header().Set("Content-Type", contentType)
	if cd := strings.TrimSpace(resp.Header.Get("Content-Disposition")); cd != "" {
		w.Header().Set("Content-Disposition", cd)
	}
	if cl := strings.TrimSpace(resp.Header.Get("Content-Length")); cl != "" {
		w.Header().Set("Content-Length", cl)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, resp.Body)
}

func normalizeGitHubRepo(repo, project string) (string, error) {
	if v := strings.TrimSpace(repo); v != "" {
		parts := strings.Split(strings.Trim(v, "/"), "/")
		if len(parts) >= 2 {
			return parts[0] + "/" + parts[1], nil
		}
	}

	p := strings.TrimSpace(project)
	if p == "" {
		return "", errors.New("repo or project is required")
	}

	if strings.Contains(p, "github.com") {
		u, err := url.Parse(p)
		if err != nil {
			return "", errors.New("invalid project url")
		}
		path := strings.Trim(strings.TrimSuffix(u.Path, ".git"), "/")
		parts := strings.Split(path, "/")
		if len(parts) >= 2 {
			return parts[0] + "/" + parts[1], nil
		}
		return "", errors.New("project url must include owner/repo")
	}

	parts := strings.Split(strings.Trim(p, "/"), "/")
	if len(parts) >= 2 {
		return parts[0] + "/" + parts[1], nil
	}
	return "", errors.New("project must be github repo path like owner/repo or github url")
}

func sanitizeFilename(name string) string {
	name = strings.TrimSpace(strings.ReplaceAll(name, `"`, ""))
	if name == "." || name == "/" || name == "\\" {
		return ""
	}
	return name
}
