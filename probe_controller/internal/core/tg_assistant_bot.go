package core

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	tgAssistantBotManagerInterval   = 2 * time.Second
	tgAssistantBotErrorBackoff      = 2 * time.Second
	tgAssistantBotHTTPTimeout       = 75 * time.Second
	tgAssistantBotLongTimeout       = 50
	tgAssistantBotModePolling       = "polling"
	tgAssistantBotModeWebhook       = "webhook"
	tgAssistantBotWebhookBaseURLEnv = "TG_BOT_WEBHOOK_BASE_URL"
	tgAssistantBotWebhookPathPrefix = "/api/tg/"
	tgAssistantBotWebhookHeader     = "X-Telegram-Bot-Api-Secret-Token"
)

type tgAssistantBotAPIKeyRequest struct {
	AccountID string `json:"account_id"`
	APIKey    string `json:"api_key"`
	Mode      string `json:"mode,omitempty"`
}

type tgAssistantBotAPIKey struct {
	AccountID      string `json:"account_id"`
	APIKey         string `json:"api_key"`
	Configured     bool   `json:"configured"`
	Mode           string `json:"mode,omitempty"`
	WebhookPath    string `json:"webhook_path,omitempty"`
	WebhookEnabled bool   `json:"webhook_enabled,omitempty"`
}

type tgAssistantBotTestSendRequest struct {
	AccountID string `json:"account_id"`
	Message   string `json:"message"`
}

type tgAssistantBotTestSendResult struct {
	AccountID string `json:"account_id"`
	ChatID    int64  `json:"chat_id"`
	MessageID int    `json:"message_id"`
	Message   string `json:"message"`
	SentAt    string `json:"sent_at"`
}

type telegramBotGetUpdatesRequest struct {
	Offset         int `json:"offset,omitempty"`
	Limit          int `json:"limit,omitempty"`
	TimeoutSeconds int `json:"timeout,omitempty"`
}

type telegramBotSetWebhookRequest struct {
	URL         string `json:"url"`
	SecretToken string `json:"secret_token,omitempty"`
}

type telegramBotDeleteWebhookRequest struct {
	DropPendingUpdates bool `json:"drop_pending_updates,omitempty"`
}

type telegramBotSendMessageRequest struct {
	ChatID string `json:"chat_id"`
	Text   string `json:"text"`
}

type telegramBotAPIResponse struct {
	OK          bool            `json:"ok"`
	Description string          `json:"description"`
	Result      json.RawMessage `json:"result"`
	Parameters  map[string]any  `json:"parameters,omitempty"`
}

type telegramBotUpdate struct {
	UpdateID      int                 `json:"update_id"`
	Message       *telegramBotMessage `json:"message,omitempty"`
	EditedMessage *telegramBotMessage `json:"edited_message,omitempty"`
}

type telegramBotMessage struct {
	MessageID int              `json:"message_id"`
	Text      string           `json:"text"`
	Chat      telegramBotChat  `json:"chat"`
	From      *telegramBotUser `json:"from,omitempty"`
	Date      int              `json:"date,omitempty"`
}

type telegramBotChat struct {
	ID int64 `json:"id"`
}

type telegramBotUser struct {
	ID int64 `json:"id"`
}

type tgAssistantBotPollAccount struct {
	AccountID     string
	BotAPIKey     string
	AllowedChatID int64
	NextUpdateID  int
}

var tgAssistantBotEngine = struct {
	mu      sync.Mutex
	started bool
	workers map[string]chan struct{}
}{
	workers: map[string]chan struct{}{},
}

func initTGAssistantBotEngine() {
	tgAssistantBotEngine.mu.Lock()
	if tgAssistantBotEngine.started {
		tgAssistantBotEngine.mu.Unlock()
		return
	}
	tgAssistantBotEngine.started = true
	tgAssistantBotEngine.mu.Unlock()

	go runTGAssistantBotEngine()
	log.Println("tg assistant bot engine started")
}

func runTGAssistantBotEngine() {
	ticker := time.NewTicker(tgAssistantBotManagerInterval)
	defer ticker.Stop()

	reconcileTGAssistantBotWorkers()
	for range ticker.C {
		reconcileTGAssistantBotWorkers()
	}
}

