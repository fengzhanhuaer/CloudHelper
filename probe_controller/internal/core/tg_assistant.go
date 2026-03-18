package core

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	tgauth "github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"
)

const (
	tgAssistantTempDir        = "./tg"
	tgAssistantStoreFile      = "tg.json"
	tgAssistantSessionDirName = "tg_sessions"
	tgAssistantTargetsDirName = "targets"
	tgAssistantHistoryFile    = "history.jsonl"
	tgAssistantLoginCodeTTL   = 10 * time.Minute
	tgTaskTypeScheduledSend   = "scheduled_send"
)

var (
	errTGAssistantPasswordRequired = errors.New("password is required for 2FA account")
)

type tgAssistantAccountRecord struct {
	ID              string                      `json:"id"`
	Label           string                      `json:"label"`
	Phone           string                      `json:"phone"`
	Authorized      bool                        `json:"authorized"`
	LastError       string                      `json:"last_error"`
	CreatedAt       string                      `json:"created_at"`
	UpdatedAt       string                      `json:"updated_at"`
	LastLoginAt     string                      `json:"last_login_at"`
	SelfUserID      int64                       `json:"self_user_id"`
	SelfUsername    string                      `json:"self_username"`
	SelfDisplayName string                      `json:"self_display_name"`
	SelfPhone       string                      `json:"self_phone"`
	Schedules       []tgAssistantScheduleRecord `json:"schedules,omitempty"`
}

type tgAssistantAccount struct {
	ID              string                `json:"id"`
	Label           string                `json:"label"`
	Phone           string                `json:"phone"`
	APIID           int                   `json:"api_id"`
	Authorized      bool                  `json:"authorized"`
	PendingCode     bool                  `json:"pending_code"`
	LastError       string                `json:"last_error"`
	CreatedAt       string                `json:"created_at"`
	UpdatedAt       string                `json:"updated_at"`
	LastLoginAt     string                `json:"last_login_at"`
	SelfUserID      int64                 `json:"self_user_id"`
	SelfUsername    string                `json:"self_username"`
	SelfDisplayName string                `json:"self_display_name"`
	SelfPhone       string                `json:"self_phone"`
	Schedules       []tgAssistantSchedule `json:"schedules"`
}

type tgAssistantScheduleRecord struct {
	ID        string `json:"id"`
	TaskType  string `json:"task_type"`
	Enabled   bool   `json:"enabled"`
	Target    string `json:"target"`
	SendAt    string `json:"send_at"`
	Message   string `json:"message"`
	DelayMin  int    `json:"delay_min_sec"`
	DelayMax  int    `json:"delay_max_sec"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type tgAssistantSchedule struct {
	ID        string `json:"id"`
	TaskType  string `json:"task_type"`
	Enabled   bool   `json:"enabled"`
	Target    string `json:"target"`
	SendAt    string `json:"send_at"`
	Message   string `json:"message"`
	DelayMin  int    `json:"delay_min_sec"`
	DelayMax  int    `json:"delay_max_sec"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type tgAssistantHistoryRecord struct {
	Time      string `json:"time"`
	Action    string `json:"action"`
	AccountID string `json:"account_id,omitempty"`
	Success   bool   `json:"success"`
	Message   string `json:"message,omitempty"`
}

type tgAssistantTarget struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Username string `json:"username,omitempty"`
	Type     string `json:"type,omitempty"`
}

type tgAssistantLoginChallenge struct {
	PhoneCodeHash string
	ExpiresAt     time.Time
}

type tgAssistantState struct {
	mu         sync.Mutex
	challenges map[string]tgAssistantLoginChallenge
}

type tgAssistantStore struct {
	mu   sync.RWMutex
	path string
	data tgAssistantStoreData
}

type tgAssistantStoreData struct {
	APIID    int                        `json:"api_id"`
	APIHash  string                     `json:"api_hash"`
	Accounts []tgAssistantAccountRecord `json:"accounts"`
}

var tgState = tgAssistantState{
	challenges: map[string]tgAssistantLoginChallenge{},
}

var TGAssistantStore *tgAssistantStore

type tgAssistantAddAccountRequest struct {
	Label string `json:"label"`
	Phone string `json:"phone"`
}

type tgAssistantAccountIDRequest struct {
	AccountID string `json:"account_id"`
}

type tgAssistantSignInRequest struct {
	AccountID string `json:"account_id"`
	Code      string `json:"code"`
	Password  string `json:"password"`
}

type tgAssistantAPIKeyRequest struct {
	APIID   int    `json:"api_id"`
	APIHash string `json:"api_hash"`
}

type tgAssistantAPIKey struct {
	APIID      int    `json:"api_id"`
	APIHash    string `json:"api_hash"`
	Configured bool   `json:"configured"`
}

type tgAssistantScheduleAddRequest struct {
	AccountID string `json:"account_id"`
	TaskType  string `json:"task_type"`
	Enabled   bool   `json:"enabled"`
	Target    string `json:"target"`
	SendAt    string `json:"send_at"`
	Message   string `json:"message"`
	DelayMin  int    `json:"delay_min_sec"`
	DelayMax  int    `json:"delay_max_sec"`
}

type tgAssistantScheduleRemoveRequest struct {
	AccountID string `json:"account_id"`
	TaskID    string `json:"task_id"`
}

type tgAssistantScheduleSetEnabledRequest struct {
	AccountID string `json:"account_id"`
	TaskID    string `json:"task_id"`
	Enabled   bool   `json:"enabled"`
}

type tgAssistantScheduleSendNowRequest struct {
	AccountID string `json:"account_id"`
	TaskID    string `json:"task_id"`
}

type tgAssistantScheduleSendNowResult struct {
	AccountID string `json:"account_id"`
	TaskID    string `json:"task_id"`
	Target    string `json:"target"`
	DelaySec  int    `json:"delay_sec"`
	SentAt    string `json:"sent_at"`
}

func initTGAssistantStore() {
	tempDir := tgAssistantTempDirPath()
	if err := os.MkdirAll(tempDir, 0o755); err != nil {
		log.Fatalf("failed to create tg temporary directory: %v", err)
	}
	migrateTGAssistantSessionFilesToDataDir()

	storePath := filepath.Join(dataDir, tgAssistantStoreFile)
	TGAssistantStore = &tgAssistantStore{
		path: storePath,
		data: tgAssistantStoreData{
			Accounts: []tgAssistantAccountRecord{},
		},
	}

	if _, err := os.Stat(storePath); err == nil {
		content, readErr := os.ReadFile(storePath)
		if readErr != nil {
			log.Fatalf("failed to read tg assistant store file: %v", readErr)
		}
		if len(strings.TrimSpace(string(content))) > 0 {
			var raw tgAssistantStoreData
			if unmarshalErr := json.Unmarshal(content, &raw); unmarshalErr != nil {
				log.Fatalf("failed to parse tg assistant store file: %v", unmarshalErr)
			}
			raw.APIHash = strings.TrimSpace(raw.APIHash)
			TGAssistantStore.data.Accounts = normalizeTGAssistantAccountRecords(raw.Accounts)
			TGAssistantStore.data.APIID = raw.APIID
			TGAssistantStore.data.APIHash = raw.APIHash
		}
	} else if os.IsNotExist(err) {
		if saveErr := TGAssistantStore.Save(); saveErr != nil {
			log.Fatalf("failed to initialize tg assistant store file: %v", saveErr)
		}
	} else {
		log.Fatalf("failed to check tg assistant store file: %v", err)
	}

	log.Println("TG assistant datastore initialized at", storePath, "session_dir=", filepath.Join(dataDir, tgAssistantSessionDirName), "history=", tgAssistantHistoryPath())
}

