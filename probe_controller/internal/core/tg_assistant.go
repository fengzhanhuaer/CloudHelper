package core

import (
	"context"
	"crypto/rand"
	"encoding/base64"
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
	tgAssistantTempDir          = "./temp/tg"
	tgAssistantLegacyTempDir    = "./tg"
	tgAssistantStoreFile        = "tg.json"
	tgAssistantSessionDirName   = "tg_sessions"
	tgAssistantTargetsDirName   = "targets"
	tgAssistantTaskHistoryDir   = "task_history"
	tgAssistantHistoryFile      = "history.jsonl"
	tgAssistantTaskHistoryMax   = 360
	tgAssistantArchivedFolderID = 1
	tgAssistantMainFolderID     = 0
	tgAssistantDialogPageLimit  = 100
	tgAssistantDialogMaxPages   = 200
	tgAssistantLoginCodeTTL     = 10 * time.Minute
	tgTaskTypeScheduledSend     = "scheduled_send"
)

var (
	errTGAssistantPasswordRequired = errors.New("password is required for 2FA account")
	tgAssistantTaskHistoryMu       sync.Mutex
)

type tgAssistantAccountRecord struct {
	ID              string                      `json:"id"`
	Label           string                      `json:"label"`
	Phone           string                      `json:"phone"`
	BotAPIKey       string                      `json:"bot_api_key,omitempty"`
	BotMode         string                      `json:"bot_mode,omitempty"`
	BotWebhookPath  string                      `json:"bot_webhook_path,omitempty"`
	BotWebhookToken string                      `json:"bot_webhook_token,omitempty"`
	BotLastUpdateID int                         `json:"bot_last_update_id,omitempty"`
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
	SessionToken    string                `json:"session_token,omitempty"`
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

type tgAssistantTaskHistoryRecord struct {
	Time      string `json:"time"`
	Action    string `json:"action"`
	AccountID string `json:"account_id,omitempty"`
	TaskID    string `json:"task_id"`
	Success   bool   `json:"success"`
	Message   string `json:"message,omitempty"`
}

type tgAssistantTarget struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Username string `json:"username,omitempty"`
	Type     string `json:"type,omitempty"`
	Archived bool   `json:"archived,omitempty"`
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
	Notify   tgAssistantNotifySettings  `json:"notify"`
}

// tgAssistantNotifySettings holds the global "主控通知机器人" configuration.
// Only the selected account's bot pushes probe offline/renewal notifications and
// handles ops commands. Notifications are delivered to that account's own private
// chat (chat_id == SelfUserID).
type tgAssistantNotifySettings struct {
	NotifyAccountID       string `json:"notify_account_id"`
	EnableOffline         bool   `json:"enable_offline"`
	EnableRenewal         bool   `json:"enable_renewal"`
	OfflineDebounceSec    int    `json:"offline_debounce_sec"`
	RenewalThresholds     []int  `json:"renewal_thresholds"`
	RenewalCheckHour      int    `json:"renewal_check_hour"`
	LastControllerBaseURL string `json:"last_controller_base_url,omitempty"`
}

type tgAssistantNotifySettingsRequest struct {
	NotifyAccountID    string `json:"notify_account_id"`
	EnableOffline      bool   `json:"enable_offline"`
	EnableRenewal      bool   `json:"enable_renewal"`
	OfflineDebounceSec int    `json:"offline_debounce_sec"`
	RenewalCheckHour   int    `json:"renewal_check_hour"`
}

const (
	tgAssistantNotifyDefaultDebounceSec = 60
	tgAssistantNotifyDefaultCheckHour   = 9
)

var tgAssistantNotifyDefaultThresholds = []int{7, 3, 1}

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

