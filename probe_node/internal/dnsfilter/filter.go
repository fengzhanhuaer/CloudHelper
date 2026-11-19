// Package dnsfilter enforces local DNS-server restrictions on intercepted traffic.
// It does not modify controller policy or the host's firewall.
package dnsfilter

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

const FileName = "dns_filter.json"

type Config struct {
	BlockDoH  bool     `json:"block_doh"`
	BlockDoT  bool     `json:"block_dot"`
	Blacklist []string `json:"blacklist"`
}

func Normalize(c Config) (Config, error) {
	if len(c.Blacklist) > 32 {
		return Config{}, errors.New("DNS 黑名单最多 32 条")
	}
	out := Config{BlockDoH: c.BlockDoH, BlockDoT: c.BlockDoT, Blacklist: []string{}}
	seen := map[string]bool{}
	for _, value := range c.Blacklist {
		s := strings.TrimSpace(value)
		if s == "" {
			continue
		}
		if ip := net.ParseIP(s); ip != nil {
			s = ip.String()
		} else {
			s = strings.ToLower(strings.TrimSuffix(s, "."))
			if len(s) == 0 || len(s) > 253 {
				return Config{}, fmt.Errorf("无效 DNS 域名或 IP：%s", value)
			}
			for _, label := range strings.Split(s, ".") {
				if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
					return Config{}, fmt.Errorf("无效 DNS 域名：%s", value)
				}
				for _, r := range label {
					if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
						return Config{}, fmt.Errorf("无效 DNS 域名：%s", value)
					}
				}
			}
		}
		if !seen[s] {
			out.Blacklist = append(out.Blacklist, s)
			seen[s] = true
		}
	}
	return out, nil
}

func matches(c Config, domain string) bool {
	domain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
	if ip := net.ParseIP(domain); ip != nil {
		domain = ip.String()
	}
	for _, entry := range c.Blacklist {
		if domain == entry || (net.ParseIP(entry) == nil && strings.HasSuffix(domain, "."+entry)) {
			return true
		}
	}
	return false
}

type memo struct {
	domain string
	fake   bool
	until  time.Time
}

// Store caches settings and local mapping decisions. Only Save writes to disk.
// Lookups supplied to BlockTarget must only consult existing in-memory mappings.
type Store struct {
	mu           sync.Mutex
	path         string
	dir          string
	loaded       bool
	active       atomic.Bool
	loadErr      error
	config       Config
	version      uint64
	cacheVersion uint64
	cache        map[string]memo
	resolved     map[string][]string
	nextRefresh  time.Time
	refreshing   bool
}

func (s *Store) Ready() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.loaded }

func (s *Store) Enabled() bool { return s.active.Load() }

// Ensure avoids copying blacklist strings or reading files on the packet path.
func (s *Store) Ensure(dir string) error {
	s.mu.Lock()
	ready, err := s.loaded && s.dir == dir, s.loadErr
	s.mu.Unlock()
	if ready {
		return err
	}
	_, err = s.Current(dir)
	return err
}

func clone(c Config) Config { c.Blacklist = append([]string{}, c.Blacklist...); return c }

func (s *Store) Current(dir string) (Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(dir) == "" {
		return Config{}, errors.New("DNS 配置目录为空")
	}
	path := filepath.Join(dir, FileName)
	if s.loaded && s.path == path {
		return clone(s.config), s.loadErr
	}
	c := Config{Blacklist: []string{}}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		err = nil
	} else if err == nil {
		err = json.Unmarshal(raw, &c)
		if err == nil {
			c, err = Normalize(c)
		}
	}
	if err != nil {
		c = Config{Blacklist: []string{}}
	}
	s.path, s.loaded, s.config, s.loadErr = path, true, c, err
	s.dir = dir
	s.active.Store(c.BlockDoH || c.BlockDoT)
	s.invalidateLocked()
	return clone(c), err
}

func (s *Store) invalidateLocked() {
	s.version++
	s.cacheVersion++
	s.cache = map[string]memo{}
	s.resolved = map[string][]string{}
	s.nextRefresh = time.Time{}
}

func (s *Store) Save(dir string, c Config) error {
	c, err := Normalize(c)
	if err != nil {
		return err
	}
	if strings.TrimSpace(dir) == "" {
		return errors.New("DNS 配置目录为空")
	}
	_, _ = s.Current(dir)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr == nil && reflect.DeepEqual(s.config, c) {
		return nil
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".dns-filter-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
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
	path := filepath.Join(dir, FileName)
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		return err
	}
	s.config, s.path, s.loaded, s.loadErr = c, path, true, nil
	s.dir = dir
	s.active.Store(c.BlockDoH || c.BlockDoT)
	s.invalidateLocked()
	return nil
}

func (s *Store) BlockDomain(domain string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.config.BlockDoH && matches(s.config, domain)
}

func (s *Store) Invalidate(ip string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cacheVersion++
	if ip == "" {
		s.cache = map[string]memo{}
	} else {
		delete(s.cache, ip)
	}
}

func (s *Store) RememberResolved(domain string, ips []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.config.BlockDoH || !matches(s.config, domain) {
		return
	}
	clean := []string{}
	for _, raw := range ips {
		if ip := net.ParseIP(raw); ip != nil {
			clean = append(clean, ip.String())
			if len(clean) == 4 {
				break
			}
		}
	}
	if len(clean) > 0 {
		s.resolved[domain] = clean
	}
}