func getTGAssistantAPIKey() tgAssistantAPIKey {
	if TGAssistantStore == nil {
		return tgAssistantAPIKey{}
	}

	TGAssistantStore.mu.RLock()
	apiID, apiHash := loadTGAssistantAPIKeyLocked()
	TGAssistantStore.mu.RUnlock()
	return tgAssistantAPIKey{
		APIID:      apiID,
		APIHash:    apiHash,
		Configured: isTGAssistantAPIKeyConfigured(apiID, apiHash),
	}
}

func setTGAssistantAPIKey(req tgAssistantAPIKeyRequest) (tgAssistantAPIKey, error) {
	if TGAssistantStore == nil {
		return tgAssistantAPIKey{}, errors.New("tg assistant datastore is not initialized")
	}
	apiID := req.APIID
	apiHash := strings.TrimSpace(req.APIHash)
	if apiID <= 0 {
		return tgAssistantAPIKey{}, errors.New("api_id must be a positive integer")
	}
	if apiHash == "" {
		return tgAssistantAPIKey{}, errors.New("api_hash is required")
	}

	TGAssistantStore.mu.Lock()
	TGAssistantStore.data.APIID = apiID
	TGAssistantStore.data.APIHash = apiHash
	TGAssistantStore.mu.Unlock()

	if err := TGAssistantStore.Save(); err != nil {
		return tgAssistantAPIKey{}, err
	}
	appendTGAssistantHistory("api.set", "", true, fmt.Sprintf("api_id=%d", apiID))

	return tgAssistantAPIKey{
		APIID:      apiID,
		APIHash:    apiHash,
		Configured: true,
	}, nil
}

func (s *tgAssistantStore) Save() error {
	s.mu.RLock()
	content, err := json.MarshalIndent(s.data, "", "  ")
	s.mu.RUnlock()
	if err != nil {
		return err
	}
	if err := os.WriteFile(s.path, content, 0o644); err != nil {
		return err
	}
	triggerAutoBackupControllerDataAsync("tg_store_save")
	return nil
}

func listTGAssistantAccounts() []tgAssistantAccount {
	if TGAssistantStore == nil {
		return []tgAssistantAccount{}
	}

	TGAssistantStore.mu.RLock()
	records := loadTGAssistantAccountsLocked()
	apiID, _ := loadTGAssistantAPIKeyLocked()
	TGAssistantStore.mu.RUnlock()

	return buildTGAssistantAccountViews(records, apiID)
}

func refreshTGAssistantAccounts() ([]tgAssistantAccount, error) {
	if TGAssistantStore == nil {
		return nil, errors.New("tg assistant datastore is not initialized")
	}

	TGAssistantStore.mu.RLock()
	records := loadTGAssistantAccountsLocked()
	apiID, apiHash := loadTGAssistantAPIKeyLocked()
	TGAssistantStore.mu.RUnlock()

	for i := range records {
		refreshOneTGAccountRecord(&records[i], apiID, apiHash)
	}

	TGAssistantStore.mu.Lock()
	TGAssistantStore.data.Accounts = records
	TGAssistantStore.mu.Unlock()

	if err := TGAssistantStore.Save(); err != nil {
		return nil, err
	}

	return buildTGAssistantAccountViews(records, apiID), nil
}

func addTGAssistantAccount(req tgAssistantAddAccountRequest) (tgAssistantAccount, error) {
	if TGAssistantStore == nil {
		return tgAssistantAccount{}, errors.New("tg assistant datastore is not initialized")
	}

	phone := normalizeTGPhone(req.Phone)
	if phone == "" {
		return tgAssistantAccount{}, errors.New("phone is required")
	}

	label := strings.TrimSpace(req.Label)
	if label == "" {
		label = phone
	}

	now := time.Now().UTC().Format(time.RFC3339)
	record := tgAssistantAccountRecord{
		ID:          newTGAssistantAccountID(),
		Label:       label,
		Phone:       phone,
		Authorized:  false,
		LastError:   "",
		CreatedAt:   now,
		UpdatedAt:   now,
		LastLoginAt: "",
	}

	TGAssistantStore.mu.Lock()
	records := loadTGAssistantAccountsLocked()
	apiID, _ := loadTGAssistantAPIKeyLocked()
	for _, existing := range records {
		if existing.Phone == record.Phone {
			TGAssistantStore.mu.Unlock()
			return tgAssistantAccount{}, fmt.Errorf("account already exists for phone=%s", record.Phone)
		}
	}
	records = append(records, record)
	TGAssistantStore.data.Accounts = records
	TGAssistantStore.mu.Unlock()

	if err := TGAssistantStore.Save(); err != nil {
		return tgAssistantAccount{}, err
	}
	appendTGAssistantHistory("account.add", record.ID, true, fmt.Sprintf("label=%s phone=%s", record.Label, record.Phone))

	return buildTGAssistantAccountView(record, apiID), nil
}

func removeTGAssistantAccount(req tgAssistantAccountIDRequest) ([]tgAssistantAccount, error) {
	if TGAssistantStore == nil {
		return nil, errors.New("tg assistant datastore is not initialized")
	}

	accountID := strings.TrimSpace(req.AccountID)
	if accountID == "" {
		return nil, errors.New("account_id is required")
	}

	TGAssistantStore.mu.Lock()
	records := loadTGAssistantAccountsLocked()
	apiID, _ := loadTGAssistantAPIKeyLocked()
	next := make([]tgAssistantAccountRecord, 0, len(records))
	found := false
	for _, item := range records {
		if item.ID == accountID {
			found = true
			continue
		}
		next = append(next, item)
	}
	if !found {
		TGAssistantStore.mu.Unlock()
		return nil, errors.New("account not found")
	}
	TGAssistantStore.data.Accounts = next
	TGAssistantStore.mu.Unlock()

	if err := TGAssistantStore.Save(); err != nil {
		return nil, err
	}

	clearTGAssistantLoginChallenge(accountID)
	_ = os.Remove(tgAssistantSessionPath(accountID))
	_ = os.Remove(tgAssistantTargetsPath(accountID))
	appendTGAssistantHistory("account.remove", accountID, true, "removed")

	return buildTGAssistantAccountViews(next, apiID), nil
}

