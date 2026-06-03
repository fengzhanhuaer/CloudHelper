package core

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"
)

// parseBotCommand splits a message into a lowercased command token and the
// remaining argument string. It strips a leading slash and a trailing @botname.
func parseBotCommand(text string) (string, string) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "", ""
	}
	fields := strings.Fields(trimmed)
	cmd := fields[0]
	arg := strings.TrimSpace(strings.TrimPrefix(trimmed, cmd))
	if i := strings.Index(cmd, "@"); i >= 0 {
		cmd = cmd[:i]
	}
	cmd = strings.TrimPrefix(cmd, "/")
	return strings.ToLower(cmd), arg
}

// handleTGAssistantBotCommand maps a probe ops command (slash or Chinese alias) to
// a reply. handled is false for non-commands so the caller can ignore them.
func handleTGAssistantBotCommand(text string) (string, bool) {
	cmd, arg := parseBotCommand(text)
	switch cmd {
	case "status", "状态":
		return cmdProbeStatusSummary(), true
	case "upgrade", "升级":
		return cmdUpgradeOne(arg), true
	case "upgrade_all", "全部升级", "升级全部":
		return cmdUpgradeAll(), true
	case "help", "帮助", "start":
		return botHelpText(), true
	default:
		if strings.HasPrefix(strings.TrimSpace(text), "/") {
			return "未知命令。\n" + botHelpText(), true
		}
		return "", false
	}
}

func botHelpText() string {
	return strings.Join([]string{
		"可用命令：",
		"/status 查看探针节点状态",
		"/upgrade <编号> 升级指定探针",
		"/upgrade_all 升级全部探针",
		"/ping 测试机器人",
		"/help 查看帮助",
	}, "\n")
}

func cmdProbeStatusSummary() string {
	var items []probeNodeStatusRecord
	if ProbeStore != nil {
		ProbeStore.mu.RLock()
		items = loadProbeNodeStatusLocked()
		ProbeStore.mu.RUnlock()
	}
	if len(items) == 0 {
		return "暂无探针节点"
	}

	online := 0
	for _, it := range items {
		if it.Runtime.Online {
			online++
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "探针状态：在线 %d / 共 %d", online, len(items))
	now := time.Now()
	for _, it := range items {
		icon := "🔴"
		if it.Runtime.Online {
			icon = "🟢"
		}
		line := fmt.Sprintf("#%d %s %s", it.NodeNo, strings.TrimSpace(it.NodeName), icon)
		if v := strings.TrimSpace(it.Runtime.Version); v != "" {
			line += " v" + v
		}
		if exp := strings.TrimSpace(it.ExpireAt); exp != "" {
			if t, ok := parseProbeExpireAt(exp); ok {
				days := int(math.Floor(t.Sub(now).Hours() / 24))
				if days < 0 {
					line += " 已过期"
				} else {
					line += fmt.Sprintf(" 剩余%d天", days)
				}
			}
		}
		if b.Len()+len(line)+1 > 3800 {
			b.WriteString("\n…")
			break
		}
		b.WriteString("\n")
		b.WriteString(line)
	}
	return b.String()
}

func cmdUpgradeOne(arg string) string {
	nodeID := normalizeProbeNodeID(strings.TrimSpace(arg))
	if nodeID == "" {
		return "用法：/upgrade <节点编号>"
	}
	node, ok := getProbeNodeByID(nodeID)
	if !ok {
		return fmt.Sprintf("未找到探针节点 %s", nodeID)
	}
	result, err := dispatchUpgradeToProbe(node, resolveControllerBaseURL())
	if err != nil {
		return fmt.Sprintf("❌ 升级下发失败 #%d：%s", node.NodeNo, err.Error())
	}
	return fmt.Sprintf("✅ 已下发升级 #%d %s（%s）", result.NodeNo, strings.TrimSpace(result.NodeName), result.Mode)
}

func cmdUpgradeAll() string {
	var nodes []probeNodeRecord
	if ProbeStore != nil {
		ProbeStore.mu.RLock()
		nodes = loadProbeNodesLocked()
		ProbeStore.mu.RUnlock()
	}
	if len(nodes) == 0 {
		return "暂无探针节点"
	}

	base := resolveControllerBaseURL()
	success := 0
	failures := make([]string, 0)
	for _, node := range nodes {
		if _, err := dispatchUpgradeToProbe(node, base); err != nil {
			failures = append(failures, fmt.Sprintf("#%d:%s", node.NodeNo, err.Error()))
			continue
		}
		success++
	}
	msg := fmt.Sprintf("升级下发完成：成功 %d / 共 %d", success, len(nodes))
	if len(failures) > 0 {
		msg += "\n失败：" + strings.Join(failures, "，")
	}
	return msg
}

// resolveControllerBaseURL picks the base URL probe nodes use to download upgrades
// when an upgrade is triggered from Telegram (no HTTP request to derive Host from):
// persisted last web base URL, then the webhook base URL env, then loopback.
func resolveControllerBaseURL() string {
	settings := getTGAssistantNotifySettings()
	if base := strings.TrimSpace(settings.LastControllerBaseURL); base != "" {
		return base
	}
	if base := tgAssistantBotWebhookBaseURL(); base != "" {
		return base
	}
	return "http://127.0.0.1:15030"
}

// sendTGAssistantBotReply sends a bot reply to the bound chat and records history.
func sendTGAssistantBotReply(ctx context.Context, item tgAssistantBotPollAccount, action, text string) {
	if _, _, err := sendTGAssistantBotTextMessage(ctx, item.BotAPIKey, item.AllowedChatID, text); err != nil {
		appendTGAssistantHistory(action, item.AccountID, false, fmt.Sprintf("chat_id=%d err=%s", item.AllowedChatID, err.Error()))
		return
	}
	appendTGAssistantHistory(action, item.AccountID, true, fmt.Sprintf("chat_id=%d", item.AllowedChatID))
}
