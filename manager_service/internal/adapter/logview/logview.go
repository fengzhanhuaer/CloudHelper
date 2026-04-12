// Package logview provides log file reading utilities migrated from probe_manager/backend.
// It reads and tail-filters the manager_service log file.
// PKG-W2-04 / RQ-004
package logview

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultLines    = 200
	maxLines        = 2000
	timeLayout      = "2006/01/02 15:04:05.000000"
	logFileName     = "manager_service.log"
)

// LogLevel denotes the severity of a log entry.
type LogLevel string

const (
	LevelRealtime LogLevel = "realtime"
	LevelNormal   LogLevel = "normal"
	LevelWarning  LogLevel = "warning"
	LevelError    LogLevel = "error"
)

// LogEntry is a parsed log line.
type LogEntry struct {
	Time    string   `json:"time"`
	Level   LogLevel `json:"level"`
	Message string   `json:"message"`
	Line    string   `json:"line"`
}

// Response is the structured response returned by ReadManagerLogs.
type Response struct {
	FilePath string     `json:"file_path"`
	Lines    int        `json:"lines"`
	Content  string     `json:"content"`
	FetchedAt string    `json:"fetched_at"`
	Entries  []LogEntry `json:"entries,omitempty"`
}

// ReadManagerLogs reads tail lines from the manager log file.
// logDir should point to the directory containing manager_service.log.
func ReadManagerLogs(logDir string, lines, sinceMinutes int, minLevel string) (Response, error) {
	logPath := filepath.Join(logDir, logFileName)
	lineLimit := normalizeLines(lines)
	content, entries, err := readTail(logPath, lineLimit, sinceMinutes, minLevel)
	if err != nil {
		return Response{}, err
	}
	return Response{
		FilePath:  logPath,
		Lines:     lineLimit,
		Content:   content,
		FetchedAt: time.Now().Format(time.RFC3339),
		Entries:   entries,
	}, nil
}

func readTail(filePath string, lineLimit, sinceMinutes int, minLevel string) (string, []LogEntry, error) {
	raw, err := os.ReadFile(filePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil, nil
		}
		return "", nil, fmt.Errorf("read log file: %w", err)
	}
	normalized := strings.ReplaceAll(string(raw), "\r\n", "\n")
	allLines := strings.Split(normalized, "\n")
	if len(allLines) > 0 && allLines[len(allLines)-1] == "" {
		allLines = allLines[:len(allLines)-1]
	}
	cutoffEnabled := sinceMinutes > 0
	cutoff := time.Now().Add(-time.Duration(sinceMinutes) * time.Minute)
	threshold := normalizeLevel(minLevel)

	filtered := make([]LogEntry, 0, len(allLines))
	for _, line := range allLines {
		if line == "" {
			continue
		}
		lTime, _ := parseTime(line)
		if cutoffEnabled && !lTime.IsZero() && lTime.Before(cutoff) {
			continue
		}
		entry := buildEntry(line)
		if !levelGTE(entry.Level, threshold) {
			continue
		}
		filtered = append(filtered, entry)
	}
	if lineLimit > 0 && len(filtered) > lineLimit {
		filtered = filtered[len(filtered)-lineLimit:]
	}
	lines := make([]string, 0, len(filtered))
	for _, e := range filtered {
		lines = append(lines, e.Line)
	}
	return strings.Join(lines, "\n"), filtered, nil
}

func parseTime(line string) (time.Time, bool) {
	if len(line) < len(timeLayout) {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation(timeLayout, line[:len(timeLayout)], time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

func buildEntry(line string) LogEntry {
	trimmed := strings.TrimSpace(line)
	entry := LogEntry{
		Level:   inferLevel(trimmed),
		Message: trimmed,
		Line:    trimmed,
	}
	if ts, ok := parseTime(trimmed); ok {
		entry.Time = ts.Format(time.RFC3339)
		if len(trimmed) > len(timeLayout) {
			entry.Message = strings.TrimSpace(trimmed[len(timeLayout):])
		}
	}
	return entry
}

func inferLevel(line string) LogLevel {
	lower := strings.ToLower(line)
	switch {
	case strings.Contains(lower, "[error]") || strings.Contains(lower, "failed") ||
		strings.Contains(lower, "panic") || strings.Contains(lower, "fatal"):
		return LevelError
	case strings.Contains(lower, "[warning]") || strings.Contains(lower, "warn"):
		return LevelWarning
	case strings.Contains(lower, "trace") || strings.Contains(lower, "debug"):
		return LevelRealtime
	default:
		return LevelNormal
	}
}

func normalizeLevel(raw string) LogLevel {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "realtime":
		return LevelRealtime
	case "warning", "warn":
		return LevelWarning
	case "error", "err":
		return LevelError
	default:
		return LevelNormal
	}
}

func levelRank(l LogLevel) int {
	switch normalizeLevel(string(l)) {
	case LevelRealtime:
		return 0
	case LevelNormal:
		return 1
	case LevelWarning:
		return 2
	case LevelError:
		return 3
	}
	return 1
}

func levelGTE(level, min LogLevel) bool { return levelRank(level) >= levelRank(min) }

func normalizeLines(n int) int {
	if n <= 0 {
		return defaultLines
	}
	if n > maxLines {
		return maxLines
	}
	return n
}