func sendTGAssistantLoginCode(req tgAssistantAccountIDRequest) (tgAssistantAccount, error) {
	if TGAssistantStore == nil {
		return tgAssistantAccount{}, errors.New("tg assistant datastore is not initialized")
	}

	accountID := strings.TrimSpace(req.AccountID)
	if accountID == "" {
		return tgAssistantAccount{}, errors.New("account_id is required")
	}

	TGAssistantStore.mu.Lock()
	records := loadTGAssistantAccountsLocked()
	apiID, apiHash := loadTGAssistantAPIKeyLocked()
	index := indexTGAssistantAccountByID(records, accountID)
	if index < 0 {
		TGAssistantStore.mu.Unlock()
		return tgAssistantAccount{}, errors.New("account not found")
	}
	record := records[index]
	TGAssistantStore.mu.Unlock()
	if !isTGAssistantAPIKeyConfigured(apiID, apiHash) {
		return tgAssistantAccount{}, errors.New("shared tg api key is not configured")
	}

	var (
		codeHash    string
		status      *tgauth.Status
		runErr      error
		nowRFC3339  = time.Now().UTC().Format(time.RFC3339)
		recordError = ""
	)

	runErr = runTGAssistantClient(apiID, apiHash, record, func(ctx context.Context, client *telegram.Client) error {
		authStatus, err := client.Auth().Status(ctx)
		if err != nil {
			return err
		}
		if authStatus.Authorized {
			status = authStatus
			return nil
		}

		sentCode, err := client.Auth().SendCode(ctx, record.Phone, tgauth.SendCodeOptions{})
		if err != nil {
			return err
		}
		hash, err := tgAssistantPhoneCodeHash(sentCode)
		if err != nil {
			return err
		}
		codeHash = hash
		return nil
	})

	TGAssistantStore.mu.Lock()
	records = loadTGAssistantAccountsLocked()
	apiID, _ = loadTGAssistantAPIKeyLocked()
	index = indexTGAssistantAccountByID(records, accountID)
	if index < 0 {
		TGAssistantStore.mu.Unlock()
		return tgAssistantAccount{}, errors.New("account not found")
	}
	record = records[index]
	record.UpdatedAt = nowRFC3339

	if runErr != nil {
		record.Authorized = false
		recordError = runErr.Error()
		record.LastError = recordError
		clearTGAssistantIdentityFields(&record)
	} else if status != nil && status.Authorized {
		record.Authorized = true
		record.LastError = "account already authorized"
		applyTGAssistantIdentityFromStatus(&record, status)
	} else {
		record.Authorized = false
		record.LastError = "verification code sent"
		clearTGAssistantIdentityFields(&record)
		setTGAssistantLoginChallenge(accountID, codeHash, tgAssistantLoginCodeTTL)
	}
	records[index] = record
	TGAssistantStore.data.Accounts = records
	TGAssistantStore.mu.Unlock()

	if err := TGAssistantStore.Save(); err != nil {
		return tgAssistantAccount{}, err
	}
	if runErr != nil {
		appendTGAssistantHistory("account.send_code", accountID, false, runErr.Error())
		return tgAssistantAccount{}, runErr
	}
	appendTGAssistantHistory("account.send_code", accountID, true, record.LastError)

	return buildTGAssistantAccountView(record, apiID), nil
}

func completeTGAssistantLogin(req tgAssistantSignInRequest) (tgAssistantAccount, error) {
	if TGAssistantStore == nil {
		return tgAssistantAccount{}, errors.New("tg assistant datastore is not initialized")
	}

	accountID := strings.TrimSpace(req.AccountID)
	if accountID == "" {
		return tgAssistantAccount{}, errors.New("account_id is required")
	}
	code := strings.TrimSpace(req.Code)
	if code == "" {
		return tgAssistantAccount{}, errors.New("code is required")
	}

	challengeHash, ok := getTGAssistantLoginChallenge(accountID)
	if !ok {
		return tgAssistantAccount{}, errors.New("verification code is missing or expired, please send code again")
	}

	TGAssistantStore.mu.Lock()
	records := loadTGAssistantAccountsLocked()
	apiID, apiHash := loadTGAssistantAPIKeyLocked()
	index := indexTGAssistantAccountByID(records, accountID)
	if index < 0 {
		TGAssistantStore.mu.Unlock()
		return tgAssistantAccount{}, errors.New("account not found")
	}
	record := records[index]
	TGAssistantStore.mu.Unlock()
	if !isTGAssistantAPIKeyConfigured(apiID, apiHash) {
		return tgAssistantAccount{}, errors.New("shared tg api key is not configured")
	}

	password := req.Password
	var status *tgauth.Status
	runErr := runTGAssistantClient(apiID, apiHash, record, func(ctx context.Context, client *telegram.Client) error {
		if _, err := client.Auth().SignIn(ctx, record.Phone, code, challengeHash); err != nil {
			if errors.Is(err, tgauth.ErrPasswordAuthNeeded) {
				if strings.TrimSpace(password) == "" {
					return errTGAssistantPasswordRequired
				}
				if _, err := client.Auth().Password(ctx, password); err != nil {
					return err
				}
			} else {
				return err
			}
		}

		authStatus, err := client.Auth().Status(ctx)
		if err != nil {
			return err
		}
		if !authStatus.Authorized {
			return errors.New("telegram authorization failed")
		}
		status = authStatus
		return nil
	})

	TGAssistantStore.mu.Lock()
	records = loadTGAssistantAccountsLocked()
	apiID, _ = loadTGAssistantAPIKeyLocked()
	index = indexTGAssistantAccountByID(records, accountID)
	if index < 0 {
		TGAssistantStore.mu.Unlock()
		return tgAssistantAccount{}, errors.New("account not found")
	}
	record = records[index]
	nowRFC3339 := time.Now().UTC().Format(time.RFC3339)
	record.UpdatedAt = nowRFC3339

	if runErr != nil {
		record.Authorized = false
		record.LastError = runErr.Error()
		clearTGAssistantIdentityFields(&record)
	} else {
		record.Authorized = true
		record.LastError = "authorized"
		record.LastLoginAt = nowRFC3339
		applyTGAssistantIdentityFromStatus(&record, status)
		clearTGAssistantLoginChallenge(accountID)
	}
	records[index] = record
	TGAssistantStore.data.Accounts = records
	TGAssistantStore.mu.Unlock()

	if err := TGAssistantStore.Save(); err != nil {
		return tgAssistantAccount{}, err
	}
	if runErr != nil {
		appendTGAssistantHistory("account.sign_in", accountID, false, runErr.Error())
		return tgAssistantAccount{}, runErr
	}
	appendTGAssistantHistory("account.sign_in", accountID, true, "authorized")

	return buildTGAssistantAccountView(record, apiID), nil
}

func logoutTGAssistantAccount(req tgAssistantAccountIDRequest) (tgAssistantAccount, error) {
	if TGAssistantStore == nil {
		return tgAssistantAccount{}, errors.New("tg assistant datastore is not initialized")
	}

	accountID := strings.TrimSpace(req.AccountID)
	if accountID == "" {
		return tgAssistantAccount{}, errors.New("account_id is required")
	}

	TGAssistantStore.mu.Lock()
	records := loadTGAssistantAccountsLocked()
	apiID, _ := loadTGAssistantAPIKeyLocked()
	index := indexTGAssistantAccountByID(records, accountID)
	if index < 0 {
		TGAssistantStore.mu.Unlock()
		return tgAssistantAccount{}, errors.New("account not found")
	}
	record := records[index]
	clearTGAssistantIdentityFields(&record)
	record.Authorized = false
	record.LastError = "logged out"
	record.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	records[index] = record
	TGAssistantStore.data.Accounts = records
	TGAssistantStore.mu.Unlock()

	if err := TGAssistantStore.Save(); err != nil {
		return tgAssistantAccount{}, err
	}

	clearTGAssistantLoginChallenge(accountID)
	_ = os.Remove(tgAssistantSessionPath(accountID))
	appendTGAssistantHistory("account.logout", accountID, true, "logged out")

	return buildTGAssistantAccountView(record, apiID), nil
}

func listTGAssistantSchedules(req tgAssistantAccountIDRequest) ([]tgAssistantSchedule, error) {
	if TGAssistantStore == nil {
		return nil, errors.New("tg assistant datastore is not initialized")
	}

	accountID := strings.TrimSpace(req.AccountID)
	if accountID == "" {
		return nil, errors.New("account_id is required")
	}

	TGAssistantStore.mu.RLock()
	records := loadTGAssistantAccountsLocked()
	index := indexTGAssistantAccountByID(records, accountID)
	if index < 0 {
		TGAssistantStore.mu.RUnlock()
		return nil, errors.New("account not found")
	}
	result := buildTGAssistantScheduleViews(records[index].Schedules)
	TGAssistantStore.mu.RUnlock()
	return result, nil
}

