package executor

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
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
