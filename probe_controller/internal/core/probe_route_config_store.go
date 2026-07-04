package core

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type probeRouteConfigStore struct {
	mu   sync.RWMutex
	path string
	data probeRouteConfigStoreData
}

type probeRouteConfigStoreData struct {
	VirtualRouter probeVirtualRouterConfig `json:"virtual_router,omitempty"`
}

var ProbeRouteConfigStore *probeRouteConfigStore

func initProbeRouteConfigStore() {
	storePath := filepath.Join(dataDir, probeRouteConfigStoreFile)
	ProbeRouteConfigStore = &probeRouteConfigStore{
		path: storePath,
		data: probeRouteConfigStoreData{
			VirtualRouter: defaultProbeVirtualRouterConfig(),
		},
	}

	if _, err := os.Stat(storePath); err == nil {
		content, readErr := os.ReadFile(storePath)
		if readErr != nil {
			log.Fatalf("failed to read probe route config store: %v", readErr)
		}
		if len(strings.TrimSpace(string(content))) > 0 {
			var raw probeRouteConfigStoreData
			if unmarshalErr := json.Unmarshal(content, &raw); unmarshalErr != nil {
				log.Fatalf("failed to parse probe route config store: %v", unmarshalErr)
			}
			ProbeRouteConfigStore.data.VirtualRouter = normalizeProbeVirtualRouterConfig(raw.VirtualRouter)
		}
	} else if os.IsNotExist(err) {
		if saveErr := ProbeRouteConfigStore.Save(); saveErr != nil {
			log.Fatalf("failed to initialize probe route config store: %v", saveErr)
		}
	} else {
		log.Fatalf("failed to check probe route config store: %v", err)
	}

	log.Println("Probe route config datastore initialized at", storePath)
}

func (s *probeRouteConfigStore) Save() error {
	s.mu.RLock()
	content, err := json.MarshalIndent(s.data, "", "  ")
	s.mu.RUnlock()
	if err != nil {
		return err
	}
	if err := os.WriteFile(s.path, content, 0o644); err != nil {
		return err
	}
	triggerAutoBackupControllerDataAsync("probe_route_config_store_save")
	return nil
}
