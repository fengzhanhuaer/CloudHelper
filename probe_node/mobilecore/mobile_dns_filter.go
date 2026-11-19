package mobilecore

import (
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/cloudhelper/probe_node/internal/dnsfilter"
)

var mobileDNSFilter = &dnsfilter.Store{}

func ensureMobileDNSFilter(configDir string) {
	_ = mobileDNSFilter.Ensure(configDir)
	mobileDNSFilter.Refresh(func(domain string) ([]string, error) {
		return dnsfilter.ResolveIPv4(domain, queryAndroidVPNDNSUpstream)
	})
}

// GetDNSFilterSettings and SaveDNSFilterSettings are exported to the Android bridge.
// No controller credentials are required for these strictly local settings.
func GetDNSFilterSettings(configDir string) string {
	c, err := mobileDNSFilter.Current(normalizeMobileConfigDir(configDir))
	if err != nil {
		return marshalRouteJSON(map[string]any{"ok": false, "error": err.Error()})
	}
	ensureMobileDNSFilter(configDir)
	return marshalRouteJSON(map[string]any{"ok": true, "config": c})
}

func SaveDNSFilterSettings(configDir string, payload string) string {
	var c dnsfilter.Config
	var err error
	if len(payload) > 16384 {
		err = errors.New("DNS 配置过大")
	} else {
		decoder := json.NewDecoder(strings.NewReader(payload))
		decoder.DisallowUnknownFields()
		err = decoder.Decode(&c)
		if err == nil {
			var tail any
			if decoder.Decode(&tail) != io.EOF {
				err = errors.New("invalid trailing JSON")
			}
		}
	}
	if err == nil {
		err = mobileDNSFilter.Save(normalizeMobileConfigDir(configDir), c)
	}
	if err != nil {
		return marshalRouteJSON(map[string]any{"ok": false, "error": err.Error()})
	}
	return GetDNSFilterSettings(configDir)
}

func mobileDNSFilterBlocksPacket(configDir string, packet []byte) bool {
	ensureMobileDNSFilter(configDir)
	return mobileDNSFilter.BlockPacket(packet, func(ip string) (string, bool) {
		vpnDNSState.mu.Lock()
		defer vpnDNSState.mu.Unlock()
		if entry, ok := vpnDNSState.fakeIPToEntry[ip]; ok {
			return entry.Domain, true
		}
		if entry, ok := vpnDNSState.routeIPHints[ip]; ok {
			return entry.Domain, false
		}
		return "", false
	})
}
