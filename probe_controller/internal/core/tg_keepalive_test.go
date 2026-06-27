package core

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestKeepaliveTGAssistantAccountsOnceRefreshesAuthorizedOnly(t *testing.T) {
	chdirTemp(t)

	origStore := TGAssistantStore
	origRefresh := tgAssistantKeepaliveRefreshFunc
	defer func() {
		TGAssistantStore = origStore
		tgAssistantKeepaliveRefreshFunc = origRefresh
	}()

	storePath := filepath.Join(t.TempDir(), "tg.json")
	TGAssistantStore = &tgAssistantStore{
		path: storePath,
		data: tgAssistantStoreData{
			APIID:   12345,
			APIHash: "hash",
			Accounts: []tgAssistantAccountRecord{
				{
					ID:         "authorized",
					Phone:      "+10000000001",
					Authorized: true,
					UpdatedAt:  "old",
				},
				{
					ID:         "unauthorized",
					Phone:      "+10000000002",
					Authorized: false,
					UpdatedAt:  "old",
				},
			},
		},
	}

	refreshed := []string{}
	tgAssistantKeepaliveRefreshFunc = func(record *tgAssistantAccountRecord, apiID int, apiHash string) {
		if apiID != 12345 || apiHash != "hash" {
			t.Fatalf("unexpected api config: %d %q", apiID, apiHash)
		}
		refreshed = append(refreshed, record.ID)
		record.UpdatedAt = time.Date(2026, 6, 27, 4, 0, 0, 0, time.UTC).Format(time.RFC3339)
		record.LastError = ""
		record.Authorized = true
	}

	keepaliveTGAssistantAccountsOnce(context.Background())

	if len(refreshed) != 1 || refreshed[0] != "authorized" {
		t.Fatalf("refreshed accounts = %#v, want only authorized", refreshed)
	}

	content, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatalf("read saved tg store: %v", err)
	}
	var saved tgAssistantStoreData
	if err := json.Unmarshal(content, &saved); err != nil {
		t.Fatalf("unmarshal saved tg store: %v", err)
	}
	if saved.Accounts[0].UpdatedAt == "old" {
		t.Fatalf("authorized account was not saved: %+v", saved.Accounts[0])
	}
	if saved.Accounts[1].UpdatedAt != "old" {
		t.Fatalf("unauthorized account should be untouched: %+v", saved.Accounts[1])
	}
}

func TestKeepaliveTGAssistantAccountsOnceSkipsWithoutAPIKey(t *testing.T) {
	chdirTemp(t)

	origStore := TGAssistantStore
	origRefresh := tgAssistantKeepaliveRefreshFunc
	defer func() {
		TGAssistantStore = origStore
		tgAssistantKeepaliveRefreshFunc = origRefresh
	}()

	TGAssistantStore = &tgAssistantStore{
		path: filepath.Join(t.TempDir(), "tg.json"),
		data: tgAssistantStoreData{
			Accounts: []tgAssistantAccountRecord{
				{ID: "authorized", Phone: "+10000000001", Authorized: true},
			},
		},
	}

	called := false
	tgAssistantKeepaliveRefreshFunc = func(record *tgAssistantAccountRecord, apiID int, apiHash string) {
		called = true
	}

	keepaliveTGAssistantAccountsOnce(context.Background())

	if called {
		t.Fatal("keepalive should skip refresh when API key is not configured")
	}
}
