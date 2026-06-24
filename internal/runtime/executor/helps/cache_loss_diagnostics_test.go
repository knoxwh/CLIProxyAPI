package helps

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestCacheLossDiagnosticsSkipsFirstSuccessfulRequest(t *testing.T) {
	resetCacheLossDiagnosticStateForTest()
	record := usage.Record{
		Provider: "codex",
		Model:    "gpt-5.4",
		AuthID:   "auth-a",
		Detail: usage.Detail{
			InputTokens:     1200,
			CacheReadTokens: 800,
		},
	}
	info := cacheLossRequestInfo{SessionID: "session-a", SessionSource: "metadata"}

	if event, ok := observeCacheLossDiagnostic(time.Unix(100, 0), record, info); ok {
		t.Fatalf("first request emitted diagnostic: %#v", event)
	}
}

func TestCacheLossDiagnosticsSuppressesZeroReadWhenUsageMissing(t *testing.T) {
	resetCacheLossDiagnosticStateForTest()
	info := cacheLossRequestInfo{SessionID: "session-a", SessionSource: "metadata"}
	first := usage.Record{
		Provider: "codex",
		Model:    "gpt-5.4",
		AuthID:   "auth-a",
		Detail: usage.Detail{
			InputTokens:     1500,
			CacheReadTokens: 1200,
		},
	}
	second := first
	second.Detail = usage.Detail{}

	observeCacheLossDiagnostic(time.Unix(100, 0), first, info)
	if event, ok := observeCacheLossDiagnostic(time.Unix(101, 0), second, info); ok {
		t.Fatalf("missing usage emitted zero-read diagnostic: %#v", event)
	}
}

func TestCacheLossDiagnosticsEmitsWhenCacheReadDropsToZeroAfterHit(t *testing.T) {
	resetCacheLossDiagnosticStateForTest()
	info := cacheLossRequestInfo{
		SessionID:                 "session-a",
		SessionSource:             "header",
		RequestShapeHashPrefix:    "shape1234",
		PromptCacheKeyPrefix:      "cachekey",
		ToolsCount:                2,
		InputItemsCount:           8,
		HasPreviousResponseID:     true,
		StoreValue:                "false",
		TKLiteSessionKeyPresent:   true,
		InstructionsHashPrefix:    "instr123",
		MetadataUserIDSessionSeen: true,
	}
	first := usage.Record{
		Provider: "codex",
		Model:    "gpt-5.4",
		AuthID:   "auth-a",
		Detail: usage.Detail{
			InputTokens:     1500,
			CacheReadTokens: 1200,
		},
	}
	second := first
	second.Detail = usage.Detail{InputTokens: 2200, CacheReadTokens: 0, CacheCreationTokens: 2100}

	observeCacheLossDiagnostic(time.Unix(100, 0), first, info)
	event, ok := observeCacheLossDiagnostic(time.Unix(101, 0), second, info)
	if !ok {
		t.Fatal("expected cache_read_zero diagnostic")
	}
	if event.Trigger != cacheLossTriggerCacheReadZero {
		t.Fatalf("trigger = %q, want %q", event.Trigger, cacheLossTriggerCacheReadZero)
	}
	if event.SessionHash == "session-a" || event.SessionHash == "" {
		t.Fatalf("session hash = %q, want non-empty redacted hash", event.SessionHash)
	}
	if event.PreviousCacheReadTokens != 1200 {
		t.Fatalf("previous cache read = %d, want 1200", event.PreviousCacheReadTokens)
	}
	if event.RequestShapeHashPrefix != "shape1234" || event.PromptCacheKeyPrefix != "cachekey" {
		t.Fatalf("missing request shape breadcrumbs: %#v", event)
	}
}

func TestCacheLossDiagnosticsEmitsWhenRatioFallsBelowHalfAfterFirstRequest(t *testing.T) {
	resetCacheLossDiagnosticStateForTest()
	info := cacheLossRequestInfo{SessionID: "session-a", SessionSource: "metadata"}
	first := usage.Record{
		Provider: "codex",
		Model:    "gpt-5.4",
		AuthID:   "auth-a",
		Detail: usage.Detail{
			InputTokens:     1000,
			CacheReadTokens: 3000,
		},
	}
	second := first
	second.Detail = usage.Detail{InputTokens: 2000, CacheReadTokens: 400}

	observeCacheLossDiagnostic(time.Unix(100, 0), first, info)
	event, ok := observeCacheLossDiagnostic(time.Unix(101, 0), second, info)
	if !ok {
		t.Fatal("expected cache_ratio_low diagnostic")
	}
	if event.Trigger != cacheLossTriggerCacheRatioLow {
		t.Fatalf("trigger = %q, want %q", event.Trigger, cacheLossTriggerCacheRatioLow)
	}
	if event.CacheRatio >= 0.50 {
		t.Fatalf("cache ratio = %f, want < 0.50", event.CacheRatio)
	}
}

