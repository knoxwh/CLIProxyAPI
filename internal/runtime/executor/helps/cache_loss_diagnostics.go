package helps

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

const (
	cacheLossTriggerCacheReadZero = "cache_read_zero"
	cacheLossTriggerCacheRatioLow = "cache_ratio_low"

	cacheLossRatioThreshold       = 0.50
	cacheLossMinTokenWindow int64 = 1000
	cacheLossHashPrefixLen        = 12
	cacheLossSessionTTL           = 24 * time.Hour
	cacheLossMaxSessions          = 4096
)

const cacheLossRequestInfoContextKey = "CACHE_LOSS_REQUEST_INFO"

// CacheDiagnosticsOptions carries safe request-path facts for cache-loss diagnostics.
type CacheDiagnosticsOptions struct {
	TKLiteSessionKeyPresent bool
	ResponseChainingEnabled bool
}

type cacheLossRequestInfo struct {
	SessionID                 string
	SessionSource             string
	RequestShapeHashPrefix    string
	PromptCacheKeyPrefix      string
	ToolsCount                int
	InputItemsCount           int
	HasPreviousResponseID     bool
	StoreValue                string
	TKLiteSessionKeyPresent   bool
	ResponseChainingEnabled   bool
	InstructionsHashPrefix    string
	MetadataUserIDSessionSeen bool
}

type cacheLossDiagnosticEvent struct {
	Trigger                 string
	SessionHash             string
	SessionSource           string
	Provider                string
	Model                   string
	Alias                   string
	AuthHash                string
	Source                  string
	ReasoningEffort         string
	ServiceTier             string
	LatencyMS               int64
	TTFTMS                  int64
	InputTokens             int64
	CacheReadTokens         int64
	CacheCreationTokens     int64
	CacheRatio              float64
	PreviousCacheReadTokens int64
	PreviousCacheRatio      float64
	RequestCountInSession   int
	LowRatioStreak          int
	ZeroReadStreak          int
	RequestShapeHashPrefix  string
	PromptCacheKeyPrefix    string
	ToolsCount              int
	InputItemsCount         int
	HasPreviousResponseID   bool
	StoreValue              string
	TKLiteSessionKeyPresent bool
	ResponseChainingEnabled bool
	InstructionsHashPrefix  string
	MetadataSessionPresent  bool
}

type cacheLossSessionState struct {
	RequestCount        int
	PreviousCacheRead   int64
	PreviousCacheRatio  float64
	LowRatioStreak      int
	ZeroReadStreak      int
	LastSeen            time.Time
	LastEmittedByReason map[string]time.Time
}

type cacheLossDiagnosticStore struct {
	mu       sync.Mutex
	sessions map[string]cacheLossSessionState
}

var globalCacheLossDiagnostics = &cacheLossDiagnosticStore{sessions: make(map[string]cacheLossSessionState)}

// RecordCacheLossRequestInfo stores safe request breadcrumbs for the usage reporter.
func RecordCacheLossRequestInfo(ctx context.Context, upstreamBody []byte, originalPayload []byte, headers http.Header, options CacheDiagnosticsOptions) {
	if ctx == nil {
		return
	}
	info := buildCacheLossRequestInfo(ctx, upstreamBody, originalPayload, headers, options)
	if info.SessionID == "" {
		return
	}
	if ginCtx := ginContextFrom(ctx); ginCtx != nil {
		ginCtx.Set(cacheLossRequestInfoContextKey, info)
	}
}

func cacheLossRequestInfoFromContext(ctx context.Context) cacheLossRequestInfo {
	if ctx == nil {
		return cacheLossRequestInfo{}
	}
	if ginCtx := ginContextFrom(ctx); ginCtx != nil {
		if value, ok := ginCtx.Get(cacheLossRequestInfoContextKey); ok {
			if info, ok := value.(cacheLossRequestInfo); ok {
				return info
			}
		}
	}
	return cacheLossRequestInfo{}
}

