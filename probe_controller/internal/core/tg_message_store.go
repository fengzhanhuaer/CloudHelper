package core

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const (
	tgAssistantMessageDBFile       = "messages.db"
	tgAssistantStoragePruneTarget  = 9
	tgAssistantStoragePruneDivisor = 10
)

var (
	tgAssistantMessageDBMu       sync.Mutex
	tgAssistantVideoDir          = "./temp/mv"
	tgAssistantMessageDBMaxBytes = int64(64 * 1024 * 1024)
	tgAssistantVideoDirMaxBytes  = int64(2 * 1024 * 1024 * 1024)
)

func tgAssistantMessageDBPath() string {
	return filepath.Join(tgAssistantTempDirPath(), tgAssistantMessageDBFile)
}

func tgAssistantVideoDirPath() string {
	return filepath.Clean(tgAssistantVideoDir)
}

func tgAssistantVideoFilePath(accountID, target string, messageID int, documentID int64, mimeType string) string {
	return tgAssistantMediaFilePath(accountID, target, messageID, documentID, mimeType, "video")
}

func tgAssistantMediaFilePath(accountID, target string, messageID int, documentID int64, mimeType, mediaType string) string {
	account := safeTGAssistantMessagePathSegment(accountID)
	peer := safeTGAssistantMessagePathSegment(target)
	kind := safeTGAssistantMessagePathSegment(mediaType)
	if kind == "unknown" {
		kind = "media"
	}
	ext := tgAssistantVideoFileExtension(mimeType)
	return filepath.Join(tgAssistantVideoDirPath(), account, peer, strings.TrimSpace(strings.Join([]string{
		strings.TrimSpace(strconv.Itoa(messageID)),
		strings.TrimSpace(strconv.FormatInt(documentID, 10)),
	}, "_"))+"_"+kind+ext)
}

func tgAssistantMediaFileURL(accountID, target string, messageID int, documentID int64, mimeType, mediaType string) string {
	values := url.Values{}
	values.Set("account_id", accountID)
	values.Set("target", target)
	values.Set("message_id", strconv.Itoa(messageID))
	values.Set("document_id", strconv.FormatInt(documentID, 10))
	values.Set("media_type", mediaType)
	values.Set("mime_type", mimeType)
	return "/mng/api/tg/session/media?" + values.Encode()
}

func safeTGAssistantMessagePathSegment(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_", " ", "_", "\t", "_", "\n", "_", "\r", "_")
	value = replacer.Replace(value)
	if value == "" || value == "." || value == ".." {
		return "unknown"
	}
	return value
}

func tgAssistantVideoFileExtension(mimeType string) string {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	case "video/webm":
		return ".webm"
	case "video/quicktime":
		return ".mov"
	case "video/x-matroska":
		return ".mkv"
	default:
		return ".mp4"
	}
}

func openTGAssistantMessageDB() (*sql.DB, error) {
	if err := os.MkdirAll(tgAssistantTempDirPath(), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", tgAssistantMessageDBPath())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA busy_timeout = 5000`); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS tg_messages (
	account_id TEXT NOT NULL,
	target TEXT NOT NULL,
	message_id INTEGER NOT NULL,
	date TEXT NOT NULL,
	out INTEGER NOT NULL DEFAULT 0,
	sender_id TEXT,
	sender_name TEXT,
	text TEXT,
	service INTEGER NOT NULL DEFAULT 0,
	media_type TEXT,
	media_path TEXT,
	media_size INTEGER NOT NULL DEFAULT 0,
	formats_json TEXT,
	web_preview_json TEXT,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	PRIMARY KEY (account_id, target, message_id)
);
CREATE INDEX IF NOT EXISTS idx_tg_messages_account_target_date
ON tg_messages (account_id, target, date);
CREATE INDEX IF NOT EXISTS idx_tg_messages_media_path
ON tg_messages (media_path);
`); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := ensureTGAssistantMessageDBColumns(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func ensureTGAssistantMessageDBColumns(db *sql.DB) error {
	columns, err := listTGAssistantMessageDBColumns(db)
	if err != nil {
		return err
	}
	for _, stmt := range []struct {
		name string
		sql  string
	}{
		{name: "media_type", sql: `ALTER TABLE tg_messages ADD COLUMN media_type TEXT`},
		{name: "media_path", sql: `ALTER TABLE tg_messages ADD COLUMN media_path TEXT`},
		{name: "media_size", sql: `ALTER TABLE tg_messages ADD COLUMN media_size INTEGER NOT NULL DEFAULT 0`},
		{name: "formats_json", sql: `ALTER TABLE tg_messages ADD COLUMN formats_json TEXT`},
		{name: "web_preview_json", sql: `ALTER TABLE tg_messages ADD COLUMN web_preview_json TEXT`},
	} {
		if columns[stmt.name] {
			continue
		}
		if _, err := db.Exec(stmt.sql); err != nil {
			return err
		}
	}
	_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_tg_messages_media_path ON tg_messages (media_path)`)
	return err
}