func (s *Store) BlockTarget(ip string, port uint16, lookup func(string) (string, bool)) bool {
	if !s.Enabled() {
		return false
	}
	s.mu.Lock()
	if port == 853 && s.config.BlockDoT {
		s.mu.Unlock()
		return true
	}
	if !s.config.BlockDoH {
		s.mu.Unlock()
		return false
	}
	if literal := net.ParseIP(ip); literal != nil {
		ip = literal.String()
	}
	if port == 443 || port == 0 {
		if matches(s.config, ip) {
			s.mu.Unlock()
			return true
		}
		for _, ips := range s.resolved {
			for _, value := range ips {
				if value == ip {
					s.mu.Unlock()
					return true
				}
			}
		}
	}
	entry, found := s.cache[ip]
	if !found || (!entry.fake && time.Now().After(entry.until)) {
		version := s.cacheVersion
		s.mu.Unlock()
		if lookup == nil {
			return false
		}
		domain, fake := lookup(ip)
		entry = memo{domain: domain, fake: fake, until: time.Now().Add(30 * time.Second)}
		if domain == "" {
			entry.until = time.Now().Add(time.Second)
		}
		s.mu.Lock()
		// A settings/mapping update must win over a lookup started before it.
		if version != s.cacheVersion {
			s.mu.Unlock()
			return s.BlockTarget(ip, port, lookup)
		}
		if len(s.cache) >= 4096 {
			s.cache = map[string]memo{}
		}
		if s.cache == nil {
			s.cache = map[string]memo{}
		}
		s.cache[ip] = entry
	}
	blocked := (matches(s.config, entry.domain) || matches(s.config, ip)) && (entry.fake || port == 443 || port == 0)
	s.mu.Unlock()
	return blocked
}

// BlockPacket runs before existing-flow fast paths, including TCP/UDP fragments.
func (s *Store) BlockPacket(packet []byte, lookup func(string) (string, bool)) bool {
	if !s.Enabled() {
		return false
	}
	if len(packet) < 20 || packet[0]>>4 != 4 {
		return false
	}
	ihl := int(packet[0]&15) * 4
	if ihl < 20 || len(packet) < ihl || (packet[9] != 6 && packet[9] != 17) {
		return false
	}
	port := uint16(0)
	if binary.BigEndian.Uint16(packet[6:8])&0x1fff == 0 {
		if len(packet) < ihl+4 {
			return false
		}
		port = binary.BigEndian.Uint16(packet[ihl+2 : ihl+4])
	}
	return s.BlockTarget(net.IP(packet[16:20]).String(), port, lookup)
}

// Refresh resolves blacklist names in the background, at most once per five
// minutes. Failed refreshes retain known addresses; runtime results are not saved.
func (s *Store) Refresh(resolve func(string) ([]string, error)) {
	if resolve == nil || !s.Enabled() {
		return
	}
	s.mu.Lock()
	if !s.config.BlockDoH || s.refreshing || time.Now().Before(s.nextRefresh) {
		s.mu.Unlock()
		return
	}
	domains := []string{}
	for _, entry := range s.config.Blacklist {
		if net.ParseIP(entry) == nil {
			domains = append(domains, entry)
		}
	}
	if len(domains) == 0 {
		s.mu.Unlock()
		return
	}
	version := s.version
	s.refreshing = true
	s.nextRefresh = time.Now().Add(5 * time.Minute)
	s.mu.Unlock()
	go func() {
		defer func() { s.mu.Lock(); s.refreshing = false; s.mu.Unlock() }()
		for _, domain := range domains {
			ips, err := resolve(domain)
			s.mu.Lock()
			if s.version != version {
				s.mu.Unlock()
				return
			}
			if err == nil && len(ips) > 0 {
				s.resolved[domain] = append([]string{}, ips...)
			}
			s.mu.Unlock()
		}
	}()
}

func ResolveIPv4(domain string, query func([]byte) ([]byte, error)) ([]string, error) {
	name, err := dnsmessage.NewName(domain + ".")
	if err != nil {
		return nil, err
	}
	q, err := (&dnsmessage.Message{Header: dnsmessage.Header{ID: 0x711c, RecursionDesired: true}, Questions: []dnsmessage.Question{{Name: name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}}}).Pack()
	if err != nil {
		return nil, err
	}
	resp, err := query(q)
	if err != nil {
		return nil, err
	}
	var m dnsmessage.Message
	if err = m.Unpack(resp); err != nil {
		return nil, err
	}
	if !m.Header.Response || m.Header.ID != 0x711c || m.Header.RCode != dnsmessage.RCodeSuccess || len(m.Questions) != 1 || !strings.EqualFold(m.Questions[0].Name.String(), name.String()) {
		return nil, errors.New("invalid DNS response")
	}
	ips := []string{}
	for _, answer := range m.Answers {
		if a, ok := answer.Body.(*dnsmessage.AResource); ok {
			ips = append(ips, net.IP(a.A[:]).String())
			if len(ips) == 4 {
				break
			}
		}
	}
	return ips, nil
}
