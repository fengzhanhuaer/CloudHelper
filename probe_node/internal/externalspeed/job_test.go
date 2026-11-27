package externalspeed

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestJobStopBusyAndNoFallback(t *testing.T) {
	var j Job
	started := make(chan string, 1)
	cleaned := make(chan struct{})
	factory := func(ctx context.Context, node string) (*http.Client, func(), []string, error) {
		started <- node
		<-ctx.Done()
		return nil, func() { close(cleaned) }, []string{"1", node}, errors.New("selected exit failed")
	}
	if err := j.Start("27", factory); err != nil {
		t.Fatal(err)
	}
	if node := <-started; node != "27" {
		t.Fatal(node)
	}
	if err := j.Start("", factory); err == nil {
		t.Fatal("parallel test allowed")
	}
	j.Stop()
	select {
	case <-cleaned:
	case <-time.After(time.Second):
		t.Fatal("cancel did not release route")
	}
	deadline := time.Now().Add(time.Second)
	for j.Snapshot().Running && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	r := j.Snapshot()
	if r.Running || r.OK || !r.Cancelled || r.Mode != "node" || r.NodeID != "27" || r.Error != "测速已停止" {
		t.Fatalf("result %+v", r)
	}
}
func TestDownloadReadWindowAndCancellation(t *testing.T) {
	for _, stop := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		reader, writer := io.Pipe()
		client := &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
			go func() {
				writer.Write(make([]byte, 4096))
				if stop {
					cancel()
				}
			}()
			return &http.Response{StatusCode: 200, Body: reader}, nil
		})}
		start := time.Now()
		r := Run(ctx, client, 64<<20, 30*time.Millisecond, nil)
		cancel()
		writer.Close()
		if time.Since(start) > time.Second || r.Bytes != 4096 || r.Running {
			t.Fatalf("unbounded result %+v", r)
		}
		if stop && !r.Cancelled {
			t.Fatalf("cancel result %+v", r)
		}
		if !stop && !r.OK {
			t.Fatalf("time limit result %+v", r)
		}
	}
}
func TestPublicSpeedTarget(t *testing.T) {
	for _, ip := range []string{"198.18.0.1", "127.0.0.1", "192.168.1.1", "100.64.0.1", "::1", "203.0.113.2"} {
		if PublicIPv4(ip) {
			t.Fatal(ip)
		}
	}
	if !PublicIPv4("1.1.1.1") {
		t.Fatal("public target rejected")
	}
}
