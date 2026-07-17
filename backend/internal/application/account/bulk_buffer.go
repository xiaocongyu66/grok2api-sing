package account

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	accountdomain "github.com/chenyme/grok2api/backend/internal/domain/account"
	"github.com/chenyme/grok2api/backend/internal/infra/config"
	"github.com/chenyme/grok2api/backend/internal/repository"
)

// bulkWorkingSet is an optional intermediate cache for bulk account ops.
// Flow: main DB GetMany → buffer (memory + redis/sqlite) → workers read buffer →
// queue writes → batch flush links back to main DB.
type bulkWorkingSet struct {
	enabled bool
	driver  string
	jobID   string

	mu   sync.RWMutex
	byID map[uint64]accountdomain.Credential

	linkMu        sync.Mutex
	pendingLinks  [][2]uint64 // webID, buildID
	deferredLinks bool

	// optional durable backends (nil = memory only)
	redis  *redis.Client
	prefix string
	sqlite *gorm.DB
	dbPath string
}

type bulkCredentialRow struct {
	ID      uint64 `gorm:"primaryKey"`
	Payload []byte `gorm:"type:blob;not null"`
}

func (bulkCredentialRow) TableName() string { return "bulk_credentials" }

// openBulkWorkingSet loads ids (and their linked accounts) from main DB into a
// working set. When dbBuffer is off, still returns an in-memory map from GetMany
// but does not defer writes.
func (s *Service) openBulkWorkingSet(ctx context.Context, ids []uint64) (*bulkWorkingSet, error) {
	ws := &bulkWorkingSet{
		jobID:  fmt.Sprintf("%d", time.Now().UTC().UnixNano()),
		byID:   make(map[uint64]accountdomain.Credential, len(ids)*2),
		driver: "none",
	}
	cfg := s.dbBuffer
	if cfg.Enabled && strings.TrimSpace(cfg.Driver) != "" && strings.ToLower(cfg.Driver) != "none" {
		ws.enabled = true
		ws.driver = strings.ToLower(strings.TrimSpace(cfg.Driver))
		ws.deferredLinks = true
		ws.redis = s.dbBufferRedis
		ws.prefix = s.dbBufferRedisPrefix
		if err := ws.openBackend(cfg); err != nil {
			s.logger.Warn("db_buffer_backend_open_failed", "driver", ws.driver, "error", err)
			// Fall back to memory-only buffering; still reduce main-DB re-reads.
			ws.driver = "memory"
			ws.sqlite = nil
		}
	}

	if len(ids) == 0 {
		return ws, nil
	}
	preloaded, err := s.accounts.GetMany(ctx, ids)
	if err != nil {
		return nil, err
	}
	ws.putMany(preloaded)

	// Prefetch linked Build accounts in one round-trip so convert workers avoid N Gets.
	linked := make([]uint64, 0, len(preloaded))
	seen := make(map[uint64]struct{}, len(preloaded))
	for _, value := range preloaded {
		if value.LinkedAccountID == 0 {
			continue
		}
		if _, ok := seen[value.LinkedAccountID]; ok {
			continue
		}
		if _, ok := ws.byID[value.LinkedAccountID]; ok {
			continue
		}
		seen[value.LinkedAccountID] = struct{}{}
		linked = append(linked, value.LinkedAccountID)
	}
	if len(linked) > 0 {
		more, getErr := s.accounts.GetMany(ctx, linked)
		if getErr == nil {
			ws.putMany(more)
		} else {
			s.logger.Warn("db_buffer_linked_prefetch_failed", "count", len(linked), "error", getErr)
		}
	}

	if ws.enabled {
		if err := ws.persistBackend(ctx); err != nil {
			s.logger.Warn("db_buffer_backend_store_failed", "driver", ws.driver, "error", err)
		}
		s.logger.Info("db_buffer_loaded",
			"driver", ws.driver,
			"accounts", len(ws.byID),
			"requested", len(ids),
			"linked_prefetched", len(linked),
		)
	}
	return ws, nil
}

func (ws *bulkWorkingSet) openBackend(cfg config.DBBufferConfig) error {
	switch ws.driver {
	case "memory", "":
		ws.driver = "memory"
		return nil
	case "sqlite":
		path := strings.TrimSpace(cfg.Path)
		if path == "" {
			path = filepath.Join(os.TempDir(), "grok2api-bulk-buffer.db")
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil && !os.IsExist(err) {
			// path may be a bare filename; ignore
		}
		db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
		if err != nil {
			return fmt.Errorf("open sqlite buffer: %w", err)
		}
		if err := db.AutoMigrate(&bulkCredentialRow{}); err != nil {
			sqlDB, _ := db.DB()
			if sqlDB != nil {
				_ = sqlDB.Close()
			}
			return fmt.Errorf("migrate sqlite buffer: %w", err)
		}
		ws.sqlite = db
		ws.dbPath = path
		return nil
	case "redis":
		if ws.redis == nil {
			return fmt.Errorf("redis client not configured for dbBuffer")
		}
		return nil
	default:
		return fmt.Errorf("unsupported dbBuffer driver %q", ws.driver)
	}
}

// SetDBBufferRedis attaches a Redis client used when driver=redis.
func (s *Service) SetDBBufferRedis(client *redis.Client, keyPrefix string) {
	if s == nil {
		return
	}
	s.dbBufferRedis = client
	s.dbBufferRedisPrefix = keyPrefix
}

func (ws *bulkWorkingSet) putMany(values []accountdomain.Credential) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	for _, value := range values {
		ws.byID[value.ID] = value
	}
}

