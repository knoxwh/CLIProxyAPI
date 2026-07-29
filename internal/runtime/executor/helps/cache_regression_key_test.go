package helps

import (
	"context"
	"net/http"
	"strings"
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

func TestCacheRegressionKeyOpenAI_PromptCacheKeyBucket(t *testing.T) {
	body := []byte(`{"prompt_cache_key":"pck-abc","messages":[{"role":"system","content":"You are GLM."},{"role":"user","content":"hi"}]}`)
	rc, ok := CacheRegressionKeyOpenAI(context.Background(), body, nil, nil)
	if !ok {
		t.Fatal("expected ok when prompt_cache_key present")
	}
	if rc.SessionID != "pck-abc" {
		t.Fatalf("SessionID = %q, want prompt_cache_key pck-abc", rc.SessionID)
	}
	if !strings.Contains(rc.Key, "pck-abc") {
		t.Fatalf("Key %q must contain prompt_cache_key", rc.Key)
	}
	if rc.SystemHash == "" {
		t.Fatal("SystemHash must be derived from system message")
	}
}

func TestCacheRegressionKeyOpenAI_FallbackSessionFromMetadata(t *testing.T) {
	body := []byte(`{"metadata":{"user_id":"user_session_11111111-2222-3333-4444-555555555555"},"messages":[{"role":"user","content":"hi"}]}`)
	rc, ok := CacheRegressionKeyOpenAI(context.Background(), body, nil, nil)
	if !ok {
		t.Fatal("expected ok when metadata.user_id session present")
	}
	if rc.SessionID != "11111111-2222-3333-4444-555555555555" {
		t.Fatalf("SessionID = %q, want extracted session uuid", rc.SessionID)
	}
}

func TestCacheRegressionKeyOpenAI_NoBucket(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	rc, ok := CacheRegressionKeyOpenAI(context.Background(), body, nil, nil)
	if ok || rc.Key != "" {
		t.Fatalf("expected not ok when no prompt_cache_key and no session, got %+v", rc)
	}
}

func TestCacheRegressionKeyOpenAI_SystemHashStable(t *testing.T) {
	body1 := []byte(`{"prompt_cache_key":"k","messages":[{"role":"system","content":"S"},{"role":"user","content":"a"}]}`)
	body2 := []byte(`{"prompt_cache_key":"k","messages":[{"role":"system","content":"S"},{"role":"user","content":"b"}]}`)
	h1, _ := CacheRegressionKeyOpenAI(context.Background(), body1, nil, nil)
	h2, _ := CacheRegressionKeyOpenAI(context.Background(), body2, nil, nil)
	if h1.SystemHash != h2.SystemHash {
		t.Fatalf("SystemHash must be stable when only non-system messages change: %s vs %s", h1.SystemHash, h2.SystemHash)
	}
}

func TestCacheRegressionKeyOpenAI_InstructionsHash(t *testing.T) {
	body := []byte(`{"prompt_cache_key":"k","instructions":"be concise"}`)
	rc, ok := CacheRegressionKeyOpenAI(context.Background(), body, nil, nil)
	if !ok || rc.SystemHash == "" {
		t.Fatalf("expected instructions to yield SystemHash, got %+v ok=%v", rc, ok)
	}
}
