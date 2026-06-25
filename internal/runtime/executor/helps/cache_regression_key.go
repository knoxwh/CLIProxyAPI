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
