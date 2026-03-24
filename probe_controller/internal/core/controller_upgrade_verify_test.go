package core

import "testing"

func TestNormalizeControllerUpgradeVerifyDurationSec(t *testing.T) {
	tests := []struct {
		name string
		in   int
		want int
	}{
		{name: "too small", in: 0, want: minControllerUpgradeVerifyDurationSec},
		{name: "lower bound", in: minControllerUpgradeVerifyDurationSec, want: minControllerUpgradeVerifyDurationSec},
		{name: "middle", in: 19, want: 19},
		{name: "too large", in: maxControllerUpgradeVerifyDurationSec + 10, want: maxControllerUpgradeVerifyDurationSec},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeControllerUpgradeVerifyDurationSec(tc.in); got != tc.want {
				t.Fatalf("normalizeControllerUpgradeVerifyDurationSec(%d)=%d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseControllerUpgradeVerifyOptions(t *testing.T) {
	t.Run("disabled", func(t *testing.T) {
		opts, enabled, err := parseControllerUpgradeVerifyOptions([]string{"--listen=:15030"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if enabled {
			t.Fatalf("expected disabled verify mode, got enabled")
		}
		if opts.DurationSec != defaultControllerUpgradeVerifyDurationSec {
			t.Fatalf("unexpected default duration: %d", opts.DurationSec)
		}
	})

	t.Run("enabled with inline duration", func(t *testing.T) {
		opts, enabled, err := parseControllerUpgradeVerifyOptions([]string{"--upgrade-verify", "--upgrade-verify-duration=33"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !enabled {
			t.Fatalf("expected verify mode enabled")
		}
		if opts.DurationSec != 33 {
			t.Fatalf("unexpected duration: %d", opts.DurationSec)
		}
	})

	t.Run("enabled with separated duration", func(t *testing.T) {
		opts, enabled, err := parseControllerUpgradeVerifyOptions([]string{"--upgrade-verify-duration", "2"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !enabled {
			t.Fatalf("expected verify mode enabled")
		}
		if opts.DurationSec != minControllerUpgradeVerifyDurationSec {
			t.Fatalf("expected normalized duration=%d, got %d", minControllerUpgradeVerifyDurationSec, opts.DurationSec)
		}
	})

	t.Run("invalid duration", func(t *testing.T) {
		_, enabled, err := parseControllerUpgradeVerifyOptions([]string{"--upgrade-verify-duration=abc"})
		if !enabled {
			t.Fatalf("expected verify mode to be recognized as enabled")
		}
		if err == nil {
			t.Fatalf("expected parse error")
		}
	})
}

func TestTrimControllerUpgradeVerifyOutputForLog(t *testing.T) {
	cases := []struct {
		name  string
		raw   []byte
		limit int
		want  string
	}{
		{
			name:  "empty",
			raw:   []byte("   "),
			limit: 8,
			want:  "",
		},
		{
			name:  "within limit",
			raw:   []byte("hello"),
			limit: 8,
			want:  "hello",
		},
		{
			name:  "truncate with suffix",
			raw:   []byte("123456789"),
			limit: 8,
			want:  "12345...",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := trimControllerUpgradeVerifyOutputForLog(tc.raw, tc.limit)
			if got != tc.want {
				t.Fatalf("trimControllerUpgradeVerifyOutputForLog(%q, %d)=%q, want %q", string(tc.raw), tc.limit, got, tc.want)
			}
		})
	}
}