func reconcileTGAssistantBotWorkers() {
	accounts := collectTGAssistantBotPollAccounts()
	desired := make(map[string]struct{}, len(accounts))
	for _, item := range accounts {
		accountID := strings.TrimSpace(item.AccountID)
		if accountID == "" {
			continue
		}
		desired[accountID] = struct{}{}
	}

	tgAssistantBotEngine.mu.Lock()
	for accountID, stopCh := range tgAssistantBotEngine.workers {
		if _, ok := desired[accountID]; ok {
			continue
		}
		close(stopCh)
		delete(tgAssistantBotEngine.workers, accountID)
		log.Printf("tg bot worker stopped: account=%s", accountID)
	}
	for accountID := range desired {
		if _, ok := tgAssistantBotEngine.workers[accountID]; ok {
			continue
		}
		stopCh := make(chan struct{})
		tgAssistantBotEngine.workers[accountID] = stopCh
		go runTGAssistantBotWorker(accountID, stopCh)
		log.Printf("tg bot worker started: account=%s", accountID)
	}
	tgAssistantBotEngine.mu.Unlock()
}

func runTGAssistantBotWorker(accountID string, stopCh <-chan struct{}) {
	normalizedAccountID := strings.TrimSpace(accountID)
	if normalizedAccountID == "" {
		return
	}

	for {
		select {
		case <-stopCh:
			return
		default:
		}

		account, ok := findTGAssistantBotPollAccount(normalizedAccountID)
		if !ok {
			select {
			case <-stopCh:
				return
			case <-time.After(tgAssistantBotErrorBackoff):
			}
			continue
		}

		if err := pollOneTGAssistantBotAccount(account); err != nil {
			log.Printf("tg bot poll failed: account=%s err=%v", normalizedAccountID, err)
			select {
			case <-stopCh:
				return
			case <-time.After(tgAssistantBotErrorBackoff):
			}
			continue
		}
	}
}

func collectTGAssistantBotPollAccounts() []tgAssistantBotPollAccount {
	if TGAssistantStore == nil {
		return nil
	}
	TGAssistantStore.mu.RLock()
	accounts := loadTGAssistantAccountsLocked()
	TGAssistantStore.mu.RUnlock()

	result := make([]tgAssistantBotPollAccount, 0, len(accounts))
	for _, item := range accounts {
		if normalizeTGAssistantBotMode(item.BotMode) == tgAssistantBotModeWebhook {
			continue
		}
		accountID := strings.TrimSpace(item.ID)
		botAPIKey := strings.TrimSpace(item.BotAPIKey)
		if accountID == "" || botAPIKey == "" {
			continue
		}
		if !item.Authorized || item.SelfUserID == 0 {
			continue
		}
		result = append(result, tgAssistantBotPollAccount{
			AccountID:     accountID,
			BotAPIKey:     botAPIKey,
			AllowedChatID: item.SelfUserID,
			NextUpdateID:  item.BotLastUpdateID,
		})
	}
	return result
}

func findTGAssistantBotPollAccount(accountID string) (tgAssistantBotPollAccount, bool) {
	normalizedAccountID := strings.TrimSpace(accountID)
	if normalizedAccountID == "" || TGAssistantStore == nil {
		return tgAssistantBotPollAccount{}, false
	}

	TGAssistantStore.mu.RLock()
	accounts := loadTGAssistantAccountsLocked()
	TGAssistantStore.mu.RUnlock()

	for _, item := range accounts {
		if strings.TrimSpace(item.ID) != normalizedAccountID {
			continue
		}
		if normalizeTGAssistantBotMode(item.BotMode) == tgAssistantBotModeWebhook {
			return tgAssistantBotPollAccount{}, false
		}
		botAPIKey := strings.TrimSpace(item.BotAPIKey)
		if botAPIKey == "" || !item.Authorized || item.SelfUserID == 0 {
			return tgAssistantBotPollAccount{}, false
		}
		return tgAssistantBotPollAccount{
			AccountID:     normalizedAccountID,
			BotAPIKey:     botAPIKey,
			AllowedChatID: item.SelfUserID,
			NextUpdateID:  item.BotLastUpdateID,
		}, true
	}
	return tgAssistantBotPollAccount{}, false
}

