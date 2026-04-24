package main

import (
	"net"
	"net/http"
	"testing"
	"time"
)

func TestNormalizeProbeLocalListenAddr(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: ""},
		{name: "invalid format", in: "127.0.0.1", want: ""},
		{name: "invalid port", in: "127.0.0.1:0", want: ""},
		{name: "invalid big port", in: "127.0.0.1:99999", want: ""},
		{name: "valid ipv4", in: "127.0.0.1:16032", want: "127.0.0.1:16032"},
		{name: "trim whitespace", in: " 127.0.0.1:16032 ", want: "127.0.0.1:16032"},
		{name: "empty host fallback", in: ":16032", want: "127.0.0.1:16032"},
		{name: "valid localhost", in: "localhost:16032", want: "localhost:16032"},
		{name: "valid ipv6", in: "[::1]:16032", want: "[::1]:16032"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeProbeLocalListenAddr(tc.in)
			if got != tc.want {
				t.Fatalf("normalizeProbeLocalListenAddr(%q)=%q want=%q", tc.in, got, tc.want)
			}
		})
	}
}

func TestResolveProbeLocalListenAddrPriority(t *testing.T) {
	t.Run("explicit takes priority", func(t *testing.T) {
		t.Setenv("PROBE_LOCAL_LISTEN", "127.0.0.1:19999")
		got := resolveProbeLocalListenAddr("127.0.0.1:18888")
		if got != "127.0.0.1:18888" {
			t.Fatalf("resolve explicit priority got=%q", got)
		}
	})

	t.Run("env used when explicit empty", func(t *testing.T) {
		t.Setenv("PROBE_LOCAL_LISTEN", "127.0.0.1:17777")
		got := resolveProbeLocalListenAddr("")
		if got != "127.0.0.1:17777" {
			t.Fatalf("resolve env fallback got=%q", got)
		}
	})

	t.Run("default when explicit and env invalid", func(t *testing.T) {
		t.Setenv("PROBE_LOCAL_LISTEN", "bad")
		got := resolveProbeLocalListenAddr("invalid")
		if got != probeLocalListenAddrDefault {
			t.Fatalf("resolve default fallback got=%q want=%q", got, probeLocalListenAddrDefault)
		}
	})
}

func TestResolveProbeLocalListenAddrIgnoresProbeNodeListen(t *testing.T) {
	t.Run("default local listen does not depend on probe node listen", func(t *testing.T) {
		t.Setenv("PROBE_NODE_LISTEN", ":26030")
		t.Setenv("PROBE_LOCAL_LISTEN", "")
		got := resolveProbeLocalListenAddr("")
		if got != probeLocalListenAddrDefault {
			t.Fatalf("local listen should stay default, got=%q want=%q", got, probeLocalListenAddrDefault)
		}
	})

	t.Run("local env still takes priority over probe node listen", func(t *testing.T) {
		t.Setenv("PROBE_NODE_LISTEN", ":26030")
		t.Setenv("PROBE_LOCAL_LISTEN", "127.0.0.1:26666")
		got := resolveProbeLocalListenAddr("")
		if got != "127.0.0.1:26666" {
			t.Fatalf("local listen should use PROBE_LOCAL_LISTEN, got=%q", got)
		}
	})
}

func reserveProbeLocalListenAddrForTest(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve listen addr failed: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func cleanupProbeLocalConsoleServerForTest(t *testing.T) {
	t.Helper()
	probeLocalConsoleState.mu.Lock()
	srv := probeLocalConsoleState.server
	probeLocalConsoleState.mu.Unlock()
	if srv != nil {
		_ = srv.Close()
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		probeLocalConsoleState.mu.Lock()
		srv := probeLocalConsoleState.server
		listen := probeLocalConsoleState.listenAddr
		probeLocalConsoleState.mu.Unlock()
		if srv == nil && listen == "" {
			return
		}
		if time.Now().After(deadline) {
			probeLocalConsoleState.mu.Lock()
			probeLocalConsoleState.server = nil
			probeLocalConsoleState.listenAddr = ""
			probeLocalConsoleState.mu.Unlock()
			t.Fatalf("probe local console server cleanup timeout")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestResolveProbeLocalConsoleURLDefault(t *testing.T) {
	cleanupProbeLocalConsoleServerForTest(t)
	got := resolveProbeLocalConsoleURL()
	want := "http://" + probeLocalListenAddrDefault
	if got != want {
		t.Fatalf("resolveProbeLocalConsoleURL default=%q want=%q", got, want)
	}
}

func TestStartProbeLocalConsoleServerAndCurrentListen(t *testing.T) {
	cleanupProbeLocalConsoleServerForTest(t)
	addr := reserveProbeLocalListenAddrForTest(t)
	handler := http.NewServeMux()
	handler.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	if err := startProbeLocalConsoleServer(handler, addr); err != nil {
		t.Fatalf("startProbeLocalConsoleServer failed: %v", err)
	}
	t.Cleanup(func() { cleanupProbeLocalConsoleServerForTest(t) })

	if got := currentProbeLocalConsoleListen(); got != addr {
		t.Fatalf("currentProbeLocalConsoleListen=%q want=%q", got, addr)
	}
	if got := resolveProbeLocalConsoleURL(); got != "http://"+addr {
		t.Fatalf("resolveProbeLocalConsoleURL=%q want=%q", got, "http://"+addr)
	}

	resp, err := http.Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatalf("healthz request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("healthz status=%d", resp.StatusCode)
	}

	anotherAddr := reserveProbeLocalListenAddrForTest(t)
	if err := startProbeLocalConsoleServer(handler, anotherAddr); err != nil {
		t.Fatalf("start second time failed: %v", err)
	}
	if got := currentProbeLocalConsoleListen(); got != addr {
		t.Fatalf("second start should keep original addr, got=%q want=%q", got, addr)
	}
}

func TestStartProbeLocalConsoleServerNilHandler(t *testing.T) {
	cleanupProbeLocalConsoleServerForTest(t)
	if err := startProbeLocalConsoleServer(nil, "127.0.0.1:16033"); err == nil {
		t.Fatalf("startProbeLocalConsoleServer should reject nil handler")
	}
}
