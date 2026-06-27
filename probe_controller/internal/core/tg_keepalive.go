package core

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
)

const (
	tgAssistantKeepaliveJobKey  = "tg.account.keepalive.daily"
	tgAssistantKeepaliveHour    = 4
	tgAssistantKeepaliveTimeout = 10 * time.Minute
)

var tgAssistantKeepaliveRefreshFunc = refreshOneTGAccountRecord

func initTGAssistantKeepaliveEngine() {
	scheduleNextTGAssistantKeepalive()
	log.Println("tg assistant account keepalive engine started")
}

func scheduleNextTGAssistantKeepalive() {
	runAt := nextDailyRunAt(tgAssistantKeepaliveHour, time.Now())
	scheduleGlobalTask(tgAssistantKeepaliveJobKey, runAt, tgAssistantKeepaliveTimeout, runTGAssistantKeepalive)
}

func runTGAssistantKeepalive(ctx context.Context) {
	defer scheduleNextTGAssistantKeepalive()
	keepaliveTGAssistantAccountsOnce(ctx)
}

func keepaliveTGAssistantAccountsOnce(ctx context.Context) {
	if TGAssistantStore == nil {
		return
	}

	TGAssistantStore.mu.RLock()
	records := loadTGAssistantAccountsLocked()
	apiID, apiHash := loadTGAssistantAPIKeyLocked()
	TGAssistantStore.mu.RUnlock()

	if !isTGAssistantAPIKeyConfigured(apiID, apiHash) {
		return
	}

	changed := false
	checked := 0
	for i := range records {
		if ctx != nil {
			select {
			case <-ctx.Done():
				appendTGAssistantHistory("account.keepalive", "", false, fmt.Sprintf("checked=%d err=%s", checked, ctx.Err().Error()))
				if changed {
					saveTGAssistantKeepaliveRecords(records)
				}
				return
			default:
			}
		}

		if !records[i].Authorized {
			continue
		}
		tgAssistantKeepaliveRefreshFunc(&records[i], apiID, apiHash)
		checked++
		changed = true
		accountID := strings.TrimSpace(records[i].ID)
		if records[i].Authorized {
			appendTGAssistantHistory("account.keepalive", accountID, true, "authorized")
		} else {
			appendTGAssistantHistory("account.keepalive", accountID, false, records[i].LastError)
		}
	}

	if changed {
		saveTGAssistantKeepaliveRecords(records)
	}
	if checked > 0 {
		appendTGAssistantHistory("account.keepalive.summary", "", true, fmt.Sprintf("checked=%d", checked))
	}
}

func saveTGAssistantKeepaliveRecords(records []tgAssistantAccountRecord) {
	if TGAssistantStore == nil {
		return
	}
	TGAssistantStore.mu.Lock()
	TGAssistantStore.data.Accounts = records
	TGAssistantStore.mu.Unlock()
	if err := TGAssistantStore.Save(); err != nil {
		log.Printf("tg assistant account keepalive save failed: %v", err)
		appendTGAssistantHistory("account.keepalive.save", "", false, err.Error())
	}
}
