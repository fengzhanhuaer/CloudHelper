package core

import (
	"fmt"
	"path/filepath"
	"strings"
)

const (
	backupEnabledStoreField            = "backup_enabled"
	backupLocalDirStoreField           = "backup_local_dir"
	backupSourceDirsStoreField         = "backup_source_dirs"
	backupGoogleClientIDStoreField     = "backup_google_client_id"
	backupGoogleClientSecretStoreField = "backup_google_client_secret"
	backupGoogleFolderStoreField       = "backup_google_folder"
	backupGoogleTokenStoreField        = "backup_google_token"
)

type backupSettings struct {
	Enabled            bool
	LocalDir           string
	SourceDirs         []string
	GoogleClientID     string
	GoogleClientSecret string
	GoogleFolder       string
	GoogleToken        googleDriveToken
}

func getBackupSettings() backupSettings {
	settings := backupSettings{Enabled: false, LocalDir: "", SourceDirs: nil, GoogleFolder: defaultBackupGoogleFolder}
	if Store == nil {
		return settings
	}

	Store.mu.RLock()
	enabled, _ := Store.Data[backupEnabledStoreField].(bool)
	rawLocalDir, _ := Store.Data[backupLocalDirStoreField].(string)
	rawSourceDirs := Store.Data[backupSourceDirsStoreField]
	rawGoogleClientID, _ := Store.Data[backupGoogleClientIDStoreField].(string)
	rawGoogleClientSecret, _ := Store.Data[backupGoogleClientSecretStoreField].(string)
	rawGoogleFolder, _ := Store.Data[backupGoogleFolderStoreField].(string)
	rawGoogleToken := Store.Data[backupGoogleTokenStoreField]
	Store.mu.RUnlock()

	settings.Enabled = enabled
	settings.LocalDir = strings.TrimSpace(rawLocalDir)
	settings.SourceDirs = normalizeBackupSourceDirsFromStore(rawSourceDirs)
	settings.GoogleClientID = strings.TrimSpace(rawGoogleClientID)
	settings.GoogleClientSecret = strings.TrimSpace(rawGoogleClientSecret)
	settings.GoogleFolder = firstNonEmptyBackupString(strings.TrimSpace(rawGoogleFolder), defaultBackupGoogleFolder)
	settings.GoogleToken = parseGoogleDriveTokenFromStore(rawGoogleToken)
	return settings
}

func setBackupSettings(enabled bool, rawLocalDir string, rawSourceDirs []string, googleClientID string, googleClientSecret string, googleFolder string) (backupSettings, error) {
	if Store == nil {
		return backupSettings{}, fmt.Errorf("store is not initialized")
	}

	localDir, err := normalizeBackupLocalDirForStore(rawLocalDir)
	if err != nil {
		return backupSettings{}, err
	}
	sourceDirs, err := normalizeBackupSourceDirsForStore(rawSourceDirs, localDir)
	if err != nil {
		return backupSettings{}, err
	}
	clientID := strings.TrimSpace(googleClientID)
	clientSecret := strings.TrimSpace(googleClientSecret)
	folder := normalizeGoogleDriveFolderPath(googleFolder)
	if enabled {
		if clientID == "" {
			return backupSettings{}, fmt.Errorf("google client id is required when backup is enabled")
		}
		if !getBackupSettings().GoogleToken.HasRefreshToken() {
			return backupSettings{}, fmt.Errorf("google drive is not authorized")
		}
	}

	Store.mu.Lock()
	Store.Data[backupEnabledStoreField] = enabled
	Store.Data[backupLocalDirStoreField] = localDir
	Store.Data[backupSourceDirsStoreField] = sourceDirs
	Store.Data[backupGoogleClientIDStoreField] = clientID
	Store.Data[backupGoogleClientSecretStoreField] = clientSecret
	Store.Data[backupGoogleFolderStoreField] = folder
	Store.mu.Unlock()

	if err := Store.Save(); err != nil {
		return backupSettings{}, err
	}
	settings := getBackupSettings()
	settings.Enabled = enabled
	settings.LocalDir = localDir
	settings.SourceDirs = sourceDirs
	settings.GoogleClientID = clientID
	settings.GoogleClientSecret = clientSecret
	settings.GoogleFolder = folder
	return settings, nil
}

func normalizeBackupLocalDirForStore(rawLocalDir string) (string, error) {
	localDir := strings.TrimSpace(rawLocalDir)
	if localDir == "" {
		return "", nil
	}
	abs, err := filepath.Abs(localDir)
	if err != nil {
		return "", err
	}
	dataPath, err := filepath.Abs(dataDir)
	if err != nil {
		return "", err
	}
	if pathInsideOrEqual(abs, dataPath) {
		return "", fmt.Errorf("backup local directory must not be inside controller data directory")
	}
	return abs, nil
}

func resolveBackupLocalDir(settings backupSettings, dataPath string) (string, error) {
	if strings.TrimSpace(settings.LocalDir) != "" {
		return filepath.Abs(settings.LocalDir)
	}
	return filepath.Abs(filepath.Join(filepath.Dir(dataPath), backupDirName))
}

func resolveBackupSourceDirs(settings backupSettings) ([]string, error) {
	rawSources := settings.SourceDirs
	if len(rawSources) == 0 {
		rawSources = []string{dataDir}
	}
	return normalizeBackupSourceDirsForStore(rawSources, settings.LocalDir)
}

func normalizeBackupSourceDirsFromStore(raw any) []string {
	values := []string{}
	switch typed := raw.(type) {
	case []string:
		values = append(values, typed...)
	case []any:
		for _, item := range typed {
			if text, ok := item.(string); ok {
				values = append(values, text)
			}
		}
	case string:
		for _, line := range strings.Split(typed, "\n") {
			values = append(values, line)
		}
	}
	normalized, err := normalizeBackupSourceDirsForStore(values, "")
	if err != nil {
		return []string{}
	}
	return normalized
}

func normalizeBackupSourceDirsForStore(rawSourceDirs []string, localDir string) ([]string, error) {
	out := []string{}
	seen := map[string]struct{}{}
	backupDir := strings.TrimSpace(localDir)
	if backupDir != "" {
		if abs, err := filepath.Abs(backupDir); err == nil {
			backupDir = abs
		}
	}
	for _, raw := range rawSourceDirs {
		source := strings.TrimSpace(raw)
		if source == "" {
			continue
		}
		abs, err := filepath.Abs(source)
		if err != nil {
			return nil, err
		}
		if backupDir != "" {
			if pathInsideOrEqual(abs, backupDir) || pathInsideOrEqual(backupDir, abs) {
				return nil, fmt.Errorf("backup source directory must not overlap backup local directory: %s", abs)
			}
		}
		key := strings.ToLower(abs)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, abs)
	}
	return out, nil
}

func pathInsideOrEqual(child string, parent string) bool {
	childAbs, childErr := filepath.Abs(child)
	parentAbs, parentErr := filepath.Abs(parent)
	if childErr != nil || parentErr != nil {
		return false
	}
	rel, err := filepath.Rel(parentAbs, childAbs)
	if err != nil {
		return false
	}
	return rel == "." || (rel != "" && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
