package core

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	tgAssistantScheduleSyncInterval = 5 * time.Second
	tgAssistantScheduleJobTimeout   = 90 * time.Second
	taskQueueDir                    = "./temp/task"
)

type tgAssistantScheduleTaskSnapshot struct {
	AccountID string
	TaskID    string
	TaskType  string
	Enabled   bool
	Target    string
	SendAt    string
	Message   string
	DelayMin  int
	DelayMax  int
	UpdatedAt string
}

type tgAssistantSchedulePlanState struct {
	Fingerprint string
	NextRunAt   time.Time
	TimeoutAt   time.Time
	DelaySec    int
	Running     bool
	LastError   string
}

type tgAssistantPendingTaskRecord struct {
	JobKey      string `json:"job_key"`
	AccountID   string `json:"account_id"`
	TaskID      string `json:"task_id"`
	Fingerprint string `json:"fingerprint"`
	NextRunAt   string `json:"next_run_at"`
	TimeoutAt   string `json:"timeout_at"`
	DelaySec    int    `json:"delay_sec"`
	UpdatedAt   string `json:"updated_at"`
}

var tgAssistantScheduleEngine = struct {
	mu      sync.Mutex
	started bool
	plans   map[string]tgAssistantSchedulePlanState
}{
	plans: map[string]tgAssistantSchedulePlanState{},
}

func initTGAssistantScheduleEngine() {
	tgAssistantScheduleEngine.mu.Lock()
	if tgAssistantScheduleEngine.started {
		tgAssistantScheduleEngine.mu.Unlock()
		return
	}
	tgAssistantScheduleEngine.started = true
	tgAssistantScheduleEngine.mu.Unlock()

	go runTGAssistantScheduleEngine()
	log.Println("tg assistant schedule engine started")
}

func runTGAssistantScheduleEngine() {
	syncTGAssistantScheduleJobs()
	ticker := time.NewTicker(tgAssistantScheduleSyncInterval)
	defer ticker.Stop()

	for range ticker.C {
		syncTGAssistantScheduleJobs()
	}
}

func syncTGAssistantScheduleJobs() {
	tasks := collectTGAssistantEnabledScheduledTasks()
	now := time.Now()

	type pendingSchedule struct {
		JobKey    string
		AccountID string
		TaskID    string
		RunAt     time.Time
		DelaySec  int
	}
	toSchedule := make([]pendingSchedule, 0, len(tasks))

	tgAssistantScheduleEngine.mu.Lock()
	for jobKey := range tgAssistantScheduleEngine.plans {
		if _, ok := tasks[jobKey]; ok {
			continue
		}
		delete(tgAssistantScheduleEngine.plans, jobKey)
		cancelGlobalTask(jobKey)
		removeTGAssistantPendingTask(jobKey)
	}

	for jobKey, task := range tasks {
		fingerprint := buildTGAssistantScheduleFingerprint(task)
		state, ok := tgAssistantScheduleEngine.plans[jobKey]
		if !ok {
			if persisted, hasPersisted := loadTGAssistantPendingTask(jobKey); hasPersisted && persisted.Fingerprint == fingerprint {
				loadedState, loadOK := buildTGAssistantScheduleStateFromPending(persisted)
				if loadOK {
					state = loadedState
					ok = true
				}
			}
		}
		if !ok {
			state = tgAssistantSchedulePlanState{}
		}

		if state.Fingerprint != fingerprint {
			nextState, err := computeTGAssistantNextScheduleState(task, fingerprint, now)
			if err != nil {
				errText := err.Error()
				if state.LastError != errText {
					log.Printf("tg schedule parse failed: account=%s task=%s send_at=%q err=%v", task.AccountID, task.TaskID, task.SendAt, err)
					appendTGAssistantHistory("schedule.parse", task.AccountID, false, fmt.Sprintf("task_id=%s err=%s", task.TaskID, errText))
					appendTGAssistantTaskHistory("schedule.parse", task.AccountID, task.TaskID, false, errText)
				}
				state = tgAssistantSchedulePlanState{
					Fingerprint: fingerprint,
					LastError:   errText,
				}
				tgAssistantScheduleEngine.plans[jobKey] = state
				cancelGlobalTask(jobKey)
				removeTGAssistantPendingTask(jobKey)
				continue
			}
			state = nextState
			tgAssistantScheduleEngine.plans[jobKey] = state
			saveTGAssistantPendingTask(task, jobKey, state)
			toSchedule = append(toSchedule, pendingSchedule{
				JobKey:    jobKey,
				AccountID: task.AccountID,
				TaskID:    task.TaskID,
				RunAt:     state.NextRunAt,
				DelaySec:  state.DelaySec,
			})
			continue
		}

		if state.Running {
			tgAssistantScheduleEngine.plans[jobKey] = state
			continue
		}

		// Time or timeout reached: execute immediately.
		if !state.NextRunAt.IsZero() && (now.After(state.NextRunAt) || now.Equal(state.NextRunAt) || (!state.TimeoutAt.IsZero() && (now.After(state.TimeoutAt) || now.Equal(state.TimeoutAt)))) {
			state.NextRunAt = now
			tgAssistantScheduleEngine.plans[jobKey] = state
			saveTGAssistantPendingTask(task, jobKey, state)
			toSchedule = append(toSchedule, pendingSchedule{
				JobKey:    jobKey,
				AccountID: task.AccountID,
				TaskID:    task.TaskID,
				RunAt:     state.NextRunAt,
				DelaySec:  state.DelaySec,
			})
			continue
		}

		if state.NextRunAt.IsZero() {
			nextState, err := computeTGAssistantNextScheduleState(task, fingerprint, now)
			if err != nil {
				state.LastError = err.Error()
				tgAssistantScheduleEngine.plans[jobKey] = state
				cancelGlobalTask(jobKey)
				removeTGAssistantPendingTask(jobKey)
				continue
			}
			state = nextState
			tgAssistantScheduleEngine.plans[jobKey] = state
			saveTGAssistantPendingTask(task, jobKey, state)
		}

		// Ensure pending plan survives process restarts and is actively scheduled.
		toSchedule = append(toSchedule, pendingSchedule{
			JobKey:    jobKey,
			AccountID: task.AccountID,
			TaskID:    task.TaskID,
			RunAt:     state.NextRunAt,
			DelaySec:  state.DelaySec,
		})
	}
	tgAssistantScheduleEngine.mu.Unlock()

	for _, item := range toSchedule {
		scheduled := item
		scheduleGlobalTask(scheduled.JobKey, scheduled.RunAt, tgAssistantScheduleJobTimeout, func(ctx context.Context) {
			runTGAssistantScheduledTask(ctx, scheduled.JobKey, scheduled.AccountID, scheduled.TaskID, scheduled.RunAt, scheduled.DelaySec)
		})
	}
}

