// Package promptcache resolves a stable xAI conversation affinity id so upstream
// prompt-cache (cached_tokens) can hit across turns.
//
// Priority (first non-empty wins):
//  1. Explicit client session headers / body (Claude Code, Codex, Grok CLI, …)
//  2. previous_response_id → stored turn linkage (same conversation continues)
//  3. Conversation seed from system + first user message (stable multi-turn chats)
//  4. Fingerprint of client key + IP + User-Agent (last-resort client-wide key)
//
// The resolved value is forwarded as x-grok-conv-id and body prompt_cache_key.
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
	ClientKeyID        uint64
	ClientIP           string
	UserAgent          string
	Headers            map[string]string // lower-case header names
	Explicit           string            // body prompt_cache_key
	User               string            // body user
	MetadataUserID     string            // body metadata.user_id
	PreviousResponseID string            // body previous_response_id (Responses multi-turn)
	ConversationSeed   string            // stable seed from system + first user message
}

// Resolve returns a stable affinity id, or "" when disabled and no client id is present.
func (r *Resolver) Resolve(ctx context.Context, req Request) (string, error) {
	policy := r.Policy()

	// 1) Always honor explicit client session identifiers first (even if feature disabled).
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
		header(req.Headers, "x-conversation-id"),
		header(req.Headers, "conversation-id"),
		header(req.Headers, "x-client-request-id"),
		header(req.Headers, "x-openwebui-chat-id"),
	); id != "" {
		return normalizeAffinityID(id), nil
	}

	if !policy.Enabled || r == nil || r.store == nil {
		return "", nil
	}

	// 2) Continue the same cache key when the client continues a stored response.
	if prev := strings.TrimSpace(req.PreviousResponseID); prev != "" && req.ClientKeyID > 0 {
		fp := turnFingerprint(req.ClientKeyID, prev)
		// Reuse existing mapping only — do not create a new random id for a previous_response_id
		// that we have never seen (would fragment cache). Fall through to seed/fingerprint.
		if id, err := r.lookupOnly(ctx, fp, policy); err != nil {
			return "", err
		} else if id != "" {
			return id, nil
		}
	}

	// 3) Conversation seed (system + first user) keeps multi-turn chats sticky without session headers.
	if seed := strings.TrimSpace(req.ConversationSeed); seed != "" && req.ClientKeyID > 0 {
		fp := seedFingerprint(req.ClientKeyID, seed)
		return r.getOrCreate(ctx, fp, policy)
	}

	// 4) Last resort: client-wide fingerprint (Key+IP+UA).
	if !policy.Fingerprint {
		return "", nil
	}
	fp := fingerprint(req.ClientKeyID, req.ClientIP, req.UserAgent)
	if fp == "" {
		return "", nil
	}
	return r.getOrCreate(ctx, "client:"+fp, policy)
}

// RememberTurn links a completed response id to the affinity key used for that request
// so the next turn with previous_response_id reuses the same cache key.
func (r *Resolver) RememberTurn(ctx context.Context, clientKeyID uint64, responseID, affinityID string) error {
	if r == nil || r.store == nil || clientKeyID == 0 {
		return nil
	}
	responseID = strings.TrimSpace(responseID)
	affinityID = strings.TrimSpace(affinityID)
	if responseID == "" || affinityID == "" {
		return nil
	}
	policy := r.Policy()
	if !policy.Enabled {
		return nil
	}
	_, err := r.store.GetOrCreate(ctx, turnFingerprint(clientKeyID, responseID), normalizeAffinityID(affinityID), policy.TTL, policy.Expire)
	return err
}

func (r *Resolver) getOrCreate(ctx context.Context, fingerprint string, policy Policy) (string, error) {
	newID, err := newAffinityID()
	if err != nil {
		return "", err
	}
	id, err := r.store.GetOrCreate(ctx, fingerprint, newID, policy.TTL, policy.Expire)
	if err != nil {
		return "", err
	}
	return normalizeAffinityID(id), nil
}

// lookupOnly reads an existing mapping without creating a new affinity id.
func (r *Resolver) lookupOnly(ctx context.Context, fingerprint string, policy Policy) (string, error) {
	type looker interface {
		Lookup(ctx context.Context, fingerprint string, now time.Time) (string, bool, error)
	}
	if l, ok := r.store.(looker); ok {
		id, found, err := l.Lookup(ctx, fingerprint, time.Now().UTC())
		if err != nil || !found || id == "" {
			return "", err
		}
		// Sliding TTL refresh when expire is on.
		_, _ = r.store.GetOrCreate(ctx, fingerprint, id, policy.TTL, policy.Expire)
		return normalizeAffinityID(id), nil
	}
	return "", nil
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

func turnFingerprint(clientKeyID uint64, responseID string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("turn\n%d\n%s", clientKeyID, strings.TrimSpace(responseID))))
	return "turn:" + hex.EncodeToString(sum[:])
}

func seedFingerprint(clientKeyID uint64, seed string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("seed\n%d\n%s", clientKeyID, seed)))
	return "seed:" + hex.EncodeToString(sum[:])
}

// ConversationSeedFromMessages builds a stable seed from system + first user text.
// Used when clients resend full history without session headers (Chat / Messages).
func ConversationSeedFromMessages(systemText string, messages []MessageSeed) string {
	systemText = compressSeedText(systemText)
	firstUser := ""
	for _, message := range messages {
		role := strings.ToLower(strings.TrimSpace(message.Role))
		if role == "user" {
			firstUser = compressSeedText(message.Text)
			break
		}
	}
	if systemText == "" && firstUser == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(systemText + "\n---\n" + firstUser))
	return hex.EncodeToString(sum[:16])
}

// MessageSeed is a minimal message view for seeding.
type MessageSeed struct {
	Role string
	Text string
}

func compressSeedText(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) > 512 {
		value = value[:512]
	}
	return value
}

func normalizeAffinityID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	// Cap length for upstream header safety.
	if len(id) > 128 {
		sum := sha256.Sum256([]byte(id))
		return "xai_" + hex.EncodeToString(sum[:16])
	}
	return id
}

func newAffinityID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "xai_" + hex.EncodeToString(buf), nil
}
