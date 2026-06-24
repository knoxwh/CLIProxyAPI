package executor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

func TestCacheOptTKLiteSessionKeyIsOpaque(t *testing.T) {
	auth := &cliproxyauth.Auth{
		ID: "auth-123",
		Attributes: map[string]string{
			"api_key":  "sk-secret",
			"base_url": "https://example.test/v1",
		},
	}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5-codex",
		Payload: []byte(`{"metadata":{"user_id":"{\"session_id\":\"session-abc\"}"}}`),
	}

	key := CacheOptTKLiteSessionKey(auth, req)

	if key == "" {
		t.Fatal("expected tklite session key")
	}
	for _, raw := range []string{"auth-123", "sk-secret", "example.test", "session-abc"} {
		if strings.Contains(key, raw) {
			t.Fatalf("key leaks raw value %q: %q", raw, key)
		}
	}
	if !strings.HasPrefix(key, "cpa:") {
		t.Fatalf("key prefix = %q", key)
	}
}

func TestCacheOptDiagnosticsOptionsCaptureTKLiteAndChainingFlags(t *testing.T) {
	auth := &cliproxyauth.Auth{ID: "auth-123", Attributes: map[string]string{"api_key": "sk-secret", cliproxyauth.AttributeEnableResponseChaining: "true"}}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5-codex",
		Payload: []byte(`{"metadata":{"user_id":"_session_11111111-2222-3333-4444-555555555555"}}`),
	}
	headers := http.Header{}
	headers.Set(helps.ClaudeCodeSessionHeader, "session-from-header")

	options := cacheOptDiagnosticsOptions(auth, req, headers, true)

	if !options.TKLiteSessionKeyPresent {
		t.Fatal("expected tklite session key flag")
	}
	if !options.ResponseChainingEnabled {
		t.Fatal("expected response chaining flag")
	}
}

func TestRecordResponsesCacheLossRequestInfoCanDisableResponseChainingFlag(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	auth := &cliproxyauth.Auth{ID: "auth-123", Attributes: map[string]string{"api_key": "sk-secret", cliproxyauth.AttributeEnableResponseChaining: "true"}}
	req := cliproxyexecutor.Request{Payload: []byte(`{"metadata":{"user_id":"_session_11111111-2222-3333-4444-555555555555"}}`)}
	headers := http.Header{}
	headers.Set(helps.ClaudeCodeSessionHeader, "session-from-header")
	upstreamBody := []byte(`{"input":[{"type":"message"}],"prompt_cache_key":"abcdef123456"}`)

	recordResponsesCacheLossRequestInfo(ctx, auth, req, upstreamBody, req.Payload, headers, false)

	value, ok := ginCtx.Get("CACHE_LOSS_REQUEST_INFO")
	if !ok {
		t.Fatal("expected cache-loss request info in gin context")
	}
	got := reflect.ValueOf(value)
	if !got.FieldByName("TKLiteSessionKeyPresent").Bool() {
		t.Fatal("expected tklite session key flag")
	}
	if got.FieldByName("ResponseChainingEnabled").Bool() {
		t.Fatal("generic responses path must not inherit Codex response chaining flag")
	}
}

func TestRecordCodexCacheLossRequestInfoStoresSafeBreadcrumbs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	auth := &cliproxyauth.Auth{ID: "auth-123", Attributes: map[string]string{"api_key": "sk-secret", cliproxyauth.AttributeEnableResponseChaining: "true"}}
	req := cliproxyexecutor.Request{Payload: []byte(`{"metadata":{"user_id":"_session_11111111-2222-3333-4444-555555555555"}}`)}
	headers := http.Header{}
	headers.Set(helps.ClaudeCodeSessionHeader, "session-from-header")
	upstreamBody := []byte(`{"input":[{"type":"message"}],"tools":[{"type":"function","name":"search"}],"prompt_cache_key":"abcdef123456","store":true,"previous_response_id":"resp-1"}`)

	recordCodexCacheLossRequestInfo(ctx, auth, req, upstreamBody, req.Payload, headers)

	value, ok := ginCtx.Get("CACHE_LOSS_REQUEST_INFO")
	if !ok {
		t.Fatal("expected cache-loss request info in gin context")
	}
	got := reflect.ValueOf(value)
	if got.FieldByName("SessionID").String() != "session-from-header" {
		t.Fatalf("session id = %q, want header session", got.FieldByName("SessionID").String())
	}
	if !got.FieldByName("TKLiteSessionKeyPresent").Bool() || !got.FieldByName("ResponseChainingEnabled").Bool() {
		t.Fatalf("diagnostic flags missing: %#v", value)
	}
	if got.FieldByName("PromptCacheKeyPrefix").String() == "abcdef12" {
		t.Fatalf("prompt cache key prefix leaked raw prefix: %q", got.FieldByName("PromptCacheKeyPrefix").String())
	}
}

