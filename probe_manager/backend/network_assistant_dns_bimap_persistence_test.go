package backend

import (
	"encoding/gob"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func resetDNSBiMapCacheTestState(path string, loaded bool) {
	dnsBiMapCache.mu.Lock()
	dnsBiMapCache.loaded = loaded
	dnsBiMapCache.path = path
	dnsBiMapCache.entries = make(map[string]dnsBiMapEntry)
	dnsBiMapCache.domainIPs = make(map[string]map[string]struct{})
	dnsBiMapCache.ipDomains = make(map[string]map[string]struct{})
	dnsBiMapCache.failures = make(map[string]dnsBiMapFailureState)
	dnsBiMapCache.mu.Unlock()
}

func cleanupDNSBiMapCacheTestState(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		resetDNSBiMapCacheTestState("", true)
	})
}

func writeDNSBiMapPayloadFile(t *testing.T, path string, payload dnsBiMapPersistPayload) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir parent dir failed: %v", err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create payload file failed: %v", err)
	}
	encErr := gob.NewEncoder(f).Encode(payload)
	closeErr := f.Close()
	if encErr != nil {
		t.Fatalf("encode payload failed: %v", encErr)
	}
	if closeErr != nil {
		t.Fatalf("close payload file failed: %v", closeErr)
	}
}

func readDNSBiMapPayloadFile(t *testing.T, path string) dnsBiMapPersistPayload {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open payload file failed: %v", err)
	}
	defer f.Close()
	var payload dnsBiMapPersistPayload
	if err := gob.NewDecoder(f).Decode(&payload); err != nil {
		t.Fatalf("decode payload failed: %v", err)
	}
	return payload
}

func TestLoadDNSBiMapFromDiskLockedFallsBackToBackup(t *testing.T) {
	cleanupDNSBiMapCacheTestState(t)
	tempDir := t.TempDir()
	mainPath := filepath.Join(tempDir, dnsBidirectionalCacheFileName)
	backupPath := dnsBiMapBackupPath(mainPath)

	if err := os.WriteFile(mainPath, []byte("corrupted-main"), 0o644); err != nil {
		t.Fatalf("write corrupted main file failed: %v", err)
	}
	now := time.Now()
	writeDNSBiMapPayloadFile(t, backupPath, dnsBiMapPersistPayload{
		Version: 1,
		Entries: []dnsBiMapEntry{{
			Domain:    "backup.example.com",
			IP:        "198.51.100.10",
			Group:     "group_example",
			NodeID:    "chain:edge-a",
			ExpiresAt: now.Add(time.Hour),
			UpdatedAt: now,
		}},
	})

	dnsBiMapCache.mu.Lock()
	loadedFromBackup, requiresRewrite, err := loadDNSBiMapFromDiskLocked(mainPath)
	entry, ok := dnsBiMapCache.entries[dnsBiMapKey("backup.example.com", "198.51.100.10")]
	dnsBiMapCache.mu.Unlock()

	if err != nil {
		t.Fatalf("load from disk failed: %v", err)
	}
	if !loadedFromBackup {
		t.Fatal("expected load to fallback to backup")
	}
	if !requiresRewrite {
		t.Fatal("expected rewrite required after backup fallback")
	}
	if !ok {
		t.Fatal("expected backup entry loaded into memory")
	}
	if entry.Group != "group_example" || entry.NodeID != "chain:edge-a" {
		t.Fatalf("unexpected loaded entry: %#v", entry)
	}
}

func TestEnsureDNSBiMapCacheLoadedRewritesMainAfterBackupRecovery(t *testing.T) {
	cleanupDNSBiMapCacheTestState(t)
	tempDir := t.TempDir()
	mainPath := filepath.Join(tempDir, dnsBidirectionalCacheFileName)
	backupPath := dnsBiMapBackupPath(mainPath)

	if err := os.WriteFile(mainPath, []byte("corrupted-main"), 0o644); err != nil {
		t.Fatalf("write corrupted main file failed: %v", err)
	}
	now := time.Now()
	writeDNSBiMapPayloadFile(t, backupPath, dnsBiMapPersistPayload{
		Version: 1,
		Entries: []dnsBiMapEntry{{
			Domain:    "restore.example.com",
			IP:        "198.51.100.11",
			Group:     "group_restore",
			NodeID:    "chain:restore",
			ExpiresAt: now.Add(2 * time.Hour),
			UpdatedAt: now,
		}},
	})

	resetDNSBiMapCacheTestState(mainPath, false)
	if err := ensureDNSBiMapCacheLoaded(); err != nil {
		t.Fatalf("ensureDNSBiMapCacheLoaded failed: %v", err)
	}

	dnsBiMapCache.mu.Lock()
	_, inMemoryOK := dnsBiMapCache.entries[dnsBiMapKey("restore.example.com", "198.51.100.11")]
	loaded := dnsBiMapCache.loaded
	dnsBiMapCache.mu.Unlock()
	if !loaded {
		t.Fatal("cache should be marked loaded")
	}
	if !inMemoryOK {
		t.Fatal("expected recovered entry in memory")
	}

	persisted := readDNSBiMapPayloadFile(t, mainPath)
	if len(persisted.Entries) != 1 {
		t.Fatalf("persisted entries len=%d, want 1", len(persisted.Entries))
	}
	if persisted.Entries[0].Domain != "restore.example.com" || persisted.Entries[0].IP != "198.51.100.11" {
		t.Fatalf("unexpected persisted entry: %#v", persisted.Entries[0])
	}
	if _, err := os.Stat(backupPath); !os.IsNotExist(err) {
		t.Fatalf("backup file should be removed after successful rewrite, stat err=%v", err)
	}
}

