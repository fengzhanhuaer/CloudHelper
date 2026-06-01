package main

import (
	"errors"
	"net"
	"strings"
	"time"
)

func openProbeLocalTUNChainRelayNetConn(chainID string, secret string, relayHost string, relayPort int, layer string, bridgeRole string) (net.Conn, error) {
	conn, err := openProbeChainRelayNetConn(chainID, secret, relayHost, relayPort, layer, bridgeRole)
	if err == nil || !isProbeLocalTUNUnsupportedDefaultRelayErr(layer, err) {
		return conn, err
	}
	var lastErr error = err
	for _, protocol := range probeChainRelayProtocolCandidates(layer) {
		cleanProtocol := normalizeProbeChainLinkLayer(protocol)
		if !isProbeChainRelaySupportedProtocol(cleanProtocol) {
			continue
		}
		result := openProbeChainRelayNetConnWithLayer(chainID, secret, relayHost, relayPort, cleanProtocol, bridgeRole, probeChainPortForwardDialTimeout+probeChainPortForwardResponseReadDeadline)
		if result.Err == nil {
			return result.Conn, nil
		}
		lastErr = result.Err
		if !isProbeChainRelayProtocolSwitchableError(result.Err) {
			break
		}
	}
	return nil, lastErr
}

func openProbeLocalTUNChainRelayDataStreamNetConn(chainID string, secret string, relayHost string, relayPort int, layer string) (net.Conn, error) {
	conn, err := openProbeChainRelayDataStreamNetConn(chainID, secret, relayHost, relayPort, layer, probeChainDownstreamOpenTimeout)
	if err == nil || !isProbeLocalTUNUnsupportedDefaultRelayErr(layer, err) {
		return conn, err
	}
	var lastErr error = err
	for _, protocol := range probeChainRelayProtocolCandidates(layer) {
		cleanProtocol := normalizeProbeChainLinkLayer(protocol)
		if !isProbeChainRelaySupportedProtocol(cleanProtocol) {
			continue
		}
		conn, openErr := openProbeChainRelayDataStreamNetConnWithRole(chainID, secret, relayHost, relayPort, cleanProtocol, probeChainBridgeRoleToNext, probeChainDownstreamOpenTimeout)
		if openErr == nil {
			return conn, nil
		}
		lastErr = openErr
		if !isProbeChainRelayProtocolSwitchableError(openErr) {
			break
		}
	}
	return nil, lastErr
}

func openProbeLocalTUNChainRelayNetConnWithResolvedHost(chainID string, secret string, relayHost string, relayPort int, layer string, bridgeRole string, relayDialHost string, relayHostHeader string, openTimeout time.Duration, cacheOnSuccess bool) (net.Conn, error) {
	return openProbeChainRelayNetConnWithResolvedHost(chainID, secret, relayHost, relayPort, layer, bridgeRole, relayDialHost, relayHostHeader, openTimeout, cacheOnSuccess)
}

func snapshotProbeLocalTUNChainRelayProtocolState(relayHost string, relayPort int) probeChainRelayProtocolStateSnapshot {
	return snapshotProbeChainProtocolState(relayHost, relayPort)
}

func probeLocalTUNChainRelaySpeedTest(endpoint probeLocalTUNChainEndpoint, protocol string) []probeChainRelaySpeedTestResult {
	return probeChainRelaySpeedTestDefault(endpoint.ChainID, endpoint.ChainSecret, endpoint.EntryHost, endpoint.EntryPort, endpoint.LinkLayer, protocol, probeChainRelaySpeedTestBytes)
}

func isProbeLocalTUNUnsupportedDefaultRelayErr(layer string, err error) bool {
	if err == nil || normalizeProbeChainLinkLayer(layer) != "" {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(text, "unsupported relay protocol") && !errors.Is(err, net.ErrClosed)
}
