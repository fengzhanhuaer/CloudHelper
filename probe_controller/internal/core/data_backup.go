package core

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type backupArchive struct {
	path    string
	modTime time.Time
}

type controllerBackupRuntimeStatus struct {
	Running        bool   `json:"running"`
	LastSource     string `json:"last_source"`
	LastStatus     string `json:"last_status"`
	LastError      string `json:"last_error"`
	LastStartedAt  string `json:"last_started_at"`
	LastFinishedAt string `json:"last_finished_at"`
	LastArchive    string `json:"last_archive"`
}

const (
	backupDirName             = "backup"
	backupArchivePrefix       = "controller-data-"
	backupArchiveDateTimeFmt  = "20060102-150405.000000000"
	backupKeepPerTimeCategory = 3
)

var (
	backupAsyncMu      sync.Mutex
	backupAsyncRunning bool
	backupAsyncPending bool

	controllerBackupStatusMu sync.Mutex
	controllerBackupStatus   = controllerBackupRuntimeStatus{LastStatus: "idle"}
)

func triggerAutoBackupControllerDataAsync(source string) {
	backupAsyncMu.Lock()
	if backupAsyncRunning {
		backupAsyncPending = true
		backupAsyncMu.Unlock()
		return
	}
	backupAsyncRunning = true
	backupAsyncMu.Unlock()

	go runAutoBackupControllerDataAsync(source)
}

func runAutoBackupControllerDataAsync(source string) {
	currentSource := strings.TrimSpace(source)
	if currentSource == "" {
		currentSource = "unspecified"
	}

	for {
		markControllerBackupStarted(currentSource)
		if err := autoBackupControllerData(); err != nil {
			markControllerBackupFinished(err)
			log.Printf("warning: async controller backup failed (%s): %v", currentSource, err)
		} else {
			markControllerBackupFinished(nil)
		}

		backupAsyncMu.Lock()
		if !backupAsyncPending {
			backupAsyncRunning = false
			backupAsyncMu.Unlock()
			return
		}
		backupAsyncPending = false
		backupAsyncMu.Unlock()
		currentSource = "coalesced"
	}
}

func markControllerBackupStarted(source string) {
	now := time.Now().UTC().Format(time.RFC3339)
	controllerBackupStatusMu.Lock()
	controllerBackupStatus.Running = true
	controllerBackupStatus.LastSource = strings.TrimSpace(source)
	controllerBackupStatus.LastStatus = "running"
	controllerBackupStatus.LastError = ""
	controllerBackupStatus.LastStartedAt = now
	controllerBackupStatus.LastFinishedAt = ""
	controllerBackupStatusMu.Unlock()
}

func markControllerBackupFinished(err error) {
	now := time.Now().UTC().Format(time.RFC3339)
	controllerBackupStatusMu.Lock()
	controllerBackupStatus.Running = false
	controllerBackupStatus.LastFinishedAt = now
	if err != nil {
		controllerBackupStatus.LastStatus = "failed"
		controllerBackupStatus.LastError = strings.TrimSpace(err.Error())
	} else {
		controllerBackupStatus.LastStatus = "ok"
		controllerBackupStatus.LastError = ""
	}
	controllerBackupStatusMu.Unlock()
}

func setControllerBackupLastArchive(path string) {
	controllerBackupStatusMu.Lock()
	controllerBackupStatus.LastArchive = strings.TrimSpace(path)
	controllerBackupStatusMu.Unlock()
}

func getControllerBackupRuntimeStatus() controllerBackupRuntimeStatus {
	controllerBackupStatusMu.Lock()
	defer controllerBackupStatusMu.Unlock()
	return controllerBackupStatus
}