func runTGAssistantScheduledTask(ctx context.Context, jobKey, accountID, taskID string, plannedAt time.Time, delaySec int) {
	if !tgAssistantMarkScheduleTaskRunning(jobKey, plannedAt) {
		return
	}
	defer tgAssistantMarkScheduleTaskFinished(jobKey, plannedAt)

	_, _ = executeTGAssistantScheduleSendTask(ctx, accountID, taskID, "schedule.run", delaySec)
}

func tgAssistantMarkScheduleTaskRunning(jobKey string, plannedAt time.Time) bool {
	tgAssistantScheduleEngine.mu.Lock()
	defer tgAssistantScheduleEngine.mu.Unlock()

	state, ok := tgAssistantScheduleEngine.plans[jobKey]
	if !ok {
		return false
	}
	if !state.NextRunAt.Equal(plannedAt) || state.Running {
		return false
	}
	state.Running = true
	tgAssistantScheduleEngine.plans[jobKey] = state
	return true
}

func tgAssistantMarkScheduleTaskFinished(jobKey string, plannedAt time.Time) {
	tgAssistantScheduleEngine.mu.Lock()
	defer tgAssistantScheduleEngine.mu.Unlock()

	state, ok := tgAssistantScheduleEngine.plans[jobKey]
	if !ok {
		return
	}
	state.Running = false
	if state.NextRunAt.Equal(plannedAt) {
		state.NextRunAt = time.Time{}
		state.TimeoutAt = time.Time{}
	}
	tgAssistantScheduleEngine.plans[jobKey] = state
	removeTGAssistantPendingTask(jobKey)
}

