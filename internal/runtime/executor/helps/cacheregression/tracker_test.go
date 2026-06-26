package cacheregression

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestTracker(t *testing.T) (*Tracker, string) {
	t.Helper()
	dir := t.TempDir()
	tr := &Tracker{}
	tr.Configure(dir)
	return tr, dir
}

func TestRecord_FirstSighting_SetsBaseline_NoLog(t *testing.T) {
	tr, dir := newTestTracker(t)
	tr.Record("k", 1000, []byte(`{"i":1}`), Meta{AuthID: "a", SessionID: "s", SystemHash: "h", Model: "m"})
	if files, _ := filepath.Glob(filepath.Join(dir, "cache-regression-*.log")); len(files) != 0 {
		t.Fatalf("expected no log file on baseline, got %v", files)
	}
}

func TestRecord_MonotonicAdvance_NoLog(t *testing.T) {
	tr, dir := newTestTracker(t)
	tr.Record("k", 1000, []byte(`{"i":1}`), Meta{})
	tr.Record("k", 1500, []byte(`{"i":2}`), Meta{})
	tr.Record("k", 1500, []byte(`{"i":3}`), Meta{}) // equal is not a regression
	if files, _ := filepath.Glob(filepath.Join(dir, "cache-regression-*.log")); len(files) != 0 {
		t.Fatalf("expected no log file on monotonic advance, got %v", files)
	}
}

func TestRecord_Regression_WritesBothBodies(t *testing.T) {
	tr, dir := newTestTracker(t)
	tr.Record("k", 15000, []byte(`{"body":"prev-hit"}`), Meta{AuthID: "a", SessionID: "s", SystemHash: "h", Model: "m"})
	tr.Record("k", 8000, []byte(`{"body":"curr-drop"}`), Meta{AuthID: "a", SessionID: "s", SystemHash: "h", Model: "m"})
	files, _ := filepath.Glob(filepath.Join(dir, "cache-regression-*.log"))
	if len(files) != 1 {
		t.Fatalf("expected 1 log file, got %v", files)
	}
	data, _ := os.ReadFile(files[0])
	s := string(data)
	if !strings.Contains(s, "prev=15000") || !strings.Contains(s, "curr=8000") || !strings.Contains(s, "delta=-7000") {
		t.Fatalf("log missing regression numbers:\n%s", s)
	}
	if !strings.Contains(s, `{"body":"prev-hit"}`) || !strings.Contains(s, `{"body":"curr-drop"}`) {
		t.Fatalf("log missing one of the bodies:\n%s", s)
	}
}

func TestRecord_ZeroCacheRead_Skipped(t *testing.T) {
	tr, dir := newTestTracker(t)
	tr.Record("k", 0, []byte(`{}`), Meta{})
	if files, _ := filepath.Glob(filepath.Join(dir, "cache-regression-*.log")); len(files) != 0 {
		t.Fatalf("zero cache_read must not log, got %v", files)
	}
}

func TestRecord_EmptyKey_Skipped(t *testing.T) {
	tr, dir := newTestTracker(t)
	tr.Record("", 1000, []byte(`{}`), Meta{})
	if files, _ := filepath.Glob(filepath.Join(dir, "cache-regression-*.log")); len(files) != 0 {
		t.Fatalf("empty key must not log, got %v", files)
	}
}

func TestRecord_RegressionKeepsMaxForNextDrop(t *testing.T) {
	tr, dir := newTestTracker(t)
	tr.Record("k", 15000, []byte(`{"i":1}`), Meta{})
	tr.Record("k", 8000, []byte(`{"i":2}`), Meta{}) // regression vs max 15000
	tr.Record("k", 5000, []byte(`{"i":3}`), Meta{}) // regression again vs max 15000
	files, _ := filepath.Glob(filepath.Join(dir, "cache-regression-*.log"))
	if len(files) != 1 {
		t.Fatalf("expected single appended file, got %v", files)
	}
	data, _ := os.ReadFile(files[0])
	if strings.Count(string(data), "=== CACHE REGRESSION ===") != 2 {
		t.Fatalf("expected 2 regression entries, got:\n%s", data)
	}
}

