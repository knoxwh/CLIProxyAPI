package config

// TKLiteConfig configures the optional tklite cache optimization service integration.
// When enabled, request bodies are sent to a local tklite instance (Unix socket)
// for cache optimization before being forwarded to upstream APIs.
type TKLiteConfig struct {
	// Enabled controls whether tklite optimization is active.
	Enabled bool `yaml:"enabled" json:"enabled"`
	// Socket is the path to the tklite Unix domain socket.
	Socket string `yaml:"socket" json:"socket"`
}