func autoBackupControllerData() error {
	dataPath, err := filepath.Abs(dataDir)
	if err != nil {
		return err
	}
	settings := getBackupSettings()
	sourceDirs, err := resolveBackupSourceDirs(settings)
	if err != nil {
		return err
	}
	if len(sourceDirs) == 0 {
		return nil
	}
	backupDir, err := resolveBackupLocalDir(settings, dataPath)
	if err != nil {
		return err
	}
	for _, sourceDir := range sourceDirs {
		if err := validateBackupSourceDir(sourceDir, backupDir); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return err
	}

	now := time.Now()
	versionTag := backupSafeVersionTag(currentControllerVersion())
	archivePath := filepath.Join(backupDir, backupArchivePrefix+versionTag+"-"+now.Format(backupArchiveDateTimeFmt)+".zip")
	if err := zipBackupSources(sourceDirs, archivePath); err != nil {
		_ = os.Remove(archivePath)
		return err
	}
	setControllerBackupLastArchive(archivePath)

	if err := pruneBackupArchives(backupDir, backupArchivePrefix, now); err != nil {
		return err
	}

	if err := uploadControllerBackupArchiveToGoogleDrive(archivePath, settings); err != nil {
		return err
	}

	return nil
}

func uploadControllerBackupArchiveToGoogleDrive(archivePath string, settings backupSettings) error {
	if !settings.Enabled {
		return nil
	}
	if strings.TrimSpace(archivePath) == "" {
		return fmt.Errorf("backup archive path is empty")
	}
	info, err := os.Stat(archivePath)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("backup archive path is a directory: %s", archivePath)
	}
	return uploadGoogleDriveBackupArchive(archivePath, settings)
}

func validateBackupSourceDir(sourceDir string, backupDir string) error {
	sourceDir = strings.TrimSpace(sourceDir)
	if sourceDir == "" {
		return fmt.Errorf("backup source directory is empty")
	}
	sourceAbs, err := filepath.Abs(sourceDir)
	if err != nil {
		return err
	}
	info, err := os.Stat(sourceAbs)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("backup source path is not directory: %s", sourceAbs)
	}
	backupAbs, err := filepath.Abs(backupDir)
	if err != nil {
		return err
	}
	if pathInsideOrEqual(backupAbs, sourceAbs) || pathInsideOrEqual(sourceAbs, backupAbs) {
		return fmt.Errorf("backup source directory must not overlap backup local directory: %s", sourceAbs)
	}
	return nil
}

func backupSourceRemoteLabel(sourceDir string, idx int) string {
	base := backupSafeVersionTag(filepath.Base(strings.TrimSpace(sourceDir)))
	if base == "" || base == "." || base == string(filepath.Separator) {
		base = "source"
	}
	return fmt.Sprintf("%03d-%s", idx+1, base)
}

