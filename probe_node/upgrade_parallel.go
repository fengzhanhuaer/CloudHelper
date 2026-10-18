package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Two workers cap router/controller pressure. Each chunk has a separate file;
// only validated, complete chunks are appended to the contiguous resume file.
func downloadProbeParallelRemainder(ctx context.Context, mode, assetURL, controller string, id nodeIdentity, part string, prefix, total int64, validator string, progress func(int64, int64, int64)) error {
	dir := part + ".chunks"
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	type chunk struct {
		start, end int64
		path       string
	}
	var chunks []chunk
	objectKey := sha256.Sum256([]byte(assetURL + "\n" + validator))
	for start := prefix; start < total; start += probeUpgradeProxyChunkSize {
		end := start + probeUpgradeProxyChunkSize - 1
		if end >= total {
			end = total - 1
		}
		chunks = append(chunks, chunk{start, end, filepath.Join(dir, fmt.Sprintf("%x-%d-%d", objectKey[:8], start, end))})
	}
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan int)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var first error
	done := make([]int64, len(chunks))
	started := time.Now()
	for worker := 0; worker < 2; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				item := chunks[index]
				localValidator := validator
				var err error
				for attempt := 0; attempt <= probeUpgradeDownloadMaxRetries; attempt++ {
					size := int64(0)
					if st, e := os.Stat(item.path); e == nil {
						size = st.Size()
					}
					if size == item.end-item.start+1 {
						err = nil
						break
					}
					if size > item.end-item.start+1 {
						err = errors.New("oversized partial chunk")
						break
					}
					_, _, err = downloadProbeAssetChunk(workCtx, mode, assetURL, controller, id, item.path, item.start+size, &localValidator, total, func(n, _ int64) {
						mu.Lock()
						done[index] = n - item.start
						sum := prefix
						for _, n := range done {
							sum += n
						}
						if progress != nil {
							progress(sum, total, int64(float64(sum-prefix)/time.Since(started).Seconds()))
						}
						mu.Unlock()
					}, item.end)
					if err == nil {
						break
					}
					if workCtx.Err() != nil || !isProbeTransientHTTPError(err) {
						break
					}
					if e := waitProbeUpgradeRetry(workCtx, probeUpgradeDownloadRetryDelay(attempt)); e != nil {
						err = e
						break
					}
				}
				if err != nil {
					mu.Lock()
					if first == nil {
						first = err
					}
					mu.Unlock()
					cancel()
					return
				}
			}
		}()
	}
loop:
	for i := range chunks {
		select {
		case jobs <- i:
		case <-workCtx.Done():
			break loop
		}
	}
	close(jobs)
	wg.Wait()
	if first != nil {
		return first
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	f, err := os.OpenFile(part, os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, item := range chunks {
		chunk, err := os.Open(item.path)
		if err != nil {
			return err
		}
		st, e := chunk.Stat()
		if e != nil || st.Size() != item.end-item.start+1 {
			chunk.Close()
			return errors.New("incomplete download chunk")
		}
		_, e = io.Copy(f, chunk)
		closeErr := chunk.Close()
		if e != nil {
			return e
		}
		if closeErr != nil {
			return closeErr
		}
		if e = f.Sync(); e != nil {
			return e
		}
	}
	for _, item := range chunks {
		os.Remove(item.path)
		os.Remove(item.path + ".json")
	}
	os.Remove(dir)
	return nil
}
