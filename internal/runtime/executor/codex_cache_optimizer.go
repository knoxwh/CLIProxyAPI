// ─── Cache Optimization for API Key Auth ─────────────────────
//
// This file is self-contained and manages ALL custom logic for
// improving prompt cache hit rates on the codex-api-key path
// (standard OpenAI API / muskapi upstream). The OAuth/subscription
// path (chatgpt.com backend) keeps its original behavior.
//
// When the upstream CLIProxyAPI repo updates, you only need to
// re-add the hook calls in codex_executor.go (3-5 lines total).
// This file itself requires no changes.

package executor

import (
	"strings"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
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
// API key path (isAPIKey=true, muskapi upstream):
//   - Set store=true (enables response storage → previous_response_id)
//   - Inject previous_response_id from session map (conversation chaining)
//
// OAuth path (isAPIKey=false, chatgpt.com upstream):
//   - Override store=false (chatgpt.com requires this)
//   - Delete prompt_cache_retention (tklite re-injected it after trunk
//     deleted it; chatgpt.com rejects this field)
func CacheOptPostTKLite(auth *cliproxyauth.Auth, body []byte, req cliproxyexecutor.Request) []byte {
	if isAPIKeyAuth(auth) {
		// ── API key path: enable conversation chaining ──
		body, _ = sjson.SetBytes(body, "store", true)

		// Inject previous_response_id from session map.
		// Trunk code deleted this field before tklite (safe for OAuth),
		// so we re-inject it here for the API key path.
		if !gjson.GetBytes(body, "previous_response_id").Exists() {
			sessionKey := codexClaudeCodePromptCacheStorageKey(req)
			if sessionKey == "" {
				sessionKey = extractClaudeCodeSessionIDForCodexReplay(req.Payload)
			}
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
		// tklite re-injected prompt_cache_retention after trunk deleted it.
		// chatgpt.com rejects this field.
		body, _ = sjson.DeleteBytes(body, "prompt_cache_retention")
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
	respID := gjson.GetBytes(completedData, "response.id").String()
	if respID == "" {
		return
	}
	sessionKey := codexClaudeCodePromptCacheStorageKey(req)
	if sessionKey == "" {
		sessionKey = extractClaudeCodeSessionIDForCodexReplay(req.Payload)
	}
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
