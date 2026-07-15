package redis

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	clientkeydomain "github.com/chenyme/grok2api/backend/internal/domain/clientkey"
	redisclient "github.com/redis/go-redis/v9"
)

// TokenCacheTTL matches new-api default SYNC_FREQUENCY (60s) for API key objects.
const TokenCacheTTL = 60 * time.Second

// TokenCache is a new-api style Redis cache for client API key auth records.
// Key layout: {prefix}token:{clientKeyPrefix}
// Values are JSON-serialized Key rows without EncryptedSecret.
// Billing-limited keys are never stored (spend counters must stay fresh from DB).
type TokenCache struct {
	store *Store
	ttl   time.Duration
}

func NewTokenCache(store *Store) *TokenCache {
	return &TokenCache{store: store, ttl: TokenCacheTTL}
}

func (c *TokenCache) Get(ctx context.Context, prefix string) (clientkeydomain.Key, bool, error) {
	if prefix == "" {
		return clientkeydomain.Key{}, false, nil
	}
	payload, err := c.store.client.Get(ctx, c.store.key("token", prefix)).Bytes()
	if errors.Is(err, redisclient.Nil) {
		return clientkeydomain.Key{}, false, nil
	}
	if err != nil {
		return clientkeydomain.Key{}, false, err
	}
	var value clientkeydomain.Key
	if err := json.Unmarshal(payload, &value); err != nil {
		_ = c.store.client.Del(ctx, c.store.key("token", prefix)).Err()
		return clientkeydomain.Key{}, false, nil
	}
	if value.ID == 0 || value.Prefix == "" || value.SecretHash == "" {
		return clientkeydomain.Key{}, false, nil
	}
	// Never serve billing-limited keys from cache (quota may have changed).
	if value.BillingLimitUSDTicks > 0 {
		_ = c.Delete(ctx, prefix)
		return clientkeydomain.Key{}, false, nil
	}
	value.EncryptedSecret = ""
	value.AllowedModels = append([]uint64(nil), value.AllowedModels...)
	return value, true, nil
}

func (c *TokenCache) Set(ctx context.Context, value clientkeydomain.Key) error {
	if value.Prefix == "" || value.BillingLimitUSDTicks > 0 {
		return nil
	}
	value.EncryptedSecret = ""
	value.AllowedModels = append([]uint64(nil), value.AllowedModels...)
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	ttl := c.ttl
	if ttl <= 0 {
		ttl = TokenCacheTTL
	}
	return c.store.client.Set(ctx, c.store.key("token", value.Prefix), payload, ttl).Err()
}

func (c *TokenCache) Delete(ctx context.Context, prefix string) error {
	if prefix == "" {
		return nil
	}
	return c.store.client.Del(ctx, c.store.key("token", prefix)).Err()
}

func (c *TokenCache) DeleteMany(ctx context.Context, prefixes []string) error {
	if len(prefixes) == 0 {
		return nil
	}
	keys := make([]string, 0, len(prefixes))
	for _, prefix := range prefixes {
		if prefix == "" {
			continue
		}
		keys = append(keys, c.store.key("token", prefix))
	}
	if len(keys) == 0 {
		return nil
	}
	return c.store.client.Del(ctx, keys...).Err()
}
