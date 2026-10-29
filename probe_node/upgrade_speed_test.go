package main

import (
	"bytes"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"
)

func TestUpgradeMinimumRateCancelsSlowOrStalledBody(t *testing.T) {
	for _, burst := range []bool{false, true} {
		t.Run(map[bool]string{false: "slow", true: "fast-then-stalled"}[burst], func(t *testing.T) {
			reader, writer := io.Pipe()
			defer reader.Close()
			defer writer.Close()
			go func() {
				data := []byte("x")
				if burst {
					data = bytes.Repeat(data, 65536)
				}
				writer.Write(data)
			}()
			var cancelled atomic.Bool
			n, err := copyProbeUpgradeWithMinimumRate(io.Discard, reader, 10240, 30*time.Millisecond, func() {
				cancelled.Store(true)
				reader.CloseWithError(errors.New("cancelled slow body"))
			}, nil, nil)
			if !cancelled.Load() || !errors.Is(err, errProbeUpgradeDownloadTooSlow) || !isProbeTransientHTTPError(err) || n == 0 {
				t.Fatalf("slow connection not cancelled/retryable: %d %v", n, err)
			}
		})
	}
}

func TestUpgradeMinimumRateDoesNotDelaySmallCompletedDownload(t *testing.T) {
	var cancelled atomic.Bool
	var dst bytes.Buffer
	n, err := copyProbeUpgradeWithMinimumRate(&dst, bytes.NewBufferString("ok"), 10240, time.Hour, func() { cancelled.Store(true) }, nil, nil)
	if err != nil || n != 2 || dst.String() != "ok" || cancelled.Load() {
		t.Fatal("completed download was interrupted", n, err)
	}
}

func TestUpgradeMinimumRateKeepsHealthyStream(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	go func() {
		defer writer.Close()
		for i := 0; i < 12; i++ {
			if _, err := writer.Write(make([]byte, 4096)); err != nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	n, err := copyProbeUpgradeWithMinimumRate(io.Discard, reader, 10240, 40*time.Millisecond, func() {
		reader.CloseWithError(errors.New("unexpected low speed"))
	}, nil, nil)
	if err != nil || n != 12*4096 {
		t.Fatal("healthy stream cancelled", n, err)
	}
}
