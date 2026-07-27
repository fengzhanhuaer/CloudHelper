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
	VirtualRouter       probeVirtualRouterConfig        `json:"virtual_router,omitempty"`
	VirtualRouterFakeIP probeVirtualRouterFakeIPLibrary `json:"virtual_router_fake_ip,omitempty"`
	DoH                 probeControllerDoHConfig        `json:"doh,omitempty"`
}

var ProbeRouteConfigStore *probeRouteConfigStore

func initProbeRouteConfigStore() {
	storePath := filepath.Join(dataDir, probeRouteConfigStoreFile)
	ProbeRouteConfigStore = &probeRouteConfigStore{
		path: storePath,
		data: probeRouteConfigStoreData{
			VirtualRouter:       defaultProbeVirtualRouterConfig(),
			VirtualRouterFakeIP: defaultProbeVirtualRouterFakeIPLibrary(),
			DoH:                 defaultProbeControllerDoHConfig(),
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
			ProbeRouteConfigStore.data.VirtualRouterFakeIP = normalizeProbeVirtualRouterFakeIPLibrary(raw.VirtualRouterFakeIP)
			ProbeRouteConfigStore.data.DoH = normalizeProbeControllerDoHConfig(raw.DoH)
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
	return s.save(true)
}

func (s *probeRouteConfigStore) SaveWithoutAutoBackup() error {
	return s.save(false)
}

func (s *probeRouteConfigStore) save(triggerBackup bool) error {
	s.mu.RLock()
	content, err := json.MarshalIndent(s.data, "", "  ")
	s.mu.RUnlock()
	if err != nil {
		return err
	}
	if err := os.WriteFile(s.path, content, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(s.path, 0o600); err != nil {
		return err
	}
	if triggerBackup {
		triggerAutoBackupControllerDataAsync("probe_route_config_store_save")
	}
	return nil
}
