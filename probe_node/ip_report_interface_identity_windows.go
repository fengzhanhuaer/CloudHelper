//go:build windows

package main

import (
	"net"
	"strings"
)

var probeIPReportWindowsFindAdapterByIfIndex = windowsFindAdapterByIfIndex

func probeIPReportPlatformStableInterfaceID(iface net.Interface) string {
	if iface.Index <= 0 {
		return ""
	}
	adapter, err := probeIPReportWindowsFindAdapterByIfIndex(iface.Index)
	if err != nil {
		return ""
	}
	guid := strings.ToLower(strings.Trim(strings.TrimSpace(adapter.AdapterGUID), "{}"))
	if guid == "" {
		return ""
	}
	return "guid:" + guid
}
