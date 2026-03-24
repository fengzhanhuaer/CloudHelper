package core

import (
	"net"
	"net/http"
	"strconv"
	"strings"
)

type probeLinkConfigResponse struct {
	NodeID        string `json:"node_id"`
	Enabled       bool   `json:"enabled"`
	ServiceType   string `json:"service_type,omitempty"`
	ServiceScheme string `json:"service_scheme"`
	ServiceHost   string `json:"service_host"`
	ServicePort   int    `json:"service_port"`
	ListenAddr    string `json:"listen_addr"`
	UpdatedAt     string `json:"updated_at,omitempty"`
}

func ProbeLinkConfigHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !isHTTPSRequest(r) {
		writeJSON(w, http.StatusUpgradeRequired, map[string]string{"error": "https is required"})
		return
	}

	nodeID, err := authenticateProbeRequestOrQuerySecret(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}

	node, ok := getProbeNodeByID(nodeID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "probe node not found"})
		return
	}

	writeJSON(w, http.StatusOK, buildProbeLinkConfigResponse(node, nodeID))
}

func buildProbeLinkConfigResponse(node probeNodeRecord, nodeID string) probeLinkConfigResponse {
	serviceScheme := normalizeProbeEndpointScheme(node.ServiceScheme)
	serviceHost := strings.TrimSpace(node.ServiceHost)
	servicePort := normalizeProbeServicePort(node.ServicePort)
	listenAddr := buildProbeServiceListenAddr(serviceHost, servicePort)
	enabled := listenAddr != "" && shouldEnableProbeHTTPServiceForScheme(serviceScheme)

	return probeLinkConfigResponse{
		NodeID:        normalizeProbeNodeID(nodeID),
		Enabled:       enabled,
		ServiceType:   serviceScheme,
		ServiceScheme: serviceScheme,
		ServiceHost:   serviceHost,
		ServicePort:   servicePort,
		ListenAddr:    listenAddr,
		UpdatedAt:     strings.TrimSpace(node.UpdatedAt),
	}
}

func shouldEnableProbeHTTPServiceForScheme(serviceScheme string) bool {
	switch normalizeProbeEndpointScheme(serviceScheme) {
	case "https", "http3", "websocket":
		return true
	default:
		return false
	}
}

func buildProbeServiceListenAddr(serviceHost string, servicePort int) string {
	host := strings.TrimSpace(serviceHost)
	if host == "" {
		return ""
	}
	if servicePort <= 0 || servicePort > 65535 {
		return ""
	}

	if parsedHost, parsedPort, err := net.SplitHostPort(host); err == nil {
		host = strings.TrimSpace(parsedHost)
		if p, pErr := strconv.Atoi(strings.TrimSpace(parsedPort)); pErr == nil && p > 0 && p <= 65535 {
			servicePort = p
		}
	}
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if host == "" {
		return ""
	}
	return net.JoinHostPort(host, strconv.Itoa(servicePort))
}