func TestEnsureDNSBiMapCacheLoadedPrunesExpiredAndPersists(t *testing.T) {
	cleanupDNSBiMapCacheTestState(t)
	tempDir := t.TempDir()
	mainPath := filepath.Join(tempDir, dnsBidirectionalCacheFileName)
	now := time.Now()

	writeDNSBiMapPayloadFile(t, mainPath, dnsBiMapPersistPayload{
		Version: 1,
		Entries: []dnsBiMapEntry{
			{
				Domain:    "expired.example.com",
				IP:        "198.51.100.12",
				Group:     "group_expired",
				NodeID:    "chain:expired",
				ExpiresAt: now.Add(-time.Minute),
				UpdatedAt: now,
			},
			{
				Domain:    "alive.example.com",
				IP:        "198.51.100.13",
				Group:     "group_alive",
				NodeID:    "chain:alive",
				ExpiresAt: now.Add(time.Hour),
				UpdatedAt: now,
			},
		},
	})

	resetDNSBiMapCacheTestState(mainPath, false)
	if err := ensureDNSBiMapCacheLoaded(); err != nil {
		t.Fatalf("ensureDNSBiMapCacheLoaded failed: %v", err)
	}

	dnsBiMapCache.mu.Lock()
	_, expiredExists := dnsBiMapCache.entries[dnsBiMapKey("expired.example.com", "198.51.100.12")]
	_, aliveExists := dnsBiMapCache.entries[dnsBiMapKey("alive.example.com", "198.51.100.13")]
	dnsBiMapCache.mu.Unlock()
	if expiredExists {
		t.Fatal("expired entry should be pruned in memory")
	}
	if !aliveExists {
		t.Fatal("alive entry should remain in memory")
	}

	persisted := readDNSBiMapPayloadFile(t, mainPath)
	if len(persisted.Entries) != 1 {
		t.Fatalf("persisted entries len=%d, want 1", len(persisted.Entries))
	}
	if persisted.Entries[0].Domain != "alive.example.com" || persisted.Entries[0].IP != "198.51.100.13" {
		t.Fatalf("unexpected persisted entry after prune: %#v", persisted.Entries[0])
	}
}

func TestEnsureDNSBiMapCacheLoadedKeepsMemoryOnDecodeErrorWithoutBackup(t *testing.T) {
	cleanupDNSBiMapCacheTestState(t)
	tempDir := t.TempDir()
	mainPath := filepath.Join(tempDir, dnsBidirectionalCacheFileName)
	if err := os.WriteFile(mainPath, []byte("corrupted-main-only"), 0o644); err != nil {
		t.Fatalf("write corrupted main file failed: %v", err)
	}

	resetDNSBiMapCacheTestState(mainPath, false)
	dnsBiMapCache.mu.Lock()
	now := time.Now()
	entry := dnsBiMapEntry{
		Domain:    "keep.example.com",
		IP:        "198.51.100.14",
		Group:     "group_keep",
		NodeID:    "chain:keep",
		ExpiresAt: now.Add(time.Hour),
		UpdatedAt: now,
	}
	key := dnsBiMapKey(entry.Domain, entry.IP)
	dnsBiMapCache.entries[key] = entry
	addDNSBiMapIndexLocked(entry.Domain, entry.IP)
	dnsBiMapCache.mu.Unlock()

	err := ensureDNSBiMapCacheLoaded()
	if err == nil {
		t.Fatal("expected decode error for corrupted main without backup")
	}

	dnsBiMapCache.mu.Lock()
	_, stillExists := dnsBiMapCache.entries[key]
	loaded := dnsBiMapCache.loaded
	dnsBiMapCache.mu.Unlock()
	if !stillExists {
		t.Fatal("existing in-memory entries should not be cleared on load error")
	}
	if !loaded {
		t.Fatal("cache should still be marked loaded to avoid retry loops")
	}
}