func buildCacheLossRequestInfo(ctx context.Context, upstreamBody []byte, originalPayload []byte, headers http.Header, options CacheDiagnosticsOptions) cacheLossRequestInfo {
	body := upstreamBody
	if len(body) == 0 {
		body = originalPayload
	}
	info := cacheLossRequestInfo{
		SessionID:               ExtractClaudeCodeSessionID(ctx, originalPayload, headers),
		SessionSource:           cacheLossSessionSource(ctx, originalPayload, headers),
		RequestShapeHashPrefix:  hashJSONPrefix(body),
		PromptCacheKeyPrefix:    hashStringPrefix(gjson.GetBytes(body, "prompt_cache_key").String()),
		ToolsCount:              jsonArrayCount(body, "tools"),
		InputItemsCount:         jsonArrayCount(body, "input"),
		HasPreviousResponseID:   strings.TrimSpace(gjson.GetBytes(body, "previous_response_id").String()) != "",
		StoreValue:              cacheLossStoreValue(body),
		TKLiteSessionKeyPresent: options.TKLiteSessionKeyPresent || strings.TrimSpace(headers.Get("x-tklite-session-key")) != "",
		ResponseChainingEnabled: options.ResponseChainingEnabled,
		InstructionsHashPrefix:  hashJSONPrefix([]byte(gjson.GetBytes(body, "instructions").Raw)),
	}
	if info.SessionID == "" {
		info.SessionID = ExtractClaudeCodeSessionID(ctx, body, headers)
		info.SessionSource = cacheLossSessionSource(ctx, body, headers)
	}
	info.MetadataUserIDSessionSeen = strings.TrimSpace(gjson.GetBytes(body, "metadata.user_id").String()) != "" || strings.TrimSpace(gjson.GetBytes(originalPayload, "metadata.user_id").String()) != ""
	return info
}

func observeCacheLossDiagnostic(now time.Time, record usage.Record, info cacheLossRequestInfo) (cacheLossDiagnosticEvent, bool) {
	return globalCacheLossDiagnostics.observe(now, record, info)
}

func (s *cacheLossDiagnosticStore) observe(now time.Time, record usage.Record, info cacheLossRequestInfo) (cacheLossDiagnosticEvent, bool) {
	if record.Failed || strings.TrimSpace(info.SessionID) == "" || !cacheLossHasUsage(record.Detail) {
		return cacheLossDiagnosticEvent{}, false
	}
	key := cacheLossSessionKey(record, info)
	ratio := cacheLossRatio(record.Detail)

	s.mu.Lock()
	defer s.mu.Unlock()

	s.pruneLocked(now)
	state := s.sessions[key]
	firstRequest := state.RequestCount == 0
	state.RequestCount++

	trigger := ""
	if !firstRequest {
		if state.PreviousCacheRead > 0 && record.Detail.CacheReadTokens == 0 {
			trigger = cacheLossTriggerCacheReadZero
		} else if cacheLossTokenWindow(record.Detail) >= cacheLossMinTokenWindow && ratio < cacheLossRatioThreshold {
			trigger = cacheLossTriggerCacheRatioLow
		}
	}

	if record.Detail.CacheReadTokens == 0 {
		state.ZeroReadStreak++
	} else {
		state.ZeroReadStreak = 0
	}
	if cacheLossTokenWindow(record.Detail) >= cacheLossMinTokenWindow && ratio < cacheLossRatioThreshold {
		state.LowRatioStreak++
	} else {
		state.LowRatioStreak = 0
	}

	event := cacheLossDiagnosticEvent{}
	if trigger != "" && !cacheLossRateLimited(now, state, trigger) {
		event = buildCacheLossDiagnosticEvent(trigger, record, info, state, ratio)
		if state.LastEmittedByReason == nil {
			state.LastEmittedByReason = make(map[string]time.Time)
		}
		state.LastEmittedByReason[trigger] = now
	}

	state.PreviousCacheRead = record.Detail.CacheReadTokens
	state.PreviousCacheRatio = ratio
	state.LastSeen = now
	s.sessions[key] = state

	if trigger == "" || event.Trigger == "" {
		return cacheLossDiagnosticEvent{}, false
	}
	return event, true
}

