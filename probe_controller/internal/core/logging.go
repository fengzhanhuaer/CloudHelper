package core

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
)

const (
	controllerLogFile       = "probe_controller.log"
	controllerLogMaxSize    = 1 * 1024 * 1024
	controllerLogMaxBackups = 5
)

func initControllerLogger() {
	log.SetOutput(io.Discard)
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	writer, err := newControllerLogWriter()
	if err != nil {
		return
	}
	log.SetOutput(writer)
}

func logControllerRealtimef(format string, args ...any) {
	log.Printf("[realtime] "+format, args...)
}

func logControllerInfof(format string, args ...any) {
	log.Printf("[normal] "+format, args...)
}

func logControllerWarnf(format string, args ...any) {
	log.Printf("[warning] "+format, args...)
}

func logControllerErrorf(format string, args ...any) {
	log.Printf("[error] "+format, args...)
}

func newControllerLogWriter() (io.Writer, error) {
	var lastErr error
	for _, dir := range candidateLogDirs() {
		w, err := newRotatingFileWriter(filepath.Join(dir, controllerLogFile), controllerLogMaxSize, controllerLogMaxBackups)
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

func candidateLogDirs() []string {
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

type rotatingFileWriter struct {
	mu         sync.Mutex
	filePath   string
	maxSize    int64
	maxBackups int
	file       *os.File
	size       int64
}

func newRotatingFileWriter(filePath string, maxSize int64, maxBackups int) (*rotatingFileWriter, error) {
	if maxSize <= 0 {
		return nil, fmt.Errorf("invalid max size: %d", maxSize)
	}
	if maxBackups < 0 {
		return nil, fmt.Errorf("invalid max backups: %d", maxBackups)
	}

	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		return nil, err
	}

	w := &rotatingFileWriter{
		filePath:   filePath,
		maxSize:    maxSize,
		maxBackups: maxBackups,
	}
	if err := w.openActive(false); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *rotatingFileWriter) Write(p []byte) (int, error) {
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

func (w *rotatingFileWriter) rotate() error {
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

func (w *rotatingFileWriter) openActive(truncate bool) error {
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
