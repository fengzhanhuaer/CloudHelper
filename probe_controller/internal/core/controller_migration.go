package core

import (
	"archive/zip"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type controllerMigrationPackage struct {
	Token     string
	Path      string
	FileName  string
	CreatedAt time.Time
	ExpiresAt time.Time
}

const (
	controllerMigrationArchivePrefix = "controller-migration-data-"
	controllerMigrationTokenTTL      = 30 * time.Minute
)

var controllerMigrationPackages = struct {
	mu    sync.Mutex
	items map[string]controllerMigrationPackage
}{
	items: make(map[string]controllerMigrationPackage),
}

func createControllerUserDataBackupArchive() (string, string, error) {
	dataPath, err := filepath.Abs(dataDir)
	if err != nil {
		return "", "", err
	}
	info, err := os.Stat(dataPath)
	if err != nil {
		return "", "", err
	}
	if !info.IsDir() {
		return "", "", fmt.Errorf("controller data path is not directory: %s", dataPath)
	}
	tmpDir, err := os.MkdirTemp("", "cloudhelper-controller-backup-*")
	if err != nil {
		return "", "", err
	}
	fileName := controllerMigrationArchivePrefix + backupSafeVersionTag(currentControllerVersion()) + "-" + time.Now().Format(backupArchiveDateTimeFmt) + ".zip"
	archivePath := filepath.Join(tmpDir, fileName)
	if err := zipControllerUserDataDir(dataPath, archivePath); err != nil {
		_ = os.RemoveAll(tmpDir)
		return "", "", err
	}
	return archivePath, fileName, nil
}

func createControllerUserDataMigrationScript() (string, string, error) {
	archivePath, archiveName, err := createControllerUserDataBackupArchive()
	if err != nil {
		return "", "", err
	}
	tmpDir := filepath.Dir(archivePath)
	scriptName := strings.TrimSuffix(archiveName, filepath.Ext(archiveName)) + ".sh"
	scriptPath := filepath.Join(tmpDir, scriptName)
	if err := writeControllerMigrationScriptWithArchive(scriptPath, archivePath); err != nil {
		_ = os.RemoveAll(tmpDir)
		return "", "", err
	}
	_ = os.Remove(archivePath)
	return scriptPath, scriptName, nil
}

func writeControllerMigrationScriptWithArchive(scriptPath string, archivePath string) error {
	out, err := os.Create(scriptPath)
	if err != nil {
		return err
	}
	_, writeErr := io.WriteString(out, controllerMigrationScriptHeader())
	if writeErr == nil {
		in, err := os.Open(archivePath)
		if err != nil {
			writeErr = err
		} else {
			encoder := base64.NewEncoder(base64.StdEncoding, out)
			if _, err := io.Copy(encoder, in); err != nil {
				writeErr = err
			}
			if err := encoder.Close(); writeErr == nil && err != nil {
				writeErr = err
			}
			if err := in.Close(); writeErr == nil && err != nil {
				writeErr = err
			}
			if writeErr == nil {
				_, writeErr = io.WriteString(out, "\n")
			}
		}
	}
	closeErr := out.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

func zipControllerUserDataDir(sourceDir string, targetZip string) error {
	out, err := os.Create(targetZip)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(out)
	walkErr := filepath.WalkDir(sourceDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(sourceDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		relSlash := filepath.ToSlash(rel)
		if shouldSkipControllerUserDataBackupEntry(relSlash, d) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		return addFileSystemEntryToZip(zw, sourceDir, path, rel)
	})
	closeZipErr := zw.Close()
	closeFileErr := out.Close()
	if walkErr != nil {
		return walkErr
	}
	if closeZipErr != nil {
		return closeZipErr
	}
	if closeFileErr != nil {
		return closeFileErr
	}
	return nil
}

func shouldSkipControllerUserDataBackupEntry(relSlash string, d os.DirEntry) bool {
	clean := strings.Trim(strings.ToLower(filepath.ToSlash(relSlash)), "/")
	if clean == "" {
		return false
	}
	first := clean
	if idx := strings.Index(first, "/"); idx >= 0 {
		first = first[:idx]
	}
	if first == backupDirName || first == ".cache" || first == "cache" || first == "tmp" || first == "temp" || first == "task_history" {
		return true
	}
	if first == tgAssistantTaskHistoryDir || first == tgAssistantHistoryFile {
		return true
	}
	name := strings.ToLower(strings.TrimSpace(d.Name()))
	if d.IsDir() {
		return name == ".cache" || name == "cache" || name == "tmp" || name == "temp" || name == "task_history"
	}
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".log" || ext == ".tmp" || ext == ".cache" || strings.HasSuffix(name, ".bak")
}

func addFileSystemEntryToZip(zw *zip.Writer, sourceDir string, path string, rel string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	header.Name = filepath.ToSlash(rel)
	if info.IsDir() {
		header.Name += "/"
		_, err := zw.CreateHeader(header)
		return err
	}
	header.Method = zip.Deflate
	writer, err := zw.CreateHeader(header)
	if err != nil {
		return err
	}
	in, err := os.Open(path)
	if err != nil {
		return err
	}
	defer in.Close()
	_, err = io.Copy(writer, in)
	return err
}

func createControllerMigrationPackage() (controllerMigrationPackage, error) {
	scriptPath, fileName, err := createControllerUserDataMigrationScript()
	if err != nil {
		return controllerMigrationPackage{}, err
	}
	token, err := randomToken(32)
	if err != nil {
		_ = os.RemoveAll(filepath.Dir(scriptPath))
		return controllerMigrationPackage{}, err
	}
	now := time.Now()
	pkg := controllerMigrationPackage{
		Token:     token,
		Path:      scriptPath,
		FileName:  fileName,
		CreatedAt: now,
		ExpiresAt: now.Add(controllerMigrationTokenTTL),
	}
	controllerMigrationPackages.mu.Lock()
	pruneControllerMigrationPackagesLocked(now)
	controllerMigrationPackages.items[token] = pkg
	controllerMigrationPackages.mu.Unlock()
	return pkg, nil
}

func resolveControllerMigrationPackage(token string) (controllerMigrationPackage, bool) {
	token = strings.TrimSpace(token)
	if token == "" {
		return controllerMigrationPackage{}, false
	}
	now := time.Now()
	controllerMigrationPackages.mu.Lock()
	defer controllerMigrationPackages.mu.Unlock()
	pruneControllerMigrationPackagesLocked(now)
	pkg, ok := controllerMigrationPackages.items[token]
	if !ok || now.After(pkg.ExpiresAt) {
		if ok {
			delete(controllerMigrationPackages.items, token)
			_ = os.RemoveAll(filepath.Dir(pkg.Path))
		}
		return controllerMigrationPackage{}, false
	}
	return pkg, true
}

func pruneControllerMigrationPackagesLocked(now time.Time) {
	for token, pkg := range controllerMigrationPackages.items {
		if now.After(pkg.ExpiresAt) {
			delete(controllerMigrationPackages.items, token)
			_ = os.RemoveAll(filepath.Dir(pkg.Path))
		}
	}
}

func controllerMigrationScriptHeader() string {
	return fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail

INSTALL_DIR="${INSTALL_DIR:-/opt/cloudhelper/probe_controller}"
SERVICE_NAME="${SERVICE_NAME:-probe_controller}"
SERVICE_USER="${SERVICE_USER:-cloudhelper}"
SERVICE_GROUP="${SERVICE_GROUP:-cloudhelper}"
DATA_DIR="${INSTALL_DIR}/data"

log() { echo "[cloudhelper-migrate] $*"; }
die() { echo "[cloudhelper-migrate][ERROR] $*" >&2; exit 1; }

if [[ "${EUID}" -ne 0 ]]; then
  die "please run as root (use sudo)"
fi
command -v curl >/dev/null 2>&1 || die "curl is required"
command -v unzip >/dev/null 2>&1 || die "unzip is required"
command -v base64 >/dev/null 2>&1 || die "base64 is required"

tmp_dir="$(mktemp -d)"
trap 'rm -rf -- "${tmp_dir}"' EXIT
install_script="${tmp_dir}/install_probe_controller_service.sh"
archive="${tmp_dir}/controller-data.zip"

log "extracting embedded controller installer"
cat >"${install_script}" <<'__CLOUDHELPER_CONTROLLER_INSTALLER__'
%s
__CLOUDHELPER_CONTROLLER_INSTALLER__
chmod +x "${install_script}"

log "installing controller service"
env INSTALL_DIR="${INSTALL_DIR}" SERVICE_NAME="${SERVICE_NAME}" SERVICE_USER="${SERVICE_USER}" SERVICE_GROUP="${SERVICE_GROUP}" bash "${install_script}"

log "extracting embedded migration data"
sed -n '/^__CLOUDHELPER_CONTROLLER_DATA_ARCHIVE_BELOW__$/,$p' "$0" | tail -n +2 | base64 -d > "${archive}"

log "stopping ${SERVICE_NAME}"
systemctl stop "${SERVICE_NAME}" || true

backup_dir="${DATA_DIR}.before-migration.$(date +%%Y%%m%%d%%H%%M%%S)"
if [[ -d "${DATA_DIR}" ]]; then
  log "preserving existing data at ${backup_dir}"
  mv "${DATA_DIR}" "${backup_dir}"
fi
mkdir -p "${DATA_DIR}"
unzip -q "${archive}" -d "${DATA_DIR}"
chown -R "${SERVICE_USER}:${SERVICE_GROUP}" "${DATA_DIR}"
chmod 0750 "${DATA_DIR}"

log "starting ${SERVICE_NAME}"
systemctl start "${SERVICE_NAME}"
log "migration completed"
log "status: systemctl status ${SERVICE_NAME} --no-pager"
exit 0

__CLOUDHELPER_CONTROLLER_DATA_ARCHIVE_BELOW__
`, probeControllerInstallScriptLinux)
}

func serveControllerMigrationScript(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	pkg, ok := resolveControllerMigrationPackage(token)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid or expired migration token"})
		return
	}
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, sanitizeFilename(pkg.FileName)))
	http.ServeFile(w, r, pkg.Path)
}
