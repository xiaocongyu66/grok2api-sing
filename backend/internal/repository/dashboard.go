package repository

import (
	"context"
	"time"

	"github.com/chenyme/grok2api/backend/internal/domain/dashboard"
)

// DashboardRepository 定义管理台概览所需的只读聚合查询。
type DashboardRepository interface {
	// Snapshot aggregates resources and audit usage between the first/last bucket boundaries.
	// todayStart/todayEnd define the admin calendar-day window for "today" totals (timezone-aware).
	// liveWindow is how far back from snapshotAt to compute RPM/TPM (typically 60s).
	Snapshot(ctx context.Context, bucketBoundaries []time.Time, snapshotAt, todayStart, todayEnd time.Time, liveWindow time.Duration) (dashboard.Aggregate, error)
}
