package backend

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
)

const (
	managerLogFile       = "probe_manager.log"
	managerLogMaxSize    = 1 * 1024 * 1024
	managerLogMaxBackups = 5
)

func InitManagerLogger() {
	log.SetOutput(io.Discard)
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	writer, err := newManagerLogWriter()
	if err != nil {
		return
	}
	log.SetOutput(writer)
}


func logManagerRealtimef(format string, args ...any) {
	log.Printf("[realtime] "+format, args...)
}

func logManagerInfof(format string, args ...any) {
	log.Printf("[normal] "+format, args...)
}

func logManagerWarnf(format string, args ...any) {
	log.Printf("[warning] "+format, args...)
}

func logManagerErrorf(format string, args ...any) {
	log.Printf("[error] "+format, args...)
}

func newManagerLogWriter() (io.Writer, error) {
	var lastErr error
	for _, dir := range managerCandidateLogDirs() {
		w, err := newManagerRotatingFileWriter(filepath.Join(dir, managerLogFile), managerLogMaxSize, managerLogMaxBackups)
		if err == nil {
			return w, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no log directory candidates")
	}
	return nil, lastErr
}

func managerCandidateLogDirs() []string {
	candidates := []string{filepath.Join(".", "log")}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "log"))
	}

	seen := make(map[string]struct{}, len(candidates))
	uniq := make([]string, 0, len(candidates))
	for _, dir := range candidates {
		if dir == "" {
			continue
		}
		absDir, err := filepath.Abs(dir)
		if err != nil {
			continue
		}
		if _, ok := seen[absDir]; ok {
			continue
		}
		seen[absDir] = struct{}{}
		uniq = append(uniq, absDir)
	}
	return uniq
}

type managerRotatingFileWriter struct {
	mu         sync.Mutex
	filePath   string
	maxSize    int64
	maxBackups int
	file       *os.File
	size       int64
}

func newManagerRotatingFileWriter(filePath string, maxSize int64, maxBackups int) (*managerRotatingFileWriter, error) {
	if maxSize <= 0 {
		return nil, fmt.Errorf("invalid max size: %d", maxSize)
	}
	if maxBackups < 0 {
		return nil, fmt.Errorf("invalid max backups: %d", maxBackups)
	}

	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		return nil, err
	}

	w := &managerRotatingFileWriter{
		filePath:   filePath,
		maxSize:    maxSize,
		maxBackups: maxBackups,
	}
	if err := w.openActive(false); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *managerRotatingFileWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.file == nil {
		if err := w.openActive(false); err != nil {
			return 0, err
		}
	}

	if w.maxSize > 0 && w.size+int64(len(p)) > w.maxSize {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}

	n, err := w.file.Write(p)
	w.size += int64(n)
	return n, err
}

func (w *managerRotatingFileWriter) rotate() error {
	if w.file != nil {
		if err := w.file.Close(); err != nil {
			return err
		}
		w.file = nil
	}

	if w.maxBackups > 0 {
		oldest := fmt.Sprintf("%s.%d", w.filePath, w.maxBackups)
		_ = os.Remove(oldest)

		for i := w.maxBackups - 1; i >= 1; i-- {
			src := fmt.Sprintf("%s.%d", w.filePath, i)
			dst := fmt.Sprintf("%s.%d", w.filePath, i+1)
			_ = os.Remove(dst)
			if err := os.Rename(src, dst); err != nil && !os.IsNotExist(err) {
				return err
			}
		}

		firstBackup := fmt.Sprintf("%s.1", w.filePath)
		_ = os.Remove(firstBackup)
		if err := os.Rename(w.filePath, firstBackup); err != nil && !os.IsNotExist(err) {
			return err
		}
	} else {
		_ = os.Remove(w.filePath)
	}

	return w.openActive(true)
}

func (w *managerRotatingFileWriter) openActive(truncate bool) error {
	flags := os.O_CREATE | os.O_WRONLY
	if truncate {
		flags |= os.O_TRUNC
	} else {
		flags |= os.O_APPEND
	}

	f, err := os.OpenFile(w.filePath, flags, 0o644)
	if err != nil {
		return err
	}

	var size int64
	if !truncate {
		if info, statErr := f.Stat(); statErr == nil {
			size = info.Size()
		}
	}

	w.file = f
	w.size = size
	return nil
}
