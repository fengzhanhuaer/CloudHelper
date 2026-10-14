package core

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAccumulateProbeTrafficFromReport(t *testing.T) {
	restoreDir := setProbeTrafficStoreDirForTest(t.TempDir())
	t.Cleanup(restoreDir)

	if err := accumulateProbeTrafficFromReport("7", 1000, 2000, "2026-10-12T23:59:00Z"); err != nil {
		t.Fatalf("save baseline: %v", err)
	}
	if err := accumulateProbeTrafficFromReport("7", 1500, 2700, "2026-10-13T00:01:00Z"); err != nil {
		t.Fatalf("save delta: %v", err)
	}
	usage, err := loadProbeTrafficUsage("7", 1, time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("load usage: %v", err)
	}
	if !usage.Available || usage.TotalRXBytes != 500 || usage.TotalTXBytes != 700 {
		t.Fatalf("total usage=%+v, want RX=500 TX=700", usage)
	}
	if usage.MonthRXBytes != 500 || usage.MonthTXBytes != 700 || usage.PeriodStart != "2026-10-01" {
		t.Fatalf("monthly usage=%+v, want RX=500 TX=700 from 2026-10-01", usage)
	}

	if err := accumulateProbeTrafficFromReport("7", 50, 90, "2026-10-13T00:02:00Z"); err != nil {
		t.Fatalf("save reset counter: %v", err)
	}
	usage, err = loadProbeTrafficUsage("7", 1, time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("reload usage: %v", err)
	}
	if usage.TotalRXBytes != 550 || usage.TotalTXBytes != 790 || usage.MonthRXBytes != 550 || usage.MonthTXBytes != 790 {
		t.Fatalf("usage after counter reset=%+v, want RX=550 TX=790", usage)
	}
}

func TestAccumulateProbeTrafficResetsOldInterfaceScope(t *testing.T) {
	dir := t.TempDir()
	restoreDir := setProbeTrafficStoreDirForTest(dir)
	t.Cleanup(restoreDir)
	oldRecord := `{"version":1,"available":true,"last_rx":9000,"last_tx":12000,"last_sample_at":"2026-10-13T00:00:00Z","total_rx":8000,"total_tx":11000,"daily":[{"date":"2026-10-13","rx":8000,"tx":11000}]}`
	if err := os.WriteFile(filepath.Join(dir, "node_8.json"), []byte(oldRecord), 0o600); err != nil {
		t.Fatalf("write old scope record: %v", err)
	}
	if err := accumulateProbeTrafficFromReport("8", 100, 200, "2026-10-14T00:00:00Z"); err != nil {
		t.Fatalf("save physical baseline: %v", err)
	}
	usage, err := loadProbeTrafficUsage("8", 1, time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("load physical usage: %v", err)
	}
	if !usage.Available || usage.TotalRXBytes != 0 || usage.TotalTXBytes != 0 || usage.MonthRXBytes != 0 || usage.MonthTXBytes != 0 {
		t.Fatalf("old interface scope was retained: %+v", usage)
	}
}

func TestProbeTrafficPeriodStartClampsShortMonths(t *testing.T) {
	cases := []struct {
		name     string
		now      time.Time
		startDay int
		want     string
	}{
		{name: "default first day", now: time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC), startDay: 1, want: "2026-10-01"},
		{name: "day 31 in short month", now: time.Date(2026, 2, 28, 12, 0, 0, 0, time.UTC), startDay: 31, want: "2026-02-28"},
		{name: "before this month's start", now: time.Date(2026, 3, 30, 12, 0, 0, 0, time.UTC), startDay: 31, want: "2026-02-28"},
		{name: "invalid defaults to one", now: time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC), startDay: 0, want: "2026-10-01"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := probeTrafficPeriodStart(tc.now, tc.startDay).Format("2006-01-02")
			if got != tc.want {
				t.Fatalf("period start=%s want %s", got, tc.want)
			}
		})
	}
}
