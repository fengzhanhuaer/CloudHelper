package core

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type probeDNSUpstreams struct {
	DoH string `json:"doh"`
	DoT string `json:"dot"`
	DNS string `json:"dns"`
}

func defaultProbeDNSUpstreams() probeDNSUpstreams {
	return probeDNSUpstreams{"https://dns.alidns.com/dns-query", "dns.alidns.com:853", "223.5.5.5:53"}
}

func (c probeDNSUpstreams) Validate() error {
	for _, s := range []string{c.DoH, c.DoT, c.DNS} {
		if s == "" || len(s) > 1023 || strings.IndexFunc(s, func(r rune) bool { return r <= 32 || r >= 127 }) >= 0 {
			return errors.New("invalid DNS upstream")
		}
	}
	u, err := url.Parse(c.DoH)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" || strings.Contains(c.DoH, "\\") || strings.HasSuffix(u.Host, ":") {
		return errors.New("DoH must be an HTTPS URL without credentials or fragment")
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	if err = validateAddress(net.JoinHostPort(u.Hostname(), port), false); err != nil {
		return err
	}
	if err = validateAddress(c.DoT, false); err != nil {
		return err
	}
	return validateAddress(c.DNS, true)
}

func validateAddress(s string, literal bool) error {
	host, port, err := net.SplitHostPort(s)
	if err != nil || len(host) > 253 || host == "" {
		return errors.New("DNS upstream requires host:port")
	}
	for _, r := range port {
		if r < '0' || r > '9' {
			return errors.New("invalid DNS upstream port")
		}
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return errors.New("invalid DNS upstream port")
	}
	ip := net.ParseIP(host)
	if ip != nil {
		if ip.To4() == nil || ip.To4()[0] >= 224 || ip.Equal(net.IPv4bcast) || ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() || (ip.To4() != nil && ip[12] == 198 && (ip[13] == 18 || ip[13] == 19)) {
			return errors.New("DNS upstream cannot point to a local virtual resolver")
		}
		return nil
	}
	if literal {
		return errors.New("ordinary DNS must be an IP:port")
	}
	for _, r := range host {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-') {
			return errors.New("invalid DNS hostname")
		}
	}
	return nil
}

func currentProbeDNSUpstreams() probeDNSUpstreams {
	if ProbeRouteConfigStore == nil {
		return defaultProbeDNSUpstreams()
	}
	ProbeRouteConfigStore.mu.RLock()
	c := ProbeRouteConfigStore.data.DNSUpstreams
	ProbeRouteConfigStore.mu.RUnlock()
	if c.Validate() != nil {
		return defaultProbeDNSUpstreams()
	}
	return c
}
func mngRouteDNSUpstreamsHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"item": currentProbeDNSUpstreams()})
	case http.MethodPost:
		var c probeDNSUpstreams
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&c); err != nil {
			writeJSON(w, 400, map[string]string{"error": "invalid JSON"})
			return
		}
		if err := c.Validate(); err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
		if ProbeRouteConfigStore == nil {
			writeJSON(w, 503, map[string]string{"error": "route config unavailable"})
			return
		}
		if err := ProbeRouteConfigStore.update(func(data *probeRouteConfigStoreData) error { data.DNSUpstreams = c; return nil }); err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		scheduleProbeRouteConfigSyncToKnownNodes()
		writeJSON(w, 200, map[string]any{"item": c})
	default:
		http.Error(w, "Method not allowed", 405)
	}
}
