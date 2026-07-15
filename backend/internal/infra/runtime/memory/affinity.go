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

// AffinityStore is a process-local fingerprint → conv-id map for single-instance deploys.
type AffinityStore struct {
	mu      sync.Mutex
	entries map[string]affinityEntry
}

func NewAffinityStore() *AffinityStore {
	return &AffinityStore{entries: make(map[string]affinityEntry)}
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
	// Opportunistic cleanup of expired entries when map grows large.
	if len(s.entries) > 10000 {
		for key, value := range s.entries {
			if !value.expiresAt.IsZero() && !now.Before(value.expiresAt) {
				delete(s.entries, key)
			}
		}
	}
	return newID, nil
}