func TestRecord_DifferentSystemHash_IndependentBuckets(t *testing.T) {
	tr, dir := newTestTracker(t)
	tr.Record("auth:s:h1", 15000, []byte(`{}`), Meta{})
	tr.Record("auth:s:h2", 8000, []byte(`{}`), Meta{}) // different bucket, not a regression
	if files, _ := filepath.Glob(filepath.Join(dir, "cache-regression-*.log")); len(files) != 0 {
		t.Fatalf("different systemHash must not fire regression, got %v", files)
	}
}

func TestRecord_ConcurrentSameKey(t *testing.T) {
	tr, _ := newTestTracker(t)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			tr.Record("k", int64(1000+n), []byte(`{}`), Meta{})
		}(i)
	}
	wg.Wait()
	// no race/panic
}

func TestRecord_FallbackToCwdLogs_NoPanic(t *testing.T) {
	// Switch to a temp cwd so the fallback "logs" dir does not pollute the repo.
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(orig)

	tr := &Tracker{} // no Configure => fallback to ./logs
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Record panicked without Configure: %v", r)
		}
	}()
	tr.Record("k", 1000, []byte(`{"i":1}`), Meta{})
	tr.Record("k", 500, []byte(`{"i":2}`), Meta{})

	files, _ := filepath.Glob(filepath.Join(tmp, "logs", "cache-regression-*.log"))
	if len(files) != 1 {
		t.Fatalf("expected fallback log under ./logs, got %v", files)
	}
}

func TestLogFileNameUsesToday(t *testing.T) {
	tr, dir := newTestTracker(t)
	tr.Record("k", 1000, []byte(`{"i":1}`), Meta{})
	tr.Record("k", 500, []byte(`{"i":2}`), Meta{})
	want := "cache-regression-" + time.Now().Format("2006-01-02") + ".log"
	if _, err := os.Stat(filepath.Join(dir, want)); err != nil {
		t.Fatalf("expected file %s: %v", want, err)
	}
}

func TestRecord_FullDailyLog_RotatesToTimestampedFile(t *testing.T) {
	tr, dir := newTestTracker(t)
	baseName := "cache-regression-" + time.Now().Format("2006-01-02") + ".log"
	basePath := filepath.Join(dir, baseName)
	if err := os.WriteFile(basePath, []byte("seed"), 0o644); err != nil {
		t.Fatalf("seed base log: %v", err)
	}
	if err := os.Truncate(basePath, 10*1024*1024); err != nil {
		t.Fatalf("truncate base log: %v", err)
	}

	tr.Record("k", 15000, []byte(`{"body":"prev-hit"}`), Meta{AuthID: "a", SessionID: "s", SystemHash: "h", Model: "m"})
	tr.Record("k", 8000, []byte(`{"body":"curr-drop"}`), Meta{AuthID: "a", SessionID: "s", SystemHash: "h", Model: "m"})

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	var rotatedName string
	pattern := regexp.MustCompile(`^cache-regression-\d{12}\.log$`)
	for _, entry := range entries {
		if pattern.MatchString(entry.Name()) {
			rotatedName = entry.Name()
			break
		}
	}
	if rotatedName == "" {
		t.Fatalf("expected timestamped rotation file in %v", entries)
	}
	// After rotation the base file is recreated fresh and receives the new
	// regression entries; the rotated file holds the pre-rotation content.
	if _, err := os.Stat(filepath.Join(dir, baseName)); err != nil {
		t.Fatalf("base log missing: %v", err)
	}
	baseData, err := os.ReadFile(filepath.Join(dir, baseName))
	if err != nil {
		t.Fatalf("read base log: %v", err)
	}
	s := string(baseData)
	if !strings.Contains(s, "prev=15000") || !strings.Contains(s, "curr=8000") {
		t.Fatalf("base log missing regression data:\n%s", s)
	}
	if !strings.Contains(s, `{"body":"prev-hit"}`) || !strings.Contains(s, `{"body":"curr-drop"}`) {
		t.Fatalf("base log missing body content:\n%s", s)
	}
}
