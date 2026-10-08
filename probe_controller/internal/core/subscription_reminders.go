package core

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const subscriptionCheckInterval = 5 * time.Minute

var subscriptionMu sync.Mutex
var subscriptionCheckMu sync.Mutex

// Resume after the last attempted item when the check's network budget expires.
var subscriptionNextID string
var subscriptionBackupFunc = triggerAutoBackupControllerDataAsync

type subscriptionReminder struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	URL             string    `json:"url"`
	Mode            string    `json:"mode"`
	PeriodCount     int       `json:"period_count"`
	PeriodUnit      string    `json:"period_unit"`
	Days            int       `json:"days"`
	Enabled         bool      `json:"enabled"`
	DueAt           time.Time `json:"due_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	Revision        string    `json:"revision"`
	LastReminderDay string    `json:"last_reminder_day,omitempty"`
	LastSentAt      time.Time `json:"last_sent_at,omitzero"`
	LastError       string    `json:"last_error,omitempty"`
}

type subscriptionRequest struct {
	Action      string `json:"action"`
	ID          string `json:"id"`
	Name        string `json:"name"`
	URL         string `json:"url"`
	Mode        string `json:"mode"`
	PeriodCount int    `json:"period_count"`
	PeriodUnit  string `json:"period_unit"`
	Days        int    `json:"days"`
	Enabled     bool   `json:"enabled"`
	DueAt       string `json:"due_at"`
	ExpiryMode  string `json:"expiry_mode"`
	DueDate     string `json:"due_date"`
}

type subscriptionInputError string

func (e subscriptionInputError) Error() string { return string(e) }

func subscriptionStoragePath() string {
	return filepath.Join(dataDir, "subscription_reminders.json")
}

// Callers hold subscriptionMu. Missing data is an empty list; malformed data
// is an error rather than permission to overwrite the user's subscriptions.
func loadSubscriptionsLocked() ([]subscriptionReminder, error) {
	items := []subscriptionReminder{}
	data, err := os.ReadFile(subscriptionStoragePath())
	if os.IsNotExist(err) {
		return items, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, err
	}
	if items == nil {
		items = []subscriptionReminder{}
	}
	for i := range items {
		// v0.4.46 countdown records retain their expiry and successful-send
		// history; the entered days become their renewal period.
		if items[i].Mode == "countdown" {
			items[i].Mode, items[i].PeriodUnit = "period", "day"
			items[i].PeriodCount = items[i].Days
			if items[i].PeriodCount <= 0 {
				items[i].PeriodCount = 30
			}
		}
	}
	return items, nil
}

func saveSubscriptionsLocked(items []subscriptionReminder) error {
	path := subscriptionStoragePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path+".tmp", data, 0o600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

func getSubscriptions() ([]subscriptionReminder, error) {
	subscriptionMu.Lock()
	defer subscriptionMu.Unlock()
	return loadSubscriptionsLocked()
}

func updateSubscription(req subscriptionRequest, now time.Time) ([]subscriptionReminder, error) {
	subscriptionMu.Lock()
	defer subscriptionMu.Unlock()
	items, err := loadSubscriptionsLocked()
	if err != nil {
		return nil, err
	}
	index := -1
	for i := range items {
		if req.ID != "" && items[i].ID == req.ID {
			index = i
			break
		}
	}
	if req.ID != "" && index < 0 {
		return nil, subscriptionInputError("订阅不存在，请刷新列表")
	}
	switch req.Action {
	case "save":
		item, err := subscriptionFromRequest(req, now)
		if err != nil {
			return nil, err
		}
		if index >= 0 {
			previous := items[index]
			item.ID = previous.ID
			if item.DueAt.Equal(previous.DueAt) {
				item.LastReminderDay = previous.LastReminderDay
				item.LastSentAt = previous.LastSentAt
				item.LastError = previous.LastError
			}
			items[index] = item
		} else {
			if len(items) >= 500 {
				return nil, subscriptionInputError("最多保存 500 个订阅")
			}
			item.ID = rand.Text()
			items = append(items, item)
		}
	case "delete":
		if index < 0 {
			return nil, subscriptionInputError("请选择订阅")
		}
		items = append(items[:index], items[index+1:]...)
	case "renew":
		if index < 0 || items[index].Mode != "period" {
			return nil, subscriptionInputError("仅周期订阅支持已续订操作")
		}
		item := &items[index]
		base := item.DueAt.In(now.Location())
		if base.Before(now) {
			base = now
		}
		due, err := subscriptionPeriodEnd(base, item.PeriodCount, item.PeriodUnit)
		if err != nil {
			return nil, err
		}
		item.DueAt = due.UTC()
		item.UpdatedAt = now.UTC()
		item.Revision = rand.Text()
		item.LastReminderDay, item.LastError = "", ""
		item.LastSentAt = time.Time{}
	default:
		return nil, subscriptionInputError("不支持的操作")
	}
	if err := saveSubscriptionsLocked(items); err != nil {
		return nil, err
	}
	return items, nil
}

func subscriptionFromRequest(req subscriptionRequest, now time.Time) (subscriptionReminder, error) {
	item := subscriptionReminder{
		Name: strings.TrimSpace(req.Name), URL: strings.TrimSpace(req.URL),
		Mode: "period", PeriodCount: req.PeriodCount, PeriodUnit: req.PeriodUnit,
		Days: req.Days, Enabled: req.Enabled, UpdatedAt: now.UTC(), Revision: rand.Text(),
	}
	if item.Name == "" || len([]rune(item.Name)) > 120 || strings.ContainsAny(item.Name, "\r\n") {
		return item, subscriptionInputError("名称应为 1–120 个字符，且不能换行")
	}
	u, err := url.Parse(item.URL)
	if item.URL != "" && (err != nil || len(item.URL) > 2048 || u == nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil) {
		return item, subscriptionInputError("URL 应为不超过 2048 字节的 HTTP(S) 地址，不含用户名密码")
	}
	// Old clients can still save a countdown, but every stored subscription
	// now has a renewal period independent of its initial expiry.
	if req.Mode == "countdown" {
		item.PeriodCount, item.PeriodUnit = req.Days, "day"
		if item.PeriodCount == 0 && req.DueAt != "" {
			item.PeriodCount = 30
		}
	} else if req.Mode != "" && req.Mode != "period" {
		return item, subscriptionInputError("所有订阅均需设置续订周期")
	}
	item.DueAt, err = subscriptionPeriodEnd(now, item.PeriodCount, item.PeriodUnit)
	if err != nil {
		return item, err
	}
	if (req.ExpiryMode == "days" || req.ExpiryMode == "") && (req.Days < 0 || req.Days > 36500) {
		return item, subscriptionInputError("天数应为 0–36500")
	}
	switch req.ExpiryMode {
	case "date":
		date, parseErr := time.ParseInLocation("2006-01-02", req.DueDate, now.Location())
		if parseErr != nil || date.Year() < 2000 || date.Year() > 9998 {
			return item, subscriptionInputError("请选择有效的到期日期")
		}
		item.DueAt = date.AddDate(0, 0, 1).Add(-time.Second)
		item.Days = 0
	case "days":
		item.DueAt = now.AddDate(0, 0, req.Days)
	case "": // Compatibility with v0.4.46 payloads and exact-expiry edits.
		if req.Days > 0 {
			item.DueAt = now.AddDate(0, 0, req.Days)
		}
	default:
		return item, subscriptionInputError("请选择具体日期或剩余天数")
	}
	if req.ExpiryMode == "" && req.DueAt != "" {
		item.DueAt, err = time.Parse(time.RFC3339, req.DueAt)
		if err != nil || item.DueAt.Year() < 2000 || item.DueAt.Year() > 9999 {
			return item, subscriptionInputError("到期时间无效")
		}
	}
	item.DueAt = item.DueAt.UTC().Truncate(time.Second)
	return item, nil
}

func subscriptionPeriodEnd(base time.Time, count int, unit string) (time.Time, error) {
	limits := map[string]int{"day": 36500, "month": 1200, "year": 100}
	if count <= 0 || count > limits[unit] {
		return time.Time{}, subscriptionInputError("周期应为 1–36500 天、1–1200 月或 1–100 年")
	}
	if unit == "day" {
		return base.AddDate(0, 0, count), nil
	}
	months := count
	if unit == "year" {
		months *= 12
	}
	// Billing months clamp to the last day instead of skipping February.
	first := time.Date(base.Year(), base.Month()+time.Month(months), 1, base.Hour(), base.Minute(), base.Second(), base.Nanosecond(), base.Location())
	day := base.Day()
	if last := first.AddDate(0, 1, -1).Day(); day > last {
		day = last
	}
	return first.AddDate(0, 0, day-1), nil
}

func subscriptionDaysRemaining(due, now time.Time) int {
	due = due.In(now.Location())
	dueDay := time.Date(due.Year(), due.Month(), due.Day(), 0, 0, 0, 0, time.UTC)
	nowDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	return int(dueDay.Sub(nowDay).Hours() / 24)
}

func mngSubscriptionsPageHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/mng/subscriptions" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	writeMngPageHTML(w, r, mngSubscriptionsPageHTML)
}

func mngSubscriptionsHandler(w http.ResponseWriter, r *http.Request) {
	var items []subscriptionReminder
	var err error
	now := time.Now()
	switch r.Method {
	case http.MethodGet:
		items, err = getSubscriptions()
	case http.MethodPost:
		var req subscriptionRequest
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		if err := decodeMngJSONBody(r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求格式无效"})
			return
		}
		items, err = updateSubscription(req, now)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err != nil {
		var inputErr subscriptionInputError
		if errors.As(err, &inputErr) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": string(inputErr)})
		} else {
			logControllerErrorf("subscription storage failed: %v", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "订阅读写失败，请检查主控数据目录和日志"})
		}
		return
	}
	if r.Method == http.MethodPost {
		subscriptionBackupFunc("subscriptions_save")
	}
	type itemView struct {
		subscriptionReminder
		DaysRemaining int `json:"days_remaining"`
	}
	views := make([]itemView, 0, len(items))
	for _, item := range items {
		views = append(views, itemView{item, subscriptionDaysRemaining(item.DueAt, now)})
	}
	_, _, _, ready := resolveNotifyBot()
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"items": views, "tg_ready": ready, "server_time": now.Format(time.RFC3339),
	})
}

func initSubscriptionReminderEngine() {
	scheduleSubscriptionReminderCheck(time.Now().Add(10 * time.Second))
}

func scheduleSubscriptionReminderCheck(at time.Time) {
	scheduleGlobalTask("subscriptions.reminders", at, 2*time.Minute, func(ctx context.Context) {
		defer scheduleSubscriptionReminderCheck(time.Now().Add(subscriptionCheckInterval))
		botKey, chatID, accountID, ready := resolveNotifyBot()
		if !ready {
			return
		}
		if err := checkSubscriptionReminders(ctx, time.Now(), func(ctx context.Context, text string) error {
			_, _, err := tgNotifySendFunc(ctx, botKey, chatID, text)
			// Do not expose bot tokens or subscription URLs from transport errors.
			appendTGAssistantHistory("notify.subscription", accountID, err == nil, "subscription reminder")
			return err
		}); err != nil {
			log.Printf("subscription reminder check failed: %v", err)
		}
	})
}

// The send callback makes timing, failures and restart dedup testable without
// sending actual Telegram messages. Network I/O never holds subscriptionMu.
func checkSubscriptionReminders(ctx context.Context, now time.Time, send func(context.Context, string) error) error {
	subscriptionCheckMu.Lock()
	defer subscriptionCheckMu.Unlock()
	items, err := getSubscriptions()
	if err != nil {
		return err
	}
	day := now.Format("2006-01-02")
	start := 0
	for i, item := range items {
		if item.ID == subscriptionNextID {
			start = i
			break
		}
	}
	for offset := range items {
		if err := ctx.Err(); err != nil {
			return err
		}
		index := (start + offset) % len(items)
		snapshot := items[index]
		subscriptionNextID = items[(index+1)%len(items)].ID
		if !snapshot.Enabled || snapshot.DueAt.IsZero() || snapshot.LastReminderDay == day || subscriptionDaysRemaining(snapshot.DueAt, now) > 7 {
			continue
		}
		// A record could have been changed while another message was being sent.
		subscriptionMu.Lock()
		current, err := loadSubscriptionsLocked()
		valid := false
		for _, item := range current {
			if item.ID == snapshot.ID && item.Revision == snapshot.Revision {
				valid = true
				break
			}
		}
		subscriptionMu.Unlock()
		if err != nil {
			return err
		}
		if !valid {
			continue
		}
		status := fmt.Sprintf("距到期 %d 天", subscriptionDaysRemaining(snapshot.DueAt, now))
		if !now.Before(snapshot.DueAt) {
			status = "已到期，请续订或暂停提醒"
		} else if subscriptionDaysRemaining(snapshot.DueAt, now) == 0 {
			status = "今天到期"
		}
		text := fmt.Sprintf("订阅到期提醒\n名称：%s\n%s\n到期：%s", snapshot.Name, status, snapshot.DueAt.In(now.Location()).Format("2006-01-02 15:04 MST"))
		if snapshot.URL != "" {
			text += "\nURL：" + snapshot.URL
		}
		sendCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		sendErr := send(sendCtx, text)
		cancel()
		if err := recordSubscriptionReminderResult(snapshot, day, now, sendErr); err != nil {
			return err
		}
	}
	return nil
}

func recordSubscriptionReminderResult(snapshot subscriptionReminder, day string, now time.Time, sendErr error) error {
	subscriptionMu.Lock()
	defer subscriptionMu.Unlock()
	items, err := loadSubscriptionsLocked()
	if err != nil {
		return err
	}
	for i := range items {
		item := &items[i]
		if item.ID != snapshot.ID || !item.DueAt.Equal(snapshot.DueAt) {
			continue
		}
		// Keep successful dedup when only the name or enabled state changed in
		// flight, but never attach an old failure to a newly edited record.
		if sendErr != nil && item.Revision != snapshot.Revision {
			return nil
		}
		if sendErr == nil {
			item.LastReminderDay, item.LastSentAt, item.LastError = day, now.UTC(), ""
		} else {
			message := "TG 发送失败，将在下次检查重试"
			if item.LastError == message {
				return nil
			}
			item.LastError = message
		}
		return saveSubscriptionsLocked(items)
	}
	return nil
}
