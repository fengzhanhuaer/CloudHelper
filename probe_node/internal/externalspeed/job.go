package externalspeed

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"
)

// Factory prepares a route for this test only. It must never fall back from a
// requested exit to direct access. Cleanup releases the temporary connection.
type Factory func(context.Context, string) (*http.Client, func(), []string, error)
type Job struct {
	mu     sync.Mutex
	result Result
	cancel context.CancelFunc
}

func (j *Job) Snapshot() Result {
	j.mu.Lock()
	defer j.mu.Unlock()
	r := j.result
	r.Path = append([]string(nil), r.Path...)
	return r
}
func (j *Job) Stop() {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.cancel != nil {
		j.cancel()
	}
}
func (j *Job) Start(node string, factory Factory) error {
	return j.StartProtocol(node, "https", factory)
}

func (j *Job) StartProtocol(node, protocol string, factory Factory) error {
	switch protocol {
	case "http", "https":
	default:
		return errors.New("无效测速协议")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.result.Running {
		return errors.New("已有外部测速正在运行")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	j.cancel = cancel
	mode := "direct"
	if node != "" {
		mode = "node"
	}
	j.result = Result{Running: true, NodeID: node, Mode: mode, Source: "Cloudflare", Protocol: protocol}
	go func() {
		defer cancel()
		client, cleanup, path, err := factory(ctx, node)
		defer func() {
			if cleanup != nil {
				cleanup()
			}
			j.mu.Lock()
			j.result.Running = false
			j.cancel = nil
			j.mu.Unlock()
		}()
		publish := func(r Result) {
			r.Running = true
			r.Mode = mode
			r.Protocol = protocol
			r.NodeID = node
			r.Path = append([]string(nil), path...)
			j.mu.Lock()
			j.result = r
			j.mu.Unlock()
		}
		if err != nil {
			stopped := errors.Is(ctx.Err(), context.Canceled)
			message := err.Error()
			if stopped {
				message = "测速已停止"
			}
			publish(Result{Error: message, Cancelled: stopped, Source: "Cloudflare"})
			return
		}
		defer client.CloseIdleConnections()
		run(ctx, client, protocol, MaxBytes, Duration, publish)
	}()
	return nil
}

func Client(dial func(context.Context, string, string) (net.Conn, error)) *http.Client {
	return &http.Client{Transport: &http.Transport{Proxy: nil, DialContext: dial, DisableCompression: true, DisableKeepAlives: true, ForceAttemptHTTP2: false, TLSHandshakeTimeout: 6 * time.Second, ResponseHeaderTimeout: 8 * time.Second}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
