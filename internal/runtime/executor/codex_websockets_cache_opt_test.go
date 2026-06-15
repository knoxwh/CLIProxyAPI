package executor

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexWebsocketsAPIKeyAuthSetsStoreTrue(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	capturedPayload := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("upgrade websocket: %v", err)
		}
		defer func() { _ = conn.Close() }()

		msgType, payload, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read upstream websocket message: %v", err)
		}
		if msgType != websocket.TextMessage {
			t.Fatalf("message type = %d, want text", msgType)
		}
		capturedPayload <- bytes.Clone(payload)

		completed := []byte(`{"type":"response.completed","response":{"id":"resp-store-test","output":[],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`)
		if errWrite := conn.WriteMessage(websocket.TextMessage, completed); errWrite != nil {
			t.Fatalf("write completed websocket message: %v", errWrite)
		}
	}))
	defer server.Close()

	exec := NewCodexWebsocketsExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
	auth := &cliproxyauth.Auth{ID: "auth-store-test", Attributes: map[string]string{"api_key": "sk-test", "base_url": server.URL}}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5-codex",
		Payload: []byte(`{"model":"gpt-5-codex","input":[{"type":"message","role":"user","content":"hello"}]}`),
	}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("codex")}

	if _, err := exec.Execute(context.Background(), auth, req, opts); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	select {
	case payload := <-capturedPayload:
		if got := gjson.GetBytes(payload, "store").Bool(); !got {
			t.Fatalf("store = %v, want true; payload=%s", got, payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for upstream websocket payload")
	}
}

func TestCodexWebsocketsOAuthAuthSetsStoreFalse(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	capturedPayload := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("upgrade websocket: %v", err)
		}
		defer func() { _ = conn.Close() }()

		msgType, payload, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read upstream websocket message: %v", err)
		}
		if msgType != websocket.TextMessage {
			t.Fatalf("message type = %d, want text", msgType)
		}
		capturedPayload <- bytes.Clone(payload)

		completed := []byte(`{"type":"response.completed","response":{"id":"resp-oauth-test","output":[],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`)
		if errWrite := conn.WriteMessage(websocket.TextMessage, completed); errWrite != nil {
			t.Fatalf("write completed websocket message: %v", errWrite)
		}
	}))
	defer server.Close()

	exec := NewCodexWebsocketsExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
	// OAuth auth: no api_key attribute, uses email metadata
	auth := &cliproxyauth.Auth{
		ID:       "auth-oauth-test",
		Provider: "codex",
		Metadata: map[string]any{"email": "user@example.com"},
		Attributes: map[string]string{
			"base_url": server.URL,
		},
	}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5-codex",
		Payload: []byte(`{"model":"gpt-5-codex","input":[{"type":"message","role":"user","content":"hello"}]}`),
	}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("codex")}

	if _, err := exec.Execute(context.Background(), auth, req, opts); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	select {
	case payload := <-capturedPayload:
		if got := gjson.GetBytes(payload, "store").Bool(); got {
			t.Fatalf("store = %v, want false for OAuth; payload=%s", got, payload)
		}
		// OAuth path must not inject previous_response_id
		if gjson.GetBytes(payload, "previous_response_id").Exists() {
			t.Fatalf("OAuth path must not have previous_response_id; payload=%s", payload)
		}
		// OAuth path must delete prompt_cache_retention
		if gjson.GetBytes(payload, "prompt_cache_retention").Exists() {
			t.Fatalf("OAuth path must not have prompt_cache_retention; payload=%s", payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for upstream websocket payload")
	}
}

func TestCodexWebsocketsAPIKeyAuthInjectsPreviousResponseIDFromSessionMap(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	capturedPayload := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("upgrade websocket: %v", err)
		}
		defer func() { _ = conn.Close() }()

		msgType, payload, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read upstream websocket message: %v", err)
		}
		if msgType != websocket.TextMessage {
			t.Fatalf("message type = %d, want text", msgType)
		}
		capturedPayload <- bytes.Clone(payload)

		completed := []byte(`{"type":"response.completed","response":{"id":"resp-new-turn","output":[],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`)
		if errWrite := conn.WriteMessage(websocket.TextMessage, completed); errWrite != nil {
			t.Fatalf("write completed websocket message: %v", errWrite)
		}
	}))
	defer server.Close()

	exec := NewCodexWebsocketsExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
	auth := &cliproxyauth.Auth{ID: "auth-chaining-test", Attributes: map[string]string{"api_key": "sk-test", "base_url": server.URL}}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5-codex",
		Payload: []byte(`{"model":"gpt-5-codex","metadata":{"user_id":"{\"session_id\":\"chain-session-1\"}"},"input":[{"type":"message","role":"user","content":"follow-up"}]}`),
	}

	// Use cacheOptSessionResponseKey to compute the correct key —
	// same function used by CacheOptPostTKLite internally.
	sessionKey := cacheOptSessionResponseKey(auth, req)
	if sessionKey == "" {
		t.Skip("no session key derivable from this payload")
	}
	helps.SetSessionResponseID(sessionKey, "resp-prior-turn")

	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("codex")}

	if _, err := exec.Execute(context.Background(), auth, req, opts); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	select {
	case payload := <-capturedPayload:
		if got := gjson.GetBytes(payload, "previous_response_id").String(); got != "resp-prior-turn" {
			t.Fatalf("previous_response_id = %s, want resp-prior-turn; payload=%s", got, payload)
		}
		if got := gjson.GetBytes(payload, "store").Bool(); !got {
			t.Fatalf("store = %v, want true; payload=%s", got, payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for upstream websocket payload")
	}
}

func TestCodexWebsocketsClientPreviousResponseIDNotOverridden(t *testing.T) {
	// Pre-populate session map with a different response ID.
	sessionKey := "auth-id:auth-override-test:" + extractClaudeCodeSessionIDForCodexReplay([]byte(`{"metadata":{"user_id":"{\"session_id\":\"override-session-1\"}"}}`))
	helps.SetSessionResponseID(sessionKey, "resp-from-map")

	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	capturedPayload := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("upgrade websocket: %v", err)
		}
		defer func() { _ = conn.Close() }()

		msgType, payload, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read upstream websocket message: %v", err)
		}
		if msgType != websocket.TextMessage {
			t.Fatalf("message type = %d, want text", msgType)
		}
		capturedPayload <- bytes.Clone(payload)

		completed := []byte(`{"type":"response.completed","response":{"id":"resp-client-override","output":[],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`)
		if errWrite := conn.WriteMessage(websocket.TextMessage, completed); errWrite != nil {
			t.Fatalf("write completed websocket message: %v", errWrite)
		}
	}))
	defer server.Close()

	exec := NewCodexWebsocketsExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
	auth := &cliproxyauth.Auth{ID: "auth-override-test", Attributes: map[string]string{"api_key": "sk-test", "base_url": server.URL}}
	// Client explicitly provides previous_response_id — should NOT be overridden by session map.
	req := cliproxyexecutor.Request{
		Model:   "gpt-5-codex",
		Payload: []byte(`{"model":"gpt-5-codex","previous_response_id":"resp-client-provided","metadata":{"user_id":"{\"session_id\":\"override-session-1\"}"},"input":[{"type":"message","role":"user","content":"hello"}]}`),
	}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("codex")}

	if _, err := exec.Execute(context.Background(), auth, req, opts); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	select {
	case payload := <-capturedPayload:
		if got := gjson.GetBytes(payload, "previous_response_id").String(); got != "resp-client-provided" {
			t.Fatalf("client previous_response_id was overridden: got %s, want resp-client-provided; payload=%s", got, payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for upstream websocket payload")
	}
}

func TestCodexWebsocketsStoreResponseIDAfterCompleted(t *testing.T) {
	// First request: no previous_response_id in session map.
	// After response.completed, session map should have the response.id.
	// Second request should get that response.id injected.

	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	var capturedPayloads [][]byte
	payloadMu := make(chan struct{}, 1)
	completedResp := []byte(`{"type":"response.completed","response":{"id":"resp-store-after-completed","output":[],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("upgrade websocket: %v", err)
		}
		defer func() { _ = conn.Close() }()
		// Read request payload
		_, payload, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read message: %v", err)
		}
		capturedPayloads = append(capturedPayloads, bytes.Clone(payload))
		payloadMu <- struct{}{}

		// Send response.completed
		if errWrite := conn.WriteMessage(websocket.TextMessage, completedResp); errWrite != nil {
			t.Fatalf("write completed: %v", errWrite)
		}
	}))
	defer server.Close()

	exec := NewCodexWebsocketsExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
	auth := &cliproxyauth.Auth{ID: "auth-store-after", Attributes: map[string]string{"api_key": "sk-test", "base_url": server.URL}}
	reqPayload := []byte(`{"model":"gpt-5-codex","metadata":{"user_id":"{\"session_id\":\"store-after-session\"}"},"input":[{"type":"message","role":"user","content":"hello"}]}`)

	req := cliproxyexecutor.Request{Model: "gpt-5-codex", Payload: reqPayload}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("codex")}

	// First request
	if _, err := exec.Execute(context.Background(), auth, req, opts); err != nil {
		t.Fatalf("first Execute() error = %v", err)
	}
	<-payloadMu // wait for first payload capture

	// Second request — should have previous_response_id from first turn's response.id
	if _, err := exec.Execute(context.Background(), auth, req, opts); err != nil {
		t.Fatalf("second Execute() error = %v", err)
	}
	<-payloadMu // wait for second payload capture

	if len(capturedPayloads) < 2 {
		t.Fatalf("expected 2 captured payloads, got %d", len(capturedPayloads))
	}

	// First request: no previous_response_id (first turn)
	if gjson.GetBytes(capturedPayloads[0], "previous_response_id").Exists() {
		t.Fatalf("first request should not have previous_response_id; payload=%s", capturedPayloads[0])
	}

	// Second request: previous_response_id from first turn's response.id
	if got := gjson.GetBytes(capturedPayloads[1], "previous_response_id").String(); got != "resp-store-after-completed" {
		t.Fatalf("second request previous_response_id = %s, want resp-store-after-completed; payload=%s", got, capturedPayloads[1])
	}
}

func TestCodexWebsocketsDownstreamWSStoreResponseIDAfterCompleted(t *testing.T) {
	delta := []byte(`{"type":"response.output_text.delta","delta":"hello"}`)
	completedResp := []byte(`{"type":"response.completed","response":{"id":"resp-downstream-store","output":[],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`)

	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	var capturedPayload []byte
	payloadCh := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("upgrade websocket: %v", err)
		}
		defer func() { _ = conn.Close() }()
		_, payload, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read message: %v", err)
		}
		capturedPayload = bytes.Clone(payload)
		payloadCh <- struct{}{}

		if errWrite := conn.WriteMessage(websocket.TextMessage, delta); errWrite != nil {
			t.Fatalf("write delta: %v", errWrite)
		}
		if errWrite := conn.WriteMessage(websocket.TextMessage, completedResp); errWrite != nil {
			t.Fatalf("write completed: %v", errWrite)
		}
	}))
	defer server.Close()

	exec := NewCodexWebsocketsExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
	auth := &cliproxyauth.Auth{ID: "auth-downstream-store", Attributes: map[string]string{"api_key": "sk-test", "base_url": server.URL}}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5-codex",
		Payload: []byte(`{"model":"gpt-5-codex","metadata":{"user_id":"{\"session_id\":\"downstream-session\"}"},"input":[{"type":"message","role":"user","content":"hello"}]}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FromString("openai-response"),
		ResponseFormat: sdktranslator.FromString("openai-response"),
	}
	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())

	result, err := exec.ExecuteStream(ctx, auth, req, opts)
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	// Drain stream chunks
	for range result.Chunks {
	}

	// Verify session map has the response.id from downstream WS path
	sessionKey := cacheOptSessionResponseKey(auth, req)
	if sessionKey == "" {
		t.Skip("no session key derivable — cannot verify downstream WS storage")
	}
	storedID, ok := helps.GetSessionResponseID(sessionKey)
	if !ok || storedID != "resp-downstream-store" {
		t.Fatalf("session map storedID = %s, ok = %v; want resp-downstream-store, true; capturedPayload=%s", storedID, ok, capturedPayload)
	}
}
