package executor

import (
	"context"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/tklite"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

// ApplyReminderStripIfClaude strips historical <system-reminder> blocks from
// the Claude-native request body BEFORE protocol translation, by dispatching
// to the tklite strip-only endpoint. It is a no-op when the source format is
// not Claude (reminder blocks are Claude-protocol specific).
//
// Placed in the executor package (not helps/) because it depends on
// CacheOptTKLiteHeaders, which lives here — mirroring codex_cache_optimizer.go.
//
// tklite.Optimize returns a NEW []byte (client.go io.ReadAll); callers that
// captured req.Payload before this call (e.g. originalPayload) are unaffected
// by the reassignment.
func ApplyReminderStripIfClaude(
	ctx context.Context,
	cfg *config.Config,
	from sdktranslator.Format,
	req *cliproxyexecutor.Request,
	auth *cliproxyauth.Auth,
	headers http.Header,
) {
	if from != sdktranslator.FormatClaude {
		return
	}
	req.Payload = tklite.Optimize(ctx, cfg, "/v1/messages/strip-system-reminders",
		req.Payload, CacheOptTKLiteHeaders(auth, *req, headers))
}
