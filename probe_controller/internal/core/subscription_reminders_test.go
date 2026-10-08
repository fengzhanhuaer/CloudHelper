package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func subscriptionTestSetup(t *testing.T) time.Time {
	t.Helper()
	chdirTemp(t)
	oldBackup := subscriptionBackupFunc
	oldCursor := subscriptionNextID
	subscriptionNextID = ""
	subscriptionBackupFunc = func(string) {}
	t.Cleanup(func() { subscriptionBackupFunc = oldBackup; subscriptionNextID = oldCursor })
	return time.Date(2026, 10, 8, 12, 30, 0, 0, time.FixedZone("SGT", 8*3600))
}

func subscriptionTestRequest(days int) subscriptionRequest {
	return subscriptionRequest{Action: "save", Name: "云服务", URL: "https://example.com/account", Mode: "countdown", Days: days, Enabled: true}
}

func subscriptionMustUpdate(t *testing.T, req subscriptionRequest, now time.Time) []subscriptionReminder {
	t.Helper()
	items, err := updateSubscription(req, now)
	if err != nil {
		t.Fatal(err)
	}
	return items
}

func TestSubscriptionPersistenceEditPauseDelete(t *testing.T) {
	now := subscriptionTestSetup(t)
	item := subscriptionMustUpdate(t, subscriptionTestRequest(30), now)[0]
	loaded, err := getSubscriptions()
	if err != nil || len(loaded) != 1 || loaded[0].ID != item.ID || !loaded[0].DueAt.Equal(now.AddDate(0, 0, 30)) {
		t.Fatalf("reload: %+v, %v", loaded, err)
	}
	if err := recordSubscriptionReminderResult(item, "2026-10-08", now, nil); err != nil {
		t.Fatal(err)
	}
	req := subscriptionTestRequest(30)
	req.ID, req.Name, req.Enabled, req.DueAt = item.ID, "新名称", false, item.DueAt.Format(time.RFC3339)
	item = subscriptionMustUpdate(t, req, now.Add(time.Minute))[0]
	if item.Enabled || item.Name != "新名称" || item.LastReminderDay != "2026-10-08" {
		t.Fatalf("metadata edit lost dedup: %+v", item)
	}
	if got := subscriptionMustUpdate(t, subscriptionRequest{Action: "delete", ID: item.ID}, now); len(got) != 0 {
		t.Fatalf("delete left items: %+v", got)
	}
	data, _ := os.ReadFile(subscriptionStoragePath())
	if strings.TrimSpace(string(data)) != "[]" {
		t.Fatalf("empty list should persist: %s", data)
	}
}

func TestSubscriptionPeriodsAndRenewal(t *testing.T) {
	now := subscriptionTestSetup(t)
	for _, tc := range []struct {
		start, unit, want string
		count             int
	}{
		{"2026-01-31T12:30:00Z", "month", "2026-02-28T12:30:00Z", 1},
		{"2028-02-29T12:30:00Z", "year", "2029-02-28T12:30:00Z", 1},
		{"2026-10-08T12:30:00Z", "day", "2026-11-07T12:30:00Z", 30},
	} {
		base, _ := time.Parse(time.RFC3339, tc.start)
		got, err := subscriptionPeriodEnd(base, tc.count, tc.unit)
		if err != nil || got.Format(time.RFC3339) != tc.want {
			t.Fatalf("%s + %d %s: %v, %v", tc.start, tc.count, tc.unit, got, err)
		}
	}
	req := subscriptionTestRequest(10)
	req.Mode, req.PeriodCount, req.PeriodUnit = "period", 1, "month"
	item := subscriptionMustUpdate(t, req, now)[0]
	if !item.DueAt.Equal(now.AddDate(0, 0, 10)) {
		t.Fatal("first expiry override ignored")
	}
	if err := recordSubscriptionReminderResult(item, "2026-10-08", now, nil); err != nil {
		t.Fatal(err)
	}
	oldDue := item.DueAt
	item = subscriptionMustUpdate(t, subscriptionRequest{Action: "renew", ID: item.ID}, now)[0]
	want, _ := subscriptionPeriodEnd(oldDue.In(now.Location()), 1, "month")
	if !item.DueAt.Equal(want) || item.LastReminderDay != "" || !item.LastSentAt.IsZero() {
		t.Fatalf("renew: %+v", item)
	}
	overdueNow := item.DueAt.In(now.Location()).AddDate(0, 0, 20)
	item = subscriptionMustUpdate(t, subscriptionRequest{Action: "renew", ID: item.ID}, overdueNow)[0]
	want, _ = subscriptionPeriodEnd(overdueNow, 1, "month")
	if !item.DueAt.Equal(want) {
		t.Fatal("overdue renew should use current time")
	}
}

