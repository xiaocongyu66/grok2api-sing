package relational

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestAffinityStoreSQLRoundTrip(t *testing.T) {
	ctx := context.Background()
	db, err := OpenSQLite(ctx, filepath.Join(t.TempDir(), "affinity.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.InitializeSchema(ctx); err != nil {
		t.Fatal(err)
	}
	store := NewAffinityStore(db)
	fp := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	first, err := store.GetOrCreate(ctx, fp, "xai_first", time.Hour, true)
	if err != nil || first != "xai_first" {
		t.Fatalf("first=%q err=%v", first, err)
	}
	second, err := store.GetOrCreate(ctx, fp, "xai_other", time.Hour, true)
	if err != nil || second != "xai_first" {
		t.Fatalf("second=%q want xai_first err=%v", second, err)
	}
	// Concurrent-style: different fingerprint gets its own id.
	other, err := store.GetOrCreate(ctx, "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", "xai_second", time.Hour, true)
	if err != nil || other != "xai_second" {
		t.Fatalf("other=%q err=%v", other, err)
	}
}

func TestAffinityStoreNeverExpireAndCleanup(t *testing.T) {
	ctx := context.Background()
	db, err := OpenSQLite(ctx, filepath.Join(t.TempDir(), "affinity2.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.InitializeSchema(ctx); err != nil {
		t.Fatal(err)
	}
	store := NewAffinityStore(db)
	fp := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	id, err := store.GetOrCreate(ctx, fp, "xai_keep", time.Hour, false)
	if err != nil || id != "xai_keep" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	// Force-expired row for cleanup path.
	expiredFP := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	past := time.Now().UTC().Add(-time.Hour)
	if err := db.db.WithContext(ctx).Create(&promptCacheAffinityModel{
		Fingerprint: expiredFP, AffinityID: "xai_old", ExpiresAt: &past,
		CreatedAt: past, UpdatedAt: past,
	}).Error; err != nil {
		t.Fatal(err)
	}
	deleted, err := store.DeleteExpired(ctx, time.Now().UTC())
	if err != nil || deleted != 1 {
		t.Fatalf("deleted=%d err=%v", deleted, err)
	}
	// Never-expire row still present.
	got, err := store.GetOrCreate(ctx, fp, "xai_new", time.Hour, false)
	if err != nil || got != "xai_keep" {
		t.Fatalf("got=%q want xai_keep err=%v", got, err)
	}
}
