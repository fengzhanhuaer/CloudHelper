package core

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type probeLinkChainStore struct {
	mu   sync.RWMutex
	path string
	data probeLinkChainStoreData
}

type probeLinkChainStoreData struct {
	Chains []probeLinkChainRecord `json:"chains"`
}

var ProbeLinkChainStore *probeLinkChainStore

func initProbeLinkChainStore() {
	storePath := filepath.Join(dataDir, probeLinkChainStoreFile)
	ProbeLinkChainStore = &probeLinkChainStore{
		path: storePath,
		data: probeLinkChainStoreData{
			Chains: []probeLinkChainRecord{},
		},
	}

	if _, err := os.Stat(storePath); err == nil {
		content, readErr := os.ReadFile(storePath)
		if readErr != nil {
			log.Fatalf("failed to read probe link chain store: %v", readErr)
		}
		if len(strings.TrimSpace(string(content))) > 0 {
			var raw probeLinkChainStoreData
			if unmarshalErr := json.Unmarshal(content, &raw); unmarshalErr != nil {
				log.Fatalf("failed to parse probe link chain store: %v", unmarshalErr)
			}
			ProbeLinkChainStore.data.Chains = normalizeProbeLinkChains(raw.Chains)
		}
	} else if os.IsNotExist(err) {
		if saveErr := ProbeLinkChainStore.Save(); saveErr != nil {
			log.Fatalf("failed to initialize probe link chain store: %v", saveErr)
		}
	} else {
		log.Fatalf("failed to check probe link chain store: %v", err)
	}

	log.Println("Probe link chain datastore initialized at", storePath)
}

func (s *probeLinkChainStore) Save() error {
	s.mu.RLock()
	content, err := json.MarshalIndent(s.data, "", "  ")
	s.mu.RUnlock()
	if err != nil {
		return err
	}
	if err := os.WriteFile(s.path, content, 0o644); err != nil {
		return err
	}
	triggerAutoBackupControllerDataAsync("probe_link_chain_store_save")
	return nil
}

