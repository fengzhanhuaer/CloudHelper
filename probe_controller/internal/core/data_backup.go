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

	backupDir := filepath.Join(filepath.Dir(dataPath), "bakup")
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return err
	}

	archivePath := filepath.Join(backupDir, "controller-data-"+time.Now().Format("20060102-150405")+".zip")
	if err := zipDirectory(dataPath, archivePath); err != nil {
		_ = os.Remove(archivePath)
		return err
	}

	return pruneBackupArchives(backupDir, "controller-data-", time.Now())
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

	if len(archives) <= 1 {
		return nil
	}

	sort.Slice(archives, func(i, j int) bool {
		return archives[i].modTime.After(archives[j].modTime)
	})

	keep := map[string]struct{}{archives[0].path: {}}
	now = now.Local()
	yesterday := now.AddDate(0, 0, -1)
	lastWeekRef := now.AddDate(0, 0, -7)
	lastWeekYear, lastWeekNo := lastWeekRef.ISOWeek()
	lastMonthRef := now.AddDate(0, -1, 0)
	lastMonthYear, lastMonthNo := lastMonthRef.Year(), lastMonthRef.Month()
	lastYear := now.Year() - 1

	hasYesterday := false
	hasLastWeek := false
	hasLastMonth := false
	hasLastYear := false

	for _, archive := range archives {
		t := archive.modTime.Local()
		if !hasYesterday && sameDay(t, yesterday) {
			keep[archive.path] = struct{}{}
			hasYesterday = true
		}
		if !hasLastWeek {
			y, w := t.ISOWeek()
			if y == lastWeekYear && w == lastWeekNo {
				keep[archive.path] = struct{}{}
				hasLastWeek = true
			}
		}
		if !hasLastMonth && t.Year() == lastMonthYear && t.Month() == lastMonthNo {
			keep[archive.path] = struct{}{}
			hasLastMonth = true
		}
		if !hasLastYear && t.Year() == lastYear {
			keep[archive.path] = struct{}{}
			hasLastYear = true
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