type tgAssistantSessionTokenLoginRequest struct {
	Label        string `json:"label"`
	SessionToken string `json:"session_token"`
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

type tgAssistantScheduleUpdateRequest struct {
	AccountID string `json:"account_id"`
	TaskID    string `json:"task_id"`
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

type tgAssistantScheduleHistoryRequest struct {
	AccountID string `json:"account_id"`
	TaskID    string `json:"task_id"`
	Limit     int    `json:"limit"`
}

type tgAssistantScheduleSendNowResult struct {
	AccountID string `json:"account_id"`
	TaskID    string `json:"task_id"`
	Target    string `json:"target"`
	DelaySec  int    `json:"delay_sec"`
	SentAt    string `json:"sent_at"`
	TGMessage string `json:"tg_message,omitempty"`
}

type tgAssistantSessionMessagesRequest struct {
	AccountID string `json:"account_id"`
	Target    string `json:"target"`
	Limit     int    `json:"limit"`
	OffsetID  int    `json:"offset_id"`
}

type tgAssistantSessionSendRequest struct {
	AccountID string `json:"account_id"`
	Target    string `json:"target"`
	Message   string `json:"message"`
}

type tgAssistantSessionMessage struct {
	ID         int    `json:"id"`
	Date       string `json:"date"`
	Text       string `json:"text"`
	Out        bool   `json:"out"`
	SenderID   string `json:"sender_id,omitempty"`
	SenderName string `json:"sender_name,omitempty"`
	Service    bool   `json:"service,omitempty"`
	MediaType  string `json:"media_type,omitempty"`
	MediaPath  string `json:"media_path,omitempty"`
	MediaSize  int64  `json:"media_size,omitempty"`
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
			TGAssistantStore.data.Notify = normalizeTGAssistantNotifySettings(raw.Notify)
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
		BotAPIKey:   "",
		BotMode:     tgAssistantBotModePolling,
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

func loginTGAssistantAccountBySessionToken(req tgAssistantSessionTokenLoginRequest) (tgAssistantAccount, error) {
	if TGAssistantStore == nil {
		return tgAssistantAccount{}, errors.New("tg assistant datastore is not initialized")
	}

	sessionBytes, err := decodeTGAssistantSessionToken(req.SessionToken)
	if err != nil {
		return tgAssistantAccount{}, err
	}

	TGAssistantStore.mu.RLock()
	records := loadTGAssistantAccountsLocked()
	apiID, apiHash := loadTGAssistantAPIKeyLocked()
	TGAssistantStore.mu.RUnlock()
	if !isTGAssistantAPIKeyConfigured(apiID, apiHash) {
		return tgAssistantAccount{}, errors.New("shared tg api key is not configured")
	}

	now := time.Now().UTC().Format(time.RFC3339)
	record := tgAssistantAccountRecord{
		ID:          newTGAssistantAccountID(),
		Label:       strings.TrimSpace(req.Label),
		Phone:       "session",
		BotMode:     tgAssistantBotModePolling,
		Authorized:  false,
		CreatedAt:   now,
		UpdatedAt:   now,
		LastLoginAt: now,
	}
	if record.Label == "" {
		record.Label = "Session Token"
	}

	sessionPath := tgAssistantSessionPath(record.ID)
	if err := os.MkdirAll(filepath.Dir(sessionPath), 0o755); err != nil {
		return tgAssistantAccount{}, fmt.Errorf("failed to prepare tg session directory: %w", err)
	}
	if err := os.WriteFile(sessionPath, sessionBytes, 0o600); err != nil {
		return tgAssistantAccount{}, fmt.Errorf("failed to write tg session token: %w", err)
	}

	var status *tgauth.Status
	runErr := runTGAssistantClient(apiID, apiHash, record, func(ctx context.Context, client *telegram.Client) error {
		authStatus, err := client.Auth().Status(ctx)
		if err != nil {
			return err
		}
		if !authStatus.Authorized {
			return errors.New("session token is not authorized")
		}
		status = authStatus
		return nil
	})
	if runErr != nil {
		_ = os.Remove(sessionPath)
		appendTGAssistantHistory("account.token_login", "", false, runErr.Error())
		return tgAssistantAccount{}, runErr
	}

	record.Authorized = true
	record.LastError = "authorized"
	applyTGAssistantIdentityFromStatus(&record, status)
	if phone := normalizeTGPhone(record.SelfPhone); phone != "" {
		record.Phone = phone
	} else if record.SelfUserID > 0 {
		record.Phone = strconv.FormatInt(record.SelfUserID, 10)
	}
	if strings.TrimSpace(req.Label) == "" {
		record.Label = firstNonEmptyString(record.SelfDisplayName, record.SelfUsername, record.Phone, record.Label)
	}

	existingIndex := -1
	for i, item := range records {
		if record.SelfUserID > 0 && item.SelfUserID == record.SelfUserID {
			existingIndex = i
			break
		}
		if strings.TrimSpace(record.Phone) != "" && normalizeTGPhone(item.Phone) == normalizeTGPhone(record.Phone) {
			existingIndex = i
			break
		}
	}

	if existingIndex >= 0 {
		existing := records[existingIndex]
		existingSessionPath := tgAssistantSessionPath(existing.ID)
		if err := os.MkdirAll(filepath.Dir(existingSessionPath), 0o755); err != nil {
			_ = os.Remove(sessionPath)
			return tgAssistantAccount{}, fmt.Errorf("failed to prepare existing tg session directory: %w", err)
		}
		if err := os.WriteFile(existingSessionPath, sessionBytes, 0o600); err != nil {
			_ = os.Remove(sessionPath)
			return tgAssistantAccount{}, fmt.Errorf("failed to update existing tg session token: %w", err)
		}
		_ = os.Remove(sessionPath)
		existing.Authorized = true
		existing.LastError = "authorized"
		existing.UpdatedAt = now
		existing.LastLoginAt = now
		existing.SelfUserID = record.SelfUserID
		existing.SelfUsername = record.SelfUsername
		existing.SelfDisplayName = record.SelfDisplayName
		existing.SelfPhone = record.SelfPhone
		if strings.TrimSpace(record.Phone) != "" {
			existing.Phone = record.Phone
		}
		if strings.TrimSpace(req.Label) != "" {
			existing.Label = strings.TrimSpace(req.Label)
		}
		records[existingIndex] = existing
		record = existing
	} else {
		records = append(records, record)
	}

	TGAssistantStore.mu.Lock()
	TGAssistantStore.data.Accounts = normalizeTGAssistantAccountRecords(records)
	TGAssistantStore.mu.Unlock()
	if err := TGAssistantStore.Save(); err != nil {
		return tgAssistantAccount{}, err
	}
	appendTGAssistantHistory("account.token_login", record.ID, true, "authorized")
	return buildTGAssistantAccountView(record, apiID), nil
}

func decodeTGAssistantSessionToken(raw string) ([]byte, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return nil, errors.New("session_token is required")
	}
	if len(value) > 1024*1024 {
		return nil, errors.New("session_token is too large")
	}
	decoders := []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	}
	var lastErr error
	for _, decoder := range decoders {
		content, err := decoder.DecodeString(value)
		if err == nil && len(content) > 0 {
			return content, nil
		}
		if err != nil {
			lastErr = err
		}
	}
	if lastErr != nil {
		return nil, fmt.Errorf("session_token must be valid base64: %w", lastErr)
	}
	return nil, errors.New("session_token is empty")
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
	historyMsg := fmt.Sprintf(
		"task_id=%s type=%s target=%s enabled=%t send_at=%s delay=%d-%d",
		task.ID,
		task.TaskType,
		task.Target,
		task.Enabled,
		task.SendAt,
		task.DelayMin,
		task.DelayMax,
	)
	appendTGAssistantHistory(
		"schedule.add",
		accountID,
		true,
		historyMsg,
	)
	appendTGAssistantTaskHistory("schedule.add", accountID, task.ID, true, historyMsg)
	return buildTGAssistantScheduleViews(account.Schedules), nil
}

func updateTGAssistantSchedule(req tgAssistantScheduleUpdateRequest) ([]tgAssistantSchedule, error) {
	if TGAssistantStore == nil {
		return nil, errors.New("tg assistant datastore is not initialized")
	}

	accountID := strings.TrimSpace(req.AccountID)
	taskID := strings.TrimSpace(req.TaskID)
	taskType := strings.TrimSpace(req.TaskType)
	target := strings.TrimSpace(req.Target)
	sendAt := strings.TrimSpace(req.SendAt)
	message := strings.TrimSpace(req.Message)
	delayMin := req.DelayMin
	delayMax := req.DelayMax
	if accountID == "" {
		return nil, errors.New("account_id is required")
	}
	if taskID == "" {
		return nil, errors.New("task_id is required")
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
	task := account.Schedules[taskIndex]
	task.TaskType = taskType
	task.Enabled = req.Enabled
	task.Target = target
	task.SendAt = sendAt
	task.Message = message
	task.DelayMin = delayMin
	task.DelayMax = delayMax
	task.UpdatedAt = now
	account.Schedules[taskIndex] = task
	account.UpdatedAt = now
	account.Schedules = normalizeTGAssistantScheduleTaskRecords(account.Schedules)
	records[index] = account
	TGAssistantStore.data.Accounts = records
	TGAssistantStore.mu.Unlock()

	if err := TGAssistantStore.Save(); err != nil {
		return nil, err
	}
	historyMsg := fmt.Sprintf(
		"task_id=%s type=%s target=%s enabled=%t send_at=%s delay=%d-%d",
		task.ID,
		task.TaskType,
		task.Target,
		task.Enabled,
		task.SendAt,
		task.DelayMin,
		task.DelayMax,
	)
	appendTGAssistantHistory(
		"schedule.update",
		accountID,
		true,
		historyMsg,
	)
	appendTGAssistantTaskHistory("schedule.update", accountID, task.ID, true, historyMsg)
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
	historyMsg := fmt.Sprintf("task_id=%s", taskID)
	appendTGAssistantHistory("schedule.remove", accountID, true, historyMsg)
	appendTGAssistantTaskHistory("schedule.remove", accountID, taskID, true, historyMsg)
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
	historyMsg := fmt.Sprintf("task_id=%s enabled=%t", taskID, req.Enabled)
	appendTGAssistantHistory("schedule.set_enabled", accountID, true, historyMsg)
	appendTGAssistantTaskHistory("schedule.set_enabled", accountID, taskID, true, historyMsg)
	return buildTGAssistantScheduleViews(account.Schedules), nil
}

func sendNowTGAssistantSchedule(req tgAssistantScheduleSendNowRequest) (tgAssistantScheduleSendNowResult, error) {
	accountID := strings.TrimSpace(req.AccountID)
	taskID := strings.TrimSpace(req.TaskID)
	if accountID == "" {
		return tgAssistantScheduleSendNowResult{}, errors.New("account_id is required")
	}
	if taskID == "" {
		return tgAssistantScheduleSendNowResult{}, errors.New("task_id is required")
	}
	return executeTGAssistantScheduleSendTask(context.Background(), accountID, taskID, "schedule.send_now", 0)
}

func listTGAssistantSessionMessages(req tgAssistantSessionMessagesRequest) ([]tgAssistantSessionMessage, error) {
	accountID := strings.TrimSpace(req.AccountID)
	target := strings.TrimSpace(req.Target)
	if accountID == "" {
		return nil, errors.New("account_id is required")
	}
	if target == "" {
		return nil, errors.New("target is required")
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 40
	}
	if limit > 100 {
		limit = 100
	}

	storedMessages, err := listStoredTGAssistantSessionMessages(accountID, target, limit)
	if err != nil {
		log.Printf("tg stored history load failed: %v", err)
		storedMessages = []tgAssistantSessionMessage{}
	}
	if len(storedMessages) >= limit {
		return storedMessages, nil
	}
	offsetID := req.OffsetID
	if offsetID <= 0 && len(storedMessages) > 0 {
		offsetID = storedMessages[len(storedMessages)-1].ID
	}

	apiID, apiHash, account, err := loadTGAssistantClientConfig(accountID)
	if err != nil {
		if len(storedMessages) > 0 {
			return storedMessages, nil
		}
		return nil, err
	}

	messages := append([]tgAssistantSessionMessage(nil), storedMessages...)
	err = runTGAssistantClient(apiID, apiHash, account, func(ctx context.Context, client *telegram.Client) error {
		status, err := client.Auth().Status(ctx)
		if err != nil {
			return err
		}
		if !status.Authorized {
			return errors.New("account is not authorized")
		}

		peer, err := resolveTGAssistantInputPeer(ctx, client, target)
		if err != nil {
			return err
		}
		resp, err := client.API().MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
			Peer:       peer,
			OffsetID:   offsetID,
			OffsetDate: 0,
			AddOffset:  0,
			Limit:      limit,
			MaxID:      0,
			MinID:      0,
			Hash:       0,
		})
		if err != nil {
			return err
		}
		remoteMessages := buildTGAssistantSessionMessageViews(resp, account)
		enriched, err := downloadTGAssistantSessionVideos(ctx, client, accountID, target, resp, remoteMessages)
		remoteMessages = enriched
		if err != nil {
			log.Printf("tg video download failed: %v", err)
		}
		messages = mergeTGAssistantSessionMessages(messages, remoteMessages, limit)
		return nil
	})
	if err != nil {
		appendTGAssistantHistory("session.messages", accountID, false, err.Error())
		if len(messages) > 0 {
			return messages, nil
		}
		return nil, err
	}
	if err := storeTGAssistantSessionMessages(accountID, target, messages); err != nil {
		log.Printf("tg message store failed: %v", err)
	}
	appendTGAssistantHistory("session.messages", accountID, true, fmt.Sprintf("target=%s count=%d", target, len(messages)))
	return messages, nil
}

