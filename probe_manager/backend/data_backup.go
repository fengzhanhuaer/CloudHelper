package backend

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

type managerBackupArchive struct {
	path    string
	modTime time.Time
}

func autoBackupManagerData() error {
	dataDirs, err := managerDataDirectories()
	if err != nil {
		return err
	}
	if len(dataDirs) == 0 {
		return nil
	}

	errMessages := make([]string, 0)
	for _, dataPath := range dataDirs {
		if err := backupOneManagerDataDir(dataPath, time.Now()); err != nil {
			errMessages = append(errMessages, err.Error())
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

func backupOneManagerDataDir(dataPath string, now time.Time) error {
	backupDir := filepath.Join(filepath.Dir(dataPath), "bakup")
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return err
	}

	archivePath := filepath.Join(backupDir, "manager-data-"+now.Format("20060102-150405")+".zip")
	if err := zipManagerDirectory(dataPath, archivePath); err != nil {
		_ = os.Remove(archivePath)
		return fmt.Errorf("backup %s failed: %w", dataPath, err)
	}

	if err := pruneManagerBackupArchives(backupDir, "manager-data-", now); err != nil {
		return fmt.Errorf("prune %s failed: %w", backupDir, err)
	}
	return nil
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
		if !hasYesterday && isSameCalendarDay(t, yesterday) {
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
