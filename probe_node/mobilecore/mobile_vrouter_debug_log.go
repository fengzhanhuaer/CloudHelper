package mobilecore

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

const (
	mobileVRouteDebugLogDefaultLines     = 200
	mobileVRouteDebugLogMaxLines         = 500
	mobileVRouteDebugLogPayloadSoftLimit = mobileVRouteFrameMaxDataBytes - 4096
)

type mobileVRouteDebugLogPayload struct {
	RequestID         string            `json:"request_id"`
	SourceNodeID      string            `json:"source_node_id,omitempty"`
	TargetNodeID      string            `json:"target_node_id,omitempty"`
	Path              []string          `json:"path,omitempty"`
	Lines             int               `json:"lines,omitempty"`
	SinceMinutes      int               `json:"since_minutes,omitempty"`
	MinLevel          string            `json:"min_level,omitempty"`
	Keyword           string            `json:"keyword,omitempty"`
	Source            string            `json:"source,omitempty"`
	FilePath          string            `json:"file_path,omitempty"`
	Content           string            `json:"content,omitempty"`
	Entries           []androidLogEntry `json:"entries,omitempty"`
	Count             int               `json:"count,omitempty"`
	Truncated         bool              `json:"truncated,omitempty"`
	OK                bool              `json:"ok,omitempty"`
	Error             string            `json:"error,omitempty"`
	Responder         string            `json:"responder,omitempty"`
	CreatedAtUnixNano int64             `json:"created_at_unix_nano,omitempty"`
	RespondedUnixNano int64             `json:"responded_unix_nano,omitempty"`
}

func (c *mobileVRouteCarrier) respondToDebugLogFrame(frame mobileVRouteFrame, path []string) error {
	if c == nil {
		return errors.New("mobile vroute debug log carrier is nil")
	}
	request := mobileVRouteDebugLogPayload{}
	if err := json.Unmarshal(frame.Data, &request); err != nil {
		return err
	}
	if strings.TrimSpace(request.RequestID) == "" {
		return errors.New("mobile vroute debug log request id is empty")
	}
	localNodeID := normalizeMobileRouteNodeID(c.plan.LocalNode)
	if localNodeID == "" {
		return errors.New("mobile vroute debug log local node id is empty")
	}
	response := collectMobileVRouteDebugLogs(request, localNodeID)
	response.Path = reverseMobileVRoutePath(path)
	payload, err := json.Marshal(response)
	if err != nil {
		return err
	}
	control, err := json.Marshal(mobileVRouteFrameControlEnvelope{Path: response.Path})
	if err != nil {
		return err
	}
	androidLogStore.add("route", "normal", "vroute peer log pull completed: request_id="+response.RequestID+" source="+response.SourceNodeID+" lines="+strconv.Itoa(response.Count))
	return c.enqueueFrame(mobileVRouteFrame{
		MainType: mobileVRouteFrameMainTypeDebugLog,
		SubType:  mobileVRouteDebugLogSubTypeResponse,
		Control:  control,
		Data:     payload,
	})
}

func collectMobileVRouteDebugLogs(request mobileVRouteDebugLogPayload, localNodeID string) mobileVRouteDebugLogPayload {
	response := request
	response.Lines = normalizeMobileVRouteDebugLogLines(request.Lines)
	response.SinceMinutes = normalizeAndroidLogSinceMinutes(request.SinceMinutes)
	response.MinLevel = normalizeAndroidLogLevel(request.MinLevel)
	response.Keyword = strings.TrimSpace(request.Keyword)
	response.Source = "android"
	response.FilePath = "memory://android_app"
	response.OK = true
	response.Error = ""
	response.Responder = normalizeMobileRouteNodeID(localNodeID)
	response.RespondedUnixNano = time.Now().UnixNano()
	_, entries := androidLogStore.tail(androidLogMaxEntries, response.SinceMinutes, response.MinLevel)
	entries = filterMobileVRouteDebugLogEntries(entries, response.Keyword)
	if len(entries) > response.Lines {
		entries = entries[len(entries)-response.Lines:]
	}
	response.Entries = entries
	response.Count = len(entries)
	response.Content = buildMobileVRouteDebugLogContent(entries)
	fitMobileVRouteDebugLogPayload(&response)
	return response
}

func normalizeMobileVRouteDebugLogLines(lines int) int {
	if lines <= 0 {
		return mobileVRouteDebugLogDefaultLines
	}
	if lines > mobileVRouteDebugLogMaxLines {
		return mobileVRouteDebugLogMaxLines
	}
	return lines
}

func filterMobileVRouteDebugLogEntries(entries []androidLogEntry, keyword string) []androidLogEntry {
	needle := strings.ToLower(strings.TrimSpace(keyword))
	if needle == "" {
		return entries
	}
	out := make([]androidLogEntry, 0, len(entries))
	for _, entry := range entries {
		if strings.Contains(strings.ToLower(entry.Line), needle) || strings.Contains(strings.ToLower(entry.Message), needle) {
			out = append(out, entry)
		}
	}
	return out
}

func buildMobileVRouteDebugLogContent(entries []androidLogEntry) string {
	lines := make([]string, 0, len(entries))
	for _, entry := range entries {
		if line := strings.TrimSpace(entry.Line); line != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

func fitMobileVRouteDebugLogPayload(response *mobileVRouteDebugLogPayload) {
	if response == nil {
		return
	}
	for len(response.Entries) > 0 {
		raw, err := json.Marshal(response)
		if err != nil || len(raw) <= mobileVRouteDebugLogPayloadSoftLimit {
			return
		}
		drop := len(response.Entries) / 4
		if drop < 1 {
			drop = 1
		}
		response.Entries = response.Entries[drop:]
		response.Count = len(response.Entries)
		response.Content = buildMobileVRouteDebugLogContent(response.Entries)
		response.Truncated = true
	}
}

func reverseMobileVRoutePath(path []string) []string {
	out := make([]string, 0, len(path))
	for index := len(path) - 1; index >= 0; index-- {
		if nodeID := normalizeMobileRouteNodeID(path[index]); nodeID != "" {
			out = append(out, nodeID)
		}
	}
	return out
}
