// Package logging provides structured logging for manager_service.
package logging

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
)

const (
	logFile       = "manager_service.log"
	logMaxSize    = 2 * 1024 * 1024 // 2 MB
	logMaxBackups = 5
)

// Init sets up the global logger to write to a rotating file beside the executable.
func Init() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	w, err := newWriter()
	if err != nil {
		// Fall back to stderr — don't block startup.
		log.SetOutput(os.Stderr)
		log.Printf("[warning] failed to open log file, falling back to stderr: %v", err)
		return
	}

	// Write to both file and stderr so operators see output.
	log.SetOutput(io.MultiWriter(w, os.Stderr))
}

func Infof(format string, args ...any)  { log.Printf("[info]    "+format, args...) }
func Warnf(format string, args ...any)  { log.Printf("[warning] "+format, args...) }
func Errorf(format string, args ...any) { log.Printf("[error]   "+format, args...) }

// ---- rotating file writer ----

type rotatingFileWriter struct {
	mu         sync.Mutex
	filePath   string
	maxSize    int64
	maxBackups int
	file       *os.File
	size       int64
}

func newWriter() (*rotatingFileWriter, error) {
	dir := logDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir log dir: %w", err)
	}
	w := &rotatingFileWriter{
		filePath:   filepath.Join(dir, logFile),
		maxSize:    logMaxSize,
		maxBackups: logMaxBackups,
	}
	if err := w.openActive(false); err != nil {
		return nil, err
	}
	return w, nil
}

func logDir() string {
	if exe, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(exe), "log")
	}
	return filepath.Join(".", "log")
}

func (w *rotatingFileWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

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
	_ = w.file.Close()
	w.file = nil

	for i := w.maxBackups - 1; i >= 1; i-- {
		src := fmt.Sprintf("%s.%d", w.filePath, i)
		dst := fmt.Sprintf("%s.%d", w.filePath, i+1)
		_ = os.Remove(dst)
		_ = os.Rename(src, dst)
	}
	_ = os.Rename(w.filePath, w.filePath+".1")

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
		if info, err := f.Stat(); err == nil {
			size = info.Size()
		}
	}
	w.file = f
	w.size = size
	return nil
}
