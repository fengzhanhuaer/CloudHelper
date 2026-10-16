package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

const probeProxyDownloadRetries = 4

func waitProbeProxyDownloadRetry(ctx context.Context, attempt int) error {
	timer := time.NewTimer(time.Duration(attempt) * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func retryableProbeProxyDownloadStatus(code int) bool {
	return code == 408 || code == 429 || code == 500 || code == 502 || code == 503 || code == 504 || code == 520 || code == 522 || code == 524
}

func retryableProbeProxyDownloadError(err error) bool {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var networkError net.Error
	return errors.As(err, &networkError)
}

func doProbeProxyDownloadWithRetry(req *http.Request) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		resp, err := doProbeProxyDownload(req.Clone(req.Context()))
		retry := retryableProbeProxyDownloadError(err)
		if err == nil {
			retry = retryableProbeProxyDownloadStatus(resp.StatusCode)
		}
		if !retry || attempt >= probeProxyDownloadRetries || req.Context().Err() != nil {
			return resp, err
		}
		if resp != nil {
			resp.Body.Close()
		}
		if err := waitProbeProxyDownloadRetry(req.Context(), attempt+1); err != nil {
			return nil, err
		}
	}
}

type probeProxyFlushWriter struct {
	w   http.ResponseWriter
	err error
}

func (f *probeProxyFlushWriter) Write(p []byte) (int, error) {
	n, err := f.w.Write(p)
	if err == nil {
		flushErr := http.NewResponseController(f.w).Flush()
		if flushErr != nil && !errors.Is(flushErr, http.ErrNotSupported) {
			err = flushErr
		}
	}
	f.err = err
	return n, err
}

func parseProbeProxyContentRange(raw string) (start, end, total int64, err error) {
	n, e := fmt.Sscanf(strings.TrimSpace(raw), "bytes %d-%d/%d", &start, &end, &total)
	if e != nil || n != 3 || start < 0 || end < start || total <= end {
		return 0, 0, 0, errors.New("invalid upstream Content-Range")
	}
	return
}

// Resume only a stable, sized object. No entire asset or disk cache is needed.
func streamProbeProxyDownload(w http.ResponseWriter, req *http.Request, initial *http.Response) error {
	start, end, total := int64(0), initial.ContentLength-1, initial.ContentLength
	if initial.StatusCode == http.StatusPartialContent {
		var err error
		start, end, total, err = parseProbeProxyContentRange(initial.Header.Get("Content-Range"))
		if err != nil {
			return err
		}
	}
	validator := strings.TrimSpace(initial.Header.Get("ETag"))
	if strings.HasPrefix(validator, "W/") {
		validator = ""
	}
	if validator == "" {
		validator = strings.TrimSpace(initial.Header.Get("Last-Modified"))
	}
	resp := initial
	defer func() { resp.Body.Close() }()
	offset := start
	writer := &probeProxyFlushWriter{w: w}
	buffer := make([]byte, 64*1024)
	// Send headers before waiting for slow upstream body bytes.
	if err := http.NewResponseController(w).Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	for attempt := 0; ; attempt++ {
		n, err := io.CopyBuffer(writer, resp.Body, buffer)
		offset += n
		if err == nil && (total < 0 || offset == end+1) {
			return nil
		}
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		resp.Body.Close()
		if writer.err != nil || req.Context().Err() != nil || attempt >= probeProxyDownloadRetries || total < 0 || offset > end || validator == "" || !retryableProbeProxyDownloadError(err) {
			return err
		}
		if err := waitProbeProxyDownloadRetry(req.Context(), attempt+1); err != nil {
			return err
		}
		next := req.Clone(req.Context())
		next.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, end))
		next.Header.Set("If-Range", validator)
		resumed, e := doProbeProxyDownloadWithRetry(next)
		if e != nil {
			return e
		}
		resp = resumed
		currentValidator := strings.TrimSpace(resp.Header.Get("ETag"))
		if strings.HasPrefix(currentValidator, "W/") || currentValidator == "" {
			currentValidator = strings.TrimSpace(resp.Header.Get("Last-Modified"))
		}
		a, b, size, rangeErr := parseProbeProxyContentRange(resp.Header.Get("Content-Range"))
		if resp.StatusCode != http.StatusPartialContent || rangeErr != nil || a != offset || b != end || size != total || currentValidator != validator {
			return errors.New("upstream resume returned a different object or range")
		}
	}
}
