// Package node implements probe node CRUD persistence, migrated from probe_manager/backend.
// RQ-004: migrate and maintain behavioral equivalence with the original implementation.
package node

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const storeFile = "probe_nodes.json"

// CloudflareDDNSRecord holds a Cloudflare DDNS record entry for a node.
type CloudflareDDNSRecord struct {
	RecordClass string `json:"record_class"`
	RecordName  string `json:"record_name"`
}

// Node represents a probe node entry.
type Node struct {
	NodeNo                int                    `json:"node_no"`
	NodeName              string                 `json:"node_name"`
	Remark                string                 `json:"remark"`
	DDNS                  string                 `json:"ddns"`
	CloudflareDDNSRecords []CloudflareDDNSRecord `json:"cloudflare_ddns_records,omitempty"`
	NodeSecret            string                 `json:"node_secret"`
	TargetSystem          string                 `json:"target_system"`
	DirectConnect         bool                   `json:"direct_connect"`
	PaymentCycle          string                 `json:"payment_cycle"`
	Cost                  string                 `json:"cost"`
	ExpireAt              string                 `json:"expire_at"`
	VendorName            string                 `json:"vendor_name"`
	VendorURL             string                 `json:"vendor_url"`
	CreatedAt             string                 `json:"created_at"`
	UpdatedAt             string                 `json:"updated_at"`
}

// UpdateSettings carries all editable fields for a node update.
type UpdateSettings struct {
	NodeName      string
	Remark        string
	TargetSystem  string
	DirectConnect bool
	PaymentCycle  string
	Cost          string
	ExpireAt      string
	VendorName    string
	VendorURL     string
}

// Store manages probe node persistence with an in-memory read cache.
type Store struct {
	mu          sync.RWMutex
	dataDir     string
	cacheLoaded bool
	cache       []Node
}

// NewStore creates a Store rooted at dataDir.
func NewStore(dataDir string) *Store {
	return &Store{dataDir: dataDir}
}

// List returns all nodes.
func (s *Store) List() ([]Node, error) {
	return s.load()
}