func (ws *bulkWorkingSet) get(id uint64) (accountdomain.Credential, bool) {
	ws.mu.RLock()
	defer ws.mu.RUnlock()
	value, ok := ws.byID[id]
	return value, ok
}

func (ws *bulkWorkingSet) put(value accountdomain.Credential) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	ws.byID[value.ID] = value
}

func (ws *bulkWorkingSet) snapshot() map[uint64]accountdomain.Credential {
	ws.mu.RLock()
	defer ws.mu.RUnlock()
	out := make(map[uint64]accountdomain.Credential, len(ws.byID))
	for id, value := range ws.byID {
		out[id] = value
	}
	return out
}

func (ws *bulkWorkingSet) queueLink(webID, buildID uint64) {
	ws.linkMu.Lock()
	defer ws.linkMu.Unlock()
	ws.pendingLinks = append(ws.pendingLinks, [2]uint64{webID, buildID})
}

func (ws *bulkWorkingSet) persistBackend(ctx context.Context) error {
	ws.mu.RLock()
	values := make([]accountdomain.Credential, 0, len(ws.byID))
	for _, value := range ws.byID {
		values = append(values, value)
	}
	ws.mu.RUnlock()
	if len(values) == 0 {
		return nil
	}
	switch {
	case ws.sqlite != nil:
		rows := make([]bulkCredentialRow, 0, len(values))
		for _, value := range values {
			payload, err := json.Marshal(value)
			if err != nil {
				return err
			}
			rows = append(rows, bulkCredentialRow{ID: value.ID, Payload: payload})
		}
		return ws.sqlite.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			// Replace job snapshot: clear then insert (single-writer bulk job).
			if err := tx.Where("1 = 1").Delete(&bulkCredentialRow{}).Error; err != nil {
				return err
			}
			const chunk = 200
			for i := 0; i < len(rows); i += chunk {
				end := i + chunk
				if end > len(rows) {
					end = len(rows)
				}
				if err := tx.Create(rows[i:end]).Error; err != nil {
					return err
				}
			}
			return nil
		})
	case ws.redis != nil:
		pipe := ws.redis.Pipeline()
		key := ws.redisKey()
		fields := make(map[string]interface{}, len(values))
		for _, value := range values {
			payload, err := json.Marshal(value)
			if err != nil {
				return err
			}
			fields[strconv.FormatUint(value.ID, 10)] = payload
		}
		pipe.Del(ctx, key)
		if len(fields) > 0 {
			pipe.HSet(ctx, key, fields)
			pipe.Expire(ctx, key, 2*time.Hour)
		}
		_, err := pipe.Exec(ctx)
		return err
	default:
		return nil
	}
}

func (ws *bulkWorkingSet) redisKey() string {
	prefix := strings.TrimSpace(ws.prefix)
	if prefix == "" {
		prefix = "grok2api:"
	}
	return prefix + "dbbuffer:" + ws.jobID
}

// flushLinks batch-applies deferred LinkWebToBuild calls to the main DB.
func (ws *bulkWorkingSet) flushLinks(ctx context.Context, accounts repository.AccountRepository) (int, error) {
	ws.linkMu.Lock()
	links := append([][2]uint64(nil), ws.pendingLinks...)
	ws.pendingLinks = nil
	ws.linkMu.Unlock()
	if len(links) == 0 {
		return 0, nil
	}
	// Dedup by web ID (last wins).
	byWeb := make(map[uint64]uint64, len(links))
	order := make([]uint64, 0, len(links))
	for _, pair := range links {
		if _, ok := byWeb[pair[0]]; !ok {
			order = append(order, pair[0])
		}
		byWeb[pair[0]] = pair[1]
	}
	applied := 0
	var firstErr error
	for _, webID := range order {
		buildID := byWeb[webID]
		if err := accounts.LinkWebToBuild(ctx, webID, buildID); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		applied++
	}
	return applied, firstErr
}

func (ws *bulkWorkingSet) close(ctx context.Context) {
	if ws == nil {
		return
	}
	if ws.redis != nil && ws.jobID != "" {
		_ = ws.redis.Del(ctx, ws.redisKey()).Err()
	}
	if ws.sqlite != nil {
		sqlDB, err := ws.sqlite.DB()
		if err == nil && sqlDB != nil {
			_ = sqlDB.Close()
		}
		ws.sqlite = nil
	}
}
