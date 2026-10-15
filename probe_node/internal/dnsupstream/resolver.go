package dnsupstream

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

type Candidate struct{ Kind, Address string }

func Candidates(c Config, system []string) []Candidate {
	out := []Candidate{{"doh", c.DoH}, {"dot", c.DoT}, {"dns", c.DNS}}
	seen := map[string]bool{c.DNS: true}
	for _, address := range system {
		if net.ParseIP(address) != nil {
			address = net.JoinHostPort(address, "53")
		}
		if validateAddress(address, true) != nil || seen[address] {
			continue
		}
		seen[address] = true
		out = append(out, Candidate{"dns", address})
	}
	return out
}

type Resolver struct {
	DialContext func(context.Context, string, string) (net.Conn, error)
	TLSConfig   *tls.Config
	Timeout     time.Duration
}

func (r Resolver) Resolve(packet []byte, c Config, system []string) ([]byte, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	var last error
	for _, candidate := range Candidates(c, system) {
		timeout := r.Timeout
		if timeout <= 0 {
			timeout = 3 * time.Second
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		reply, err := r.Query(ctx, candidate, packet, c, system)
		cancel()
		if err == nil {
			return reply, nil
		}
		last = err
	}
	return nil, last
}

func ValidateReply(query, reply []byte) error {
	var q, a dnsmessage.Message
	if q.Unpack(query) != nil || a.Unpack(reply) != nil || !a.Header.Response || a.Header.ID != q.Header.ID || len(q.Questions) != 1 || len(a.Questions) != 1 || a.Questions[0].Type != q.Questions[0].Type || a.Questions[0].Class != q.Questions[0].Class || !strings.EqualFold(a.Questions[0].Name.String(), q.Questions[0].Name.String()) || a.Header.RCode == dnsmessage.RCodeServerFailure || a.Header.RCode == dnsmessage.RCodeRefused {
		return errors.New("invalid or failed DNS reply")
	}
	return nil
}

func (r Resolver) dial(ctx context.Context, network, target string) (net.Conn, error) {
	if r.DialContext != nil {
		return r.DialContext(ctx, network, target)
	}
	return (&net.Dialer{}).DialContext(ctx, network, target)
}

func (r Resolver) target(ctx context.Context, address string, c Config, system []string) (string, string, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return "", "", err
	}
	if net.ParseIP(host) != nil {
		return address, host, nil
	}
	if host == "dns.alidns.com" {
		return net.JoinHostPort("223.5.5.5", port), host, nil
	}
	name, err := dnsmessage.NewName(host + ".")
	if err != nil {
		return "", "", err
	}
	q, err := (&dnsmessage.Message{Header: dnsmessage.Header{ID: 0x73a1, RecursionDesired: true}, Questions: []dnsmessage.Question{{Name: name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}}}).Pack()
	if err != nil {
		return "", "", err
	}
	for _, candidate := range Candidates(c, system)[2:] {
		reply, err := r.plain(ctx, candidate.Address, q, false)
		if err != nil {
			continue
		}
		var m dnsmessage.Message
		if m.Unpack(reply) != nil {
			continue
		}
		for _, a := range m.Answers {
			if ip, ok := a.Body.(*dnsmessage.AResource); ok {
				return net.JoinHostPort(net.IP(ip.A[:]).String(), port), host, nil
			}
		}
	}
	return "", "", errors.New("DNS upstream bootstrap failed")
}

func (r Resolver) tlsConfig(host string) *tls.Config {
	c := &tls.Config{MinVersion: tls.VersionTLS12}
	if r.TLSConfig != nil {
		c = r.TLSConfig.Clone()
	}
	c.ServerName = host
	return c
}

func (r Resolver) Query(ctx context.Context, candidate Candidate, packet []byte, c Config, system []string) ([]byte, error) {
	if len(packet) < 12 || len(packet) > 65535 {
		return nil, errors.New("invalid DNS query length")
	}
	address := candidate.Address
	if candidate.Kind == "dns" {
		return r.plain(ctx, address, packet, false)
	}
	var u *url.URL
	if candidate.Kind == "doh" {
		var err error
		u, err = url.Parse(address)
		if err != nil {
			return nil, err
		}
		port := u.Port()
		if port == "" {
			port = "443"
		}
		address = net.JoinHostPort(u.Hostname(), port)
	}
	target, host, err := r.target(ctx, address, c, system)
	if err != nil {
		return nil, err
	}
	if candidate.Kind == "dot" {
		conn, err := r.dial(ctx, "tcp", target)
		if err != nil {
			return nil, err
		}
		defer conn.Close()
		if deadline, ok := ctx.Deadline(); ok {
			_ = conn.SetDeadline(deadline)
		}
		secure := tls.Client(conn, r.tlsConfig(host))
		if err = secure.HandshakeContext(ctx); err != nil {
			return nil, err
		}
		reply, err := framed(secure, packet)
		if err == nil {
			err = ValidateReply(packet, reply)
		}
		return reply, err
	}
	if candidate.Kind != "doh" {
		return nil, errors.New("unknown DNS transport")
	}
	transport := &http.Transport{TLSClientConfig: r.tlsConfig(host), DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) { return r.dial(ctx, network, target) }}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("DoH redirect refused") }}
	req, err := http.NewRequestWithContext(ctx, "POST", u.String(), bytes.NewReader(packet))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/dns-message")
	req.Header.Set("Accept", "application/dns-message")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	media, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if resp.StatusCode != 200 || media != "application/dns-message" {
		return nil, errors.New("invalid DoH response")
	}
	reply, err := io.ReadAll(io.LimitReader(resp.Body, 65536))
	if err == nil {
		err = ValidateReply(packet, reply)
	}
	return reply, err
}

func framed(conn net.Conn, packet []byte) ([]byte, error) {
	var h [2]byte
	binary.BigEndian.PutUint16(h[:], uint16(len(packet)))
	if _, err := io.Copy(conn, bytes.NewReader(append(h[:], packet...))); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(conn, h[:]); err != nil {
		return nil, err
	}
	n := int(binary.BigEndian.Uint16(h[:]))
	if n < 12 {
		return nil, errors.New("short DNS frame")
	}
	b := make([]byte, n)
	_, err := io.ReadFull(conn, b)
	return b, err
}

func (r Resolver) plain(ctx context.Context, address string, packet []byte, tcp bool) ([]byte, error) {
	network := "udp"
	if tcp {
		network = "tcp"
	}
	conn, err := r.dial(ctx, network, address)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	var reply []byte
	if tcp {
		reply, err = framed(conn, packet)
	} else {
		_, err = conn.Write(packet)
		if err == nil {
			reply = make([]byte, 65535)
			var n int
			n, err = conn.Read(reply)
			reply = reply[:n]
		}
	}
	if err != nil {
		return nil, err
	}
	if len(reply) >= 12 && reply[2]&2 != 0 && !tcp {
		return r.plain(ctx, address, packet, true)
	}
	return reply, ValidateReply(packet, reply)
}
