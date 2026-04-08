package core

import (
	"testing"
	"time"
)

func TestBuildTunnelControllerExceptionRateLimitKey(t *testing.T) {
	key := buildTunnelControllerExceptionRateLimitKey(" Open ", "  Mixed CASE Message  ")
	if key != "open|mixed case message" {
		t.Fatalf("key=%q, want open|mixed case message", key)
	}

	longMsg := ""
	for i := 0; i < tunnelControllerExceptionRateLimitKeyMaxLen+16; i++ {
		longMsg += "A"
	}
	key = buildTunnelControllerExceptionRateLimitKey("tcp", longMsg)
	if len(key) != len("tcp|")+tunnelControllerExceptionRateLimitKeyMaxLen {
		t.Fatalf("key length=%d, want %d", len(key), len("tcp|")+tunnelControllerExceptionRateLimitKeyMaxLen)
	}
}

func TestTunnelControllerExceptionRateLimiterAllow(t *testing.T) {
	limiter := &tunnelControllerExceptionRateLimiter{}
	now := time.Now()

	for i := 0; i < tunnelControllerExceptionRateLimitBurst; i++ {
		if !limiter.Allow("open", "tcp failed", now) {
			t.Fatalf("allow should be true at attempt=%d", i+1)
		}
	}
	if limiter.Allow("open", "tcp failed", now) {
		t.Fatal("allow should be false when burst exceeded within same window")
	}

	if !limiter.Allow("open", "tcp failed", now.Add(tunnelControllerExceptionRateLimitWindow+time.Millisecond)) {
		t.Fatal("allow should reset to true after next time window")
	}

	if !limiter.Allow("read", "tcp failed", now) {
		t.Fatal("different category should use different bucket")
	}
}
