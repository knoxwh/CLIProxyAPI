package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexExecutorRetryFailureAfterInjectedPreviousResponseIDReturnsErrorWithoutPanic(t *testing.T) {
	var bodies [][]byte
	generic400Sent := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, append([]byte(nil), body...))
		if gjson.GetBytes(body, "previous_response_id").String() != "" && !generic400Sent {
			generic400Sent = true
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"bad request"}}`))
			return
		}
		if generic400Sent {
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("response writer does not support hijacking")
			}
			conn, _, errHijack := hijacker.Hijack()
			if errHijack != nil {
				t.Fatalf("hijack retry connection: %v", errHijack)
			}
			_ = conn.Close()
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","created_at":0,"status":"completed","background":false,"error":null}}

`))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
		cliproxyauth.AttributeEnableResponseChaining: "true",
	}}
	headers := http.Header{}
	headers.Set(helps.ClaudeCodeSessionHeader, "66666666-7777-8888-9999-000000000000")
	req := cliproxyexecutor.Request{Model: "gpt-5.4", Payload: []byte(`{"model":"gpt-5.4","input":"hello"}`)}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response"), Headers: headers}

	if _, err := executor.Execute(context.Background(), auth, req, opts); err != nil {
		t.Fatalf("first Execute error: %v", err)
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("retry failure panicked: %v", recovered)
		}
	}()
	if _, err := executor.Execute(context.Background(), auth, req, opts); err == nil {
		t.Fatal("second Execute should return retry error")
	}
	if len(bodies) != 3 {
		t.Fatalf("requests = %d, want 3", len(bodies))
	}
	if gjson.GetBytes(bodies[2], "previous_response_id").Exists() {
		t.Fatalf("failed retry should clear previous_response_id: %s", bodies[2])
	}
}

func TestCodexExecutorDoesNotRetryGeneric400ForClientPreviousResponseID(t *testing.T) {
	var bodies [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, append([]byte(nil), body...))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"bad request"}}`))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
		cliproxyauth.AttributeEnableResponseChaining: "true",
	}}
	headers := http.Header{}
	headers.Set(helps.ClaudeCodeSessionHeader, "77777777-8888-9999-0000-111111111111")
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","input":"hello","previous_response_id":"client_resp"}`),
	}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response"), Headers: headers}

	if _, err := executor.Execute(context.Background(), auth, req, opts); err == nil {
		t.Fatal("Execute should return original 400")
	}
	if len(bodies) != 1 {
		t.Fatalf("requests = %d, want no retry", len(bodies))
	}
	if got := gjson.GetBytes(bodies[0], "previous_response_id").String(); got != "client_resp" {
		t.Fatalf("previous_response_id = %q, want client_resp; body=%s", got, bodies[0])
	}
}

func TestCodexExecutorRetriesGeneric400AfterInjectedPreviousResponseID(t *testing.T) {
	var bodies [][]byte
	generic400Sent := false
	successID := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, append([]byte(nil), body...))
		if gjson.GetBytes(body, "previous_response_id").String() != "" && !generic400Sent {
			generic400Sent = true
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"bad request"}}`))
			return
		}
		successID++
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, `data: {"type":"response.completed","response":{"id":"resp_%d","object":"response","created_at":0,"status":"completed","background":false,"error":null}}

`, successID)
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
		cliproxyauth.AttributeEnableResponseChaining: "true",
	}}
	headers := http.Header{}
	headers.Set(helps.ClaudeCodeSessionHeader, "55555555-6666-7777-8888-999999999999")
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","input":"hello"}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Headers:      headers,
	}

	if _, err := executor.Execute(context.Background(), auth, req, opts); err != nil {
		t.Fatalf("first Execute error: %v", err)
	}
	if _, err := executor.Execute(context.Background(), auth, req, opts); err != nil {
		t.Fatalf("second Execute should recover from generic 400: %v", err)
	}
	if _, err := executor.Execute(context.Background(), auth, req, opts); err != nil {
		t.Fatalf("third Execute error: %v", err)
	}
	if len(bodies) != 4 {
		t.Fatalf("requests = %d, want 4", len(bodies))
	}
	if gjson.GetBytes(bodies[0], "previous_response_id").Exists() {
		t.Fatalf("first request should not send previous_response_id: %s", bodies[0])
	}
	if got := gjson.GetBytes(bodies[1], "previous_response_id").String(); got != "resp_1" {
		t.Fatalf("second first attempt previous_response_id = %q, want resp_1; body=%s", got, bodies[1])
	}
	if gjson.GetBytes(bodies[2], "previous_response_id").Exists() {
		t.Fatalf("retry should clear previous_response_id: %s", bodies[2])
	}
	if got := gjson.GetBytes(bodies[3], "previous_response_id").String(); got != "resp_2" {
		t.Fatalf("third request previous_response_id = %q, want resp_2; body=%s", got, bodies[3])
	}
}

func TestCodexExecutorResponseChainingUsesClaudeCodeSessionHeader(t *testing.T) {
	var bodies [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, append([]byte(nil), body...))
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_` + string(rune('0'+len(bodies))) + `","object":"response","created_at":0,"status":"completed","background":false,"error":null}}

`))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
		cliproxyauth.AttributeEnableResponseChaining: "true",
	}}
	headers := http.Header{}
	headers.Set(helps.ClaudeCodeSessionHeader, "44444444-5555-6666-7777-888888888888")
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","input":"hello"}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Headers:      headers,
	}

	if _, err := executor.Execute(context.Background(), auth, req, opts); err != nil {
		t.Fatalf("first Execute error: %v", err)
	}
	if _, err := executor.Execute(context.Background(), auth, req, opts); err != nil {
		t.Fatalf("second Execute error: %v", err)
	}
	if len(bodies) != 2 {
		t.Fatalf("requests = %d, want 2", len(bodies))
	}
	if gjson.GetBytes(bodies[0], "previous_response_id").Exists() {
		t.Fatalf("first request should not send previous_response_id: %s", bodies[0])
	}
	if got := gjson.GetBytes(bodies[1], "previous_response_id").String(); got != "resp_1" {
		t.Fatalf("second request previous_response_id = %q, want resp_1; body=%s", got, bodies[1])
	}
}
