package main

import "testing"

func TestProbeNetworkMonitorLatencyDoesNotParseRunDuration(t *testing.T) {
	for _, output := range []string{
		"1 packets transmitted, 0 received, 100% packet loss, time 0ms",
		"From 192.0.2.1 icmp_seq=1 Destination Host Unreachable\n1 packets transmitted, 0 received, +1 errors, 100% packet loss, time 3000ms",
		"Request timed out.\nPackets: Sent = 1, Received = 0, Lost = 1 (100% loss)",
		"ping: socket: Operation not permitted",
	} {
		if latency, ok := parseProbeNetworkMonitorLatency(output); ok {
			t.Errorf("failed ping became a successful RTT (%v): %q", latency, output)
		}
	}
}

func TestProbeNetworkMonitorLatencyReplyFormats(t *testing.T) {
	for _, tc := range []struct {
		name, output string
		want         float64
	}{
		{"linux", "64 bytes from 192.0.2.1: icmp_seq=1 ttl=64 time=12.3 ms\n1 packets transmitted, 1 received, 0% packet loss, time 0ms", 12.3},
		{"busybox", "64 bytes from 192.0.2.1: seq=0 ttl=64 time=0.123 ms", 0.123},
		{"windows sub-ms", "Reply from 192.0.2.1: bytes=32 time<1ms TTL=64", 1},
		{"localized windows", "来自 192.0.2.1 的回复: 字节=32 时间=18ms TTL=64", 18},
		{"localized summary", "最短 = 1ms，最长 = 3ms，平均 = 2ms", 2},
		{"decimal comma", "64 bytes from 192.0.2.1: time=12,5 ms", 12.5},
		{"zero RTT", "64 bytes from 192.0.2.1: time=0 ms", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if latency, ok := parseProbeNetworkMonitorLatency(tc.output); !ok || latency != tc.want {
				t.Fatalf("latency=%v ok=%v want=%v", latency, ok, tc.want)
			}
		})
	}
}
