package memory

import (
	"context"
	"sync"
	"time"
)

type affinityEntry struct {
	id        string
	expiresAt time.Time // zero = never expire
}

// AffinityStore is a process-local hot cache for fingerprint → conv-id.
// Prefer LayeredStore(SQL, memory) so mappings survive process restarts.
type AffinityStore struct {
	mu      sync.Mutex
	entries map[string]affinityEntry
}

func NewAffinityStore() *AffinityStore {
	return &AffinityStore{entries: make(map[string]affinityEntry)}
}

// Lookup returns a non-expired cached id without creating one.
func (s *AffinityStore) Lookup(_ context.Context, fingerprint string) (string, bool, error) {
	if fingerprint == "" {
		return "", false, nil
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[fingerprint]
	if !ok {
		return "", false, nil
	}
	if !entry.expiresAt.IsZero() && !now.Before(entry.expiresAt) {
		delete(s.entries, fingerprint)
		return "", false, nil
	}
	return entry.id, true, nil
}

// Remember writes or refreshes a cache entry.
func (s *AffinityStore) Remember(_ context.Context, fingerprint, id string, ttl time.Duration, expire bool) error {
	if fingerprint == "" || id == "" {
		return nil
	}
	now := time.Now()
	entry := affinityEntry{id: id}
	if expire && ttl > 0 {
		entry.expiresAt = now.Add(ttl)
	}
	s.mu.Lock()
	s.entries[fingerprint] = entry
	if len(s.entries) > 10000 {
		for key, value := range s.entries {
			if !value.expiresAt.IsZero() && !now.Before(value.expiresAt) {
				delete(s.entries, key)
			}
		}
	}
	s.mu.Unlock()
	return nil
}

func (s *AffinityStore) GetOrCreate(_ context.Context, fingerprint, newID string, ttl time.Duration, expire bool) (string, error) {
	if fingerprint == "" {
		return newID, nil
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry, ok := s.entries[fingerprint]; ok {
		if entry.expiresAt.IsZero() || now.Before(entry.expiresAt) {
			if expire && ttl > 0 {
				entry.expiresAt = now.Add(ttl)
				s.entries[fingerprint] = entry
			}
			return entry.id, nil
		}
		delete(s.entries, fingerprint)
	}
	entry := affinityEntry{id: newID}
	if expire && ttl > 0 {
		entry.expiresAt = now.Add(ttl)
	}
	s.entries[fingerprint] = entry
	if len(s.entries) > 10000 {
		for key, value := range s.entries {
			if !value.expiresAt.IsZero() && !now.Before(value.expiresAt) {
				delete(s.entries, key)
			}
		}
	}
	return newID, nil
}
