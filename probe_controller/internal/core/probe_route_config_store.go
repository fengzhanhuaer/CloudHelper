package core

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type probeRouteConfigStore struct {
	mu        sync.RWMutex
	persistMu sync.Mutex
	path      string
	data      probeRouteConfigStoreData
}

type probeRouteConfigStoreData struct {
	VirtualRouter       probeVirtualRouterConfig        `json:"virtual_router,omitempty"`
	VirtualRouterFakeIP probeVirtualRouterFakeIPLibrary `json:"virtual_router_fake_ip,omitempty"`
	SpecialExits        []probeSpecialExitConfig        `json:"special_exits,omitempty"`
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
			SpecialExits:        []probeSpecialExitConfig{},
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
			ProbeRouteConfigStore.data.SpecialExits = normalizeProbeSpecialExitConfigs(raw.SpecialExits)
			ProbeRouteConfigStore.data.DoH = normalizeProbeControllerDoHConfig(raw.DoH)
		}
	} else if os.IsNotExist(err) {
		if saveErr := ProbeRouteConfigStore.Save(); saveErr != nil {
			log.Fatalf("failed to initialize probe route config store: %v", saveErr)
		}
	} else {
		log.Fatalf("failed to check probe route config store: %v", err)
	}

	reconciled, changed := reconcileProbeSpecialExitConfigsWithRouteRules(
		ProbeRouteConfigStore.data.SpecialExits,
		ProbeRouteConfigStore.data.VirtualRouter.RouteRules,
		time.Now().UTC(),
	)
	ProbeRouteConfigStore.data.SpecialExits = reconciled
	if changed {
		if saveErr := ProbeRouteConfigStore.SaveWithoutAutoBackup(); saveErr != nil {
			log.Fatalf("failed to reconcile probe special exit rules: %v", saveErr)
		}
	}

	log.Println("Probe route config datastore initialized at", storePath)
}

func (s *probeRouteConfigStore) Save() error {
	return s.save(true)
}

func (s *probeRouteConfigStore) SaveWithoutAutoBackup() error {
	return s.save(false)
}

func (s *probeRouteConfigStore) update(mutator func(*probeRouteConfigStoreData) error) error {
	s.persistMu.Lock()
	s.mu.Lock()
	current, err := json.Marshal(s.data)
	if err != nil {
		s.mu.Unlock()
		s.persistMu.Unlock()
		return err
	}
	var working probeRouteConfigStoreData
	if err = json.Unmarshal(current, &working); err != nil {
		s.mu.Unlock()
		s.persistMu.Unlock()
		return err
	}
	if err = mutator(&working); err != nil {
		s.mu.Unlock()
		s.persistMu.Unlock()
		return err
	}
	content, err := json.MarshalIndent(working, "", "  ")
	if err == nil {
		err = os.WriteFile(s.path, content, 0o600)
	}
	if err == nil {
		err = os.Chmod(s.path, 0o600)
	}
	if err != nil {
		s.mu.Unlock()
		s.persistMu.Unlock()
		return err
	}
	s.data = working
	s.mu.Unlock()
	s.persistMu.Unlock()
	triggerAutoBackupControllerDataAsync("probe_route_config_store_save")
	return nil
}

func (s *probeRouteConfigStore) save(triggerBackup bool) error {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
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
