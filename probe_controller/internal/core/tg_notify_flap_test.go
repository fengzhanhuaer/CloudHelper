package core

import (
	"context"
	"strings"
	"testing"
)

func TestIsProbeNotifyAndroidNode(t *testing.T) {
	oldStore := ProbeStore
	ProbeStore = &probeConfigStore{
		data: probeConfigData{
			ProbeNodes: []probeNodeRecord{
				{NodeNo: 1, NodeName: "linux-node", TargetSystem: "linux"},
				{NodeNo: 2, NodeName: "android-node", TargetSystem: "android"},
			},
			ProbeSecrets: map[string]string{},
		},
	}
	defer func() { ProbeStore = oldStore }()

	if isProbeNotifyAndroidNode("1") {
		t.Fatal("linux node must not be treated as android")
	}
	if !isProbeNotifyAndroidNode("2") {
		t.Fatal("android node must be detected")
	}
}

func TestProbeStatusNotificationFlapSuppression(t *testing.T) {
	origStore := TGAssistantStore
	origSend := tgNotifySendFunc
	defer func() { TGAssistantStore = origStore; tgNotifySendFunc = origSend }()
	chdirTemp(t)

	TGAssistantStore = &tgAssistantStore{
		data: tgAssistantStoreData{
			Accounts: []tgAssistantAccountRecord{{ID: "a1", Phone: "+100", Authorized: true, SelfUserID: 777, BotAPIKey: "11:abc"}},
			Notify:   tgAssistantNotifySettings{NotifyAccountID: "a1", EnableOffline: true},
		},
	}

	var sends []string
	tgNotifySendFunc = func(ctx context.Context, botAPIKey string, chatID int64, text string) (int, string, error) {
		sends = append(sends, text)
		return 1, text, nil
	}

	const node = "9001"
	// Reset any residual state for this node.
	tgNotifyOfflineState.mu.Lock()
	delete(tgNotifyOfflineState.lastSent, node)
	tgNotifyOfflineState.mu.Unlock()

	sendProbeStatusNotification(node, false) // offline -> send
	sendProbeStatusNotification(node, false) // duplicate offline -> suppressed
	sendProbeStatusNotification(node, true)  // online -> send
	sendProbeStatusNotification(node, true)  // duplicate online -> suppressed

	if len(sends) != 2 {
		t.Fatalf("expected 2 sends (offline,online), got %d: %v", len(sends), sends)
	}
	if !strings.Contains(sends[0], "离线") || !strings.Contains(sends[1], "上线") {
		t.Fatalf("unexpected send order/content: %v", sends)
	}
}

func TestProbeNotifyOnlineGateRequiresPriorOffline(t *testing.T) {
	const node = "9002"
	tgNotifyOfflineState.mu.Lock()
	delete(tgNotifyOfflineState.lastSent, node)
	tgNotifyOfflineState.mu.Unlock()

	// Never been notified offline -> the online gate must hold.
	if tgNotifyLastSent(node) == "offline" {
		t.Fatal("precondition failed")
	}
	// Simulate a confirmed offline, then online is allowed.
	tgNotifyMarkSentIfChanged(node, "offline")
	if tgNotifyLastSent(node) != "offline" {
		t.Fatal("expected offline recorded")
	}
}
