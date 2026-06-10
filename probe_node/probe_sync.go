package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	defaultProbeSyncGoogleFolder = "CloudHelper/probe_node"
	probeSyncArchivePrefix       = "probe-sync-"
	probeSyncArchiveDateTimeFmt  = "20060102-150405.000000000"
	probeSyncScheduleDaily       = "daily"
	probeSyncScheduleWeekly      = "weekly"
	probeSyncScheduleMonthly     = "monthly"

	probeSyncGoogleScopeDriveFile = "https://www.googleapis.com/auth/drive.file"
	probeSyncGoogleDeviceCodeURL  = "https://oauth2.googleapis.com/device/code"
	probeSyncGoogleTokenURL       = "https://oauth2.googleapis.com/token"
	probeSyncGoogleDriveFilesURL  = "https://www.googleapis.com/drive/v3/files"
	probeSyncGoogleDriveUploadURL = "https://www.googleapis.com/upload/drive/v3/files"
)

type probeSyncGoogleToken struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	TokenType    string    `json:"token_type"`
	Expiry       time.Time `json:"expiry"`
}

func (t probeSyncGoogleToken) hasRefreshToken() bool {
	return strings.TrimSpace(t.RefreshToken) != ""
}

func (t probeSyncGoogleToken) hasUsableAccessToken() bool {
	return strings.TrimSpace(t.AccessToken) != "" && time.Now().Before(t.Expiry.Add(-1*time.Minute))
}

type probeSyncSettings struct {
	Enabled            bool                 `json:"enabled"`
	SourcePaths        []string             `json:"source_paths"`
	LocalTempDir       string               `json:"local_temp_dir"`
	Schedule           string               `json:"schedule"`
	GoogleClientID     string               `json:"google_client_id"`
	GoogleClientSecret string               `json:"google_client_secret"`
	GoogleFolder       string               `json:"google_folder"`
	GoogleToken        probeSyncGoogleToken `json:"google_token"`
	LastAttemptAt      string               `json:"last_attempt_at,omitempty"`
	LastSuccessAt      string               `json:"last_success_at,omitempty"`
	LastError          string               `json:"last_error,omitempty"`
	LastArchiveName    string               `json:"last_archive_name,omitempty"`
}

type probeSyncRuntimeStatus struct {
	Running         bool   `json:"running"`
	LastSource      string `json:"last_source"`
	LastStatus      string `json:"last_status"`
	LastError       string `json:"last_error"`
	LastStartedAt   string `json:"last_started_at"`
	LastFinishedAt  string `json:"last_finished_at"`
	LastArchiveName string `json:"last_archive_name"`
}

type probeSyncGoogleDeviceCodeResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURL         string `json:"verification_url"`
	VerificationURLComplete string `json:"verification_url_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

type probeSyncGoogleTokenResponse struct {
	AccessToken  string `json:"access_token"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

type probeSyncGoogleAuthSession struct {
	ID           string
	ClientID     string
	ClientSecret string
	DeviceCode   string
	UserCode     string
	VerifyURL    string
	CompleteURL  string
	ExpiresAt    time.Time
	IntervalSec  int
}

var (
	probeSyncSettingsMu sync.Mutex

	probeSyncRuntimeMu     sync.Mutex
	probeSyncRuntime       = probeSyncRuntimeStatus{LastStatus: "idle"}
	probeSyncAsyncMu       sync.Mutex
	probeSyncAsyncRunning  bool
	probeSyncAsyncPending  bool
	probeSyncSchedulerOnce sync.Once

	probeSyncGoogleAuthSessions = struct {
		mu   sync.Mutex
		data map[string]probeSyncGoogleAuthSession
	}{data: map[string]probeSyncGoogleAuthSession{}}
)

func startProbeSyncScheduler(identity nodeIdentity) {
	probeSyncSchedulerOnce.Do(func() {
		go runProbeSyncScheduler(identity)
	})
}

func runProbeSyncScheduler(identity nodeIdentity) {
	for {
		settings, err := loadProbeSyncSettings()
		if err == nil && settings.Enabled && probeSyncIsDue(settings, time.Now()) {
			triggerProbeSyncAsync(identity, "schedule")
		} else if err != nil {
			logProbeWarnf("probe sync settings load failed: %v", err)
		}
		time.Sleep(time.Minute)
	}
}

func probeSyncIsDue(settings probeSyncSettings, now time.Time) bool {
	if !settings.Enabled {
		return false
	}
	last := strings.TrimSpace(settings.LastSuccessAt)
	if last == "" {
		last = strings.TrimSpace(settings.LastAttemptAt)
	}
	if last == "" {
		return true
	}
	lastAt, err := time.Parse(time.RFC3339, last)
	if err != nil {
		return true
	}
	switch normalizeProbeSyncSchedule(settings.Schedule) {
	case probeSyncScheduleWeekly:
		return !now.Before(lastAt.AddDate(0, 0, 7))
	case probeSyncScheduleMonthly:
		return !now.Before(lastAt.AddDate(0, 1, 0))
	default:
		return !now.Before(lastAt.AddDate(0, 0, 1))
	}
}

