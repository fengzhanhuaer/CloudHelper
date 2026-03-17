package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
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
	tgAssistantStoreFile      = "tg.json"
	tgAssistantSessionDirName = "tg_sessions"
	tgAssistantLoginCodeTTL   = 10 * time.Minute
)

var (
	errTGAssistantPasswordRequired = errors.New("password is required for 2FA account")
)

type tgAssistantAccountRecord struct {
	ID              string `json:"id"`
	Label           string `json:"label"`
	Phone           string `json:"phone"`
	Authorized      bool   `json:"authorized"`
	LastError       string `json:"last_error"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
	LastLoginAt     string `json:"last_login_at"`
	SelfUserID      int64  `json:"self_user_id"`
	SelfUsername    string `json:"self_username"`
	SelfDisplayName string `json:"self_display_name"`
	SelfPhone       string `json:"self_phone"`
}

type tgAssistantAccount struct {
	ID              string `json:"id"`
	Label           string `json:"label"`
	Phone           string `json:"phone"`
	APIID           int    `json:"api_id"`
	Authorized      bool   `json:"authorized"`
	PendingCode     bool   `json:"pending_code"`
	LastError       string `json:"last_error"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
	LastLoginAt     string `json:"last_login_at"`
	SelfUserID      int64  `json:"self_user_id"`
	SelfUsername    string `json:"self_username"`
	SelfDisplayName string `json:"self_display_name"`
	SelfPhone       string `json:"self_phone"`
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

func initTGAssistantStore() {
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

	log.Println("TG assistant datastore initialized at", storePath)
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
		return tgAssistantAccount{}, runErr
	}

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
		return tgAssistantAccount{}, runErr
	}

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

	return buildTGAssistantAccountView(record, apiID), nil
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

func indexTGAssistantAccountByID(records []tgAssistantAccountRecord, accountID string) int {
	for i, item := range records {
		if item.ID == accountID {
			return i
		}
	}
	return -1
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

func newTGAssistantAccountID() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "tg-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	return "tg-" + hex.EncodeToString(buf)
}

func tgAssistantSessionPath(accountID string) string {
	safeID := strings.TrimSpace(accountID)
	if safeID == "" {
		safeID = "unknown"
	}
	return filepath.Join(dataDir, tgAssistantSessionDirName, safeID+".json")
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
