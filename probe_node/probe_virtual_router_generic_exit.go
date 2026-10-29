package main

import (
	"net"
	"strings"
)

// Additional exit choices are valid only at the end of an authenticated,
// tenant-authorized carrier path. This does not create a public proxy listener.
func probeVirtualRouterGenericExitPath(runtime *probeVirtualRouterRuntime, path []string) bool {
	clean := cleanProbeVirtualRouterPath(path)
	local := currentProbeVirtualRouterLocalNodeIDForRuntime(runtime)
	return runtime != nil && len(clean) >= 2 && clean[len(clean)-1] == local && validateProbeVirtualRouterIngressPath(runtime, clean) == nil
}

func probeVirtualRouterGenericExitIP(host string) bool {
	ip := net.ParseIP(strings.TrimSpace(host)).To4()
	if ip == nil || !ip.IsGlobalUnicast() || probeVRouteProxyExitIPDenied(ip) || probeVirtualRouterIPInCurrentFakeCIDR(host) {
		return false
	}
	return true
}