func sendTGAssistantSessionMessage(req tgAssistantSessionSendRequest) (tgAssistantSessionMessage, error) {
	accountID := strings.TrimSpace(req.AccountID)
	target := strings.TrimSpace(req.Target)
	message := strings.TrimSpace(req.Message)
	if accountID == "" {
		return tgAssistantSessionMessage{}, errors.New("account_id is required")
	}
	if target == "" {
		return tgAssistantSessionMessage{}, errors.New("target is required")
	}
	if message == "" {
		return tgAssistantSessionMessage{}, errors.New("message is required")
	}
	if len([]rune(message)) > 4000 {
		return tgAssistantSessionMessage{}, errors.New("message is too long")
	}

	apiID, apiHash, account, err := loadTGAssistantClientConfig(accountID)
	if err != nil {
		return tgAssistantSessionMessage{}, err
	}

	now := time.Now()
	result := tgAssistantSessionMessage{
		Date:       now.UTC().Format(time.RFC3339),
		Text:       message,
		Out:        true,
		SenderID:   fmt.Sprintf("user:%d", account.SelfUserID),
		SenderName: firstNonEmptyString(account.SelfDisplayName, account.SelfUsername, account.Label, account.Phone),
	}
	err = runTGAssistantClient(apiID, apiHash, account, func(ctx context.Context, client *telegram.Client) error {
		status, err := client.Auth().Status(ctx)
		if err != nil {
			return err
		}
		if !status.Authorized {
			return errors.New("account is not authorized")
		}

		peer, err := resolveTGAssistantInputPeer(ctx, client, target)
		if err != nil {
			return err
		}
		updates, err := client.API().MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{
			Peer:     peer,
			Message:  message,
			RandomID: newTGAssistantMessageRandomID(),
		})
		if err != nil {
			return err
		}
		result.ID = extractTGAssistantSentMessageID(updates)
		if echoed := summarizeTGAssistantSendUpdates(updates); strings.TrimSpace(echoed) != "" {
			result.Text = echoed
		}
		return nil
	})
	if err != nil {
		appendTGAssistantHistory("session.send", accountID, false, fmt.Sprintf("target=%s err=%s", target, err.Error()))
		return tgAssistantSessionMessage{}, err
	}
	if err := storeTGAssistantSessionMessages(accountID, target, []tgAssistantSessionMessage{result}); err != nil {
		log.Printf("tg message store failed: %v", err)
	}
	appendTGAssistantHistory("session.send", accountID, true, fmt.Sprintf("target=%s message_id=%d", target, result.ID))
	return result, nil
}

func mergeTGAssistantSessionMessages(base, incoming []tgAssistantSessionMessage, limit int) []tgAssistantSessionMessage {
	merged := make([]tgAssistantSessionMessage, 0, len(base)+len(incoming))
	seen := map[int]struct{}{}
	for _, item := range base {
		if item.ID <= 0 {
			continue
		}
		merged = append(merged, item)
		seen[item.ID] = struct{}{}
	}
	for _, item := range incoming {
		if item.ID <= 0 {
			continue
		}
		if _, ok := seen[item.ID]; ok {
			for idx := range merged {
				if merged[idx].ID == item.ID {
					merged[idx] = item
					break
				}
			}
			continue
		}
		merged = append(merged, item)
		seen[item.ID] = struct{}{}
	}
	sort.SliceStable(merged, func(i, j int) bool {
		if merged[i].ID == merged[j].ID {
			return merged[i].Date < merged[j].Date
		}
		return merged[i].ID < merged[j].ID
	})
	if limit > 0 && len(merged) > limit {
		merged = merged[len(merged)-limit:]
	}
	return merged
}

func listTGAssistantScheduleTaskHistory(req tgAssistantScheduleHistoryRequest) ([]tgAssistantTaskHistoryRecord, error) {
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

	TGAssistantStore.mu.RLock()
	records := loadTGAssistantAccountsLocked()
	index := indexTGAssistantAccountByID(records, accountID)
	if index < 0 {
		TGAssistantStore.mu.RUnlock()
		return nil, errors.New("account not found")
	}
	taskIndex := indexTGAssistantScheduleByID(records[index].Schedules, taskID)
	TGAssistantStore.mu.RUnlock()
	if taskIndex < 0 {
		return nil, errors.New("task not found")
	}

	history, err := loadTGAssistantTaskHistory(taskID)
	if err != nil {
		return nil, err
	}
	filtered := make([]tgAssistantTaskHistoryRecord, 0, len(history))
	for _, item := range history {
		if strings.TrimSpace(item.TaskID) != taskID {
			continue
		}
		if strings.TrimSpace(item.AccountID) != "" && strings.TrimSpace(item.AccountID) != accountID {
			continue
		}
		filtered = append(filtered, item)
	}

	limit := req.Limit
	if limit <= 0 {
		limit = tgAssistantTaskHistoryMax
	}
	if limit > tgAssistantTaskHistoryMax {
		limit = tgAssistantTaskHistoryMax
	}
	if len(filtered) > limit {
		filtered = append([]tgAssistantTaskHistoryRecord(nil), filtered[len(filtered)-limit:]...)
	}
	for left, right := 0, len(filtered)-1; left < right; left, right = left+1, right-1 {
		filtered[left], filtered[right] = filtered[right], filtered[left]
	}
	return filtered, nil
}

func executeTGAssistantScheduleSendTask(ctx context.Context, accountID, taskID, action string, delaySec int) (tgAssistantScheduleSendNowResult, error) {
	if TGAssistantStore == nil {
		return tgAssistantScheduleSendNowResult{}, errors.New("tg assistant datastore is not initialized")
	}
	if action = strings.TrimSpace(action); action == "" {
		action = "schedule.send"
	}

	normalizedAccountID := strings.TrimSpace(accountID)
	normalizedTaskID := strings.TrimSpace(taskID)
	if normalizedAccountID == "" {
		return tgAssistantScheduleSendNowResult{}, errors.New("account_id is required")
	}
	if normalizedTaskID == "" {
		return tgAssistantScheduleSendNowResult{}, errors.New("task_id is required")
	}

	TGAssistantStore.mu.RLock()
	records := loadTGAssistantAccountsLocked()
	apiID, apiHash := loadTGAssistantAPIKeyLocked()
	index := indexTGAssistantAccountByID(records, normalizedAccountID)
	if index < 0 {
		TGAssistantStore.mu.RUnlock()
		return tgAssistantScheduleSendNowResult{}, errors.New("account not found")
	}
	account := records[index]
	taskIndex := indexTGAssistantScheduleByID(account.Schedules, normalizedTaskID)
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

	tgResponseMessage := ""
	err := runTGAssistantClientWithContext(ctx, apiID, apiHash, account, func(inner context.Context, client *telegram.Client) error {
		status, err := client.Auth().Status(inner)
		if err != nil {
			return err
		}
		if !status.Authorized {
			return errors.New("account is not authorized")
		}

		peer, err := resolveTGAssistantInputPeer(inner, client, task.Target)
		if err != nil {
			return err
		}
		updates, err := client.API().MessagesSendMessage(inner, &tg.MessagesSendMessageRequest{
			Peer:     peer,
			Message:  task.Message,
			RandomID: newTGAssistantMessageRandomID(),
		})
		if err != nil {
			return err
		}
		tgResponseMessage = waitTGAssistantSendResponseMessage(inner, client, peer, updates, time.Now().Unix(), 5*time.Second)
		return nil
	})
	if err != nil {
		historyMsg := fmt.Sprintf(
			"task_id=%s err=%s tg_response=%s",
			normalizedTaskID,
			err.Error(),
			sanitizeTGAssistantHistoryText(tgResponseMessage, 240),
		)
		appendTGAssistantHistory(action, normalizedAccountID, false, historyMsg)
		appendTGAssistantTaskHistory(action, normalizedAccountID, normalizedTaskID, false, historyMsg)
		return tgAssistantScheduleSendNowResult{}, err
	}
	if strings.TrimSpace(tgResponseMessage) == "" {
		tgResponseMessage = "5秒内未收到TG回复"
	}

	result := tgAssistantScheduleSendNowResult{
		AccountID: normalizedAccountID,
		TaskID:    normalizedTaskID,
		Target:    task.Target,
		DelaySec:  delaySec,
		SentAt:    time.Now().UTC().Format(time.RFC3339),
		TGMessage: tgResponseMessage,
	}
	historyMsg := fmt.Sprintf(
		"task_id=%s target=%s delay=%d tg_response=%s",
		normalizedTaskID,
		task.Target,
		delaySec,
		sanitizeTGAssistantHistoryText(tgResponseMessage, 240),
	)
	appendTGAssistantHistory(action, normalizedAccountID, true, historyMsg)
	appendTGAssistantTaskHistory(action, normalizedAccountID, normalizedTaskID, true, historyMsg)
	return result, nil
}

