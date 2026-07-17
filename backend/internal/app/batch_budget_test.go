package app

import (
	"log/slog"
	"testing"
	"time"

	"github.com/chenyme/grok2api/backend/internal/infra/config"
)

func TestMaxBatchConcurrencyUsesConfiguredValues(t *testing.T) {
	got := maxBatchConcurrency(config.BatchConfig{
		ImportConcurrency: 3, ConversionConcurrency: 7,
		SyncConcurrency: 5, RefreshConcurrency: 12,
	})
	if got != 12 {
		t.Fatalf("max = %d want 12", got)
	}
}

func TestWarnBatchVsPostgresDoesNotPanic(t *testing.T) {
	// Smoke: high settings only warn; concurrency stays as configured by caller.
	cfg := config.Config{
		Database: config.DatabaseConfig{
			Driver:   "postgres",
			Postgres: config.PostgresDatabaseConfig{MaxOpenConns: 20},
		},
		Batch: config.BatchConfig{
			ImportConcurrency: 25, ConversionConcurrency: 25,
			SyncConcurrency: 25, RefreshConcurrency: 25,
		},
	}
	warnBatchVsPostgres(slog.Default(), cfg)
	if cfg.Batch.RefreshConcurrency != 25 {
		t.Fatalf("settings must not be mutated, got refresh=%d", cfg.Batch.RefreshConcurrency)
	}
}

func TestEffectiveBatchConfigCapsAgainstPostgres(t *testing.T) {
	cfg := config.Config{
		Database: config.DatabaseConfig{
			Driver:   "postgres",
			Postgres: config.PostgresDatabaseConfig{MaxOpenConns: 20},
		},
		Batch: config.BatchConfig{
			ImportConcurrency: 32, ConversionConcurrency: 25,
			SyncConcurrency: 25, RefreshConcurrency: 25,
			RandomDelay: config.Duration(500 * time.Millisecond),
		},
	}
	got := effectiveBatchConfig(cfg)
	// budget = maxOpen/3 = 6
	if got.ImportConcurrency != 6 || got.RefreshConcurrency != 6 {
		t.Fatalf("effective = %+v, want import/refresh capped at 6", got)
	}
	if cfg.Batch.ImportConcurrency != 32 || cfg.Batch.RefreshConcurrency != 25 {
		t.Fatalf("settings must stay unchanged, got %+v", cfg.Batch)
	}
}

func TestEffectiveBatchConfigLeavesSQLiteAlone(t *testing.T) {
	cfg := config.Config{
		Database: config.DatabaseConfig{Driver: "sqlite"},
		Batch: config.BatchConfig{
			ImportConcurrency: 20, ConversionConcurrency: 10,
			SyncConcurrency: 10, RefreshConcurrency: 15,
		},
	}
	got := effectiveBatchConfig(cfg)
	if got != cfg.Batch {
		t.Fatalf("sqlite should not clamp, got %+v", got)
	}
}
