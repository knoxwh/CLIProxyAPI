package executor

import (
	"bytes"
	"context"
	"sync/atomic"
	"testing"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestTranslateCodexRequestPairReusesEqualPayload(t *testing.T) {
	from := sdktranslator.Format("codex-test-from-equal")
	to := sdktranslator.Format("codex-test-to-equal")
	var calls int32
	sdktranslator.Register(from, to, func(model string, rawJSON []byte, stream bool) []byte {
		atomic.AddInt32(&calls, 1)
		if model != "test-model" {
			t.Errorf("model = %q, want test-model", model)
		}
		if !stream {
			t.Error("stream = false, want true")
		}
		return append([]byte(nil), rawJSON...)
	}, sdktranslator.ResponseTransform{})

	payload := []byte(`{"model":"test-model","input":[{"role":"user"}]}`)
	originalTranslated, body := translateCodexRequestPair(context.Background(), nil, nil, from, to, "test-model", payload, bytes.Clone(payload), true)

	if gotCalls := atomic.LoadInt32(&calls); gotCalls != 1 {
		t.Fatalf("TranslateRequest calls = %d, want 1", gotCalls)
	}
	if !bytes.Equal(originalTranslated, body) {
		t.Fatalf("translated payloads differ: original=%s body=%s", originalTranslated, body)
	}
}

func TestTranslateCodexRequestPairTranslatesDifferentPayloads(t *testing.T) {
	from := sdktranslator.Format("codex-test-from-different")
	to := sdktranslator.Format("codex-test-to-different")
	var calls int32
	sdktranslator.Register(from, to, func(_ string, rawJSON []byte, _ bool) []byte {
		atomic.AddInt32(&calls, 1)
		return append([]byte(nil), rawJSON...)
	}, sdktranslator.ResponseTransform{})

	originalPayload := []byte(`{"model":"test-model","input":[{"role":"system"}]}`)
	payload := []byte(`{"model":"test-model","input":[{"role":"user"}]}`)
	originalTranslated, body := translateCodexRequestPair(context.Background(), nil, nil, from, to, "test-model", originalPayload, payload, false)

	if gotCalls := atomic.LoadInt32(&calls); gotCalls != 2 {
		t.Fatalf("TranslateRequest calls = %d, want 2", gotCalls)
	}
	if !bytes.Equal(originalTranslated, originalPayload) {
		t.Fatalf("original translated = %s, want %s", originalTranslated, originalPayload)
	}
	if !bytes.Equal(body, payload) {
		t.Fatalf("body = %s, want %s", body, payload)
	}
}

// TestTranslateCodexRequestPairPreTransformClaudeFailOpen covers the
// from==FormatClaude guard path. With cfg=nil, tklite.Optimize is
// fail-open (returns the original body untouched), so translation must
// still run and produce the same output as without the guard. This
// verifies the guard does not corrupt the body when the sidecar is
// unavailable, and that non-Claude source formats skip the guard.
func TestTranslateCodexRequestPairPreTransformClaudeFailOpen(t *testing.T) {
	to := sdktranslator.Format("codex-test-pretransform-to")
	var calls int32
	sdktranslator.Register(sdktranslator.FormatClaude, to, func(_ string, rawJSON []byte, _ bool) []byte {
		atomic.AddInt32(&calls, 1)
		return append([]byte(nil), rawJSON...)
	}, sdktranslator.ResponseTransform{})

	payload := []byte(`{"model":"m","input":[{"role":"user"}]}`)
	// cfg=nil → tklite.Optimize returns body unchanged (fail-open).
	originalTranslated, body := translateCodexRequestPair(
		context.Background(), nil, nil,
		sdktranslator.FormatClaude, to, "m", payload, bytes.Clone(payload), false,
	)

	if gotCalls := atomic.LoadInt32(&calls); gotCalls != 1 {
		t.Fatalf("TranslateRequest calls = %d, want 1 (equal-payload reuse)", gotCalls)
	}
	if !bytes.Equal(originalTranslated, body) {
		t.Fatalf("translated payloads differ: original=%s body=%s", originalTranslated, body)
	}
	if !bytes.Equal(body, payload) {
		t.Fatalf("body = %s, want original %s (fail-open must not corrupt)", body, payload)
	}
}
