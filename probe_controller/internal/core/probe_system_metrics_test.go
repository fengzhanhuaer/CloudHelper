package core

import (
	"encoding/json"
	"math"
	"testing"
)

func TestProbeSystemMetricsDecodeCapacityAndMissingSamples(t *testing.T) {
	for _, tc := range []struct {
		name, payload           string
		cpu, memory, swap, disk float64
	}{
		{"legacy router", `{"memory_total_bytes":256733184,"memory_used_bytes":196067328}`, -1, 196067328.0 / 256733184 * 100, -1, -1},
		{"idle and unused", `{"cpu_percent":0,"memory_total_bytes":1024,"memory_used_bytes":0}`, 0, 0, -1, -1},
		{"inconsistent percentages", `{"cpu_percent":12.5,"memory_total_bytes":1024,"memory_used_bytes":768,"memory_used_percent":0,"swap_total_bytes":1024,"swap_used_bytes":512,"swap_used_percent":99,"disk_total_bytes":1000,"disk_used_bytes":300}`, 12.5, 75, 50, 30},
		{"missing", `{}`, -1, -1, -1, -1},
		{"null", `{"cpu_percent":null}`, -1, -1, -1, -1},
		{"bounds", `{"cpu_percent":150,"memory_total_bytes":100,"memory_used_bytes":200}`, 100, 100, -1, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Reuse the value to ensure omitted fields cannot retain old samples.
			metrics := probeSystemMetrics{CPUPercent: 55, MemoryTotalBytes: 999, MemoryUsedPercent: 55}
			if err := json.Unmarshal([]byte(tc.payload), &metrics); err != nil {
				t.Fatal(err)
			}
			got := []float64{metrics.CPUPercent, metrics.MemoryUsedPercent, metrics.SwapUsedPercent, metrics.DiskUsedPercent}
			want := []float64{tc.cpu, tc.memory, tc.swap, tc.disk}
			for i := range got {
				if math.Abs(got[i]-want[i]) > 0.000001 {
					t.Fatalf("metrics[%d]=%v want %v", i, got[i], want[i])
				}
			}
		})
	}
}

func TestProbeSystemMetricsDecodePreservesTraffic(t *testing.T) {
	var metrics probeSystemMetrics
	if err := json.Unmarshal([]byte(`{"network_traffic_available":true,"network_rx_bytes":18446744073709551615,"network_tx_bytes":123}`), &metrics); err != nil {
		t.Fatal(err)
	}
	if !metrics.NetworkAvailable || metrics.NetworkRXBytes != math.MaxUint64 || metrics.NetworkTXBytes != 123 {
		t.Fatalf("traffic altered: %+v", metrics)
	}
	if err := json.Unmarshal([]byte(`{"memory_total_bytes":-1}`), &metrics); err == nil {
		t.Fatal("invalid unsigned capacity accepted")
	}
}
