package account

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	accountdomain "github.com/chenyme/grok2api/backend/internal/domain/account"
	"github.com/chenyme/grok2api/backend/internal/infra/config"
	"github.com/chenyme/grok2api/backend/internal/repository"
)

func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type bulkBufferRepoStub struct {
	byID  map[uint64]accountdomain.Credential
	links [][2]uint64
}

func (r *bulkBufferRepoStub) GetMany(_ context.Context, ids []uint64) ([]accountdomain.Credential, error) {
	out := make([]accountdomain.Credential, 0, len(ids))
	for _, id := range ids {
		if value, ok := r.byID[id]; ok {
			out = append(out, value)
		}
	}
	return out, nil
}

func (r *bulkBufferRepoStub) LinkWebToBuild(_ context.Context, webID, buildID uint64) error {
	r.links = append(r.links, [2]uint64{webID, buildID})
	return nil
}

// Remaining AccountRepository methods are unused by bulk buffer tests.
func (r *bulkBufferRepoStub) List(context.Context, repository.AccountListQuery) ([]accountdomain.Credential, int64, error) {
	return nil, 0, nil
}
func (r *bulkBufferRepoStub) ListProviderAccountBatch(context.Context, accountdomain.Provider, uint64, int) ([]accountdomain.Credential, int64, error) {
	return nil, 0, nil
}
func (r *bulkBufferRepoStub) Summarize(context.Context, time.Time) ([]repository.AccountSummary, error) {
	return nil, nil
}
func (r *bulkBufferRepoStub) ListEnabled(context.Context, accountdomain.Provider) ([]accountdomain.Credential, error) {
	return nil, nil
}
func (r *bulkBufferRepoStub) ListEnabledAccountIDs(context.Context, accountdomain.Provider, bool) ([]uint64, error) {
	return nil, nil
}
func (r *bulkBufferRepoStub) ListFailedAccountIDs(context.Context, accountdomain.Provider, bool, int) ([]uint64, error) {
	return nil, nil
}
func (r *bulkBufferRepoStub) ListProviderAccountIDs(context.Context, accountdomain.Provider, int) ([]uint64, error) {
	return nil, nil
}
func (r *bulkBufferRepoStub) ListSSOAccountsForDedup(context.Context, accountdomain.Provider) ([]accountdomain.Credential, error) {
	return nil, nil
}
func (r *bulkBufferRepoStub) FilterMissingBuildConversionIDs(context.Context, []uint64) ([]uint64, error) {
	return nil, nil
}
func (r *bulkBufferRepoStub) ListUnlinkedWebAccountIDs(context.Context, uint64, int) ([]uint64, int64, error) {
	return nil, 0, nil
}
func (r *bulkBufferRepoStub) ListMissingConsoleSyncAccounts(context.Context, []uint64) ([]accountdomain.Credential, error) {
	return nil, nil
}
func (r *bulkBufferRepoStub) ListMissingConsoleSyncBatch(context.Context, uint64, int) ([]accountdomain.Credential, int64, int64, error) {
	return nil, 0, 0, nil
}
func (r *bulkBufferRepoStub) HasActive(context.Context, accountdomain.Provider) (bool, error) {
	return false, nil
}
func (r *bulkBufferRepoStub) ListRoutingCandidates(context.Context, accountdomain.Provider, string, string) ([]accountdomain.RoutingCandidate, error) {
	return nil, nil
}
func (r *bulkBufferRepoStub) Get(context.Context, uint64) (accountdomain.Credential, error) {
	return accountdomain.Credential{}, repository.ErrNotFound
}
func (r *bulkBufferRepoStub) GetBillings(context.Context, []uint64) (map[uint64]accountdomain.Billing, error) {
	return nil, nil
}
func (r *bulkBufferRepoStub) GetQuotaRecoveries(context.Context, []uint64) (map[uint64]accountdomain.QuotaRecovery, error) {
	return nil, nil
}
func (r *bulkBufferRepoStub) UpsertByIdentity(context.Context, accountdomain.Credential) (accountdomain.Credential, bool, error) {
	return accountdomain.Credential{}, false, nil
}
func (r *bulkBufferRepoStub) Update(context.Context, accountdomain.Credential) (accountdomain.Credential, error) {
	return accountdomain.Credential{}, nil
}
func (r *bulkBufferRepoStub) UpdateMany(context.Context, []uint64, repository.AccountUpdates) (int64, error) {
	return 0, nil
}
func (r *bulkBufferRepoStub) Delete(context.Context, uint64) error { return nil }
func (r *bulkBufferRepoStub) DeleteMany(context.Context, []uint64) (int64, error) {
	return 0, nil
}
func (r *bulkBufferRepoStub) UpdateTokens(context.Context, uint64, string, string, time.Time) (accountdomain.Credential, error) {
	return accountdomain.Credential{}, nil
}
func (r *bulkBufferRepoStub) BackfillCredentialRefreshSchedules(context.Context, time.Time, int) (int, error) {
	return 0, nil
}
func (r *bulkBufferRepoStub) ListCriticalCredentialRefreshIDs(context.Context, time.Time, time.Time, int) ([]uint64, error) {
	return nil, nil
}
func (r *bulkBufferRepoStub) ListDueCredentialRefreshIDs(context.Context, time.Time, int) ([]uint64, error) {
	return nil, nil
}
func (r *bulkBufferRepoStub) NextCredentialRefreshDueAt(context.Context) (*time.Time, error) {
	return nil, nil
}
func (r *bulkBufferRepoStub) UpdateCredentialRefreshFailure(context.Context, uint64, int, time.Time, string, bool) error {
	return nil
}
func (r *bulkBufferRepoStub) UpdateObservedModel(context.Context, uint64, string, time.Time) error {
	return nil
}
func (r *bulkBufferRepoStub) UpdateHealth(context.Context, uint64, int, *time.Time, string, bool) error {
	return nil
}
func (r *bulkBufferRepoStub) UpsertModelQuotaBlock(context.Context, accountdomain.ModelQuotaBlock) error {
	return nil
}
func (r *bulkBufferRepoStub) PruneExpiredModelQuotaBlocks(context.Context, time.Time, int) (int64, error) {
	return 0, nil
}
func (r *bulkBufferRepoStub) SaveBilling(context.Context, accountdomain.Billing) error { return nil }
func (r *bulkBufferRepoStub) GetBilling(context.Context, uint64) (accountdomain.Billing, error) {
	return accountdomain.Billing{}, repository.ErrNotFound
}
func (r *bulkBufferRepoStub) GetQuotaRecovery(context.Context, uint64) (accountdomain.QuotaRecovery, error) {
	return accountdomain.QuotaRecovery{}, repository.ErrNotFound
}
func (r *bulkBufferRepoStub) SaveQuotaRecovery(context.Context, accountdomain.QuotaRecovery) error {
	return nil
}
func (r *bulkBufferRepoStub) ClaimQuotaProbe(context.Context, uint64, time.Time, time.Time) (bool, error) {
	return false, nil
}
func (r *bulkBufferRepoStub) ClearQuotaRecovery(context.Context, uint64) error { return nil }
func (r *bulkBufferRepoStub) HasQuotaWindows(context.Context, uint64) (bool, error) {
	return false, nil
}
func (r *bulkBufferRepoStub) GetQuotaWindows(context.Context, []uint64) (map[uint64][]accountdomain.QuotaWindow, error) {
	return nil, nil
}
func (r *bulkBufferRepoStub) ReplaceQuotaWindows(context.Context, uint64, accountdomain.WebTier, time.Time, []accountdomain.QuotaWindow) error {
	return nil
}
func (r *bulkBufferRepoStub) SaveQuotaWindows(context.Context, uint64, accountdomain.WebTier, time.Time, []accountdomain.QuotaWindow) error {
	return nil
}
func (r *bulkBufferRepoStub) UpsertManyByIdentity(context.Context, []accountdomain.Credential) ([]repository.AccountUpsertResult, error) {
	return nil, nil
}
func (r *bulkBufferRepoStub) DecrementQuotaWindow(context.Context, uint64, string, time.Time) (bool, error) {
	return false, nil
}
func (r *bulkBufferRepoStub) ExhaustQuotaWindow(context.Context, uint64, string, *time.Time, time.Time) error {
	return nil
}
func (r *bulkBufferRepoStub) ListDueQuotaWindows(context.Context, time.Time, int) ([]accountdomain.QuotaWindow, error) {
	return nil, nil
}
func (r *bulkBufferRepoStub) ListQuotaRecoveryWindows(context.Context, int) ([]accountdomain.QuotaWindow, error) {
	return nil, nil
}
func (r *bulkBufferRepoStub) ListStaleWebQuotaAccountIDs(context.Context, time.Time, int) ([]uint64, error) {
	return nil, nil
}

