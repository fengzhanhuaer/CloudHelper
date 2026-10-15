package dnsupstream

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStoreDefaultsOverrideRestartAndNoRewrite(t *testing.T) {
	dir := t.TempDir()
	var s Store
	c, err := s.Current(dir)
	if err != nil || c != Default() {
		t.Fatalf("defaults: %+v %v", c, err)
	}
	c.DNS = "192.0.2.53:5353"
	if err = s.Apply(dir, &c); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "dns_upstreams.json")
	before, _ := os.Stat(path)
	if err = s.Apply(dir, &c); err != nil {
		t.Fatal(err)
	}
	if err = s.Apply(dir, nil); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(path)
	if before.ModTime() != after.ModTime() {
		t.Fatal("unchanged values rewrote config")
	}
	var restarted Store
	actual, err := restarted.Current(dir)
	if err != nil || actual != c {
		t.Fatalf("restart %+v %v", actual, err)
	}
	bad := c
	bad.DoH = "http://example.com/dns-query"
	if s.Apply(dir, &bad) == nil {
		t.Fatal("insecure config accepted")
	}
	actual, _ = s.Current(dir)
	if actual != c {
		t.Fatal("failed update replaced valid config")
	}
}

func TestValidationAndFourStages(t *testing.T) {
	for _, bad := range []Config{{"http://dns.example/query", "dns.example:853", "223.5.5.5:53"}, {"https://user:pass@dns.example/query", "dns.example:853", "223.5.5.5:53"}, {Default().DoH, "dns.example:0", Default().DNS}, {Default().DoH, Default().DoT, "127.0.0.1:53"}, {Default().DoH, Default().DoT, "198.18.0.2:53"}, {Default().DoH, Default().DoT, "dns.example:53"}} {
		if bad.Validate() == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
	got := Candidates(Default(), []string{"127.0.0.1", "198.18.0.2", "223.5.5.5", "192.168.1.1", "192.168.1.1"})
	if len(got) != 4 || got[0].Kind != "doh" || got[1].Kind != "dot" || got[2].Address != "223.5.5.5:53" || got[3].Address != "192.168.1.1:53" {
		t.Fatalf("chain %+v", got)
	}
}

func TestControllerRepairsCorruptLocalJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dns_upstreams.json")
	if err := os.WriteFile(path, []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	var s Store
	c := Default()
	if err := s.Apply(dir, &c); err != nil {
		t.Fatal(err)
	}
	var restarted Store
	got, err := restarted.Current(dir)
	if err != nil || got != c {
		t.Fatalf("repair %+v %v", got, err)
	}
}
