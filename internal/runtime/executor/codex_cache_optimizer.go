// ─── Cache Optimization for API Key Auth ─────────────────────
//
// This file is self-contained and manages ALL custom logic for
// improving prompt cache hit rates on the codex-api-key path
// (standard OpenAI Responses API upstream). The OAuth/subscription
// path (chatgpt.com backend) keeps its original behavior.
//
// When the upstream CLIProxyAPI repo updates, you only need to
// re-add the hook calls in codex_executor.go (3-5 lines total).
// This file itself requires no changes.

package executor

import (
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
//
// All paths:
//   - Delete prompt_cache_retention (tklite may re-inject it after trunk
//     deleted it; upstream APIs reject this field)
//
// API key path, chaining enabled (default):
//   - Set store=true (enables response storage → previous_response_id)
//   - Inject previous_response_id from session map (conversation chaining)
//
// API key path, chaining disabled (disable-response-chaining: true):
//   - Set store=false (aligns with codex non-WS HTTP: store = is_azure_responses_endpoint(),
//     false for non-Azure upstreams; prompt caching is automatic and store-independent)
//   - Delete previous_response_id (no stored baseline to reference)
//   - prompt_cache_key still provides stateless prefix caching
//
// OAuth path (isAPIKey=false, chatgpt.com upstream):
//   - Override store=false (chatgpt.com requires this)
func CacheOptPostTKLite(auth *cliproxyauth.Auth, body []byte, req cliproxyexecutor.Request, originalPayloadSource []byte) []byte {
	body, _ = sjson.DeleteBytes(body, "prompt_cache_retention")

	if isAPIKeyAuth(auth) {
		if helps.CodexResponseChainingDisabled(auth) {
			// ── API key path, chaining disabled: align with codex non-WS HTTP
			// behavior. codex sets store = is_azure_responses_endpoint(), which
			// is false for non-Azure upstreams (the standard OpenAI Responses
			// API case). Prompt caching is automatic and independent of `store`
			// (per OpenAI docs), so store=false does not hurt prefix cache hit
			// rates — prompt_cache_key still routes to the stable cache bucket.
			// store=false also avoids 30-day server-side response retention
			// when no chaining is intended.
			//
			// previous_response_id is deleted: with chaining disabled there is
			// no stored baseline to reference. ──
			body, _ = sjson.SetBytes(body, "store", false)
			body, _ = sjson.DeleteBytes(body, "previous_response_id")
			if sessionKey := cacheOptSessionResponseKey(auth, req); sessionKey != "" {
				helps.DeleteSessionResponseID(sessionKey)
			}
			return body
		}
		// ── API key path: enable conversation chaining ──
		body, _ = sjson.SetBytes(body, "store", true)

		// Inject previous_response_id only when the client did not
		// explicitly provide a non-empty one. originalPayloadSource is the raw
		// client payload before trunk deletes the field for OAuth safety.
		pr := gjson.GetBytes(originalPayloadSource, "previous_response_id")
		if pr.Exists() {
			if clientValue := strings.TrimSpace(pr.String()); clientValue != "" {
				body, _ = sjson.SetBytes(body, "previous_response_id", clientValue)
			}
			// Empty client value is treated as "no explicit intent" and falls
			// through to the session-cache fallback below.
		}
		if gjson.GetBytes(body, "previous_response_id").String() == "" {
			sessionKey := cacheOptSessionResponseKey(auth, req)
			if sessionKey != "" {
				if lastRespID, ok := helps.GetSessionResponseID(sessionKey); ok && lastRespID != "" {
					body, _ = sjson.SetBytes(body, "previous_response_id", lastRespID)
				}
			}
		}
	} else {
		// ── OAuth/subscription path: chatgpt.com backend ──
		// tklite injects store=true for Responses API shape,
		// but chatgpt.com requires store=false.
		body, _ = sjson.SetBytes(body, "store", false)
		// Defensive delete: keep chatgpt.com compatible even if future
		// upstream changes leave this field in the body.
		body, _ = sjson.DeleteBytes(body, "previous_response_id")
	}
	return body
}

// ─── Hook 2: Store response.id for chaining ───────────────────
//
// Called after response.completed in Execute and ExecuteStream.
// Only active for API key auth — stores the last response.id so
// the next request in the same session can set previous_response_id.
func CacheOptStoreResponseID(auth *cliproxyauth.Auth, req cliproxyexecutor.Request, completedData []byte) {
	if !isAPIKeyAuth(auth) {
		return
	}
	if helps.CodexResponseChainingDisabled(auth) {
		if sessionKey := cacheOptSessionResponseKey(auth, req); sessionKey != "" {
			helps.DeleteSessionResponseID(sessionKey)
		}
		return
	}
	respID := gjson.GetBytes(completedData, "response.id").String()
	if respID == "" {
		return
	}
	sessionKey := cacheOptSessionResponseKey(auth, req)
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

func cacheOptSessionResponseKey(auth *cliproxyauth.Auth, req cliproxyexecutor.Request) string {
	sessionKey := helps.ExtractClaudeCodeSessionID(nil, req.Payload, nil)
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

func CacheOptTKLiteSessionKey(auth *cliproxyauth.Auth, req cliproxyexecutor.Request) string {
	sessionKey := cacheOptSessionResponseKey(auth, req)
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
	if sessionKey := CacheOptTKLiteSessionKey(auth, req); sessionKey != "" {
		cloned.Set("x-tklite-session-key", sessionKey)
	}
	return cloned
}