func computeTGAssistantNextScheduleState(task tgAssistantScheduleTaskSnapshot, fingerprint string, now time.Time) (tgAssistantSchedulePlanState, error) {
	baseRunAt, err := nextTGAssistantScheduleTime(task.SendAt, now)
	if err != nil {
		return tgAssistantSchedulePlanState{}, err
	}
	delaySec := randomTGAssistantScheduleDelaySeconds(task.DelayMin, task.DelayMax)
	nextRunAt := baseRunAt.Add(time.Duration(delaySec) * time.Second)
	timeoutAt := baseRunAt.Add(time.Duration(maxInt(task.DelayMax, delaySec)) * time.Second)
	if timeoutAt.Before(nextRunAt) {
		timeoutAt = nextRunAt
	}
	return tgAssistantSchedulePlanState{
		Fingerprint: fingerprint,
		NextRunAt:   nextRunAt,
		TimeoutAt:   timeoutAt,
		DelaySec:    delaySec,
		Running:     false,
		LastError:   "",
	}, nil
}

func buildTGAssistantScheduleStateFromPending(record tgAssistantPendingTaskRecord) (tgAssistantSchedulePlanState, bool) {
	nextRunAt, err := time.Parse(time.RFC3339, strings.TrimSpace(record.NextRunAt))
	if err != nil {
		return tgAssistantSchedulePlanState{}, false
	}
	timeoutAt := time.Time{}
	if strings.TrimSpace(record.TimeoutAt) != "" {
		if parsed, parseErr := time.Parse(time.RFC3339, strings.TrimSpace(record.TimeoutAt)); parseErr == nil {
			timeoutAt = parsed
		}
	}
	return tgAssistantSchedulePlanState{
		Fingerprint: strings.TrimSpace(record.Fingerprint),
		NextRunAt:   nextRunAt,
		TimeoutAt:   timeoutAt,
		DelaySec:    record.DelaySec,
		Running:     false,
		LastError:   "",
	}, true
}

func collectTGAssistantEnabledScheduledTasks() map[string]tgAssistantScheduleTaskSnapshot {
	result := map[string]tgAssistantScheduleTaskSnapshot{}
	if TGAssistantStore == nil {
		return result
	}

	TGAssistantStore.mu.RLock()
	accounts := loadTGAssistantAccountsLocked()
	TGAssistantStore.mu.RUnlock()
	for _, account := range accounts {
		accountID := strings.TrimSpace(account.ID)
		if accountID == "" {
			continue
		}
		for _, task := range account.Schedules {
			if !task.Enabled || task.TaskType != tgTaskTypeScheduledSend {
				continue
			}
			taskID := strings.TrimSpace(task.ID)
			if taskID == "" {
				continue
			}
			snapshot := tgAssistantScheduleTaskSnapshot{
				AccountID: accountID,
				TaskID:    taskID,
				TaskType:  task.TaskType,
				Enabled:   task.Enabled,
				Target:    strings.TrimSpace(task.Target),
				SendAt:    strings.TrimSpace(task.SendAt),
				Message:   strings.TrimSpace(task.Message),
				DelayMin:  task.DelayMin,
				DelayMax:  task.DelayMax,
				UpdatedAt: strings.TrimSpace(task.UpdatedAt),
			}
			if snapshot.Target == "" || snapshot.SendAt == "" || snapshot.Message == "" {
				continue
			}
			result[tgAssistantScheduleJobKey(accountID, taskID)] = snapshot
		}
	}
	return result
}

func tgAssistantScheduleJobKey(accountID, taskID string) string {
	return "tg.schedule." + strings.TrimSpace(accountID) + "." + strings.TrimSpace(taskID)
}

func buildTGAssistantScheduleFingerprint(task tgAssistantScheduleTaskSnapshot) string {
	return strings.Join([]string{
		task.AccountID,
		task.TaskID,
		task.TaskType,
		strconv.FormatBool(task.Enabled),
		task.Target,
		task.SendAt,
		task.Message,
		strconv.Itoa(task.DelayMin),
		strconv.Itoa(task.DelayMax),
		task.UpdatedAt,
	}, "|")
}

