package core

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
)

const tgAssistantSessionPushSnapshotLimit = 50

type tgAssistantSessionPushEvent struct {
	AccountID string
	Target    string
	Messages  []tgAssistantSessionMessage
}

type tgAssistantSessionPushRunner struct {
	cancel context.CancelFunc
	sends  chan tgAssistantSessionPushSendJob
}

type tgAssistantSessionPushSendJob struct {
	Target  string
	Message string
	Reply   chan tgAssistantSessionPushSendResult
}

type tgAssistantSessionPushSendResult struct {
	Message tgAssistantSessionMessage
	Err     error
}

type tgAssistantSessionPushHub struct {
	mu          sync.Mutex
	subscribers map[string]map[chan tgAssistantSessionPushEvent]struct{}
	runners     map[string]*tgAssistantSessionPushRunner
}

var tgAssistantSessionPush = &tgAssistantSessionPushHub{
	subscribers: map[string]map[chan tgAssistantSessionPushEvent]struct{}{},
	runners:     map[string]*tgAssistantSessionPushRunner{},
}

func subscribeTGAssistantSessionPush(accountID, target string) (<-chan tgAssistantSessionPushEvent, func()) {
	ch := make(chan tgAssistantSessionPushEvent, 8)
	key := tgAssistantSessionPushKey(accountID, target)
	tgAssistantSessionPush.mu.Lock()
	if tgAssistantSessionPush.subscribers[key] == nil {
		tgAssistantSessionPush.subscribers[key] = map[chan tgAssistantSessionPushEvent]struct{}{}
	}
	tgAssistantSessionPush.subscribers[key][ch] = struct{}{}
	tgAssistantSessionPush.mu.Unlock()
	return ch, func() {
		tgAssistantSessionPush.mu.Lock()
		if group := tgAssistantSessionPush.subscribers[key]; group != nil {
			delete(group, ch)
			if len(group) == 0 {
				delete(tgAssistantSessionPush.subscribers, key)
			}
		}
		tgAssistantSessionPush.mu.Unlock()
		close(ch)
	}
}

func publishTGAssistantSessionPush(event tgAssistantSessionPushEvent) {
	key := tgAssistantSessionPushKey(event.AccountID, event.Target)
	tgAssistantSessionPush.mu.Lock()
	defer tgAssistantSessionPush.mu.Unlock()
	for ch := range tgAssistantSessionPush.subscribers[key] {
		select {
		case ch <- event:
		default:
		}
	}
}

func ensureTGAssistantSessionPushRunner(accountID string) *tgAssistantSessionPushRunner {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return nil
	}
	tgAssistantSessionPush.mu.Lock()
	if runner, exists := tgAssistantSessionPush.runners[accountID]; exists {
		tgAssistantSessionPush.mu.Unlock()
		return runner
	}
	ctx, cancel := context.WithCancel(context.Background())
	runner := &tgAssistantSessionPushRunner{
		cancel: cancel,
		sends:  make(chan tgAssistantSessionPushSendJob, 32),
	}
	tgAssistantSessionPush.runners[accountID] = runner
	tgAssistantSessionPush.mu.Unlock()

	go func() {
		defer func() {
			tgAssistantSessionPush.mu.Lock()
			if tgAssistantSessionPush.runners[accountID] == runner {
				delete(tgAssistantSessionPush.runners, accountID)
			}
			tgAssistantSessionPush.mu.Unlock()
		}()
		runTGAssistantSessionPushLoop(ctx, accountID, runner.sends)
	}()
	return runner
}