func addTGAssistantSchedule(req tgAssistantScheduleAddRequest) ([]tgAssistantSchedule, error) {
	if TGAssistantStore == nil {
		return nil, errors.New("tg assistant datastore is not initialized")
	}

	accountID := strings.TrimSpace(req.AccountID)
	taskType := strings.TrimSpace(req.TaskType)
	target := strings.TrimSpace(req.Target)
	sendAt := strings.TrimSpace(req.SendAt)
	message := strings.TrimSpace(req.Message)
	delayMin := req.DelayMin
	delayMax := req.DelayMax
	if accountID == "" {
		return nil, errors.New("account_id is required")
	}
	if taskType == "" {
		taskType = tgTaskTypeScheduledSend
	}
	if taskType != tgTaskTypeScheduledSend {
		return nil, fmt.Errorf("unsupported task_type: %s", taskType)
	}
	if target == "" {
		return nil, errors.New("target is required")
	}
	if len(target) > 256 {
		return nil, errors.New("target is too long")
	}
	if req.Enabled {
		if sendAt == "" {
			return nil, errors.New("send_at is required when schedule is enabled")
		}
		if message == "" {
			return nil, errors.New("message is required when schedule is enabled")
		}
	}
	if len(sendAt) > 120 {
		return nil, errors.New("send_at is too long")
	}
	if len(message) > 4000 {
		return nil, errors.New("message is too long")
	}
	if delayMin < 0 || delayMax < 0 {
		return nil, errors.New("delay range must be non-negative")
	}
	if delayMax < delayMin {
		return nil, errors.New("delay_max_sec must be greater than or equal to delay_min_sec")
	}

	now := time.Now().UTC().Format(time.RFC3339)
	task := tgAssistantScheduleRecord{
		ID:        newTGAssistantScheduleID(),
		TaskType:  taskType,
		Enabled:   req.Enabled,
		Target:    target,
		SendAt:    sendAt,
		Message:   message,
		DelayMin:  delayMin,
		DelayMax:  delayMax,
		CreatedAt: now,
		UpdatedAt: now,
	}

	TGAssistantStore.mu.Lock()
	records := loadTGAssistantAccountsLocked()
	index := indexTGAssistantAccountByID(records, accountID)
	if index < 0 {
		TGAssistantStore.mu.Unlock()
		return nil, errors.New("account not found")
	}
	account := records[index]
	account.Schedules = append(account.Schedules, task)
	account.UpdatedAt = now
	account.Schedules = normalizeTGAssistantScheduleTaskRecords(account.Schedules)
	records[index] = account
	TGAssistantStore.data.Accounts = records
	TGAssistantStore.mu.Unlock()

	if err := TGAssistantStore.Save(); err != nil {
		return nil, err
	}
	appendTGAssistantHistory(
		"schedule.add",
		accountID,
		true,
		fmt.Sprintf(
			"task_id=%s type=%s target=%s enabled=%t send_at=%s delay=%d-%d",
			task.ID,
			task.TaskType,
			task.Target,
			task.Enabled,
			task.SendAt,
			task.DelayMin,
			task.DelayMax,
		),
	)
	return buildTGAssistantScheduleViews(account.Schedules), nil
}

func removeTGAssistantSchedule(req tgAssistantScheduleRemoveRequest) ([]tgAssistantSchedule, error) {
	if TGAssistantStore == nil {
		return nil, errors.New("tg assistant datastore is not initialized")
	}

	accountID := strings.TrimSpace(req.AccountID)
	taskID := strings.TrimSpace(req.TaskID)
	if accountID == "" {
		return nil, errors.New("account_id is required")
	}
	if taskID == "" {
		return nil, errors.New("task_id is required")
	}

	TGAssistantStore.mu.Lock()
	records := loadTGAssistantAccountsLocked()
	index := indexTGAssistantAccountByID(records, accountID)
	if index < 0 {
		TGAssistantStore.mu.Unlock()
		return nil, errors.New("account not found")
	}
	account := records[index]
	nextSchedules := make([]tgAssistantScheduleRecord, 0, len(account.Schedules))
	found := false
	for _, item := range account.Schedules {
		if item.ID == taskID {
			found = true
			continue
		}
		nextSchedules = append(nextSchedules, item)
	}
	if !found {
		TGAssistantStore.mu.Unlock()
		return nil, errors.New("task not found")
	}
	account.Schedules = nextSchedules
	account.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	account.Schedules = normalizeTGAssistantScheduleTaskRecords(account.Schedules)
	records[index] = account
	TGAssistantStore.data.Accounts = records
	TGAssistantStore.mu.Unlock()

	if err := TGAssistantStore.Save(); err != nil {
		return nil, err
	}
	appendTGAssistantHistory("schedule.remove", accountID, true, fmt.Sprintf("task_id=%s", taskID))
	return buildTGAssistantScheduleViews(account.Schedules), nil
}

func setTGAssistantScheduleEnabled(req tgAssistantScheduleSetEnabledRequest) ([]tgAssistantSchedule, error) {
	if TGAssistantStore == nil {
		return nil, errors.New("tg assistant datastore is not initialized")
	}

	accountID := strings.TrimSpace(req.AccountID)
	taskID := strings.TrimSpace(req.TaskID)
	if accountID == "" {
		return nil, errors.New("account_id is required")
	}
	if taskID == "" {
		return nil, errors.New("task_id is required")
	}

	now := time.Now().UTC().Format(time.RFC3339)
	TGAssistantStore.mu.Lock()
	records := loadTGAssistantAccountsLocked()
	index := indexTGAssistantAccountByID(records, accountID)
	if index < 0 {
		TGAssistantStore.mu.Unlock()
		return nil, errors.New("account not found")
	}
	account := records[index]
	taskIndex := indexTGAssistantScheduleByID(account.Schedules, taskID)
	if taskIndex < 0 {
		TGAssistantStore.mu.Unlock()
		return nil, errors.New("task not found")
	}
	account.Schedules[taskIndex].Enabled = req.Enabled
	account.Schedules[taskIndex].UpdatedAt = now
	account.UpdatedAt = now
	account.Schedules = normalizeTGAssistantScheduleTaskRecords(account.Schedules)
	records[index] = account
	TGAssistantStore.data.Accounts = records
	TGAssistantStore.mu.Unlock()

	if err := TGAssistantStore.Save(); err != nil {
		return nil, err
	}
	appendTGAssistantHistory("schedule.set_enabled", accountID, true, fmt.Sprintf("task_id=%s enabled=%t", taskID, req.Enabled))
	return buildTGAssistantScheduleViews(account.Schedules), nil
}

