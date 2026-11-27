package dashboard

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"net/http"
	"strings"
)

var (
	//go:embed page.html
	pageHTML string

	pageBytes     []byte
	pageGzipBytes []byte
	pageETag      string
)

func init() {
	pageBytes = []byte(pageHTML)
	sum := sha256.Sum256(pageBytes)
	pageETag = `W/"` + hex.EncodeToString(sum[:16]) + `"`

	var buf bytes.Buffer
	gz, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err == nil {
		if _, writeErr := gz.Write(pageBytes); writeErr == nil {
			if closeErr := gz.Close(); closeErr == nil {
				pageGzipBytes = buf.Bytes()
			}
		}
	}
}

func matchesETag(headerValue, currentETag string) bool {
	if strings.TrimSpace(headerValue) == "" || currentETag == "" {
		return false
	}
	normalize := func(tag string) string {
		tag = strings.TrimSpace(tag)
		tag = strings.TrimPrefix(tag, "W/")
		tag = strings.TrimPrefix(tag, "w/")
		return strings.TrimSpace(tag)
	}
	target := normalize(currentETag)
	for _, candidate := range strings.Split(headerValue, ",") {
		trimmed := strings.TrimSpace(candidate)
		if trimmed == "*" || normalize(trimmed) == target {
			return true
		}
	}
	return false
}

func acceptsGzip(r *http.Request) bool {
	if r == nil {
		return false
	}
	return strings.Contains(strings.ToLower(r.Header.Get("Accept-Encoding")), "gzip")
}

func Handler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Vary", "Accept-Encoding")
	if pageETag != "" {
		w.Header().Set("ETag", pageETag)
		if matchesETag(r.Header.Get("If-None-Match"), pageETag) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	if acceptsGzip(r) && len(pageGzipBytes) > 0 {
		w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = w.Write(pageGzipBytes)
		}
		return
	}
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(pageBytes)
	}
}