func (s *cacheLossDiagnosticStore) pruneLocked(now time.Time) {
	if len(s.sessions) == 0 {
		return
	}
	cutoff := now.Add(-cacheLossSessionTTL)
	for key, state := range s.sessions {
		if !state.LastSeen.IsZero() && state.LastSeen.Before(cutoff) {
			delete(s.sessions, key)
		}
	}
	if len(s.sessions) <= cacheLossMaxSessions {
		return
	}
	var oldestKey string
	var oldestTime time.Time
	for key, state := range s.sessions {
		if oldestTime.IsZero() || state.LastSeen.Before(oldestTime) {
			oldestKey = key
			oldestTime = state.LastSeen
		}
	}
	if oldestKey != "" {
		delete(s.sessions, oldestKey)
	}
}

func logCacheLossDiagnostic(ctx context.Context, record usage.Record) {
	info := cacheLossRequestInfoFromContext(ctx)
	event, ok := observeCacheLossDiagnostic(time.Now(), record, info)
	if !ok {
		return
	}
	LogWithRequestID(ctx).WithFields(log.Fields{
		"event":                      "cache_loss_diagnostic",
		"trigger":                    event.Trigger,
		"session_hash":               event.SessionHash,
		"session_source":             event.SessionSource,
		"provider":                   event.Provider,
		"model":                      event.Model,
		"alias":                      event.Alias,
		"auth_hash":                  event.AuthHash,
		"source":                     event.Source,
		"reasoning_effort":           event.ReasoningEffort,
		"service_tier":               event.ServiceTier,
		"latency_ms":                 event.LatencyMS,
		"ttft_ms":                    event.TTFTMS,
		"input_tokens":               event.InputTokens,
		"cache_read_tokens":          event.CacheReadTokens,
		"cache_creation_tokens":      event.CacheCreationTokens,
		"cache_ratio":                event.CacheRatio,
		"previous_cache_read_tokens": event.PreviousCacheReadTokens,
		"previous_cache_ratio":       event.PreviousCacheRatio,
		"request_count_in_session":   event.RequestCountInSession,
		"low_ratio_streak":           event.LowRatioStreak,
		"zero_read_streak":           event.ZeroReadStreak,
		"request_shape_hash_prefix":  event.RequestShapeHashPrefix,
		"prompt_cache_key_prefix":    event.PromptCacheKeyPrefix,
		"tools_count":                event.ToolsCount,
		"input_items_count":          event.InputItemsCount,
		"has_previous_response_id":   event.HasPreviousResponseID,
		"store":                      event.StoreValue,
		"tklite_session_key_present": event.TKLiteSessionKeyPresent,
		"response_chaining_enabled":  event.ResponseChainingEnabled,
		"instructions_hash_prefix":   event.InstructionsHashPrefix,
		"metadata_session_present":   event.MetadataSessionPresent,
	}).Warn("cache loss diagnostic triggered")
}