func sendNowTGAssistantSchedule(req tgAssistantScheduleSendNowRequest) (tgAssistantScheduleSendNowResult, error) {
	if TGAssistantStore == nil {
		return tgAssistantScheduleSendNowResult{}, errors.New("tg assistant datastore is not initialized")
	}

	accountID := strings.TrimSpace(req.AccountID)
	taskID := strings.TrimSpace(req.TaskID)
	if accountID == "" {
		return tgAssistantScheduleSendNowResult{}, errors.New("account_id is required")
	}
	if taskID == "" {
		return tgAssistantScheduleSendNowResult{}, errors.New("task_id is required")
	}

	TGAssistantStore.mu.RLock()
	records := loadTGAssistantAccountsLocked()
	apiID, apiHash := loadTGAssistantAPIKeyLocked()
	index := indexTGAssistantAccountByID(records, accountID)
	if index < 0 {
		TGAssistantStore.mu.RUnlock()
		return tgAssistantScheduleSendNowResult{}, errors.New("account not found")
	}
	account := records[index]
	taskIndex := indexTGAssistantScheduleByID(account.Schedules, taskID)
	if taskIndex < 0 {
		TGAssistantStore.mu.RUnlock()
		return tgAssistantScheduleSendNowResult{}, errors.New("task not found")
	}
	task := account.Schedules[taskIndex]
	TGAssistantStore.mu.RUnlock()

	if task.TaskType != tgTaskTypeScheduledSend {
		return tgAssistantScheduleSendNowResult{}, fmt.Errorf("unsupported task_type: %s", task.TaskType)
	}
	if strings.TrimSpace(task.Target) == "" {
		return tgAssistantScheduleSendNowResult{}, errors.New("target is required")
	}
	if strings.TrimSpace(task.Message) == "" {
		return tgAssistantScheduleSendNowResult{}, errors.New("message is required")
	}
	if !isTGAssistantAPIKeyConfigured(apiID, apiHash) {
		return tgAssistantScheduleSendNowResult{}, errors.New("shared tg api key is not configured")
	}

	delaySec := randomTGAssistantScheduleDelaySeconds(task.DelayMin, task.DelayMax)
	if delaySec > 0 {
		time.Sleep(time.Duration(delaySec) * time.Second)
	}

	err := runTGAssistantClient(apiID, apiHash, account, func(ctx context.Context, client *telegram.Client) error {
		status, err := client.Auth().Status(ctx)
		if err != nil {
			return err
		}
		if !status.Authorized {
			return errors.New("account is not authorized")
		}

		peer, err := resolveTGAssistantInputPeer(ctx, client, task.Target)
		if err != nil {
			return err
		}
		_, err = client.API().MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{
			Peer:     peer,
			Message:  task.Message,
			RandomID: newTGAssistantMessageRandomID(),
		})
		return err
	})
	if err != nil {
		appendTGAssistantHistory("schedule.send_now", accountID, false, fmt.Sprintf("task_id=%s err=%s", taskID, err.Error()))
		return tgAssistantScheduleSendNowResult{}, err
	}

	result := tgAssistantScheduleSendNowResult{
		AccountID: accountID,
		TaskID:    taskID,
		Target:    task.Target,
		DelaySec:  delaySec,
		SentAt:    time.Now().UTC().Format(time.RFC3339),
	}
	appendTGAssistantHistory(
		"schedule.send_now",
		accountID,
		true,
		fmt.Sprintf("task_id=%s target=%s delay=%d", taskID, task.Target, delaySec),
	)
	return result, nil
}

func listTGAssistantTargets(req tgAssistantAccountIDRequest) ([]tgAssistantTarget, error) {
	if TGAssistantStore == nil {
		return nil, errors.New("tg assistant datastore is not initialized")
	}

	accountID := strings.TrimSpace(req.AccountID)
	if accountID == "" {
		return nil, errors.New("account_id is required")
	}

	TGAssistantStore.mu.RLock()
	records := loadTGAssistantAccountsLocked()
	index := indexTGAssistantAccountByID(records, accountID)
	TGAssistantStore.mu.RUnlock()
	if index < 0 {
		return nil, errors.New("account not found")
	}

	targets, err := loadTGAssistantTargetsFromFile(accountID)
	if err != nil {
		return nil, err
	}
	return targets, nil
}

func refreshTGAssistantTargets(req tgAssistantAccountIDRequest) ([]tgAssistantTarget, error) {
	if TGAssistantStore == nil {
		return nil, errors.New("tg assistant datastore is not initialized")
	}

	accountID := strings.TrimSpace(req.AccountID)
	if accountID == "" {
		return nil, errors.New("account_id is required")
	}

	TGAssistantStore.mu.RLock()
	records := loadTGAssistantAccountsLocked()
	apiID, apiHash := loadTGAssistantAPIKeyLocked()
	index := indexTGAssistantAccountByID(records, accountID)
	if index < 0 {
		TGAssistantStore.mu.RUnlock()
		return nil, errors.New("account not found")
	}
	record := records[index]
	TGAssistantStore.mu.RUnlock()

	if !isTGAssistantAPIKeyConfigured(apiID, apiHash) {
		return nil, errors.New("shared tg api key is not configured")
	}

	targets := make([]tgAssistantTarget, 0, 64)
	err := runTGAssistantClient(apiID, apiHash, record, func(ctx context.Context, client *telegram.Client) error {
		status, err := client.Auth().Status(ctx)
		if err != nil {
			return err
		}
		if !status.Authorized {
			return errors.New("account is not authorized")
		}

		resp, err := client.API().MessagesGetDialogs(ctx, &tg.MessagesGetDialogsRequest{
			OffsetDate: 0,
			OffsetID:   0,
			OffsetPeer: &tg.InputPeerEmpty{},
			Limit:      100,
			Hash:       0,
		})
		if err != nil {
			return err
		}

		dialogs, chats, users, err := parseTGDialogsResponse(resp)
		if err != nil {
			return err
		}
		targets = buildTGAssistantTargets(dialogs, chats, users)
		return nil
	})
	if err != nil {
		appendTGAssistantHistory("targets.refresh", accountID, false, err.Error())
		return nil, err
	}

	if err := saveTGAssistantTargetsToFile(accountID, targets); err != nil {
		appendTGAssistantHistory("targets.refresh", accountID, false, err.Error())
		return nil, err
	}
	appendTGAssistantHistory("targets.refresh", accountID, true, fmt.Sprintf("count=%d", len(targets)))
	return targets, nil
}