func TestCacheOptTKLiteHeadersDoesNotMutateOriginal(t *testing.T) {
	original := http.Header{}
	original.Set("x-request-id", "req-1")
	auth := &cliproxyauth.Auth{ID: "auth-123", Attributes: map[string]string{"api_key": "sk-secret"}}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5-codex",
		Payload: []byte(`{"metadata":{"user_id":"{\"session_id\":\"session-abc\"}"}}`),
	}

	cloned := CacheOptTKLiteHeaders(auth, req, original)

	if fmt.Sprintf("%p", cloned) == fmt.Sprintf("%p", original) {
		t.Fatal("headers must be cloned")
	}
	if cloned.Get("x-tklite-session-key") == "" {
		t.Fatal("expected tklite session key on cloned headers")
	}
	if original.Get("x-tklite-session-key") != "" {
		t.Fatal("original headers must not be mutated")
	}
	if cloned.Get("x-request-id") != "req-1" {
		t.Fatal("existing safe headers must be preserved")
	}
}

func TestStaleResponseIDCleared(t *testing.T) {
	sessionKey := "test-stale-clear"
	helps.SetSessionResponseID(sessionKey, "stale-resp-123")

	if _, ok := helps.GetSessionResponseID(sessionKey); !ok {
		t.Fatal("precondition failed: response_id should exist")
	}

	helps.DeleteSessionResponseID(sessionKey)

	if _, ok := helps.GetSessionResponseID(sessionKey); ok {
		t.Fatal("stale response_id should be cleared")
	}
}

func TestClientPreviousResponseIDRespected(t *testing.T) {
	originalPayload := []byte(`{"previous_response_id":"client-chosen-id","input":[{"role":"user","content":"hi"}]}`)
	body := []byte(`{"input":[{"role":"user","content":"hi"}]}`)

	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "sk-test", cliproxyauth.AttributeEnableResponseChaining: "true"}}
	req := cliproxyexecutor.Request{Payload: originalPayload}
	result := CacheOptPostTKLite(auth, body, req, originalPayload)

	if got := gjson.GetBytes(result, "previous_response_id").String(); got != "client-chosen-id" {
		t.Fatalf("previous_response_id = %q, want client-chosen-id", got)
	}
	if gjson.GetBytes(result, "store").Bool() != true {
		t.Fatal("API key path should set store=true")
	}
}

func TestClientEmptyResponseIDFallsBackToSessionCache(t *testing.T) {
	body := []byte(`{"input":[{"role":"user","content":"hi"}]}`)
	auth := &cliproxyauth.Auth{
		ID:         "auth-empty",
		Attributes: map[string]string{"api_key": "sk-test", cliproxyauth.AttributeEnableResponseChaining: "true"},
	}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5-codex",
		Payload: []byte(`{"metadata":{"user_id":"_session_11111111-2222-3333-4444-555555555555"}}`),
	}

	sessionKey := cacheOptSessionResponseKey(auth, req)
	helps.SetSessionResponseID(sessionKey, "cached-resp-from-session")
	defer helps.DeleteSessionResponseID(sessionKey)

	// Client sends an explicit empty previous_response_id.
	originalPayload := []byte(`{"previous_response_id":"","input":[{"role":"user","content":"hi"}]}`)
	result := CacheOptPostTKLite(auth, body, req, originalPayload)

	// Empty client value should be treated as "no explicit intent" and fall
	// back to the cached session response_id.
	if got := gjson.GetBytes(result, "previous_response_id").String(); got != "cached-resp-from-session" {
		t.Fatalf("previous_response_id = %q, want cached-resp-from-session", got)
	}
}

func TestSessionResponseIDRoundTrip(t *testing.T) {
	sessionKey := "round-trip-session"

	// First request: empty cache.
	if _, ok := helps.GetSessionResponseID(sessionKey); ok {
		t.Fatal("first request should have no previous_response_id")
	}

	// First response completed: store response.id.
	helps.SetSessionResponseID(sessionKey, "resp-001")

	// Second request: retrieve stored id.
	respID, ok := helps.GetSessionResponseID(sessionKey)
	if !ok || respID != "resp-001" {
		t.Fatalf("second request should get resp-001, got %q", respID)
	}

	// Second response completed: overwrite with new id.
	helps.SetSessionResponseID(sessionKey, "resp-002")

	// Third request: retrieve new id.
	respID, ok = helps.GetSessionResponseID(sessionKey)
	if !ok || respID != "resp-002" {
		t.Fatalf("third request should get resp-002, got %q", respID)
	}

	// Cleanup.
	helps.DeleteSessionResponseID(sessionKey)
}

