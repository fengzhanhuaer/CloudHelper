package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultControllerUpgradeVerifyDurationSec = 15
	minControllerUpgradeVerifyDurationSec     = 5
	maxControllerUpgradeVerifyDurationSec     = 90
)

type controllerUpgradeVerifyOptions struct {
	DurationSec int
}

func normalizeControllerUpgradeVerifyDurationSec(sec int) int {
	if sec < minControllerUpgradeVerifyDurationSec {
		return minControllerUpgradeVerifyDurationSec
	}
	if sec > maxControllerUpgradeVerifyDurationSec {
		return maxControllerUpgradeVerifyDurationSec
	}
	return sec
}

func runControllerUpgradeVerifyModeFromArgs() (bool, error) {
	options, enabled, err := parseControllerUpgradeVerifyOptions(os.Args[1:])
	if !enabled {
		return false, err
	}
	if err != nil {
		return true, err
	}
	return true, runControllerUpgradeVerifyMode(options)
}

func parseControllerUpgradeVerifyOptions(args []string) (controllerUpgradeVerifyOptions, bool, error) {
	opts := controllerUpgradeVerifyOptions{DurationSec: defaultControllerUpgradeVerifyDurationSec}
	enabled := false

	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		switch {
		case arg == "--upgrade-verify":
			enabled = true
		case strings.HasPrefix(arg, "--upgrade-verify-duration="):
			raw := strings.TrimSpace(strings.TrimPrefix(arg, "--upgrade-verify-duration="))
			v, err := strconv.Atoi(raw)
			if err != nil {
				return opts, true, fmt.Errorf("invalid --upgrade-verify-duration value %q", raw)
			}
			opts.DurationSec = v
			enabled = true
		case arg == "--upgrade-verify-duration":
			if i+1 >= len(args) {
				return opts, true, fmt.Errorf("missing value for --upgrade-verify-duration")
			}
			raw := strings.TrimSpace(args[i+1])
			v, err := strconv.Atoi(raw)
			if err != nil {
				return opts, true, fmt.Errorf("invalid --upgrade-verify-duration value %q", raw)
			}
			opts.DurationSec = v
			enabled = true
			i++
		}
	}

	if !enabled {
		return opts, false, nil
	}
	opts.DurationSec = normalizeControllerUpgradeVerifyDurationSec(opts.DurationSec)
	return opts, true, nil
}

func runControllerUpgradeVerifyMode(options controllerUpgradeVerifyOptions) error {
	durationSec := normalizeControllerUpgradeVerifyDurationSec(options.DurationSec)
	stableDuration := time.Duration(durationSec) * time.Second
	log.Printf("controller upgrade verify mode started: version=%s duration=%ds", BuildVersion, durationSec)

	// Smoke checks used by the running service path.
	_ = currentControllerVersion()
	_ = NewMux()

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

	if err := waitForControllerUpgradeVerifyHealth(listener.Addr().String(), 3*time.Second); err != nil {
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

	log.Printf("controller upgrade verify mode passed: duration=%ds", durationSec)
	return nil
}

func waitForControllerUpgradeVerifyHealth(addr string, timeout time.Duration) error {
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
