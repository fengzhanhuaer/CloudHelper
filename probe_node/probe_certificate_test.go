package main

import (
	"os"
	"testing"
	"time"
)

func TestNormalizeControllerBaseURL(t *testing.T) {
	got, ok := normalizeControllerBaseURL("https://controller.example.com:15030/api/probe")
	if !ok {
		t.Fatalf("normalizeControllerBaseURL should accept valid https url")
	}
	if got != "https://controller.example.com:15030" {
		t.Fatalf("unexpected normalized url: %q", got)
	}

	if _, ok := normalizeControllerBaseURL("wss://controller.example.com/api/probe"); ok {
		t.Fatalf("normalizeControllerBaseURL should reject ws/wss scheme")
	}
}

func TestResolveProbeControllerBaseURLFallsBackToWS(t *testing.T) {
	oldWS := os.Getenv("PROBE_CONTROLLER_WS")
	defer os.Setenv("PROBE_CONTROLLER_WS", oldWS)
	oldHTTP := os.Getenv("PROBE_CONTROLLER_URL")
	defer os.Setenv("PROBE_CONTROLLER_URL", oldHTTP)

	_ = os.Setenv("PROBE_CONTROLLER_URL", "")
	_ = os.Setenv("PROBE_CONTROLLER_WS", "wss://ctrl.example.com/api/probe")

	got := resolveProbeControllerBaseURL("", "")
	if got != "https://ctrl.example.com" {
		t.Fatalf("unexpected ws derived controller base url: %q", got)
	}
}

func TestIsProbeCertificateUsable(t *testing.T) {
	if !isProbeCertificateUsable(time.Now().Add(10*time.Minute), 5*time.Minute) {
		t.Fatalf("expected certificate to be usable")
	}
	if isProbeCertificateUsable(time.Now().Add(2*time.Minute), 5*time.Minute) {
		t.Fatalf("expected near-expired certificate to be unusable")
	}
}
