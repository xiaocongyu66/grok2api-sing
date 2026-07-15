package egress

import (
	"math/rand"
	"strings"
	"sync"
)

// RandomUserAgentToken is the stored sentinel meaning "pick a UA per lease".
const RandomUserAgentToken = "random"

// BuiltinBrowserUserAgents is a small pool of common desktop browser UAs.
// Profiles intentionally stay near Chrome so they match the Chrome_146 TLS fingerprint.
var BuiltinBrowserUserAgents = []string{
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/145.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/145.0.0.0 Safari/537.36",
	"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36 Edg/146.0.0.0",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36 Edg/146.0.0.0",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:136.0) Gecko/20100101 Firefox/136.0",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:136.0) Gecko/20100101 Firefox/136.0",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.3 Safari/605.1.15",
}

var (
	uaRandMu sync.Mutex
	uaRand   = rand.New(rand.NewSource(rand.Int63())) //nolint:gosec // non-crypto UA rotation
)

// IsRandomUserAgent reports whether the stored node UA means rotate from the pool.
func IsRandomUserAgent(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case RandomUserAgentToken, "auto", "randomize", "__random__":
		return true
	default:
		return false
	}
}

// NormalizeStoredUserAgent maps UI/API values into the persisted form.
// random/auto → "random"; other non-empty trimmed; empty stays empty (provider default).
func NormalizeStoredUserAgent(value string) string {
	value = strings.TrimSpace(value)
	if IsRandomUserAgent(value) {
		return RandomUserAgentToken
	}
	return value
}

// ResolveBrowserUserAgent returns the UA to put on a lease.
// random → pick from pool; empty → DefaultUserAgent; else fixed string.
func ResolveBrowserUserAgent(stored string) string {
	stored = strings.TrimSpace(stored)
	if IsRandomUserAgent(stored) {
		return RandomBrowserUserAgent()
	}
	if stored == "" {
		return DefaultUserAgent
	}
	return stored
}

// RandomBrowserUserAgent returns one entry from BuiltinBrowserUserAgents.
func RandomBrowserUserAgent() string {
	pool := BuiltinBrowserUserAgents
	if len(pool) == 0 {
		return DefaultUserAgent
	}
	uaRandMu.Lock()
	defer uaRandMu.Unlock()
	return pool[uaRand.Intn(len(pool))]
}
