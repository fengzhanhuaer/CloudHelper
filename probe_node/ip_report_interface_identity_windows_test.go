//go:build windows

package main

import (
	"net"
	"testing"
)

func TestResolveProbeIPReportInterfaceIdentityUsesWindowsAdapterGUID(t *testing.T) {
	oldFind := probeIPReportWindowsFindAdapterByIfIndex
	probeIPReportWindowsFindAdapterByIfIndex = func(interfaceIndex int) (windowsAdapterInfo, error) {
		if interfaceIndex != 15 {
			t.Fatalf("interface index=%d want 15", interfaceIndex)
		}
		return windowsAdapterInfo{
			InterfaceIndex: 15,
			AdapterGUID:    "{90078D57-7BEB-4176-8BFB-82A52FE5D5B1}",
		}, nil
	}
	t.Cleanup(func() { probeIPReportWindowsFindAdapterByIfIndex = oldFind })

	hardwareAddr, err := net.ParseMAC("00:11:22:33:44:55")
	if err != nil {
		t.Fatalf("parse mac: %v", err)
	}
	identity := resolveProbeIPReportInterfaceIdentity(net.Interface{
		Index:        15,
		Name:         "Ethernet",
		HardwareAddr: hardwareAddr,
	})
	if identity.ID != "guid:90078d57-7beb-4176-8bfb-82a52fe5d5b1" {
		t.Fatalf("stable id=%q", identity.ID)
	}
	if len(identity.Aliases) == 0 || identity.Aliases[0] != "mac:00:11:22:33:44:55" {
		t.Fatalf("legacy aliases=%v", identity.Aliases)
	}
}
