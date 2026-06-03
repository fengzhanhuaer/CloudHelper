package main

import "testing"

func TestProbeLocalListenConfig(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	t.Setenv("PROBE_LOCAL_LISTEN", "")

	// No config file yet -> empty configured addr.
	if got := resolveProbeLocalConfiguredListenAddr(); got != "" {
		t.Fatalf("expected empty configured addr, got %q", got)
	}

	// ensure writes defaults into the file.
	ensureProbeLocalListenConfigDefaults()
	if got := resolveProbeLocalConfiguredListenAddr(); got != probeLocalListenAddrDefault {
		t.Fatalf("expected default configured addr %q, got %q", probeLocalListenAddrDefault, got)
	}
	if got := resolveProbeLocalListenAddr(""); got != probeLocalListenAddrDefault {
		t.Fatalf("resolve should use config default, got %q", got)
	}

	// A custom non-loopback config is honored end-to-end.
	state, _, err := loadProbeLocalAuthStateRaw()
	if err != nil {
		t.Fatal(err)
	}
	state.ListenIP = "0.0.0.0"
	state.ListenPort = 18080
	if err := persistProbeLocalAuthState(state); err != nil {
		t.Fatal(err)
	}
	if got := resolveProbeLocalConfiguredListenAddr(); got != "0.0.0.0:18080" {
		t.Fatalf("expected configured addr 0.0.0.0:18080, got %q", got)
	}
	if got := resolveProbeLocalListenAddr(""); got != "0.0.0.0:18080" {
		t.Fatalf("resolve should use configured addr, got %q", got)
	}

	// explicit and env still override the config.
	t.Setenv("PROBE_LOCAL_LISTEN", "127.0.0.1:17777")
	if got := resolveProbeLocalListenAddr(""); got != "127.0.0.1:17777" {
		t.Fatalf("env should override config, got %q", got)
	}
	if got := resolveProbeLocalListenAddr("127.0.0.1:18888"); got != "127.0.0.1:18888" {
		t.Fatalf("explicit should override config, got %q", got)
	}
}

func TestProbeLocalListenConfigPreservesAuthFields(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())

	// Simulate an already-registered node.
	original := probeLocalAuthState{
		Registered:   true,
		Username:     "admin",
		PasswordHash: "hash",
		PasswordSalt: "salt",
	}
	if err := persistProbeLocalAuthState(original); err != nil {
		t.Fatal(err)
	}

	ensureProbeLocalListenConfigDefaults()

	state, existed, err := loadProbeLocalAuthStateRaw()
	if err != nil || !existed {
		t.Fatalf("load raw failed: existed=%v err=%v", existed, err)
	}
	if !state.Registered || state.Username != "admin" || state.PasswordHash != "hash" || state.PasswordSalt != "salt" {
		t.Fatalf("auth fields not preserved: %+v", state)
	}
	if state.ListenIP != probeLocalListenDefaultHost || state.ListenPort != probeLocalListenDefaultPort {
		t.Fatalf("listen defaults not written: ip=%q port=%d", state.ListenIP, state.ListenPort)
	}
}

func TestIsProbeLocalLoopbackHost(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1":    true,
		"::1":          true,
		"localhost":    true,
		"0.0.0.0":      false,
		"192.168.1.10": false,
		"":             false,
	}
	for host, want := range cases {
		if got := isProbeLocalLoopbackHost(host); got != want {
			t.Fatalf("isProbeLocalLoopbackHost(%q)=%v want=%v", host, got, want)
		}
	}
}
