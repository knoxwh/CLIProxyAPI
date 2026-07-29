package config

import "strings"

const (
	// DefaultTKLiteSocket is the default Unix domain socket path.
	DefaultTKLiteSocket = "/tmp/tklite.sock"
	// DefaultTKLiteRequestTimeoutSeconds is the default timeout for
	// tklite sidecar optimization requests.
	DefaultTKLiteRequestTimeoutSeconds = 3
	// DefaultTKLiteMaxResponseBytes is the default maximum response size
	// from the tklite sidecar (10 MB).
	DefaultTKLiteMaxResponseBytes int64 = 10 * 1024 * 1024
)

// TKLiteConfig configures the optional tklite cache optimization service integration.
// When enabled, request bodies are sent to a local tklite instance (Unix socket)
// for cache optimization before being forwarded to upstream APIs.
type TKLiteConfig struct {
	// Enabled controls whether tklite optimization is active.
	Enabled bool `yaml:"enabled" json:"enabled"`
	// Socket is the path to the tklite Unix domain socket.
	Socket string `yaml:"socket" json:"socket"`
	// RequestTimeoutSeconds is the maximum time to wait for a tklite
	// optimization response. 0 or negative uses the default.
	RequestTimeoutSeconds int `yaml:"request-timeout-seconds" json:"request-timeout-seconds"`
	// MaxResponseBytes is the maximum allowed size of a tklite response.
	// 0 or negative uses the default.
	MaxResponseBytes int64 `yaml:"max-response-bytes" json:"max-response-bytes"`
}

// Normalize applies defaults for zero/empty values.
func (c *TKLiteConfig) Normalize() {
	if strings.TrimSpace(c.Socket) == "" {
		c.Socket = DefaultTKLiteSocket
	}
	if c.RequestTimeoutSeconds <= 0 {
		c.RequestTimeoutSeconds = DefaultTKLiteRequestTimeoutSeconds
	}
	if c.MaxResponseBytes <= 0 {
		c.MaxResponseBytes = DefaultTKLiteMaxResponseBytes
	}
}
