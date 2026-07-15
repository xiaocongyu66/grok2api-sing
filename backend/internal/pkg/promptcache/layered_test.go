package promptcache

import (
	"context"
	"testing"
	"time"
)

type mapStore struct {
	values map[string]string
	gets   int
}

func (m *mapStore) GetOrCreate(_ context.Context, fingerprint, newID string, _ time.Duration, _ bool) (string, error) {
	m.gets++
	if m.values == nil {
		m.values = map[string]string{}
	}
	if existing, ok := m.values[fingerprint]; ok {
		return existing, nil
	}
	m.values[fingerprint] = newID
	return newID, nil
}

type mapCache struct {
	values map[string]string
}

func (m *mapCache) Lookup(_ context.Context, fingerprint string) (string, bool, error) {
	if m.values == nil {
		return "", false, nil
	}
	id, ok := m.values[fingerprint]
	return id, ok && id != "", nil
}

func (m *mapCache) Remember(_ context.Context, fingerprint, id string, _ time.Duration, _ bool) error {
	if m.values == nil {
		m.values = map[string]string{}
	}
	m.values[fingerprint] = id
	return nil
}

func TestLayeredStoreCacheHitSkipsDurable(t *testing.T) {
	durable := &mapStore{values: map[string]string{"fp": "xai_from_sql"}}
	cache := &mapCache{values: map[string]string{"fp": "xai_from_cache"}}
	store := NewLayeredStore(durable, cache)
	id, err := store.GetOrCreate(context.Background(), "fp", "xai_new", time.Hour, true)
	if err != nil || id != "xai_from_cache" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	if durable.gets != 0 {
		t.Fatalf("durable should not be hit on cache hit, gets=%d", durable.gets)
	}
}

func TestLayeredStoreFillsCacheFromDurable(t *testing.T) {
	durable := &mapStore{}
	cache := &mapCache{}
	store := NewLayeredStore(durable, cache)
	id, err := store.GetOrCreate(context.Background(), "fp", "xai_created", time.Hour, true)
	if err != nil || id != "xai_created" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	if cached, ok, _ := cache.Lookup(context.Background(), "fp"); !ok || cached != "xai_created" {
		t.Fatalf("cache not filled: %q ok=%v", cached, ok)
	}
	// Second call hits cache.
	id2, err := store.GetOrCreate(context.Background(), "fp", "xai_other", time.Hour, true)
	if err != nil || id2 != "xai_created" {
		t.Fatalf("id2=%q err=%v", id2, err)
	}
	if durable.gets != 1 {
		t.Fatalf("durable gets=%d want 1", durable.gets)
	}
}