func loadTGAssistantClientConfig(accountID string) (int, string, tgAssistantAccountRecord, error) {
	if TGAssistantStore == nil {
		return 0, "", tgAssistantAccountRecord{}, errors.New("tg assistant datastore is not initialized")
	}
	normalizedAccountID := strings.TrimSpace(accountID)
	if normalizedAccountID == "" {
		return 0, "", tgAssistantAccountRecord{}, errors.New("account_id is required")
	}

	TGAssistantStore.mu.RLock()
	records := loadTGAssistantAccountsLocked()
	apiID, apiHash := loadTGAssistantAPIKeyLocked()
	index := indexTGAssistantAccountByID(records, normalizedAccountID)
	if index < 0 {
		TGAssistantStore.mu.RUnlock()
		return 0, "", tgAssistantAccountRecord{}, errors.New("account not found")
	}
	account := records[index]
	TGAssistantStore.mu.RUnlock()

	if !isTGAssistantAPIKeyConfigured(apiID, apiHash) {
		return 0, "", tgAssistantAccountRecord{}, errors.New("shared tg api key is not configured")
	}
	if !account.Authorized {
		return 0, "", tgAssistantAccountRecord{}, errors.New("account is not authorized")
	}
	return apiID, apiHash, account, nil
}

type tgAssistantPeerInfo struct {
	ID       string
	Name     string
	Username string
	Type     string
}

func buildTGAssistantSessionMessageViews(resp tg.MessagesMessagesClass, account tgAssistantAccountRecord) []tgAssistantSessionMessage {
	users, chats := extractTGAssistantUsersChatsFromHistory(resp)
	peerInfo := buildTGAssistantPeerInfoMap(users, chats)
	selfInfo := tgAssistantPeerInfo{
		ID:       fmt.Sprintf("user:%d", account.SelfUserID),
		Name:     firstNonEmptyString(account.SelfDisplayName, account.SelfUsername, account.Label, account.Phone, "我"),
		Username: strings.TrimSpace(account.SelfUsername),
		Type:     "user",
	}

	views := make([]tgAssistantSessionMessage, 0, len(extractTGAssistantMessagesFromHistory(resp)))
	for _, raw := range extractTGAssistantMessagesFromHistory(resp) {
		switch msg := raw.(type) {
		case *tg.Message:
			if msg == nil {
				continue
			}
			sender := selfInfo
			if !msg.Out {
				if from, ok := msg.GetFromID(); ok {
					sender = lookupTGAssistantPeerInfo(peerInfo, from)
				} else {
					sender = lookupTGAssistantPeerInfo(peerInfo, msg.GetPeerID())
				}
			}
			text := summarizeTGAssistantSessionText(msg)
			if strings.TrimSpace(text) == "" {
				text = "[空消息]"
			}
			mediaType, mediaSize := detectTGAssistantMessageMedia(msg.Media)
			views = append(views, tgAssistantSessionMessage{
				ID:         msg.ID,
				Date:       formatTGAssistantUnixTime(msg.Date),
				Text:       text,
				Out:        msg.Out,
				SenderID:   sender.ID,
				SenderName: sender.Name,
				MediaType:  mediaType,
				MediaSize:  mediaSize,
			})
		case *tg.MessageService:
			if msg == nil {
				continue
			}
			sender := selfInfo
			if !msg.Out {
				if from, ok := msg.GetFromID(); ok {
					sender = lookupTGAssistantPeerInfo(peerInfo, from)
				} else {
					sender = lookupTGAssistantPeerInfo(peerInfo, msg.GetPeerID())
				}
			}
			views = append(views, tgAssistantSessionMessage{
				ID:         msg.ID,
				Date:       formatTGAssistantUnixTime(msg.Date),
				Text:       summarizeTGAssistantServiceMessage(msg),
				Out:        msg.Out,
				SenderID:   sender.ID,
				SenderName: sender.Name,
				Service:    true,
			})
		}
	}

	sort.SliceStable(views, func(i, j int) bool {
		if views[i].ID == views[j].ID {
			return views[i].Date < views[j].Date
		}
		return views[i].ID < views[j].ID
	})
	return views
}

func extractTGAssistantUsersChatsFromHistory(resp tg.MessagesMessagesClass) ([]tg.UserClass, []tg.ChatClass) {
	switch value := resp.(type) {
	case *tg.MessagesMessages:
		return value.Users, value.Chats
	case *tg.MessagesMessagesSlice:
		return value.Users, value.Chats
	case *tg.MessagesChannelMessages:
		return value.Users, value.Chats
	default:
		return nil, nil
	}
}

func buildTGAssistantPeerInfoMap(users []tg.UserClass, chats []tg.ChatClass) map[string]tgAssistantPeerInfo {
	result := map[string]tgAssistantPeerInfo{}
	for _, raw := range users {
		switch item := raw.(type) {
		case *tg.User:
			name := strings.TrimSpace(strings.TrimSpace(item.FirstName) + " " + strings.TrimSpace(item.LastName))
			name = firstNonEmptyString(name, item.Username, normalizeTGPhone(item.Phone), fmt.Sprintf("User %d", item.ID))
			result[fmt.Sprintf("user:%d", item.ID)] = tgAssistantPeerInfo{
				ID:       fmt.Sprintf("user:%d", item.ID),
				Name:     name,
				Username: strings.TrimSpace(item.Username),
				Type:     "user",
			}
		case *tg.UserEmpty:
			result[fmt.Sprintf("user:%d", item.ID)] = tgAssistantPeerInfo{
				ID:   fmt.Sprintf("user:%d", item.ID),
				Name: fmt.Sprintf("User %d", item.ID),
				Type: "user",
			}
		}
	}
	for _, raw := range chats {
		switch item := raw.(type) {
		case *tg.Chat:
			result[fmt.Sprintf("chat:%d", item.ID)] = tgAssistantPeerInfo{ID: fmt.Sprintf("chat:%d", item.ID), Name: firstNonEmptyString(item.Title, fmt.Sprintf("Chat %d", item.ID)), Type: "chat"}
		case *tg.ChatForbidden:
			result[fmt.Sprintf("chat:%d", item.ID)] = tgAssistantPeerInfo{ID: fmt.Sprintf("chat:%d", item.ID), Name: firstNonEmptyString(item.Title, fmt.Sprintf("Chat %d", item.ID)), Type: "chat"}
		case *tg.Channel:
			result[fmt.Sprintf("channel:%d", item.ID)] = tgAssistantPeerInfo{ID: fmt.Sprintf("channel:%d", item.ID), Name: firstNonEmptyString(item.Title, fmt.Sprintf("Channel %d", item.ID)), Username: strings.TrimSpace(item.Username), Type: "channel"}
		case *tg.ChannelForbidden:
			result[fmt.Sprintf("channel:%d", item.ID)] = tgAssistantPeerInfo{ID: fmt.Sprintf("channel:%d", item.ID), Name: firstNonEmptyString(item.Title, fmt.Sprintf("Channel %d", item.ID)), Type: "channel"}
		}
	}
	return result
}

func lookupTGAssistantPeerInfo(peers map[string]tgAssistantPeerInfo, peer tg.PeerClass) tgAssistantPeerInfo {
	key := formatTGAssistantPeerID(peer)
	if key != "" {
		if info, ok := peers[key]; ok {
			return info
		}
	}
	return tgAssistantPeerInfo{ID: key, Name: firstNonEmptyString(key, "未知发送者")}
}

