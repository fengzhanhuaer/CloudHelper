package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const probeDownloadCacheTTL = 30 * time.Minute
const probeDownloadCacheFileLimit int64 = 512 << 20
const probeDownloadCacheSlots = 8

type probeDownloadCacheEntry struct {
	path, etag string
	touched    time.Time
	ready      bool
	refs       int
}
type probeDownloadCache struct {
	mu      sync.Mutex
	dir     string
	entries map[string]*probeDownloadCacheEntry
	once    sync.Once
}

var probeAssetCache = &probeDownloadCache{dir: filepath.Join("temp", "probe-download-cache"), entries: map[string]*probeDownloadCacheEntry{}}

// Authentication and the GitHub host/redirect allowlist run before this cache.
// A cache miss starts one bounded, detached fetch; clients can retry without
// cancelling the shared origin download or holding a Cloudflare request open.
func (c *probeDownloadCache) serve(w http.ResponseWriter, r *http.Request, raw string) {
	c.once.Do(func() {
		go func() {
			timer := time.NewTicker(time.Minute)
			defer timer.Stop()
			for range timer.C {
				c.clean(time.Now())
			}
		}()
	})
	sum := sha256.Sum256([]byte(raw))
	key := hex.EncodeToString(sum[:])
	c.mu.Lock()
	e := c.entries[key]
	if e == nil {
		if len(c.entries) >= probeDownloadCacheSlots {
			var oldestKey string
			var oldest *probeDownloadCacheEntry
			for k, v := range c.entries {
				if v.ready && v.refs == 0 && (oldest == nil || v.touched.Before(oldest.touched)) {
					oldestKey = k
					oldest = v
				}
			}
			if oldest != nil {
				os.Remove(oldest.path)
				os.Remove(oldest.path + ".json")
				delete(c.entries, oldestKey)
			}
		}
		if len(c.entries) >= probeDownloadCacheSlots {
			c.mu.Unlock()
			w.Header().Set("Retry-After", "3")
			http.Error(w, "download cache busy", 503)
			return
		}
		e = &probeDownloadCacheEntry{path: filepath.Join(c.dir, key+".asset"), touched: time.Now()}
		var receipt struct {
			ETag string
			Size int64
		}
		if data, err := os.ReadFile(e.path + ".json"); err == nil && len(data) < 1024 && json.Unmarshal(data, &receipt) == nil {
			if st, err := os.Stat(e.path); err == nil && st.Mode().IsRegular() && st.Size() == receipt.Size && st.Size() > 0 && time.Since(st.ModTime()) < probeDownloadCacheTTL && len(receipt.ETag) == 66 {
				if _, err := hex.DecodeString(strings.Trim(receipt.ETag, "\"")); err == nil {
					e.etag = receipt.ETag
					e.ready = true
				}
			}
		}
		c.entries[key] = e
		if !e.ready {
			go c.fill(key, raw, e)
		}
	}
	e.touched = time.Now()
	if !e.ready {
		c.mu.Unlock()
		w.Header().Set("Retry-After", "2")
		w.Header().Set("X-CloudHelper-Cache", "preparing")
		http.Error(w, "download cache preparing", 503)
		return
	}
	e.refs++
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		e.refs--
		e.touched = time.Now()
		os.Chtimes(e.path, e.touched, e.touched)
		c.mu.Unlock()
	}()
	file, err := os.Open(e.path)
	if err != nil {
		http.Error(w, "cached file unavailable", 503)
		return
	}
	defer file.Close()
	st, err := file.Stat()
	if err != nil {
		http.Error(w, "cached file unavailable", 503)
		return
	}
	w.Header().Set("ETag", e.etag)
	w.Header().Set("X-CloudHelper-Cache", "ready")
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "private, no-store")
	http.ServeContent(w, r, "asset", st.ModTime(), file)
}
func (c *probeDownloadCache) fill(key, raw string, e *probeDownloadCacheEntry) {
	success := false
	defer func() {
		if !success {
			os.Remove(e.path + ".part")
			c.mu.Lock()
			if c.entries[key] == e {
				delete(c.entries, key)
			}
			c.mu.Unlock()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	req, err := newProbeProxyDownloadRequest(ctx, raw)
	if err != nil {
		return
	}
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := doProbeProxyDownloadWithRetry(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || resp.ContentLength > probeDownloadCacheFileLimit {
		return
	}
	if os.MkdirAll(c.dir, 0700) != nil {
		return
	}
	f, err := os.OpenFile(e.path+".part", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	hash := sha256.New()
	sink := &probeCacheWriter{HeaderMap: make(http.Header), out: io.MultiWriter(f, hash)}
	err = streamProbeProxyDownload(sink, req, resp)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil || sink.written == 0 {
		return
	}
	if os.Rename(e.path+".part", e.path) != nil {
		return
	}
	c.mu.Lock()
	e.etag = fmt.Sprintf("\"%x\"", hash.Sum(nil))
	receipt, _ := json.Marshal(struct {
		ETag string
		Size int64
	}{e.etag, sink.written})
	os.WriteFile(e.path+".json", receipt, 0600)
	e.ready = true
	e.touched = time.Now()
	c.mu.Unlock()
	success = true
}

type probeCacheWriter struct {
	HeaderMap http.Header
	out       io.Writer
	written   int64
}

func (w *probeCacheWriter) Header() http.Header { return w.HeaderMap }
func (w *probeCacheWriter) WriteHeader(int)     {}
func (w *probeCacheWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > probeDownloadCacheFileLimit-w.written {
		return 0, errors.New("download cache object exceeds limit")
	}
	n, e := w.out.Write(p)
	w.written += int64(n)
	return n, e
}
func (c *probeDownloadCache) clean(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, e := range c.entries {
		if e.ready && e.refs == 0 && now.Sub(e.touched) >= probeDownloadCacheTTL {
			os.Remove(e.path)
			os.Remove(e.path + ".json")
			delete(c.entries, k)
		}
	}
	// Remove stale files left by an interrupted controller, without traversing
	// arbitrary paths or touching active fills/readers.
	files, _ := os.ReadDir(c.dir)
	for _, f := range files {
		name := f.Name()
		key := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(name, ".json"), ".part"), ".asset")
		if f.IsDir() || len(key) != 64 || c.entries[key] != nil {
			continue
		}
		if _, err := hex.DecodeString(key); err != nil {
			continue
		}
		if st, err := f.Info(); err == nil && now.Sub(st.ModTime()) >= probeDownloadCacheTTL {
			os.Remove(filepath.Join(c.dir, name))
		}
	}
}