func triggerProbeSyncAsync(identity nodeIdentity, source string) bool {
	probeSyncAsyncMu.Lock()
	if probeSyncAsyncRunning {
		probeSyncAsyncPending = true
		probeSyncAsyncMu.Unlock()
		return false
	}
	probeSyncAsyncRunning = true
	probeSyncAsyncMu.Unlock()

	go runProbeSyncAsync(identity, source)
	return true
}

func runProbeSyncAsync(identity nodeIdentity, source string) {
	currentSource := firstNonEmpty(strings.TrimSpace(source), "unspecified")
	for {
		markProbeSyncStarted(currentSource)
		settings, err := runProbeSyncOnce(identity)
		markProbeSyncFinished(err, settings.LastArchiveName)
		if err != nil {
			logProbeWarnf("probe sync failed source=%s err=%v", currentSource, err)
		}

		probeSyncAsyncMu.Lock()
		if !probeSyncAsyncPending {
			probeSyncAsyncRunning = false
			probeSyncAsyncMu.Unlock()
			return
		}
		probeSyncAsyncPending = false
		probeSyncAsyncMu.Unlock()
		currentSource = "coalesced"
	}
}

func markProbeSyncStarted(source string) {
	now := time.Now().UTC().Format(time.RFC3339)
	probeSyncRuntimeMu.Lock()
	probeSyncRuntime.Running = true
	probeSyncRuntime.LastSource = strings.TrimSpace(source)
	probeSyncRuntime.LastStatus = "running"
	probeSyncRuntime.LastError = ""
	probeSyncRuntime.LastStartedAt = now
	probeSyncRuntime.LastFinishedAt = ""
	probeSyncRuntimeMu.Unlock()
}

func markProbeSyncFinished(err error, archiveName string) {
	now := time.Now().UTC().Format(time.RFC3339)
	probeSyncRuntimeMu.Lock()
	probeSyncRuntime.Running = false
	probeSyncRuntime.LastFinishedAt = now
	probeSyncRuntime.LastArchiveName = strings.TrimSpace(archiveName)
	if err != nil {
		probeSyncRuntime.LastStatus = "failed"
		probeSyncRuntime.LastError = strings.TrimSpace(err.Error())
	} else {
		probeSyncRuntime.LastStatus = "ok"
		probeSyncRuntime.LastError = ""
	}
	probeSyncRuntimeMu.Unlock()
}

func getProbeSyncRuntimeStatus() probeSyncRuntimeStatus {
	probeSyncRuntimeMu.Lock()
	defer probeSyncRuntimeMu.Unlock()
	return probeSyncRuntime
}

func runProbeSyncOnce(identity nodeIdentity) (probeSyncSettings, error) {
	settings, err := loadProbeSyncSettings()
	if err != nil {
		return settings, err
	}
	now := time.Now()
	settings.LastAttemptAt = now.UTC().Format(time.RFC3339)
	settings.LastError = ""
	if saveErr := persistProbeSyncSettings(settings); saveErr != nil {
		return settings, saveErr
	}
	if !settings.Enabled {
		return settings, nil
	}
	if err := validateProbeSyncSettingsForRun(settings); err != nil {
		settings.LastError = err.Error()
		_ = persistProbeSyncSettings(settings)
		return settings, err
	}

	archivePath, archiveName, err := createProbeSyncArchive(settings.SourcePaths, settings.LocalTempDir, identity, now)
	if err != nil {
		settings.LastError = err.Error()
		_ = persistProbeSyncSettings(settings)
		return settings, err
	}
	defer os.RemoveAll(filepath.Dir(archivePath))
	settings.LastArchiveName = archiveName

	token, err := ensureProbeSyncGoogleAccessToken(settings)
	if err != nil {
		settings.LastError = err.Error()
		_ = persistProbeSyncSettings(settings)
		return settings, err
	}
	settings.GoogleToken = token
	remoteFolder := probeSyncRemoteFolderPath(settings.GoogleFolder, identity.NodeID)
	folderID, err := ensureProbeSyncGoogleDriveFolderPath(token.AccessToken, remoteFolder)
	if err != nil {
		settings.LastError = err.Error()
		_ = persistProbeSyncSettings(settings)
		return settings, err
	}
	if err := uploadProbeSyncGoogleDriveFileResumable(token.AccessToken, folderID, archivePath); err != nil {
		settings.LastError = err.Error()
		_ = persistProbeSyncSettings(settings)
		return settings, err
	}
	settings.LastSuccessAt = now.UTC().Format(time.RFC3339)
	settings.LastError = ""
	if err := persistProbeSyncSettings(settings); err != nil {
		return settings, err
	}
	return settings, nil
}

