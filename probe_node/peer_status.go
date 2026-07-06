package main

import (
	"strings"
	"time"
)

type probePeerStatusSidePayload struct {
	Type           string                               `json:"type"`
	RequestID      string                               `json:"request_id,omitempty"`
	NodeID         string                               `json:"node_id,omitempty"`
	OK             bool                                 `json:"ok"`
	Scope          string                               `json:"scope,omitempty"`
	ProtocolState  probeChainRelayProtocolStateSnapshot `json:"protocol_state,omitempty"`
	BridgeSessions []probeChainBridgeSessionSnapshot    `json:"bridge_sessions,omitempty"`
	TCP            probeTCPDebugResultPayload           `json:"tcp,omitempty"`
	Speed          probeSpeedDebugResultPayload         `json:"speed,omitempty"`
	Substreams     probeSubstreamMonitorPayload         `json:"substreams,omitempty"`
	FetchedAt      string                               `json:"fetched_at,omitempty"`
	Error          string                               `json:"error,omitempty"`
	Timestamp      string                               `json:"timestamp,omitempty"`
}

func snapshotProbePeerStatusSidePayload(nodeID string, requestID string, scope string, protocolState probeChainRelayProtocolStateSnapshot) probePeerStatusSidePayload {
	now := time.Now().UTC()
	cleanRequestID := strings.TrimSpace(requestID)
	payload := probePeerStatusSidePayload{
		Type:           "peer_status_result",
		RequestID:      cleanRequestID,
		NodeID:         strings.TrimSpace(nodeID),
		OK:             true,
		Scope:          firstNonEmptyProbeTCPDebugString(strings.TrimSpace(scope), "chain_exit"),
		ProtocolState:  protocolState,
		BridgeSessions: nil,
		TCP:            globalProbeTCPDebugState.snapshotPayload(strings.TrimSpace(nodeID), cleanRequestID),
		Speed:          globalProbeSpeedDebugState.snapshotPayload(strings.TrimSpace(nodeID), cleanRequestID),
		Substreams:     snapshotProbeSubstreamMonitorPayload(strings.TrimSpace(nodeID), cleanRequestID, strings.TrimSpace(scope)),
		FetchedAt:      now.Format(time.RFC3339),
		Timestamp:      now.Format(time.RFC3339),
	}
	payload.TCP.Scope = payload.Scope
	payload.Speed.Scope = payload.Scope
	payload.Substreams.Scope = payload.Scope
	return payload
}

func probePeerStatusSideHasData(payload probePeerStatusSidePayload) bool {
	return strings.TrimSpace(payload.Type) != "" || payload.OK || payload.TCP.ActiveCount > 0 || payload.Speed.ActiveCount > 0 || payload.Substreams.ActiveCount > 0 || payload.Substreams.CompletedCount > 0 || payload.Substreams.FailureCount > 0 || strings.TrimSpace(payload.ProtocolState.Endpoint) != ""
}