func runTGAssistantClient(apiID int, apiHash string, record tgAssistantAccountRecord, fn func(ctx context.Context, client *telegram.Client) error) error {
	if apiID <= 0 {
		return errors.New("api_id must be a positive integer")
	}
	if strings.TrimSpace(apiHash) == "" {
		return errors.New("api_hash is required")
	}
	if strings.TrimSpace(record.Phone) == "" {
		return errors.New("phone is required")
	}

	sessionPath := tgAssistantSessionPath(record.ID)
	if err := os.MkdirAll(filepath.Dir(sessionPath), 0o755); err != nil {
		return fmt.Errorf("failed to prepare tg session directory: %w", err)
	}

	client := telegram.NewClient(apiID, apiHash, telegram.Options{
		SessionStorage: &session.FileStorage{Path: sessionPath},
		NoUpdates:      true,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	return client.Run(ctx, func(inner context.Context) error {
		return fn(inner, client)
	})
}

func refreshOneTGAccountRecord(record *tgAssistantAccountRecord, apiID int, apiHash string) {
	if record == nil {
		return
	}
	record.UpdatedAt = time.Now().UTC().Format(time.RFC3339)

	if !isTGAssistantAPIKeyConfigured(apiID, apiHash) {
		record.Authorized = false
		record.LastError = "api key is not configured"
		clearTGAssistantIdentityFields(record)
		return
	}

	sessionPath := tgAssistantSessionPath(record.ID)
	if _, err := os.Stat(sessionPath); errors.Is(err, os.ErrNotExist) {
		record.Authorized = false
		record.LastError = "session file not found"
		clearTGAssistantIdentityFields(record)
		return
	}

	err := runTGAssistantClient(apiID, apiHash, *record, func(ctx context.Context, client *telegram.Client) error {
		status, err := client.Auth().Status(ctx)
		if err != nil {
			return err
		}
		if status.Authorized {
			record.Authorized = true
			record.LastError = ""
			if strings.TrimSpace(record.LastLoginAt) == "" {
				record.LastLoginAt = time.Now().UTC().Format(time.RFC3339)
			}
			applyTGAssistantIdentityFromStatus(record, status)
			return nil
		}

		record.Authorized = false
		record.LastError = "not authorized"
		clearTGAssistantIdentityFields(record)
		return nil
	})
	if err != nil {
		record.Authorized = false
		record.LastError = err.Error()
		clearTGAssistantIdentityFields(record)
	}
}

func tgAssistantPhoneCodeHash(sentCode tg.AuthSentCodeClass) (string, error) {
	switch value := sentCode.(type) {
	case *tg.AuthSentCode:
		hash := strings.TrimSpace(value.PhoneCodeHash)
		if hash == "" {
			return "", errors.New("telegram returned empty phone_code_hash")
		}
		return hash, nil
	case *tg.AuthSentCodePaymentRequired:
		hash := strings.TrimSpace(value.PhoneCodeHash)
		if hash == "" {
			return "", errors.New("telegram returned empty phone_code_hash")
		}
		return hash, nil
	default:
		return "", fmt.Errorf("unexpected sent code type: %T", sentCode)
	}
}

func applyTGAssistantIdentityFromStatus(record *tgAssistantAccountRecord, status *tgauth.Status) {
	if record == nil {
		return
	}
	if status == nil || status.User == nil {
		clearTGAssistantIdentityFields(record)
		return
	}

	user := status.User
	record.SelfUserID = user.ID
	record.SelfUsername = strings.TrimSpace(user.Username)
	record.SelfPhone = strings.TrimSpace(user.Phone)

	fullName := strings.TrimSpace(strings.TrimSpace(user.FirstName) + " " + strings.TrimSpace(user.LastName))
	if fullName == "" {
		fullName = record.SelfUsername
	}
	if fullName == "" {
		fullName = record.SelfPhone
	}
	record.SelfDisplayName = fullName
}

func clearTGAssistantIdentityFields(record *tgAssistantAccountRecord) {
	if record == nil {
		return
	}
	record.SelfUserID = 0
	record.SelfUsername = ""
	record.SelfDisplayName = ""
	record.SelfPhone = ""
}

func buildTGAssistantAccountViews(records []tgAssistantAccountRecord, sharedAPIID int) []tgAssistantAccount {
	views := make([]tgAssistantAccount, 0, len(records))
	for _, record := range records {
		views = append(views, buildTGAssistantAccountView(record, sharedAPIID))
	}

	sort.SliceStable(views, func(i, j int) bool {
		if views[i].Authorized != views[j].Authorized {
			return views[i].Authorized
		}
		return views[i].CreatedAt < views[j].CreatedAt
	})
	return views
}

func buildTGAssistantAccountView(record tgAssistantAccountRecord, sharedAPIID int) tgAssistantAccount {
	view := tgAssistantAccount{
		ID:              record.ID,
		Label:           record.Label,
		Phone:           record.Phone,
		APIID:           sharedAPIID,
		Authorized:      record.Authorized,
		LastError:       record.LastError,
		CreatedAt:       record.CreatedAt,
		UpdatedAt:       record.UpdatedAt,
		LastLoginAt:     record.LastLoginAt,
		SelfUserID:      record.SelfUserID,
		SelfUsername:    record.SelfUsername,
		SelfDisplayName: record.SelfDisplayName,
		SelfPhone:       record.SelfPhone,
		Schedules:       buildTGAssistantScheduleViews(record.Schedules),
	}

	_, pending := getTGAssistantLoginChallenge(record.ID)
	view.PendingCode = pending
	return view
}

func loadTGAssistantAccountsLocked() []tgAssistantAccountRecord {
	if TGAssistantStore == nil {
		return []tgAssistantAccountRecord{}
	}

	result := make([]tgAssistantAccountRecord, len(TGAssistantStore.data.Accounts))
	copy(result, TGAssistantStore.data.Accounts)
	return normalizeTGAssistantAccountRecords(result)
}

func loadTGAssistantAPIKeyLocked() (int, string) {
	if TGAssistantStore == nil {
		return 0, ""
	}
	return TGAssistantStore.data.APIID, strings.TrimSpace(TGAssistantStore.data.APIHash)
}

func isTGAssistantAPIKeyConfigured(apiID int, apiHash string) bool {
	return apiID > 0 && strings.TrimSpace(apiHash) != ""
}

func normalizeTGAssistantAccountRecords(records []tgAssistantAccountRecord) []tgAssistantAccountRecord {
	normalized := make([]tgAssistantAccountRecord, 0, len(records))
	now := time.Now().UTC().Format(time.RFC3339)
	for _, item := range records {
		item.ID = strings.TrimSpace(item.ID)
		if item.ID == "" {
			continue
		}
		item.Label = strings.TrimSpace(item.Label)
		item.Phone = normalizeTGPhone(item.Phone)
		item.SelfUsername = strings.TrimSpace(item.SelfUsername)
		item.SelfDisplayName = strings.TrimSpace(item.SelfDisplayName)
		item.SelfPhone = normalizeTGPhone(item.SelfPhone)
		if item.Label == "" {
			item.Label = item.Phone
		}
		if item.CreatedAt == "" {
			item.CreatedAt = now
		}
		if item.UpdatedAt == "" {
			item.UpdatedAt = item.CreatedAt
		}
		item.Schedules = normalizeTGAssistantScheduleTaskRecords(item.Schedules)
		if item.Phone == "" {
			continue
		}
		normalized = append(normalized, item)
	}

	sort.SliceStable(normalized, func(i, j int) bool {
		return normalized[i].CreatedAt < normalized[j].CreatedAt
	})
	return normalized
}

func normalizeTGAssistantScheduleTaskRecords(records []tgAssistantScheduleRecord) []tgAssistantScheduleRecord {
	normalized := make([]tgAssistantScheduleRecord, 0, len(records))
	now := time.Now().UTC().Format(time.RFC3339)
	for _, item := range records {
		item.ID = strings.TrimSpace(item.ID)
		item.TaskType = strings.TrimSpace(item.TaskType)
		item.Target = strings.TrimSpace(item.Target)
		item.SendAt = strings.TrimSpace(item.SendAt)
		item.Message = strings.TrimSpace(item.Message)
		if item.DelayMin < 0 {
			item.DelayMin = 0
		}
		if item.DelayMax < 0 {
			item.DelayMax = 0
		}
		if item.DelayMax < item.DelayMin {
			item.DelayMax = item.DelayMin
		}
		item.CreatedAt = strings.TrimSpace(item.CreatedAt)
		item.UpdatedAt = strings.TrimSpace(item.UpdatedAt)
		if item.ID == "" {
			continue
		}
		if item.TaskType == "" {
			item.TaskType = tgTaskTypeScheduledSend
		}
		if item.CreatedAt == "" {
			item.CreatedAt = now
		}
		if item.UpdatedAt == "" {
			item.UpdatedAt = item.CreatedAt
		}
		normalized = append(normalized, item)
	}
	sort.SliceStable(normalized, func(i, j int) bool {
		return normalized[i].CreatedAt < normalized[j].CreatedAt
	})
	return normalized
}

func indexTGAssistantAccountByID(records []tgAssistantAccountRecord, accountID string) int {
	for i, item := range records {
		if item.ID == accountID {
			return i
		}
	}
	return -1
}

func indexTGAssistantScheduleByID(records []tgAssistantScheduleRecord, taskID string) int {
	for i, item := range records {
		if item.ID == taskID {
			return i
		}
	}
	return -1
}

func buildTGAssistantScheduleViews(records []tgAssistantScheduleRecord) []tgAssistantSchedule {
	result := make([]tgAssistantSchedule, 0, len(records))
	for _, record := range records {
		result = append(result, tgAssistantSchedule{
			ID:        record.ID,
			TaskType:  record.TaskType,
			Enabled:   record.Enabled,
			Target:    record.Target,
			SendAt:    record.SendAt,
			Message:   record.Message,
			DelayMin:  record.DelayMin,
			DelayMax:  record.DelayMax,
			CreatedAt: record.CreatedAt,
			UpdatedAt: record.UpdatedAt,
		})
	}
	return result
}

func tgAssistantTempDirPath() string {
	return filepath.Clean(tgAssistantTempDir)
}

func tgAssistantHistoryPath() string {
	return filepath.Join(tgAssistantTempDirPath(), tgAssistantHistoryFile)
}

func tgAssistantTargetsDirPath() string {
	return filepath.Join(tgAssistantTempDirPath(), tgAssistantTargetsDirName)
}

func tgAssistantTargetsPath(accountID string) string {
	safeID := strings.TrimSpace(accountID)
	if safeID == "" {
		safeID = "unknown"
	}
	return filepath.Join(tgAssistantTargetsDirPath(), safeID+".json")
}

func appendTGAssistantHistory(action, accountID string, success bool, message string) {
	record := tgAssistantHistoryRecord{
		Time:      time.Now().UTC().Format(time.RFC3339),
		Action:    strings.TrimSpace(action),
		AccountID: strings.TrimSpace(accountID),
		Success:   success,
		Message:   strings.TrimSpace(message),
	}
	if record.Action == "" {
		return
	}

	if err := os.MkdirAll(tgAssistantTempDirPath(), 0o755); err != nil {
		log.Printf("tg history mkdir failed: %v", err)
		return
	}

	line, err := json.Marshal(record)
	if err != nil {
		log.Printf("tg history marshal failed: %v", err)
		return
	}

	f, err := os.OpenFile(tgAssistantHistoryPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		log.Printf("tg history open failed: %v", err)
		return
	}
	defer func() {
		_ = f.Close()
	}()

	if _, err := f.Write(append(line, '\n')); err != nil {
		log.Printf("tg history append failed: %v", err)
	}
}

func parseTGDialogsResponse(resp tg.MessagesDialogsClass) ([]tg.DialogClass, []tg.ChatClass, []tg.UserClass, error) {
	switch value := resp.(type) {
	case *tg.MessagesDialogs:
		return value.Dialogs, value.Chats, value.Users, nil
	case *tg.MessagesDialogsSlice:
		return value.Dialogs, value.Chats, value.Users, nil
	case *tg.MessagesDialogsNotModified:
		return []tg.DialogClass{}, []tg.ChatClass{}, []tg.UserClass{}, nil
	default:
		return nil, nil, nil, fmt.Errorf("unexpected dialogs response: %T", resp)
	}
}

func resolveTGAssistantInputPeer(ctx context.Context, client *telegram.Client, target string) (tg.InputPeerClass, error) {
	targetType, targetID, err := parseTGAssistantTarget(target)
	if err != nil {
		return nil, err
	}

	resp, err := client.API().MessagesGetDialogs(ctx, &tg.MessagesGetDialogsRequest{
		OffsetDate: 0,
		OffsetID:   0,
		OffsetPeer: &tg.InputPeerEmpty{},
		Limit:      100,
		Hash:       0,
	})
	if err != nil {
		return nil, err
	}

	_, chats, users, err := parseTGDialogsResponse(resp)
	if err != nil {
		return nil, err
	}

	switch targetType {
	case "user":
		for _, raw := range users {
			switch item := raw.(type) {
			case *tg.User:
				if item.ID == targetID {
					return item.AsInputPeer(), nil
				}
			case *tg.UserEmpty:
				if item.ID == targetID {
					return nil, fmt.Errorf("target user %d has no access info", targetID)
				}
			}
		}
		return nil, fmt.Errorf("target user %d not found in current dialogs", targetID)
	case "chat":
		for _, raw := range chats {
			switch item := raw.(type) {
			case *tg.Chat:
				if item.ID == targetID {
					return item.AsInputPeer(), nil
				}
			case *tg.ChatForbidden:
				if item.ID == targetID {
					return nil, fmt.Errorf("no access to target chat %d", targetID)
				}
			}
		}
		return nil, fmt.Errorf("target chat %d not found in current dialogs", targetID)
	case "channel":
		for _, raw := range chats {
			switch item := raw.(type) {
			case *tg.Channel:
				if item.ID == targetID {
					return item.AsInputPeer(), nil
				}
			case *tg.ChannelForbidden:
				if item.ID == targetID {
					return &tg.InputPeerChannel{
						ChannelID:  item.ID,
						AccessHash: item.AccessHash,
					}, nil
				}
			}
		}
		return nil, fmt.Errorf("target channel %d not found in current dialogs", targetID)
	default:
		return nil, fmt.Errorf("unsupported target type: %s", targetType)
	}
}

func parseTGAssistantTarget(rawTarget string) (string, int64, error) {
	target := strings.TrimSpace(rawTarget)
	if target == "" {
		return "", 0, errors.New("target is required")
	}
	parts := strings.SplitN(target, ":", 2)
	if len(parts) != 2 {
		return "", 0, fmt.Errorf("invalid target format: %s", target)
	}
	targetType := strings.TrimSpace(parts[0])
	idText := strings.TrimSpace(parts[1])
	if targetType == "" || idText == "" {
		return "", 0, fmt.Errorf("invalid target format: %s", target)
	}
	if targetType != "user" && targetType != "chat" && targetType != "channel" {
		return "", 0, fmt.Errorf("unsupported target type: %s", targetType)
	}
	targetID, err := strconv.ParseInt(idText, 10, 64)
	if err != nil || targetID <= 0 {
		return "", 0, fmt.Errorf("invalid target id: %s", idText)
	}
	return targetType, targetID, nil
}

func buildTGAssistantTargets(dialogs []tg.DialogClass, chats []tg.ChatClass, users []tg.UserClass) []tgAssistantTarget {
	userMap := map[int64]tgAssistantTarget{}
	for _, raw := range users {
		switch item := raw.(type) {
		case *tg.User:
			name := strings.TrimSpace(strings.TrimSpace(item.FirstName) + " " + strings.TrimSpace(item.LastName))
			if name == "" {
				name = strings.TrimSpace(item.Username)
			}
			if name == "" {
				name = normalizeTGPhone(item.Phone)
			}
			if name == "" {
				name = fmt.Sprintf("User %d", item.ID)
			}
			userMap[item.ID] = tgAssistantTarget{
				ID:       fmt.Sprintf("user:%d", item.ID),
				Name:     name,
				Username: strings.TrimSpace(item.Username),
				Type:     "user",
			}
		case *tg.UserEmpty:
			userMap[item.ID] = tgAssistantTarget{
				ID:   fmt.Sprintf("user:%d", item.ID),
				Name: fmt.Sprintf("User %d", item.ID),
				Type: "user",
			}
		}
	}

	chatMap := map[int64]tgAssistantTarget{}
	channelMap := map[int64]tgAssistantTarget{}
	for _, raw := range chats {
		switch item := raw.(type) {
		case *tg.Chat:
			chatMap[item.ID] = tgAssistantTarget{
				ID:   fmt.Sprintf("chat:%d", item.ID),
				Name: strings.TrimSpace(item.Title),
				Type: "chat",
			}
		case *tg.ChatForbidden:
			chatMap[item.ID] = tgAssistantTarget{
				ID:   fmt.Sprintf("chat:%d", item.ID),
				Name: strings.TrimSpace(item.Title),
				Type: "chat",
			}
		case *tg.Channel:
			channelMap[item.ID] = tgAssistantTarget{
				ID:       fmt.Sprintf("channel:%d", item.ID),
				Name:     strings.TrimSpace(item.Title),
				Username: strings.TrimSpace(item.Username),
				Type:     "channel",
			}
		case *tg.ChannelForbidden:
			channelMap[item.ID] = tgAssistantTarget{
				ID:   fmt.Sprintf("channel:%d", item.ID),
				Name: strings.TrimSpace(item.Title),
				Type: "channel",
			}
		}
	}

	targets := make([]tgAssistantTarget, 0, len(dialogs))
	seen := map[string]struct{}{}
	appendTarget := func(item tgAssistantTarget) {
		item.ID = strings.TrimSpace(item.ID)
		item.Name = strings.TrimSpace(item.Name)
		item.Username = strings.TrimSpace(item.Username)
		item.Type = strings.TrimSpace(item.Type)
		if item.ID == "" {
			return
		}
		if item.Name == "" {
			item.Name = item.ID
		}
		if _, ok := seen[item.ID]; ok {
			return
		}
		seen[item.ID] = struct{}{}
		targets = append(targets, item)
	}

	for _, raw := range dialogs {
		dialog, ok := raw.(*tg.Dialog)
		if !ok || dialog == nil {
			continue
		}
		switch peer := dialog.Peer.(type) {
		case *tg.PeerUser:
			if item, ok := userMap[peer.UserID]; ok {
				appendTarget(item)
			} else {
				appendTarget(tgAssistantTarget{ID: fmt.Sprintf("user:%d", peer.UserID), Name: fmt.Sprintf("User %d", peer.UserID), Type: "user"})
			}
		case *tg.PeerChat:
			if item, ok := chatMap[peer.ChatID]; ok {
				appendTarget(item)
			} else {
				appendTarget(tgAssistantTarget{ID: fmt.Sprintf("chat:%d", peer.ChatID), Name: fmt.Sprintf("Chat %d", peer.ChatID), Type: "chat"})
			}
		case *tg.PeerChannel:
			if item, ok := channelMap[peer.ChannelID]; ok {
				appendTarget(item)
			} else {
				appendTarget(tgAssistantTarget{ID: fmt.Sprintf("channel:%d", peer.ChannelID), Name: fmt.Sprintf("Channel %d", peer.ChannelID), Type: "channel"})
			}
		}
	}

	return targets
}

func loadTGAssistantTargetsFromFile(accountID string) ([]tgAssistantTarget, error) {
	filePath := tgAssistantTargetsPath(accountID)
	content, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return []tgAssistantTarget{}, nil
		}
		return nil, fmt.Errorf("read targets file failed: %w", err)
	}
	if len(strings.TrimSpace(string(content))) == 0 {
		return []tgAssistantTarget{}, nil
	}
	var targets []tgAssistantTarget
	if err := json.Unmarshal(content, &targets); err != nil {
		return nil, fmt.Errorf("parse targets file failed: %w", err)
	}

	normalized := make([]tgAssistantTarget, 0, len(targets))
	for _, item := range targets {
		item.ID = strings.TrimSpace(item.ID)
		item.Name = strings.TrimSpace(item.Name)
		item.Username = strings.TrimSpace(item.Username)
		item.Type = strings.TrimSpace(item.Type)
		if item.ID == "" {
			continue
		}
		if item.Name == "" {
			item.Name = item.ID
		}
		normalized = append(normalized, item)
	}
	return normalized, nil
}

