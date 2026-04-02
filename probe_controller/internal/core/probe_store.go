package core

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

type probeConfigStore struct {
	mu   sync.RWMutex
	path string
	data probeConfigData
}

type probeConfigData struct {
	ProbeNodes          []probeNodeRecord          `json:"probe_nodes"`
	ProbeSecrets        map[string]string          `json:"probe_secrets"`
	ProbeShellShortcuts []probeShellShortcutRecord `json:"probe_shell_shortcuts"`
	DeletedProbeNodeNos []int                      `json:"deleted_probe_node_nos,omitempty"`
}

var ProbeStore *probeConfigStore

func initProbeStore() {
	storePath := filepath.Join(dataDir, probeConfigStoreFile)
	ProbeStore = &probeConfigStore{
		path: storePath,
		data: probeConfigData{
			ProbeNodes:          []probeNodeRecord{},
			ProbeSecrets:        map[string]string{},
			ProbeShellShortcuts: []probeShellShortcutRecord{},
			DeletedProbeNodeNos: []int{},
		},
	}

	if _, err := os.Stat(storePath); err == nil {
		content, readErr := os.ReadFile(storePath)
		if readErr != nil {
			log.Fatalf("failed to read probe config file: %v", readErr)
		}
		if len(strings.TrimSpace(string(content))) > 0 {
			var raw probeConfigData
			if unmarshalErr := json.Unmarshal(content, &raw); unmarshalErr != nil {
				log.Fatalf("failed to parse probe config file: %v", unmarshalErr)
			}
			nodes, secrets, shortcuts, deletedNos := normalizeProbeConfig(raw.ProbeNodes, raw.ProbeSecrets, raw.ProbeShellShortcuts, raw.DeletedProbeNodeNos)
			ProbeStore.data.ProbeNodes = nodes
			ProbeStore.data.ProbeSecrets = secrets
			ProbeStore.data.ProbeShellShortcuts = shortcuts
			ProbeStore.data.DeletedProbeNodeNos = deletedNos
		}
	} else if os.IsNotExist(err) {
		nodes, secrets, shortcuts, deletedNos := normalizeProbeConfig(loadLegacyProbeNodesFromMainStore(), loadLegacyProbeSecretsFromMainStore(), nil, nil)
		ProbeStore.data.ProbeNodes = nodes
		ProbeStore.data.ProbeSecrets = secrets
		ProbeStore.data.ProbeShellShortcuts = shortcuts
		ProbeStore.data.DeletedProbeNodeNos = deletedNos
		if saveErr := ProbeStore.Save(); saveErr != nil {
			log.Fatalf("failed to initialize probe config file: %v", saveErr)
		}
	} else {
		log.Fatalf("failed to check probe config file: %v", err)
	}

	cleanupLegacyProbeDataFromMainStore()
	log.Println("Probe datastore initialized at", storePath)
}

func (s *probeConfigStore) Save() error {
	s.mu.RLock()
	content, err := json.MarshalIndent(s.data, "", "  ")
	s.mu.RUnlock()
	if err != nil {
		return err
	}
	if err := os.WriteFile(s.path, content, 0o644); err != nil {
		return err
	}
	triggerAutoBackupControllerDataAsync("probe_store_save")
	return nil
}

func normalizeProbeConfig(nodes []probeNodeRecord, secrets map[string]string, shortcuts []probeShellShortcutRecord, deletedNos []int) ([]probeNodeRecord, map[string]string, []probeShellShortcutRecord, []int) {
	normalizedNodes, secretsFromNodes := normalizeProbeNodes(nodes)
	normalizedSecrets := make(map[string]string)
	for nodeID, secret := range secretsFromNodes {
		normalizedSecrets[nodeID] = secret
	}
	for key, value := range secrets {
		nodeID := normalizeProbeNodeID(key)
		trimmed := strings.TrimSpace(value)
		if nodeID != "" && trimmed != "" {
			normalizedSecrets[nodeID] = trimmed
		}
	}
	normalizedShortcuts := normalizeProbeShellShortcuts(shortcuts)
	normalizedDeletedNos := normalizeDeletedProbeNodeNos(deletedNos)
	return normalizedNodes, normalizedSecrets, normalizedShortcuts, normalizedDeletedNos
}

func normalizeDeletedProbeNodeNos(items []int) []int {
	if len(items) == 0 {
		return []int{}
	}
	seen := make(map[int]struct{}, len(items))
	out := make([]int, 0, len(items))
	for _, no := range items {
		if no <= 0 {
			continue
		}
		if _, ok := seen[no]; ok {
			continue
		}
		seen[no] = struct{}{}
		out = append(out, no)
	}
	sort.Ints(out)
	return out
}

func loadLegacyProbeNodesFromMainStore() []probeNodeRecord {
	if Store == nil {
		return []probeNodeRecord{}
	}
	Store.mu.RLock()
	defer Store.mu.RUnlock()
	rawAny, ok := Store.Data[probeNodesStoreField]
	if !ok {
		return []probeNodeRecord{}
	}
	rawJSON, err := json.Marshal(rawAny)
	if err != nil {
		return []probeNodeRecord{}
	}
	result := make([]probeNodeRecord, 0)
	if err := json.Unmarshal(rawJSON, &result); err != nil {
		return []probeNodeRecord{}
	}
	return result
}

func loadLegacyProbeSecretsFromMainStore() map[string]string {
	if Store == nil {
		return map[string]string{}
	}
	Store.mu.RLock()
	defer Store.mu.RUnlock()
	rawAny, ok := Store.Data[probeSecretsStoreField]
	if !ok {
		return map[string]string{}
	}
	result := make(map[string]string)
	switch raw := rawAny.(type) {
	case map[string]string:
		for key, value := range raw {
			nodeID := normalizeProbeNodeID(key)
			trimmed := strings.TrimSpace(value)
			if nodeID != "" && trimmed != "" {
				result[nodeID] = trimmed
			}
		}
	case map[string]interface{}:
		for key, value := range raw {
			text, ok := value.(string)
			if !ok {
				continue
			}
			nodeID := normalizeProbeNodeID(key)
			trimmed := strings.TrimSpace(text)
			if nodeID != "" && trimmed != "" {
				result[nodeID] = trimmed
			}
		}
	}
	return result
}

func cleanupLegacyProbeDataFromMainStore() {
	if Store == nil {
		return
	}
	Store.mu.Lock()
	changed := false
	if _, ok := Store.Data[probeNodesStoreField]; ok {
		delete(Store.Data, probeNodesStoreField)
		changed = true
	}
	if _, ok := Store.Data[probeSecretsStoreField]; ok {
		delete(Store.Data, probeSecretsStoreField)
		changed = true
	}
	Store.mu.Unlock()

	if changed {
		if err := Store.Save(); err != nil {
			log.Printf("warning: failed to cleanup legacy probe config in main store: %v", err)
		}
	}
}
