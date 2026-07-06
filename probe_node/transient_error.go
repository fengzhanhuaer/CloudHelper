package main

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
)

func isProbeTransientHTTPError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	if errors.Is(err, errProbeUpgradeDownloadIdleTimeout) {
		return true
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr != nil {
		return netErr.Timeout()
	}
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(text, "timeout") ||
		strings.Contains(text, "temporarily unavailable") ||
		strings.Contains(text, "unexpected eof") ||
		strings.Contains(text, "connection reset") ||
		strings.Contains(text, "connection refused") ||
		strings.Contains(text, "broken pipe") ||
		strings.Contains(text, "server closed idle connection")
}
