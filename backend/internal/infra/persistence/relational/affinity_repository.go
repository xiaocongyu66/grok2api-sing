package relational

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// AffinityStore persists fingerprint → affinity-id mappings in SQL.
// Used as durable source of truth; Redis/memory may cache in front.
type AffinityStore struct {
	db *Database
}

func NewAffinityStore(db *Database) *AffinityStore {
	return &AffinityStore{db: db}
}

// GetOrCreate returns an existing non-expired affinity id or inserts newID.
// When expire is false, expires_at is stored as NULL (never expire).
func (s *AffinityStore) GetOrCreate(ctx context.Context, fingerprint, newID string, ttl time.Duration, expire bool) (string, error) {
	if s == nil || s.db == nil || fingerprint == "" {
		return newID, nil
	}
	now := time.Now().UTC()
	var existing promptCacheAffinityModel
	err := s.db.db.WithContext(ctx).
		Where("fingerprint = ?", fingerprint).
		First(&existing).Error
	if err == nil {
		if existing.ExpiresAt != nil && !existing.ExpiresAt.After(now) {
			// Expired row: replace with a new mapping.
			_ = s.db.db.WithContext(ctx).Where("fingerprint = ?", fingerprint).Delete(&promptCacheAffinityModel{}).Error
		} else {
			// Sliding TTL refresh when expire is enabled.
			if expire && ttl > 0 {
				expires := now.Add(ttl)
				_ = s.db.db.WithContext(ctx).Model(&promptCacheAffinityModel{}).
					Where("fingerprint = ?", fingerprint).
					Updates(map[string]any{"expires_at": expires, "updated_at": now}).Error
			}
			return existing.AffinityID, nil
		}
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return "", err
	}

	if newID == "" {
		return "", errors.New("affinity id is required")
	}
	row := promptCacheAffinityModel{
		Fingerprint: fingerprint,
		AffinityID:  newID,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if expire && ttl > 0 {
		expires := now.Add(ttl)
		row.ExpiresAt = &expires
	}
	// Concurrent first writers: insert-or-ignore, then re-read winner.
	createErr := s.db.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error
	if createErr != nil {
		return "", createErr
	}
	var winner promptCacheAffinityModel
	if err := s.db.db.WithContext(ctx).Where("fingerprint = ?", fingerprint).First(&winner).Error; err != nil {
		// Race deleted the row; return the id we intended to store.
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return newID, nil
		}
		return "", err
	}
	if winner.ExpiresAt != nil && !winner.ExpiresAt.After(now) {
		return newID, nil
	}
	return winner.AffinityID, nil
}

// DeleteExpired removes expired affinity rows. Safe to call periodically.
func (s *AffinityStore) DeleteExpired(ctx context.Context, now time.Time) (int64, error) {
	if s == nil || s.db == nil {
		return 0, nil
	}
	result := s.db.db.WithContext(ctx).
		Where("expires_at IS NOT NULL AND expires_at <= ?", now.UTC()).
		Delete(&promptCacheAffinityModel{})
	return result.RowsAffected, result.Error
}

// Lookup returns a non-expired mapping without creating one.
func (s *AffinityStore) Lookup(ctx context.Context, fingerprint string, now time.Time) (string, bool, error) {
	if s == nil || s.db == nil || fingerprint == "" {
		return "", false, nil
	}
	var row promptCacheAffinityModel
	err := s.db.db.WithContext(ctx).Where("fingerprint = ?", fingerprint).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if row.ExpiresAt != nil && !row.ExpiresAt.After(now.UTC()) {
		return "", false, nil
	}
	if row.AffinityID == "" {
		return "", false, nil
	}
	return row.AffinityID, true, nil
}
