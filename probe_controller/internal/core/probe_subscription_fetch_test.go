package core

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestFetchProbeSpecialExitSubscriptionFromNodeUsesAuthenticatedResult(t *testing.T) {
	oldStore := ProbeStore
	ProbeStore = &probeConfigStore{data: probeConfigData{ProbeNodes: []probeNodeRecord{{NodeNo: 7, NodeName: "fetch", TargetSystem: "windows"}}}}
	serverConn, clientConn := net.Pipe()
	session := &probeSession{nodeID: "7", stream: serverConn, enc: json.NewEncoder(serverConn)}
	probeSessions.mu.Lock()
	oldSession := probeSessions.data["7"]
	probeSessions.data["7"] = session
	probeSessions.mu.Unlock()
	t.Cleanup(func() {
		ProbeStore = oldStore
		probeSessions.mu.Lock()
		if oldSession != nil {
			probeSessions.data["7"] = oldSession
		} else {
			delete(probeSessions.data, "7")
		}
		probeSessions.mu.Unlock()
		_ = clientConn.Close()
		_ = serverConn.Close()
	})

	content := []byte("proxies:\n  - name: fetched\n    type: socks5\n")
	go func() {
		var command probeSubscriptionFetchCommand
		if err := json.NewDecoder(clientConn).Decode(&command); err != nil {
			return
		}
		if command.Type != "subscription_fetch" || command.URL != "https://subscription.example/config?token=secret" || command.MaxBytes != probeSpecialExitSubscriptionMaxBytes {
			return
		}
		sum := sha256.Sum256(content)
		consumeProbeSubscriptionFetchResult(probeSubscriptionFetchResultMessage{
			Type: "subscription_fetch_result", RequestID: command.RequestID, NodeID: "7", OK: true,
			ContentBase64: base64.StdEncoding.EncodeToString(content), ContentSHA256: hex.EncodeToString(sum[:]), Size: int64(len(content)),
		})
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	got, err := fetchProbeSpecialExitSubscriptionFromNode(ctx, "7", "https://subscription.example/config?token=secret")
	if err != nil || string(got) != string(content) {
		t.Fatalf("content=%q err=%v", got, err)
	}
}

func TestDecodeProbeSubscriptionFetchContentRejectsTampering(t *testing.T) {
	content := []byte("payload")
	sum := sha256.Sum256(content)
	valid := probeSubscriptionFetchResultMessage{
		OK: true, ContentBase64: base64.StdEncoding.EncodeToString(content), ContentSHA256: hex.EncodeToString(sum[:]), Size: int64(len(content)),
	}
	if decoded, err := decodeProbeSubscriptionFetchContent(valid); err != nil || string(decoded) != "payload" {
		t.Fatalf("decoded=%q err=%v", decoded, err)
	}
	for name, mutate := range map[string]func(*probeSubscriptionFetchResultMessage){
		"size": func(result *probeSubscriptionFetchResultMessage) { result.Size++ },
		"hash": func(result *probeSubscriptionFetchResultMessage) { result.ContentSHA256 = strings.Repeat("0", 64) },
		"body": func(result *probeSubscriptionFetchResultMessage) { result.ContentBase64 = "not-base64" },
	} {
		t.Run(name, func(t *testing.T) {
			result := valid
			mutate(&result)
			if _, err := decodeProbeSubscriptionFetchContent(result); err == nil {
				t.Fatal("tampered subscription result accepted")
			}
		})
	}
}

func TestFetchProbeSpecialExitSubscriptionFromNodeRejectsAndroidAndOffline(t *testing.T) {
	oldStore := ProbeStore
	ProbeStore = &probeConfigStore{data: probeConfigData{ProbeNodes: []probeNodeRecord{
		{NodeNo: 8, TargetSystem: "android"}, {NodeNo: 9, TargetSystem: "linux"},
	}}}
	t.Cleanup(func() { ProbeStore = oldStore })
	if _, err := fetchProbeSpecialExitSubscriptionFromNode(context.Background(), "8", "https://example.com"); err == nil || !strings.Contains(err.Error(), "Android") {
		t.Fatalf("Android fetch probe error=%v", err)
	}
	if _, err := fetchProbeSpecialExitSubscriptionFromNode(context.Background(), "9", "https://example.com"); err == nil || !strings.Contains(err.Error(), "offline") {
		t.Fatalf("offline fetch probe error=%v", err)
	}
}

func TestFetchProbeSpecialExitSubscriptionFromControllerFollowsSafeRedirect(t *testing.T) {
	oldLookup := probeControllerSubscriptionFetchLookupIP
	oldDo := probeControllerSubscriptionFetchDo
	probeControllerSubscriptionFetchLookupIP = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("1.1.1.1")}}, nil
	}
	requests := []string{}
	probeControllerSubscriptionFetchDo = func(_ context.Context, target *url.URL, ips []netip.Addr) (*http.Response, error) {
		requests = append(requests, target.String())
		if len(ips) != 1 || ips[0].String() != "1.1.1.1" {
			t.Fatalf("resolved addresses=%v", ips)
		}
		if len(requests) == 1 {
			return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": []string{"https://cdn.example/config.yaml"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("proxies: []\n"))}, nil
	}
	t.Cleanup(func() {
		probeControllerSubscriptionFetchLookupIP = oldLookup
		probeControllerSubscriptionFetchDo = oldDo
	})

	content, err := fetchProbeSpecialExitSubscriptionFromController(context.Background(), "https://subscription.example/config")
	if err != nil || string(content) != "proxies: []\n" {
		t.Fatalf("content=%q err=%v", content, err)
	}
	if len(requests) != 2 || requests[0] != "https://subscription.example/config" || requests[1] != "https://cdn.example/config.yaml" {
		t.Fatalf("requests=%v", requests)
	}
}

