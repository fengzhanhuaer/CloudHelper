package core

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	maxProbeShellShortcutCount   = 200
	maxProbeShellShortcutNameLen = 64
	maxProbeShellShortcutCmdLen  = 4096
)

type probeShellShortcutRecord struct {
	Name      string `json:"name"`
	Command   string `json:"command"`
	UpdatedAt string `json:"updated_at"`
}

func loadProbeShellShortcutsLocked() []probeShellShortcutRecord {
	raw := ProbeStore.data.ProbeShellShortcuts
	if len(raw) == 0 {
		return []probeShellShortcutRecord{}
	}
	out := make([]probeShellShortcutRecord, 0, len(raw))
	out = append(out, raw...)
	return normalizeProbeShellShortcuts(out)
}

func upsertProbeShellShortcutLocked(name string, command string) ([]probeShellShortcutRecord, error) {
	trimmedName := strings.TrimSpace(name)
	trimmedCommand := strings.TrimSpace(command)
	if trimmedName == "" {
		return nil, fmt.Errorf("shortcut name is required")
	}
	if trimmedCommand == "" {
		return nil, fmt.Errorf("shortcut command is required")
	}
	if len([]rune(trimmedName)) > maxProbeShellShortcutNameLen {
		return nil, fmt.Errorf("shortcut name must be <= %d characters", maxProbeShellShortcutNameLen)
	}
	if len(trimmedCommand) > maxProbeShellShortcutCmdLen {
		return nil, fmt.Errorf("shortcut command must be <= %d bytes", maxProbeShellShortcutCmdLen)
	}

	items := loadProbeShellShortcutsLocked()
	now := time.Now().UTC().Format(time.RFC3339)
	key := strings.ToLower(trimmedName)
	found := false
	for i := range items {
		if strings.ToLower(strings.TrimSpace(items[i].Name)) != key {
			continue
		}
		items[i].Name = trimmedName
		items[i].Command = trimmedCommand
		items[i].UpdatedAt = now
		found = true
		break
	}
	if !found {
		if len(items) >= maxProbeShellShortcutCount {
			return nil, fmt.Errorf("shortcut count exceeded limit (%d)", maxProbeShellShortcutCount)
		}
		items = append(items, probeShellShortcutRecord{
			Name:      trimmedName,
			Command:   trimmedCommand,
			UpdatedAt: now,
		})
	}

	normalized := normalizeProbeShellShortcuts(items)
	ProbeStore.data.ProbeShellShortcuts = normalized
	return normalized, nil
}

func removeProbeShellShortcutLocked(name string) ([]probeShellShortcutRecord, error) {
	trimmedName := strings.TrimSpace(name)
	if trimmedName == "" {
		return nil, fmt.Errorf("shortcut name is required")
	}
	key := strings.ToLower(trimmedName)

	items := loadProbeShellShortcutsLocked()
	next := make([]probeShellShortcutRecord, 0, len(items))
	for _, item := range items {
		if strings.ToLower(strings.TrimSpace(item.Name)) == key {
			continue
		}
		next = append(next, item)
	}
	normalized := normalizeProbeShellShortcuts(next)
	ProbeStore.data.ProbeShellShortcuts = normalized
	return normalized, nil
}

func normalizeProbeShellShortcuts(items []probeShellShortcutRecord) []probeShellShortcutRecord {
	if len(items) == 0 {
		return []probeShellShortcutRecord{}
	}
	out := make([]probeShellShortcutRecord, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		name := strings.TrimSpace(item.Name)
		command := strings.TrimSpace(item.Command)
		if name == "" || command == "" {
			continue
		}
		if len([]rune(name)) > maxProbeShellShortcutNameLen {
			continue
		}
		if len(command) > maxProbeShellShortcutCmdLen {
			continue
		}
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, probeShellShortcutRecord{
			Name:      name,
			Command:   command,
			UpdatedAt: strings.TrimSpace(item.UpdatedAt),
		})
		if len(out) >= maxProbeShellShortcutCount {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}
