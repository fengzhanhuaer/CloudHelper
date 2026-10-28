package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

var errProbeUpgradeRestartDownload = errors.New("download checkpoint cannot be reused")

func probeUpgradeResponseValidator(resp *http.Response) string {
	v := strings.TrimSpace(resp.Header.Get("ETag"))
	if v == "" || strings.HasPrefix(v, "W/") {
		v = strings.TrimSpace(resp.Header.Get("Last-Modified"))
	}
	return v
}

// A cache and its upstream may assign different ETags to identical bytes. Verify
// all bytes already stored, not a sample, before appending the new response.
// offset is absolute; a parallel chunk's local file can start after byte zero.
func verifyProbeUpgradeCheckpoint(ctx context.Context, client *http.Client, original *http.Request, mode string, identity nodeIdentity, part string, offset, total int64, validator string) error {
	f, err := os.Open(part)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	size := st.Size()
	if size == 0 {
		return nil
	}
	if size > offset || validator == "" {
		return errProbeUpgradeRestartDownload
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req := original.Clone(ctx)
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset-size, offset-1))
	req.Header.Set("If-Range", validator)
	if mode == "proxy" {
		// The original request already consumed its one-time nonce.
		if err := applyProbeAuthHeaders(req, identity); err != nil {
			return err
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		if probeUpgradeRetryableStatus(resp.StatusCode) {
			return &probeUpgradeHTTPError{Status: resp.StatusCode, Detail: "checkpoint verification temporarily unavailable"}
		}
		if resp.StatusCode == http.StatusOK {
			return errProbeUpgradeRestartDownload
		}
		return fmt.Errorf("checkpoint verification status: %d", resp.StatusCode)
	}
	first, last, length, err := parseProbeUpgradeContentRange(resp.Header.Get("Content-Range"))
	if err != nil || first != offset-size || last != offset-1 || length != total || (resp.ContentLength >= 0 && resp.ContentLength != size) || probeUpgradeResponseValidator(resp) != validator {
		return fmt.Errorf("checkpoint verification response changed or has invalid range")
	}
	remote, local := sha256.New(), sha256.New()
	n, err := copyProbeUpgradeWithProgressAndIdleTimeout(remote, io.LimitReader(resp.Body, size+1), probeUpgradeDownloadIdleTimeout, cancel, nil)
	if err != nil {
		return err
	}
	if n != size {
		return io.ErrUnexpectedEOF
	}
	if _, err = io.Copy(local, f); err != nil {
		return err
	}
	if string(local.Sum(nil)) != string(remote.Sum(nil)) {
		return errProbeUpgradeRestartDownload
	}
	return nil
}
