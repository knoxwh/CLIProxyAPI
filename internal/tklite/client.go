// Package tklite provides a client for the tklite cache optimization service.
//
// tklite runs as a separate process listening on a Unix domain socket.
// This client sends request bodies to tklite for cache optimization
// (auto cache_control, prompt_cache_key, tool normalization) and
// returns the optimized body for upstream delivery.
package tklite

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	log "github.com/sirupsen/logrus"
)

// ── Singleton client ──────────────────────────────────────────────────────

var (
	defaultClient     *Client
	defaultClientOnce sync.Once
)

func getClient(socketPath string) *Client {
	defaultClientOnce.Do(func() {
		defaultClient = NewClient(socketPath)
	})
	return defaultClient
}

// ── Public API ─────────────────────────────────────────────────────────────

// Optimize sends the request body to tklite for cache optimization if enabled.
// Returns the original body if disabled, misconfigured, or on any error.
// This is the single entry point for all executor integrations.
func Optimize(ctx context.Context, cfg *config.Config, endpoint string, body []byte, headers http.Header) []byte {
	if cfg == nil || !cfg.TKLite.Enabled || cfg.TKLite.Socket == "" {
		return body
	}

	client := getClient(cfg.TKLite.Socket)
	fwd := extractHeaders(headers)
	optimized, err := client.Optimize(ctx, endpoint, body, fwd)
	if err != nil {
		log.WithError(err).WithField("endpoint", endpoint).
			Warn("tklite optimization failed, using original body")
		return body
	}
	return optimized
}

// ── Header forwarding ─────────────────────────────────────────────────────

// forwardHeaders lists headers relevant to tklite optimization.
var forwardHeaders = []string{
	"anthropic-beta",
	"x-api-key",
	"x-headroom-session-id",
	"x-headroom-bypass",
	"x-client",
	"x-request-id",
}

func extractHeaders(h http.Header) map[string]string {
	if h == nil {
		return nil
	}
	fwd := make(map[string]string, len(forwardHeaders))
	for _, key := range forwardHeaders {
		if v := h.Get(key); v != "" {
			fwd[key] = v
		}
	}
	if len(fwd) == 0 {
		return nil
	}
	return fwd
}

// ── Client ─────────────────────────────────────────────────────────────────

// Client communicates with the tklite optimization service over a Unix socket.
type Client struct {
	httpClient *http.Client
	socketPath string
}

// NewClient creates a tklite client connected to the given Unix socket path.
func NewClient(socketPath string) *Client {
	transport := &http.Transport{
		DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
			return net.DialTimeout("unix", socketPath, 2*time.Second)
		},
		MaxIdleConns:        4,
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     90 * time.Second,
	}
	return &Client{
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   5 * time.Second,
		},
		socketPath: socketPath,
	}
}

// Optimize sends a request body to tklite for cache optimization.
// Returns the optimized body, or the original body on error.
func (c *Client) Optimize(ctx context.Context, endpoint string, body []byte, headers map[string]string) ([]byte, error) {
	url := "http://localhost" + endpoint
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return body, fmt.Errorf("tklite: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return body, fmt.Errorf("tklite: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, resp.Body)
		return body, fmt.Errorf("tklite: unexpected status %d", resp.StatusCode)
	}

	optimized, err := io.ReadAll(resp.Body)
	if err != nil {
		return body, fmt.Errorf("tklite: read response: %w", err)
	}

	return optimized, nil
}

// Health checks if the tklite service is reachable.
func (c *Client) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost/v1/health", nil)
	if err != nil {
		return fmt.Errorf("tklite: build health request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("tklite: health check failed: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("tklite: health returned status %d", resp.StatusCode)
	}
	return nil
}