func runTGAssistantSessionPushLoop(ctx context.Context, accountID string, sends <-chan tgAssistantSessionPushSendJob) {
	backoff := 3 * time.Second
	for ctx.Err() == nil {
		err := runTGAssistantSessionPushClient(ctx, accountID, sends)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			appendTGAssistantHistory("session.push", accountID, false, err.Error())
			log.Printf("tg session push stopped for %s: %v", accountID, err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < time.Minute {
			backoff *= 2
		}
	}
}

func runTGAssistantSessionPushClient(parent context.Context, accountID string, sends <-chan tgAssistantSessionPushSendJob) error {
	apiID, apiHash, account, err := loadTGAssistantClientConfig(accountID)
	if err != nil {
		return err
	}
	if apiID <= 0 || strings.TrimSpace(apiHash) == "" {
		return errors.New("shared tg api key is not configured")
	}
	sessionPath := tgAssistantSessionPath(account.ID)
	if err := os.MkdirAll(filepath.Dir(sessionPath), 0o755); err != nil {
		return fmt.Errorf("failed to prepare tg session directory: %w", err)
	}
	handler := telegram.UpdateHandlerFunc(func(ctx context.Context, updates tg.UpdatesClass) error {
		handleTGAssistantSessionPushUpdates(ctx, account, updates)
		return nil
	})
	client := telegram.NewClient(apiID, apiHash, telegram.Options{
		SessionStorage: &session.FileStorage{Path: sessionPath},
		UpdateHandler:  handler,
	})
	return client.Run(parent, func(inner context.Context) error {
		status, err := client.Auth().Status(inner)
		if err != nil {
			return err
		}
		if !status.Authorized {
			return errors.New("tg session is not authorized for requested account")
		}
		appendTGAssistantHistory("session.push", accountID, true, "connected")
		for {
			select {
			case <-inner.Done():
				return inner.Err()
			case job := <-sends:
				message, err := sendTGAssistantSessionMessageWithClient(inner, client, account, job.Target, job.Message)
				select {
				case job.Reply <- tgAssistantSessionPushSendResult{Message: message, Err: err}:
				default:
				}
			}
		}
	})
}

func sendTGAssistantSessionPushMessage(req tgAssistantSessionSendRequest) (tgAssistantSessionMessage, error) {
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
	runner := ensureTGAssistantSessionPushRunner(accountID)
	if runner == nil {
		return tgAssistantSessionMessage{}, errors.New("tg session push runner is not available")
	}
	reply := make(chan tgAssistantSessionPushSendResult, 1)
	job := tgAssistantSessionPushSendJob{
		Target:  target,
		Message: message,
		Reply:   reply,
	}
	timer := time.NewTimer(45 * time.Second)
	defer timer.Stop()
	select {
	case runner.sends <- job:
	case <-timer.C:
		return tgAssistantSessionMessage{}, errors.New("tg session send timed out waiting for runner")
	}
	select {
	case result := <-reply:
		return result.Message, result.Err
	case <-timer.C:
		return tgAssistantSessionMessage{}, errors.New("tg session send timed out")
	}
}

func sendTGAssistantSessionMessageWithClient(ctx context.Context, client *telegram.Client, account tgAssistantAccountRecord, target, message string) (tgAssistantSessionMessage, error) {
	now := time.Now()
	result := tgAssistantSessionMessage{
		Date:       now.UTC().Format(time.RFC3339),
		Text:       message,
		Out:        true,
		SenderID:   fmt.Sprintf("user:%d", account.SelfUserID),
		SenderName: firstNonEmptyString(account.SelfDisplayName, account.SelfUsername, account.Label, account.Phone),
	}
	status, err := client.Auth().Status(ctx)
	if err != nil {
		return tgAssistantSessionMessage{}, err
	}
	if !status.Authorized {
		return tgAssistantSessionMessage{}, errors.New("tg session is not authorized for requested account")
	}
	peer, err := resolveTGAssistantInputPeer(ctx, client, account.ID, target)
	if err != nil {
		return tgAssistantSessionMessage{}, err
	}
	updates, err := client.API().MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{
		Peer:     peer,
		Message:  message,
		RandomID: newTGAssistantMessageRandomID(),
	})
	if err != nil {
		return tgAssistantSessionMessage{}, err
	}
	result.ID = extractTGAssistantSentMessageID(updates)
	if result.ID <= 0 {
		result.ID = newTGAssistantLocalMessageID()
	}
	if echoed := summarizeTGAssistantSendUpdates(updates); strings.TrimSpace(echoed) != "" {
		result.Text = echoed
	}
	if err := storeTGAssistantSessionMessages(account.ID, target, []tgAssistantSessionMessage{result}); err != nil {
		log.Printf("tg message store failed: %v", err)
	}
	snapshot, err := listStoredTGAssistantSessionMessages(account.ID, target, tgAssistantSessionPushSnapshotLimit)
	if err == nil {
		publishTGAssistantSessionPush(tgAssistantSessionPushEvent{
			AccountID: account.ID,
			Target:    target,
			Messages:  snapshot,
		})
	}
	return result, nil
}

func handleTGAssistantSessionPushUpdates(ctx context.Context, account tgAssistantAccountRecord, updates tg.UpdatesClass) {
	events := buildTGAssistantSessionPushMessages(account, updates)
	if len(events) == 0 {
		return
	}
	grouped := map[string][]tgAssistantSessionMessage{}
	for target, messages := range events {
		if target == "" || len(messages) == 0 {
			continue
		}
		grouped[target] = append(grouped[target], messages...)
	}
	for target, messages := range grouped {
		sort.SliceStable(messages, func(i, j int) bool {
			if messages[i].ID == messages[j].ID {
				return messages[i].Date < messages[j].Date
			}
			return messages[i].ID < messages[j].ID
		})
		if err := storeTGAssistantSessionMessages(account.ID, target, messages); err != nil {
			log.Printf("tg push message store failed: %v", err)
			continue
		}
		snapshot, err := listStoredTGAssistantSessionMessages(account.ID, target, tgAssistantSessionPushSnapshotLimit)
		if err != nil {
			log.Printf("tg push message snapshot failed: %v", err)
			continue
		}
		publishTGAssistantSessionPush(tgAssistantSessionPushEvent{
			AccountID: account.ID,
			Target:    target,
			Messages:  snapshot,
		})
	}
	_ = ctx
}

