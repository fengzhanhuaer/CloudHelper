package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	mngNotepadDirName      = "notepad"
	mngNotepadFileName     = "notepad.json"
	mngNotepadMaxRunes     = 200000
	mngNotepadMaxNameRunes = 80
)

var mngNotepadMu sync.Mutex

type mngNotepadRequest struct {
	Action   string `json:"action"`
	FolderID string `json:"folder_id"`
	FileID   string `json:"file_id"`
	Name     string `json:"name"`
	Content  string `json:"content"`
}

type mngNotepadWorkspace struct {
	Folders        []mngNotepadFolder `json:"folders"`
	Files          []mngNotepadFile   `json:"files"`
	SelectedFileID string             `json:"selected_file_id"`
}

type mngNotepadFolder struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
}

type mngNotepadFile struct {
	ID        string `json:"id"`
	FolderID  string `json:"folder_id"`
	Name      string `json:"name"`
	Content   string `json:"content"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	Length    int    `json:"length"`
}

func mngNotepadPageHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/mng/notepad" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(mngNotepadPageHTML))
}

func mngNotepadHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		view, err := getMngNotepad()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, view)
	case http.MethodPost:
		var req mngNotepadRequest
		if err := decodeMngJSONBody(r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
			return
		}
		view, err := updateMngNotepad(req)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, view)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func getMngNotepad() (mngNotepadWorkspace, error) {
	mngNotepadMu.Lock()
	defer mngNotepadMu.Unlock()
	return loadMngNotepadWorkspaceLocked()
}

func updateMngNotepad(req mngNotepadRequest) (mngNotepadWorkspace, error) {
	mngNotepadMu.Lock()
	defer mngNotepadMu.Unlock()

	workspace, err := loadMngNotepadWorkspaceLocked()
	if err != nil {
		return mngNotepadWorkspace{}, err
	}
	updated, err := applyMngNotepadRequest(workspace, req)
	if err != nil {
		return mngNotepadWorkspace{}, err
	}
	if err := saveMngNotepadWorkspaceLocked(updated); err != nil {
		return mngNotepadWorkspace{}, err
	}
	return updated, nil
}

func applyMngNotepadRequest(workspace mngNotepadWorkspace, req mngNotepadRequest) (mngNotepadWorkspace, error) {
	switch strings.TrimSpace(req.Action) {
	case "create_folder":
		name, err := normalizeMngNotepadName(req.Name, "新文件夹")
		if err != nil {
			return mngNotepadWorkspace{}, err
		}
		now := time.Now().UTC().Format(time.RFC3339)
		folder := mngNotepadFolder{
			ID:        newMngNotepadID("folder"),
			Name:      uniqueMngNotepadFolderName(workspace, name),
			CreatedAt: now,
		}
		workspace.Folders = append(workspace.Folders, folder)
		return normalizeMngNotepadWorkspace(workspace), nil
	case "create_file":
		name, err := normalizeMngNotepadName(req.Name, "未命名")
		if err != nil {
			return mngNotepadWorkspace{}, err
		}
		folderID := req.FolderID
		if folderID == "" {
			folderID = workspace.Folders[0].ID
		}
		if !mngNotepadFolderExists(workspace, folderID) {
			return mngNotepadWorkspace{}, errors.New("folder not found")
		}
		now := time.Now().UTC().Format(time.RFC3339)
		file := mngNotepadFile{
			ID:        newMngNotepadID("file"),
			FolderID:  folderID,
			Name:      uniqueMngNotepadFileName(workspace, folderID, name),
			CreatedAt: now,
			UpdatedAt: now,
		}
		workspace.Files = append(workspace.Files, file)
		workspace.SelectedFileID = file.ID
		return normalizeMngNotepadWorkspace(workspace), nil
	case "save_file":
		if len([]rune(req.Content)) > mngNotepadMaxRunes {
			return mngNotepadWorkspace{}, errors.New("content is too long")
		}
		fileIndex := mngNotepadFileIndex(workspace, req.FileID)
		if fileIndex < 0 {
			return mngNotepadWorkspace{}, errors.New("file not found")
		}
		workspace.Files[fileIndex].Content = normalizeMngNotepadContent(req.Content)
		workspace.Files[fileIndex].UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		workspace.Files[fileIndex].Length = len([]rune(workspace.Files[fileIndex].Content))
		workspace.SelectedFileID = workspace.Files[fileIndex].ID
		return normalizeMngNotepadWorkspace(workspace), nil
	case "select_file":
		if mngNotepadFileIndex(workspace, req.FileID) < 0 {
			return mngNotepadWorkspace{}, errors.New("file not found")
		}
		workspace.SelectedFileID = req.FileID
		return normalizeMngNotepadWorkspace(workspace), nil
	default:
		return mngNotepadWorkspace{}, errors.New("unknown action")
	}
}

func loadMngNotepadWorkspaceLocked() (mngNotepadWorkspace, error) {
	data, err := os.ReadFile(mngNotepadStoragePath())
	if err == nil {
		var workspace mngNotepadWorkspace
		if len(strings.TrimSpace(string(data))) > 0 {
			if err := json.Unmarshal(data, &workspace); err != nil {
				return mngNotepadWorkspace{}, err
			}
		}
		return normalizeMngNotepadWorkspace(workspace), nil
	}
	if !os.IsNotExist(err) {
		return mngNotepadWorkspace{}, err
	}
	return newMngNotepadWorkspace(""), nil
}

func saveMngNotepadWorkspaceLocked(workspace mngNotepadWorkspace) error {
	workspace = normalizeMngNotepadWorkspace(workspace)
	path := mngNotepadStoragePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(workspace, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}
	triggerAutoBackupControllerDataAsync("notepad_save")
	return nil
}

func mngNotepadStoragePath() string {
	return filepath.Join(dataDir, mngNotepadDirName, mngNotepadFileName)
}

func newMngNotepadWorkspace(content string) mngNotepadWorkspace {
	now := time.Now().UTC().Format(time.RFC3339)
	normalized := normalizeMngNotepadContent(content)
	return mngNotepadWorkspace{
		Folders: []mngNotepadFolder{{
			ID:        "root",
			Name:      "默认",
			CreatedAt: now,
		}},
		Files: []mngNotepadFile{{
			ID:        "default",
			FolderID:  "root",
			Name:      "默认笔记",
			Content:   normalized,
			CreatedAt: now,
			UpdatedAt: now,
			Length:    len([]rune(normalized)),
		}},
		SelectedFileID: "default",
	}
}

func normalizeMngNotepadWorkspace(workspace mngNotepadWorkspace) mngNotepadWorkspace {
	if len(workspace.Folders) == 0 {
		workspace.Folders = []mngNotepadFolder{{ID: "root", Name: "默认", CreatedAt: time.Now().UTC().Format(time.RFC3339)}}
	}
	folderByID := make(map[string]bool, len(workspace.Folders))
	for i := range workspace.Folders {
		if strings.TrimSpace(workspace.Folders[i].ID) == "" {
			workspace.Folders[i].ID = newMngNotepadID("folder")
		}
		if strings.TrimSpace(workspace.Folders[i].Name) == "" {
			workspace.Folders[i].Name = "未命名文件夹"
		}
		folderByID[workspace.Folders[i].ID] = true
	}
	defaultFolderID := workspace.Folders[0].ID
	for i := range workspace.Files {
		if strings.TrimSpace(workspace.Files[i].ID) == "" {
			workspace.Files[i].ID = newMngNotepadID("file")
		}
		if !folderByID[workspace.Files[i].FolderID] {
			workspace.Files[i].FolderID = defaultFolderID
		}
		if strings.TrimSpace(workspace.Files[i].Name) == "" {
			workspace.Files[i].Name = "未命名"
		}
		workspace.Files[i].Content = normalizeMngNotepadContent(workspace.Files[i].Content)
		workspace.Files[i].Length = len([]rune(workspace.Files[i].Content))
	}
	if len(workspace.Files) == 0 {
		now := time.Now().UTC().Format(time.RFC3339)
		workspace.Files = []mngNotepadFile{{
			ID:        newMngNotepadID("file"),
			FolderID:  defaultFolderID,
			Name:      "未命名",
			CreatedAt: now,
			UpdatedAt: now,
		}}
	}
	if mngNotepadFileIndex(workspace, workspace.SelectedFileID) < 0 {
		workspace.SelectedFileID = workspace.Files[0].ID
	}
	return workspace
}

func normalizeMngNotepadName(name, fallback string) (string, error) {
	value := strings.TrimSpace(name)
	if value == "" {
		value = fallback
	}
	value = strings.NewReplacer("/", "-", "\\", "-", "\n", " ", "\r", " ", "\t", " ").Replace(value)
	if len([]rune(value)) > mngNotepadMaxNameRunes {
		return "", errors.New("name is too long")
	}
	return value, nil
}

func normalizeMngNotepadContent(content string) string {
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	return strings.ReplaceAll(normalized, "\r", "\n")
}

func mngNotepadFolderExists(workspace mngNotepadWorkspace, folderID string) bool {
	for _, folder := range workspace.Folders {
		if folder.ID == folderID {
			return true
		}
	}
	return false
}

func mngNotepadFileIndex(workspace mngNotepadWorkspace, fileID string) int {
	for i, file := range workspace.Files {
		if file.ID == fileID {
			return i
		}
	}
	return -1
}

func uniqueMngNotepadFolderName(workspace mngNotepadWorkspace, name string) string {
	exists := make(map[string]bool, len(workspace.Folders))
	for _, folder := range workspace.Folders {
		exists[folder.Name] = true
	}
	return uniqueMngNotepadName(name, exists)
}

func uniqueMngNotepadFileName(workspace mngNotepadWorkspace, folderID, name string) string {
	exists := make(map[string]bool)
	for _, file := range workspace.Files {
		if file.FolderID == folderID {
			exists[file.Name] = true
		}
	}
	return uniqueMngNotepadName(name, exists)
}

func uniqueMngNotepadName(name string, exists map[string]bool) string {
	if !exists[name] {
		return name
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s %d", name, i)
		if !exists[candidate] {
			return candidate
		}
	}
}

func newMngNotepadID(prefix string) string {
	return fmt.Sprintf("%s_%d", prefix, time.Now().UTC().UnixNano())
}
