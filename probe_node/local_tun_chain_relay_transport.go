package main

import (
	"net"
	"time"
)

func openProbeLocalTUNChainRelayNetConn(chainID string, secret string, relayHost string, relayPort int, layer string, bridgeRole string) (net.Conn, error) {
	return openProbeChainRelayNetConn(chainID, secret, relayHost, relayPort, layer, bridgeRole)
}

func openProbeLocalTUNChainRelayNetConnWithResolvedHost(chainID string, secret string, relayHost string, relayPort int, layer string, bridgeRole string, relayDialHost string, relayHostHeader string, openTimeout time.Duration, cacheOnSuccess bool) (net.Conn, error) {
	return openProbeChainRelayNetConnWithResolvedHost(chainID, secret, relayHost, relayPort, layer, bridgeRole, relayDialHost, relayHostHeader, openTimeout, cacheOnSuccess)
}

func snapshotProbeLocalTUNChainRelayProtocolState(relayHost string, relayPort int) probeChainRelayProtocolStateSnapshot {
	return snapshotProbeChainProtocolState(relayHost, relayPort)
}

func probeLocalTUNChainRelaySpeedTest(endpoint probeLocalTUNChainEndpoint, protocol string) []probeChainRelaySpeedTestResult {
	return probeChainRelaySpeedTestAuto(endpoint.ChainID, endpoint.ChainSecret, endpoint.EntryHost, endpoint.EntryPort, endpoint.LinkLayer, protocol, probeChainRelaySpeedTestBytes)
}
