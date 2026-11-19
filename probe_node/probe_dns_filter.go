package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/cloudhelper/probe_node/internal/dnsfilter"
)

var probeDNSFilter = &dnsfilter.Store{}

func ensureProbeDNSFilter() {
	if !probeDNSFilter.Ready() {
		if dir, err := resolveDataDir(); err == nil {
			_, _ = probeDNSFilter.Current(dir)
		}
	}
	probeDNSFilter.Refresh(func(domain string) ([]string, error) {
		return dnsfilter.ResolveIPv4(domain, func(q []byte) ([]byte, error) {
			reply, _, err := resolveProbeVirtualRouterDNSUpstreamResponse(q, domain, probeLocalDNSRouteDecision{})
			return reply, err
		})
	})
}

func probeDNSFilterBlocksPacket(packet []byte) bool {
	ensureProbeDNSFilter()
	return probeDNSFilter.BlockPacket(packet, probeDNSFilterLookupIP)
}

func probeDNSFilterBlocksTarget(host, port string) bool {
	ensureProbeDNSFilter()
	n, err := strconv.ParseUint(port, 10, 16)
	return err == nil && probeDNSFilter.BlockTarget(host, uint16(n), probeDNSFilterLookupIP)
}

func probeDNSFilterLookupIP(ip string) (string, bool) {
	if item, ok := currentProbeVirtualRouterStoredFakeIPEntryByIP(ip); ok {
		return item.Domain, true
	}
	if item, ok := lookupProbeLocalDNSFakeIPEntry(ip); ok {
		return item.Domain, true
	}
	if item, ok := lookupProbeLocalDNSRouteHintEntryByIP(ip); ok {
		return item.Domain, false
	}
	return "", false
}

func probeLocalDNSFilterHandler(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	dir, err := resolveDataDir()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	switch r.Method {
	case http.MethodGet:
	case http.MethodPost:
		var c dnsfilter.Config
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
		decoder.DisallowUnknownFields()
		if err = decoder.Decode(&c); err == nil {
			var tail any
			if decoder.Decode(&tail) != io.EOF {
				err = errors.New("invalid trailing JSON")
			}
		}
		if err == nil {
			err = probeDNSFilter.Save(dir, c)
		}
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	c, err := probeDNSFilter.Current(dir)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	ensureProbeDNSFilter()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "config": c})
}