// Create adds a new node with a generated secret and auto-incremented NodeNo.
func (s *Store) Create(nodeName string) (Node, error) {
	name := strings.TrimSpace(nodeName)
	if name == "" {
		return Node{}, errors.New("node name is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	nodes, err := s.loadLocked()
	if err != nil {
		return Node{}, err
	}
	for _, n := range nodes {
		if strings.EqualFold(strings.TrimSpace(n.NodeName), name) {
			return Node{}, errors.New("node name already exists")
		}
	}
	nextNo := 1
	for _, n := range nodes {
		if n.NodeNo >= nextNo {
			nextNo = n.NodeNo + 1
		}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	node := Node{
		NodeNo:        nextNo,
		NodeName:      name,
		NodeSecret:    randomSecret(32),
		TargetSystem:  "linux",
		DirectConnect: true,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	nodes = append(nodes, node)
	if err := s.writeLocked(nodes); err != nil {
		return Node{}, err
	}
	return node, nil
}

// Update modifies editable fields of an existing node by NodeNo.
func (s *Store) Update(nodeNo int, settings UpdateSettings) (Node, error) {
	if nodeNo <= 0 {
		return Node{}, errors.New("invalid node number")
	}
	name := strings.TrimSpace(settings.NodeName)
	system := strings.ToLower(strings.TrimSpace(settings.TargetSystem))
	if system != "linux" && system != "windows" {
		return Node{}, errors.New("target system must be linux or windows")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	nodes, err := s.loadLocked()
	if err != nil {
		return Node{}, err
	}

	// Resolve name if empty, check for duplicate.
	for _, n := range nodes {
		if name == "" && n.NodeNo == nodeNo {
			name = strings.TrimSpace(n.NodeName)
		}
	}
	if name == "" {
		return Node{}, errors.New("node name is required")
	}
	for _, n := range nodes {
		if n.NodeNo == nodeNo {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(n.NodeName), name) {
			return Node{}, errors.New("node name already exists")
		}
	}

	for i := range nodes {
		if nodes[i].NodeNo != nodeNo {
			continue
		}
		nodes[i].NodeName = name
		nodes[i].Remark = strings.TrimSpace(settings.Remark)
		nodes[i].TargetSystem = system
		nodes[i].DirectConnect = settings.DirectConnect
		nodes[i].PaymentCycle = strings.TrimSpace(settings.PaymentCycle)
		nodes[i].Cost = strings.TrimSpace(settings.Cost)
		nodes[i].ExpireAt = strings.TrimSpace(settings.ExpireAt)
		nodes[i].VendorName = strings.TrimSpace(settings.VendorName)
		nodes[i].VendorURL = strings.TrimSpace(settings.VendorURL)
		nodes[i].UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		if err := s.writeLocked(nodes); err != nil {
			return Node{}, err
		}
		return nodes[i], nil
	}
	return Node{}, fmt.Errorf("node %d not found", nodeNo)
}

// Replace overwrites the entire node list (bulk import).
func (s *Store) Replace(nodes []Node) ([]Node, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	normalized := normalize(nodes)
	if err := s.writeLocked(normalized); err != nil {
		return nil, err
	}
	return normalized, nil
}

// ---- internal ----

func (s *Store) load() ([]Node, error) {
	s.mu.RLock()
	if s.cacheLoaded {
		out := clone(s.cache)
		s.mu.RUnlock()
		return out, nil
	}
	s.mu.RUnlock()

	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

func (s *Store) loadLocked() ([]Node, error) {
	if s.cacheLoaded {
		return clone(s.cache), nil
	}
	path := filepath.Join(s.dataDir, storeFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			s.cache = []Node{}
			s.cacheLoaded = true
			return []Node{}, nil
		}
		return nil, err
	}
	if strings.TrimSpace(string(raw)) == "" {
		s.cache = []Node{}
		s.cacheLoaded = true
		return []Node{}, nil
	}
	var nodes []Node
	if err := json.Unmarshal(raw, &nodes); err != nil {
		return nil, fmt.Errorf("parse probe_nodes.json: %w", err)
	}
	s.cache = normalize(nodes)
	s.cacheLoaded = true
	return clone(s.cache), nil
}

func (s *Store) writeLocked(nodes []Node) error {
	normalized := normalize(nodes)
	raw, err := json.MarshalIndent(normalized, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	path := filepath.Join(s.dataDir, storeFile)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return err
	}
	s.cache = clone(normalized)
	s.cacheLoaded = true
	return nil
}

// ---- helpers ----

func normalize(nodes []Node) []Node {
	if len(nodes) == 0 {
		return []Node{}
	}
	seenNo := map[int]struct{}{}
	seenName := map[string]struct{}{}
	now := time.Now().UTC().Format(time.RFC3339)
	out := make([]Node, 0, len(nodes))
	for _, n := range nodes {
		if n.NodeNo <= 0 {
			continue
		}
		name := strings.TrimSpace(n.NodeName)
		if name == "" {
			continue
		}
		nameKey := strings.ToLower(name)
		if _, ok := seenNo[n.NodeNo]; ok {
			continue
		}
		if _, ok := seenName[nameKey]; ok {
			continue
		}
		seenNo[n.NodeNo] = struct{}{}
		seenName[nameKey] = struct{}{}

		n.NodeName = name
		n.Remark = strings.TrimSpace(n.Remark)
		n.DDNS = strings.TrimSpace(n.DDNS)
		n.CloudflareDDNSRecords = normalizeDDNSRecords(n.CloudflareDDNSRecords)
		n.NodeSecret = strings.TrimSpace(n.NodeSecret)
		if n.NodeSecret == "" {
			n.NodeSecret = randomSecret(32)
		}
		n.TargetSystem = strings.ToLower(strings.TrimSpace(n.TargetSystem))
		if n.TargetSystem != "windows" {
			n.TargetSystem = "linux"
		}
		n.PaymentCycle = strings.TrimSpace(n.PaymentCycle)
		n.Cost = strings.TrimSpace(n.Cost)
		n.ExpireAt = strings.TrimSpace(n.ExpireAt)
		n.VendorName = strings.TrimSpace(n.VendorName)
		n.VendorURL = strings.TrimSpace(n.VendorURL)
		if strings.TrimSpace(n.CreatedAt) == "" {
			n.CreatedAt = now
		}
		if strings.TrimSpace(n.UpdatedAt) == "" {
			n.UpdatedAt = n.CreatedAt
		}
		out = append(out, n)
	}
	return out
}

func normalizeDDNSRecords(in []CloudflareDDNSRecord) []CloudflareDDNSRecord {
	if len(in) == 0 {
		return []CloudflareDDNSRecord{}
	}
	out := make([]CloudflareDDNSRecord, 0, len(in))
	seen := map[string]struct{}{}
	for _, r := range in {
		rc := strings.TrimSpace(strings.ToLower(r.RecordClass))
		rn := strings.TrimSpace(strings.ToLower(r.RecordName))
		if rc == "" {
			rc = "public"
		}
		if rn == "" {
			continue
		}
		key := rc + "|" + rn
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, CloudflareDDNSRecord{RecordClass: rc, RecordName: rn})
	}
	return out
}

func clone(in []Node) []Node {
	if len(in) == 0 {
		return []Node{}
	}
	out := make([]Node, len(in))
	for i := range in {
		out[i] = in[i]
		out[i].CloudflareDDNSRecords = cloneDDNS(in[i].CloudflareDDNSRecords)
	}
	return out
}

func cloneDDNS(in []CloudflareDDNSRecord) []CloudflareDDNSRecord {
	if len(in) == 0 {
		return []CloudflareDDNSRecord{}
	}
	out := make([]CloudflareDDNSRecord, len(in))
	copy(out, in)
	return out
}

func randomSecret(length int) string {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"
	b := make([]byte, length)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("node-secret-%d", time.Now().UnixNano())
	}
	out := make([]byte, length)
	for i := range b {
		out[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(out)
}
