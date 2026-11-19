package dnsfilter

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLocalFilterPersistsAndBlocksCachedFakeIP(t *testing.T) {
	dir := t.TempDir()
	var s Store
	c := Config{BlockDoH: true, BlockDoT: true, Blacklist: []string{"DNS.Google.COM.", "1.1.1.1", "dns.google.com"}}
	if err := s.Save(dir, c); err != nil {
		t.Fatal(err)
	}
	got, err := s.Current(dir)
	if err != nil || len(got.Blacklist) != 2 || !s.BlockDomain("sub.dns.google.com") || s.BlockDomain("notdns.google.com") {
		t.Fatalf("config=%+v err=%v", got, err)
	}
	calls := 0
	lookup := func(ip string) (string, bool) { calls++; return "dns.google.com", true }
	for i := 0; i < 64; i++ {
		if !s.BlockTarget("198.18.4.9", 443, lookup) || !s.BlockTarget("198.18.4.9", 80, lookup) {
			t.Fatal("cached fake IP escaped blacklist")
		}
	}
	if calls != 1 {
		t.Fatalf("lookups=%d want one local lookup", calls)
	}
	if !s.BlockTarget("1.1.1.1", 443, nil) || s.BlockTarget("1.1.1.1", 80, nil) || !s.BlockTarget("9.9.9.9", 853, nil) {
		t.Fatal("real IP / port policy")
	}
	var restarted Store
	if _, err := restarted.Current(dir); err != nil || !restarted.BlockDomain("dns.google.com") {
		t.Fatalf("restart lost settings: %v", err)
	}
	before, _ := os.Stat(filepath.Join(dir, FileName))
	if err := s.Save(dir, got); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(filepath.Join(dir, FileName))
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("unchanged config rewritten")
	}
	if err := s.Save(dir, Config{}); err != nil {
		t.Fatal(err)
	}
	if s.BlockTarget("198.18.4.9", 443, lookup) || s.BlockDomain("dns.google.com") {
		t.Fatal("disable retained old denial")
	}
}

func TestBackgroundResolutionCannotRestoreDisabledPolicy(t *testing.T) {
	var s Store
	dir := t.TempDir()
	if err := s.Save(dir, Config{BlockDoH: true, Blacklist: []string{"dns.example"}}); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	s.Refresh(func(string) ([]string, error) { close(started); <-release; return []string{"203.0.113.53"}, nil })
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("resolver did not start")
	}
	if err := s.Save(dir, Config{}); err != nil {
		t.Fatal(err)
	}
	close(release)
	deadline := time.Now().Add(time.Second)
	for {
		s.mu.Lock()
		busy := s.refreshing
		s.mu.Unlock()
		if !busy {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("resolver did not finish")
		}
		time.Sleep(time.Millisecond)
	}
	if s.BlockTarget("203.0.113.53", 443, nil) {
		t.Fatal("stale resolver restored a disabled policy")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.resolved) != 0 {
		t.Fatal("stale results retained after policy change")
	}
}

func TestFilterValidationAndMappingReplacement(t *testing.T) {
	for _, entry := range []string{"https://dns.google/dns-query", "1.1.1.0/24", "dns.google;id", "*.google", "-bad.example", "a..b"} {
		if _, err := Normalize(Config{Blacklist: []string{entry}}); err == nil {
			t.Fatalf("accepted %q", entry)
		}
	}
	var s Store
	dir := t.TempDir()
	if err := s.Save(dir, Config{BlockDoH: true, Blacklist: []string{"dns.google", "2001:4860:4860::8888"}}); err != nil {
		t.Fatal(err)
	}
	if !s.BlockTarget("2001:4860:4860::8888", 443, nil) {
		t.Fatal("IPv6 literal escaped")
	}
	if !s.BlockTarget("198.18.4.9", 443, func(string) (string, bool) { return "dns.google", true }) {
		t.Fatal("fake IP escaped")
	}
	s.Invalidate("198.18.4.9")
	if s.BlockTarget("198.18.4.9", 443, func(string) (string, bool) { return "safe.example", true }) {
		t.Fatal("reused fake IP retained denial")
	}
	s.RememberResolved("dns.google", []string{"8.8.8.8"})
	if !s.BlockTarget("8.8.8.8", 443, nil) || s.BlockTarget("8.8.8.8", 80, nil) {
		t.Fatal("resolved real IP policy")
	}
	packet := make([]byte, 40)
	packet[0] = 0x45
	packet[9] = 6
	copy(packet[16:20], net.ParseIP("8.8.8.8").To4())
	packet[22] = 1
	packet[23] = 187
	if !s.BlockPacket(packet, nil) {
		t.Fatal("TCP packet escaped")
	}
	packet[9] = 17
	if !s.BlockPacket(packet, nil) {
		t.Fatal("UDP packet escaped")
	}
}