func TestOAuthPathDeletesPreviousResponseID(t *testing.T) {
	body := []byte(`{"previous_response_id":"should-be-deleted","input":[{"role":"user","content":"hi"}]}`)
	// Non-API-key auth (no api_key attribute) so the OAuth branch is taken.
	auth := &cliproxyauth.Auth{Attributes: map[string]string{}}
	req := cliproxyexecutor.Request{}

	result := CacheOptPostTKLite(auth, body, req, body)

	if gjson.GetBytes(result, "previous_response_id").Exists() {
		t.Fatal("OAuth path should delete previous_response_id")
	}
	if gjson.GetBytes(result, "store").Bool() != false {
		t.Fatal("OAuth path should set store=false")
	}
}

func TestAPIKeyPathDefaultsToNoResponseChaining(t *testing.T) {
	body := []byte(`{"prompt_cache_retention":"24h","previous_response_id":"must-drop","input":[{"role":"user","content":"hi"}]}`)
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "sk-test"}}
	req := cliproxyexecutor.Request{}

	result := CacheOptPostTKLite(auth, body, req, body)

	if gjson.GetBytes(result, "prompt_cache_retention").Exists() {
		t.Fatal("API key path should delete prompt_cache_retention")
	}
	if gjson.GetBytes(result, "previous_response_id").Exists() {
		t.Fatalf("API key path should drop previous_response_id by default: %s", result)
	}
	if gjson.GetBytes(result, "store").Bool() != false {
		t.Fatal("API key path should set store=false by default")
	}
}

func TestCacheOptStoreResponseIDReadsCompactTopLevelID(t *testing.T) {
	auth := &cliproxyauth.Auth{
		ID:         "auth-compact-id",
		Attributes: map[string]string{"api_key": "sk-test", cliproxyauth.AttributeEnableResponseChaining: "true"},
	}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5-codex",
		Payload: []byte(`{"metadata":{"user_id":"_session_33333333-4444-5555-6666-777777777777"}}`),
	}
	sessionKey := cacheOptSessionResponseKey(auth, req)
	helps.DeleteSessionResponseID(sessionKey)
	defer helps.DeleteSessionResponseID(sessionKey)

	CacheOptStoreResponseID(auth, req, []byte(`{"id":"resp-compact-123","object":"response"}`))

	if got, ok := helps.GetSessionResponseID(sessionKey); !ok || got != "resp-compact-123" {
		t.Fatalf("stored response_id = %q, ok=%v; want resp-compact-123", got, ok)
	}
}

func TestCacheOptStoreResponseIDNoopsUnlessChainingEnabled(t *testing.T) {
	auth := &cliproxyauth.Auth{
		ID:         "auth-default-off",
		Attributes: map[string]string{"api_key": "sk-test"},
	}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5-codex",
		Payload: []byte(`{"metadata":{"user_id":"_session_99999999-8888-7777-6666-555555555555"}}`),
	}
	sessionKey := cacheOptSessionResponseKey(auth, req)
	helps.DeleteSessionResponseID(sessionKey)

	CacheOptStoreResponseID(auth, req, []byte(`{"response":{"id":"resp-default-off"}}`))

	if got, ok := helps.GetSessionResponseID(sessionKey); ok {
		t.Fatalf("default API key path should not store response_id, got %q", got)
	}
}

func TestCacheOptFallsBackToSessionCache(t *testing.T) {
	body := []byte(`{"input":[{"role":"user","content":"hi"}]}`)
	auth := &cliproxyauth.Auth{
		ID:         "auth-789",
		Attributes: map[string]string{"api_key": "sk-test", cliproxyauth.AttributeEnableResponseChaining: "true"},
	}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5-codex",
		Payload: []byte(`{"metadata":{"user_id":"_session_aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"}}`),
	}

	// Use the same key derivation as production code.
	sessionKey := cacheOptSessionResponseKey(auth, req)
	if sessionKey == "" {
		t.Fatal("expected derived session key")
	}
	helps.SetSessionResponseID(sessionKey, "cached-resp-789")

	result := CacheOptPostTKLite(auth, body, req, body)

	if got := gjson.GetBytes(result, "previous_response_id").String(); got != "cached-resp-789" {
		t.Fatalf("previous_response_id = %q, want cached-resp-789", got)
	}

	helps.DeleteSessionResponseID(sessionKey)
}