func defaultProbeSyncSettings() probeSyncSettings {
	return probeSyncSettings{
		Enabled:      false,
		Schedule:     probeSyncScheduleDaily,
		GoogleFolder: defaultProbeSyncGoogleFolder,
	}
}

func resolveProbeSyncSettingsPath() (string, error) {
	dataDir, err := resolveDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataDir, "probe_sync_settings.json"), nil
}

func loadProbeSyncSettings() (probeSyncSettings, error) {
	probeSyncSettingsMu.Lock()
	defer probeSyncSettingsMu.Unlock()
	return loadProbeSyncSettingsUnlocked()
}

func loadProbeSyncSettingsUnlocked() (probeSyncSettings, error) {
	path, err := resolveProbeSyncSettingsPath()
	if err != nil {
		return probeSyncSettings{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			settings := defaultProbeSyncSettings()
			return settings, writeProbeSyncSettingsFile(path, settings)
		}
		return probeSyncSettings{}, err
	}
	settings := defaultProbeSyncSettings()
	if err := json.Unmarshal(raw, &settings); err != nil {
		return probeSyncSettings{}, err
	}
	settings.SourcePaths = normalizeProbeSyncSourcePathsFromStore(settings.SourcePaths)
	settings.LocalTempDir = normalizeProbeSyncLocalTempDirFromStore(settings.LocalTempDir)
	settings.Schedule = normalizeProbeSyncSchedule(settings.Schedule)
	settings.GoogleClientID = strings.TrimSpace(settings.GoogleClientID)
	settings.GoogleClientSecret = strings.TrimSpace(settings.GoogleClientSecret)
	settings.GoogleFolder = normalizeProbeSyncGoogleDriveFolderPath(settings.GoogleFolder)
	return settings, nil
}

func persistProbeSyncSettings(settings probeSyncSettings) error {
	probeSyncSettingsMu.Lock()
	defer probeSyncSettingsMu.Unlock()
	path, err := resolveProbeSyncSettingsPath()
	if err != nil {
		return err
	}
	settings.SourcePaths = normalizeProbeSyncSourcePathsFromStore(settings.SourcePaths)
	settings.LocalTempDir = normalizeProbeSyncLocalTempDirFromStore(settings.LocalTempDir)
	settings.Schedule = normalizeProbeSyncSchedule(settings.Schedule)
	settings.GoogleClientID = strings.TrimSpace(settings.GoogleClientID)
	settings.GoogleClientSecret = strings.TrimSpace(settings.GoogleClientSecret)
	settings.GoogleFolder = normalizeProbeSyncGoogleDriveFolderPath(settings.GoogleFolder)
	return writeProbeSyncSettingsFile(path, settings)
}

func writeProbeSyncSettingsFile(path string, settings probeSyncSettings) error {
	encoded, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(encoded, '\n'), 0o600)
}

func updateProbeSyncSettings(enabled bool, sourcePaths []string, localTempDir string, schedule string, clientID string, clientSecret string, googleFolder string) (probeSyncSettings, error) {
	current, err := loadProbeSyncSettings()
	if err != nil {
		return probeSyncSettings{}, err
	}
	normalizeProbeSyncSubmittedGoogleOAuthCredentials(&clientID, &clientSecret, current.GoogleClientSecret)
	settings := current
	settings.Enabled = enabled
	settings.SourcePaths, err = normalizeProbeSyncSourcePathsForStore(sourcePaths)
	if err != nil {
		return probeSyncSettings{}, err
	}
	settings.LocalTempDir, err = normalizeProbeSyncLocalTempDirForStore(localTempDir)
	if err != nil {
		return probeSyncSettings{}, err
	}
	settings.Schedule = normalizeProbeSyncSchedule(schedule)
	settings.GoogleClientID = strings.TrimSpace(clientID)
	settings.GoogleClientSecret = strings.TrimSpace(clientSecret)
	settings.GoogleFolder = normalizeProbeSyncGoogleDriveFolderPath(googleFolder)
	if enabled {
		if err := validateProbeSyncSettingsForRun(settings); err != nil {
			return probeSyncSettings{}, err
		}
	}
	if err := persistProbeSyncSettings(settings); err != nil {
		return probeSyncSettings{}, err
	}
	return loadProbeSyncSettings()
}

