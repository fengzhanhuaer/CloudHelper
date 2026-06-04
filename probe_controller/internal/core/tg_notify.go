package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// tgNotifySendFunc is the indirection used for all outbound notification sends so
// tests can capture messages without hitting the real Telegram bot API.
var tgNotifySendFunc = sendTGAssistantBotTextMessage

// tgNotifyOfflineState tracks a per-node generation counter used to debounce
// offline notifications: a pending offline job only fires if no newer transition
// (online recovery or another offline event) happened in the meantime. lastSent
// records the last state we actually notified ("online"/"offline") so we never send
// a second "上线" without an intervening "掉线" (flap suppression).
var tgNotifyOfflineState = struct {
	mu       sync.Mutex
	gen      map[string]int64
	lastSent map[string]string
}{gen: map[string]int64{}, lastSent: map[string]string{}}

type tgAssistantNotifyView struct {
	Settings          tgAssistantNotifySettings `json:"settings"`
	AccountID         string                    `json:"account_id,omitempty"`
	AccountLabel      string                    `json:"account_label,omitempty"`
	AccountAuthorized bool                      `json:"account_authorized"`
	BotConfigured     bool                      `json:"bot_configured"`
	Ready             bool                      `json:"ready"`
	LastPush          *tgAssistantHistoryRecord `json:"last_push,omitempty"`
}

type telegramBotCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

type telegramBotSetMyCommandsRequest struct {
	Commands []telegramBotCommand `json:"commands"`
}

func tgNotifyOfflineJobKey(nodeID string) string {
	return "tg.notify.offline." + strings.TrimSpace(nodeID)
}

func tgNotifyBumpGeneration(nodeID string) int64 {
	tgNotifyOfflineState.mu.Lock()
	defer tgNotifyOfflineState.mu.Unlock()
	tgNotifyOfflineState.gen[nodeID]++
	return tgNotifyOfflineState.gen[nodeID]
}

func tgNotifyCurrentGeneration(nodeID string) int64 {
	tgNotifyOfflineState.mu.Lock()
	defer tgNotifyOfflineState.mu.Unlock()
	return tgNotifyOfflineState.gen[nodeID]
}

// tgNotifyMarkSentIfChanged records that we just notified `state` for nodeID and
// reports whether that differs from the last notified state. It returns false when
// the same state was already notified, so callers can suppress duplicate
// notifications (e.g. repeated "上线" while a node flaps).
func tgNotifyMarkSentIfChanged(nodeID, state string) bool {
	tgNotifyOfflineState.mu.Lock()
	defer tgNotifyOfflineState.mu.Unlock()
	if tgNotifyOfflineState.lastSent[nodeID] == state {
		return false
	}
	tgNotifyOfflineState.lastSent[nodeID] = state
	return true
}

func tgNotifyLastSent(nodeID string) string {
	tgNotifyOfflineState.mu.Lock()
	defer tgNotifyOfflineState.mu.Unlock()
	return tgNotifyOfflineState.lastSent[nodeID]
}

// isProbeNotifyAndroidNode reports whether the node is an Android probe. Android
// devices sleep and reconnect constantly, so their online/offline churn is noise
// and must not be notified.
func isProbeNotifyAndroidNode(nodeID string) bool {
	if node, ok := getProbeNodeByID(nodeID); ok {
		if normalizeProbeTargetSystem(node.TargetSystem) == "android" {
			return true
		}
	}
	if rt, ok := getProbeRuntime(nodeID); ok && strings.EqualFold(strings.TrimSpace(rt.Platform), "android") {
		return true
	}
	return false
}

// onProbeRuntimeTransition is invoked from setProbeRuntimeOnline when a probe node
// crosses an online<->offline edge (only for already-seen nodes, so controller
// cold-starts do not spam). Offline events are debounced to swallow brief flaps;
// online events are gated so a reconnect only notifies if we previously reported
// the node offline. Android probes are skipped entirely (their churn is noise).
func onProbeRuntimeTransition(nodeID string, online bool) {
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" {
		return
	}

	settings := getTGAssistantNotifySettings()
	jobKey := tgNotifyOfflineJobKey(nodeID)
	if !settings.EnableOffline {
		// Invalidate any pending offline job so it never fires after disable.
		tgNotifyBumpGeneration(nodeID)
		cancelGlobalTask(jobKey)
		return
	}
	if isProbeNotifyAndroidNode(nodeID) {
		tgNotifyBumpGeneration(nodeID)
		cancelGlobalTask(jobKey)
		return
	}

	gen := tgNotifyBumpGeneration(nodeID)
	if online {
		cancelGlobalTask(jobKey)
		// Only announce recovery if we actually told the user it was offline;
		// otherwise a flapping node would spam "上线".
		if tgNotifyLastSent(nodeID) != "offline" {
			return
		}
		go sendProbeStatusNotification(nodeID, true)
		return
	}

	debounce := time.Duration(settings.OfflineDebounceSec) * time.Second
	scheduleGlobalTask(jobKey, time.Now().Add(debounce), 30*time.Second, func(ctx context.Context) {
		if tgNotifyCurrentGeneration(nodeID) != gen {
			return
		}
		if rt, ok := getProbeRuntime(nodeID); ok && rt.Online {
			return
		}
		sendProbeStatusNotification(nodeID, false)
	})
}

