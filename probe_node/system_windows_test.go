//go:build windows

package main

import "testing"

func TestWindowsSystemMetricsAvailable(t *testing.T) {
	snapshot, ok := readCPUSnapshot()
	if !ok {
		t.Fatal("expected windows CPU snapshot")
	}
	if snapshot.total == 0 || snapshot.idle > snapshot.total {
		t.Fatalf("unexpected CPU snapshot: %+v", snapshot)
	}

	memoryTotal, memoryUsed, _, _ := readLinuxMemInfo()
	if memoryTotal == 0 || memoryUsed == 0 || memoryUsed > memoryTotal {
		t.Fatalf("unexpected memory metrics: total=%d used=%d", memoryTotal, memoryUsed)
	}

	diskTotal, diskUsed := readDiskUsageRoot()
	if diskTotal == 0 || diskUsed == 0 || diskUsed > diskTotal {
		t.Fatalf("unexpected disk metrics: total=%d used=%d", diskTotal, diskUsed)
	}
}