func validateProbeSyncSettingsForRun(settings probeSyncSettings) error {
	if len(settings.SourcePaths) == 0 {
		return fmt.Errorf("source path is required")
	}
	for _, source := range settings.SourcePaths {
		if _, err := os.Stat(source); err != nil {
			return err
		}
	}
	if strings.TrimSpace(settings.LocalTempDir) != "" {
		if err := os.MkdirAll(settings.LocalTempDir, 0o755); err != nil {
			return fmt.Errorf("create local temp directory failed: %w", err)
		}
		info, err := os.Stat(settings.LocalTempDir)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("local temp path is not a directory: %s", settings.LocalTempDir)
		}
	}
	if strings.TrimSpace(settings.GoogleClientID) == "" {
		return fmt.Errorf("google client id is required")
	}
	if !settings.GoogleToken.hasRefreshToken() {
		return fmt.Errorf("google drive is not authorized")
	}
	return nil
}

func normalizeProbeSyncSchedule(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "week", "weekly", "周", "每周":
		return probeSyncScheduleWeekly
	case "month", "monthly", "月", "每月":
		return probeSyncScheduleMonthly
	default:
		return probeSyncScheduleDaily
	}
}

func normalizeProbeSyncSourcePathsFromStore(raw []string) []string {
	paths, err := normalizeProbeSyncSourcePathsForStore(raw)
	if err != nil {
		return []string{}
	}
	return paths
}

func normalizeProbeSyncSourcePathsForStore(raw []string) ([]string, error) {
	out := []string{}
	seen := map[string]struct{}{}
	for _, item := range raw {
		for _, line := range strings.Split(strings.ReplaceAll(item, "\r\n", "\n"), "\n") {
			path := strings.TrimSpace(line)
			if path == "" {
				continue
			}
			abs, err := filepath.Abs(path)
			if err != nil {
				return nil, err
			}
			key := strings.ToLower(abs)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, abs)
		}
	}
	return out, nil
}

func normalizeProbeSyncLocalTempDirFromStore(raw string) string {
	dir, err := normalizeProbeSyncLocalTempDirForStore(raw)
	if err != nil {
		return ""
	}
	return dir
}

func normalizeProbeSyncLocalTempDirForStore(raw string) (string, error) {
	dir := strings.TrimSpace(raw)
	if dir == "" {
		return "", nil
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	return abs, nil
}

func normalizeProbeSyncGoogleDriveFolderPath(raw string) string {
	path := strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/"))
	path = strings.Trim(path, "/")
	if path == "" {
		return defaultProbeSyncGoogleFolder
	}
	parts := []string{}
	for _, part := range strings.Split(path, "/") {
		part = strings.TrimSpace(part)
		if part == "" || part == "." {
			continue
		}
		parts = append(parts, part)
	}
	if len(parts) == 0 {
		return defaultProbeSyncGoogleFolder
	}
	return strings.Join(parts, "/")
}

func probeSyncRemoteFolderPath(baseFolder string, nodeID string) string {
	nodeFolder := probeSyncSafePathPart(firstNonEmpty(nodeID, "probe-node"))
	return normalizeProbeSyncGoogleDriveFolderPath(normalizeProbeSyncGoogleDriveFolderPath(baseFolder) + "/" + nodeFolder)
}

func probeSyncSafePathPart(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "probe-node"
	}
	buf := make([]byte, 0, len(value))
	for i := 0; i < len(value); i++ {
		ch := value[i]
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '-' || ch == '_' || ch == '.' {
			buf = append(buf, ch)
		} else {
			buf = append(buf, '_')
		}
	}
	out := strings.Trim(strings.TrimSpace(string(buf)), "._-")
	if out == "" {
		return "probe-node"
	}
	return out
}

func createProbeSyncArchive(sourcePaths []string, localTempDir string, identity nodeIdentity, now time.Time) (string, string, error) {
	if len(sourcePaths) == 0 {
		return "", "", fmt.Errorf("source path is required")
	}
	parentDir := strings.TrimSpace(localTempDir)
	if parentDir != "" {
		if err := os.MkdirAll(parentDir, 0o755); err != nil {
			return "", "", err
		}
	}
	tmpDir, err := os.MkdirTemp(parentDir, "probe-node-sync-*")
	if err != nil {
		return "", "", err
	}
	nodeTag := probeSyncSafePathPart(firstNonEmpty(identity.NodeID, currentProbeSyncNodeID()))
	archiveName := probeSyncArchivePrefix + nodeTag + "-" + now.Format(probeSyncArchiveDateTimeFmt) + ".zip"
	archivePath := filepath.Join(tmpDir, archiveName)
	if err := zipProbeSyncSources(sourcePaths, archivePath); err != nil {
		_ = os.RemoveAll(tmpDir)
		return "", "", err
	}
	return archivePath, archiveName, nil
}

