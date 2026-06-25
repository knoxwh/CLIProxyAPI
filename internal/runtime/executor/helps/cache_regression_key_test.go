package helps

import (
	"context"
	"net/http"
	"testing"
)

func TestHashSystemPrompt_StableAndDistinct(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"You are Claude."},{"type":"text","text":"Be concise."}]}`)
	h1 := HashSystemPrompt(body)
	h2 := HashSystemPrompt(body)
	if h1 != h2 {
		t.Fatalf("hash not stable: %s vs %s", h1, h2)
	}
	body2 := []byte(`{"system":[{"type":"text","text":"You are Claude."},{"type":"text","text":"Be verbose."}]}`)
	if HashSystemPrompt(body2) == h1 {
		t.Fatal("hash should differ when system text changes")
	}
}

func TestHashSystemPrompt_StringForm(t *testing.T) {
	if HashSystemPrompt([]byte(`{"system":"hello"}`)) != HashSystemPrompt([]byte(`{"system":"hello"}`)) {
		t.Fatal("string system form not stable")
	}
}

func TestHashSystemPrompt_Empty(t *testing.T) {
	if HashSystemPrompt([]byte(`{}`)) == "" {
		t.Fatal("empty system should still yield a hash, not empty string")
	}
}

func TestCacheRegressionKey_SessionFromHeader(t *testing.T) {
	hdr := http.Header{}
	hdr.Set(ClaudeCodeSessionHeader, "sess-123")
	body := []byte(`{"system":"x"}`)
	rc, ok := CacheRegressionKey(context.Background(), body, hdr, nil)
	if !ok || rc.SessionID != "sess-123" {
		t.Fatalf("expected ok sess-123, got %+v ok=%v", rc, ok)
	}
	if rc.SystemHash == "" || rc.Key == "" {
		t.Fatal("key and systemhash must be populated")
	}
	if rc.AuthID != "" {
		t.Fatal("nil auth => empty AuthID")
	}
}

func TestCacheRegressionKey_NoSession(t *testing.T) {
	rc, ok := CacheRegressionKey(context.Background(), []byte(`{}`), nil, nil)
	if ok || rc.Key != "" {
		t.Fatalf("expected not ok when no session, got %+v", rc)
	}
}
