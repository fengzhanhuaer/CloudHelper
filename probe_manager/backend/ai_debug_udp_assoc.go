package backend

import (
	"errors"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

type aiDebugUDPAssociationItemPayload struct {
	Key              string `json:"key"`
	AssocKeyV2       string `json:"assoc_key_v2,omitempty"`
	FlowID           string `json:"flow_id,omitempty"`
	SourceKey        string `json:"source_key,omitempty"`
	SourceRefs       int64  `json:"source_refs,omitempty"`
	Target           string `json:"target,omitempty"`
	RouteTarget      string `json:"route_target,omitempty"`
	RouteFingerprint string `json:"route_fingerprint,omitempty"`
	NodeID           string `json:"node_id,omitempty"`
	Group            string `json:"group,omitempty"`
	NATMode          string `json:"nat_mode,omitempty"`
	TTLProfile       string `json:"ttl_profile,omitempty"`
	IdleTimeoutMS    int64  `json:"idle_timeout_ms,omitempty"`
	GCIntervalMS     int64  `json:"gc_interval_ms,omitempty"`
	Direct           bool   `json:"direct"`
	Transport        string `json:"transport,omitempty"`
	Refs             int32  `json:"refs,omitempty"`
	Active           bool   `json:"active"`
	LastActive       string `json:"last_active,omitempty"`
	IdleMS           int64  `json:"idle_ms"`
}

type aiDebugUDPAssociationsPayload struct {
	Kind      string                             `json:"kind"`
	Scope     string                             `json:"scope"`
	NodeID    string                             `json:"node_id,omitempty"`
	Count     int                                `json:"count"`
	Items     []aiDebugUDPAssociationItemPayload `json:"items"`
	FetchedAt string                             `json:"fetched_at"`
}

func (s *aiDebugService) handleNetworkUDPAssociationsManager(w http.ResponseWriter, r *http.Request) {
	service, err := aiDebugActiveNetworkAssistant()
	if err != nil {
		s.writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	payload, err := buildAIDebugManagerUDPAssociationsPayload(service)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.writeJSON(w, http.StatusOK, payload)
}

func (s *aiDebugService) handleNetworkUDPAssociationsController(w http.ResponseWriter, r *http.Request) {
	payload, err := fetchControllerUDPAssociationsForDebug()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.writeJSON(w, http.StatusOK, payload)
}

func (s *aiDebugService) handleNetworkUDPAssociationsProbe(w http.ResponseWriter, r *http.Request) {
	nodeID := strings.TrimSpace(r.URL.Query().Get("node_id"))
	if nodeID == "" {
		s.writeErrorMessage(w, http.StatusBadRequest, "node_id is required")
		return
	}
	payload, err := fetchProbeUDPAssociationsForDebug(nodeID)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.writeJSON(w, http.StatusOK, payload)
}

func buildAIDebugManagerUDPAssociationsPayload(service *networkAssistantService) (aiDebugUDPAssociationsPayload, error) {
	if service == nil {
		return aiDebugUDPAssociationsPayload{}, errors.New("network assistant service is not initialized")
	}
	service.mu.RLock()
	items := make([]aiDebugUDPAssociationItemPayload, 0, len(service.tunUDPRelays))
	for key, relay := range service.tunUDPRelays {
		if relay == nil {
			continue
		}
		sourceKey := strings.TrimSpace(relay.sourceKey)
		sourceRefs := int64(0)
		if sourceKey != "" {
			if source, ok := service.tunUDPSources[sourceKey]; ok && source != nil {
				sourceRefs = source.refs.Load()
			}
		}
		item := aiDebugUDPAssociationItemPayload{
			Key:              strings.TrimSpace(key),
			AssocKeyV2:       strings.TrimSpace(relay.assocKeyV2),
			FlowID:           strings.TrimSpace(relay.flowID),
			SourceKey:        sourceKey,
			SourceRefs:       sourceRefs,
			Target:           strings.TrimSpace(net.JoinHostPort(relay.dstIP.String(), strconv.Itoa(int(relay.dstPort)))),
			RouteTarget:      strings.TrimSpace(relay.routeTarget),
			RouteFingerprint: strings.TrimSpace(relay.routeFingerprint),
			NodeID:           strings.TrimSpace(relay.routeNodeID),
			Group:            strings.TrimSpace(relay.routeGroup),
			NATMode:          strings.TrimSpace(relay.natMode),
			TTLProfile:       strings.TrimSpace(relay.ttlProfile),
			IdleTimeoutMS:    relay.idleTimeout.Milliseconds(),
			GCIntervalMS:     relay.gcInterval.Milliseconds(),
			Direct:           relay.routeDirect,
			Active:           !relay.closed.Load(),
		}
		switch {
		case relay.directConn != nil:
			item.Transport = "direct"
		case relay.tunnelStream != nil:
			item.Transport = "tunnel"
		default:
			item.Transport = "unknown"
		}
		if lastActive := relay.lastActiveUnix.Load(); lastActive > 0 {
			lastActiveAt := time.Unix(lastActive, 0).UTC()
			item.LastActive = lastActiveAt.Format(time.RFC3339)
			item.IdleMS = time.Since(lastActiveAt).Milliseconds()
		}
		items = append(items, item)
	}
	service.mu.RUnlock()
	sort.Slice(items, func(i, j int) bool {
		if items[i].Target == items[j].Target {
			return items[i].Key < items[j].Key
		}
		return items[i].Target < items[j].Target
	})
	return aiDebugUDPAssociationsPayload{
		Kind:      "network_udp_associations",
		Scope:     "manager",
		Count:     len(items),
		Items:     items,
		FetchedAt: time.Now().Format(time.RFC3339),
	}, nil
}

func fetchControllerUDPAssociationsForDebug() (aiDebugUDPAssociationsPayload, error) {
	token, baseURL, err := loadManagerDebugControllerAuth()
	if err != nil {
		return aiDebugUDPAssociationsPayload{}, err
	}
	payload, err := callAdminWSForDebug[aiDebugUDPAssociationsPayload](baseURL, token, "admin.tunnel.udp.associations", map[string]any{})
	if err != nil {
		return aiDebugUDPAssociationsPayload{}, err
	}
	payload.Kind = firstNonEmpty(strings.TrimSpace(payload.Kind), "network_udp_associations")
	payload.Scope = firstNonEmpty(strings.TrimSpace(payload.Scope), "controller")
	payload.FetchedAt = firstNonEmpty(strings.TrimSpace(payload.FetchedAt), time.Now().Format(time.RFC3339))
	payload.Count = len(payload.Items)
	return payload, nil
}

func fetchProbeUDPAssociationsForDebug(nodeID string) (aiDebugUDPAssociationsPayload, error) {
	token, baseURL, err := loadManagerDebugControllerAuth()
	if err != nil {
		return aiDebugUDPAssociationsPayload{}, err
	}
	payload, err := callAdminWSForDebug[aiDebugUDPAssociationsPayload](baseURL, token, "admin.probe.udp.associations.get", map[string]any{
		"node_id": strings.TrimSpace(nodeID),
	})
	if err != nil {
		return aiDebugUDPAssociationsPayload{}, err
	}
	payload.Kind = firstNonEmpty(strings.TrimSpace(payload.Kind), "network_udp_associations")
	payload.Scope = firstNonEmpty(strings.TrimSpace(payload.Scope), "probe")
	payload.NodeID = firstNonEmpty(strings.TrimSpace(payload.NodeID), strings.TrimSpace(nodeID))
	payload.FetchedAt = firstNonEmpty(strings.TrimSpace(payload.FetchedAt), time.Now().Format(time.RFC3339))
	payload.Count = len(payload.Items)
	return payload, nil
}
