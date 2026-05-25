package core

import (
	"reflect"
	"testing"
)

func TestNormalizeProbeLinkChainHopConfigsForUpsertSupportsListenExternalPorts(t *testing.T) {
	input := []probeLinkChainHopConfig{
		{
			NodeNo:       2,
			ListenPort:   16040,
			ExternalPort: 26040,
			LinkLayer:    "http2",
		},
		{
			NodeNo:     3,
			ListenPort: 16050,
			LinkLayer:  "http3",
		},
	}

	items, err := normalizeProbeLinkChainHopConfigsForUpsert(input, []string{"2", "3"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 hop configs, got %d", len(items))
	}
	// external_port auto-filled to listen_port when not configured
	if items[0].NodeNo != 2 || items[0].ListenPort != 16040 || items[0].ExternalPort != 26040 || items[0].LinkLayer != "http2" {
		t.Fatalf("unexpected first hop config: %+v", items[0])
	}
	if items[1].NodeNo != 3 || items[1].ListenPort != 16050 || items[1].ExternalPort != 16050 || items[1].LinkLayer != "http3" {
		t.Fatalf("unexpected second hop config: %+v", items[1])
	}
}

func TestResolveProbeLinkChainNodeSettingsUsesListenPort(t *testing.T) {
	item := probeLinkChainRecord{
		LinkLayer: "http",
		HopConfigs: []probeLinkChainHopConfig{
			{
				NodeNo:     2,
				ListenPort: 18080,
				LinkLayer:  "http3",
			},
		},
	}

	settings := resolveProbeLinkChainNodeSettings(item, "2")
	if settings.ListenPort != 18080 {
		t.Fatalf("expected listen_port=18080, got %d", settings.ListenPort)
	}
	if settings.ExternalPort != 0 {
		t.Fatalf("expected external_port=0, got %d", settings.ExternalPort)
	}
	if settings.LinkLayer != "http3" {
		t.Fatalf("expected link_layer=http3, got %q", settings.LinkLayer)
	}
}

func TestIsProbeLinkChainNodeInRoute(t *testing.T) {
	chain := probeLinkChainRecord{
		EntryNodeID:    "1",
		CascadeNodeIDs: []string{"2", "3"},
		ExitNodeID:     "4",
	}
	for _, id := range []string{"1", "2", "3", "4"} {
		if !isProbeLinkChainNodeInRoute(chain, id) {
			t.Fatalf("expected node %s to be in route", id)
		}
	}
	if isProbeLinkChainNodeInRoute(chain, "5") {
		t.Fatalf("expected node 5 not to be in route")
	}
}

func TestNormalizeProbeLinkChainEntryAndCascades(t *testing.T) {
	tests := []struct {
		name        string
		entry       string
		exitNode    string
		cascades    []string
		wantEntry   string
		wantCascade []string
	}{
		{
			name:        "entry provided keeps order and removes duplicates",
			entry:       "9",
			exitNode:    "10",
			cascades:    []string{"9", "11", "10", "11", "12"},
			wantEntry:   "9",
			wantCascade: []string{"11", "12"},
		},
		{
			name:        "entry missing infer from first cascade",
			entry:       "",
			exitNode:    "10",
			cascades:    []string{"9", "11", "10"},
			wantEntry:   "9",
			wantCascade: []string{"11"},
		},
		{
			name:        "entry and cascades missing fallback to exit",
			entry:       "",
			exitNode:    "10",
			cascades:    []string{},
			wantEntry:   "10",
			wantCascade: []string{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotEntry, gotCascade := normalizeProbeLinkChainEntryAndCascades(tc.entry, tc.exitNode, tc.cascades)
			if gotEntry != tc.wantEntry {
				t.Fatalf("entry=%q, want %q", gotEntry, tc.wantEntry)
			}
			if !reflect.DeepEqual(gotCascade, tc.wantCascade) {
				t.Fatalf("cascades=%v, want %v", gotCascade, tc.wantCascade)
			}
		})
	}
}

func TestProjectProbeLinkEntriesForClientUsesIndependentEntryIDs(t *testing.T) {
	oldStore := ProbeLinkChainStore
	t.Cleanup(func() {
		ProbeLinkChainStore = oldStore
	})

	chain := probeLinkChainRecord{
		ChainID:       "5",
		Name:          "github",
		ChainType:     "proxy_chain",
		UserID:        "u",
		UserPublicKey: "pub",
		Secret:        "secret",
		EntryNodeID:   "1",
		ExitNodeID:    "1",
		ListenHost:    "0.0.0.0",
		ListenPort:    16030,
		LinkLayer:     "http2",
		EgressHost:    "127.0.0.1",
		EgressPort:    1080,
		HopConfigs: []probeLinkChainHopConfig{{
			NodeNo:       1,
			ListenPort:   16030,
			ExternalPort: 16030,
			RelayHost:    "origin.example.com",
			LinkLayer:    "http2",
		}},
	}
	ProbeLinkChainStore = &probeLinkChainStore{
		data: probeLinkChainStoreData{
			Chains: []probeLinkChainRecord{chain},
			EntryProfiles: []probeLinkEntryProfileRecord{{
				ChainID: "5",
				Entries: []probeLinkEntryConfig{
					{EntryType: "pub", Host: "origin.example.com"},
					{EntryType: "cf", Host: "api_copilot_nq.example.com"},
				},
			}},
		},
	}

	projected := projectProbeLinkEntriesForClient([]probeLinkChainRecord{chain})
	if len(projected) != 2 {
		t.Fatalf("expected 2 projected entries, got %d: %+v", len(projected), projected)
	}
	var cf probeLinkChainRecord
	for _, item := range projected {
		if item.ClientEntryType == "cf" {
			cf = item
			break
		}
	}
	if cf.ChainID != "5_cf" || cf.RelayChainID != "5" || cf.Name != "github_cf" {
		t.Fatalf("unexpected cf projection: %+v", cf)
	}
	if len(cf.HopConfigs) != 1 || cf.HopConfigs[0].RelayHost != "api_copilot_nq.example.com" || cf.HopConfigs[0].ExternalPort != 443 {
		t.Fatalf("unexpected cf hop projection: %+v", cf.HopConfigs)
	}
	if chain.HopConfigs[0].RelayHost != "origin.example.com" || chain.HopConfigs[0].ExternalPort != 16030 {
		t.Fatalf("original chain mutated: %+v", chain.HopConfigs[0])
	}
}

func TestProjectProbeLinkEntriesRefreshesGeneratedNameAfterChainRename(t *testing.T) {
	oldStore := ProbeLinkChainStore
	t.Cleanup(func() {
		ProbeLinkChainStore = oldStore
	})

	chain := probeLinkChainRecord{
		ChainID:       "7",
		Name:          "Home",
		ChainType:     "proxy_chain",
		UserID:        "u",
		UserPublicKey: "pub",
		Secret:        "secret",
		EntryNodeID:   "1",
		ExitNodeID:    "1",
		ListenHost:    "0.0.0.0",
		ListenPort:    16030,
		LinkLayer:     "http2",
		EgressHost:    "127.0.0.1",
		EgressPort:    1080,
		HopConfigs: []probeLinkChainHopConfig{{
			NodeNo:       1,
			ListenPort:   16030,
			ExternalPort: 16030,
			RelayHost:    "origin.example.com",
			LinkLayer:    "http2",
		}},
	}
	ProbeLinkChainStore = &probeLinkChainStore{
		data: probeLinkChainStoreData{
			Chains: []probeLinkChainRecord{chain},
			EntryProfiles: []probeLinkEntryProfileRecord{{
				ChainID: "7",
				Entries: []probeLinkEntryConfig{
					{EntryID: "7_cf", EntryType: "cf", Host: "api.example.com", Name: "HomeChain_cf"},
					{EntryID: "custom-entry", EntryType: "pub", Host: "origin.example.com", Name: "My Entry"},
				},
			}},
		},
	}

	projected := projectProbeLinkEntriesForClient([]probeLinkChainRecord{chain})
	names := map[string]string{}
	for _, item := range projected {
		names[item.ClientEntryType] = item.Name
	}
	if names["cf"] != "Home_cf" {
		t.Fatalf("generated cf name=%q, want Home_cf; projected=%+v", names["cf"], projected)
	}
	if names["pub"] != "My Entry" {
		t.Fatalf("custom pub name=%q, want My Entry; projected=%+v", names["pub"], projected)
	}
}