func saveTGAssistantTargetsToFile(accountID string, targets []tgAssistantTarget) error {
	if err := os.MkdirAll(tgAssistantTargetsDirPath(), 0o755); err != nil {
		return fmt.Errorf("create targets directory failed: %w", err)
	}

	content, err := json.MarshalIndent(targets, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal targets failed: %w", err)
	}
	if err := os.WriteFile(tgAssistantTargetsPath(accountID), content, 0o644); err != nil {
		return fmt.Errorf("write targets file failed: %w", err)
	}
	return nil
}

func normalizeTGPhone(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}

	builder := strings.Builder{}
	for idx, r := range value {
		if r >= '0' && r <= '9' {
			builder.WriteRune(r)
			continue
		}
		if r == '+' && idx == 0 {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

func randomTGAssistantScheduleDelaySeconds(delayMin int, delayMax int) int {
	if delayMin < 0 {
		delayMin = 0
	}
	if delayMax < 0 {
		delayMax = 0
	}
	if delayMax < delayMin {
		delayMax = delayMin
	}
	if delayMax == delayMin {
		return delayMin
	}
	span := delayMax - delayMin + 1
	if span <= 1 {
		return delayMin
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(span)))
	if err != nil {
		return delayMin
	}
	return delayMin + int(n.Int64())
}

func newTGAssistantMessageRandomID() int64 {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err == nil {
		value := int64(binary.LittleEndian.Uint64(buf[:]) & 0x7fffffffffffffff)
		if value != 0 {
			return value
		}
	}
	fallback := time.Now().UnixNano() & 0x7fffffffffffffff
	if fallback == 0 {
		return 1
	}
	return fallback
}

func newTGAssistantAccountID() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "tg-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	return "tg-" + hex.EncodeToString(buf)
}

