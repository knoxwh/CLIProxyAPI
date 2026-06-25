package cacheregression

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const cacheRegressionMaxLogSizeBytes int64 = 10 * 1024 * 1024

var logFileMu sync.Map // path -> *sync.Mutex

func writeRegressionLog(logDir, key string, cacheRead int64, body []byte, e *entry, meta Meta) {
	if e == nil {
		return
	}
	now := time.Now()
	name := "cache-regression-" + now.Format("2006-01-02") + ".log"
	path := filepath.Join(logDir, name)

	mu := loadFileMu(path)
	mu.Lock()
	defer mu.Unlock()

	if err := os.MkdirAll(logDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "cacheregression: mkdir %s failed: %v\n", logDir, err)
		return
	}
	path = regressionLogPathBeforeWrite(logDir, path, now)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cacheregression: open %s failed: %v\n", path, err)
		return
	}
	defer f.Close()

	delta := cacheRead - e.maxRead
	fmt.Fprintf(f, "=== CACHE REGRESSION ===\n")
	fmt.Fprintf(f, "Timestamp:      %s\n", time.Now().Format("2006-01-02T15:04:05Z07:00"))
	fmt.Fprintf(f, "Auth:           %s (%s)\n", meta.AuthID, meta.AuthLabel)
	fmt.Fprintf(f, "Session:        %s\n", meta.SessionID)
	fmt.Fprintf(f, "SystemHash:     %s\n", meta.SystemHash)
	fmt.Fprintf(f, "Model:          %s\n", meta.Model)
	fmt.Fprintf(f, "Provider:       %s\n", meta.Provider)
	fmt.Fprintf(f, "Bucket:         %s\n", key)
	fmt.Fprintf(f, "CacheRead:      prev=%d → curr=%d  delta=%d  (max=%d)\n", e.prevRead, cacheRead, delta, e.maxRead)
	fmt.Fprintf(f, "--- CURRENT REQUEST BODY (post-tklite, upstream-bound) ---\n")
	fmt.Fprintf(f, "%s\n", string(body))
	fmt.Fprintf(f, "--- PREVIOUS REQUEST BODY (last hit, cache_read=%d) ---\n", e.prevRead)
	fmt.Fprintf(f, "%s\n", string(e.prevBody))
	fmt.Fprintf(f, "=== END ===\n\n")
}

func regressionLogPathBeforeWrite(logDir, basePath string, now time.Time) string {
	info, err := os.Stat(basePath)
	if err != nil || info.Size() < cacheRegressionMaxLogSizeBytes {
		return basePath
	}
	name := "cache-regression-" + now.Format("2006-01-02-0102150405") + ".log"
	return filepath.Join(logDir, name)
}

func loadFileMu(path string) *sync.Mutex {
	v, _ := logFileMu.LoadOrStore(path, &sync.Mutex{})
	return v.(*sync.Mutex)
}