func pruneBackupArchives(backupDir, prefix string, now time.Time) error {
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		return err
	}

	archives := make([]backupArchive, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(strings.ToLower(name), ".zip") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		archives = append(archives, backupArchive{
			path:    filepath.Join(backupDir, name),
			modTime: info.ModTime(),
		})
	}

	if len(archives) == 0 {
		return nil
	}

	sort.Slice(archives, func(i, j int) bool {
		return archives[i].modTime.After(archives[j].modTime)
	})

	keep := make(map[string]struct{})
	buckets := map[string][]backupArchive{
		"today":      {},
		"yesterday":  {},
		"last_week":  {},
		"last_month": {},
		"last_year":  {},
	}
	now = now.Local()
	for _, archive := range archives {
		if bucket := backupTimeBucket(archive.modTime, now); bucket != "" {
			buckets[bucket] = append(buckets[bucket], archive)
		}
	}

	for _, key := range []string{"today", "yesterday", "last_week", "last_month", "last_year"} {
		bucketArchives := buckets[key]
		if len(bucketArchives) == 0 {
			continue
		}
		if len(bucketArchives) > backupKeepPerTimeCategory {
			bucketArchives = bucketArchives[:backupKeepPerTimeCategory]
		}
		for _, archive := range bucketArchives {
			keep[archive.path] = struct{}{}
		}
	}

	var firstErr error
	for _, archive := range archives {
		if _, ok := keep[archive.path]; ok {
			continue
		}
		if err := os.Remove(archive.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

func backupTimeBucket(target, now time.Time) string {
	t := target.Local()
	n := now.Local()

	if sameDay(t, n) {
		return "today"
	}

	yesterday := n.AddDate(0, 0, -1)
	if sameDay(t, yesterday) {
		return "yesterday"
	}

	lastWeekRef := n.AddDate(0, 0, -7)
	lastWeekYear, lastWeekNo := lastWeekRef.ISOWeek()
	targetWeekYear, targetWeekNo := t.ISOWeek()
	if targetWeekYear == lastWeekYear && targetWeekNo == lastWeekNo {
		return "last_week"
	}

	lastMonthRef := n.AddDate(0, -1, 0)
	if t.Year() == lastMonthRef.Year() && t.Month() == lastMonthRef.Month() {
		return "last_month"
	}

	if t.Year() == n.Year()-1 {
		return "last_year"
	}

	return ""
}

func backupSafeVersionTag(version string) string {
	v := strings.TrimSpace(version)
	if v == "" {
		return "dev"
	}

	buf := make([]byte, 0, len(v))
	for i := 0; i < len(v); i++ {
		ch := v[i]
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '-' || ch == '_' || ch == '.' {
			buf = append(buf, ch)
			continue
		}
		buf = append(buf, '_')
	}
	out := strings.Trim(strings.TrimSpace(string(buf)), "._-")
	if out == "" {
		return "dev"
	}
	return out
}

func zipBackupSources(sourceDirs []string, targetZip string) error {
	if len(sourceDirs) == 0 {
		return fmt.Errorf("backup source directories are required")
	}
	if len(sourceDirs) == 1 {
		return zipDirectory(sourceDirs[0], targetZip)
	}

	out, err := os.Create(targetZip)
	if err != nil {
		return err
	}

	zw := zip.NewWriter(out)
	seenNames := map[string]int{}
	var walkErr error
	for idx, sourceDir := range sourceDirs {
		label := backupSourceRemoteLabel(sourceDir, idx)
		if count := seenNames[label]; count > 0 {
			label = fmt.Sprintf("%s-%d", label, count+1)
		}
		seenNames[label]++
		if err := addDirectoryToZip(zw, sourceDir, label); err != nil {
			walkErr = err
			break
		}
	}

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

func zipDirectory(sourceDir, targetZip string) error {
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

		info, err := d.Info()
		if err != nil {
			return err
		}
		nameLower := strings.ToLower(info.Name())
		if info.IsDir() && nameLower == ".cache" {
			return filepath.SkipDir
		}
		if !info.IsDir() {
			ext := strings.ToLower(filepath.Ext(info.Name()))
			if ext == ".log" || ext == ".tmp" || ext == ".cache" {
				return nil
			}
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

		if _, err := io.Copy(writer, in); err != nil {
			return err
		}
		return nil
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

func addDirectoryToZip(zw *zip.Writer, sourceDir string, rootLabel string) error {
	return filepath.WalkDir(sourceDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		rel, err := filepath.Rel(sourceDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			header := &zip.FileHeader{Name: filepath.ToSlash(strings.Trim(rootLabel, "/")) + "/", Method: zip.Deflate}
			_, err := zw.CreateHeader(header)
			return err
		}

		info, err := d.Info()
		if err != nil {
			return err
		}
		nameLower := strings.ToLower(info.Name())
		if info.IsDir() && nameLower == ".cache" {
			return filepath.SkipDir
		}
		if !info.IsDir() {
			ext := strings.ToLower(filepath.Ext(info.Name()))
			if ext == ".log" || ext == ".tmp" || ext == ".cache" {
				return nil
			}
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}

		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(filepath.Join(rootLabel, rel))

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

		if _, err := io.Copy(writer, in); err != nil {
			return err
		}
		return nil
	})
}

func sameDay(a, b time.Time) bool {
	a = a.Local()
	b = b.Local()
	return a.Year() == b.Year() && a.Month() == b.Month() && a.Day() == b.Day()
}
