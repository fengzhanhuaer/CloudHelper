//go:build !windows

package main

const (
	probeLocalTUNRouteGatewayIPv4   = "198.18.0.1"
	probeLocalTUNInterfaceIPv4      = "198.18.0.2"
	probeLocalTUNRouteIPv4PrefixLen = 15
)
