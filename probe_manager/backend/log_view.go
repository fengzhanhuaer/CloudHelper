package backend

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultLogViewLines = 200
	maxLogViewLines     = 2000
	logLineTimeLayout   = "2006/01/02 15:04:05.000000"
)

type LogViewResponse struct {
	Source   string `json:"source"`
	FilePath string `json:"file_path"`
	Lines    int    `json:"lines"`
	Content  string `json:"content"`
	Fetched  string `json:"fetched_at"`
}

func (a *App) GetLocalManagerLogs(lines int, sinceMinutes int) (LogViewResponse, error) {
	lineLimit := normalizeLogViewLines(lines)
	logPath, err := resolveManagerLogPath()
	if err != nil {
		return LogViewResponse{}, err
	}

	content, err := readLogTailLines(logPath, lineLimit, sinceMinutes)
	if err != nil {
		return LogViewResponse{}, err
	}

	return LogViewResponse{
		Source:   "local",
		FilePath: logPath,
		Lines:    lineLimit,
		Content:  content,
		Fetched:  time.Now().Format(time.RFC3339),
	}, nil
}

func normalizeLogViewLines(lines int) int {
	if lines <= 0 {
		return defaultLogViewLines
	}
	if lines > maxLogViewLines {
		return maxLogViewLines
	}
	return lines
}

func resolveManagerLogPath() (string, error) {
	dirs := managerCandidateLogDirs()
	if len(dirs) == 0 {
		return "", errors.New("manager log directory is not available")
	}

	for _, dir := range dirs {
		candidate := filepath.Join(dir, managerLogFile)
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

	return filepath.Join(dirs[0], managerLogFile), nil
}

func readLogTailLines(filePath string, lineLimit int, sinceMinutes int) (string, error) {
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
			lineTime, ok := parseLogLineTime(line)
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

func parseLogLineTime(line string) (time.Time, bool) {
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
