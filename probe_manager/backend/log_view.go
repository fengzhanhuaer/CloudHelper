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

type LogLevel string

const (
	LogLevelRealtime LogLevel = "realtime"
	LogLevelNormal   LogLevel = "normal"
	LogLevelWarning  LogLevel = "warning"
	LogLevelError    LogLevel = "error"
)

type LogEntry struct {
	Time    string   `json:"time"`
	Level   LogLevel `json:"level"`
	Message string   `json:"message"`
	Line    string   `json:"line"`
}

type LogViewResponse struct {
	Source   string     `json:"source"`
	FilePath string     `json:"file_path"`
	Lines    int        `json:"lines"`
	Content  string     `json:"content"`
	Fetched  string     `json:"fetched_at"`
	Entries  []LogEntry `json:"entries,omitempty"`
}

func (a *App) GetLocalManagerLogs(lines int, sinceMinutes int, minLevel string) (LogViewResponse, error) {
	lineLimit := normalizeLogViewLines(lines)
	logPath, err := resolveManagerLogPath()
	if err != nil {
		return LogViewResponse{}, err
	}

	content, entries, err := readLogTailLines(logPath, lineLimit, sinceMinutes, minLevel)
	if err != nil {
		return LogViewResponse{}, err
	}

	return LogViewResponse{
		Source:   "local",
		FilePath: logPath,
		Lines:    lineLimit,
		Content:  content,
		Fetched:  time.Now().Format(time.RFC3339),
		Entries:  entries,
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

func readLogTailLines(filePath string, lineLimit int, sinceMinutes int, minLevel string) (string, []LogEntry, error) {
	raw, err := os.ReadFile(filePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil, nil
		}
		return "", nil, fmt.Errorf("read log file failed: %w", err)
	}

	normalized := strings.ReplaceAll(string(raw), "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	cutoffEnabled := sinceMinutes > 0
	cutoff := time.Now().Add(-time.Duration(sinceMinutes) * time.Minute)
	threshold := normalizeLogLevel(minLevel)
	filteredEntries := make([]LogEntry, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			continue
		}
		lineTime, _ := parseLogLineTime(line)
		if cutoffEnabled && !lineTime.IsZero() && lineTime.Before(cutoff) {
			continue
		}
		entry := buildLogEntry(line)
		if !logLevelGTE(entry.Level, threshold) {
			continue
		}
		filteredEntries = append(filteredEntries, entry)
	}

	if lineLimit > 0 && len(filteredEntries) > lineLimit {
		filteredEntries = filteredEntries[len(filteredEntries)-lineLimit:]
	}
	linesOut := make([]string, 0, len(filteredEntries))
	for _, entry := range filteredEntries {
		linesOut = append(linesOut, entry.Line)
	}
	return strings.Join(linesOut, "\n"), filteredEntries, nil
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

func buildLogEntry(line string) LogEntry {
	trimmed := strings.TrimSpace(line)
	entry := LogEntry{
		Level:   inferLogLevel(trimmed),
		Message: trimmed,
		Line:    trimmed,
	}
	if ts, ok := parseLogLineTime(trimmed); ok {
		entry.Time = ts.Format(time.RFC3339)
		if len(trimmed) > len(logLineTimeLayout) {
			entry.Message = strings.TrimSpace(trimmed[len(logLineTimeLayout):])
		}
	}
	return entry
}

func normalizeLogLevel(raw string) LogLevel {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "realtime", "实时":
		return LogLevelRealtime
	case "warning", "warn", "告警":
		return LogLevelWarning
	case "error", "err", "错误":
		return LogLevelError
	default:
		return LogLevelNormal
	}
}

func inferLogLevel(line string) LogLevel {
	lower := strings.ToLower(strings.TrimSpace(line))
	switch {
	case strings.Contains(lower, "[error]") || strings.Contains(lower, " error ") || strings.Contains(lower, "failed") || strings.Contains(lower, "panic") || strings.Contains(lower, "fatal") || strings.Contains(lower, "错误"):
		return LogLevelError
	case strings.Contains(lower, "[warn]") || strings.Contains(lower, " warning") || strings.Contains(lower, "告警") || strings.Contains(lower, "警告"):
		return LogLevelWarning
	case strings.Contains(lower, "实时") || strings.Contains(lower, "trace") || strings.Contains(lower, "debug"):
		return LogLevelRealtime
	default:
		return LogLevelNormal
	}
}

func logLevelRank(level LogLevel) int {
	switch normalizeLogLevel(string(level)) {
	case LogLevelRealtime:
		return 0
	case LogLevelNormal:
		return 1
	case LogLevelWarning:
		return 2
	case LogLevelError:
		return 3
	default:
		return 1
	}
}

func logLevelGTE(level LogLevel, minLevel LogLevel) bool {
	return logLevelRank(level) >= logLevelRank(minLevel)
}
