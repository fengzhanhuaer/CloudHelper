package core

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	adminManagerBackupMaxArchiveBytes = 64 * 1024 * 1024
	adminManagerBackupMaxExtractBytes = 256 * 1024 * 1024
)

type adminManagerBackupUploadRequest struct {
	ArchiveName   string `json:"archive_name"`
	ArchiveBase64 string `json:"archive_base64"`
}

func handleAdminWSManagerBackupUpload(payload json.RawMessage) (interface{}, error) {
	var req adminManagerBackupUploadRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload")
	}

	encoded := strings.TrimSpace(req.ArchiveBase64)
	if encoded == "" {
		return nil, errors.New("archive_base64 is required")
	}
	if base64.StdEncoding.DecodedLen(len(encoded)) > adminManagerBackupMaxArchiveBytes {
		return nil, fmt.Errorf("backup archive exceeds size limit (%d bytes)", adminManagerBackupMaxArchiveBytes)
	}

	archiveContent, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("invalid archive_base64")
	}
	if len(archiveContent) == 0 {
		return nil, errors.New("backup archive is empty")
	}

	username, _, _ := currentIdentityClaims()
	targetUserDir := filepath.Join(dataDir, sanitizeManagerBackupUsername(username))

	fileCount, err := restoreManagerBackupArchive(archiveContent, targetUserDir)
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"username":     sanitizeManagerBackupUsername(username),
		"target_dir":   targetUserDir,
		"file_count":   fileCount,
		"archive_name": strings.TrimSpace(req.ArchiveName),
	}, nil
}

func sanitizeManagerBackupUsername(username string) string {
	name := strings.TrimSpace(username)
	if name == "" {
		name = defaultUsername
	}

	buf := make([]byte, 0, len(name))
	for i := 0; i < len(name); i++ {
		ch := name[i]
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '-' || ch == '_' || ch == '.' {
			buf = append(buf, ch)
			continue
		}
		buf = append(buf, '_')
	}
	out := strings.Trim(string(buf), "._-")
	if out == "" {
		return defaultUsername
	}
	return out
}

func restoreManagerBackupArchive(archiveContent []byte, targetDir string) (int, error) {
	reader, err := zip.NewReader(bytes.NewReader(archiveContent), int64(len(archiveContent)))
	if err != nil {
		return 0, fmt.Errorf("invalid backup archive: %w", err)
	}

	if err := os.RemoveAll(targetDir); err != nil {
		return 0, err
	}
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return 0, err
	}

	targetRoot := filepath.Clean(targetDir)
	targetPrefix := targetRoot + string(filepath.Separator)
	var totalExtracted uint64
	fileCount := 0

	for _, f := range reader.File {
		name := strings.TrimSpace(f.Name)
		if name == "" {
			continue
		}
		cleanName := filepath.Clean(strings.ReplaceAll(name, "\\", "/"))
		if cleanName == "." || strings.HasPrefix(cleanName, "..") || filepath.IsAbs(cleanName) {
			return 0, fmt.Errorf("invalid archive entry path: %s", name)
		}

		destPath := filepath.Join(targetRoot, filepath.FromSlash(cleanName))
		destPath = filepath.Clean(destPath)
		if destPath != targetRoot && !strings.HasPrefix(destPath+string(filepath.Separator), targetPrefix) {
			return 0, fmt.Errorf("archive entry escapes target directory: %s", name)
		}

		info := f.FileInfo()
		if info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		if info.IsDir() {
			if err := os.MkdirAll(destPath, 0o755); err != nil {
				return 0, err
			}
			continue
		}

		totalExtracted += f.UncompressedSize64
		if totalExtracted > adminManagerBackupMaxExtractBytes {
			return 0, fmt.Errorf("backup archive exceeds extract size limit (%d bytes)", adminManagerBackupMaxExtractBytes)
		}

		if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
			return 0, err
		}

		rc, err := f.Open()
		if err != nil {
			return 0, err
		}
		perm := info.Mode().Perm()
		if perm == 0 {
			perm = 0o644
		}
		out, err := os.OpenFile(destPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
		if err != nil {
			rc.Close()
			return 0, err
		}

		limitedReader := io.LimitReader(rc, int64(f.UncompressedSize64)+1)
		written, copyErr := io.Copy(out, limitedReader)
		closeErr := out.Close()
		rcCloseErr := rc.Close()
		if copyErr != nil {
			return 0, copyErr
		}
		if closeErr != nil {
			return 0, closeErr
		}
		if rcCloseErr != nil {
			return 0, rcCloseErr
		}
		if written > int64(f.UncompressedSize64) {
			return 0, fmt.Errorf("archive entry size overflow: %s", name)
		}
		fileCount++
	}

	return fileCount, nil
}