func formatTGAssistantPeerID(peer tg.PeerClass) string {
	switch value := peer.(type) {
	case *tg.PeerUser:
		return fmt.Sprintf("user:%d", value.UserID)
	case *tg.PeerChat:
		return fmt.Sprintf("chat:%d", value.ChatID)
	case *tg.PeerChannel:
		return fmt.Sprintf("channel:%d", value.ChannelID)
	default:
		return ""
	}
}

func summarizeTGAssistantSessionText(msg *tg.Message) string {
	if msg == nil {
		return ""
	}
	if text := strings.TrimSpace(msg.Message); text != "" {
		return text
	}
	if msg.Media != nil {
		return formatTGAssistantMediaPlaceholder(msg.Media)
	}
	return ""
}

func formatTGAssistantMediaPlaceholder(media tg.MessageMediaClass) string {
	switch media.(type) {
	case nil, *tg.MessageMediaEmpty:
		return ""
	case *tg.MessageMediaPhoto:
		return "[图片]"
	case *tg.MessageMediaDocument:
		if mediaType, _ := detectTGAssistantMessageMedia(media); mediaType == "video" {
			return "[视频]"
		}
		return "[文件/媒体]"
	case *tg.MessageMediaContact:
		return "[联系人]"
	case *tg.MessageMediaGeo, *tg.MessageMediaGeoLive, *tg.MessageMediaVenue:
		return "[位置]"
	case *tg.MessageMediaPoll:
		return "[投票]"
	case *tg.MessageMediaDice:
		return "[骰子]"
	case *tg.MessageMediaWebPage:
		return "[网页预览]"
	default:
		return "[媒体消息]"
	}
}

func detectTGAssistantMessageMedia(media tg.MessageMediaClass) (string, int64) {
	switch value := media.(type) {
	case nil, *tg.MessageMediaEmpty:
		return "", 0
	case *tg.MessageMediaPhoto:
		return "photo", 0
	case *tg.MessageMediaDocument:
		document, ok := extractTGAssistantVideoDocument(value)
		if ok {
			return "video", document.Size
		}
		if _, ok := value.Document.(*tg.Document); !ok {
			return "document", 0
		}
		document = value.Document.(*tg.Document)
		return "document", document.Size
	default:
		return "", 0
	}
}

func extractTGAssistantVideoDocument(media tg.MessageMediaClass) (*tg.Document, bool) {
	documentMedia, ok := media.(*tg.MessageMediaDocument)
	if !ok || documentMedia == nil {
		return nil, false
	}
	document, ok := documentMedia.Document.(*tg.Document)
	if !ok || document == nil {
		return nil, false
	}
	for _, attr := range document.Attributes {
		if _, ok := attr.(*tg.DocumentAttributeVideo); ok {
			return document, true
		}
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(document.MimeType)), "video/") {
		return document, true
	}
	return nil, false
}

func downloadTGAssistantSessionVideos(ctx context.Context, client *telegram.Client, accountID, target string, resp tg.MessagesMessagesClass, views []tgAssistantSessionMessage) ([]tgAssistantSessionMessage, error) {
	if len(views) == 0 {
		return views, nil
	}
	byID := map[int]int{}
	for idx, item := range views {
		byID[item.ID] = idx
	}
	next := append([]tgAssistantSessionMessage(nil), views...)
	for _, raw := range extractTGAssistantMessagesFromHistory(resp) {
		msg, ok := raw.(*tg.Message)
		if !ok || msg == nil {
			continue
		}
		idx, ok := byID[msg.ID]
		if !ok {
			continue
		}
		document, ok := extractTGAssistantVideoDocument(msg.Media)
		if !ok {
			continue
		}
		path, err := ensureTGAssistantVideoFile(ctx, client, accountID, target, msg.ID, document)
		if err != nil {
			return next, err
		}
		next[idx].MediaType = "video"
		next[idx].MediaPath = path
		next[idx].MediaSize = document.Size
	}
	return next, nil
}

func ensureTGAssistantVideoFile(ctx context.Context, client *telegram.Client, accountID, target string, messageID int, document *tg.Document) (string, error) {
	if document == nil {
		return "", errors.New("video document is nil")
	}
	path := tgAssistantVideoFilePath(accountID, target, messageID, document.ID, document.MimeType)
	if info, err := os.Stat(path); err == nil && info.Size() > 0 {
		return path, nil
	} else if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	tmp := path + ".tmp"
	_ = os.Remove(tmp)
	location := &tg.InputDocumentFileLocation{
		ID:            document.ID,
		AccessHash:    document.AccessHash,
		FileReference: document.FileReference,
		ThumbSize:     "",
	}
	if _, err := client.Download(location).ToPath(ctx, tmp); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return path, nil
}

func summarizeTGAssistantServiceMessage(msg *tg.MessageService) string {
	if msg == nil {
		return "[服务消息]"
	}
	return "[服务消息]"
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func waitTGAssistantSendResponseMessage(
	ctx context.Context,
	client *telegram.Client,
	peer tg.InputPeerClass,
	updates tg.UpdatesClass,
	sentAtUnix int64,
	maxWait time.Duration,
) string {
	sentID := extractTGAssistantSentMessageID(updates)
	allowLooseMatch := isTGAssistantPrivateInputPeer(peer)
	if maxWait <= 0 {
		return ""
	}

	deadline := time.Now().Add(maxWait)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			break
		}
		resp, err := client.API().MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
			Peer:       peer,
			OffsetID:   0,
			OffsetDate: 0,
			AddOffset:  0,
			Limit:      20,
			MaxID:      0,
			MinID:      0,
			Hash:       0,
		})
		if err == nil {
			if value := extractTGAssistantIncomingReplyText(resp, sentID, sentAtUnix, allowLooseMatch); value != "" {
				return value
			}
		}

		wait := 250 * time.Millisecond
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		if remaining < wait {
			wait = remaining
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ""
		case <-timer.C:
		}
	}
	return ""
}

func extractTGAssistantIncomingReplyText(resp tg.MessagesMessagesClass, sentID int, sentAtUnix int64, allowLooseMatch bool) string {
	minUnix := sentAtUnix - 1
	if minUnix < 0 {
		minUnix = 0
	}
	bestReplyID := 0
	bestReplyText := ""
	bestLooseID := 0
	bestLooseText := ""
	for _, raw := range extractTGAssistantMessagesFromHistory(resp) {
		msg, ok := raw.(*tg.Message)
		if !ok || msg == nil || msg.Out {
			continue
		}
		if sentID > 0 && msg.ID <= sentID {
			continue
		}
		if sentAtUnix > 0 && int64(msg.Date) < minUnix {
			continue
		}
		value := summarizeTGAssistantTLMessage(msg)
		if strings.TrimSpace(value) == "" || value == "-" {
			continue
		}
		if isTGAssistantMessageReplyToID(msg, sentID) {
			if msg.ID > bestReplyID {
				bestReplyID = msg.ID
				bestReplyText = value
			}
			continue
		}
		if allowLooseMatch && msg.ID > bestLooseID {
			bestLooseID = msg.ID
			bestLooseText = value
		}
	}
	if bestReplyText != "" {
		return bestReplyText
	}
	return bestLooseText
}

func isTGAssistantPrivateInputPeer(peer tg.InputPeerClass) bool {
	switch peer.(type) {
	case *tg.InputPeerUser, *tg.InputPeerSelf:
		return true
	default:
		return false
	}
}

func isTGAssistantMessageReplyToID(msg *tg.Message, sentID int) bool {
	if msg == nil || sentID <= 0 {
		return false
	}
	replyClass, ok := msg.GetReplyTo()
	if !ok || replyClass == nil {
		return false
	}
	if replyHeader, ok := replyClass.(*tg.MessageReplyHeader); ok {
		replyID, hasReplyID := replyHeader.GetReplyToMsgID()
		return hasReplyID && replyID == sentID
	}
	if getter, ok := replyClass.(interface {
		GetReplyToMsgID() (int, bool)
	}); ok {
		replyID, hasReplyID := getter.GetReplyToMsgID()
		return hasReplyID && replyID == sentID
	}
	return false
}

func summarizeTGAssistantSendUpdates(updates tg.UpdatesClass) string {
	switch value := updates.(type) {
	case *tg.UpdateShortSentMessage:
		return ""
	case *tg.UpdateShortMessage:
		return sanitizeTGAssistantHistoryText(value.Message, 240)
	case *tg.UpdateShortChatMessage:
		return sanitizeTGAssistantHistoryText(value.Message, 240)
	case *tg.UpdateShort:
		return summarizeTGAssistantUpdate(value.Update)
	case *tg.Updates:
		return summarizeTGAssistantUpdatesList(value.Updates)
	case *tg.UpdatesCombined:
		return summarizeTGAssistantUpdatesList(value.Updates)
	default:
		return ""
	}
}