func zipProbeSyncSources(sourcePaths []string, targetZip string) error {
	out, err := os.Create(targetZip)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(out)
	seenNames := map[string]int{}
	var zipErr error
	for idx, source := range sourcePaths {
		source = strings.TrimSpace(source)
		if source == "" {
			continue
		}
		info, err := os.Stat(source)
		if err != nil {
			zipErr = err
			break
		}
		label := probeSyncSourceLabel(source, idx)
		if count := seenNames[label]; count > 0 {
			label = fmt.Sprintf("%s-%d", label, count+1)
		}
		seenNames[label]++
		if info.IsDir() {
			zipErr = addProbeSyncDirectoryToZip(zw, source, label)
		} else {
			zipErr = addProbeSyncFileToZip(zw, source, label)
		}
		if zipErr != nil {
			break
		}
	}
	closeZipErr := zw.Close()
	closeFileErr := out.Close()
	if zipErr != nil {
		return zipErr
	}
	if closeZipErr != nil {
		return closeZipErr
	}
	return closeFileErr
}

func probeSyncSourceLabel(source string, idx int) string {
	base := probeSyncSafePathPart(filepath.Base(strings.TrimSpace(source)))
	if base == "" || base == "." || base == string(filepath.Separator) {
		base = "source"
	}
	return fmt.Sprintf("%03d-%s", idx+1, base)
}

func addProbeSyncDirectoryToZip(zw *zip.Writer, sourceDir string, rootLabel string) error {
	return filepath.WalkDir(sourceDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(sourceDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			header := &zip.FileHeader{Name: filepath.ToSlash(strings.Trim(rootLabel, "/")) + "/", Method: zip.Deflate}
			_, err := zw.CreateHeader(header)
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(filepath.Join(rootLabel, rel))
		if info.IsDir() {
			header.Name += "/"
			_, err := zw.CreateHeader(header)
			return err
		}
		header.Method = zip.Deflate
		writer, err := zw.CreateHeader(header)
		if err != nil {
			return err
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		_, err = io.Copy(writer, in)
		return err
	})
}

func addProbeSyncFileToZip(zw *zip.Writer, sourcePath string, rootLabel string) error {
	info, err := os.Stat(sourcePath)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	header.Name = filepath.ToSlash(rootLabel)
	header.Method = zip.Deflate
	writer, err := zw.CreateHeader(header)
	if err != nil {
		return err
	}
	in, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer in.Close()
	_, err = io.Copy(writer, in)
	return err
}

func startProbeSyncGoogleDeviceAuth(clientID string, clientSecret string) (probeSyncGoogleAuthSession, error) {
	clientID = strings.TrimSpace(clientID)
	clientSecret = strings.TrimSpace(clientSecret)
	if clientID == "" {
		return probeSyncGoogleAuthSession{}, fmt.Errorf("google client id is required")
	}
	if clientSecret == "" {
		return probeSyncGoogleAuthSession{}, fmt.Errorf("google client secret is required for device authorization")
	}
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("scope", probeSyncGoogleScopeDriveFile)
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, probeSyncGoogleDeviceCodeURL, strings.NewReader(form.Encode()))
	if err != nil {
		return probeSyncGoogleAuthSession{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return probeSyncGoogleAuthSession{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return probeSyncGoogleAuthSession{}, fmt.Errorf("google device auth failed: %s", strings.TrimSpace(string(body)))
	}
	var payload probeSyncGoogleDeviceCodeResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return probeSyncGoogleAuthSession{}, err
	}
	if strings.TrimSpace(payload.DeviceCode) == "" || strings.TrimSpace(payload.UserCode) == "" {
		return probeSyncGoogleAuthSession{}, fmt.Errorf("google device auth response is invalid")
	}
	if payload.Interval <= 0 {
		payload.Interval = 5
	}
	if payload.ExpiresIn <= 0 {
		payload.ExpiresIn = 1800
	}
	session := probeSyncGoogleAuthSession{
		ID:           probeSyncRandomHexID(18),
		ClientID:     clientID,
		ClientSecret: clientSecret,
		DeviceCode:   strings.TrimSpace(payload.DeviceCode),
		UserCode:     strings.TrimSpace(payload.UserCode),
		VerifyURL:    strings.TrimSpace(payload.VerificationURL),
		CompleteURL:  strings.TrimSpace(payload.VerificationURLComplete),
		ExpiresAt:    time.Now().Add(time.Duration(payload.ExpiresIn) * time.Second),
		IntervalSec:  payload.Interval,
	}
	probeSyncGoogleAuthSessions.mu.Lock()
	now := time.Now()
	for key, item := range probeSyncGoogleAuthSessions.data {
		if now.After(item.ExpiresAt) {
			delete(probeSyncGoogleAuthSessions.data, key)
		}
	}
	probeSyncGoogleAuthSessions.data[session.ID] = session
	probeSyncGoogleAuthSessions.mu.Unlock()
	return session, nil
}

func pollProbeSyncGoogleDeviceAuth(sessionID string) (bool, string, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return false, "", fmt.Errorf("session_id is required")
	}
	probeSyncGoogleAuthSessions.mu.Lock()
	session, ok := probeSyncGoogleAuthSessions.data[sessionID]
	if ok && time.Now().After(session.ExpiresAt) {
		delete(probeSyncGoogleAuthSessions.data, sessionID)
		ok = false
	}
	probeSyncGoogleAuthSessions.mu.Unlock()
	if !ok {
		return false, "", fmt.Errorf("google auth session not found or expired")
	}
	token, pending, err := exchangeProbeSyncGoogleDeviceCode(session)
	if pending || err != nil {
		return false, "pending", err
	}
	if err := saveProbeSyncGoogleToken(token); err != nil {
		return false, "", err
	}
	probeSyncGoogleAuthSessions.mu.Lock()
	delete(probeSyncGoogleAuthSessions.data, sessionID)
	probeSyncGoogleAuthSessions.mu.Unlock()
	return true, "authorized", nil
}