func nextTGAssistantScheduleTime(sendAt string, from time.Time) (time.Time, error) {
	expr := strings.TrimSpace(sendAt)
	if expr == "" {
		return time.Time{}, fmt.Errorf("send_at is empty")
	}

	if strings.HasPrefix(expr, "每天") {
		timePart := strings.TrimSpace(strings.TrimPrefix(expr, "每天"))
		hour, minute, err := parseTGAssistantScheduleClock(timePart)
		if err != nil {
			return time.Time{}, err
		}
		next := time.Date(from.Year(), from.Month(), from.Day(), hour, minute, 0, 0, from.Location())
		if !next.After(from) {
			next = next.AddDate(0, 0, 1)
		}
		return next, nil
	}

	if strings.HasPrefix(expr, "每周") {
		rest := strings.TrimSpace(strings.TrimPrefix(expr, "每周"))
		runes := []rune(rest)
		if len(runes) < 2 {
			return time.Time{}, fmt.Errorf("invalid weekly send_at format: %s", expr)
		}
		targetWeekday, ok := parseTGAssistantChineseWeekday(runes[0])
		if !ok {
			return time.Time{}, fmt.Errorf("invalid weekday in send_at: %s", expr)
		}
		timePart := strings.TrimSpace(string(runes[1:]))
		hour, minute, err := parseTGAssistantScheduleClock(timePart)
		if err != nil {
			return time.Time{}, err
		}

		days := (int(targetWeekday) - int(from.Weekday()) + 7) % 7
		next := time.Date(from.Year(), from.Month(), from.Day(), hour, minute, 0, 0, from.Location()).AddDate(0, 0, days)
		if !next.After(from) {
			next = next.AddDate(0, 0, 7)
		}
		return next, nil
	}

	return time.Time{}, fmt.Errorf("unsupported send_at format: %s", expr)
}

func parseTGAssistantScheduleClock(value string) (int, int, error) {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid time format: %s", value)
	}
	hour, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, 0, fmt.Errorf("invalid hour in time format: %s", value)
	}
	minute, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return 0, 0, fmt.Errorf("invalid minute in time format: %s", value)
	}
	if hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return 0, 0, fmt.Errorf("time out of range: %s", value)
	}
	return hour, minute, nil
}

func parseTGAssistantChineseWeekday(value rune) (time.Weekday, bool) {
	switch value {
	case '日', '天':
		return time.Sunday, true
	case '一':
		return time.Monday, true
	case '二':
		return time.Tuesday, true
	case '三':
		return time.Wednesday, true
	case '四':
		return time.Thursday, true
	case '五':
		return time.Friday, true
	case '六':
		return time.Saturday, true
	default:
		return time.Sunday, false
	}
}

func tgAssistantPendingTaskDirPath() string {
	return filepath.Clean(taskQueueDir)
}

func tgAssistantPendingTaskPath(jobKey string) string {
	safeName := sanitizeTGAssistantTaskQueueName(jobKey)
	if safeName == "" {
		safeName = "unknown"
	}
	return filepath.Join(tgAssistantPendingTaskDirPath(), safeName+".json")
}

func sanitizeTGAssistantTaskQueueName(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	var builder strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			builder.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			builder.WriteRune(r)
		case r >= '0' && r <= '9':
			builder.WriteRune(r)
		default:
			builder.WriteRune('_')
		}
	}
	return builder.String()
}

func saveTGAssistantPendingTask(task tgAssistantScheduleTaskSnapshot, jobKey string, state tgAssistantSchedulePlanState) {
	if state.NextRunAt.IsZero() {
		return
	}
	if err := os.MkdirAll(tgAssistantPendingTaskDirPath(), 0o755); err != nil {
		log.Printf("task queue mkdir failed: %v", err)
		return
	}
	record := tgAssistantPendingTaskRecord{
		JobKey:      jobKey,
		AccountID:   task.AccountID,
		TaskID:      task.TaskID,
		Fingerprint: state.Fingerprint,
		NextRunAt:   state.NextRunAt.UTC().Format(time.RFC3339),
		TimeoutAt:   state.TimeoutAt.UTC().Format(time.RFC3339),
		DelaySec:    state.DelaySec,
		UpdatedAt:   time.Now().UTC().Format(time.RFC3339),
	}
	content, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		log.Printf("task queue marshal failed: %v", err)
		return
	}
	path := tgAssistantPendingTaskPath(jobKey)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, content, 0o644); err != nil {
		log.Printf("task queue write tmp failed: %v", err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		log.Printf("task queue rename failed: %v", err)
	}
}

func loadTGAssistantPendingTask(jobKey string) (tgAssistantPendingTaskRecord, bool) {
	path := tgAssistantPendingTaskPath(jobKey)
	content, err := os.ReadFile(path)
	if err != nil {
		return tgAssistantPendingTaskRecord{}, false
	}
	var record tgAssistantPendingTaskRecord
	if err := json.Unmarshal(content, &record); err != nil {
		return tgAssistantPendingTaskRecord{}, false
	}
	if strings.TrimSpace(record.JobKey) == "" {
		record.JobKey = jobKey
	}
	return record, true
}

func removeTGAssistantPendingTask(jobKey string) {
	path := tgAssistantPendingTaskPath(jobKey)
	_ = os.Remove(path)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