func summarizeTGAssistantUpdatesList(list []tg.UpdateClass) string {
	for _, item := range list {
		if value := summarizeTGAssistantUpdate(item); value != "" {
			return value
		}
	}
	return ""
}

func summarizeTGAssistantUpdate(update tg.UpdateClass) string {
	switch value := update.(type) {
	case *tg.UpdateNewMessage:
		return summarizeTGAssistantMessageClass(value.Message)
	case *tg.UpdateNewChannelMessage:
		return summarizeTGAssistantMessageClass(value.Message)
	case *tg.UpdateEditMessage:
		return summarizeTGAssistantMessageClass(value.Message)
	case *tg.UpdateEditChannelMessage:
		return summarizeTGAssistantMessageClass(value.Message)
	case *tg.UpdateMessageID:
		return ""
	default:
		return ""
	}
}

func summarizeTGAssistantMessageClass(message tg.MessageClass) string {
	switch value := message.(type) {
	case *tg.Message:
		return summarizeTGAssistantTLMessage(value)
	default:
		return ""
	}
}

func summarizeTGAssistantTLMessage(message *tg.Message) string {
	if message == nil {
		return ""
	}
	return sanitizeTGAssistantHistoryText(message.Message, 240)
}

func extractTGAssistantSentMessageID(updates tg.UpdatesClass) int {
	switch value := updates.(type) {
	case *tg.UpdateShortSentMessage:
		return value.ID
	case *tg.UpdateShortMessage:
		return value.ID
	case *tg.UpdateShortChatMessage:
		return value.ID
	case *tg.UpdateShort:
		return extractTGAssistantSentIDFromUpdate(value.Update)
	case *tg.Updates:
		return extractTGAssistantSentIDFromUpdates(value.Updates)
	case *tg.UpdatesCombined:
		return extractTGAssistantSentIDFromUpdates(value.Updates)
	default:
		return 0
	}
}

func extractTGAssistantSentIDFromUpdates(list []tg.UpdateClass) int {
	for _, item := range list {
		if id := extractTGAssistantSentIDFromUpdate(item); id > 0 {
			return id
		}
	}
	return 0
}

func extractTGAssistantSentIDFromUpdate(update tg.UpdateClass) int {
	switch value := update.(type) {
	case *tg.UpdateMessageID:
		return value.ID
	case *tg.UpdateNewMessage:
		if msg, ok := value.Message.(*tg.Message); ok {
			return msg.ID
		}
		return 0
	case *tg.UpdateNewChannelMessage:
		if msg, ok := value.Message.(*tg.Message); ok {
			return msg.ID
		}
		return 0
	case *tg.UpdateEditMessage:
		if msg, ok := value.Message.(*tg.Message); ok {
			return msg.ID
		}
		return 0
	case *tg.UpdateEditChannelMessage:
		if msg, ok := value.Message.(*tg.Message); ok {
			return msg.ID
		}
		return 0
	default:
		return 0
	}
}

func extractTGAssistantMessagesFromHistory(resp tg.MessagesMessagesClass) []tg.MessageClass {
	switch value := resp.(type) {
	case *tg.MessagesMessages:
		return value.Messages
	case *tg.MessagesMessagesSlice:
		return value.Messages
	case *tg.MessagesChannelMessages:
		return value.Messages
	default:
		return nil
	}
}

func sanitizeTGAssistantHistoryText(raw string, maxLen int) string {
	value := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(raw, "\r", " "), "\n", " "))
	if value == "" {
		return "-"
	}
	if maxLen <= 0 {
		return value
	}
	runes := []rune(value)
	if len(runes) <= maxLen {
		return value
	}
	return string(runes[:maxLen]) + "..."
}

func formatTGAssistantUnixTime(timestamp int) string {
	if timestamp <= 0 {
		return "-"
	}
	return time.Unix(int64(timestamp), 0).UTC().Format(time.RFC3339)
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
	return filterTGAssistantTargets(targets), nil
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

		dialogs, chats, users, err := fetchTGAssistantDialogs(ctx, client.API(), tgAssistantMainFolderID)
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

	targets = filterTGAssistantTargets(targets)
	if err := saveTGAssistantTargetsToFile(accountID, targets); err != nil {
		appendTGAssistantHistory("targets.refresh", accountID, false, err.Error())
		return nil, err
	}
	appendTGAssistantHistory("targets.refresh", accountID, true, fmt.Sprintf("count=%d", len(targets)))
	return targets, nil
}

func runTGAssistantClient(apiID int, apiHash string, record tgAssistantAccountRecord, fn func(ctx context.Context, client *telegram.Client) error) error {
	return runTGAssistantClientWithContext(context.Background(), apiID, apiHash, record, fn)
}

func runTGAssistantClientWithContext(parent context.Context, apiID int, apiHash string, record tgAssistantAccountRecord, fn func(ctx context.Context, client *telegram.Client) error) error {
	if parent == nil {
		parent = context.Background()
	}
	if apiID <= 0 {
		return errors.New("api_id must be a positive integer")
	}
	if strings.TrimSpace(apiHash) == "" {
		return errors.New("api_hash is required")
	}

	sessionPath := tgAssistantSessionPath(record.ID)
	if err := os.MkdirAll(filepath.Dir(sessionPath), 0o755); err != nil {
		return fmt.Errorf("failed to prepare tg session directory: %w", err)
	}

	client := telegram.NewClient(apiID, apiHash, telegram.Options{
		SessionStorage: &session.FileStorage{Path: sessionPath},
		NoUpdates:      true,
	})

	ctx, cancel := context.WithTimeout(parent, 45*time.Second)
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
		SessionToken:    loadTGAssistantSessionToken(record.ID),
		Schedules:       buildTGAssistantScheduleViews(record.Schedules),
	}

	_, pending := getTGAssistantLoginChallenge(record.ID)
	view.PendingCode = pending
	return view
}

