package core

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	defaultAdminLogLines = 200
	maxAdminLogLines     = 2000
	logLineTimeLayout    = "2006/01/02 15:04:05.000000"
)

type adminLogsResponse struct {
	Source   string `json:"source"`
	FilePath string `json:"file_path"`
	Lines    int    `json:"lines"`
	Content  string `json:"content"`
	Fetched  string `json:"fetched_at"`
}

func AdminLogsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	lineLimit := normalizeAdminLogLines(r.URL.Query().Get("lines"))
	sinceMinutes := normalizeAdminSinceMinutes(r.URL.Query().Get("since_minutes"))
	logPath, err := resolveControllerLogPath()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	content, err := readControllerLogTailLines(logPath, lineLimit, sinceMinutes)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, adminLogsResponse{
		Source:   "server",
		FilePath: logPath,
		Lines:    lineLimit,
		Content:  content,
		Fetched:  time.Now().Format(time.RFC3339),
	})
}

func normalizeAdminLogLines(raw string) int {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return defaultAdminLogLines
	}

	value, err := strconv.Atoi(trimmed)
	if err != nil || value <= 0 {
		return defaultAdminLogLines
	}
	if value > maxAdminLogLines {
		return maxAdminLogLines
	}
	return value
}

func normalizeAdminSinceMinutes(raw string) int {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0
	}
	value, err := strconv.Atoi(trimmed)
	if err != nil || value <= 0 {
		return 0
	}
	if value > maxAdminLogLines {
		return maxAdminLogLines
	}
	return value
}

func resolveControllerLogPath() (string, error) {
	dirs := candidateLogDirs()
	if len(dirs) == 0 {
		return "", errors.New("controller log directory is not available")
	}

	for _, dir := range dirs {
		candidate := filepath.Join(dir, controllerLogFile)
		info, err := os.Stat(candidate)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return "", err
		}
		if info.IsDir() {
			continue
		}
		return candidate, nil
	}

	return filepath.Join(dirs[0], controllerLogFile), nil
}

func readControllerLogTailLines(filePath string, lineLimit int, sinceMinutes int) (string, error) {
	raw, err := os.ReadFile(filePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("read log file failed: %w", err)
	}

	normalized := strings.ReplaceAll(string(raw), "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	if sinceMinutes > 0 {
		cutoff := time.Now().Add(-time.Duration(sinceMinutes) * time.Minute)
		filtered := make([]string, 0, len(lines))
		for _, line := range lines {
			if line == "" {
				continue
			}
			lineTime, ok := parseControllerLogLineTime(line)
			if ok && lineTime.Before(cutoff) {
				continue
			}
			filtered = append(filtered, line)
		}
		lines = filtered
	}

	if lineLimit > 0 && len(lines) > lineLimit {
		lines = lines[len(lines)-lineLimit:]
	}
	return strings.Join(lines, "\n"), nil
}

func parseControllerLogLineTime(line string) (time.Time, bool) {
	if len(line) < len(logLineTimeLayout) {
		return time.Time{}, false
	}
	prefix := line[:len(logLineTimeLayout)]
	parsed, err := time.ParseInLocation(logLineTimeLayout, prefix, time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}
