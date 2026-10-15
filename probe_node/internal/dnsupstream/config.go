// Package dnsupstream defines the persisted, controller-managed DNS fallback chain.
package dnsupstream

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

type Config struct {
	DoH string `json:"doh"`
	DoT string `json:"dot"`
	DNS string `json:"dns"`
}

func Default() Config {
	return Config{"https://dns.alidns.com/dns-query", "dns.alidns.com:853", "223.5.5.5:53"}
}

func (c Config) Validate() error {
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

// Store serializes updates and only writes changed values. Each node has one store.
type Store struct {
	mu     sync.Mutex
	path   string
	config Config
	loaded bool
}

func (s *Store) Current(dir string) (Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load(dir)
}

func (s *Store) load(dir string) (Config, error) {
	if strings.TrimSpace(dir) == "" {
		return Default(), errors.New("DNS config directory is required")
	}
	path := filepath.Join(dir, "dns_upstreams.json")
	if s.loaded && s.path == path {
		return s.config, nil
	}
	c := Default()
	raw, err := os.ReadFile(path)
	if err == nil {
		if err = json.Unmarshal(raw, &c); err != nil {
			return Default(), err
		}
		if err = c.Validate(); err != nil {
			return Default(), err
		}
	} else if !os.IsNotExist(err) {
		return Default(), err
	}
	if os.IsNotExist(err) {
		if err = save(path, c); err != nil {
			return Default(), err
		}
	}
	s.config = c
	s.path = path
	s.loaded = true
	return c, nil
}

func (s *Store) Apply(dir string, c *Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c != nil {
		if err := c.Validate(); err != nil {
			return err
		}
	}
	old, err := s.load(dir)
	if c == nil {
		return err
	} // Older controllers do not reset saved values.
	if strings.TrimSpace(dir) == "" {
		return errors.New("DNS config directory is required")
	}
	if err == nil && old == *c {
		return nil
	}
	path := filepath.Join(dir, "dns_upstreams.json")
	if err = save(path, *c); err != nil {
		return err
	}
	s.path = path
	s.config = *c
	s.loaded = true
	return nil
}

func save(path string, c Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".dns-upstreams-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(append(raw, '\n'))
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(name, path)
	}
	if err != nil {
		return fmt.Errorf("persist DNS upstreams: %w", err)
	}
	return nil
}
