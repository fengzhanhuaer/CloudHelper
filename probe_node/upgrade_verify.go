package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"
)

const (
	defaultUpgradeVerifyDurationSec = 20
	minUpgradeVerifyDurationSec     = 5
	maxUpgradeVerifyDurationSec     = 90
)

func normalizeUpgradeVerifyDurationSec(sec int) int {
	if sec < minUpgradeVerifyDurationSec {
		return minUpgradeVerifyDurationSec
	}
	if sec > maxUpgradeVerifyDurationSec {
		return maxUpgradeVerifyDurationSec
	}
	return sec
}

func runProbeUpgradeVerifyMode(options probeLaunchOptions) error {
	durationSec := normalizeUpgradeVerifyDurationSec(options.UpgradeVerifyDurationSec)
	stableDuration := time.Duration(durationSec) * time.Second
	log.Printf("probe upgrade verify mode started: version=%s duration=%ds", BuildVersion, durationSec)

	// Run a small in-process smoke check before opening a short-lived health endpoint.
	_, _ = collectIPs()
	_ = collectSystemStatus(&cpuSampler{})

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		_, _ = w.Write([]byte("ok\n"))
	})

	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 3 * time.Second,
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("open verify listener failed: %w", err)
	}
	defer listener.Close()

	serverErrCh := make(chan error, 1)
	go func() {
		err := server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			serverErrCh <- nil
			return
		}
		serverErrCh <- err
	}()

	if err := waitForProbeUpgradeVerifyHealth(listener.Addr().String(), 3*time.Second); err != nil {
		_ = server.Close()
		<-serverErrCh
		return err
	}

	timer := time.NewTimer(stableDuration)
	defer timer.Stop()
	select {
	case err := <-serverErrCh:
		if err == nil {
			return fmt.Errorf("verify process exited before stability window")
		}
		return fmt.Errorf("verify process exited early: %w", err)
	case <-timer.C:
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		return fmt.Errorf("shutdown verify server failed: %w", err)
	}
	if err := <-serverErrCh; err != nil {
		return fmt.Errorf("verify server wait failed: %w", err)
	}

	log.Printf("probe upgrade verify mode passed: duration=%ds", durationSec)
	return nil
}

func waitForProbeUpgradeVerifyHealth(addr string, timeout time.Duration) error {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return fmt.Errorf("verify listener address is empty")
	}
	if timeout <= 0 {
		timeout = 3 * time.Second
	}

	client := &http.Client{
		Timeout: 700 * time.Millisecond,
		Transport: &http.Transport{
			Proxy: nil,
		},
	}
	defer client.CloseIdleConnections()

	url := "http://" + addr + "/healthz"
	deadline := time.Now().Add(timeout)
	lastErr := error(nil)

	for {
		resp, err := client.Get(url)
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			lastErr = fmt.Errorf("status=%d", resp.StatusCode)
		} else {
			lastErr = err
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("verify health endpoint unavailable: %w", lastErr)
		}
		time.Sleep(120 * time.Millisecond)
	}
}
