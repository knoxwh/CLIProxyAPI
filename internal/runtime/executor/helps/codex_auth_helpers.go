package helps

import (
	"strings"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// CodexResponseChainingDisabled returns true when the upstream rejects
// previous_response_id and response storage for this Codex credential.
func CodexResponseChainingDisabled(auth *cliproxyauth.Auth) bool {
	if auth == nil || auth.Attributes == nil {
		return false
	}
	return strings.TrimSpace(auth.Attributes[cliproxyauth.AttributeDisableResponseChaining]) == "true"
}
