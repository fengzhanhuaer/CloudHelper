package core

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type backupArchive struct {
	path    string
	modTime time.Time
}

const (
	backupDirName             = "backup"
	backupArchivePrefix       = "controller-data-"
	backupArchiveDateTimeFmt  = "20060102-150405.000000000"
	backupKeepPerTimeCategory = 3
)

func autoBackupControllerData() error {
	dataPath, err := filepath.Abs(dataDir)
	if err != nil {
		return err
	}
	info, err := os.Stat(dataPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("controller data path is not directory: %s", dataPath)
	}

	backupDir := filepath.Join(filepath.Dir(dataPath), backupDirName)
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return err
	}

	now := time.Now()
	archivePath := filepath.Join(backupDir, backupArchivePrefix+now.Format(backupArchiveDateTimeFmt)+".zip")
	if err := zipDirectory(dataPath, archivePath); err != nil {
		_ = os.Remove(archivePath)
		return err
	}

	return pruneBackupArchives(backupDir, backupArchivePrefix, now)
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

func sameDay(a, b time.Time) bool {
	a = a.Local()
	b = b.Local()
	return a.Year() == b.Year() && a.Month() == b.Month() && a.Day() == b.Day()
}
