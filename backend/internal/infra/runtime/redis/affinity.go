package redis

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	redisclient "github.com/redis/go-redis/v9"
)

// AffinityStore maps request fingerprints to stable xAI conversation affinity ids.
// Key: {prefix}prompt-cache-affinity:{fingerprint}
type AffinityStore struct {
	store *Store
}

func NewAffinityStore(store *Store) *AffinityStore {
	return &AffinityStore{store: store}
}

// GetOrCreate returns a persisted affinity id for fingerprint.
// When expire is false the key is stored without a short TTL (10y safety cap).
func (a *AffinityStore) GetOrCreate(ctx context.Context, fingerprint, newID string, ttl time.Duration, expire bool) (string, error) {
	if a == nil || a.store == nil || fingerprint == "" {
		return newID, nil
	}
	key := a.store.key("prompt-cache-affinity", fingerprint)
	// Fast path: existing mapping.
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
	// SET NX so concurrent first requests share one id.
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
