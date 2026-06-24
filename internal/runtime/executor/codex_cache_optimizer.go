// ─── Cache Optimization and TKLite Helpers ───────────────────
//
// This file is self-contained and manages custom prompt-cache logic for
// Codex requests. API-key response chaining is opt-in: credentials must set
// enable-response-chaining: true before previous_response_id is sent. All
// other paths strip response-chaining fields and avoid response storage.

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
// Deletes prompt_cache_retention on every path. Opt-in API-key credentials may
// send previous_response_id and store responses; all other paths force
// store=false, drop previous_response_id, and clear stale cached response IDs.
func CacheOptPostTKLite(auth *cliproxyauth.Auth, body []byte, req cliproxyexecutor.Request, originalPayloadSource []byte, headers ...http.Header) []byte {
	body, _ = sjson.DeleteBytes(body, "prompt_cache_retention")

	if isAPIKeyAuth(auth) && helps.CodexResponseChainingEnabled(auth) {
		body, _ = sjson.SetBytes(body, "store", true)

		pr := gjson.GetBytes(originalPayloadSource, "previous_response_id")
		if pr.Exists() {
			if clientValue := strings.TrimSpace(pr.String()); clientValue != "" {
				body, _ = sjson.SetBytes(body, "previous_response_id", clientValue)
			}
		}
		if gjson.GetBytes(body, "previous_response_id").String() == "" {
			sessionKey := cacheOptSessionResponseKey(auth, req, headers...)
			if sessionKey != "" {
				if lastRespID, ok := helps.GetSessionResponseID(sessionKey); ok && lastRespID != "" {
					body, _ = sjson.SetBytes(body, "previous_response_id", lastRespID)
				}
			}
		}
		return body
	}

	body, _ = sjson.SetBytes(body, "store", false)
	body, _ = sjson.DeleteBytes(body, "previous_response_id")
	if sessionKey := cacheOptSessionResponseKey(auth, req, headers...); sessionKey != "" {
		helps.DeleteSessionResponseID(sessionKey)
	}
	return body
}

// ─── Hook 2: Store response.id for chaining ───────────────────
//
// Called after response.completed in Execute and ExecuteStream.
// Only active for opt-in API key auth — stores the last response.id so
// the next request in the same session can set previous_response_id.
func CacheOptStoreResponseID(auth *cliproxyauth.Auth, req cliproxyexecutor.Request, completedData []byte, headers ...http.Header) {
	if !isAPIKeyAuth(auth) || !helps.CodexResponseChainingEnabled(auth) {
		// Clear here too in case this hook runs after config flips or without CacheOptPostTKLite.
		if sessionKey := cacheOptSessionResponseKey(auth, req, headers...); sessionKey != "" {
			helps.DeleteSessionResponseID(sessionKey)
		}
		return
	}
	respID := gjson.GetBytes(completedData, "response.id").String()
	if respID == "" {
		respID = gjson.GetBytes(completedData, "id").String()
	}
	if respID == "" {
		return
	}
	sessionKey := cacheOptSessionResponseKey(auth, req, headers...)
	if sessionKey == "" {
		return
	}
	helps.SetSessionResponseID(sessionKey, respID)
}

// ─── Hook 3: Resolve prompt_cache_key ─────────────────────────
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
// Prevents cross-auth response-id reuse: two different API keys or
// base URLs must not share a previous_response_id.

func cacheOptSessionResponseKey(auth *cliproxyauth.Auth, req cliproxyexecutor.Request, headers ...http.Header) string {
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
	sessionKey := cacheOptSessionResponseKey(auth, req, headers...)
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

func cacheOptDiagnosticsOptions(auth *cliproxyauth.Auth, req cliproxyexecutor.Request, headers http.Header, responseChainingEnabled bool) helps.CacheDiagnosticsOptions {
	return helps.CacheDiagnosticsOptions{
		TKLiteSessionKeyPresent: strings.TrimSpace(CacheOptTKLiteSessionKey(auth, req, headers)) != "",
		ResponseChainingEnabled: responseChainingEnabled,
	}
}

func recordResponsesCacheLossRequestInfo(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, upstreamBody []byte, originalPayload []byte, headers http.Header, responseChainingEnabled bool) {
	helps.RecordCacheLossRequestInfo(ctx, upstreamBody, originalPayload, headers, cacheOptDiagnosticsOptions(auth, req, headers, responseChainingEnabled))
}

func recordCodexCacheLossRequestInfo(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, upstreamBody []byte, originalPayload []byte, headers http.Header) {
	recordResponsesCacheLossRequestInfo(ctx, auth, req, upstreamBody, originalPayload, headers, isAPIKeyAuth(auth) && helps.CodexResponseChainingEnabled(auth))
}
