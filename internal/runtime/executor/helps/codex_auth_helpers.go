package helps

import (
	"strings"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// CodexResponseChainingEnabled returns true when this Codex credential may
// send previous_response_id and store response IDs for follow-up requests.
func CodexResponseChainingEnabled(auth *cliproxyauth.Auth) bool {
	if auth == nil || auth.Attributes == nil {
		return false
	}
	return strings.TrimSpace(auth.Attributes[cliproxyauth.AttributeEnableResponseChaining]) == "true"
}
