// Package promptcache resolves a stable xAI conversation affinity id so upstream
// prompt-cache (cached_tokens) can hit across turns.
//
// Priority (first non-empty wins):
//  1. Explicit client session headers (Claude Code / Codex / Grok CLI style)
//  2. Request body fields (prompt_cache_key, user, metadata.user_id)
//  3. Fingerprint of client key + IP + User-Agent → persistent mapping (Redis/memory)
//
// The resolved value is forwarded as x-grok-conv-id / prompt_cache_key affinity.
// This does NOT invent cached_tokens; it only stabilizes routing so the upstream
// may return real cache hits.
package promptcache

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"time"
)

// Store persists fingerprint → affinity-id mappings.
type Store interface {
	// GetOrCreate returns an existing id for fingerprint or creates one.
	// When expire is false, the mapping is kept without TTL (or with a very long TTL).
	GetOrCreate(ctx context.Context, fingerprint, newID string, ttl time.Duration, expire bool) (string, error)
}

// Policy is hot-reloadable resolver configuration.
type Policy struct {
	Enabled     bool
	Expire      bool
	TTL         time.Duration
	Fingerprint bool // when true, derive id from IP+UA+key if no session header
}

// DefaultPolicy matches sensible production defaults.
func DefaultPolicy() Policy {
	return Policy{
		Enabled:     true,
		Expire:      true,
		TTL:         24 * time.Hour,
		Fingerprint: true,
	}
}

// Resolver maps requests to a stable affinity id.
type Resolver struct {
	store  Store
	policy atomic.Value // Policy
}

func NewResolver(store Store, policy Policy) *Resolver {
	r := &Resolver{store: store}
	r.UpdatePolicy(policy)
	return r
}

func (r *Resolver) UpdatePolicy(policy Policy) {
	if policy.TTL <= 0 {
		policy.TTL = 24 * time.Hour
	}
	r.policy.Store(policy)
}

func (r *Resolver) Policy() Policy {
	if r == nil {
		return DefaultPolicy()
	}
	if value := r.policy.Load(); value != nil {
		return value.(Policy)
	}
	return DefaultPolicy()
}

// Request carries the pieces needed to resolve affinity without importing gin.
type Request struct {
	ClientKeyID    uint64
	ClientIP       string
	UserAgent      string
	Headers        map[string]string // lower-case header names
	Explicit       string            // body prompt_cache_key
	User           string            // body user
	MetadataUserID string            // body metadata.user_id
}

// Resolve returns a stable affinity id, or "" when disabled and no client id is present.
func (r *Resolver) Resolve(ctx context.Context, req Request) (string, error) {
	policy := r.Policy()

	// Always honor explicit client session identifiers first (even if feature disabled,
	// so true client-provided keys still flow through).
	if id := firstNonEmpty(
		req.Explicit,
		req.User,
		req.MetadataUserID,
		header(req.Headers, "x-grok-conv-id"),
		header(req.Headers, "x-grok-conversation-id"),
		header(req.Headers, "x-claude-code-session-id"),
		header(req.Headers, "session-id"),
		header(req.Headers, "x-session-id"),
		header(req.Headers, "x-codex-window-id"),
		header(req.Headers, "x-codex-session-id"),
	); id != "" {
		return id, nil
	}

	if !policy.Enabled || !policy.Fingerprint || r == nil || r.store == nil {
		return "", nil
	}

	fp := fingerprint(req.ClientKeyID, req.ClientIP, req.UserAgent)
	if fp == "" {
		return "", nil
	}
	newID, err := newAffinityID()
	if err != nil {
		return "", err
	}
	return r.store.GetOrCreate(ctx, fp, newID, policy.TTL, policy.Expire)
}

func header(headers map[string]string, name string) string {
	if headers == nil {
		return ""
	}
	return strings.TrimSpace(headers[strings.ToLower(name)])
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func fingerprint(clientKeyID uint64, clientIP, userAgent string) string {
	ip := strings.TrimSpace(clientIP)
	// Strip port from host:port if present.
	if host, _, err := net.SplitHostPort(ip); err == nil {
		ip = host
	}
	ua := strings.TrimSpace(userAgent)
	if clientKeyID == 0 && ip == "" && ua == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d\n%s\n%s", clientKeyID, ip, ua)))
	return hex.EncodeToString(sum[:])
}

func newAffinityID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "xai_" + hex.EncodeToString(buf), nil
}
