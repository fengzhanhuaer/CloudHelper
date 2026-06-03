package core

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const tgRenewalStateFile = "renewal_state.json"

var renewalStateMu sync.Mutex

// renewalNotifyState dedupes renewal reminders: Nodes[nodeKey][thresholdKey] stores
// the ExpireAt value that the reminder was last sent for. When ExpireAt changes (a
// renewal), the stored value no longer matches and the thresholds reset.
type renewalNotifyState struct {
	Nodes map[string]map[string]string `json:"nodes"`
}

func initTGAssistantRenewalEngine() {
	scheduleNextRenewalCheck()
	log.Println("tg assistant renewal engine started")
}

func scheduleNextRenewalCheck() {
	settings := getTGAssistantNotifySettings()
	runAt := nextDailyRunAt(settings.RenewalCheckHour, time.Now())
	scheduleGlobalTask("tg.renewal.daily", runAt, tgAssistantScheduleJobTimeout, runRenewalCheck)
}

func nextDailyRunAt(hour int, from time.Time) time.Time {
	if hour < 0 || hour > 23 {
		hour = tgAssistantNotifyDefaultCheckHour
	}
	next := time.Date(from.Year(), from.Month(), from.Day(), hour, 0, 0, 0, from.Location())
	if !next.After(from) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}

func runRenewalCheck(ctx context.Context) {
	// Always keep the daily cadence alive, even when we return early.
	defer scheduleNextRenewalCheck()

	settings := getTGAssistantNotifySettings()
	if !settings.EnableRenewal {
		return
	}
	botAPIKey, chatID, accountID, ok := resolveNotifyBot()
	if !ok {
		return
	}

	var nodes []probeNodeRecord
	if ProbeStore != nil {
		ProbeStore.mu.RLock()
		nodes = loadProbeNodesLocked()
		ProbeStore.mu.RUnlock()
	}

	now := time.Now()
	state := loadRenewalNotifyState()
	changed := false
	for _, node := range nodes {
		nodeKey := strconv.Itoa(node.NodeNo)
		expireRaw := strings.TrimSpace(node.ExpireAt)
		if expireRaw == "" {
			if _, exists := state.Nodes[nodeKey]; exists {
				delete(state.Nodes, nodeKey)
				changed = true
			}
			continue
		}
		expireTime, parsed := parseProbeExpireAt(expireRaw)
		if !parsed {
			continue
		}
		daysUntil := int(math.Floor(expireTime.Sub(now).Hours() / 24))
		fired := state.Nodes[nodeKey]
		key, fire := renewalKeyToFire(daysUntil, settings.RenewalThresholds, func(k string) bool {
			return fired != nil && fired[k] == expireRaw
		})
		if !fire {
			continue
		}

		text := renewalNotificationText(node, daysUntil, expireRaw)
		sendCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		_, _, err := tgNotifySendFunc(sendCtx, botAPIKey, chatID, text)
		cancel()
		if err != nil {
			appendTGAssistantHistory("notify.renewal", accountID, false, fmt.Sprintf("node=%s err=%s", nodeKey, err.Error()))
			continue
		}
		if fired == nil {
			fired = map[string]string{}
		}
		fired[key] = expireRaw
		if state.Nodes == nil {
			state.Nodes = map[string]map[string]string{}
		}
		state.Nodes[nodeKey] = fired
		changed = true
		appendTGAssistantHistory("notify.renewal", accountID, true, fmt.Sprintf("node=%s threshold=%s days=%d", nodeKey, key, daysUntil))
	}

	if changed {
		saveRenewalNotifyState(state)
	}
}

// renewalKeyToFire returns the most urgent unfired threshold key for the given
// days-until-expiry, or fire=false when nothing should be sent this run. It fires
// at most one reminder per run so a freshly-added node does not blast 7/3/1 at once.
func renewalKeyToFire(daysUntil int, thresholds []int, isFired func(string) bool) (string, bool) {
	if daysUntil < 0 {
		if !isFired("expired") {
			return "expired", true
		}
		return "", false
	}
	asc := append([]int(nil), thresholds...)
	sort.Ints(asc) // ascending = most urgent first
	for _, t := range asc {
		if t <= 0 || daysUntil > t {
			continue
		}
		key := strconv.Itoa(t)
		if !isFired(key) {
			return key, true
		}
	}
	return "", false
}

func renewalNotificationText(node probeNodeRecord, daysUntil int, expireRaw string) string {
	header := fmt.Sprintf("#%d", node.NodeNo)
	if name := strings.TrimSpace(node.NodeName); name != "" {
		header += " " + name
	}
	if daysUntil < 0 {
		return fmt.Sprintf("⏰ 续费提醒\n%s\n已过期（到期 %s）", header, expireRaw)
	}
	return fmt.Sprintf("⏰ 续费提醒\n%s\n到期 %s（剩余 %d 天）", header, expireRaw, daysUntil)
}

// parseProbeExpireAt mirrors the frontend parseExpireAt semantics: a bare
// YYYY-MM-DD is treated as the end of that local day, otherwise RFC3339 (or a
// common datetime layout) is accepted.
func parseProbeExpireAt(value string) (time.Time, bool) {
	text := strings.TrimSpace(value)
	if text == "" {
		return time.Time{}, false
	}
	if len(text) == 10 {
		if t, err := time.ParseInLocation("2006-01-02", text, time.Local); err == nil {
			return t.Add(23*time.Hour + 59*time.Minute + 59*time.Second), true
		}
	}
	if t, err := time.Parse(time.RFC3339, text); err == nil {
		return t, true
	}
	if t, err := time.ParseInLocation("2006-01-02 15:04:05", text, time.Local); err == nil {
		return t, true
	}
	return time.Time{}, false
}

func renewalStatePath() string {
	return filepath.Join(tgAssistantTempDirPath(), tgRenewalStateFile)
}

func loadRenewalNotifyState() renewalNotifyState {
	renewalStateMu.Lock()
	defer renewalStateMu.Unlock()

	state := renewalNotifyState{Nodes: map[string]map[string]string{}}
	content, err := os.ReadFile(renewalStatePath())
	if err != nil {
		return state
	}
	if len(strings.TrimSpace(string(content))) == 0 {
		return state
	}
	if err := json.Unmarshal(content, &state); err != nil {
		return renewalNotifyState{Nodes: map[string]map[string]string{}}
	}
	if state.Nodes == nil {
		state.Nodes = map[string]map[string]string{}
	}
	return state
}

func saveRenewalNotifyState(state renewalNotifyState) {
	renewalStateMu.Lock()
	defer renewalStateMu.Unlock()

	if err := os.MkdirAll(tgAssistantTempDirPath(), 0o755); err != nil {
		log.Printf("renewal state mkdir failed: %v", err)
		return
	}
	content, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		log.Printf("renewal state marshal failed: %v", err)
		return
	}
	path := renewalStatePath()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, content, 0o644); err != nil {
		log.Printf("renewal state write tmp failed: %v", err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		log.Printf("renewal state rename failed: %v", err)
	}
}