func TestCacheLossDiagnosticsPrunesStaleSessions(t *testing.T) {
	resetCacheLossDiagnosticStateForTest()
	oldRecord := usage.Record{Provider: "codex", Model: "gpt-5.4", AuthID: "auth-a", Detail: usage.Detail{InputTokens: 1200, CacheReadTokens: 800}}
	newRecord := usage.Record{Provider: "codex", Model: "gpt-5.4", AuthID: "auth-b", Detail: usage.Detail{InputTokens: 1200, CacheReadTokens: 800}}
	observeCacheLossDiagnostic(time.Unix(100, 0), oldRecord, cacheLossRequestInfo{SessionID: "old-session", SessionSource: "metadata"})
	observeCacheLossDiagnostic(time.Unix(100, 0).Add(cacheLossSessionTTL+time.Second), newRecord, cacheLossRequestInfo{SessionID: "new-session", SessionSource: "metadata"})

	globalCacheLossDiagnostics.mu.Lock()
	defer globalCacheLossDiagnostics.mu.Unlock()
	if len(globalCacheLossDiagnostics.sessions) != 1 {
		t.Fatalf("session count = %d, want stale sessions pruned to 1", len(globalCacheLossDiagnostics.sessions))
	}
	for key := range globalCacheLossDiagnostics.sessions {
		if strings.Contains(key, "old-session") {
			t.Fatalf("stale session was not pruned: %q", key)
		}
	}
}

func TestCacheLossDiagnosticsSuppressesLowRatioForSmallRequests(t *testing.T) {
	resetCacheLossDiagnosticStateForTest()
	info := cacheLossRequestInfo{SessionID: "session-a", SessionSource: "metadata"}
	first := usage.Record{
		Provider: "codex",
		Model:    "gpt-5.4",
		AuthID:   "auth-a",
		Detail: usage.Detail{
			InputTokens:     1000,
			CacheReadTokens: 3000,
		},
	}
	second := first
	second.Detail = usage.Detail{InputTokens: 200, CacheReadTokens: 20}

	observeCacheLossDiagnostic(time.Unix(100, 0), first, info)
	if event, ok := observeCacheLossDiagnostic(time.Unix(101, 0), second, info); ok {
		t.Fatalf("small request emitted diagnostic: %#v", event)
	}
}

func TestUsageReporterPublishRecordObservesCacheLossDiagnostics(t *testing.T) {
	resetCacheLossDiagnosticStateForTest()
	gin.SetMode(gin.TestMode)
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	headers := http.Header{}
	headers.Set(ClaudeCodeSessionHeader, "session-from-header")
	body := []byte(`{"input":[{"type":"message"}],"prompt_cache_key":"abcdef123456","metadata":{"user_id":"user_x_session_11111111-2222-3333-4444-555555555555"}}`)
	RecordCacheLossRequestInfo(ctx, body, body, headers, CacheDiagnosticsOptions{})

	reporter := &UsageReporter{provider: "codex", model: "gpt-5.4", authID: "auth-a", requestedAt: time.Unix(100, 0)}
	firstRecord := reporter.buildRecord(usage.Detail{InputTokens: 1200, CacheReadTokens: 800}, false, usage.Failure{})
	secondRecord := reporter.buildRecord(usage.Detail{InputTokens: 2200, CacheReadTokens: 0}, false, usage.Failure{})
	reporter.publishRecord(ctx, firstRecord)
	reporter.publishRecord(ctx, secondRecord)

	info := cacheLossRequestInfoFromContext(ctx)
	key := cacheLossSessionKey(secondRecord, info)
	globalCacheLossDiagnostics.mu.Lock()
	state, ok := globalCacheLossDiagnostics.sessions[key]
	globalCacheLossDiagnostics.mu.Unlock()
	if !ok {
		t.Fatalf("publishRecord did not create cache-loss diagnostic state for key %q", key)
	}
	if state.RequestCount != 2 || state.PreviousCacheRead != 0 || state.ZeroReadStreak != 1 {
		t.Fatalf("diagnostic state = %#v, want request count 2, previous cache read 0, zero-read streak 1", state)
	}
}

func TestBuildCacheLossRequestInfoExtractsSafeBreadcrumbs(t *testing.T) {
	headers := http.Header{}
	headers.Set(ClaudeCodeSessionHeader, "session-from-header")
	upstream := []byte(`{
		"model":"gpt-5.4",
		"prompt_cache_key":"abcdef1234567890",
		"instructions":"secret instructions",
		"input":[{"type":"message"},{"type":"message"}],
		"tools":[{"type":"function","name":"search","parameters":{"type":"object"}}],
		"previous_response_id":"resp_123",
		"store":false,
		"metadata":{"user_id":"user_x_session_11111111-2222-3333-4444-555555555555"}
	}`)

	info := buildCacheLossRequestInfo(context.Background(), upstream, upstream, headers, CacheDiagnosticsOptions{
		TKLiteSessionKeyPresent: true,
	})

	if info.SessionID != "session-from-header" {
		t.Fatalf("session id = %q, want header session", info.SessionID)
	}
	if info.SessionSource != "header" {
		t.Fatalf("session source = %q, want header", info.SessionSource)
	}
	if info.RequestShapeHashPrefix == "" || info.InstructionsHashPrefix == "" {
		t.Fatalf("expected hash breadcrumbs: %#v", info)
	}
	if info.PromptCacheKeyPrefix == "abcdef12" {
		t.Fatalf("prompt key prefix leaked raw prompt_cache_key prefix: %q", info.PromptCacheKeyPrefix)
	}
	if len(info.PromptCacheKeyPrefix) != cacheLossHashPrefixLen {
		t.Fatalf("prompt key hash prefix length = %d, want %d", len(info.PromptCacheKeyPrefix), cacheLossHashPrefixLen)
	}
	if info.ToolsCount != 1 || info.InputItemsCount != 2 {
		t.Fatalf("counts = tools %d input %d, want 1/2", info.ToolsCount, info.InputItemsCount)
	}
	if !info.HasPreviousResponseID || info.StoreValue != "false" {
		t.Fatalf("response chain fields missing: %#v", info)
	}
	if !info.TKLiteSessionKeyPresent {
		t.Fatalf("expected cache flags: %#v", info)
	}
}
