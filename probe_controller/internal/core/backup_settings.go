package core

import (
	"fmt"
	"strings"
)

const (
	backupRcloneRemoteStoreField = "backup_rclone_remote"
	backupEnabledStoreField      = "backup_enabled"
)

type backupSettings struct {
	Enabled      bool
	RcloneRemote string
}

func getBackupSettings() backupSettings {
	settings := backupSettings{Enabled: false, RcloneRemote: ""}
	if Store == nil {
		return settings
	}

	Store.mu.RLock()
	enabled, _ := Store.Data[backupEnabledStoreField].(bool)
	raw, _ := Store.Data[backupRcloneRemoteStoreField].(string)
	Store.mu.RUnlock()

	settings.Enabled = enabled
	settings.RcloneRemote = strings.TrimSpace(raw)
	return settings
}

func setBackupSettings(enabled bool, rawRemote string) (backupSettings, error) {
	if Store == nil {
		return backupSettings{}, fmt.Errorf("store is not initialized")
	}

	remote := strings.TrimSpace(rawRemote)
	if enabled && remote == "" {
		return backupSettings{}, fmt.Errorf("rclone remote is required when backup is enabled")
	}

	Store.mu.Lock()
	Store.Data[backupEnabledStoreField] = enabled
	Store.Data[backupRcloneRemoteStoreField] = remote
	Store.mu.Unlock()

	if err := Store.Save(); err != nil {
		return backupSettings{}, err
	}
	return backupSettings{Enabled: enabled, RcloneRemote: remote}, nil
}
