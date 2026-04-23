package main

import "testing"

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
