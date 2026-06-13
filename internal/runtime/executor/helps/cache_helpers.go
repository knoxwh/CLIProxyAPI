package helps

import (
	"sync"
	"time"
)

type CodexCache struct {
	ID     string
	Expire time.Time
}

// codexCacheMap stores prompt cache IDs keyed by model+user_id.
// Protected by codexCacheMu. Entries expire after 1 hour.
var (
	codexCacheMap = make(map[string]CodexCache)
	codexCacheMu  sync.RWMutex
)

// codexCacheCleanupInterval controls how often expired entries are purged.
const codexCacheCleanupInterval = 15 * time.Minute

// codexCacheCleanupOnce ensures the background cleanup goroutine starts only once.
var codexCacheCleanupOnce sync.Once

// startCodexCacheCleanup launches a background goroutine that periodically
// removes expired entries from codexCacheMap to prevent memory leaks.
func startCodexCacheCleanup() {
	go func() {
		ticker := time.NewTicker(codexCacheCleanupInterval)
		defer ticker.Stop()
		for range ticker.C {
			purgeExpiredCodexCache()
			purgeExpiredSessionResponses()
		}
	}()
}

// purgeExpiredCodexCache removes entries that have expired.
func purgeExpiredCodexCache() {
	now := time.Now()
	codexCacheMu.Lock()
	defer codexCacheMu.Unlock()
	for key, cache := range codexCacheMap {
		if cache.Expire.Before(now) {
			delete(codexCacheMap, key)
		}
	}
}

// GetCodexCache retrieves a cached entry, returning ok=false if not found or expired.
func GetCodexCache(key string) (CodexCache, bool) {
	codexCacheCleanupOnce.Do(startCodexCacheCleanup)
	codexCacheMu.RLock()
	cache, ok := codexCacheMap[key]
	codexCacheMu.RUnlock()
	if !ok || cache.Expire.Before(time.Now()) {
		return CodexCache{}, false
	}
	return cache, true
}

// SetCodexCache stores a cache entry.
func SetCodexCache(key string, cache CodexCache) {
	codexCacheCleanupOnce.Do(startCodexCacheCleanup)
	codexCacheMu.Lock()
	codexCacheMap[key] = cache
	codexCacheMu.Unlock()
}

// ─── session → response_id mapping ────────────────────────────
//
// Stores the last response.id for each session so that subsequent
// requests can set previous_response_id for conversation chaining.
// This enables OpenAI's server-side KV cache reuse, which is the
// key mechanism for achieving 90%+ prompt cache hit rates.

type SessionResponseID struct {
	ResponseID string
	Expire     time.Time
}

var (
	sessionResponseMap = make(map[string]SessionResponseID)
	sessionResponseMu  sync.RWMutex
)

// GetSessionResponseID retrieves the last response.id for a session.
func GetSessionResponseID(sessionKey string) (string, bool) {
	codexCacheCleanupOnce.Do(startCodexCacheCleanup)
	sessionResponseMu.RLock()
	entry, ok := sessionResponseMap[sessionKey]
	sessionResponseMu.RUnlock()
	if !ok || entry.Expire.Before(time.Now()) {
		return "", false
	}
	return entry.ResponseID, true
}

// SetSessionResponseID stores the last response.id for a session.
// TTL matches prompt_cache_retention (24h for gpt-5.x).
func SetSessionResponseID(sessionKey string, responseID string) {
	codexCacheCleanupOnce.Do(startCodexCacheCleanup)
	sessionResponseMu.Lock()
	sessionResponseMap[sessionKey] = SessionResponseID{
		ResponseID: responseID,
		Expire:     time.Now().Add(24 * time.Hour),
	}
	sessionResponseMu.Unlock()
}

// purgeExpiredSessionResponses removes expired session→response_id entries.
// Called by the existing cleanup goroutine.
func purgeExpiredSessionResponses() {
	now := time.Now()
	sessionResponseMu.Lock()
	defer sessionResponseMu.Unlock()
	for key, entry := range sessionResponseMap {
		if entry.Expire.Before(now) {
			delete(sessionResponseMap, key)
		}
	}
}
