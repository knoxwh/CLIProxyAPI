package helps

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

// CacheRegressionContext carries the cache-bucket key plus its parsed parts,
// so the regression log can emit auth/session/systemHash without re-parsing the
// request body at publish time.
type CacheRegressionContext struct {
	Key        string // authID:sessionID:systemHash
	SessionID  string
	SystemHash string
	AuthID     string
}

// CacheRegressionKey derives the regression bucket for a Claude request.
// Returns ok=false when no session id can be resolved (no bucket possible).
// payload is the original incoming request body (for session metadata);
// systemHash is derived from the system field inside payload.
func CacheRegressionKey(ctx context.Context, payload []byte, headers http.Header, authObj *cliproxyauth.Auth) (CacheRegressionContext, bool) {
	sessionID := ExtractClaudeCodeSessionID(ctx, payload, headers)
	if sessionID == "" {
		return CacheRegressionContext{}, false
	}
	authID := ""
	if authObj != nil {
		authID = authObj.ID
	}
	systemHash := HashSystemPrompt(payload)
	return CacheRegressionContext{
		Key:        authID + ":" + sessionID + ":" + systemHash,
		SessionID:  sessionID,
		SystemHash: systemHash,
		AuthID:     authID,
	}, true
}

// HashSystemPrompt returns the first 8 hex chars of SHA256 over the request's
// `system` field text. String form hashes the string directly; array form joins
// every type==text block's text in order. Tools/cache_control/non-text blocks
// are excluded so the hash tracks only the stable agent system prompt.
func HashSystemPrompt(payload []byte) string {
	var joined strings.Builder
	system := gjson.GetBytes(payload, "system")
	if system.Exists() {
		if system.Type == gjson.String {
			joined.WriteString(system.String())
		} else if system.IsArray() {
			system.ForEach(func(_, block gjson.Result) bool {
				if block.Get("type").String() == "text" {
					joined.WriteString(block.Get("text").String())
				}
				return true
			})
		}
	}
	sum := sha256.Sum256([]byte(joined.String()))
	return hex.EncodeToString(sum[:])[:8]
}

// CacheRegressionKeyOpenAI derives the regression bucket for an OpenAI/Codex
// request. The bucket is authID:prompt_cache_key when the body carries a
// prompt_cache_key (injected by tklite or the client); otherwise it falls back
// to authID:sessionID using the Claude Code session id resolved from headers or
// metadata.user_id. Returns ok=false when neither is available.
//
// systemHash is derived from the OpenAI-style system prompt: the `instructions`
// field (Responses API) or the concatenated text of every role=system message
// (Chat Completions). Non-system messages are excluded so the hash tracks only
// the stable agent system prompt.
func CacheRegressionKeyOpenAI(ctx context.Context, payload []byte, headers http.Header, authObj *cliproxyauth.Auth) (CacheRegressionContext, bool) {
	authID := ""
	if authObj != nil {
		authID = authObj.ID
	}
	sessionID := strings.TrimSpace(gjson.GetBytes(payload, "prompt_cache_key").String())
	if sessionID == "" {
		sessionID = ExtractClaudeCodeSessionID(ctx, payload, headers)
	}
	if sessionID == "" {
		return CacheRegressionContext{}, false
	}
	systemHash := HashOpenAISystemPrompt(payload)
	return CacheRegressionContext{
		Key:        authID + ":" + sessionID + ":" + systemHash,
		SessionID:  sessionID,
		SystemHash: systemHash,
		AuthID:     authID,
	}, true
}

// HashOpenAISystemPrompt returns the first 8 hex chars of SHA256 over the
// OpenAI-style system prompt: the `instructions` field when present (Responses
// API), otherwise the concatenated text of every role=system message (Chat
// Completions). Returns a hash of the empty string when neither is present so
// the bucket key remains stable.
func HashOpenAISystemPrompt(payload []byte) string {
	var joined strings.Builder
	if instructions := gjson.GetBytes(payload, "instructions"); instructions.Exists() && instructions.Type == gjson.String {
		joined.WriteString(instructions.String())
	} else {
		messages := gjson.GetBytes(payload, "messages")
		if messages.IsArray() {
			messages.ForEach(func(_, msg gjson.Result) bool {
				if msg.Get("role").String() != "system" {
					return true
				}
				content := msg.Get("content")
				if content.Type == gjson.String {
					joined.WriteString(content.String())
				} else if content.IsArray() {
					content.ForEach(func(_, part gjson.Result) bool {
						if part.Get("type").String() == "text" {
							joined.WriteString(part.Get("text").String())
						}
						return true
					})
				}
				return true
			})
		}
	}
	sum := sha256.Sum256([]byte(joined.String()))
	return hex.EncodeToString(sum[:])[:8]
}
