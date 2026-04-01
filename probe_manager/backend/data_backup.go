package backend

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"time"
)

type managerBackupArchive struct {
	path    string
	modTime time.Time
}

const (
	managerBackupDirName             = "backup"
	managerBackupArchivePrefix       = "manager-data-"
	managerBackupArchiveDateTimeFmt  = "20060102-150405.000000000"
	managerBackupKeepPerTimeCategory = 3
)

func autoBackupManagerData() error {
	dataDirs, err := managerDataDirectories()
	if err != nil {
		return err
	}
	if len(dataDirs) == 0 {
		return nil
	}

	errMessages := make([]string, 0)
	now := time.Now()
	for _, dataPath := range dataDirs {
		_, err := backupOneManagerDataDir(dataPath, now)
		if err != nil {
			errMessages = append(errMessages, err.Error())
			continue
		}
	}

	if len(errMessages) > 0 {
		return errors.New(strings.Join(errMessages, "; "))
	}
	return nil
}

func managerDataDirectories() ([]string, error) {
	candidates := []string{filepath.Join(".", "data")}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(dir, "data"),
			filepath.Join(dir, "..", "data"),
		)
	}

	seen := map[string]struct{}{}
	resolved := make([]string, 0, len(candidates))
	for _, p := range candidates {
		absPath, err := filepath.Abs(p)
		if err != nil {
			continue
		}
		if _, ok := seen[absPath]; ok {
			continue
		}
		info, err := os.Stat(absPath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		if !info.IsDir() {
			continue
		}
		seen[absPath] = struct{}{}
		resolved = append(resolved, absPath)
	}
	return resolved, nil
}

func backupOneManagerDataDir(dataPath string, now time.Time) (string, error) {
	backupDir := filepath.Join(filepath.Dir(dataPath), managerBackupDirName)
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return "", err
	}

	versionTag := managerBackupSafeVersionTag(currentManagerVersion())
	archivePath := filepath.Join(backupDir, managerBackupArchivePrefix+versionTag+"-"+now.Format(managerBackupArchiveDateTimeFmt)+".zip")
	if err := zipManagerDirectory(dataPath, archivePath); err != nil {
		_ = os.Remove(archivePath)
		return "", fmt.Errorf("backup %s failed: %w", dataPath, err)
	}

	if err := pruneManagerBackupArchives(backupDir, managerBackupArchivePrefix, now); err != nil {
		return "", fmt.Errorf("prune %s failed: %w", backupDir, err)
	}
	return archivePath, nil
}

func pruneManagerBackupArchives(backupDir, prefix string, now time.Time) error {
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		return err
	}

	archives := make([]managerBackupArchive, 0, len(entries))
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
		archives = append(archives, managerBackupArchive{
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
	buckets := map[string][]managerBackupArchive{
		"today":      {},
		"yesterday":  {},
		"last_week":  {},
		"last_month": {},
		"last_year":  {},
	}
	now = now.Local()
	for _, archive := range archives {
		if bucket := managerBackupTimeBucket(archive.modTime, now); bucket != "" {
			buckets[bucket] = append(buckets[bucket], archive)
		}
	}

	for _, key := range []string{"today", "yesterday", "last_week", "last_month", "last_year"} {
		bucketArchives := buckets[key]
		if len(bucketArchives) == 0 {
			continue
		}
		if len(bucketArchives) > managerBackupKeepPerTimeCategory {
			bucketArchives = bucketArchives[:managerBackupKeepPerTimeCategory]
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

func managerBackupTimeBucket(target, now time.Time) string {
	t := target.Local()
	n := now.Local()

	if isSameCalendarDay(t, n) {
		return "today"
	}

	yesterday := n.AddDate(0, 0, -1)
	if isSameCalendarDay(t, yesterday) {
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

func currentManagerVersion() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		if v := strings.TrimSpace(bi.Main.Version); v != "" && v != "(devel)" {
			return v
		}
	}

	if v := strings.TrimSpace(BuildVersion); v != "" && v != "(devel)" && !strings.EqualFold(v, "dev") {
		return v
	}
	return "dev"
}

func managerBackupSafeVersionTag(version string) string {
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

func zipManagerDirectory(sourceDir, targetZip string) error {
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

func isSameCalendarDay(a, b time.Time) bool {
	a = a.Local()
	b = b.Local()
	return a.Year() == b.Year() && a.Month() == b.Month() && a.Day() == b.Day()
}