func exchangeProbeSyncGoogleDeviceCode(session probeSyncGoogleAuthSession) (probeSyncGoogleToken, bool, error) {
	form := probeSyncGoogleDeviceCodeTokenForm(session, true)
	resp, body, err := postProbeSyncGoogleOAuthForm(form)
	if err != nil {
		return probeSyncGoogleToken{}, false, err
	}
	if probeSyncGoogleTokenExchangeShouldRetryWithoutSecret(resp.StatusCode, body, session.ClientSecret) {
		resp, body, err = postProbeSyncGoogleOAuthForm(probeSyncGoogleDeviceCodeTokenForm(session, false))
		if err != nil {
			return probeSyncGoogleToken{}, false, err
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var payload probeSyncGoogleTokenResponse
		_ = json.Unmarshal(body, &payload)
		switch strings.TrimSpace(payload.Error) {
		case "authorization_pending", "slow_down":
			return probeSyncGoogleToken{}, true, nil
		case "expired_token":
			return probeSyncGoogleToken{}, false, fmt.Errorf("google authorization expired")
		case "access_denied":
			return probeSyncGoogleToken{}, false, fmt.Errorf("google authorization denied")
		default:
			return probeSyncGoogleToken{}, false, fmt.Errorf("google token exchange failed: %s", firstNonEmpty(payload.ErrorDesc, strings.TrimSpace(string(body))))
		}
	}
	return parseProbeSyncGoogleTokenResponse(body, ""), false, nil
}

func probeSyncGoogleDeviceCodeTokenForm(session probeSyncGoogleAuthSession, includeSecret bool) url.Values {
	form := url.Values{}
	form.Set("client_id", session.ClientID)
	if includeSecret && strings.TrimSpace(session.ClientSecret) != "" {
		form.Set("client_secret", session.ClientSecret)
	}
	form.Set("device_code", session.DeviceCode)
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")
	return form
}

func probeSyncGoogleTokenExchangeShouldRetryWithoutSecret(statusCode int, body []byte, clientSecret string) bool {
	if strings.TrimSpace(clientSecret) == "" || statusCode < 400 {
		return false
	}
	var payload probeSyncGoogleTokenResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return false
	}
	errCode := strings.TrimSpace(payload.Error)
	errDesc := strings.ToLower(strings.TrimSpace(payload.ErrorDesc))
	return errCode == "invalid_client" && strings.Contains(errDesc, "client_secret") &&
		(strings.Contains(errDesc, "not allowed") || strings.Contains(errDesc, "must not") || strings.Contains(errDesc, "should not"))
}

func saveProbeSyncGoogleToken(token probeSyncGoogleToken) error {
	settings, err := loadProbeSyncSettings()
	if err != nil {
		return err
	}
	settings.GoogleToken = token
	return persistProbeSyncSettings(settings)
}

func clearProbeSyncGoogleToken() error {
	settings, err := loadProbeSyncSettings()
	if err != nil {
		return err
	}
	settings.GoogleToken = probeSyncGoogleToken{}
	settings.Enabled = false
	return persistProbeSyncSettings(settings)
}

func ensureProbeSyncGoogleAccessToken(settings probeSyncSettings) (probeSyncGoogleToken, error) {
	if settings.GoogleToken.hasUsableAccessToken() {
		return settings.GoogleToken, nil
	}
	return refreshProbeSyncGoogleAccessToken(settings)
}

