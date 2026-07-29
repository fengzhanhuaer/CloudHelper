package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type probeDDNSRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn probeDDNSRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func probeDDNSTestHTTPResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestNormalizeProbeDDNSConfig(t *testing.T) {
	config, err := normalizeProbeDDNSConfig(probeDDNSConfig{
		Enabled:             true,
		SelectedInterfaceID: " NAME:Ethernet ",
		InterfaceDomains:    []string{"Node.Example.COM.", "node.example.com"},
		PublicDomains:       []string{"exit.example.net"},
		APIToken:            " token ",
	})
	if err != nil {
		t.Fatalf("normalize config: %v", err)
	}
	if config.SelectedInterfaceID != "name:ethernet" {
		t.Fatalf("selected interface=%q", config.SelectedInterfaceID)
	}
	if !reflect.DeepEqual(config.InterfaceDomains, []string{"node.example.com"}) {
		t.Fatalf("interface domains=%v", config.InterfaceDomains)
	}
	if config.APIToken != "token" {
		t.Fatalf("api token was not trimmed")
	}

	_, err = normalizeProbeDDNSConfig(probeDDNSConfig{
		Enabled: true, SelectedInterfaceID: "name:ethernet", APIToken: "token",
		InterfaceDomains: []string{"same.example.com"}, PublicDomains: []string{"SAME.example.com"},
	})
	if err == nil || !strings.Contains(err.Error(), "both interface and public") {
		t.Fatalf("expected duplicate source validation, got %v", err)
	}

	if _, err := normalizeProbeDDNSDomains([]string{"https://bad.example.com"}); err == nil {
		t.Fatal("expected URL-shaped domain to be rejected")
	}
}

func TestProbeLocalSystemDDNSAPITokenIsNotReturned(t *testing.T) {
	mux := setupProbeLocalConsoleTest(t)
	sessionCookie := registerAndLoginProbeLocal(t, mux, "admin", "secret1234")

	response := doProbeLocalRequest(t, mux, http.MethodPost, "/local/api/system/ddns", map[string]any{
		"enabled":               false,
		"selected_interface_id": "name:ethernet",
		"interface_domains":     []string{"node.example.com"},
		"public_domains":        []string{"exit.example.com"},
		"api_token":             "top-secret-token",
	}, sessionCookie)
	if response.Code != http.StatusOK {
		t.Fatalf("save status=%d body=%s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "top-secret-token") || strings.Contains(response.Body.String(), "api_token\"") {
		t.Fatalf("response leaked api token: %s", response.Body.String())
	}
	payload := decodeProbeLocalJSON(t, response)
	view, ok := payload["config"].(map[string]any)
	if !ok || view["api_token_configured"] != true {
		t.Fatalf("unexpected config view: %+v", payload["config"])
	}

	config, err := loadProbeDDNSConfig()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if config.APIToken != "top-secret-token" {
		t.Fatalf("stored token=%q", config.APIToken)
	}
	path, err := resolveProbeDDNSPath(probeDDNSConfigFileName)
	if err != nil {
		t.Fatalf("resolve config path: %v", err)
	}
	if filepath.Base(filepath.Dir(path)) != probeDDNSDirName {
		t.Fatalf("config path=%s, want data/ddns", path)
	}
}

func TestMatchProbeDDNSCloudflareZoneUsesLongestSuffix(t *testing.T) {
	zones := []probeDDNSCloudflareZone{{ID: "root", Name: "example.com"}, {ID: "sub", Name: "internal.example.com"}}
	zone, err := matchProbeDDNSCloudflareZone("node.internal.example.com", zones)
	if err != nil {
		t.Fatalf("match zone: %v", err)
	}
	if zone.ID != "sub" {
		t.Fatalf("zone=%+v, want sub", zone)
	}
	if _, err := matchProbeDDNSCloudflareZone("notexample.com", zones); err == nil {
		t.Fatal("expected label-boundary mismatch")
	}
}

func TestReconcileProbeDDNSSourceCreatesAllIPsAndDeletesStaleAddress(t *testing.T) {
	oldClient := probeDDNSCloudflareHTTPClient
	t.Cleanup(func() { probeDDNSCloudflareHTTPClient = oldClient })
	requests := []string{}
	probeDDNSCloudflareHTTPClient = func() *http.Client {
		return &http.Client{Transport: probeDDNSRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.Header.Get("Authorization") != "Bearer token" {
				t.Fatalf("authorization=%q", req.Header.Get("Authorization"))
			}
			requests = append(requests, req.Method+" "+req.URL.Path+"?"+req.URL.RawQuery)
			switch {
			case req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/dns_records"):
				return probeDDNSTestHTTPResponse(http.StatusOK, `{"success":true,"result":[]}`), nil
			case req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/dns_records"):
				id := "new-a"
				if strings.Contains(strings.ToUpper(readProbeDDNSTestRequestBody(t, req)), `"AAAA"`) {
					id = "new-aaaa"
				}
				return probeDDNSTestHTTPResponse(http.StatusOK, `{"success":true,"result":{"id":"`+id+`"}}`), nil
			case req.Method == http.MethodDelete && strings.HasSuffix(req.URL.Path, "/old-record"):
				return probeDDNSTestHTTPResponse(http.StatusOK, `{"success":true}`), nil
			default:
				return probeDDNSTestHTTPResponse(http.StatusNotFound, `{}`), nil
			}
		})}
	}

	existing := []probeDDNSManagedRecord{{Source: "interface", Domain: "node.example.com", RecordType: "A", Content: "10.0.0.1", ZoneID: "zone", RecordID: "old-record"}}
	next, err := reconcileProbeDDNSSource(context.Background(), "token", []probeDDNSCloudflareZone{{ID: "zone", Name: "example.com"}}, "interface", []string{"node.example.com"}, probeDDNSAddressSet{IPv4: []string{"10.0.0.2"}, IPv6: []string{"2001:db8::2"}}, existing)
	if err != nil {
		t.Fatalf("reconcile source: %v", err)
	}
	if len(next) != 2 || next[0].Content == "10.0.0.1" || next[1].Content == "10.0.0.1" {
		t.Fatalf("next records=%+v", next)
	}
	joined := strings.Join(requests, "\n")
	if !strings.Contains(joined, "DELETE /client/v4/zones/zone/dns_records/old-record") {
		t.Fatalf("stale record was not deleted; requests:\n%s", joined)
	}
}

