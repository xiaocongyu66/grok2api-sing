package promptcache

import (
	"context"
	"time"
)

// Cache is an optional hot layer in front of durable SQL storage.
// Misses must not invent ids — LayeredStore always consults Durable first.
type Cache interface {
	Lookup(ctx context.Context, fingerprint string) (id string, ok bool, err error)
	Remember(ctx context.Context, fingerprint, id string, ttl time.Duration, expire bool) error
}

// DurableLookup is optional on durable stores for non-creating reads.
type DurableLookup interface {
	Lookup(ctx context.Context, fingerprint string, now time.Time) (string, bool, error)
}

// LayeredStore: Redis/memory cache → SQL durable GetOrCreate.
// SQL is source of truth across restarts and multi-instance deploys.
type LayeredStore struct {
	cache   Cache // optional
	durable Store
}

func NewLayeredStore(durable Store, cache Cache) *LayeredStore {
	return &LayeredStore{durable: durable, cache: cache}
}

func (s *LayeredStore) GetOrCreate(ctx context.Context, fingerprint, newID string, ttl time.Duration, expire bool) (string, error) {
	if fingerprint == "" {
		return newID, nil
	}
	// Hot path: cache hit.
	if s.cache != nil {
		if id, ok, err := s.cache.Lookup(ctx, fingerprint); err == nil && ok && id != "" {
			// Refresh cache TTL on hit when expire is on.
			if expire && ttl > 0 {
				_ = s.cache.Remember(ctx, fingerprint, id, ttl, expire)
			}
			return id, nil
		}
	}
	if s.durable == nil {
		return newID, nil
	}
	id, err := s.durable.GetOrCreate(ctx, fingerprint, newID, ttl, expire)
	if err != nil {
		return "", err
	}
	if id != "" && s.cache != nil {
		_ = s.cache.Remember(ctx, fingerprint, id, ttl, expire)
	}
	return id, nil
}

// Lookup returns an existing mapping without creating one.
func (s *LayeredStore) Lookup(ctx context.Context, fingerprint string, now time.Time) (string, bool, error) {
	if fingerprint == "" {
		return "", false, nil
	}
	if s.cache != nil {
		if id, ok, err := s.cache.Lookup(ctx, fingerprint); err == nil && ok && id != "" {
			return id, true, nil
		}
	}
	if looker, ok := s.durable.(DurableLookup); ok {
		id, found, err := looker.Lookup(ctx, fingerprint, now)
		if err != nil || !found {
			return "", found, err
		}
		if s.cache != nil {
			_ = s.cache.Remember(ctx, fingerprint, id, 24*time.Hour, true)
		}
		return id, true, nil
	}
	return "", false, nil
}
