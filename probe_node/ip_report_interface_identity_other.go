//go:build !windows

package main

import "net"

func probeIPReportPlatformStableInterfaceID(net.Interface) string {
	return ""
}
