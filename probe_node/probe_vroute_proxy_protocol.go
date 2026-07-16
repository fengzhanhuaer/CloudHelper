package main

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	probeVRouteProxySessionIDBytes = 16
	probeVRouteProxyTCPChunkBytes  = 32 * 1024
)

type probeVRouteProxyTCPOpenPayload struct {
	SessionID    string `json:"session_id"`
	TargetAddr   string `json:"target_addr"`
	SourceNodeID string `json:"source_node_id"`
	ExitNodeID   string `json:"exit_node_id"`
}

type probeVRouteProxyTCPOpenResultPayload struct {
	SessionID string `json:"session_id"`
	OK        bool   `json:"ok"`
	Error     string `json:"error,omitempty"`
}

type probeVRouteProxyClosePayload struct {
	SessionID string `json:"session_id"`
	Error     string `json:"error,omitempty"`
}

var probeVRouteProxyFrameSender func(subType uint16, payload []byte, path []string) error

func handleProbeVRouteProxyFrame(runtime *probeVirtualRouterRuntime, subType uint16, payload []byte, framePath []string) error {
	path := cleanProbeVirtualRouterPath(framePath)
	if len(path) == 0 {
		return errors.New("vroute proxy frame path is empty")
	}
	localNodeID := currentProbeVirtualRouterLocalNodeIDForRuntime(runtime)
	if localNodeID == "" {
		return errors.New("local virtual router node id is empty")
	}
	if nextNodeID := probeVirtualRouterNextHopInPath(path, localNodeID); nextNodeID != "" {
		return forwardProbeVirtualRouterBusinessAlongPath(probeVirtualRouterFrameMainTypeProxy, subType, payload, path)
	}
	if normalizeProbeRouteNodeID(path[len(path)-1]) != localNodeID {
		return fmt.Errorf("vroute proxy frame does not target local node: local=%s path=%s", localNodeID, strings.Join(path, ">"))
	}
	switch subType {
	case probeVirtualRouterProxySubTypeTCPOpen:
		return handleProbeVRouteProxyTCPOpen(payload, path)
	case probeVirtualRouterProxySubTypeTCPOpenResult:
		return handleProbeVRouteProxyTCPOpenResult(payload)
	case probeVirtualRouterProxySubTypeTCPData:
		return handleProbeVRouteProxyTCPData(payload)
	case probeVirtualRouterProxySubTypeTCPClose:
		return handleProbeVRouteProxyTCPClose(payload)
	case probeVirtualRouterProxySubTypeUDPRequest:
		return handleProbeVRouteProxyUDPRequest(payload, path)
	case probeVirtualRouterProxySubTypeUDPResponse:
		return handleProbeVRouteProxyUDPResponse(payload)
	case probeVirtualRouterProxySubTypeUDPClose:
		return handleProbeVRouteProxyUDPClose(payload)
	default:
		return fmt.Errorf("unsupported vroute proxy subtype=%d", subType)
	}
}

func forwardProbeVRouteProxyFrame(subType uint16, payload []byte, path []string) error {
	if probeVRouteProxyFrameSender != nil {
		return probeVRouteProxyFrameSender(subType, payload, path)
	}
	return forwardProbeVirtualRouterBusinessAlongPath(probeVirtualRouterFrameMainTypeProxy, subType, payload, path)
}

func marshalProbeVRouteProxyJSON(value any) ([]byte, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if len(payload) > probeVirtualRouterFrameMaxDataBytes {
		return nil, errors.New("vroute proxy json payload is too large")
	}
	return payload, nil
}

func marshalProbeVRouteProxyTCPData(sessionID string, data []byte) ([]byte, error) {
	id, err := decodeProbeVRouteProxySessionID(sessionID)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, errors.New("vroute proxy tcp data is empty")
	}
	if len(data)+probeVRouteProxySessionIDBytes > probeVirtualRouterFrameMaxDataBytes {
		return nil, errors.New("vroute proxy tcp data is too large")
	}
	payload := make([]byte, probeVRouteProxySessionIDBytes+len(data))
	copy(payload, id)
	copy(payload[probeVRouteProxySessionIDBytes:], data)
	return payload, nil
}

func unmarshalProbeVRouteProxyTCPData(payload []byte) (string, []byte, error) {
	if len(payload) <= probeVRouteProxySessionIDBytes {
		return "", nil, errors.New("invalid vroute proxy tcp data payload")
	}
	return hex.EncodeToString(payload[:probeVRouteProxySessionIDBytes]), payload[probeVRouteProxySessionIDBytes:], nil
}

func probeVRouteProxyFrameDispatchHash(subType uint16, payload []byte, seed uint32) uint32 {
	sessionID := probeVRouteProxyFrameSessionID(subType, payload)
	if sessionID == "" {
		return seed
	}
	const fnvPrime uint32 = 16777619
	h := seed
	for index := 0; index < len(sessionID); index++ {
		h ^= uint32(sessionID[index])
		h *= fnvPrime
	}
	return h
}

func probeVRouteProxyFrameSessionID(subType uint16, payload []byte) string {
	switch subType {
	case probeVirtualRouterProxySubTypeTCPData, probeVirtualRouterProxySubTypeUDPRequest, probeVirtualRouterProxySubTypeUDPResponse:
		if len(payload) >= probeVRouteProxySessionIDBytes {
			return hex.EncodeToString(payload[:probeVRouteProxySessionIDBytes])
		}
		return ""
	default:
		var value struct {
			SessionID string `json:"session_id"`
		}
		if json.Unmarshal(payload, &value) != nil {
			return ""
		}
		if _, err := decodeProbeVRouteProxySessionID(value.SessionID); err != nil {
			return ""
		}
		return strings.ToLower(strings.TrimSpace(value.SessionID))
	}
}

func decodeProbeVRouteProxySessionID(sessionID string) ([]byte, error) {
	cleanID := strings.ToLower(strings.TrimSpace(sessionID))
	if len(cleanID) != probeVRouteProxySessionIDBytes*2 {
		return nil, errors.New("invalid vroute proxy session id length")
	}
	id, err := hex.DecodeString(cleanID)
	if err != nil || len(id) != probeVRouteProxySessionIDBytes {
		return nil, errors.New("invalid vroute proxy session id")
	}
	return id, nil
}