func refreshProbeSyncGoogleAccessToken(settings probeSyncSettings) (probeSyncGoogleToken, error) {
	if strings.TrimSpace(settings.GoogleClientID) == "" {
		return probeSyncGoogleToken{}, fmt.Errorf("google client id is required")
	}
	if !settings.GoogleToken.hasRefreshToken() {
		return probeSyncGoogleToken{}, fmt.Errorf("google drive is not authorized")
	}
	form := url.Values{}
	form.Set("client_id", settings.GoogleClientID)
	if strings.TrimSpace(settings.GoogleClientSecret) != "" {
		form.Set("client_secret", settings.GoogleClientSecret)
	}
	form.Set("refresh_token", settings.GoogleToken.RefreshToken)
	form.Set("grant_type", "refresh_token")
	resp, body, err := postProbeSyncGoogleOAuthForm(form)
	if err != nil {
		return probeSyncGoogleToken{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return probeSyncGoogleToken{}, fmt.Errorf("google token refresh failed: %s", strings.TrimSpace(string(body)))
	}
	token := parseProbeSyncGoogleTokenResponse(body, settings.GoogleToken.RefreshToken)
	if err := saveProbeSyncGoogleToken(token); err != nil {
		return probeSyncGoogleToken{}, err
	}
	return token, nil
}

func postProbeSyncGoogleOAuthForm(form url.Values) (*http.Response, []byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, probeSyncGoogleTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp, body, nil
}

func parseProbeSyncGoogleTokenResponse(body []byte, fallbackRefreshToken string) probeSyncGoogleToken {
	var payload probeSyncGoogleTokenResponse
	_ = json.Unmarshal(body, &payload)
	refreshToken := strings.TrimSpace(payload.RefreshToken)
	if refreshToken == "" {
		refreshToken = strings.TrimSpace(fallbackRefreshToken)
	}
	expiresIn := payload.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	return probeSyncGoogleToken{
		AccessToken:  strings.TrimSpace(payload.AccessToken),
		RefreshToken: refreshToken,
		TokenType:    firstNonEmpty(strings.TrimSpace(payload.TokenType), "Bearer"),
		Expiry:       time.Now().Add(time.Duration(expiresIn) * time.Second),
	}
}

func ensureProbeSyncGoogleDriveFolderPath(accessToken string, folderPath string) (string, error) {
	parentID := "root"
	for _, part := range strings.Split(normalizeProbeSyncGoogleDriveFolderPath(folderPath), "/") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		folderID, err := findProbeSyncGoogleDriveChildFolder(accessToken, parentID, part)
		if err != nil {
			return "", err
		}
		if folderID == "" {
			folderID, err = createProbeSyncGoogleDriveChildFolder(accessToken, parentID, part)
			if err != nil {
				return "", err
			}
		}
		parentID = folderID
	}
	return parentID, nil
}