func TestFetchProbeSpecialExitSubscriptionFromControllerRejectsPrivateRedirect(t *testing.T) {
	oldLookup := probeControllerSubscriptionFetchLookupIP
	oldDo := probeControllerSubscriptionFetchDo
	probeControllerSubscriptionFetchLookupIP = func(_ context.Context, host string) ([]net.IPAddr, error) {
		if host == "private.example" {
			return []net.IPAddr{{IP: net.ParseIP("192.168.1.2")}}, nil
		}
		return []net.IPAddr{{IP: net.ParseIP("1.1.1.1")}}, nil
	}
	probeControllerSubscriptionFetchDo = func(_ context.Context, _ *url.URL, _ []netip.Addr) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": []string{"https://private.example/config"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
	}
	t.Cleanup(func() {
		probeControllerSubscriptionFetchLookupIP = oldLookup
		probeControllerSubscriptionFetchDo = oldDo
	})

	if _, err := fetchProbeSpecialExitSubscriptionFromController(context.Background(), "https://subscription.example/config"); err == nil || !strings.Contains(err.Error(), "non-public") {
		t.Fatalf("private redirect error=%v", err)
	}
}

func TestFetchProbeSpecialExitSubscriptionFromControllerDoesNotRetryForbidden(t *testing.T) {
	oldLookup := probeControllerSubscriptionFetchLookupIP
	oldHTTPDo := probeControllerSubscriptionFetchHTTPDo
	probeControllerSubscriptionFetchLookupIP = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("1.1.1.1")}}, nil
	}
	requests := 0
	probeControllerSubscriptionFetchHTTPDo = func(_ *http.Client, request *http.Request) (*http.Response, error) {
		requests++
		if request.Header.Get("User-Agent") != probeSubscriptionFetchUserAgent {
			t.Fatalf("User-Agent=%q", request.Header.Get("User-Agent"))
		}
		if request.Header.Get("Accept") != probeSubscriptionFetchAccept {
			t.Fatalf("Accept=%q", request.Header.Get("Accept"))
		}
		return &http.Response{StatusCode: http.StatusForbidden, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("denied"))}, nil
	}
	t.Cleanup(func() {
		probeControllerSubscriptionFetchLookupIP = oldLookup
		probeControllerSubscriptionFetchHTTPDo = oldHTTPDo
	})

	_, err := fetchProbeSpecialExitSubscriptionFromController(context.Background(), "https://subscription.example/config?token=do-not-leak")
	if err == nil || !strings.Contains(err.Error(), "HTTP 403") || !strings.Contains(err.Error(), "temporarily rate-limited") {
		t.Fatalf("forbidden error=%v", err)
	}
	if requests != 1 {
		t.Fatalf("requests=%d want=1", requests)
	}
	if strings.Contains(err.Error(), "do-not-leak") || strings.Contains(err.Error(), "subscription.example") {
		t.Fatalf("subscription URL leaked in forbidden error: %v", err)
	}
}

func TestSanitizeProbeSubscriptionFetchResultErrorRemovesURLDetails(t *testing.T) {
	rawURL := "https://subscription.example:8443/config?token=do-not-leak"
	got := sanitizeProbeSubscriptionFetchResultError("fetch "+rawURL+" at subscription.example failed: do-not-leak\nretry", rawURL)
	for _, secret := range []string{rawURL, "subscription.example", "do-not-leak", "\n"} {
		if strings.Contains(got, secret) {
			t.Fatalf("sanitized error %q contains %q", got, secret)
		}
	}
}
