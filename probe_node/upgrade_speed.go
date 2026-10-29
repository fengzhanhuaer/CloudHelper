package main

import (
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"time"
)

const (
	probeUpgradeDownloadMinimumRate int64 = 10 * 1024
	probeUpgradeDownloadRateWindow        = 10 * time.Second
)

var errProbeUpgradeDownloadTooSlow = errors.New("download speed below minimum")

type probeUpgradeCountingWriter struct {
	io.Writer
	bytes *atomic.Int64
}

func (w probeUpgradeCountingWriter) Write(p []byte) (int, error) {
	n, err := w.Writer.Write(p)
	w.bytes.Add(int64(n))
	return n, err
}

// Observe complete windows after the response headers. A separate watchdog
// also cancels a blocked body read; completed small downloads never wait for it.
func copyProbeUpgradeWithMinimumRate(dst io.Writer, src io.Reader, minimum int64, window time.Duration, onSlow, onIdle func(), progress func(int64)) (int64, error) {
	var bytes atomic.Int64
	var slow atomic.Bool
	done, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(window)
		defer ticker.Stop()
		last, at := int64(0), time.Now()
		for {
			select {
			case <-done:
				return
			case now := <-ticker.C:
				current := bytes.Load()
				if float64(current-last)/now.Sub(at).Seconds() < float64(minimum) {
					slow.Store(true)
					if onSlow != nil {
						onSlow()
					}
					return
				}
				last, at = current, now
			}
		}
	}()
	n, err := copyProbeUpgradeWithProgressAndIdleTimeout(probeUpgradeCountingWriter{dst, &bytes}, src, probeUpgradeDownloadIdleTimeout, onIdle, progress)
	close(done)
	<-stopped
	if slow.Load() {
		return n, errors.Join(fmt.Errorf("%w: less than %d B/s for %s", errProbeUpgradeDownloadTooSlow, minimum, window), err)
	}
	return n, err
}
