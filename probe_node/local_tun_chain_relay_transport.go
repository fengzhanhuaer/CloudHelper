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

func openProbeLocalTUNChainRelayNetConnForEndpoint(endpoint probeLocalTUNChainEndpoint, bridgeRole string) (net.Conn, error) {
	if !endpoint.PreserveRelayDomain {
		return probeLocalTUNOpenChainRelayNetConn(endpoint.ChainID, endpoint.ChainSecret, endpoint.EntryHost, endpoint.EntryPort, endpoint.LinkLayer, bridgeRole)
	}
	conn, err := openProbeChainRelayNetConnWithLayerConnAndDomainPolicy(endpoint.ChainID, endpoint.ChainSecret, endpoint.EntryHost, endpoint.EntryPort, endpoint.LinkLayer, bridgeRole, probeChainPortForwardDialTimeout+probeChainPortForwardResponseReadDeadline, true)
	if err == nil || !isProbeLocalTUNUnsupportedDefaultRelayErr(endpoint.LinkLayer, err) {
		return conn, err
	}
	var lastErr error = err
	for _, protocol := range probeChainRelayProtocolCandidates(endpoint.LinkLayer) {
		cleanProtocol := normalizeProbeChainLinkLayer(protocol)
		if !isProbeChainRelaySupportedProtocol(cleanProtocol) {
			continue
		}
		result := probeChainRelayProtocolDialResult{Protocol: cleanProtocol}
		result.StartedAt = time.Now()
		result.Conn, result.Err = openProbeChainRelayNetConnWithLayerConnAndDomainPolicy(endpoint.ChainID, endpoint.ChainSecret, endpoint.EntryHost, endpoint.EntryPort, cleanProtocol, bridgeRole, probeChainPortForwardDialTimeout+probeChainPortForwardResponseReadDeadline, true)
		result.EndedAt = time.Now()
		result.Latency = result.EndedAt.Sub(result.StartedAt)
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
