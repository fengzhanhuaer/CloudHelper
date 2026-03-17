package core

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
)

// DataStore represents our JSON storage.
type DataStore struct {
	mu   sync.RWMutex
	path string
	Data map[string]interface{} `json:"data"`
}

var Store *DataStore

func initStore() {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		log.Fatalf("failed to create data directory: %v", err)
	}

	dbPath := filepath.Join(dataDir, mainStoreFile)
	Store = &DataStore{
		path: dbPath,
		Data: make(map[string]interface{}),
	}

	if _, err := os.Stat(dbPath); err == nil {
		content, readErr := os.ReadFile(dbPath)
		if readErr != nil {
			log.Fatalf("failed to read JSON data file: %v", readErr)
		}
		if len(content) > 0 {
			if unmarshalErr := json.Unmarshal(content, &Store.Data); unmarshalErr != nil {
				log.Fatalf("failed to parse JSON data file: %v", unmarshalErr)
			}
		}
	} else if os.IsNotExist(err) {
		if saveErr := Store.Save(); saveErr != nil {
			log.Fatalf("failed to initialize JSON data file: %v", saveErr)
		}
	} else {
		log.Fatalf("failed to check JSON data file: %v", err)
	}

	log.Println("JSON datastore initialized at", dbPath)
	initProbeStore()
	initTGAssistantStore()
}

func (s *DataStore) Save() error {
	s.mu.RLock()
	content, err := json.MarshalIndent(s.Data, "", "  ")
	s.mu.RUnlock()
	if err != nil {
		return err
	}
	if err := os.WriteFile(s.path, content, 0o644); err != nil {
		return err
	}
	triggerAutoBackupControllerDataAsync("main_store_save")
	return nil
}