func loadTGAssistantSessionToken(accountID string) string {
	normalizedAccountID := strings.TrimSpace(accountID)
	if normalizedAccountID == "" {
		return ""
	}
	content, err := os.ReadFile(tgAssistantSessionPath(normalizedAccountID))
	if err != nil || len(content) == 0 {
		return ""
	}
	return base64.StdEncoding.EncodeToString(content)
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

func normalizeTGAssistantNotifySettings(raw tgAssistantNotifySettings) tgAssistantNotifySettings {
	out := tgAssistantNotifySettings{
		NotifyAccountID:       strings.TrimSpace(raw.NotifyAccountID),
		EnableOffline:         raw.EnableOffline,
		EnableRenewal:         raw.EnableRenewal,
		OfflineDebounceSec:    raw.OfflineDebounceSec,
		RenewalCheckHour:      raw.RenewalCheckHour,
		LastControllerBaseURL: strings.TrimSpace(raw.LastControllerBaseURL),
	}
	if out.OfflineDebounceSec <= 0 {
		out.OfflineDebounceSec = tgAssistantNotifyDefaultDebounceSec
	}
	if out.OfflineDebounceSec > 3600 {
		out.OfflineDebounceSec = 3600
	}
	if out.RenewalCheckHour <= 0 || out.RenewalCheckHour > 23 {
		out.RenewalCheckHour = tgAssistantNotifyDefaultCheckHour
	}
	out.RenewalThresholds = normalizeTGAssistantRenewalThresholds(raw.RenewalThresholds)
	return out
}

func normalizeTGAssistantRenewalThresholds(values []int) []int {
	seen := map[int]struct{}{}
	out := make([]int, 0, len(values))
	for _, v := range values {
		if v <= 0 {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	if len(out) == 0 {
		out = append(out, tgAssistantNotifyDefaultThresholds...)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(out)))
	return out
}

func getTGAssistantNotifySettings() tgAssistantNotifySettings {
	if TGAssistantStore == nil {
		return normalizeTGAssistantNotifySettings(tgAssistantNotifySettings{})
	}
	TGAssistantStore.mu.RLock()
	raw := TGAssistantStore.data.Notify
	TGAssistantStore.mu.RUnlock()
	return normalizeTGAssistantNotifySettings(raw)
}

func setTGAssistantNotifySettings(req tgAssistantNotifySettingsRequest) (tgAssistantNotifySettings, error) {
	if TGAssistantStore == nil {
		return tgAssistantNotifySettings{}, errors.New("tg assistant datastore is not initialized")
	}

	notifyAccountID := strings.TrimSpace(req.NotifyAccountID)
	previousAccountID := ""

	TGAssistantStore.mu.Lock()
	if notifyAccountID != "" {
		records := loadTGAssistantAccountsLocked()
		if indexTGAssistantAccountByID(records, notifyAccountID) < 0 {
			TGAssistantStore.mu.Unlock()
			return tgAssistantNotifySettings{}, errors.New("notify account not found")
		}
	}
	current := normalizeTGAssistantNotifySettings(TGAssistantStore.data.Notify)
	previousAccountID = current.NotifyAccountID
	current.NotifyAccountID = notifyAccountID
	current.EnableOffline = req.EnableOffline
	current.EnableRenewal = req.EnableRenewal
	if req.OfflineDebounceSec > 0 {
		current.OfflineDebounceSec = req.OfflineDebounceSec
	}
	if req.RenewalCheckHour >= 0 && req.RenewalCheckHour <= 23 {
		current.RenewalCheckHour = req.RenewalCheckHour
	}
	current = normalizeTGAssistantNotifySettings(current)
	TGAssistantStore.data.Notify = current
	TGAssistantStore.mu.Unlock()

	if err := TGAssistantStore.Save(); err != nil {
		return tgAssistantNotifySettings{}, err
	}
	appendTGAssistantHistory("notify.settings.set", current.NotifyAccountID, true, fmt.Sprintf("offline=%t renewal=%t debounce=%d hour=%d", current.EnableOffline, current.EnableRenewal, current.OfflineDebounceSec, current.RenewalCheckHour))

	if current.NotifyAccountID != "" && current.NotifyAccountID != previousAccountID {
		registerTGAssistantNotifyBotCommands(current.NotifyAccountID)
	}
	return current, nil
}

func setTGAssistantLastControllerBaseURL(rawURL string) {
	if TGAssistantStore == nil {
		return
	}
	url := strings.TrimSpace(rawURL)
	if url == "" || isLoopbackControllerBaseURL(url) {
		return
	}
	TGAssistantStore.mu.Lock()
	if strings.TrimSpace(TGAssistantStore.data.Notify.LastControllerBaseURL) == url {
		TGAssistantStore.mu.Unlock()
		return
	}
	TGAssistantStore.data.Notify.LastControllerBaseURL = url
	TGAssistantStore.mu.Unlock()
	_ = TGAssistantStore.Save()
}

func isLoopbackControllerBaseURL(rawURL string) bool {
	lower := strings.ToLower(strings.TrimSpace(rawURL))
	return strings.Contains(lower, "127.0.0.1") || strings.Contains(lower, "localhost") || strings.Contains(lower, "[::1]")
}

// resolveNotifyBot returns the bot api key and target chat for the configured
// global notify account. ok is false when no account is selected, the account
// is missing/unauthorized, or its bot api key is not configured.
func resolveNotifyBot() (botAPIKey string, chatID int64, accountID string, ok bool) {
	if TGAssistantStore == nil {
		return "", 0, "", false
	}
	settings := getTGAssistantNotifySettings()
	accountID = settings.NotifyAccountID
	if accountID == "" {
		return "", 0, "", false
	}
	TGAssistantStore.mu.RLock()
	records := loadTGAssistantAccountsLocked()
	index := indexTGAssistantAccountByID(records, accountID)
	if index < 0 {
		TGAssistantStore.mu.RUnlock()
		return "", 0, "", false
	}
	record := records[index]
	TGAssistantStore.mu.RUnlock()

	botAPIKey = strings.TrimSpace(record.BotAPIKey)
	if botAPIKey == "" || !record.Authorized || record.SelfUserID == 0 {
		return "", 0, accountID, false
	}
	return botAPIKey, record.SelfUserID, accountID, true
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
		item.BotAPIKey = strings.TrimSpace(item.BotAPIKey)
		item.BotMode = normalizeTGAssistantBotMode(item.BotMode)
		item.BotWebhookPath = sanitizeTGAssistantBotWebhookPath(item.BotWebhookPath)
		item.BotWebhookToken = strings.TrimSpace(item.BotWebhookToken)
		if item.BotLastUpdateID < 0 {
			item.BotLastUpdateID = 0
		}
		if item.BotMode == tgAssistantBotModeWebhook && (item.BotWebhookPath == "" || item.BotWebhookToken == "") {
			item.BotMode = tgAssistantBotModePolling
		}
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

func tgAssistantLegacyTempDirPath() string {
	return filepath.Clean(tgAssistantLegacyTempDir)
}

func tgAssistantHistoryPath() string {
	return filepath.Join(tgAssistantTempDirPath(), tgAssistantHistoryFile)
}

func tgAssistantTaskHistoryDirPath() string {
	return filepath.Join(tgAssistantTempDirPath(), tgAssistantTaskHistoryDir)
}

func tgAssistantTaskHistoryPath(taskID string) string {
	safeID := strings.TrimSpace(taskID)
	if safeID == "" {
		safeID = "unknown"
	}
	safeID = strings.ReplaceAll(safeID, "/", "_")
	safeID = strings.ReplaceAll(safeID, "\\", "_")
	return filepath.Join(tgAssistantTaskHistoryDirPath(), safeID+".json")
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

func appendTGAssistantTaskHistory(action, accountID, taskID string, success bool, message string) {
	record := tgAssistantTaskHistoryRecord{
		Time:      time.Now().UTC().Format(time.RFC3339),
		Action:    strings.TrimSpace(action),
		AccountID: strings.TrimSpace(accountID),
		TaskID:    strings.TrimSpace(taskID),
		Success:   success,
		Message:   strings.TrimSpace(message),
	}
	if record.Action == "" || record.TaskID == "" {
		return
	}

	tgAssistantTaskHistoryMu.Lock()
	defer tgAssistantTaskHistoryMu.Unlock()

	if err := os.MkdirAll(tgAssistantTaskHistoryDirPath(), 0o755); err != nil {
		log.Printf("tg task history mkdir failed: %v", err)
		return
	}

	path := tgAssistantTaskHistoryPath(record.TaskID)
	records := make([]tgAssistantTaskHistoryRecord, 0, tgAssistantTaskHistoryMax)
	content, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("tg task history read failed: %v", err)
			return
		}
	} else if len(strings.TrimSpace(string(content))) > 0 {
		if err := json.Unmarshal(content, &records); err != nil {
			log.Printf("tg task history parse failed: %v", err)
			records = make([]tgAssistantTaskHistoryRecord, 0, tgAssistantTaskHistoryMax)
		}
	}

	records = append(records, record)
	if len(records) > tgAssistantTaskHistoryMax {
		records = append([]tgAssistantTaskHistoryRecord(nil), records[len(records)-tgAssistantTaskHistoryMax:]...)
	}

	next, err := json.Marshal(records)
	if err != nil {
		log.Printf("tg task history marshal failed: %v", err)
		return
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, next, 0o644); err != nil {
		log.Printf("tg task history write tmp failed: %v", err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		log.Printf("tg task history rename failed: %v", err)
	}
}

func loadTGAssistantTaskHistory(taskID string) ([]tgAssistantTaskHistoryRecord, error) {
	normalizedTaskID := strings.TrimSpace(taskID)
	if normalizedTaskID == "" {
		return nil, errors.New("task_id is required")
	}

	tgAssistantTaskHistoryMu.Lock()
	defer tgAssistantTaskHistoryMu.Unlock()

	path := tgAssistantTaskHistoryPath(normalizedTaskID)
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []tgAssistantTaskHistoryRecord{}, nil
		}
		return nil, err
	}
	if len(strings.TrimSpace(string(content))) == 0 {
		return []tgAssistantTaskHistoryRecord{}, nil
	}

	records := make([]tgAssistantTaskHistoryRecord, 0, tgAssistantTaskHistoryMax)
	if err := json.Unmarshal(content, &records); err != nil {
		return nil, err
	}
	return records, nil
}

func parseTGDialogsResponse(resp tg.MessagesDialogsClass) ([]tg.DialogClass, []tg.ChatClass, []tg.UserClass, error) {
	dialogs, chats, users, _, err := parseTGDialogsResponseWithMessages(resp)
	return dialogs, chats, users, err
}

func parseTGDialogsResponseWithMessages(resp tg.MessagesDialogsClass) ([]tg.DialogClass, []tg.ChatClass, []tg.UserClass, []tg.MessageClass, error) {
	switch value := resp.(type) {
	case *tg.MessagesDialogs:
		return value.Dialogs, value.Chats, value.Users, value.Messages, nil
	case *tg.MessagesDialogsSlice:
		return value.Dialogs, value.Chats, value.Users, value.Messages, nil
	case *tg.MessagesDialogsNotModified:
		return []tg.DialogClass{}, []tg.ChatClass{}, []tg.UserClass{}, []tg.MessageClass{}, nil
	default:
		return nil, nil, nil, nil, fmt.Errorf("unexpected dialogs response: %T", resp)
	}
}

func fetchTGAssistantDialogs(ctx context.Context, api *tg.Client, folderID int) ([]tg.DialogClass, []tg.ChatClass, []tg.UserClass, error) {
	if api == nil {
		return nil, nil, nil, errors.New("tg api client is nil")
	}
	allDialogs := make([]tg.DialogClass, 0, tgAssistantDialogPageLimit)
	allChats := make([]tg.ChatClass, 0, tgAssistantDialogPageLimit)
	allUsers := make([]tg.UserClass, 0, tgAssistantDialogPageLimit)
	offsetPeer := tg.InputPeerClass(&tg.InputPeerEmpty{})
	offsetID := 0
	offsetDate := 0
	seenOffsets := map[string]struct{}{}

	for page := 0; page < tgAssistantDialogMaxPages; page++ {
		req := &tg.MessagesGetDialogsRequest{
			OffsetDate: offsetDate,
			OffsetID:   offsetID,
			OffsetPeer: offsetPeer,
			Limit:      tgAssistantDialogPageLimit,
			Hash:       0,
		}
		req.SetFolderID(folderID)
		resp, err := api.MessagesGetDialogs(ctx, req)
		if err != nil {
			return nil, nil, nil, err
		}

		dialogs, chats, users, messages, err := parseTGDialogsResponseWithMessages(resp)
		if err != nil {
			return nil, nil, nil, err
		}
		if len(dialogs) == 0 {
			break
		}

		allDialogs = append(allDialogs, dialogs...)
		allChats = append(allChats, chats...)
		allUsers = append(allUsers, users...)
		if len(dialogs) < tgAssistantDialogPageLimit {
			break
		}

		nextPeer, nextOffsetID, nextOffsetDate, ok := nextTGAssistantDialogOffset(dialogs, chats, users, messages)
		if !ok || nextOffsetID <= 0 {
			break
		}
		offsetKey := fmt.Sprintf("%T:%d:%d", nextPeer, nextOffsetID, nextOffsetDate)
		if _, exists := seenOffsets[offsetKey]; exists {
			break
		}
		seenOffsets[offsetKey] = struct{}{}
		offsetPeer = nextPeer
		offsetID = nextOffsetID
		offsetDate = nextOffsetDate
	}

	return allDialogs, allChats, allUsers, nil
}

func nextTGAssistantDialogOffset(dialogs []tg.DialogClass, chats []tg.ChatClass, users []tg.UserClass, messages []tg.MessageClass) (tg.InputPeerClass, int, int, bool) {
	if len(dialogs) == 0 {
		return nil, 0, 0, false
	}
	var lastDialog *tg.Dialog
	for idx := len(dialogs) - 1; idx >= 0; idx-- {
		if dialog, ok := dialogs[idx].(*tg.Dialog); ok && dialog != nil {
			lastDialog = dialog
			break
		}
	}
	if lastDialog == nil {
		return nil, 0, 0, false
	}
	offsetPeer, ok := tgAssistantInputPeerFromPeer(lastDialog.Peer, chats, users)
	if !ok {
		return nil, 0, 0, false
	}
	offsetDate := 0
	topMessageID := lastDialog.TopMessage
	if topMessageID > 0 {
		for _, raw := range messages {
			if raw == nil || raw.GetID() != topMessageID {
				continue
			}
			offsetDate = tgAssistantMessageDate(raw)
			break
		}
	}
	return offsetPeer, topMessageID, offsetDate, true
}

func tgAssistantMessageDate(raw tg.MessageClass) int {
	switch item := raw.(type) {
	case *tg.Message:
		return item.Date
	case *tg.MessageService:
		return item.Date
	default:
		return 0
	}
}

func tgAssistantInputPeerFromPeer(peer tg.PeerClass, chats []tg.ChatClass, users []tg.UserClass) (tg.InputPeerClass, bool) {
	switch value := peer.(type) {
	case *tg.PeerUser:
		for _, raw := range users {
			switch item := raw.(type) {
			case *tg.User:
				if item.ID == value.UserID {
					return item.AsInputPeer(), true
				}
			}
		}
	case *tg.PeerChat:
		for _, raw := range chats {
			switch item := raw.(type) {
			case *tg.Chat:
				if item.ID == value.ChatID {
					return item.AsInputPeer(), true
				}
			}
		}
	case *tg.PeerChannel:
		for _, raw := range chats {
			switch item := raw.(type) {
			case *tg.Channel:
				if item.ID == value.ChannelID {
					return item.AsInputPeer(), true
				}
			case *tg.ChannelForbidden:
				if item.ID == value.ChannelID {
					return &tg.InputPeerChannel{ChannelID: item.ID, AccessHash: item.AccessHash}, true
				}
			}
		}
	}
	return nil, false
}

func resolveTGAssistantInputPeer(ctx context.Context, client *telegram.Client, target string) (tg.InputPeerClass, error) {
	targetType, targetID, err := parseTGAssistantTarget(target)
	if err != nil {
		return nil, err
	}

	_, chats, users, err := fetchTGAssistantDialogs(ctx, client.API(), tgAssistantMainFolderID)
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
		archived := false
		if folderID, ok := dialog.GetFolderID(); ok && folderID == tgAssistantArchivedFolderID {
			archived = true
		}
		switch peer := dialog.Peer.(type) {
		case *tg.PeerUser:
			if item, ok := userMap[peer.UserID]; ok {
				item.Archived = archived
				appendTarget(item)
			} else {
				appendTarget(tgAssistantTarget{ID: fmt.Sprintf("user:%d", peer.UserID), Name: fmt.Sprintf("User %d", peer.UserID), Type: "user", Archived: archived})
			}
		case *tg.PeerChat:
			if item, ok := chatMap[peer.ChatID]; ok {
				item.Archived = archived
				appendTarget(item)
			} else {
				appendTarget(tgAssistantTarget{ID: fmt.Sprintf("chat:%d", peer.ChatID), Name: fmt.Sprintf("Chat %d", peer.ChatID), Type: "chat", Archived: archived})
			}
		case *tg.PeerChannel:
			if item, ok := channelMap[peer.ChannelID]; ok {
				item.Archived = archived
				appendTarget(item)
			} else {
				appendTarget(tgAssistantTarget{ID: fmt.Sprintf("channel:%d", peer.ChannelID), Name: fmt.Sprintf("Channel %d", peer.ChannelID), Type: "channel", Archived: archived})
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
	return filterTGAssistantTargets(normalized), nil
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

func filterTGAssistantTargets(targets []tgAssistantTarget) []tgAssistantTarget {
	if len(targets) == 0 {
		return []tgAssistantTarget{}
	}
	filtered := make([]tgAssistantTarget, 0, len(targets))
	for _, item := range targets {
		if item.Archived {
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered
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
	newDir := filepath.Join(dataDir, tgAssistantSessionDirName)
	if err := os.MkdirAll(newDir, 0o755); err != nil {
		log.Printf("create tg session dir in data failed: %v", err)
		return
	}

	sourceDirs := []string{
		filepath.Join(tgAssistantTempDirPath(), tgAssistantSessionDirName),
		filepath.Join(tgAssistantLegacyTempDirPath(), tgAssistantSessionDirName),
	}
	seenDir := map[string]struct{}{}
	for _, oldDir := range sourceDirs {
		normalized := filepath.Clean(oldDir)
		if _, ok := seenDir[normalized]; ok {
			continue
		}
		seenDir[normalized] = struct{}{}

		entries, err := os.ReadDir(normalized)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			log.Printf("list legacy tg session dir failed: %v", err)
			continue
		}

		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := strings.TrimSpace(entry.Name())
			if name == "" || !strings.HasSuffix(strings.ToLower(name), ".json") {
				continue
			}

			src := filepath.Join(normalized, name)
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