func TestSubscriptionValidationAndCorruptStorage(t *testing.T) {
	now := subscriptionTestSetup(t)
	for name, change := range map[string]func(*subscriptionRequest){
		"empty name":     func(r *subscriptionRequest) { r.Name = " " },
		"multiline name": func(r *subscriptionRequest) { r.Name = "a\nb" },
		"long name":      func(r *subscriptionRequest) { r.Name = strings.Repeat("名", 121) },
		"script url":     func(r *subscriptionRequest) { r.URL = "javascript:alert(1)" },
		"credentials":    func(r *subscriptionRequest) { r.URL = "https://user:secret@example.com" },
		"long url":       func(r *subscriptionRequest) { r.URL = "https://example.com/" + strings.Repeat("x", 2048) },
		"missing host":   func(r *subscriptionRequest) { r.URL = "https:///abc" },
		"negative days":  func(r *subscriptionRequest) { r.Days = -1 },
		"zero days":      func(r *subscriptionRequest) { r.Days = 0 },
		"many days":      func(r *subscriptionRequest) { r.Days = 36501 },
		"unknown mode":   func(r *subscriptionRequest) { r.Mode = "unknown" },
		"zero period":    func(r *subscriptionRequest) { r.Mode = "period" },
		"unknown unit":   func(r *subscriptionRequest) { r.Mode = "period"; r.PeriodCount = 1; r.PeriodUnit = "week" },
		"huge year":      func(r *subscriptionRequest) { r.Mode = "period"; r.PeriodCount = 101; r.PeriodUnit = "year" },
		"bad date":       func(r *subscriptionRequest) { r.DueAt = "tomorrow" },
		"missing id":     func(r *subscriptionRequest) { r.ID = "missing" },
	} {
		t.Run(name, func(t *testing.T) {
			req := subscriptionTestRequest(7)
			change(&req)
			_, err := updateSubscription(req, now)
			var inputErr subscriptionInputError
			if !errors.As(err, &inputErr) {
				t.Fatalf("expected input error, got %v", err)
			}
		})
	}
	subscriptionMustUpdate(t, subscriptionTestRequest(7), now)
	if err := os.WriteFile(subscriptionStoragePath(), []byte("broken json"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := updateSubscription(subscriptionTestRequest(30), now); err == nil {
		t.Fatal("must not overwrite corrupt storage")
	}
	data, _ := os.ReadFile(subscriptionStoragePath())
	if string(data) != "broken json" {
		t.Fatal("corrupt original overwritten")
	}
}

func TestSubscriptionReminderWindowDailyDedupAndRestart(t *testing.T) {
	now := subscriptionTestSetup(t)
	item := subscriptionMustUpdate(t, subscriptionTestRequest(8), now)[0]
	calls := 0
	send := func(_ context.Context, text string) error {
		calls++
		if !strings.Contains(text, item.Name) || !strings.Contains(text, item.URL) {
			t.Fatal("message missing subscription details")
		}
		return nil
	}
	check := func(at time.Time) {
		t.Helper()
		if err := checkSubscriptionReminders(context.Background(), at, send); err != nil {
			t.Fatal(err)
		}
	}
	check(now)
	if calls != 0 {
		t.Fatal("sent before seven-day window")
	}
	check(now.AddDate(0, 0, 1))
	if calls != 1 {
		t.Fatal("did not send at seven-day boundary")
	}
	check(now.AddDate(0, 0, 1).Add(time.Hour))
	if calls != 1 {
		t.Fatal("same-day duplicate")
	}
	// The checker has no in-memory successful-send state. Reloading the file is
	// exactly the restart path; another check must still be deduplicated.
	loaded, _ := getSubscriptions()
	if loaded[0].LastReminderDay != "2026-10-09" {
		t.Fatal("success not durable")
	}
	check(now.AddDate(0, 0, 2))
	if calls != 2 {
		t.Fatal("next-day reminder missing")
	}
	check(now.AddDate(0, 0, 9))
	if calls != 3 {
		t.Fatal("overdue reminder missing")
	}
	check(now.AddDate(0, 0, 9))
	if calls != 3 {
		t.Fatal("overdue duplicate")
	}
}

func TestSubscriptionReminderFailureRetryAndDisable(t *testing.T) {
	now := subscriptionTestSetup(t)
	req := subscriptionTestRequest(1)
	item := subscriptionMustUpdate(t, req, now)[0]
	calls := 0
	send := func(_ context.Context, _ string) error {
		calls++
		if calls == 1 {
			return errors.New("secret transport details")
		}
		return nil
	}
	if err := checkSubscriptionReminders(context.Background(), now, send); err != nil {
		t.Fatal(err)
	}
	loaded, _ := getSubscriptions()
	if loaded[0].LastReminderDay != "" || loaded[0].LastError == "" || strings.Contains(loaded[0].LastError, "secret") {
		t.Fatal("failure should be retryable and sanitized")
	}
	if err := checkSubscriptionReminders(context.Background(), now.Add(time.Minute), send); err != nil {
		t.Fatal(err)
	}
	loaded, _ = getSubscriptions()
	if calls != 2 || loaded[0].LastReminderDay == "" || loaded[0].LastError != "" {
		t.Fatal("retry did not succeed")
	}
	req.ID, req.Enabled, req.DueAt = item.ID, false, item.DueAt.Format(time.RFC3339)
	subscriptionMustUpdate(t, req, now)
	if err := checkSubscriptionReminders(context.Background(), now.AddDate(0, 0, 1), send); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("disabled subscription sent")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := checkSubscriptionReminders(ctx, now, send); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled check: %v", err)
	}
}

func TestSubscriptionEditDuringSendDoesNotCorruptNewExpiry(t *testing.T) {
	now := subscriptionTestSetup(t)
	item := subscriptionMustUpdate(t, subscriptionTestRequest(1), now)[0]
	err := checkSubscriptionReminders(context.Background(), now, func(_ context.Context, _ string) error {
		req := subscriptionTestRequest(60)
		req.ID = item.ID
		subscriptionMustUpdate(t, req, now)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	loaded, _ := getSubscriptions()
	if loaded[0].LastReminderDay != "" || !loaded[0].DueAt.Equal(now.AddDate(0, 0, 60)) {
		t.Fatal("old send overwrote edited expiry")
	}
	// A metadata-only edit must keep the successful day marker.
	req := subscriptionTestRequest(1)
	req.ID = item.ID
	subscriptionMustUpdate(t, req, now)
	err = checkSubscriptionReminders(context.Background(), now, func(_ context.Context, _ string) error {
		req.Name, req.DueAt = "改名", now.AddDate(0, 0, 1).UTC().Format(time.RFC3339)
		subscriptionMustUpdate(t, req, now)
		return nil
	})
	loaded, _ = getSubscriptions()
	if err != nil || loaded[0].Name != "改名" || loaded[0].LastReminderDay == "" {
		t.Fatal("metadata edit lost successful dedup")
	}
}

func TestSubscriptionCalendarDaysAcrossDST(t *testing.T) {
	zone, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 3, 7, 12, 0, 0, 0, zone)
	due := now.AddDate(0, 0, 7)
	if subscriptionDaysRemaining(due, now) != 7 {
		t.Fatal("DST should not affect calendar days")
	}
	// Due is stored in UTC but notification day follows the controller location.
	now = time.Date(2026, 10, 8, 23, 30, 0, 0, time.FixedZone("SGT", 8*3600))
	if subscriptionDaysRemaining(now.AddDate(0, 0, 7).UTC(), now) != 7 {
		t.Fatal("stored UTC changed reminder boundary")
	}
}

func TestSubscriptionCheckResumesAfterTimeout(t *testing.T) {
	now := subscriptionTestSetup(t)
	subscriptionMustUpdate(t, subscriptionTestRequest(1), now)
	req := subscriptionTestRequest(1)
	req.Name = "下一条"
	subscriptionMustUpdate(t, req, now)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := checkSubscriptionReminders(ctx, now, func(context.Context, string) error {
		cancel()
		return context.Canceled
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected timeout simulation: %v", err)
	}
	texts := []string{}
	err = checkSubscriptionReminders(context.Background(), now, func(_ context.Context, text string) error { texts = append(texts, text); return nil })
	if err != nil || len(texts) != 2 || !strings.Contains(texts[0], "下一条") {
		t.Fatalf("a slow failure starved later subscriptions: %v %v", texts, err)
	}
}

func TestSubscriptionSchedulerUsesExistingNotifyBot(t *testing.T) {
	subscriptionTestSetup(t)
	now := time.Now()
	subscriptionMustUpdate(t, subscriptionTestRequest(1), now)
	oldScheduler, oldStore, oldSend := controllerScheduler, TGAssistantStore, tgNotifySendFunc
	controllerScheduler = &controllerSchedulerEngine{jobs: map[string]controllerScheduledJob{}, wakeCh: make(chan struct{}, 1)}
	TGAssistantStore = nil
	t.Cleanup(func() { controllerScheduler, TGAssistantStore, tgNotifySendFunc = oldScheduler, oldStore, oldSend })
	calls := 0
	tgNotifySendFunc = func(_ context.Context, key string, chat int64, text string) (int, string, error) {
		calls++
		if key != "test-bot" || chat != 123 || !strings.Contains(text, "订阅到期提醒") {
			t.Fatal("wrong TG destination or message")
		}
		return 1, text, nil
	}
	initSubscriptionReminderEngine()
	job, ok := controllerScheduler.jobs["subscriptions.reminders"]
	if !ok || !job.runAt.After(now) {
		t.Fatal("startup check not scheduled")
	}
	job.fn(context.Background())
	if calls != 0 {
		t.Fatal("sent without configured bot")
	}
	if _, ok := controllerScheduler.jobs["subscriptions.reminders"]; !ok {
		t.Fatal("unconfigured bot stopped periodic checks")
	}
	TGAssistantStore = &tgAssistantStore{
		path: filepath.Join(t.TempDir(), "tg.json"),
		data: tgAssistantStoreData{
			Accounts: []tgAssistantAccountRecord{{ID: "notify-test", Phone: "+100", Authorized: true, SelfUserID: 123, BotAPIKey: "test-bot"}},
			Notify:   tgAssistantNotifySettings{NotifyAccountID: "notify-test", EnableRenewal: false},
		},
	}
	job.fn(context.Background())
	job.fn(context.Background())
	if calls != 1 {
		t.Fatalf("subscription notification depends on node renewal toggle or duplicated: %d", calls)
	}
}

func TestSubscriptionManagementHTTP(t *testing.T) {
	subscriptionTestSetup(t)
	oldAuth, oldTG := mngAuthInstance, TGAssistantStore
	mngAuthInstance = &mngAuthManager{sessions: map[string]mngSessionState{"test-session": {Username: "test", ExpiresAt: time.Now().Add(time.Hour)}}}
	TGAssistantStore = nil
	t.Cleanup(func() { mngAuthInstance, TGAssistantStore = oldAuth, oldTG })
	mux := NewMux()
	for _, path := range []string{"/mng/subscriptions", "/mng/api/subscriptions"} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		want := http.StatusUnauthorized
		if path == "/mng/subscriptions" {
			want = http.StatusFound
		}
		if w.Code != want {
			t.Fatalf("unauthed %s: %d", path, w.Code)
		}
	}
	request := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: mngSessionCookieName, Value: "test-session"})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	w := request("GET", "/mng/subscriptions", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "订阅提醒") || strings.Contains(w.Body.String(), mngQuickNavPlaceholder) {
		t.Fatal("page not rendered")
	}
	body, _ := json.Marshal(subscriptionTestRequest(30))
	w = request("POST", "/mng/api/subscriptions", string(body))
	if w.Code != 200 {
		t.Fatalf("save HTTP: %d %s", w.Code, w.Body.String())
	}
	var view struct {
		Items   []subscriptionReminder `json:"items"`
		TGReady bool                   `json:"tg_ready"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil || len(view.Items) != 1 || view.TGReady {
		t.Fatalf("unexpected view: %+v %v", view, err)
	}
	w = request("GET", "/mng/api/subscriptions", "")
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("GET failed")
	}
	for _, body := range []string{"{", `{"action":"save","name":"bad","url":"javascript:evil","mode":"countdown","days":1}`} {
		if w := request("POST", "/mng/api/subscriptions", body); w.Code != 400 {
			t.Fatalf("bad input accepted: %d", w.Code)
		}
	}
	if w := request("POST", "/mng/api/subscriptions", strings.Repeat("x", 20<<10)); w.Code != 400 {
		t.Fatal("oversized body accepted")
	}
	if w := request("DELETE", "/mng/api/subscriptions", ""); w.Code != 405 {
		t.Fatal("unsupported method accepted")
	}
	if err := os.WriteFile(subscriptionStoragePath(), []byte("bad"), 0600); err != nil {
		t.Fatal(err)
	}
	if w := request("GET", "/mng/api/subscriptions", ""); w.Code != 500 {
		t.Fatal("storage error hidden")
	}
}
