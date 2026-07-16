package main

import (
	"net"
	"strconv"
	"strings"
)

type probeIPReportInterfaceIdentity struct {
	ID      string
	Aliases []string
}

func resolveProbeIPReportInterfaceIdentity(iface net.Interface) probeIPReportInterfaceIdentity {
	fallbackIDs := probeIPReportFallbackInterfaceIDs(iface)
	primaryID := normalizeProbeIPReportInterfaceID(probeIPReportPlatformStableInterfaceID(iface))
	if primaryID == "" && len(fallbackIDs) > 0 {
		primaryID = fallbackIDs[0]
	}

	seen := map[string]struct{}{}
	aliases := make([]string, 0, len(fallbackIDs))
	if primaryID != "" {
		seen[primaryID] = struct{}{}
	}
	for _, candidate := range fallbackIDs {
		id := normalizeProbeIPReportInterfaceID(candidate)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		aliases = append(aliases, id)
	}
	return probeIPReportInterfaceIdentity{ID: primaryID, Aliases: aliases}
}

func probeIPReportFallbackInterfaceIDs(iface net.Interface) []string {
	items := make([]string, 0, 3)
	if mac := strings.TrimSpace(iface.HardwareAddr.String()); mac != "" {
		items = append(items, "mac:"+mac)
	}
	if name := strings.TrimSpace(iface.Name); name != "" {
		items = append(items, "name:"+name)
	}
	if iface.Index > 0 {
		items = append(items, "index:"+strconv.Itoa(iface.Index))
	}
	return normalizeProbeIPReportInterfaceIDsPreserveOrder(items)
}

func probeIPReportInterfaceID(iface net.Interface) string {
	return resolveProbeIPReportInterfaceIdentity(iface).ID
}

func probeIPReportInterfaceIdentityIDs(identity probeIPReportInterfaceIdentity) []string {
	items := make([]string, 0, 1+len(identity.Aliases))
	items = append(items, identity.ID)
	items = append(items, identity.Aliases...)
	return normalizeProbeIPReportInterfaceIDsPreserveOrder(items)
}

func normalizeProbeIPReportInterfaceIDsPreserveOrder(items []string) []string {
	seen := make(map[string]struct{}, len(items))
	out := make([]string, 0, len(items))
	for _, item := range items {
		id := normalizeProbeIPReportInterfaceID(item)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}
