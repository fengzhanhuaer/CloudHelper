package main

import "time"

type probePeerStatusGroupSnapshot struct {
	Group     string                               `json:"group,omitempty"`
	Entry     probePeerStatusSidePayload           `json:"entry,omitempty"`
	Exit      probePeerStatusSidePayload           `json:"exit,omitempty"`
	Link      probeChainRelayProtocolStateSnapshot `json:"link,omitempty"`
	FetchedAt string                               `json:"fetched_at,omitempty"`
	Error     string                               `json:"error,omitempty"`
}

type probeLocalPeerStatusMonitorSnapshot struct {
	FetchedAt string                         `json:"fetched_at,omitempty"`
	Groups    []probePeerStatusGroupSnapshot `json:"groups,omitempty"`
}

func currentProbeLocalPeerStatusMonitorSnapshot() probeLocalPeerStatusMonitorSnapshot {
	return probeLocalPeerStatusMonitorSnapshot{
		FetchedAt: time.Now().UTC().Format(time.RFC3339),
		Groups:    []probePeerStatusGroupSnapshot{},
	}
}
