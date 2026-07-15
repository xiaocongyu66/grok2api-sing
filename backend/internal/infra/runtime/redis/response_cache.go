package redis

import (
	"context"
	"encoding/json"
	"time"

	inferencedomain "github.com/chenyme/grok2api/backend/internal/domain/inference"
	"github.com/chenyme/grok2api/backend/internal/repository"
)

// ResponseStateCache wraps a ResponseRepository and caches WebResponseState in Redis
// (new-api style: read-through + write-through, DB remains source of truth).
// Ownership rows stay DB-only; only the hot previous_response_id path is cached.
type ResponseStateCache struct {
	inner  repository.ResponseRepository
	store  *Store
	// maxCacheTTL caps Redis TTL even when the row expires later (avoid huge keys).
	maxCacheTTL time.Duration
}

func NewResponseStateCache(inner repository.ResponseRepository, store *Store) *ResponseStateCache {
	return &ResponseStateCache{inner: inner, store: store, maxCacheTTL: 24 * time.Hour}
}

func (c *ResponseStateCache) Save(ctx context.Context, value inferencedomain.ResponseOwnership) error {
	return c.inner.Save(ctx, value)
}

func (c *ResponseStateCache) Get(ctx context.Context, responseID string, clientKeyID uint64, now time.Time) (inferencedomain.ResponseOwnership, error) {
	return c.inner.Get(ctx, responseID, clientKeyID, now)
}

func (c *ResponseStateCache) Delete(ctx context.Context, responseID string, clientKeyID uint64) error {
	return c.inner.Delete(ctx, responseID, clientKeyID)
}

func (c *ResponseStateCache) DeleteExpired(ctx context.Context, now time.Time) (int64, error) {
	return c.inner.DeleteExpired(ctx, now)
}

func (c *ResponseStateCache) SaveWebState(ctx context.Context, value inferencedomain.WebResponseState) error {
	if err := c.inner.SaveWebState(ctx, value); err != nil {
		return err
	}
	c.cacheWebState(ctx, value)
	return nil
}

func (c *ResponseStateCache) GetWebState(ctx context.Context, responseID string, now time.Time) (inferencedomain.WebResponseState, error) {
	if value, ok := c.loadWebState(ctx, responseID, now); ok {
		return value, nil
	}
	value, err := c.inner.GetWebState(ctx, responseID, now)
	if err != nil {
		return inferencedomain.WebResponseState{}, err
	}
	c.cacheWebState(ctx, value)
	return value, nil
}

func (c *ResponseStateCache) DeleteWebState(ctx context.Context, responseID string) error {
	err := c.inner.DeleteWebState(ctx, responseID)
	_ = c.store.client.Del(ctx, c.store.key("web-state", responseID)).Err()
	return err
}

func (c *ResponseStateCache) loadWebState(ctx context.Context, responseID string, now time.Time) (inferencedomain.WebResponseState, bool) {
	payload, err := c.store.client.Get(ctx, c.store.key("web-state", responseID)).Bytes()
	if err != nil {
		return inferencedomain.WebResponseState{}, false
	}
	var value inferencedomain.WebResponseState
	if json.Unmarshal(payload, &value) != nil {
		return inferencedomain.WebResponseState{}, false
	}
	if value.ResponseID == "" || !now.Before(value.ExpiresAt) {
		_ = c.store.client.Del(ctx, c.store.key("web-state", responseID)).Err()
		return inferencedomain.WebResponseState{}, false
	}
	return value, true
}

func (c *ResponseStateCache) cacheWebState(ctx context.Context, value inferencedomain.WebResponseState) {
	ttl := time.Until(value.ExpiresAt)
	if ttl <= 0 {
		return
	}
	if c.maxCacheTTL > 0 && ttl > c.maxCacheTTL {
		ttl = c.maxCacheTTL
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return
	}
	// Best-effort cache; failures must not break the request path.
	_ = c.store.client.Set(ctx, c.store.key("web-state", value.ResponseID), payload, ttl).Err()
}

var _ repository.ResponseRepository = (*ResponseStateCache)(nil)
