package mobilecore

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// ControllerUpgradeRequest keeps APK metadata and downloads on the same pinned,
// authenticated adaptive transport as reporting. It returns one bounded chunk
// so the Android UI can publish progress and retry without restarting the file.
func ControllerUpgradeRequest(controller, node, secret, target, output string) string {
	result := map[string]interface{}{}
	run := func() error {
		base := strings.TrimRight(controller, "/")
		if !strings.HasPrefix(target, base+"/api/probe/proxy/") {
			return errors.New("invalid controller upgrade URL")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, "GET", target, nil)
		if err != nil {
			return err
		}
		headers, err := buildAuthHeaders(target, node, secret, "GET")
		if err != nil {
			return err
		}
		req.Header = headers
		req.Header.Set("Accept-Encoding", "identity")
		offset := int64(0)
		var saved struct {
			URL   string `json:"url"`
			ETag  string `json:"etag"`
			Total int64  `json:"total"`
		}
		if output != "" {
			if data, e := os.ReadFile(output + ".json"); e == nil {
				json.Unmarshal(data, &saved)
			}
			if saved.URL != target {
				os.Remove(output)
				os.Remove(output + ".json")
				saved.ETag = ""
				saved.Total = 0
			}
			saved.URL = target
			if st, e := os.Stat(output); e == nil {
				offset = st.Size()
			}
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, offset+(4<<20)-1))
			req.Header.Set("X-CloudHelper-Download-Cache", "1")
		}
		resp, err := mobileControllerDo(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		result["status"] = resp.StatusCode
		if resp.StatusCode == 503 && resp.Header.Get("X-CloudHelper-Cache") == "preparing" {
			result["preparing"] = true
			return nil
		}
		if output == "" {
			if resp.StatusCode != 200 {
				return fmt.Errorf("controller status %d", resp.StatusCode)
			}
			data, e := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
			result["body"] = string(data)
			return e
		}
		var total, expected int64
		switch resp.StatusCode {
		case 416:
			var size int64
			if _, e := fmt.Sscanf(resp.Header.Get("Content-Range"), "bytes */%d", &size); e != nil || size <= 0 || size != offset || saved.Total != size {
				return errors.New("invalid completed APK range")
			}
			result["downloaded"] = offset
			result["total"] = size
			result["complete"] = true
			return nil
		case 206:
			var first, last int64
			if n, e := fmt.Sscanf(resp.Header.Get("Content-Range"), "bytes %d-%d/%d", &first, &last, &total); e != nil || n != 3 || first != offset || last < first || last >= total {
				return errors.New("invalid APK content range")
			}
			expected = last - first + 1
		case 200:
			offset = 0
			total = resp.ContentLength
			expected = total
		default:
			return fmt.Errorf("controller download status %d", resp.StatusCode)
		}
		etag := resp.Header.Get("ETag")
		if offset > 0 && (saved.ETag == "" || saved.ETag != etag || saved.Total != total) {
			os.Remove(output)
			os.Remove(output + ".json")
			return errors.New("APK changed during resume")
		}
		saved.ETag = etag
		saved.Total = total
		data, _ := json.Marshal(saved)
		if err = os.WriteFile(output+".json", data, 0600); err != nil {
			return err
		}
		flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
		if offset == 0 {
			flags = os.O_CREATE | os.O_WRONLY | os.O_TRUNC
		}
		f, err := os.OpenFile(output, flags, 0600)
		if err != nil {
			return err
		}
		written, e := io.Copy(f, resp.Body)
		closeErr := f.Close()
		if e != nil {
			return e
		}
		if closeErr != nil {
			return closeErr
		}
		if expected >= 0 && written != expected {
			return io.ErrUnexpectedEOF
		}
		if total-(offset+written) >= 8<<20 && etag != "" {
			if err := downloadMobileUpgradePair(controller, node, secret, target, output, etag, offset+written, total); err != nil {
				return err
			}
			written += 8 << 20
		}
		result["downloaded"] = offset + written
		result["total"] = total
		result["complete"] = total > 0 && offset+written == total
		return nil
	}
	if err := run(); err != nil {
		result["error"] = err.Error()
	}
	data, _ := json.Marshal(result)
	return string(data)
}

func downloadMobileUpgradePair(controller, node, secret, target, output, etag string, prefix, total int64) error {
	var wg sync.WaitGroup
	failures := make([]error, 2)
	paths := make([]string, 2)
	hash := sha256.Sum256([]byte(target + etag))
	for i := 0; i < 2; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			first := prefix + int64(i)*(4<<20)
			last := first + (4 << 20) - 1
			path := fmt.Sprintf("%s.chunk-%x-%d", output, hash[:8], first)
			paths[i] = path
			for attempt := 0; attempt < 3; attempt++ {
				size := int64(0)
				if st, e := os.Stat(path); e == nil {
					size = st.Size()
				}
				if size == 4<<20 {
					return
				}
				if size > 4<<20 {
					failures[i] = errors.New("oversized APK chunk")
					return
				}
				failures[i] = downloadMobileUpgradeRange(node, secret, target, path, etag, first+size, last, total)
				if failures[i] == nil {
					return
				}
			}
		}()
	}
	wg.Wait()
	for _, e := range failures {
		if e != nil {
			return e
		}
	}
	file, err := os.OpenFile(output, os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	for _, path := range paths {
		chunk, e := os.Open(path)
		if e != nil {
			return e
		}
		_, e = io.Copy(file, chunk)
		chunk.Close()
		if e != nil {
			return e
		}
		if e = file.Sync(); e != nil {
			return e
		}
	}
	for _, path := range paths {
		os.Remove(path)
	}
	return nil
}
func downloadMobileUpgradeRange(node, secret, target, path, etag string, first, last, total int64) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", target, nil)
	if err != nil {
		return err
	}
	headers, err := buildAuthHeaders(target, node, secret, "GET")
	if err != nil {
		return err
	}
	req.Header = headers
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", first, last))
	req.Header.Set("If-Range", etag)
	req.Header.Set("X-CloudHelper-Download-Cache", "1")
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := mobileControllerDo(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var a, b, size int64
	n, e := fmt.Sscanf(resp.Header.Get("Content-Range"), "bytes %d-%d/%d", &a, &b, &size)
	if resp.StatusCode != 206 || e != nil || n != 3 || a != first || b != last || size != total || resp.Header.Get("ETag") != etag {
		return errors.New("APK parallel object/range mismatch")
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	written, err := io.Copy(file, io.LimitReader(resp.Body, last-first+2))
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if written != last-first+1 {
		return io.ErrUnexpectedEOF
	}
	return nil
}