func findProbeSyncGoogleDriveChildFolder(accessToken string, parentID string, name string) (string, error) {
	q := fmt.Sprintf("mimeType='application/vnd.google-apps.folder' and name='%s' and '%s' in parents and trashed=false", escapeProbeSyncGoogleDriveQueryString(name), escapeProbeSyncGoogleDriveQueryString(parentID))
	params := url.Values{}
	params.Set("q", q)
	params.Set("spaces", "drive")
	params.Set("fields", "files(id,name)")
	body, err := probeSyncGoogleDriveJSONRequest(accessToken, http.MethodGet, probeSyncGoogleDriveFilesURL+"?"+params.Encode(), nil)
	if err != nil {
		return "", err
	}
	var payload struct {
		Files []struct {
			ID string `json:"id"`
		} `json:"files"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", err
	}
	if len(payload.Files) == 0 {
		return "", nil
	}
	return strings.TrimSpace(payload.Files[0].ID), nil
}

func createProbeSyncGoogleDriveChildFolder(accessToken string, parentID string, name string) (string, error) {
	payload := map[string]any{
		"name":     name,
		"mimeType": "application/vnd.google-apps.folder",
		"parents":  []string{parentID},
	}
	params := url.Values{}
	params.Set("fields", "id")
	body, err := probeSyncGoogleDriveJSONRequest(accessToken, http.MethodPost, probeSyncGoogleDriveFilesURL+"?"+params.Encode(), payload)
	if err != nil {
		return "", err
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", err
	}
	if strings.TrimSpace(out.ID) == "" {
		return "", fmt.Errorf("google drive folder create response is invalid")
	}
	return strings.TrimSpace(out.ID), nil
}

func uploadProbeSyncGoogleDriveFileResumable(accessToken string, parentID string, localPath string) error {
	info, err := os.Stat(localPath)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("google drive upload path is a directory: %s", localPath)
	}
	fileName := filepath.Base(localPath)
	contentType := mime.TypeByExtension(strings.ToLower(filepath.Ext(fileName)))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	metadata := map[string]any{
		"name":    fileName,
		"parents": []string{parentID},
	}
	metaBytes, _ := json.Marshal(metadata)
	params := url.Values{}
	params.Set("uploadType", "resumable")
	params.Set("fields", "id,name,webViewLink")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, probeSyncGoogleDriveUploadURL+"?"+params.Encode(), bytes.NewReader(metaBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("X-Upload-Content-Type", contentType)
	req.Header.Set("X-Upload-Content-Length", fmt.Sprintf("%d", info.Size()))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	initBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("google drive upload session create failed: %s", strings.TrimSpace(string(initBody)))
	}
	uploadURL := strings.TrimSpace(resp.Header.Get("Location"))
	if uploadURL == "" {
		return fmt.Errorf("google drive upload session location is empty")
	}
	in, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer in.Close()
	uploadCtx, uploadCancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer uploadCancel()
	uploadReq, err := http.NewRequestWithContext(uploadCtx, http.MethodPut, uploadURL, in)
	if err != nil {
		return err
	}
	uploadReq.Header.Set("Content-Type", contentType)
	uploadReq.ContentLength = info.Size()
	uploadResp, err := http.DefaultClient.Do(uploadReq)
	if err != nil {
		return err
	}
	defer uploadResp.Body.Close()
	uploadBody, _ := io.ReadAll(io.LimitReader(uploadResp.Body, 1<<20))
	if uploadResp.StatusCode < 200 || uploadResp.StatusCode >= 300 {
		return fmt.Errorf("google drive upload failed: %s", strings.TrimSpace(string(uploadBody)))
	}
	return nil
}

func probeSyncGoogleDriveJSONRequest(accessToken string, method string, reqURL string, payload any) ([]byte, error) {
	var body io.Reader
	if payload != nil {
		raw, _ := json.Marshal(payload)
		body = bytes.NewReader(raw)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, reqURL, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("google drive api failed: %s", strings.TrimSpace(string(respBody)))
	}
	return respBody, nil
}

func escapeProbeSyncGoogleDriveQueryString(value string) string {
	return strings.ReplaceAll(value, "'", "\\'")
}

func probeSyncRandomHexID(byteLen int) string {
	if byteLen <= 0 {
		byteLen = 16
	}
	buf := make([]byte, byteLen)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

func currentProbeSyncNodeID() string {
	if envID := strings.TrimSpace(os.Getenv("PROBE_NODE_ID")); envID != "" {
		return envID
	}
	dataDir, err := resolveDataDir()
	if err == nil {
		raw, readErr := os.ReadFile(filepath.Join(dataDir, "node_identity.json"))
		if readErr == nil {
			var identity nodeIdentity
			if json.Unmarshal(raw, &identity) == nil && strings.TrimSpace(identity.NodeID) != "" {
				return strings.TrimSpace(identity.NodeID)
			}
		}
	}
	return firstNonEmpty(detectHostName(), "probe-node")
}

func normalizeProbeSyncSubmittedGoogleOAuthCredentials(clientID *string, clientSecret *string, savedSecret string) {
	if clientID == nil || clientSecret == nil {
		return
	}
	if parsedID, parsedSecret, ok := parseProbeSyncGoogleOAuthCredentialJSON(*clientID); ok {
		*clientID = parsedID
		if strings.TrimSpace(parsedSecret) != "" {
			*clientSecret = parsedSecret
		}
		return
	}
	if parsedID, parsedSecret, ok := parseProbeSyncGoogleOAuthCredentialJSON(*clientSecret); ok {
		*clientID = parsedID
		*clientSecret = parsedSecret
		return
	}
	if strings.TrimSpace(*clientSecret) == probeSyncSecretConfiguredLabel("x") {
		*clientSecret = savedSecret
	}
}

func parseProbeSyncGoogleOAuthCredentialJSON(raw string) (string, string, bool) {
	text := strings.TrimSpace(raw)
	if !strings.HasPrefix(text, "{") {
		return "", "", false
	}
	var payload map[string]map[string]any
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		return "", "", false
	}
	for _, key := range []string{"installed", "web"} {
		section, ok := payload[key]
		if !ok {
			continue
		}
		clientID, _ := section["client_id"].(string)
		clientSecret, _ := section["client_secret"].(string)
		clientID = strings.TrimSpace(clientID)
		clientSecret = strings.TrimSpace(clientSecret)
		if clientID != "" {
			return clientID, clientSecret, true
		}
	}
	return "", "", false
}

func probeSyncSecretConfiguredLabel(secret string) string {
	if strings.TrimSpace(secret) == "" {
		return ""
	}
	return "(已保存)"
}

func probeSyncGoogleTokenExpiryString(token probeSyncGoogleToken) string {
	if token.Expiry.IsZero() {
		return ""
	}
	return token.Expiry.UTC().Format(time.RFC3339)
}
