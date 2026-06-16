package executor

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

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
	if !strings.HasPrefix(key, "codex:") {
		t.Fatalf("key prefix = %q", key)
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

	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "sk-test"}}
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
		Attributes: map[string]string{"api_key": "sk-test"},
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

func TestCacheOptFallsBackToSessionCache(t *testing.T) {
	body := []byte(`{"input":[{"role":"user","content":"hi"}]}`)
	auth := &cliproxyauth.Auth{
		ID:         "auth-789",
		Attributes: map[string]string{"api_key": "sk-test"},
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