func buildCacheLossDiagnosticEvent(trigger string, record usage.Record, info cacheLossRequestInfo, state cacheLossSessionState, ratio float64) cacheLossDiagnosticEvent {
	return cacheLossDiagnosticEvent{
		Trigger:                 trigger,
		SessionHash:             hashStringPrefix(info.SessionID),
		SessionSource:           info.SessionSource,
		Provider:                record.Provider,
		Model:                   record.Model,
		Alias:                   record.Alias,
		AuthHash:                hashStringPrefix(record.AuthID + "\x00" + record.AuthIndex + "\x00" + record.AuthType),
		Source:                  record.Source,
		ReasoningEffort:         record.ReasoningEffort,
		ServiceTier:             record.ServiceTier,
		LatencyMS:               record.Latency.Milliseconds(),
		TTFTMS:                  record.TTFT.Milliseconds(),
		InputTokens:             record.Detail.InputTokens,
		CacheReadTokens:         record.Detail.CacheReadTokens,
		CacheCreationTokens:     record.Detail.CacheCreationTokens,
		CacheRatio:              ratio,
		PreviousCacheReadTokens: state.PreviousCacheRead,
		PreviousCacheRatio:      state.PreviousCacheRatio,
		RequestCountInSession:   state.RequestCount,
		LowRatioStreak:          state.LowRatioStreak,
		ZeroReadStreak:          state.ZeroReadStreak,
		RequestShapeHashPrefix:  info.RequestShapeHashPrefix,
		PromptCacheKeyPrefix:    info.PromptCacheKeyPrefix,
		ToolsCount:              info.ToolsCount,
		InputItemsCount:         info.InputItemsCount,
		HasPreviousResponseID:   info.HasPreviousResponseID,
		StoreValue:              info.StoreValue,
		TKLiteSessionKeyPresent: info.TKLiteSessionKeyPresent,
		ResponseChainingEnabled: info.ResponseChainingEnabled,
		InstructionsHashPrefix:  info.InstructionsHashPrefix,
		MetadataSessionPresent:  info.MetadataUserIDSessionSeen,
	}
}

func cacheLossRateLimited(now time.Time, state cacheLossSessionState, trigger string) bool {
	if state.LastEmittedByReason == nil {
		return false
	}
	last := state.LastEmittedByReason[trigger]
	return !last.IsZero() && now.Sub(last) < 5*time.Minute
}

func cacheLossSessionKey(record usage.Record, info cacheLossRequestInfo) string {
	return strings.Join([]string{info.SessionID, record.Provider, record.Model, record.AuthID, record.AuthIndex, record.AuthType}, "\x00")
}

func cacheLossHasUsage(detail usage.Detail) bool {
	return detail.InputTokens > 0 || detail.OutputTokens > 0 || detail.CacheCreationTokens > 0 || detail.TotalTokens > 0
}

func cacheLossRatio(detail usage.Detail) float64 {
	window := cacheLossTokenWindow(detail)
	if window <= 0 {
		return 0
	}
	return float64(detail.CacheReadTokens) / float64(window)
}

func cacheLossTokenWindow(detail usage.Detail) int64 {
	return detail.InputTokens + detail.CacheReadTokens
}

func cacheLossSessionSource(ctx context.Context, payload []byte, headers http.Header) string {
	if headers != nil && strings.TrimSpace(headers.Get(ClaudeCodeSessionHeader)) != "" {
		return "header"
	}
	if ctx != nil {
		if ginCtx := ginContextFrom(ctx); ginCtx != nil && ginCtx.Request != nil && strings.TrimSpace(ginCtx.Request.Header.Get(ClaudeCodeSessionHeader)) != "" {
			return "header"
		}
	}
	if extractClaudeCodeSessionIDFromPayload(payload) != "" {
		return "metadata"
	}
	return "none"
}

func jsonArrayCount(body []byte, path string) int {
	value := gjson.GetBytes(body, path)
	if !value.IsArray() {
		return 0
	}
	return len(value.Array())
}

func cacheLossStoreValue(body []byte) string {
	value := gjson.GetBytes(body, "store")
	if !value.Exists() {
		return "absent"
	}
	if value.Type == gjson.True || value.Type == gjson.False {
		return strconv.FormatBool(value.Bool())
	}
	return value.String()
}

func hashJSONPrefix(body []byte) string {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return ""
	}
	return hashStringPrefix(trimmed)
}

func hashStringPrefix(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return prefixString(hex.EncodeToString(sum[:]), cacheLossHashPrefixLen)
}

func prefixString(value string, maxLen int) string {
	value = strings.TrimSpace(value)
	if len(value) <= maxLen {
		return value
	}
	return value[:maxLen]
}

func resetCacheLossDiagnosticStateForTest() {
	globalCacheLossDiagnostics.mu.Lock()
	defer globalCacheLossDiagnostics.mu.Unlock()
	globalCacheLossDiagnostics.sessions = make(map[string]cacheLossSessionState)
}
