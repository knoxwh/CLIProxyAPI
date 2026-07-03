// Package cacheregression detects drops in upstream cache_read_input_tokens
// within a cache bucket (auth+session+system_hash) and logs the current and
// previous upstream request bodies so operators can diff what broke the cache.
package cacheregression

import (
	"sync"
	"sync/atomic"
	"time"
)

// Meta decorates a regression log entry with identifying context.
type Meta struct {
	AuthID     string
	AuthLabel  string
	SessionID  string
	SystemHash string
	Model      string
	Provider   string
}

type entry struct {
	mu       sync.Mutex
	maxRead  int64
	prevRead int64
	prevBody []byte
	prevTime time.Time
}

// Tracker holds per-bucket historical max cache_read and the last request body.
type Tracker struct {
	m      sync.Map // key -> *entry
	logDir atomic.Pointer[string]
}

// DefaultTracker is the process-wide singleton.
var DefaultTracker = &Tracker{}

// Configure sets the log directory. Call at startup and on config reload.
// Safe for concurrent use.
func (t *Tracker) Configure(logDir string) {
	if t == nil {
		return
	}
	s := logDir
	t.logDir.Store(&s)
}

// Record observes one cache_read value for a bucket. On a regression (cacheRead
// below the bucket's historical max, with a prior non-zero baseline), it writes
// the current and previous request bodies to the regression log.
//
// A zero cacheRead is a meaningful observation: once a non-zero baseline exists,
// a drop to zero means the cache was lost entirely and is logged as a regression.
// Only a zero on the very first sighting is skipped (no baseline to regress from).
func (t *Tracker) Record(key string, cacheRead int64, body []byte, meta Meta) {
	if t == nil || key == "" {
		return
	}
	e := t.loadOrCreate(key)
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.maxRead == 0 {
		// No baseline yet. A zero first sighting cannot establish one; skip
		// until we see a non-zero value to regress from.
		if cacheRead == 0 {
			return
		}
		// first non-zero sighting: establish baseline
		e.maxRead = cacheRead
		e.prevRead = cacheRead
		e.prevBody = body
		e.prevTime = time.Now()
		return
	}
	if cacheRead >= e.maxRead {
		// monotonic OK; advance
		e.maxRead = cacheRead
		e.prevRead = cacheRead
		e.prevBody = body
		e.prevTime = time.Now()
		return
	}
	// regression: cacheRead < maxRead (includes drop-to-zero)
	writeRegressionLog(t.logDirPath(), key, cacheRead, body, e, meta)
	e.prevRead = cacheRead
	e.prevBody = body
	e.prevTime = time.Now()
}

func (t *Tracker) loadOrCreate(key string) *entry {
	actual, _ := t.m.LoadOrStore(key, &entry{})
	return actual.(*entry)
}

func (t *Tracker) logDirPath() string {
	if p := t.logDir.Load(); p != nil && *p != "" {
		return *p
	}
	return "logs"
}