func TestEnsureProbeDDNSCloudflareRecordDoesNotAdoptManualRecord(t *testing.T) {
	oldClient := probeDDNSCloudflareHTTPClient
	t.Cleanup(func() { probeDDNSCloudflareHTTPClient = oldClient })
	created := false
	probeDDNSCloudflareHTTPClient = func() *http.Client {
		return &http.Client{Transport: probeDDNSRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			switch req.Method {
			case http.MethodGet:
				return probeDDNSTestHTTPResponse(http.StatusOK, `{"success":true,"result":[{"id":"manual","type":"A","name":"node.example.com","content":"1.1.1.1","comment":"created by user"}]}`), nil
			case http.MethodPost:
				created = true
				body := readProbeDDNSTestRequestBody(t, req)
				if !strings.Contains(body, probeDDNSCloudflareRecordComment) {
					t.Fatalf("create payload missing ownership comment: %s", body)
				}
				return probeDDNSTestHTTPResponse(http.StatusOK, `{"success":true,"result":{"id":"managed"}}`), nil
			default:
				return probeDDNSTestHTTPResponse(http.StatusMethodNotAllowed, `{}`), nil
			}
		})}
	}
	recordID, err := ensureProbeDDNSCloudflareRecord(context.Background(), "token", "zone", "node.example.com", "A", "1.1.1.1", "")
	if err != nil {
		t.Fatalf("ensure record: %v", err)
	}
	if !created || recordID != "managed" {
		t.Fatalf("created=%v recordID=%q", created, recordID)
	}
}

func readProbeDDNSTestRequestBody(t *testing.T, req *http.Request) string {
	t.Helper()
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read request body: %v", err)
	}
	return string(raw)
}

