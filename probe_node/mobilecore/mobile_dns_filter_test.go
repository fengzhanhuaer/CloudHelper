package mobilecore

import (
	"encoding/binary"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/cloudhelper/probe_node/internal/dnsfilter"
	"golang.org/x/net/dns/dnsmessage"
)

func TestMobileDNSFilterSettingsAndCachedFakeIP(t *testing.T) {
	dir := t.TempDir()
	resetMobileVRouteVPNStateForTest(t, dir)
	old := mobileDNSFilter
	mobileDNSFilter = &dnsfilter.Store{}
	t.Cleanup(func() { mobileDNSFilter = old })
	var result struct {
		OK     bool             `json:"ok"`
		Config dnsfilter.Config `json:"config"`
	}
	if err := json.Unmarshal([]byte(SaveDNSFilterSettings(dir, `{"block_doh":true,"block_dot":true,"blacklist":["1.1.1.1"]}`)), &result); err != nil || !result.OK {
		t.Fatalf("save %+v err=%v", result, err)
	}
	q, err := buildAndroidVPNDNSQuery("1.1.1.1", dnsmessage.TypeA)
	if err != nil {
		t.Fatal(err)
	}
	reply, err := resolveAndroidVPNDNSPacket(q)
	if err != nil || len(reply) < 4 || reply[3]&15 != 5 {
		t.Fatalf("DNS response=%x err=%v", reply, err)
	}
	if _, ok := storeAndroidVPNControllerFakeIP(dir, "1.1.1.1", "198.18.4.9", androidRouteDecision{}, time.Now()); !ok {
		t.Fatal("seed mapping")
	}
	p := make([]byte, 40)
	p[0] = 0x45
	p[9] = 17
	copy(p[16:20], net.ParseIP("198.18.4.9").To4())
	binary.BigEndian.PutUint16(p[22:24], 80)
	handled, err := mobileVRouteHandleVPNPacket(dir, p, nil)
	if err != nil || !handled {
		t.Fatalf("cached fake IP escaped forwarding: handled=%v err=%v", handled, err)
	}
	p[9] = 6 // An already established TCP flow must also pass through the filter.
	for i := 0; i < 64; i++ {
		handled, err = mobileVRouteHandleVPNPacket(dir, p, nil)
		if err != nil || !handled {
			t.Fatalf("TCP escaped: %v", err)
		}
	}
	if SaveDNSFilterSettings(dir, `{"block_doh":true,"blacklist":["https://bad"]}`) == "" {
		t.Fatal("missing validation result")
	}
	if !mobileDNSFilter.BlockDomain("1.1.1.1") {
		t.Fatal("invalid save changed active policy")
	}
	if err := json.Unmarshal([]byte(SaveDNSFilterSettings(dir, `{"block_doh":false,"block_dot":false,"blacklist":[]}`)), &result); err != nil || !result.OK {
		t.Fatal("disable failed")
	}
	if mobileDNSFilterBlocksPacket(dir, p) {
		t.Fatal("disable retained cache denial")
	}
}
