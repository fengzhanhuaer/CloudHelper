package core

import "testing"

func TestNormalizeProbeLinkChainHopConfigsForUpsertSupportsServiceExternalPorts(t *testing.T) {
	input := []probeLinkChainHopConfig{
		{
			NodeNo:       2,
			ServicePort:  16040,
			ExternalPort: 26040,
			LinkLayer:    "http2",
		},
		{
			NodeNo:      3,
			ServicePort: 16050,
			LinkLayer:   "http3",
		},
	}

	items, err := normalizeProbeLinkChainHopConfigsForUpsert(input, []string{"2", "3"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 hop configs, got %d", len(items))
	}
	if items[0].NodeNo != 2 || items[0].ServicePort != 16040 || items[0].ExternalPort != 26040 || items[0].LinkLayer != "http2" {
		t.Fatalf("unexpected first hop config: %+v", items[0])
	}
	if items[1].NodeNo != 3 || items[1].ServicePort != 16050 || items[1].ExternalPort != 0 || items[1].LinkLayer != "http3" {
		t.Fatalf("unexpected second hop config: %+v", items[1])
	}
}

func TestResolveProbeLinkChainNodeSettingsKeepsLegacyListenPortForCompatibility(t *testing.T) {
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
	if settings.ServicePort != 0 {
		t.Fatalf("expected legacy listen_port not to overwrite service_port, got %d", settings.ServicePort)
	}
	if settings.ExternalPort != 0 {
		t.Fatalf("expected external_port=0, got %d", settings.ExternalPort)
	}
	if settings.LegacyNextPort != 18080 {
		t.Fatalf("expected legacy_next_port=18080, got %d", settings.LegacyNextPort)
	}
	if settings.LinkLayer != "http3" {
		t.Fatalf("expected link_layer=http3, got %q", settings.LinkLayer)
	}
}
