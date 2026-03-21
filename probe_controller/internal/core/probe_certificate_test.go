package core

import (
	"testing"
	"time"

	"golang.org/x/crypto/acme"
)

func TestNormalizeProbeCertificateHost(t *testing.T) {
	got := normalizeProbeCertificateHost("https://API.Example.com:8443/path")
	if got != "api.example.com" {
		t.Fatalf("unexpected normalized host: %q", got)
	}

	got = normalizeProbeCertificateHost("api.example.com.")
	if got != "api.example.com" {
		t.Fatalf("unexpected trailing dot normalization: %q", got)
	}
}

func TestSelectDNS01Challenge(t *testing.T) {
	authz := &acme.Authorization{
		URI: "test-authz",
		Challenges: []*acme.Challenge{
			{Type: "http-01"},
			{Type: "dns-01"},
		},
	}
	chal, err := selectDNS01Challenge(authz)
	if err != nil {
		t.Fatalf("selectDNS01Challenge returned error: %v", err)
	}
	if chal == nil || chal.Type != "dns-01" {
		t.Fatalf("expected dns-01 challenge, got %+v", chal)
	}
}

func TestIsProbeCertificateUsable(t *testing.T) {
	if !isProbeCertificateUsable(time.Now().Add(30*time.Minute), 5*time.Minute) {
		t.Fatalf("expected certificate to be usable")
	}
	if isProbeCertificateUsable(time.Now().Add(2*time.Minute), 5*time.Minute) {
		t.Fatalf("expected near-expired certificate to be unusable")
	}
}
