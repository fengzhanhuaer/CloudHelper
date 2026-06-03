package core

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeTGAssistantNotifySettings(t *testing.T) {
	// Zero value should produce safe defaults with toggles off.
	got := normalizeTGAssistantNotifySettings(tgAssistantNotifySettings{})
	if got.EnableOffline || got.EnableRenewal {
		t.Fatalf("expected toggles off by default, got %+v", got)
	}
	if got.OfflineDebounceSec != tgAssistantNotifyDefaultDebounceSec {
		t.Fatalf("expected default debounce %d, got %d", tgAssistantNotifyDefaultDebounceSec, got.OfflineDebounceSec)
	}
	if got.RenewalCheckHour != tgAssistantNotifyDefaultCheckHour {
		t.Fatalf("expected default hour %d, got %d", tgAssistantNotifyDefaultCheckHour, got.RenewalCheckHour)
	}
	if len(got.RenewalThresholds) != 3 || got.RenewalThresholds[0] != 7 || got.RenewalThresholds[2] != 1 {
		t.Fatalf("expected default thresholds [7 3 1], got %v", got.RenewalThresholds)
	}

	// Clamps and dedupe/sort.
	got = normalizeTGAssistantNotifySettings(tgAssistantNotifySettings{
		OfflineDebounceSec: 99999,
		RenewalCheckHour:   48,
		RenewalThresholds:  []int{3, 3, 7, 0, -2, 1},
	})
	if got.OfflineDebounceSec != 3600 {
		t.Fatalf("expected debounce clamped to 3600, got %d", got.OfflineDebounceSec)
	}
	if got.RenewalCheckHour != tgAssistantNotifyDefaultCheckHour {
		t.Fatalf("expected hour reset to default, got %d", got.RenewalCheckHour)
	}
	if len(got.RenewalThresholds) != 3 || got.RenewalThresholds[0] != 7 || got.RenewalThresholds[2] != 1 {
		t.Fatalf("expected deduped sorted [7 3 1], got %v", got.RenewalThresholds)
	}
}

func TestResolveNotifyBot(t *testing.T) {
	orig := TGAssistantStore
	defer func() { TGAssistantStore = orig }()

	newStore := func(acc tgAssistantAccountRecord, accountID string) {
		TGAssistantStore = &tgAssistantStore{
			path: filepath.Join(t.TempDir(), "tg.json"),
			data: tgAssistantStoreData{
				Accounts: []tgAssistantAccountRecord{acc},
				Notify:   tgAssistantNotifySettings{NotifyAccountID: accountID},
			},
		}
	}

	// Ready account.
	newStore(tgAssistantAccountRecord{ID: "a1", Phone: "+100", Authorized: true, SelfUserID: 555, BotAPIKey: "11:abc"}, "a1")
	key, chat, acc, ok := resolveNotifyBot()
	if !ok || key != "11:abc" || chat != 555 || acc != "a1" {
		t.Fatalf("expected ready bot, got key=%q chat=%d acc=%q ok=%v", key, chat, acc, ok)
	}

	// No account selected.
	newStore(tgAssistantAccountRecord{ID: "a1", Phone: "+100", Authorized: true, SelfUserID: 555, BotAPIKey: "11:abc"}, "")
	if _, _, _, ok := resolveNotifyBot(); ok {
		t.Fatal("expected not ready when no account selected")
	}

	// Unauthorized account.
	newStore(tgAssistantAccountRecord{ID: "a1", Phone: "+100", Authorized: false, SelfUserID: 555, BotAPIKey: "11:abc"}, "a1")
	if _, _, _, ok := resolveNotifyBot(); ok {
		t.Fatal("expected not ready when account unauthorized")
	}

	// Missing bot key.
	newStore(tgAssistantAccountRecord{ID: "a1", Phone: "+100", Authorized: true, SelfUserID: 555, BotAPIKey: ""}, "a1")
	if _, _, _, ok := resolveNotifyBot(); ok {
		t.Fatal("expected not ready when bot key missing")
	}
}

func TestSendProbeStatusNotification(t *testing.T) {
	origStore := TGAssistantStore
	origSend := tgNotifySendFunc
	defer func() { TGAssistantStore = origStore; tgNotifySendFunc = origSend }()
	chdirTemp(t)

	TGAssistantStore = &tgAssistantStore{
		path: filepath.Join(t.TempDir(), "tg.json"),
		data: tgAssistantStoreData{
			Accounts: []tgAssistantAccountRecord{{ID: "a1", Phone: "+100", Authorized: true, SelfUserID: 777, BotAPIKey: "11:abc"}},
			Notify:   tgAssistantNotifySettings{NotifyAccountID: "a1", EnableOffline: true},
		},
	}

	var gotChat int64
	var gotText string
	tgNotifySendFunc = func(ctx context.Context, botAPIKey string, chatID int64, text string) (int, string, error) {
		gotChat = chatID
		gotText = text
		return 1, text, nil
	}

	sendProbeStatusNotification("1", false)
	if gotChat != 777 {
		t.Fatalf("expected chat 777, got %d", gotChat)
	}
	if !strings.Contains(gotText, "离线") {
		t.Fatalf("expected offline text, got %q", gotText)
	}

	gotText = ""
	sendProbeStatusNotification("1", true)
	if !strings.Contains(gotText, "上线") {
		t.Fatalf("expected online text, got %q", gotText)
	}
}

func TestParseBotCommand(t *testing.T) {
	cases := []struct {
		in      string
		wantCmd string
		wantArg string
	}{
		{"/status", "status", ""},
		{"/upgrade 3", "upgrade", "3"},
		{"升级 5", "升级", "5"},
		{"/status@my_bot", "status", ""},
		{"/upgrade_all", "upgrade_all", ""},
		{"  /help  ", "help", ""},
		{"状态", "状态", ""},
	}
	for _, c := range cases {
		cmd, arg := parseBotCommand(c.in)
		if cmd != c.wantCmd || arg != c.wantArg {
			t.Fatalf("parseBotCommand(%q) = (%q,%q), want (%q,%q)", c.in, cmd, arg, c.wantCmd, c.wantArg)
		}
	}
}
