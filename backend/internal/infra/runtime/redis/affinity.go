package redis

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	redisclient "github.com/redis/go-redis/v9"
)

// AffinityStore is a Redis hot cache for fingerprint → xAI affinity ids.
// Prefer LayeredStore(SQL, Redis) so SQL remains durable across Redis flushes.
// Key: {prefix}prompt-cache-affinity:{fingerprint}
type AffinityStore struct {
	store *Store
}

func NewAffinityStore(store *Store) *AffinityStore {
	return &AffinityStore{store: store}
}

// Lookup returns a cached affinity id without creating one.
func (a *AffinityStore) Lookup(ctx context.Context, fingerprint string) (string, bool, error) {
	if a == nil || a.store == nil || fingerprint == "" {
		return "", false, nil
	}
	value, err := a.store.client.Get(ctx, a.store.key("prompt-cache-affinity", fingerprint)).Result()
	if errors.Is(err, redisclient.Nil) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if value == "" {
		return "", false, nil
	}
	return value, true, nil
}

// Remember writes (or refreshes) a cache entry. Does not invent ids.
func (a *AffinityStore) Remember(ctx context.Context, fingerprint, id string, ttl time.Duration, expire bool) error {
	if a == nil || a.store == nil || fingerprint == "" || id == "" {
		return nil
	}
	return a.store.client.Set(ctx, a.store.key("prompt-cache-affinity", fingerprint), id, affinityTTL(ttl, expire)).Err()
}

// GetOrCreate is kept for standalone Redis-only use (no SQL). Prefer LayeredStore.
func (a *AffinityStore) GetOrCreate(ctx context.Context, fingerprint, newID string, ttl time.Duration, expire bool) (string, error) {
	if a == nil || a.store == nil || fingerprint == "" {
		return newID, nil
	}
	key := a.store.key("prompt-cache-affinity", fingerprint)
	if existing, err := a.store.client.Get(ctx, key).Result(); err == nil && existing != "" {
		if expire && ttl > 0 {
			_ = a.store.client.Expire(ctx, key, ttl).Err()
		}
		return existing, nil
	} else if err != nil && !errors.Is(err, redisclient.Nil) {
		return "", err
	}
	if newID == "" {
		var err error
		newID, err = randomAffinityID()
		if err != nil {
			return "", err
		}
	}
	ok, err := a.store.client.SetNX(ctx, key, newID, affinityTTL(ttl, expire)).Result()
	if err != nil {
		return "", err
	}
	if ok {
		return newID, nil
	}
	existing, err := a.store.client.Get(ctx, key).Result()
	if err != nil {
		return "", err
	}
	if existing == "" {
		return newID, nil
	}
	return existing, nil
}

func affinityTTL(ttl time.Duration, expire bool) time.Duration {
	if !expire {
		// Redis still benefits from a far-future TTL to avoid unbounded growth.
		return 10 * 365 * 24 * time.Hour
	}
	if ttl <= 0 {
		return 24 * time.Hour
	}
	return ttl
}

func randomAffinityID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "xai_" + hex.EncodeToString(buf), nil
}