func sendProbeStatusNotification(nodeID string, online bool) {
	state := "offline"
	if online {
		state = "online"
	}
	// Collapse duplicates: never two "上线" (or two "掉线") in a row for one node.
	if !tgNotifyMarkSentIfChanged(nodeID, state) {
		return
	}

	botAPIKey, chatID, accountID, ok := resolveNotifyBot()
	if !ok {
		return
	}

	label := probeNotifyNodeLabel(nodeID)
	icon, stateText, action := "🔴", "离线", "notify.offline"
	if online {
		icon, stateText, action = "🟢", "上线", "notify.online"
	}
	text := fmt.Sprintf("%s 探针%s\n%s\n时间: %s", icon, stateText, label, time.Now().Format("2006-01-02 15:04:05"))

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, _, err := tgNotifySendFunc(ctx, botAPIKey, chatID, text); err != nil {
		appendTGAssistantHistory(action, accountID, false, fmt.Sprintf("node=%s err=%s", nodeID, err.Error()))
		return
	}
	appendTGAssistantHistory(action, accountID, true, fmt.Sprintf("node=%s", nodeID))
}

// probeNotifyNodeLabel resolves a human-friendly label like "#3 香港节点" for a node id.
func probeNotifyNodeLabel(nodeID string) string {
	name := ""
	no := 0
	if ProbeStore != nil {
		ProbeStore.mu.RLock()
		rec, found := loadProbeNodeStatusByIDLocked(nodeID)
		ProbeStore.mu.RUnlock()
		if found {
			name = strings.TrimSpace(rec.NodeName)
			no = rec.NodeNo
		}
	}
	switch {
	case no > 0 && name != "":
		return fmt.Sprintf("#%d %s", no, name)
	case no > 0:
		return fmt.Sprintf("#%d", no)
	case name != "":
		return name
	default:
		return "节点 " + nodeID
	}
}

func testTGAssistantNotifyPush() (tgAssistantHistoryRecord, error) {
	botAPIKey, chatID, accountID, ok := resolveNotifyBot()
	if !ok {
		return tgAssistantHistoryRecord{}, errors.New("通知机器人未就绪：请选择已登录且已配置 BOT 的账号")
	}
	text := fmt.Sprintf("✅ 主控通知测试\n时间: %s", time.Now().Format("2006-01-02 15:04:05"))

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, _, err := tgNotifySendFunc(ctx, botAPIKey, chatID, text); err != nil {
		appendTGAssistantHistory("notify.test", accountID, false, err.Error())
		return tgAssistantHistoryRecord{}, err
	}
	appendTGAssistantHistory("notify.test", accountID, true, "ok")
	return tgAssistantHistoryRecord{
		Time:      time.Now().UTC().Format(time.RFC3339),
		Action:    "notify.test",
		AccountID: accountID,
		Success:   true,
		Message:   "ok",
	}, nil
}

func getTGAssistantNotifyOverview() tgAssistantNotifyView {
	settings := getTGAssistantNotifySettings()
	view := tgAssistantNotifyView{Settings: settings}

	if settings.NotifyAccountID != "" && TGAssistantStore != nil {
		TGAssistantStore.mu.RLock()
		records := loadTGAssistantAccountsLocked()
		index := indexTGAssistantAccountByID(records, settings.NotifyAccountID)
		if index >= 0 {
			rec := records[index]
			view.AccountID = rec.ID
			view.AccountLabel = rec.Label
			view.AccountAuthorized = rec.Authorized
			view.BotConfigured = strings.TrimSpace(rec.BotAPIKey) != ""
		}
		TGAssistantStore.mu.RUnlock()
	}

	_, _, _, ready := resolveNotifyBot()
	view.Ready = ready
	if last, ok := loadLastTGAssistantNotifyHistory(); ok {
		view.LastPush = &last
	}
	return view
}

func loadLastTGAssistantNotifyHistory() (tgAssistantHistoryRecord, bool) {
	content, err := os.ReadFile(tgAssistantHistoryPath())
	if err != nil {
		return tgAssistantHistoryRecord{}, false
	}
	lines := strings.Split(string(content), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		var rec tgAssistantHistoryRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if strings.HasPrefix(rec.Action, "notify.") {
			return rec, true
		}
	}
	return tgAssistantHistoryRecord{}, false
}

// registerTGAssistantBotCommands publishes the bot command menu via setMyCommands.
// full=true exposes the probe ops commands (only for the global notify account).
func registerTGAssistantBotCommands(ctx context.Context, botAPIKey string, full bool) error {
	commands := []telegramBotCommand{
		{Command: "ping", Description: "测试机器人是否在线"},
	}
	if full {
		commands = []telegramBotCommand{
			{Command: "status", Description: "查看探针节点状态"},
			{Command: "upgrade", Description: "升级指定探针：/upgrade <编号>"},
			{Command: "upgrade_all", Description: "升级全部探针"},
			{Command: "help", Description: "查看可用命令"},
			{Command: "ping", Description: "测试机器人是否在线"},
		}
	}
	return callTelegramBotAPI(ctx, botAPIKey, "setMyCommands", telegramBotSetMyCommandsRequest{Commands: commands}, nil)
}

// registerTGAssistantNotifyBotCommands registers the full ops command menu for the
// given notify account's bot, best-effort and asynchronous.
func registerTGAssistantNotifyBotCommands(accountID string) {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" || TGAssistantStore == nil {
		return
	}
	TGAssistantStore.mu.RLock()
	records := loadTGAssistantAccountsLocked()
	index := indexTGAssistantAccountByID(records, accountID)
	botAPIKey := ""
	if index >= 0 {
		botAPIKey = strings.TrimSpace(records[index].BotAPIKey)
	}
	TGAssistantStore.mu.RUnlock()
	if botAPIKey == "" {
		return
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := registerTGAssistantBotCommands(ctx, botAPIKey, true); err != nil {
			appendTGAssistantHistory("notify.commands.register", accountID, false, err.Error())
			return
		}
		appendTGAssistantHistory("notify.commands.register", accountID, true, "full menu")
	}()
}
