package repository

import (
	"context"
	"time"

	"github.com/chenyme/grok2api/backend/internal/domain/account"
	"github.com/chenyme/grok2api/backend/internal/domain/model"
)

// AccountCapabilitySync is one account's model capability snapshot for bulk write.
type AccountCapabilitySync struct {
	AccountID      uint64
	UpstreamModels []string
	SyncedAt       time.Time
}

// ModelRepository 定义公开模型路由持久化能力。
type ModelRepository interface {
	List(ctx context.Context, query ModelListQuery) ([]model.Route, int64, error)
	ListEnabled(ctx context.Context) ([]model.Route, error)
	ListConfiguredEnabled(ctx context.Context) ([]model.Route, error)
	Get(ctx context.Context, id uint64) (model.Route, error)
	GetByPublicID(ctx context.Context, publicID string) (model.Route, error)
	GetByPublicIDCandidates(ctx context.Context, publicID string) ([]model.Route, error)
	// GetByPublicIDIncludingDisabled resolves a public id even when the route is disabled
	// (used so disabled effort-alias rows are not re-listed via registry aliases).
	GetByPublicIDIncludingDisabled(ctx context.Context, publicID string) (model.Route, error)
	// GetConfiguredPublicIDCandidates returns enabled routes without requiring a ready account.
	GetConfiguredPublicIDCandidates(ctx context.Context, publicID string) ([]model.Route, error)
	GetByProviderUpstream(ctx context.Context, provider account.Provider, upstreamModel string) (model.Route, error)
	GetConfiguredByProviderUpstream(ctx context.Context, provider account.Provider, upstreamModel string) (model.Route, error)
	UpsertDiscovered(ctx context.Context, provider account.Provider, upstreamModels []string) error
	UpsertRoutes(ctx context.Context, values []model.Route) error
	ReplaceProviderRoutes(ctx context.Context, provider account.Provider, values []model.Route) error
	ReplaceAccountCapabilities(ctx context.Context, accountID uint64, upstreamModels []string, syncedAt time.Time) error
	// ReplaceAccountCapabilitiesMany bulk-writes capability rows for many accounts.
	// Each item is independent (models may differ by Web tier). Prefer this for static catalogs
	// so admin "同步模型" does not issue one transaction per account across 10k+ Web rows.
	ReplaceAccountCapabilitiesMany(ctx context.Context, items []AccountCapabilitySync) error
	MarkAccountCapabilitySyncFailed(ctx context.Context, accountID uint64, attemptedAt time.Time, message string) error
	HasSuccessfulAccountSync(ctx context.Context, accountID uint64) (bool, error)
	ListStaleAccountSyncIDs(ctx context.Context, before time.Time, limit int) ([]uint64, error)
	Create(ctx context.Context, value model.Route, accountIDs []uint64) (model.Route, error)
	Update(ctx context.Context, value model.Route, accountIDs *[]uint64) (model.Route, error)
	Delete(ctx context.Context, id uint64) error
	DeleteMany(ctx context.Context, ids []uint64) (int64, error)
	UpdateManyEnabled(ctx context.Context, ids []uint64, enabled bool) (int64, error)
}
