package externalspeed

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

const MaxBytes = 64 << 20
const Duration = 10 * time.Second
const speedHostPath = "://speed.cloudflare.com/__down"

type Result struct {
	NodeID      string   `json:"node_id,omitempty"`
	Path        []string `json:"path,omitempty"`
	Running     bool     `json:"running"`
	OK          bool     `json:"ok"`
	Cancelled   bool     `json:"cancelled"`
	Mode        string   `json:"mode"`
	Source      string   `json:"source"`
	Protocol    string   `json:"protocol,omitempty"`
	Bytes       int64    `json:"bytes"`
	DurationMS  int64    `json:"duration_ms"`
	TotalMS     int64    `json:"total_ms"`
	Mbps        float64  `json:"mbps"`
	MBPerSecond float64  `json:"mb_per_second"`
	HTTPStatus  int      `json:"http_status"`
	StartedAt   string   `json:"started_at"`
	Error       string   `json:"error,omitempty"`
}

func Run(ctx context.Context, client *http.Client, maxBytes int64, duration time.Duration, publish func(Result)) Result {
	return run(ctx, client, "https", maxBytes, duration, publish)
}

func run(ctx context.Context, client *http.Client, protocol string, maxBytes int64, duration time.Duration, publish func(Result)) Result {
	started := time.Now()
	result := Result{Running: true, Mode: "direct", Source: "Cloudflare", Protocol: protocol, StartedAt: started.UTC().Format(time.RFC3339Nano)}
	var first, lastData time.Time
	update := func() {
		now := time.Now()
		result.TotalMS = now.Sub(started).Milliseconds()
		if !first.IsZero() {
			result.DurationMS = max(1, lastData.Sub(first).Milliseconds())
			result.Mbps = float64(result.Bytes) * 8 / float64(result.DurationMS) / 1000
			result.MBPerSecond = result.Mbps / 8
		}
		if publish != nil {
			publish(result)
		}
	}
	finish := func(err error) Result {
		result.Running = false
		if errors.Is(ctx.Err(), context.Canceled) {
			result.Cancelled = true
			result.Error = "测速已停止"
		} else if err != nil {
			result.Error = err.Error()
		} else {
			result.OK = result.Bytes > 0
			if !result.OK {
				result.Error = "未收到测速数据"
			}
		}
		update()
		return result
	}
	if ctx.Err() != nil {
		return finish(ctx.Err())
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, protocol+speedHostPath+"?bytes="+strconv.FormatInt(maxBytes, 10), nil)
	if err != nil {
		return finish(err)
	}
	request.Header.Set("Accept-Encoding", "identity")
	request.Header.Set("Cache-Control", "no-cache")
	request.Header.Set("User-Agent", "CloudHelper-external-speed")
	response, err := client.Do(request)
	if err != nil {
		return finish(err)
	}
	defer response.Body.Close()
	result.HTTPStatus = response.StatusCode
	if response.StatusCode != http.StatusOK {
		return finish(fmt.Errorf("测速源返回 HTTP %d", response.StatusCode))
	}
	// A detached timer closes a blocked read when the transfer window expires.
	done := make(chan struct{})
	windowEnded := make(chan struct{})
	defer close(done)
	go func() {
		timer := time.NewTimer(duration)
		defer timer.Stop()
		select {
		case <-done:
		case <-ctx.Done():
			response.Body.Close()
		case <-timer.C:
			close(windowEnded)
			response.Body.Close()
		}
	}()
	buffer := make([]byte, 32<<10)
	last := time.Now()
	for result.Bytes < maxBytes {
		want := int64(len(buffer))
		if remain := maxBytes - result.Bytes; remain < want {
			want = remain
		}
		n, readErr := response.Body.Read(buffer[:want])
		if n > 0 {
			if first.IsZero() {
				first = time.Now()
			}
			lastData = time.Now()
			result.Bytes += int64(n)
		}
		if time.Since(last) >= 250*time.Millisecond {
			update()
			last = time.Now()
		}
		if ctx.Err() != nil {
			return finish(ctx.Err())
		}
		if readErr != nil {
			select {
			case <-windowEnded:
				return finish(nil)
			default:
			}
			if errors.Is(readErr, io.EOF) {
				return finish(nil)
			}
			return finish(readErr)
		}
		select {
		case <-windowEnded:
			return finish(nil)
		default:
		}
	}
	return finish(nil)
}
