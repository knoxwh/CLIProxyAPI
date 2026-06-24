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

func TestCacheOptDiagnosticsOptionsCaptureTKLiteFlag(t *testing.T) {
	auth := &cliproxyauth.Auth{ID: "auth-123", Attributes: map[string]string{"api_key": "sk-secret"}}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5-codex",
		Payload: []byte(`{"metadata":{"user_id":"_session_11111111-2222-3333-4444-555555555555"}}`),
	}
	headers := http.Header{}
	headers.Set(helps.ClaudeCodeSessionHeader, "session-from-header")

	options := cacheOptDiagnosticsOptions(auth, req, headers)

	if !options.TKLiteSessionKeyPresent {
		t.Fatal("expected tklite session key flag")
	}
}

func TestRecordCacheLossRequestInfoCapturesTKLiteFlag(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	auth := &cliproxyauth.Auth{ID: "auth-123", Attributes: map[string]string{"api_key": "sk-secret"}}
	req := cliproxyexecutor.Request{Payload: []byte(`{"metadata":{"user_id":"_session_11111111-2222-3333-4444-555555555555"}}`)}
	headers := http.Header{}
	headers.Set(helps.ClaudeCodeSessionHeader, "session-from-header")
	upstreamBody := []byte(`{"input":[{"type":"message"}],"prompt_cache_key":"abcdef123456"}`)

	recordCacheLossRequestInfo(ctx, auth, req, upstreamBody, req.Payload, headers)

	value, ok := ginCtx.Get("CACHE_LOSS_REQUEST_INFO")
	if !ok {
		t.Fatal("expected cache-loss request info in gin context")
	}
	got := reflect.ValueOf(value)
	if !got.FieldByName("TKLiteSessionKeyPresent").Bool() {
		t.Fatal("expected tklite session key flag")
	}
}

func TestRecordCacheLossRequestInfoStoresSafeBreadcrumbs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	auth := &cliproxyauth.Auth{ID: "auth-123", Attributes: map[string]string{"api_key": "sk-secret"}}
	req := cliproxyexecutor.Request{Payload: []byte(`{"metadata":{"user_id":"_session_11111111-2222-3333-4444-555555555555"}}`)}
	headers := http.Header{}
	headers.Set(helps.ClaudeCodeSessionHeader, "session-from-header")
	upstreamBody := []byte(`{"input":[{"type":"message"}],"tools":[{"type":"function","name":"search"}],"prompt_cache_key":"abcdef123456","store":false,"previous_response_id":"resp-1"}`)

	recordCacheLossRequestInfo(ctx, auth, req, upstreamBody, req.Payload, headers)

	value, ok := ginCtx.Get("CACHE_LOSS_REQUEST_INFO")
	if !ok {
		t.Fatal("expected cache-loss request info in gin context")
	}
	got := reflect.ValueOf(value)
	if got.FieldByName("SessionID").String() != "session-from-header" {
		t.Fatalf("session id = %q, want header session", got.FieldByName("SessionID").String())
	}
	if !got.FieldByName("TKLiteSessionKeyPresent").Bool() {
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

func TestCacheOptPostTKLiteStripsChainingFields(t *testing.T) {
	body := []byte(`{"prompt_cache_retention":"24h","previous_response_id":"must-drop","store":true,"input":[{"role":"user","content":"hi"}]}`)

	result := CacheOptPostTKLite(body)

	if gjson.GetBytes(result, "prompt_cache_retention").Exists() {
		t.Fatal("prompt_cache_retention should be deleted")
	}
	if gjson.GetBytes(result, "previous_response_id").Exists() {
		t.Fatalf("previous_response_id should be dropped: %s", result)
	}
	if gjson.GetBytes(result, "store").Bool() != false {
		t.Fatal("store should be forced to false")
	}
}