func findTGAssistantBotWebhookAccount(pathToken string) (tgAssistantBotPollAccount, string, bool) {
	normalizedPathToken := sanitizeTGAssistantBotWebhookPath(pathToken)
	if normalizedPathToken == "" || TGAssistantStore == nil {
		return tgAssistantBotPollAccount{}, "", false
	}

	TGAssistantStore.mu.RLock()
	accounts := loadTGAssistantAccountsLocked()
	TGAssistantStore.mu.RUnlock()

	for _, item := range accounts {
		if normalizeTGAssistantBotMode(item.BotMode) != tgAssistantBotModeWebhook {
			continue
		}
		if sanitizeTGAssistantBotWebhookPath(item.BotWebhookPath) != normalizedPathToken {
			continue
		}
		botAPIKey := strings.TrimSpace(item.BotAPIKey)
		webhookToken := strings.TrimSpace(item.BotWebhookToken)
		if botAPIKey == "" || webhookToken == "" {
			return tgAssistantBotPollAccount{}, "", false
		}
		if !item.Authorized || item.SelfUserID == 0 {
			return tgAssistantBotPollAccount{}, "", false
		}
		return tgAssistantBotPollAccount{
			AccountID:     strings.TrimSpace(item.ID),
			BotAPIKey:     botAPIKey,
			AllowedChatID: item.SelfUserID,
			NextUpdateID:  item.BotLastUpdateID,
		}, webhookToken, true
	}
	return tgAssistantBotPollAccount{}, "", false
}

func TGAssistantBotWebhookHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	pathToken := strings.TrimSpace(strings.TrimPrefix(r.URL.Path, tgAssistantBotWebhookPathPrefix))
	pathToken = strings.Trim(pathToken, "/")
	if pathToken == "" {
		http.NotFound(w, r)
		return
	}

	account, secretToken, ok := findTGAssistantBotWebhookAccount(pathToken)
	if !ok {
		http.NotFound(w, r)
		return
	}

	receivedSecret := strings.TrimSpace(r.Header.Get(tgAssistantBotWebhookHeader))
	if secretToken != "" && subtle.ConstantTimeCompare([]byte(receivedSecret), []byte(secretToken)) != 1 {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	var update telegramBotUpdate
	if err := json.Unmarshal(body, &update); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	handleTGAssistantBotUpdate(ctx, account, update, "bot.webhook.auto_reply")
	if update.UpdateID > 0 {
		_ = setTGAssistantBotLastUpdateID(account.AccountID, update.UpdateID+1)
	}

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

func pollOneTGAssistantBotAccount(item tgAssistantBotPollAccount) error {
	ctx, cancel := context.WithTimeout(context.Background(), tgAssistantBotHTTPTimeout)
	defer cancel()

	req := telegramBotGetUpdatesRequest{
		Offset:         item.NextUpdateID,
		Limit:          50,
		TimeoutSeconds: tgAssistantBotLongTimeout,
	}
	var updates []telegramBotUpdate
	if err := callTelegramBotAPI(ctx, item.BotAPIKey, "getUpdates", req, &updates); err != nil {
		return err
	}
	if len(updates) == 0 {
		return nil
	}

	nextOffset := item.NextUpdateID
	for _, update := range updates {
		if update.UpdateID+1 > nextOffset {
			nextOffset = update.UpdateID + 1
		}
		handleTGAssistantBotUpdate(ctx, item, update, "bot.auto_reply")
	}

	if nextOffset > item.NextUpdateID {
		return setTGAssistantBotLastUpdateID(item.AccountID, nextOffset)
	}
	return nil
}

func handleTGAssistantBotUpdate(ctx context.Context, item tgAssistantBotPollAccount, update telegramBotUpdate, action string) {
	msg := update.Message
	if msg == nil {
		msg = update.EditedMessage
	}
	if msg == nil {
		return
	}
	text := strings.TrimSpace(msg.Text)
	if text != "/ping" {
		return
	}
	if msg.Chat.ID != item.AllowedChatID {
		return
	}

	if _, _, err := sendTGAssistantBotTextMessage(ctx, item.BotAPIKey, msg.Chat.ID, "/pong"); err != nil {
		appendTGAssistantHistory(action, item.AccountID, false, fmt.Sprintf("chat_id=%d err=%s", msg.Chat.ID, err.Error()))
		return
	}
	appendTGAssistantHistory(action, item.AccountID, true, fmt.Sprintf("chat_id=%d text=/pong", msg.Chat.ID))
}

func getTGAssistantBotAPIKey(req tgAssistantAccountIDRequest) (tgAssistantBotAPIKey, error) {
	if TGAssistantStore == nil {
		return tgAssistantBotAPIKey{}, errors.New("tg assistant datastore is not initialized")
	}

	accountID := strings.TrimSpace(req.AccountID)
	if accountID == "" {
		return tgAssistantBotAPIKey{}, errors.New("account_id is required")
	}

	TGAssistantStore.mu.RLock()
	records := loadTGAssistantAccountsLocked()
	index := indexTGAssistantAccountByID(records, accountID)
	if index < 0 {
		TGAssistantStore.mu.RUnlock()
		return tgAssistantBotAPIKey{}, errors.New("account not found")
	}
	record := records[index]
	key := strings.TrimSpace(record.BotAPIKey)
	mode := normalizeTGAssistantBotMode(record.BotMode)
	webhookPath := sanitizeTGAssistantBotWebhookPath(record.BotWebhookPath)
	webhookToken := strings.TrimSpace(record.BotWebhookToken)
	TGAssistantStore.mu.RUnlock()

	responsePath := ""
	if webhookPath != "" {
		responsePath = tgAssistantBotWebhookPathPrefix + webhookPath
	}

	return tgAssistantBotAPIKey{
		AccountID:      accountID,
		APIKey:         key,
		Configured:     key != "",
		Mode:           mode,
		WebhookPath:    responsePath,
		WebhookEnabled: mode == tgAssistantBotModeWebhook && responsePath != "" && webhookToken != "",
	}, nil
}

func setTGAssistantBotAPIKey(req tgAssistantBotAPIKeyRequest) (tgAssistantBotAPIKey, error) {
	if TGAssistantStore == nil {
		return tgAssistantBotAPIKey{}, errors.New("tg assistant datastore is not initialized")
	}

	accountID := strings.TrimSpace(req.AccountID)
	if accountID == "" {
		return tgAssistantBotAPIKey{}, errors.New("account_id is required")
	}

	requestedKey := strings.TrimSpace(req.APIKey)
	requestedMode := normalizeTGAssistantBotMode(req.Mode)

	TGAssistantStore.mu.Lock()
	records := loadTGAssistantAccountsLocked()
	index := indexTGAssistantAccountByID(records, accountID)
	if index < 0 {
		TGAssistantStore.mu.Unlock()
		return tgAssistantBotAPIKey{}, errors.New("account not found")
	}
	account := records[index]
	currentKey := strings.TrimSpace(account.BotAPIKey)
	finalKey := requestedKey
	if finalKey == "" {
		finalKey = currentKey
	}
	if finalKey == "" {
		TGAssistantStore.mu.Unlock()
		return tgAssistantBotAPIKey{}, errors.New("api_key is required")
	}
	if !strings.Contains(finalKey, ":") {
		TGAssistantStore.mu.Unlock()
		return tgAssistantBotAPIKey{}, errors.New("invalid bot api_key format")
	}

	finalMode := requestedMode
	if finalMode == "" {
		finalMode = normalizeTGAssistantBotMode(account.BotMode)
	}
	webhookPath := sanitizeTGAssistantBotWebhookPath(account.BotWebhookPath)
	webhookToken := strings.TrimSpace(account.BotWebhookToken)
	if finalMode == tgAssistantBotModeWebhook {
		if webhookPath == "" {
			webhookPath = newTGAssistantBotRandomToken(12)
		}
		if webhookToken == "" {
			webhookToken = newTGAssistantBotRandomToken(24)
		}
	} else {
		webhookPath = ""
		webhookToken = ""
	}
	TGAssistantStore.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if finalMode == tgAssistantBotModeWebhook {
		if err := configureTGAssistantBotWebhook(ctx, finalKey, webhookPath, webhookToken); err != nil {
			return tgAssistantBotAPIKey{}, err
		}
	} else {
		if err := clearTGAssistantBotWebhook(ctx, finalKey); err != nil {
			return tgAssistantBotAPIKey{}, err
		}
	}

	now := time.Now().UTC().Format(time.RFC3339)
	TGAssistantStore.mu.Lock()
	records = loadTGAssistantAccountsLocked()
	index = indexTGAssistantAccountByID(records, accountID)
	if index < 0 {
		TGAssistantStore.mu.Unlock()
		return tgAssistantBotAPIKey{}, errors.New("account not found")
	}
	account = records[index]
	account.BotAPIKey = finalKey
	account.BotMode = finalMode
	account.BotWebhookPath = webhookPath
	account.BotWebhookToken = webhookToken
	account.BotLastUpdateID = 0
	account.UpdatedAt = now
	records[index] = account
	TGAssistantStore.data.Accounts = records
	TGAssistantStore.mu.Unlock()

	if err := TGAssistantStore.Save(); err != nil {
		return tgAssistantBotAPIKey{}, err
	}
	appendTGAssistantHistory("bot.api.set", accountID, true, fmt.Sprintf("mode=%s", finalMode))

	responsePath := ""
	if webhookPath != "" {
		responsePath = tgAssistantBotWebhookPathPrefix + webhookPath
	}
	return tgAssistantBotAPIKey{
		AccountID:      accountID,
		APIKey:         finalKey,
		Configured:     true,
		Mode:           finalMode,
		WebhookPath:    responsePath,
		WebhookEnabled: finalMode == tgAssistantBotModeWebhook && responsePath != "",
	}, nil
}

func configureTGAssistantBotWebhook(ctx context.Context, botAPIKey, webhookPath, webhookToken string) error {
	webhookURL, err := buildTGAssistantBotWebhookURL(webhookPath)
	if err != nil {
		return err
	}
	req := telegramBotSetWebhookRequest{
		URL:         webhookURL,
		SecretToken: webhookToken,
	}
	if err := callTelegramBotAPI(ctx, botAPIKey, "setWebhook", req, nil); err != nil {
		return fmt.Errorf("set webhook failed: %w", err)
	}
	return nil
}

func clearTGAssistantBotWebhook(ctx context.Context, botAPIKey string) error {
	req := telegramBotDeleteWebhookRequest{
		DropPendingUpdates: false,
	}
	if err := callTelegramBotAPI(ctx, botAPIKey, "deleteWebhook", req, nil); err != nil {
		return fmt.Errorf("delete webhook failed: %w", err)
	}
	return nil
}

func testSendTGAssistantBotMessage(req tgAssistantBotTestSendRequest) (tgAssistantBotTestSendResult, error) {
	if TGAssistantStore == nil {
		return tgAssistantBotTestSendResult{}, errors.New("tg assistant datastore is not initialized")
	}

	accountID := strings.TrimSpace(req.AccountID)
	message := strings.TrimSpace(req.Message)
	if accountID == "" {
		return tgAssistantBotTestSendResult{}, errors.New("account_id is required")
	}
	if message == "" {
		return tgAssistantBotTestSendResult{}, errors.New("message is required")
	}
	if len(message) > 4000 {
		return tgAssistantBotTestSendResult{}, errors.New("message is too long")
	}

	TGAssistantStore.mu.RLock()
	records := loadTGAssistantAccountsLocked()
	index := indexTGAssistantAccountByID(records, accountID)
	if index < 0 {
		TGAssistantStore.mu.RUnlock()
		return tgAssistantBotTestSendResult{}, errors.New("account not found")
	}
	account := records[index]
	TGAssistantStore.mu.RUnlock()

	botAPIKey := strings.TrimSpace(account.BotAPIKey)
	if botAPIKey == "" {
		return tgAssistantBotTestSendResult{}, errors.New("bot api_key is not configured for this account")
	}
	if !account.Authorized || account.SelfUserID == 0 {
		return tgAssistantBotTestSendResult{}, errors.New("current account is not ready, please refresh login status")
	}

	ctx, cancel := context.WithTimeout(context.Background(), tgAssistantBotHTTPTimeout)
	defer cancel()
	messageID, text, err := sendTGAssistantBotTextMessage(ctx, botAPIKey, account.SelfUserID, message)
	if err != nil {
		appendTGAssistantHistory("bot.test_send", accountID, false, err.Error())
		return tgAssistantBotTestSendResult{}, err
	}
	if text == "" {
		text = message
	}

	result := tgAssistantBotTestSendResult{
		AccountID: accountID,
		ChatID:    account.SelfUserID,
		MessageID: messageID,
		Message:   text,
		SentAt:    time.Now().UTC().Format(time.RFC3339),
	}
	appendTGAssistantHistory("bot.test_send", accountID, true, fmt.Sprintf("chat_id=%d message_id=%d", result.ChatID, result.MessageID))
	return result, nil
}

func sendTGAssistantBotTextMessage(ctx context.Context, botAPIKey string, chatID int64, text string) (int, string, error) {
	req := telegramBotSendMessageRequest{
		ChatID: fmt.Sprintf("%d", chatID),
		Text:   text,
	}
	var message telegramBotMessage
	if err := callTelegramBotAPI(ctx, botAPIKey, "sendMessage", req, &message); err != nil {
		return 0, "", err
	}
	return message.MessageID, strings.TrimSpace(message.Text), nil
}

func callTelegramBotAPI(ctx context.Context, botAPIKey, method string, reqPayload any, respPayload any) error {
	token := strings.TrimSpace(botAPIKey)
	if token == "" {
		return errors.New("bot api key is required")
	}
	apiMethod := strings.TrimSpace(method)
	if apiMethod == "" {
		return errors.New("bot api method is required")
	}

	requestBytes, err := json.Marshal(reqPayload)
	if err != nil {
		return err
	}

	url := fmt.Sprintf("https://api.telegram.org/bot%s/%s", token, apiMethod)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(requestBytes))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: tgAssistantBotHTTPTimeout}
	resp, err := client.Do(httpReq)
	if err != nil {
		return err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("telegram bot api http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var envelope telegramBotAPIResponse
	if err := json.Unmarshal(body, &envelope); err != nil {
		return err
	}
	if !envelope.OK {
		msg := strings.TrimSpace(envelope.Description)
		if msg == "" {
			msg = "telegram bot api returned error"
		}
		return errors.New(msg)
	}
	if respPayload == nil {
		return nil
	}
	if len(envelope.Result) == 0 || string(envelope.Result) == "null" {
		return nil
	}
	return json.Unmarshal(envelope.Result, respPayload)
}

func setTGAssistantBotLastUpdateID(accountID string, nextUpdateID int) error {
	if TGAssistantStore == nil {
		return errors.New("tg assistant datastore is not initialized")
	}
	normalizedAccountID := strings.TrimSpace(accountID)
	if normalizedAccountID == "" || nextUpdateID <= 0 {
		return nil
	}

	TGAssistantStore.mu.Lock()
	records := loadTGAssistantAccountsLocked()
	index := indexTGAssistantAccountByID(records, normalizedAccountID)
	if index < 0 {
		TGAssistantStore.mu.Unlock()
		return errors.New("account not found")
	}
	account := records[index]
	if nextUpdateID <= account.BotLastUpdateID {
		TGAssistantStore.mu.Unlock()
		return nil
	}
	account.BotLastUpdateID = nextUpdateID
	account.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	records[index] = account
	TGAssistantStore.data.Accounts = records
	TGAssistantStore.mu.Unlock()

	return TGAssistantStore.Save()
}

func normalizeTGAssistantBotMode(raw string) string {
	mode := strings.ToLower(strings.TrimSpace(raw))
	switch mode {
	case tgAssistantBotModeWebhook:
		return tgAssistantBotModeWebhook
	case tgAssistantBotModePolling:
		return tgAssistantBotModePolling
	default:
		return tgAssistantBotModePolling
	}
}

func sanitizeTGAssistantBotWebhookPath(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	value = strings.Trim(value, "/")
	var builder strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			builder.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			builder.WriteRune(r)
		case r >= '0' && r <= '9':
			builder.WriteRune(r)
		case r == '-' || r == '_':
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

func tgAssistantBotWebhookBaseURL() string {
	return strings.TrimRight(strings.TrimSpace(os.Getenv(tgAssistantBotWebhookBaseURLEnv)), "/")
}

func buildTGAssistantBotWebhookURL(path string) (string, error) {
	baseURL := tgAssistantBotWebhookBaseURL()
	if baseURL == "" {
		return "", fmt.Errorf("%s is required for webhook mode", tgAssistantBotWebhookBaseURLEnv)
	}
	if !strings.HasPrefix(strings.ToLower(baseURL), "https://") {
		return "", fmt.Errorf("%s must start with https://", tgAssistantBotWebhookBaseURLEnv)
	}
	tokenPath := sanitizeTGAssistantBotWebhookPath(path)
	if tokenPath == "" {
		return "", errors.New("webhook path is empty")
	}
	return baseURL + tgAssistantBotWebhookPathPrefix + tokenPath, nil
}

func newTGAssistantBotRandomToken(size int) string {
	if size <= 0 {
		size = 16
	}
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}
