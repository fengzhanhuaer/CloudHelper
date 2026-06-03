package core

import (
	"os"
	"testing"
	"time"
)

// chdirTemp switches the working directory to a per-test temp dir so relative
// artifacts (./temp/tg/...) written by history/state helpers stay contained.
func chdirTemp(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
}

func TestRenewalKeyToFire(t *testing.T) {
	thresholds := []int{7, 3, 1}
	none := func(string) bool { return false }

	if _, fire := renewalKeyToFire(8, thresholds, none); fire {
		t.Fatal("daysUntil=8 should not fire")
	}
	if key, fire := renewalKeyToFire(7, thresholds, none); !fire || key != "7" {
		t.Fatalf("daysUntil=7 expected fire 7, got %q %v", key, fire)
	}
	// Added mid-window: most urgent unfired threshold whose value >= daysUntil.
	if key, fire := renewalKeyToFire(5, thresholds, none); !fire || key != "7" {
		t.Fatalf("daysUntil=5 expected fire 7, got %q %v", key, fire)
	}
	fired7 := func(k string) bool { return k == "7" }
	if key, fire := renewalKeyToFire(2, thresholds, fired7); !fire || key != "3" {
		t.Fatalf("daysUntil=2 with 7 fired expected 3, got %q %v", key, fire)
	}
	fired73 := func(k string) bool { return k == "7" || k == "3" }
	if key, fire := renewalKeyToFire(1, thresholds, fired73); !fire || key != "1" {
		t.Fatalf("daysUntil=1 with 7,3 fired expected 1, got %q %v", key, fire)
	}
	firedAll := func(k string) bool { return k == "7" || k == "3" || k == "1" }
	if _, fire := renewalKeyToFire(1, thresholds, firedAll); fire {
		t.Fatal("daysUntil=1 with all fired should not fire")
	}
	if key, fire := renewalKeyToFire(-1, thresholds, none); !fire || key != "expired" {
		t.Fatalf("daysUntil=-1 expected expired, got %q %v", key, fire)
	}
	firedExpired := func(k string) bool { return k == "expired" }
	if _, fire := renewalKeyToFire(-1, thresholds, firedExpired); fire {
		t.Fatal("daysUntil=-1 with expired fired should not fire")
	}
}

func TestParseProbeExpireAt(t *testing.T) {
	if _, ok := parseProbeExpireAt(""); ok {
		t.Fatal("empty should not parse")
	}
	if _, ok := parseProbeExpireAt("not-a-date"); ok {
		t.Fatal("garbage should not parse")
	}
	day, ok := parseProbeExpireAt("2026-06-10")
	if !ok {
		t.Fatal("YYYY-MM-DD should parse")
	}
	if day.Hour() != 23 || day.Minute() != 59 {
		t.Fatalf("expected end-of-day, got %v", day)
	}
	if _, ok := parseProbeExpireAt("2026-06-10T12:00:00Z"); !ok {
		t.Fatal("RFC3339 should parse")
	}
}

func TestRenewalNotifyStateRoundTrip(t *testing.T) {
	chdirTemp(t)

	state := renewalNotifyState{Nodes: map[string]map[string]string{
		"1": {"7": "2026-06-10"},
	}}
	saveRenewalNotifyState(state)

	loaded := loadRenewalNotifyState()
	if loaded.Nodes["1"]["7"] != "2026-06-10" {
		t.Fatalf("round trip mismatch: %+v", loaded.Nodes)
	}
}

func TestNextDailyRunAt(t *testing.T) {
	from := time.Date(2026, 6, 3, 10, 0, 0, 0, time.Local)
	// Hour already passed today -> tomorrow.
	next := nextDailyRunAt(9, from)
	if next.Day() != 4 || next.Hour() != 9 {
		t.Fatalf("expected next day 09:00, got %v", next)
	}
	// Hour later today -> same day.
	next = nextDailyRunAt(15, from)
	if next.Day() != 3 || next.Hour() != 15 {
		t.Fatalf("expected same day 15:00, got %v", next)
	}
}
