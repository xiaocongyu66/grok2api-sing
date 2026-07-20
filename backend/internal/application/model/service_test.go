package model

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	accountapp "github.com/chenyme/grok2api/backend/internal/application/account"
	"github.com/chenyme/grok2api/backend/internal/domain/account"
	modeldomain "github.com/chenyme/grok2api/backend/internal/domain/model"
	"github.com/chenyme/grok2api/backend/internal/infra/persistence/relational"
	"github.com/chenyme/grok2api/backend/internal/infra/provider"
	consoleprovider "github.com/chenyme/grok2api/backend/internal/infra/provider/console"
	"github.com/chenyme/grok2api/backend/internal/infra/runtime/memory"
	"github.com/chenyme/grok2api/backend/internal/infra/security"
	"github.com/chenyme/grok2api/backend/internal/repository"
)

func TestModelProviderFilterAcceptsOnlyKnownProviders(t *testing.T) {
	for _, value := range []string{"", string(account.ProviderBuild), string(account.ProviderWeb), string(account.ProviderConsole)} {
		if !validProviderFilter(value) {
			t.Fatalf("known provider rejected: %q", value)
		}
	}
	if validProviderFilter("cli") {
		t.Fatal("unsupported provider filter was accepted")
	}
}

func TestSyncAggregatesCapabilitiesFromAllAccounts(t *testing.T) {
	ctx := context.Background()
	database, err := relational.OpenSQLite(ctx, filepath.Join(t.TempDir(), "model-sync.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.InitializeSchema(ctx); err != nil {
		t.Fatal(err)
	}
	cipher, err := security.NewCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := cipher.Encrypt("access-token")
	if err != nil {
		t.Fatal(err)
	}

	accountRepo := relational.NewAccountRepository(database)
	modelRepo := relational.NewModelRepository(database)
	auditRepo := relational.NewAuditRepository(database)
	first, _, err := accountRepo.UpsertByIdentity(ctx, account.Credential{Provider: account.ProviderBuild, Name: "basic", SourceKey: "basic", EncryptedAccessToken: encrypted, ExpiresAt: time.Now().Add(time.Hour), AuthStatus: account.AuthStatusActive})
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := accountRepo.UpsertByIdentity(ctx, account.Credential{Provider: account.ProviderBuild, Name: "premium", SourceKey: "premium", EncryptedAccessToken: encrypted, ExpiresAt: time.Now().Add(time.Hour), AuthStatus: account.AuthStatusActive})
	if err != nil {
		t.Fatal(err)
	}
	webAccount, _, err := accountRepo.UpsertByIdentity(ctx, account.Credential{Provider: account.ProviderWeb, AuthType: account.AuthTypeSSO, WebTier: account.WebTierSuper, Name: "web-super", SourceKey: "web-super", EncryptedAccessToken: encrypted, ExpiresAt: time.Now().Add(time.Hour), AuthStatus: account.AuthStatusActive})
	if err != nil {
		t.Fatal(err)
	}
	adapter := &modelCapabilityAdapter{models: map[uint64][]string{
		first.ID:  {"grok-basic"},
		second.ID: {"grok-basic", "grok-premium"},
	}}
	webAdapter := &modelCapabilityAdapter{
		provider: account.ProviderWeb,
		catalog:  provider.ModelCatalogStatic,
		models: map[uint64][]string{
			webAccount.ID: {"grok-chat-fast", "grok-chat-auto"},
		},
	}
	registry := provider.NewRegistry(adapter, webAdapter)
	sticky := memory.NewStickyStore()
	accountService := accountapp.NewService(accountRepo, auditRepo, memory.NewDeviceSessionStore(), sticky, registry, cipher, nil)
	service := NewService(modelRepo, accountRepo, accountService, registry)

	count, err := service.Sync(ctx)
	if err != nil || count != 4 {
		t.Fatalf("sync count = %d, err = %v", count, err)
	}
	if attempts := adapter.attemptCount(); attempts != 2 {
		t.Fatalf("attempts = %d", attempts)
	}
	if attempts := webAdapter.attemptCount(); attempts != 1 {
		t.Fatalf("web attempts = %d", attempts)
	}
	candidates, err := accountRepo.ListRoutingCandidates(ctx, account.ProviderBuild, "grok-premium", "")
	if err != nil {
		t.Fatal(err)
	}
	support := make(map[uint64]bool, len(candidates))
	for _, candidate := range candidates {
		if !candidate.ModelCapabilityKnown {
			t.Fatalf("capability unknown for account %d", candidate.Credential.ID)
		}
		support[candidate.Credential.ID] = candidate.SupportsModel
	}
	if support[first.ID] || !support[second.ID] {
		t.Fatalf("support = %#v", support)
	}
	webCandidates, err := accountRepo.ListRoutingCandidates(ctx, account.ProviderWeb, "grok-chat-auto", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(webCandidates) != 1 || !webCandidates[0].ModelCapabilityKnown || !webCandidates[0].SupportsModel {
		t.Fatalf("web candidates = %#v", webCandidates)
	}
}

func TestListPublicModelsFollowsConfiguredListAndAliases(t *testing.T) {
	ctx := context.Background()
	database, err := relational.OpenSQLite(ctx, filepath.Join(t.TempDir(), "public-models.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.InitializeSchema(ctx); err != nil {
		t.Fatal(err)
	}
	modelRepo := relational.NewModelRepository(database)
	// Seed catalog + effort aliases as real model_routes (each has its own id for keys).
	if err := modelRepo.ReplaceProviderRoutes(ctx, account.ProviderConsole, consoleprovider.Routes()); err != nil {
		t.Fatal(err)
	}
	registry := provider.NewRegistry(consoleprovider.NewAdapter(consoleprovider.Config{}, nil, nil))
	service := NewService(modelRepo, nil, nil, registry)
	routes, aliases, err := service.ListPublicModels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) == 0 {
		t.Fatal("expected configured console routes")
	}
	// Effort shortcuts are real rows (public id) and also listed as aliases.
	foundXHighRoute := false
	for _, route := range routes {
		if modeldomain.ExternalPublicID(route.Provider, route.PublicID) == "grok-4.20-multi-agent-xhigh" {
			foundXHighRoute = true
			if route.ID == 0 {
				t.Fatal("effort alias must have a real route id for client-key ACL")
			}
			break
		}
	}
	if !foundXHighRoute {
		t.Fatalf("routes missing multi-agent-xhigh row: count=%d aliases=%#v", len(routes), aliases)
	}
	foundXHighAlias := false
	for _, name := range aliases {
		if name == "grok-4.20-multi-agent-xhigh" {
			foundXHighAlias = true
			break
		}
	}
	if !foundXHighAlias {
		t.Fatalf("aliases missing multi-agent-xhigh: %#v", aliases)
	}
	// Disable the effort-alias route itself → it must leave the public list.
	values, _, err := modelRepo.List(ctx, repository.ModelListQuery{Page: repository.PageQuery{Limit: 200}})
	if err != nil {
		t.Fatal(err)
	}
	var xhighID uint64
	for _, value := range values {
		if modeldomain.ExternalPublicID(value.Provider, value.PublicID) == "grok-4.20-multi-agent-xhigh" {
			xhighID = value.ID
			break
		}
	}
	if xhighID == 0 {
		t.Fatal("multi-agent-xhigh route missing from seeded list")
	}
	enabled := false
	if _, err := service.Update(ctx, xhighID, UpdateInput{Enabled: &enabled}); err != nil {
		t.Fatal(err)
	}
	routes, aliases, err = service.ListPublicModels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range routes {
		if modeldomain.ExternalPublicID(route.Provider, route.PublicID) == "grok-4.20-multi-agent-xhigh" {
			t.Fatal("disabled effort alias must not appear in public routes")
		}
	}
	for _, name := range aliases {
		if name == "grok-4.20-multi-agent-xhigh" {
			t.Fatal("disabled effort alias must not reappear via registry aliases")
		}
	}
}

func TestSyncStaticCatalogUsesBulkWrite(t *testing.T) {
	ctx := context.Background()
	database, err := relational.OpenSQLite(ctx, filepath.Join(t.TempDir(), "model-static-sync.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.InitializeSchema(ctx); err != nil {
		t.Fatal(err)
	}
	cipher, err := security.NewCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := cipher.Encrypt("sso-token")
	if err != nil {
		t.Fatal(err)
	}
	accountRepo := relational.NewAccountRepository(database)
	modelRepo := relational.NewModelRepository(database)
	auditRepo := relational.NewAuditRepository(database)
	const n = 1200
	modelsByID := make(map[uint64][]string, n)
	for index := range n {
		value, _, createErr := accountRepo.UpsertByIdentity(ctx, account.Credential{
			Provider: account.ProviderWeb, AuthType: account.AuthTypeSSO, WebTier: account.WebTierBasic,
			Name: fmt.Sprintf("web-%d", index), SourceKey: fmt.Sprintf("web-%d", index),
			EncryptedAccessToken: encrypted, Enabled: true, AuthStatus: account.AuthStatusActive,
		})
		if createErr != nil {
			t.Fatal(createErr)
		}
		modelsByID[value.ID] = []string{"grok-chat-fast", "grok-imagine-image"}
	}
	webAdapter := &modelCapabilityAdapter{
		provider: account.ProviderWeb,
		catalog:  provider.ModelCatalogStatic,
		models:   modelsByID,
	}
	registry := provider.NewRegistry(webAdapter)
	accountService := accountapp.NewService(accountRepo, auditRepo, memory.NewDeviceSessionStore(), memory.NewStickyStore(), registry, cipher, nil)
	service := NewService(modelRepo, accountRepo, accountService, registry)
	started := time.Now()
	count, err := service.Sync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count < 2 {
		t.Fatalf("synced models = %d", count)
	}
	// 1200 static accounts must finish well under the old multi-minute path.
	if elapsed := time.Since(started); elapsed > 15*time.Second {
		t.Fatalf("static bulk sync too slow: %s", elapsed)
	}
	if attempts := webAdapter.attemptCount(); attempts != n {
		t.Fatalf("list calls = %d want %d", attempts, n)
	}
	ok, err := modelRepo.HasSuccessfulAccountSync(ctx, 1)
	if err != nil || !ok {
		t.Fatalf("first account sync state ok=%v err=%v", ok, err)
	}
}

func TestSyncAccountRunsUpstreamDiscoveryConcurrently(t *testing.T) {
	ctx := context.Background()
	database, err := relational.OpenSQLite(ctx, filepath.Join(t.TempDir(), "model-account-sync.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.InitializeSchema(ctx); err != nil {
		t.Fatal(err)
	}
	cipher, err := security.NewCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := cipher.Encrypt("access-token")
	if err != nil {
		t.Fatal(err)
	}

	const accountCount = 10
	accountRepo := relational.NewAccountRepository(database)
	modelRepo := relational.NewModelRepository(database)
	auditRepo := relational.NewAuditRepository(database)
	adapter := &modelCapabilityAdapter{
		models:  make(map[uint64][]string, accountCount),
		entered: make(chan struct{}, accountCount),
		release: make(chan struct{}),
	}
	accountIDs := make([]uint64, 0, accountCount)
	for index := range accountCount {
		value, _, createErr := accountRepo.UpsertByIdentity(ctx, account.Credential{
			Provider: account.ProviderBuild, Name: fmt.Sprintf("account-%d", index), SourceKey: fmt.Sprintf("account-%d", index),
			EncryptedAccessToken: encrypted, ExpiresAt: time.Now().Add(time.Hour), AuthStatus: account.AuthStatusActive,
		})
		if createErr != nil {
			t.Fatal(createErr)
		}
		accountIDs = append(accountIDs, value.ID)
		adapter.models[value.ID] = []string{"grok-shared"}
	}
	registry := provider.NewRegistry(adapter)
	accountService := accountapp.NewService(accountRepo, auditRepo, memory.NewDeviceSessionStore(), memory.NewStickyStore(), registry, cipher, nil)
	service := NewService(modelRepo, accountRepo, accountService, registry)

	results := make(chan error, accountCount)
	for _, accountID := range accountIDs {
		go func() {
			_, syncErr := service.SyncAccount(ctx, accountID)
			results <- syncErr
		}()
	}
	deadline := time.NewTimer(time.Second)
	for range accountCount {
		select {
		case <-adapter.entered:
		case <-deadline.C:
			close(adapter.release)
			t.Fatalf("upstream discovery peak = %d, want %d", adapter.peak.Load(), accountCount)
		}
	}
	deadline.Stop()
	close(adapter.release)
	for range accountCount {
		if syncErr := <-results; syncErr != nil {
			t.Fatal(syncErr)
		}
	}
	if adapter.peak.Load() != accountCount {
		t.Fatalf("upstream discovery peak = %d, want %d", adapter.peak.Load(), accountCount)
	}
}

type modelCapabilityAdapter struct {
	provider account.Provider
	catalog  provider.ModelCatalogKind
	mu       sync.Mutex
	models   map[uint64][]string
	attempts []uint64
	entered  chan struct{}
	release  chan struct{}
	active   atomic.Int64
	peak     atomic.Int64
}

func (a *modelCapabilityAdapter) Provider() account.Provider {
	if a.provider == "" {
		return account.ProviderBuild
	}
	return a.provider
}
func (a *modelCapabilityAdapter) Definition() provider.Definition {
	catalog := a.catalog
	if catalog == "" {
		catalog = provider.ModelCatalogRemote
	}
	return provider.Definition{Provider: a.Provider(), ModelCatalog: catalog}
}
func (a *modelCapabilityAdapter) ListModels(ctx context.Context, credential account.Credential) ([]string, error) {
	a.mu.Lock()
	a.attempts = append(a.attempts, credential.ID)
	models := append([]string(nil), a.models[credential.ID]...)
	a.mu.Unlock()
	if a.entered == nil {
		return models, nil
	}
	current := a.active.Add(1)
	defer a.active.Add(-1)
	for {
		peak := a.peak.Load()
		if current <= peak || a.peak.CompareAndSwap(peak, current) {
			break
		}
	}
	a.entered <- struct{}{}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-a.release:
		return models, nil
	}
}
func (a *modelCapabilityAdapter) attemptCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.attempts)
}
func (a *modelCapabilityAdapter) ForwardResponse(context.Context, provider.ResponseResourceRequest) (*provider.Response, error) {
	return &provider.Response{StatusCode: http.StatusOK}, nil
}
func (a *modelCapabilityAdapter) GetBilling(context.Context, account.Credential) (account.Billing, error) {
	return account.Billing{}, nil
}
func (a *modelCapabilityAdapter) RefreshCredential(context.Context, account.Credential) (provider.RefreshedCredential, error) {
	return provider.RefreshedCredential{}, nil
}
func (a *modelCapabilityAdapter) StartDeviceAuthorization(context.Context) (provider.DeviceAuthorization, error) {
	return provider.DeviceAuthorization{}, nil
}
func (a *modelCapabilityAdapter) PollDeviceAuthorization(context.Context, string) (provider.CredentialSeed, error) {
	return provider.CredentialSeed{}, nil
}
func (a *modelCapabilityAdapter) ParseImportedCredentials([]byte) ([]provider.CredentialSeed, error) {
	return nil, nil
}
func (a *modelCapabilityAdapter) MarshalCredentials([]provider.CredentialSeed) ([]byte, error) {
	return nil, nil
}
