package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/cloudhelper/probe_node/internal/transportnet"
	"net"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// Transport names must never be resolved through the intercepted/Fake IP DNS
// listener. Ask real bootstrap servers for AAAA, independently of business DNS.
var probeTransportLookupIPv6 = lookupProbeTransportIPv6
var probeTransportDNSWorkers = make(chan struct{}, 16)

func lookupProbeTransportIPv6(ctx context.Context, host string) ([]string, error) {
	query, err := buildProbeLocalDNSQueryA(host)
	if err != nil {
		return nil, err
	}
	query[len(query)-3] = byte(dnsmessage.TypeAAAA)
	for _, candidate := range currentProbeLocalDNSUpstreamCandidatesForDecision(probeLocalDNSRouteDecision{Group: "probe-transport", Action: "direct"}) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		reply, err := probeLocalDNSQueryUpstream(candidate, query)
		if err != nil {
			continue
		}
		var ips []string
		for _, raw := range extractProbeLocalDNSResponseIPsBestEffort(reply) {
			if ip := net.ParseIP(raw); ip != nil && ip.To4() == nil && ip.IsGlobalUnicast() {
				ips = append(ips, ip.String())
			}
		}
		// A valid empty answer is authoritative too; do not ask every server.
		return ips, nil
	}
	return nil, errors.New("transport AAAA lookup failed")
}

func resolveProbeTransportIPs(ctx context.Context, host string) ([]string, error) {
	return resolveProbeTransportIPsWithLookup(ctx, host, resolveProbeLocalDNSIPv4s, transportnet.HasIPv6Source())
}

func resolveProbeTransportIPsWithLookup(ctx context.Context, host string, lookup4 func(string) ([]string, error), ipv6Allowed ...bool) ([]string, error) {
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if ip := net.ParseIP(host); ip != nil {
		return []string{ip.String()}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	type answer struct {
		ips []string
		err error
	}
	results := make(chan answer, 2)
	lookup6 := probeTransportLookupIPv6
	start := func(lookup func() ([]string, error)) {
		go func() {
			select {
			case probeTransportDNSWorkers <- struct{}{}:
				defer func() { <-probeTransportDNSWorkers }()
			case <-ctx.Done():
				results <- answer{err: ctx.Err()}
				return
			}
			ips, err := lookup()
			results <- answer{ips, err}
		}()
	}
	start(func() ([]string, error) { return lookup4(host) })
	queries := 1
	if len(ipv6Allowed) == 0 || ipv6Allowed[0] {
		start(func() ([]string, error) { return lookup6(ctx, host) })
		queries++
	}
	var v4, v6 []string
	var failures []error
	var grace <-chan time.Time
	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for remaining := queries; remaining > 0; remaining-- {
		select {
		case result := <-results:
			if result.err != nil {
				failures = append(failures, result.err)
			}
			for _, raw := range result.ips {
				ip := net.ParseIP(raw)
				if ip == nil {
					continue
				}
				if b := ip.To4(); b != nil && b[0] == 198 && b[1]&254 == 18 {
					continue
				}
				if ip.To4() != nil {
					v4 = append(v4, ip.String())
				} else {
					v6 = append(v6, ip.String())
				}
			}
			if grace == nil && len(v4)+len(v6) > 0 {
				timer = time.NewTimer(200 * time.Millisecond)
				grace = timer.C
			}
		case <-grace:
			remaining = 1
		case <-ctx.Done():
			if len(v4)+len(v6) == 0 {
				return nil, ctx.Err()
			}
			remaining = 1
		}
	}
	// Interleave families so a long A list cannot exclude AAAA from the socket cap.
	out := make([]string, 0, 4)
	seen := map[string]bool{}
	for i := 0; i < len(v4) || i < len(v6); i++ {
		for _, list := range [][]string{v4, v6} {
			if i < len(list) && !seen[list[i]] && len(out) < 4 {
				out = append(out, list[i])
				seen[list[i]] = true
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("transport DNS has no usable address: %s: %v", host, errors.Join(failures...))
	}
	return out, nil
}