func TestOpenBulkWorkingSetPrefetchesLinked(t *testing.T) {
	repo := &bulkBufferRepoStub{byID: map[uint64]accountdomain.Credential{
		1: {ID: 1, Provider: accountdomain.ProviderWeb, LinkedAccountID: 9, AuthType: accountdomain.AuthTypeSSO},
		9: {ID: 9, Provider: accountdomain.ProviderBuild, SourceKey: "build-9"},
	}}
	svc := &Service{accounts: repo, logger: silentLogger(), dbBuffer: config.DBBufferConfig{Enabled: true, Driver: "memory"}}
	ws, err := svc.openBulkWorkingSet(context.Background(), []uint64{1})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.close(context.Background())
	if !ws.enabled || !ws.deferredLinks {
		t.Fatalf("expected enabled deferred buffer, got enabled=%v deferred=%v", ws.enabled, ws.deferredLinks)
	}
	if _, ok := ws.get(1); !ok {
		t.Fatal("web account missing from buffer")
	}
	if linked, ok := ws.get(9); !ok || linked.SourceKey != "build-9" {
		t.Fatalf("linked build not prefetched: %#v ok=%v", linked, ok)
	}
}

func TestBulkWorkingSetFlushLinksDedups(t *testing.T) {
	repo := &bulkBufferRepoStub{byID: map[uint64]accountdomain.Credential{}}
	ws := &bulkWorkingSet{enabled: true, deferredLinks: true, byID: map[uint64]accountdomain.Credential{}}
	ws.queueLink(1, 10)
	ws.queueLink(1, 11) // last wins
	ws.queueLink(2, 20)
	applied, err := ws.flushLinks(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if applied != 2 {
		t.Fatalf("applied=%d want 2", applied)
	}
	if len(repo.links) != 2 || repo.links[0] != [2]uint64{1, 11} || repo.links[1] != [2]uint64{2, 20} {
		t.Fatalf("links=%v", repo.links)
	}
}

func TestBulkWorkingSetSQLiteBackend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "buffer.db")
	repo := &bulkBufferRepoStub{byID: map[uint64]accountdomain.Credential{
		3: {ID: 3, Provider: accountdomain.ProviderWeb, AuthType: accountdomain.AuthTypeSSO, Email: "a@b.c"},
	}}
	svc := &Service{
		accounts: repo,
		logger:   silentLogger(),
		dbBuffer: config.DBBufferConfig{Enabled: true, Driver: "sqlite", Path: path},
	}
	ws, err := svc.openBulkWorkingSet(context.Background(), []uint64{3})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.close(context.Background())
	if ws.driver != "sqlite" || ws.sqlite == nil {
		t.Fatalf("sqlite backend not opened: driver=%s sqlite=%v", ws.driver, ws.sqlite != nil)
	}
	value, ok := ws.get(3)
	if !ok || value.Email != "a@b.c" {
		t.Fatalf("buffer miss: %#v ok=%v", value, ok)
	}
}