func TestDropProbeDDNSUnconfiguredSourceRecordsDoesNotCallRemote(t *testing.T) {
	records := []probeDDNSManagedRecord{
		{Source: "interface", Domain: "removed.example.com", RecordType: "A", Content: "10.0.0.1", ZoneID: "zone", RecordID: "one"},
		{Source: "public", Domain: "exit.example.com", RecordType: "A", Content: "1.1.1.1", ZoneID: "zone", RecordID: "two"},
	}
	next := dropProbeDDNSUnconfiguredSourceRecords(records, "interface", nil)
	if len(next) != 1 || next[0].Source != "public" {
		t.Fatalf("next=%+v", next)
	}
}

func TestCollectProbeDDNSPublicAddressesUsesCurrentSniff(t *testing.T) {
	oldSniffer := probeDDNSPublicIPSniffer
	t.Cleanup(func() { probeDDNSPublicIPSniffer = oldSniffer })
	probeDDNSPublicIPSniffer = func() ([]string, []string, bool) {
		return []string{"1.1.1.1", "1.1.1.1", "bad"}, []string{"2001:db8::1"}, true
	}
	addresses, err := collectProbeDDNSPublicAddresses()
	if err != nil {
		t.Fatalf("collect public addresses: %v", err)
	}
	if !reflect.DeepEqual(addresses.IPv4, []string{"1.1.1.1"}) || !reflect.DeepEqual(addresses.IPv6, []string{"2001:db8::1"}) {
		t.Fatalf("addresses=%+v", addresses)
	}
}

func TestTriggerProbeDDNSSyncCoalescesPendingEvents(t *testing.T) {
	oldReconcile := probeDDNSReconcileFn
	t.Cleanup(func() {
		probeDDNSReconcileFn = oldReconcile
		probeDDNSRuntime.mu.Lock()
		probeDDNSRuntime.syncPending = false
		probeDDNSRuntime.syncRunning = false
		probeDDNSRuntime.mu.Unlock()
	})
	probeDDNSRuntime.mu.Lock()
	probeDDNSRuntime.syncPending = false
	probeDDNSRuntime.syncRunning = false
	probeDDNSRuntime.mu.Unlock()

	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	done := make(chan struct{}, 2)
	var calls atomic.Int32
	probeDDNSReconcileFn = func(context.Context) error {
		call := calls.Add(1)
		if call == 1 {
			close(firstStarted)
			<-releaseFirst
		}
		done <- struct{}{}
		return nil
	}
	triggerProbeDDNSSync("first")
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first sync did not start")
	}
	triggerProbeDDNSSync("second")
	triggerProbeDDNSSync("third")
	close(releaseFirst)
	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("coalesced sync did not finish")
		}
	}
	time.Sleep(20 * time.Millisecond)
	if calls.Load() != 2 {
		t.Fatalf("sync calls=%d, want 2", calls.Load())
	}
}

func TestEnsureProbeDDNSCertificatePersistsSANAndSkipsEarlyRenewal(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	config, err := normalizeProbeDDNSConfig(probeDDNSConfig{
		Enabled: true, SelectedInterfaceID: "name:ethernet", APIToken: "token",
		InterfaceDomains: []string{"node.example.com"}, PublicDomains: []string{"exit.example.com"},
	})
	if err != nil {
		t.Fatalf("normalize config: %v", err)
	}
	if err := persistProbeDDNSConfig(config); err != nil {
		t.Fatalf("persist config: %v", err)
	}

	oldIssuer := probeDDNSCertificateIssuer
	t.Cleanup(func() { probeDDNSCertificateIssuer = oldIssuer })
	issuerCalls := 0
	probeDDNSCertificateIssuer = func(_ context.Context, token string, domains []string) (probeDDNSIssuedCertificate, error) {
		issuerCalls++
		if token != "token" {
			t.Fatalf("token=%q", token)
		}
		return makeProbeDDNSTestCertificate(t, domains, 60*24*time.Hour), nil
	}
	if err := ensureProbeDDNSCertificate(context.Background()); err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	if err := ensureProbeDDNSCertificate(context.Background()); err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	if issuerCalls != 1 {
		t.Fatalf("issuer calls=%d, want 1", issuerCalls)
	}
	stored, err := readProbeDDNSCertificate()
	if err != nil {
		t.Fatalf("read certificate: %v", err)
	}
	wantDomains := []string{"exit.example.com", "node.example.com"}
	if !reflect.DeepEqual(stored.Domains, wantDomains) {
		t.Fatalf("certificate domains=%v want=%v", stored.Domains, wantDomains)
	}
	for _, name := range []string{probeDDNSCertificateFileName, probeDDNSPrivateKeyFileName, probeDDNSCertificateMetaName} {
		path, _ := resolveProbeDDNSPath(name)
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("missing %s: %v", path, err)
		}
	}
}

