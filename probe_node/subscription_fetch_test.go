package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"testing"
)

func TestRunProbeSubscriptionFetchReturnsHashedContent(t *testing.T) {
	oldFetch := probeSubscriptionFetchContent
	content := []byte("proxies:\n  - name: local-node\n")
	gotURL := ""
	gotMaxBytes := int64(0)
	probeSubscriptionFetchContent = func(_ context.Context, rawURL string, maxBytes int64) ([]byte, error) {
		gotURL = rawURL
		gotMaxBytes = maxBytes
		return content, nil
	}
	t.Cleanup(func() { probeSubscriptionFetchContent = oldFetch })
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()
	processProbeControlMessage(
		probeControlMessage{Type: "subscription_fetch", RequestID: "request-1", URL: "https://subscription.example/config?token=do-not-leak", MaxBytes: probeSubscriptionFetchMaxBytes},
		nodeIdentity{NodeID: "7"}, serverConn, json.NewEncoder(serverConn), &sync.Mutex{},
	)
	var result probeSubscriptionFetchResultPayload
	if err := json.NewDecoder(clientConn).Decode(&result); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	if !result.OK || result.NodeID != "7" || result.Size != int64(len(content)) || result.ContentSHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("result=%+v", result)
	}
	if gotURL != "https://subscription.example/config?token=do-not-leak" || gotMaxBytes != probeSubscriptionFetchMaxBytes {
		t.Fatalf("rawURL=%q maxBytes=%d", gotURL, gotMaxBytes)
	}
	decoded, err := base64.StdEncoding.DecodeString(result.ContentBase64)
	if err != nil || string(decoded) != string(content) {
		t.Fatalf("decoded=%q err=%v", decoded, err)
	}
	raw, _ := json.Marshal(result)
	if strings.Contains(string(raw), "do-not-leak") || strings.Contains(string(raw), "subscription.example") {
		t.Fatalf("subscription URL leaked in result: %s", raw)
	}
}

func TestValidateProbeSubscriptionFetchURLSupportsCustomPortAndRejectsPrivate(t *testing.T) {
	oldLookup := probeSubscriptionFetchLookupIP
	probeSubscriptionFetchLookupIP = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("1.1.1.1")}}, nil
	}
	t.Cleanup(func() { probeSubscriptionFetchLookupIP = oldLookup })
	target, _, err := validateProbeSubscriptionFetchURL(context.Background(), "https://subscription.example:8443/config")
	if err != nil || target.Port() != "8443" {
		t.Fatalf("target=%v err=%v", target, err)
	}
	for _, rawURL := range []string{"http://subscription.example/config", "https://subscription.example:0/config", "https://subscription.example:65536/config"} {
		if _, _, err := validateProbeSubscriptionFetchURL(context.Background(), rawURL); err == nil {
			t.Fatalf("invalid URL accepted: %s", rawURL)
		}
	}
	probeSubscriptionFetchLookupIP = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("192.168.1.2")}}, nil
	}
	if _, _, err := validateProbeSubscriptionFetchURL(context.Background(), "https://subscription.example/config"); err == nil {
		t.Fatal("private subscription address accepted")
	}
}

func TestFetchProbeSubscriptionContentUsesConfiguredPortWithoutLeakingURL(t *testing.T) {
	oldLookup := probeSubscriptionFetchLookupIP
	oldDial := probeSubscriptionFetchDialContext
	probeSubscriptionFetchLookupIP = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("1.1.1.1")}}, nil
	}
	dialAddress := ""
	probeSubscriptionFetchDialContext = func(_ context.Context, _, address string) (net.Conn, error) {
		dialAddress = address
		return nil, context.DeadlineExceeded
	}
	t.Cleanup(func() {
		probeSubscriptionFetchLookupIP = oldLookup
		probeSubscriptionFetchDialContext = oldDial
	})
	_, err := fetchProbeSubscriptionContent(context.Background(), "https://subscription.example:8443/config?token=do-not-leak", 1024)
	if err == nil || dialAddress != "1.1.1.1:8443" {
		t.Fatalf("dialAddress=%q err=%v", dialAddress, err)
	}
	if strings.Contains(err.Error(), "do-not-leak") || strings.Contains(err.Error(), "subscription.example") {
		t.Fatalf("subscription URL leaked in error: %v", err)
	}
}

func TestFetchProbeSubscriptionContentFollowsSafeRedirect(t *testing.T) {
	oldLookup := probeSubscriptionFetchLookupIP
	oldDo := probeSubscriptionFetchDo
	probeSubscriptionFetchLookupIP = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("1.1.1.1")}}, nil
	}
	requests := []string{}
	probeSubscriptionFetchDo = func(_ context.Context, target *url.URL, ips []netip.Addr) (*http.Response, error) {
		requests = append(requests, target.String())
		if len(ips) != 1 || ips[0].String() != "1.1.1.1" {
			t.Fatalf("resolved addresses=%v", ips)
		}
		if len(requests) == 1 {
			return &http.Response{StatusCode: http.StatusTemporaryRedirect, Header: http.Header{"Location": []string{"https://cdn.example/config.yaml"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("proxies: []\n"))}, nil
	}
	t.Cleanup(func() {
		probeSubscriptionFetchLookupIP = oldLookup
		probeSubscriptionFetchDo = oldDo
	})

	content, err := fetchProbeSubscriptionContent(context.Background(), "https://subscription.example/config", 1024)
	if err != nil || string(content) != "proxies: []\n" {
		t.Fatalf("content=%q err=%v", content, err)
	}
	if len(requests) != 2 || requests[1] != "https://cdn.example/config.yaml" {
		t.Fatalf("requests=%v", requests)
	}
}

func TestProbeSubscriptionFetchPublicAddrRejectsReservedTranslationRanges(t *testing.T) {
	for raw, want := range map[string]bool{
		"1.1.1.1": true, "2606:4700:4700::1111": true,
		"240.0.0.1": false, "64:ff9b::c0a8:102": false, "2002:c0a8:102::1": false,
	} {
		if got := probeSubscriptionFetchPublicAddr(netip.MustParseAddr(raw)); got != want {
			t.Fatalf("address=%s public=%v want=%v", raw, got, want)
		}
	}
}

func TestSanitizeProbeSubscriptionFetchErrorRemovesURLDetails(t *testing.T) {
	rawURL := "https://subscription.example:8443/config?token=do-not-leak"
	got := sanitizeProbeSubscriptionFetchError(errors.New("fetch "+rawURL+" at subscription.example failed: do-not-leak\nretry"), rawURL)
	for _, secret := range []string{rawURL, "subscription.example", "do-not-leak", "\n"} {
		if strings.Contains(got, secret) {
			t.Fatalf("sanitized error %q contains %q", got, secret)
		}
	}
}
