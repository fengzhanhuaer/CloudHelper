package core

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestMngNotepadWorkspaceSaveAndLoad(t *testing.T) {
	chdirTemp(t)

	workspace, err := updateMngNotepad(mngNotepadRequest{
		Action: "create_folder",
		Name:   "资料",
	})
	if err != nil {
		t.Fatalf("create folder: %v", err)
	}
	folderID := workspace.Folders[len(workspace.Folders)-1].ID

	workspace, err = updateMngNotepad(mngNotepadRequest{
		Action:   "create_file",
		FolderID: folderID,
		Name:     "链接",
	})
	if err != nil {
		t.Fatalf("create file: %v", err)
	}
	fileID := workspace.SelectedFileID

	workspace, err = updateMngNotepad(mngNotepadRequest{
		Action:  "save_file",
		FileID:  fileID,
		Content: "hello\r\nworld\ragain",
	})
	if err != nil {
		t.Fatalf("save file: %v", err)
	}
	file := workspace.Files[mngNotepadFileIndex(workspace, fileID)]
	if file.Content != "hello\nworld\nagain" {
		t.Fatalf("expected normalized content, got %q", file.Content)
	}
	if file.Length != len([]rune(file.Content)) {
		t.Fatalf("expected length %d, got %d", len([]rune(file.Content)), file.Length)
	}
	if file.UpdatedAt == "" {
		t.Fatal("expected updated_at")
	}

	loaded, err := getMngNotepad()
	if err != nil {
		t.Fatalf("get notepad: %v", err)
	}
	loadedFile := loaded.Files[mngNotepadFileIndex(loaded, fileID)]
	if loadedFile.Content != file.Content {
		t.Fatalf("expected loaded content %q, got %q", file.Content, loadedFile.Content)
	}
	if loaded.SelectedFileID != fileID {
		t.Fatalf("expected selected file %q, got %q", fileID, loaded.SelectedFileID)
	}
}

func TestMngNotepadStoresStandaloneJSONFile(t *testing.T) {
	chdirTemp(t)

	workspace, err := getMngNotepad()
	if err != nil {
		t.Fatalf("get notepad: %v", err)
	}
	if _, err := updateMngNotepad(mngNotepadRequest{
		Action:  "save_file",
		FileID:  workspace.SelectedFileID,
		Content: "standalone json",
	}); err != nil {
		t.Fatalf("save file: %v", err)
	}

	data, err := os.ReadFile(mngNotepadStoragePath())
	if err != nil {
		t.Fatalf("read notepad json: %v", err)
	}
	var stored mngNotepadWorkspace
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatalf("unmarshal notepad json: %v", err)
	}
	if len(stored.Files) != 1 || stored.Files[0].Content != "standalone json" {
		t.Fatalf("unexpected stored workspace: %+v", stored)
	}
}

func TestMngNotepadRejectsTooLongContent(t *testing.T) {
	chdirTemp(t)

	workspace, err := getMngNotepad()
	if err != nil {
		t.Fatalf("get notepad: %v", err)
	}
	if _, err := updateMngNotepad(mngNotepadRequest{
		Action:  "save_file",
		FileID:  workspace.SelectedFileID,
		Content: strings.Repeat("a", mngNotepadMaxRunes+1),
	}); err == nil {
		t.Fatal("expected too long content error")
	}
}
