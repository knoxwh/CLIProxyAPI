// ─── Cache Optimization and TKLite Helpers ───────────────────
//
// This file is self-contained and manages custom prompt-cache logic for
// Codex requests. Non-WebSocket Codex paths force store=false and delete
// previous_response_id; response.id storage and previous_response_id
// injection are intentionally not supported outside the WebSocket path,
// where chaining is client-native.

package executor

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// isAPIKeyAuth returns true when the auth uses an API key
// (codex-api-key → standard OpenAI API upstream) as opposed to
// OAuth subscription (chatgpt.com backend).
func isAPIKeyAuth(auth *cliproxyauth.Auth) bool {
	if auth == nil || auth.Attributes == nil {
		return false
	}
	return strings.TrimSpace(auth.Attributes["api_key"]) != ""
}

// ─── Hook 1: Post-tklite adjustments ──────────────────────────
//
// Called after tklite.Optimize() in Execute and ExecuteStream.
// Non-WebSocket Codex paths force store=false and delete
// previous_response_id. prompt_cache_retention is also stripped because
// tklite may re-inject it and the upstream Responses API rejects that
// field with HTTP 400 ("Unsupported parameter: prompt_cache_retention").
func CacheOptPostTKLite(body []byte) []byte {
	body, _ = sjson.DeleteBytes(body, "prompt_cache_retention")
	body, _ = sjson.DeleteBytes(body, "previous_response_id")
	body, _ = sjson.SetBytes(body, "store", false)
	return body
}

// ─── Hook 2: Resolve prompt_cache_key ─────────────────────────
//
// Called inside cacheHelper to decide whether to preserve
// tklite's stable prompt_cache_key or use the generated uuid.
//
// API key path: preserve tklite's key (session-based, stable,
// enables consistent cache routing across turns).
//
// OAuth path: use generated uuid (original behavior, chatgpt.com
// accepts any key but doesn't need stable routing).
func CacheOptResolveCacheKey(auth *cliproxyauth.Auth, rawJSON []byte, proposedID string) string {
	if !isAPIKeyAuth(auth) {
		return proposedID // OAuth: use uuid (original behavior)
	}
	existingKey := gjson.GetBytes(rawJSON, "prompt_cache_key").String()
	if strings.TrimSpace(existingKey) != "" {
		return existingKey // API key: preserve tklite's stable key
	}
	return proposedID // No tklite key found, use proposed uuid
}

// ─── Auth-scoped session key helpers ───────────────────────────
//
// Prevents cross-auth cache mixing: two different API keys or
// base URLs must not share a tklite session bucket.

func cacheOptSessionKey(auth *cliproxyauth.Auth, req cliproxyexecutor.Request, headers ...http.Header) string {
	var hdr http.Header
	if len(headers) > 0 {
		hdr = headers[0]
	}
	sessionKey := helps.ExtractClaudeCodeSessionID(nil, req.Payload, hdr)
	if sessionKey == "" {
		return ""
	}
	return cacheOptAuthScope(auth) + ":" + sessionKey
}

func cacheOptAuthScope(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return "auth:unknown"
	}
	if strings.TrimSpace(auth.ID) != "" {
		return "auth-id:" + strings.TrimSpace(auth.ID)
	}
	apiKey := strings.TrimSpace(auth.Attributes["api_key"])
	baseURL := strings.TrimSpace(auth.Attributes["base_url"])
	if apiKey != "" || baseURL != "" {
		return "auth-hash:" + uuid.NewSHA1(uuid.NameSpaceOID, []byte(apiKey+"\x00"+baseURL)).String()
	}
	return "auth:unknown"
}

// ─── Opaque tklite session key + header cloning ────────────────
//
// CacheOptTKLiteSessionKey derives a stable opaque session bucket
// for tklite drift detection. Hashes the auth-scoped session key
// so no raw auth ID, API key, base URL, or session ID leaks.
//
// CacheOptTKLiteHeaders clones request headers and injects the
// sidecar-only x-tklite-session-key header. Use for every
// tklite.Optimize() call to prevent opts.Headers mutation.

func CacheOptTKLiteSessionKey(auth *cliproxyauth.Auth, req cliproxyexecutor.Request, headers ...http.Header) string {
	sessionKey := cacheOptSessionKey(auth, req, headers...)
	if strings.TrimSpace(sessionKey) == "" {
		return ""
	}
	return "cpa:" + uuid.NewSHA1(uuid.NameSpaceOID, []byte(sessionKey)).String()
}

func CacheOptTKLiteHeaders(auth *cliproxyauth.Auth, req cliproxyexecutor.Request, headers http.Header) http.Header {
	cloned := headers.Clone()
	if cloned == nil {
		cloned = http.Header{}
	}
	if sessionKey := CacheOptTKLiteSessionKey(auth, req, headers); sessionKey != "" {
		cloned.Set("x-tklite-session-key", sessionKey)
	}
	return cloned
}

func cacheOptDiagnosticsOptions(auth *cliproxyauth.Auth, req cliproxyexecutor.Request, headers http.Header) helps.CacheDiagnosticsOptions {
	return helps.CacheDiagnosticsOptions{
		TKLiteSessionKeyPresent: strings.TrimSpace(CacheOptTKLiteSessionKey(auth, req, headers)) != "",
	}
}

func recordCacheLossRequestInfo(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, upstreamBody []byte, originalPayload []byte, headers http.Header) {
	helps.RecordCacheLossRequestInfo(ctx, upstreamBody, originalPayload, headers, cacheOptDiagnosticsOptions(auth, req, headers))
}
