// Package cacheregression detects drops in upstream cache_read_input_tokens
// within a cache bucket (auth+session+system_hash) and logs the current and
// previous upstream request bodies so operators can diff what broke the cache.
package cacheregression

import (
	"sync"
	"sync/atomic"
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
	maxRead  int64 // historical peak, for display only
	prevRead int64 // last turn's cache_read, the regression baseline
	prevBody []byte
}

// Tracker holds per-bucket cache_read history and the last request body.
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

// Record observes one cache_read value for a bucket. A regression is a drop
// from the previous turn's cache_read (cacheRead < prevRead), including a drop
// to zero. Recovery turns (cacheRead >= prevRead) do not log, so a cache that
// is rebuilding does not spam the log. maxRead is tracked only for display.
//
// A zero on the very first sighting is skipped (no prior turn to regress from).
func (t *Tracker) Record(key string, cacheRead int64, body []byte, meta Meta) {
	if t == nil || key == "" {
		return
	}
	e := t.loadOrCreate(key)
	e.mu.Lock()
	defer e.mu.Unlock()

	// No baseline yet: a zero first sighting cannot establish one, skip until
	// we see a non-zero value to regress from.
	if e.prevRead == 0 && cacheRead == 0 {
		return
	}

	if e.prevRead > 0 && cacheRead < e.prevRead {
		// regression: drop from previous turn (includes drop-to-zero)
		writeRegressionLog(t.logDirPath(), key, cacheRead, body, e, meta)
	}

	// Advance baseline; track peak for display.
	if cacheRead > e.maxRead {
		e.maxRead = cacheRead
	}
	e.prevRead = cacheRead
	e.prevBody = body
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