func TestEnsureProbeDDNSCertificateRenewsInsideWindowAndPreservesOnFailure(t *testing.T) {
	t.Setenv("PROBE_NODE_DATA_DIR", t.TempDir())
	config, err := normalizeProbeDDNSConfig(probeDDNSConfig{Enabled: true, APIToken: "token", PublicDomains: []string{"exit.example.com"}})
	if err != nil {
		t.Fatalf("normalize config: %v", err)
	}
	if err := persistProbeDDNSConfig(config); err != nil {
		t.Fatalf("persist config: %v", err)
	}
	oldCert := makeProbeDDNSTestCertificate(t, config.PublicDomains, 20*24*time.Hour)
	if err := writeProbeDDNSCertificate(oldCert); err != nil {
		t.Fatalf("write old certificate: %v", err)
	}

	oldIssuer := probeDDNSCertificateIssuer
	t.Cleanup(func() { probeDDNSCertificateIssuer = oldIssuer })
	probeDDNSCertificateIssuer = func(context.Context, string, []string) (probeDDNSIssuedCertificate, error) {
		return probeDDNSIssuedCertificate{}, io.ErrUnexpectedEOF
	}
	if err := ensureProbeDDNSCertificate(context.Background()); err == nil {
		t.Fatal("expected renewal failure")
	}
	preserved, err := readProbeDDNSCertificate()
	if err != nil {
		t.Fatalf("read preserved certificate: %v", err)
	}
	if preserved.NotAfter.Unix() != oldCert.NotAfter.Unix() {
		t.Fatalf("old certificate was replaced: got=%s want=%s", preserved.NotAfter, oldCert.NotAfter)
	}

	issuerCalls := 0
	probeDDNSCertificateIssuer = func(_ context.Context, _ string, domains []string) (probeDDNSIssuedCertificate, error) {
		issuerCalls++
		return makeProbeDDNSTestCertificate(t, domains, 60*24*time.Hour), nil
	}
	if err := ensureProbeDDNSCertificate(context.Background()); err != nil {
		t.Fatalf("renew certificate: %v", err)
	}
	if issuerCalls != 1 {
		t.Fatalf("issuer calls=%d, want 1", issuerCalls)
	}
	renewed, err := readProbeDDNSCertificate()
	if err != nil {
		t.Fatalf("read renewed certificate: %v", err)
	}
	if !renewed.NotAfter.After(oldCert.NotAfter) {
		t.Fatalf("certificate was not renewed: old=%s new=%s", oldCert.NotAfter, renewed.NotAfter)
	}
}

func makeProbeDDNSTestCertificate(t *testing.T, domains []string, validity time.Duration) probeDDNSIssuedCertificate {
	t.Helper()
	domains, err := normalizeProbeDDNSDomains(domains)
	if err != nil {
		t.Fatalf("normalize domains: %v", err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: domains[0]}, DNSNames: domains,
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(validity), KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	keyPEM, err := encodeProbeDDNSPrivateKey(key)
	if err != nil {
		t.Fatalf("encode key: %v", err)
	}
	return probeDDNSIssuedCertificate{
		CertPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), KeyPEM: keyPEM,
		Domains: domains, NotBefore: template.NotBefore, NotAfter: template.NotAfter, RenewedAt: now,
	}
}