func buildTGAssistantSessionPushMessages(account tgAssistantAccountRecord, updates tg.UpdatesClass) map[string][]tgAssistantSessionMessage {
	result := map[string][]tgAssistantSessionMessage{}
	switch value := updates.(type) {
	case *tg.UpdateShortMessage:
		target := fmt.Sprintf("user:%d", value.UserID)
		text := strings.TrimSpace(value.Message)
		if text == "" {
			text = "[空消息]"
		}
		senderID := ""
		senderName := ""
		if !value.Out {
			senderID = target
			senderName = target
		}
		result[target] = append(result[target], tgAssistantSessionMessage{
			ID:         value.ID,
			Date:       formatTGAssistantUnixTime(value.Date),
			Text:       text,
			Out:        value.Out,
			SenderID:   senderID,
			SenderName: senderName,
		})
	case *tg.UpdateShortChatMessage:
		target := fmt.Sprintf("chat:%d", value.ChatID)
		text := strings.TrimSpace(value.Message)
		if text == "" {
			text = "[空消息]"
		}
		result[target] = append(result[target], tgAssistantSessionMessage{
			ID:         value.ID,
			Date:       formatTGAssistantUnixTime(value.Date),
			Text:       text,
			Out:        value.Out,
			SenderID:   fmt.Sprintf("user:%d", value.FromID),
			SenderName: fmt.Sprintf("user:%d", value.FromID),
		})
	case *tg.UpdateShort:
		appendTGAssistantSessionPushUpdate(result, account, value.Update, nil)
	case *tg.Updates:
		peers := buildTGAssistantPeerInfoMap(value.Users, value.Chats)
		for _, update := range value.Updates {
			appendTGAssistantSessionPushUpdate(result, account, update, peers)
		}
	case *tg.UpdatesCombined:
		peers := buildTGAssistantPeerInfoMap(value.Users, value.Chats)
		for _, update := range value.Updates {
			appendTGAssistantSessionPushUpdate(result, account, update, peers)
		}
	}
	return result
}

func appendTGAssistantSessionPushUpdate(result map[string][]tgAssistantSessionMessage, account tgAssistantAccountRecord, update tg.UpdateClass, peers map[string]tgAssistantPeerInfo) {
	switch value := update.(type) {
	case *tg.UpdateNewMessage:
		target, message, ok := buildTGAssistantSessionPushMessage(account, value.Message, peers)
		if ok {
			result[target] = append(result[target], message)
		}
	case *tg.UpdateNewChannelMessage:
		target, message, ok := buildTGAssistantSessionPushMessage(account, value.Message, peers)
		if ok {
			result[target] = append(result[target], message)
		}
	}
}

func buildTGAssistantSessionPushMessage(account tgAssistantAccountRecord, raw tg.MessageClass, peers map[string]tgAssistantPeerInfo) (string, tgAssistantSessionMessage, bool) {
	selfInfo := tgAssistantPeerInfo{
		ID:   fmt.Sprintf("user:%d", account.SelfUserID),
		Name: firstNonEmptyString(account.SelfDisplayName, account.SelfUsername, account.SelfPhone, "我"),
		Type: "user",
	}
	switch msg := raw.(type) {
	case *tg.Message:
		if msg == nil {
			return "", tgAssistantSessionMessage{}, false
		}
		target := formatTGAssistantPeerID(msg.GetPeerID())
		if target == "" {
			return "", tgAssistantSessionMessage{}, false
		}
		sender := selfInfo
		if !msg.Out {
			if from, ok := msg.GetFromID(); ok {
				sender = lookupTGAssistantPeerInfo(peers, from)
			} else {
				sender = lookupTGAssistantPeerInfo(peers, msg.GetPeerID())
			}
		}
		text := summarizeTGAssistantSessionText(msg)
		if strings.TrimSpace(text) == "" {
			text = "[空消息]"
		}
		mediaType, mediaSize := detectTGAssistantMessageMedia(msg.Media)
		return target, tgAssistantSessionMessage{
			ID:         msg.ID,
			Date:       formatTGAssistantUnixTime(msg.Date),
			Text:       text,
			Out:        msg.Out,
			SenderID:   sender.ID,
			SenderName: sender.Name,
			MediaType:  mediaType,
			MediaSize:  mediaSize,
		}, true
	case *tg.MessageService:
		if msg == nil {
			return "", tgAssistantSessionMessage{}, false
		}
		target := formatTGAssistantPeerID(msg.GetPeerID())
		if target == "" {
			return "", tgAssistantSessionMessage{}, false
		}
		sender := selfInfo
		if !msg.Out {
			if from, ok := msg.GetFromID(); ok {
				sender = lookupTGAssistantPeerInfo(peers, from)
			} else {
				sender = lookupTGAssistantPeerInfo(peers, msg.GetPeerID())
			}
		}
		return target, tgAssistantSessionMessage{
			ID:         msg.ID,
			Date:       formatTGAssistantUnixTime(msg.Date),
			Text:       summarizeTGAssistantServiceMessage(msg),
			Out:        msg.Out,
			SenderID:   sender.ID,
			SenderName: sender.Name,
			Service:    true,
		}, true
	default:
		return "", tgAssistantSessionMessage{}, false
	}
}

func tgAssistantSessionPushKey(accountID, target string) string {
	return strings.TrimSpace(accountID) + "\x00" + strings.TrimSpace(target)
}