func newTGAssistantScheduleID() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "task-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	return "task-" + hex.EncodeToString(buf)
}

func tgAssistantSessionPath(accountID string) string {
	safeID := strings.TrimSpace(accountID)
	if safeID == "" {
		safeID = "unknown"
	}
	return filepath.Join(dataDir, tgAssistantSessionDirName, safeID+".json")
}

func migrateTGAssistantSessionFilesToDataDir() {
	oldDir := filepath.Join(tgAssistantTempDirPath(), tgAssistantSessionDirName)
	entries, err := os.ReadDir(oldDir)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		log.Printf("list legacy tg session dir failed: %v", err)
		return
	}

	newDir := filepath.Join(dataDir, tgAssistantSessionDirName)
	if err := os.MkdirAll(newDir, 0o755); err != nil {
		log.Printf("create tg session dir in data failed: %v", err)
		return
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := strings.TrimSpace(entry.Name())
		if name == "" || !strings.HasSuffix(strings.ToLower(name), ".json") {
			continue
		}

		src := filepath.Join(oldDir, name)
		dst := filepath.Join(newDir, name)
		if _, err := os.Stat(dst); err == nil {
			continue
		}

		if err := os.Rename(src, dst); err == nil {
			log.Printf("migrated tg session file to data dir: %s", name)
			continue
		}

		content, readErr := os.ReadFile(src)
		if readErr != nil {
			log.Printf("read legacy tg session file failed: %v", readErr)
			continue
		}
		if writeErr := os.WriteFile(dst, content, 0o644); writeErr != nil {
			log.Printf("write migrated tg session file failed: %v", writeErr)
			continue
		}
		_ = os.Remove(src)
		log.Printf("copied tg session file to data dir: %s", name)
	}
}

func setTGAssistantLoginChallenge(accountID, phoneCodeHash string, ttl time.Duration) {
	if ttl <= 0 {
		ttl = tgAssistantLoginCodeTTL
	}

	tgState.mu.Lock()
	tgState.challenges[accountID] = tgAssistantLoginChallenge{
		PhoneCodeHash: strings.TrimSpace(phoneCodeHash),
		ExpiresAt:     time.Now().UTC().Add(ttl),
	}
	tgState.mu.Unlock()
}

func getTGAssistantLoginChallenge(accountID string) (string, bool) {
	tgState.mu.Lock()
	challenge, ok := tgState.challenges[accountID]
	if !ok {
		tgState.mu.Unlock()
		return "", false
	}
	if time.Now().UTC().After(challenge.ExpiresAt) {
		delete(tgState.challenges, accountID)
		tgState.mu.Unlock()
		return "", false
	}
	tgState.mu.Unlock()
	return challenge.PhoneCodeHash, true
}

func clearTGAssistantLoginChallenge(accountID string) {
	tgState.mu.Lock()
	delete(tgState.challenges, accountID)
	tgState.mu.Unlock()
}
