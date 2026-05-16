package core

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	defaultAdminLogLines = 200
	maxAdminLogLines     = 2000
	logLineTimeLayout    = "2006/01/02 15:04:05.000000"
)

type adminLogLevel string

const (
	adminLogLevelRealtime adminLogLevel = "realtime"
	adminLogLevelNormal   adminLogLevel = "normal"
	adminLogLevelWarning  adminLogLevel = "warning"
	adminLogLevelError    adminLogLevel = "error"
)

type adminLogEntry struct {
	Time    string        `json:"time"`
	Level   adminLogLevel `json:"level"`
	Message string        `json:"message"`
	Line    string        `json:"line"`
}

type adminLogsResponse struct {
	Source   string          `json:"source"`
	FilePath string          `json:"file_path,omitempty"`
	Lines    int             `json:"lines"`
	Content  string          `json:"content"`
	Fetched  string          `json:"fetched_at"`
	Entries  []adminLogEntry `json:"entries,omitempty"`
}

func AdminLogsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	lineLimit := normalizeAdminLogLines(r.URL.Query().Get("lines"))
	sinceMinutes := normalizeAdminSinceMinutes(r.URL.Query().Get("since_minutes"))
	minLevel := r.URL.Query().Get("min_level")
	writeJSON(w, http.StatusOK, buildControllerRuntimeLogsResponse(lineLimit, sinceMinutes, minLevel))
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

func buildControllerRuntimeLogsResponse(lineLimit int, sinceMinutes int, minLevel string) adminLogsResponse {
	content, entries := snapshotControllerRuntimeLogTailLines(lineLimit, sinceMinutes, minLevel)
	return adminLogsResponse{
		Source:  "controller_runtime_memory",
		Lines:   lineLimit,
		Content: content,
		Fetched: time.Now().Format(time.RFC3339),
		Entries: entries,
	}
}

func snapshotControllerRuntimeLogTailLines(lineLimit int, sinceMinutes int, minLevel string) (string, []adminLogEntry) {
	lines := controllerRuntimeLogs.snapshotLines()
	cutoffEnabled := sinceMinutes > 0
	cutoff := time.Now().Add(-time.Duration(sinceMinutes) * time.Minute)
	threshold := normalizeAdminLogLevel(minLevel)
	entries := make([]adminLogEntry, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			continue
		}
		lineTime, _ := parseControllerLogLineTime(line)
		if cutoffEnabled && !lineTime.IsZero() && lineTime.Before(cutoff) {
			continue
		}
		entry := buildAdminLogEntry(line)
		if !adminLogLevelGTE(entry.Level, threshold) {
			continue
		}
		entries = append(entries, entry)
	}

	if lineLimit > 0 && len(entries) > lineLimit {
		entries = entries[len(entries)-lineLimit:]
	}
	linesOut := make([]string, 0, len(entries))
	for _, entry := range entries {
		linesOut = append(linesOut, entry.Line)
	}
	return strings.Join(linesOut, "\n"), entries
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

func buildAdminLogEntry(line string) adminLogEntry {
	trimmed := strings.TrimSpace(line)
	entry := adminLogEntry{
		Level:   inferAdminLogLevel(trimmed),
		Message: trimmed,
		Line:    trimmed,
	}
	if ts, ok := parseControllerLogLineTime(trimmed); ok {
		entry.Time = ts.Format(time.RFC3339)
		if len(trimmed) > len(logLineTimeLayout) {
			entry.Message = strings.TrimSpace(trimmed[len(logLineTimeLayout):])
		}
	}
	return entry
}

func normalizeAdminLogLevel(raw string) adminLogLevel {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "realtime", "实时":
		return adminLogLevelRealtime
	case "warning", "warn", "告警":
		return adminLogLevelWarning
	case "error", "err", "错误":
		return adminLogLevelError
	default:
		return adminLogLevelNormal
	}
}

func inferAdminLogLevel(line string) adminLogLevel {
	lower := strings.ToLower(strings.TrimSpace(line))
	switch {
	case strings.Contains(lower, "[error]") || strings.Contains(lower, " error ") || strings.Contains(lower, "failed") || strings.Contains(lower, "panic") || strings.Contains(lower, "fatal") || strings.Contains(lower, "错误"):
		return adminLogLevelError
	case strings.Contains(lower, "[warning]") || strings.Contains(lower, "[warn]") || strings.Contains(lower, " warning") || strings.Contains(lower, "告警") || strings.Contains(lower, "警告"):
		return adminLogLevelWarning
	case strings.Contains(lower, "[realtime]") || strings.Contains(lower, "实时") || strings.Contains(lower, "trace") || strings.Contains(lower, "debug"):
		return adminLogLevelRealtime
	default:
		return adminLogLevelNormal
	}
}

func adminLogLevelRank(level adminLogLevel) int {
	switch normalizeAdminLogLevel(string(level)) {
	case adminLogLevelRealtime:
		return 0
	case adminLogLevelNormal:
		return 1
	case adminLogLevelWarning:
		return 2
	case adminLogLevelError:
		return 3
	default:
		return 1
	}
}

func adminLogLevelGTE(level adminLogLevel, minLevel adminLogLevel) bool {
	return adminLogLevelRank(level) >= adminLogLevelRank(minLevel)
}