func listTGAssistantMessageDBColumns(db *sql.DB) (map[string]bool, error) {
	rows, err := db.Query(`PRAGMA table_info(tg_messages)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	columns := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return nil, err
		}
		columns[strings.ToLower(strings.TrimSpace(name))] = true
	}
	return columns, rows.Err()
}

func storeTGAssistantSessionMessages(accountID, target string, messages []tgAssistantSessionMessage) error {
	accountID = strings.TrimSpace(accountID)
	target = strings.TrimSpace(target)
	if accountID == "" {
		return errors.New("account_id is required")
	}
	if target == "" {
		return errors.New("target is required")
	}
	if len(messages) == 0 {
		return nil
	}

	tgAssistantMessageDBMu.Lock()
	defer tgAssistantMessageDBMu.Unlock()

	db, err := openTGAssistantMessageDB()
	if err != nil {
		return err
	}
	defer db.Close()

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(`
INSERT INTO tg_messages (
	account_id, target, message_id, date, out, sender_id, sender_name, text, service, media_type, media_path, media_size, formats_json, web_preview_json, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(account_id, target, message_id) DO UPDATE SET
	date = excluded.date,
	out = excluded.out,
	sender_id = excluded.sender_id,
	sender_name = excluded.sender_name,
	text = excluded.text,
	service = excluded.service,
	media_type = excluded.media_type,
	media_path = excluded.media_path,
	media_size = excluded.media_size,
	formats_json = excluded.formats_json,
	web_preview_json = excluded.web_preview_json,
	updated_at = excluded.updated_at
`)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	defer stmt.Close()

	now := time.Now().UTC().Format(time.RFC3339)
	for _, message := range messages {
		if message.ID <= 0 {
			continue
		}
		date := strings.TrimSpace(message.Date)
		if date == "" || date == "-" {
			date = now
		}
		formatsJSON, _ := json.Marshal(message.Formats)
		webPreviewJSON, _ := json.Marshal(message.WebPreview)
		if _, err := stmt.Exec(
			accountID,
			target,
			message.ID,
			date,
			boolToSQLiteInt(message.Out),
			strings.TrimSpace(message.SenderID),
			strings.TrimSpace(message.SenderName),
			message.Text,
			boolToSQLiteInt(message.Service),
			strings.TrimSpace(message.MediaType),
			strings.TrimSpace(message.MediaPath),
			message.MediaSize,
			string(formatsJSON),
			string(webPreviewJSON),
			now,
			now,
		); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return pruneTGAssistantMessageStorageLocked(db)
}

func listStoredTGAssistantSessionMessages(accountID, target string, limit int) ([]tgAssistantSessionMessage, error) {
	accountID = strings.TrimSpace(accountID)
	target = strings.TrimSpace(target)
	if accountID == "" {
		return nil, errors.New("account_id is required")
	}
	if target == "" {
		return nil, errors.New("target is required")
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}

	tgAssistantMessageDBMu.Lock()
	defer tgAssistantMessageDBMu.Unlock()

	db, err := openTGAssistantMessageDB()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := db.Query(`
SELECT message_id, date, text, out, sender_id, sender_name, service, media_type, media_path, media_size, formats_json, web_preview_json
FROM tg_messages
WHERE account_id = ? AND target = ?
ORDER BY message_id DESC
LIMIT ?
`, accountID, target, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	reversed := []tgAssistantSessionMessage{}
	for rows.Next() {
		var item tgAssistantSessionMessage
		var out, service int
		var mediaType, mediaPath, formatsJSON, webPreviewJSON sql.NullString
		if err := rows.Scan(&item.ID, &item.Date, &item.Text, &out, &item.SenderID, &item.SenderName, &service, &mediaType, &mediaPath, &item.MediaSize, &formatsJSON, &webPreviewJSON); err != nil {
			return nil, err
		}
		item.Out = out != 0
		item.Service = service != 0
		item.MediaType = mediaType.String
		item.MediaPath = mediaPath.String
		if formatsJSON.Valid && strings.TrimSpace(formatsJSON.String) != "" {
			_ = json.Unmarshal([]byte(formatsJSON.String), &item.Formats)
		}
		if webPreviewJSON.Valid && strings.TrimSpace(webPreviewJSON.String) != "" && strings.TrimSpace(webPreviewJSON.String) != "null" {
			_ = json.Unmarshal([]byte(webPreviewJSON.String), &item.WebPreview)
		}
		reversed = append(reversed, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for i, j := 0, len(reversed)-1; i < j; i, j = i+1, j-1 {
		reversed[i], reversed[j] = reversed[j], reversed[i]
	}
	return reversed, nil
}

func pruneTGAssistantMessageStorageLocked(db *sql.DB) error {
	if err := pruneTGAssistantMessageDBLocked(db); err != nil {
		return err
	}
	return pruneTGAssistantVideoDirLocked(db)
}

func pruneTGAssistantMessageDBLocked(db *sql.DB) error {
	targetBytes := tgAssistantMessageDBMaxBytes * tgAssistantStoragePruneTarget / tgAssistantStoragePruneDivisor
	for i := 0; i < 100; i++ {
		info, err := os.Stat(tgAssistantMessageDBPath())
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.Size() <= tgAssistantMessageDBMaxBytes {
			return nil
		}

		paths, err := oldestTGAssistantMessageMediaPaths(db, 1000)
		if err != nil {
			return err
		}
		result, err := db.Exec(`
DELETE FROM tg_messages
WHERE rowid IN (
	SELECT rowid FROM tg_messages ORDER BY date ASC, message_id ASC LIMIT 1000
)
`)
		if err != nil {
			return err
		}
		deleted, _ := result.RowsAffected()
		if deleted <= 0 {
			return nil
		}
		removeTGAssistantMediaFiles(paths)
		if _, err := db.Exec(`VACUUM`); err != nil {
			return err
		}
		if info, err := os.Stat(tgAssistantMessageDBPath()); err == nil && info.Size() <= targetBytes {
			return nil
		}
	}
	return nil
}

func oldestTGAssistantMessageMediaPaths(db *sql.DB, limit int) ([]string, error) {
	rows, err := db.Query(`
SELECT media_path FROM tg_messages
WHERE media_path IS NOT NULL AND TRIM(media_path) != ''
ORDER BY date ASC, message_id ASC
LIMIT ?
`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	paths := []string{}
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, err
		}
		if path = strings.TrimSpace(path); path != "" {
			paths = append(paths, path)
		}
	}
	return paths, rows.Err()
}

type tgAssistantVideoFileInfo struct {
	path    string
	size    int64
	modTime time.Time
}

func pruneTGAssistantVideoDirLocked(db *sql.DB) error {
	files, total, err := listTGAssistantVideoFiles()
	if err != nil {
		return err
	}
	if total <= tgAssistantVideoDirMaxBytes {
		return nil
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].modTime.Equal(files[j].modTime) {
			return files[i].path < files[j].path
		}
		return files[i].modTime.Before(files[j].modTime)
	})

	targetBytes := tgAssistantVideoDirMaxBytes * tgAssistantStoragePruneTarget / tgAssistantStoragePruneDivisor
	removed := []string{}
	for _, file := range files {
		if total <= targetBytes {
			break
		}
		if err := os.Remove(file.path); err != nil && !os.IsNotExist(err) {
			return err
		}
		total -= file.size
		removed = append(removed, file.path)
	}
	return clearTGAssistantRemovedMediaPaths(db, removed)
}

func listTGAssistantVideoFiles() ([]tgAssistantVideoFileInfo, int64, error) {
	dir := tgAssistantVideoDirPath()
	files := []tgAssistantVideoFileInfo{}
	var total int64
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		size := info.Size()
		total += size
		files = append(files, tgAssistantVideoFileInfo{path: path, size: size, modTime: info.ModTime()})
		return nil
	})
	if os.IsNotExist(err) {
		return nil, 0, nil
	}
	return files, total, err
}

func clearTGAssistantRemovedMediaPaths(db *sql.DB, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	stmt, err := db.Prepare(`UPDATE tg_messages SET media_path = '', media_size = 0 WHERE media_path = ?`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, path := range paths {
		if _, err := stmt.Exec(path); err != nil {
			return err
		}
	}
	return nil
}

func loadTGAssistantSessionMediaPath(accountID, target string, messageID int, documentID string) (string, error) {
	accountID = strings.TrimSpace(accountID)
	target = strings.TrimSpace(target)
	documentID = strings.TrimSpace(documentID)
	if accountID == "" || target == "" || messageID <= 0 || documentID == "" {
		return "", errors.New("invalid media key")
	}

	tgAssistantMessageDBMu.Lock()
	defer tgAssistantMessageDBMu.Unlock()

	db, err := openTGAssistantMessageDB()
	if err != nil {
		return "", err
	}
	defer db.Close()

	var path string
	err = db.QueryRow(`
SELECT media_path
FROM tg_messages
WHERE account_id = ? AND target = ? AND message_id = ?
LIMIT 1
`, accountID, target, messageID).Scan(&path)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", errors.New("media not found")
		}
		return "", err
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("media not found")
	}
	if !pathInsideOrEqual(path, tgAssistantVideoDirPath()) {
		return "", errors.New("media path is out of range")
	}
	base := filepath.Base(path)
	if !strings.Contains(base, documentID) {
		return "", errors.New("media document mismatch")
	}
	return path, nil
}

func removeTGAssistantMediaFiles(paths []string) {
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		if !pathInsideOrEqual(path, tgAssistantVideoDirPath()) {
			continue
		}
		_ = os.Remove(path)
	}
}

func boolToSQLiteInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
