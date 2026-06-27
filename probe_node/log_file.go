package main

import (
	"io"
	"os"
	"sync"
)

type probeRotatingLogFileWriter struct {
	mu       sync.Mutex
	path     string
	file     *os.File
	maxBytes int64
}

func newProbeRotatingLogFileWriter(path string, file *os.File, maxBytes int) *probeRotatingLogFileWriter {
	if maxBytes <= 0 {
		maxBytes = probeLogMaxBytes
	}
	return &probeRotatingLogFileWriter{
		path:     path,
		file:     file,
		maxBytes: int64(maxBytes),
	}
}

func (w *probeRotatingLogFileWriter) Write(p []byte) (int, error) {
	if w == nil || w.file == nil {
		return len(p), nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	n, err := w.file.Write(p)
	if err != nil {
		return n, err
	}
	if w.maxBytes <= 0 {
		return n, nil
	}
	if err := trimProbeLogFileLocked(w.file, w.path, w.maxBytes); err != nil {
		return n, err
	}
	return n, nil
}

func (w *probeRotatingLogFileWriter) Close() error {
	if w == nil || w.file == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	err := w.file.Close()
	w.file = nil
	return err
}

func trimProbeLogFileLocked(file *os.File, path string, maxBytes int64) error {
	if file == nil || maxBytes <= 0 {
		return nil
	}
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Size() <= maxBytes {
		return nil
	}
	tail, err := readProbeLogFileTail(path, maxBytes)
	if err != nil {
		return err
	}
	if err := file.Truncate(0); err != nil {
		return err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if len(tail) == 0 {
		return nil
	}
	_, err = file.Write(tail)
	return err
}

func readProbeLogFileTail(path string, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		return []byte{}, nil
	}
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []byte{}, nil
		}
		return nil, err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	size := info.Size()
	if size <= maxBytes {
		return io.ReadAll(file)
	}
	start := size - maxBytes
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(file)
}
