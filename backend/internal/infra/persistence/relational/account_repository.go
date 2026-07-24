package relational

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/chenyme/grok2api/backend/internal/domain/account"
	"github.com/chenyme/grok2api/backend/internal/repository"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type AccountRepository struct{ db *Database }

func NewAccountRepository(db *Database) *AccountRepository { return &AccountRepository{db: db} }

type quotaBreakdownJSON struct {
	ProductCode  int     `json:"productCode"`
	UsagePercent float64 `json:"usagePercent"`
}

const (
	accountPaidBillingPredicate     = `EXISTS (SELECT 1 FROM account_billing_snapshots billing WHERE billing.account_id = provider_accounts.id AND (billing.monthly_limit > 0 OR billing.on_demand_cap > 0 OR billing.on_demand_used > 0 OR billing.prepaid_balance > 0 OR billing.credit_usage_percent > 0))`
	accountFreeSignalPredicate      = `(LOWER(TRIM(provider_accounts.observed_model)) LIKE '%-build-free' OR EXISTS (SELECT 1 FROM account_billing_snapshots billing WHERE billing.account_id = provider_accounts.id AND (billing.is_unified_billing_user = TRUE OR billing.usage_period_type <> '' OR billing.top_up_method <> '' OR billing.billing_period_start <> '' OR (billing.history_json <> '' AND billing.history_json <> '[]' AND billing.history_json <> 'null'))))`
	accountRecoveryPredicate        = `EXISTS (SELECT 1 FROM account_quota_recovery recovery WHERE recovery.account_id = provider_accounts.id AND recovery.status IN ('exhausted', 'probing'))`
	providerQuotaExhaustedPredicate = `((provider_accounts.provider = 'grok_web' AND ((EXISTS (SELECT 1 FROM account_quota_windows quota WHERE quota.account_id = provider_accounts.id AND quota.mode = 'weekly') AND NOT EXISTS (SELECT 1 FROM account_quota_windows quota WHERE quota.account_id = provider_accounts.id AND quota.mode = 'weekly' AND quota.remaining > 0)) OR (NOT EXISTS (SELECT 1 FROM account_quota_windows quota WHERE quota.account_id = provider_accounts.id AND quota.mode = 'weekly') AND EXISTS (SELECT 1 FROM account_quota_windows quota WHERE quota.account_id = provider_accounts.id) AND NOT EXISTS (SELECT 1 FROM account_quota_windows quota WHERE quota.account_id = provider_accounts.id AND quota.remaining > 0)))) OR (provider_accounts.provider = 'grok_console' AND EXISTS (SELECT 1 FROM account_quota_windows quota WHERE quota.account_id = provider_accounts.id) AND NOT EXISTS (SELECT 1 FROM account_quota_windows quota WHERE quota.account_id = provider_accounts.id AND quota.remaining > 0)))`
	accountTypeSortExpression       = `CASE WHEN provider_accounts.provider = 'grok_web' THEN COALESCE((SELECT profile.tier FROM web_account_profiles profile WHERE profile.account_id = provider_accounts.id), 'auto') WHEN ` + accountPaidBillingPredicate + ` THEN 'paid' WHEN ` + accountFreeSignalPredicate + ` THEN 'free' ELSE 'unknown' END`
	accountStatusSortExpression     = `CASE WHEN provider_accounts.enabled = FALSE THEN 4 WHEN provider_accounts.auth_status = 'reauthRequired' THEN 5 WHEN EXISTS (SELECT 1 FROM account_quota_recovery recovery WHERE recovery.account_id = provider_accounts.id AND recovery.status = 'probing') THEN 3 WHEN EXISTS (SELECT 1 FROM account_quota_recovery recovery WHERE recovery.account_id = provider_accounts.id AND recovery.status = 'exhausted') OR ` + providerQuotaExhaustedPredicate + ` THEN 2 WHEN provider_accounts.cooldown_until > CURRENT_TIMESTAMP THEN 1 ELSE 0 END`
	missingConsoleAccountPredicate  = `NOT EXISTS (SELECT 1 FROM provider_accounts AS console_account WHERE console_account.provider = ? AND console_account.source_key = ('console-' || provider_accounts.source_key))`
)

func (r *AccountRepository) List(ctx context.Context, input repository.AccountListQuery) ([]account.Credential, int64, error) {
	var total int64
	query := r.db.db.WithContext(ctx).Model(&accountModel{})
	if input.Filter.Provider != "" {
		query = query.Where("provider = ?", input.Filter.Provider)
	}
	if search := strings.TrimSpace(input.Page.Search); search != "" {
		pattern := "%" + strings.ToLower(search) + "%"
		query = query.Where("LOWER(name) LIKE ? OR LOWER(email) LIKE ? OR LOWER(user_id) LIKE ? OR LOWER(team_id) LIKE ?", pattern, pattern, pattern, pattern)
	}
	switch input.Filter.QuotaType {
	case "free":
		query = query.Where("EXISTS (SELECT 1 FROM account_quota_recovery recovery WHERE recovery.account_id = provider_accounts.id AND recovery.kind = 'free') OR (NOT " + accountPaidBillingPredicate + " AND " + accountFreeSignalPredicate + ")")
	case "paid":
		query = query.Where(accountPaidBillingPredicate)
	case "unknown":
		query = query.Where("NOT " + accountRecoveryPredicate + " AND NOT " + accountPaidBillingPredicate + " AND NOT " + accountFreeSignalPredicate)
	case "auto", "basic", "super", "heavy":
		query = query.Where("EXISTS (SELECT 1 FROM web_account_profiles profile WHERE profile.account_id = provider_accounts.id AND profile.tier = ?)", input.Filter.QuotaType)
	}
	switch input.Filter.Status {
	case "active":
		query = query.Where("enabled = ? AND auth_status = ? AND NOT "+accountRecoveryPredicate+" AND NOT "+providerQuotaExhaustedPredicate+" AND (cooldown_until IS NULL OR cooldown_until <= ?)", true, account.AuthStatusActive, input.Filter.Now)
	case "disabled":
		query = query.Where("enabled = ?", false)
	case "reauthRequired":
		query = query.Where("enabled = ? AND auth_status = ?", true, account.AuthStatusReauthRequired)
	case "cooldown":
		query = query.Where("enabled = ? AND auth_status = ? AND NOT "+accountRecoveryPredicate+" AND cooldown_until > ?", true, account.AuthStatusActive, input.Filter.Now)
	case "waitingReset":
		query = query.Where("enabled = ? AND auth_status = ? AND (EXISTS (SELECT 1 FROM account_quota_recovery recovery WHERE recovery.account_id = provider_accounts.id AND recovery.status = 'exhausted') OR "+providerQuotaExhaustedPredicate+")", true, account.AuthStatusActive)
	case "probing":
		query = query.Where("enabled = ? AND auth_status = ? AND EXISTS (SELECT 1 FROM account_quota_recovery recovery WHERE recovery.account_id = provider_accounts.id AND recovery.status = 'probing')", true, account.AuthStatusActive)
	}
	if input.Filter.Refreshable != nil {
		if *input.Filter.Refreshable {
			query = query.Where("EXISTS (SELECT 1 FROM account_credentials credential WHERE credential.account_id = provider_accounts.id AND credential.encrypted_refresh <> '')")
		} else {
			query = query.Where("NOT EXISTS (SELECT 1 FROM account_credentials credential WHERE credential.account_id = provider_accounts.id AND credential.encrypted_refresh <> '')")
		}
	}
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []accountModel
	query = applyStableSort(query, input.Page.Sort, map[string]sortSpec{
		"name":      {expression: "LOWER(provider_accounts.name)"},
		"type":      {expression: accountTypeSortExpression},
		"status":    {expression: accountStatusSortExpression},
		"createdAt": {expression: "provider_accounts.created_at", defaultDirection: repository.SortDescending},
	}, sortSpec{expression: "provider_accounts.created_at", defaultDirection: repository.SortDescending}, "provider_accounts.id")
	if err := query.Preload("Credential").Preload("WebProfile").Offset(input.Page.Offset).Limit(input.Page.Limit).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	out := make([]account.Credential, 0, len(rows))
	for _, row := range rows {
		out = append(out, toAccountDomain(row))
	}
	if err := r.attachAccountLinks(ctx, out); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

func (r *AccountRepository) ListProviderAccountBatch(ctx context.Context, providerValue account.Provider, afterID uint64, limit int) ([]account.Credential, int64, error) {
	if limit < 1 {
		return []account.Credential{}, 0, nil
	}
	var total int64
	if afterID == 0 {
		if err := r.db.db.WithContext(ctx).Model(&accountModel{}).Where("provider = ?", providerValue).Count(&total).Error; err != nil {
			return nil, 0, err
		}
	}
	var rows []accountModel
	if err := r.db.db.WithContext(ctx).
		Preload("Credential").Preload("WebProfile").
		Where("provider = ? AND id > ?", providerValue, afterID).
		Order("id ASC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	out := make([]account.Credential, 0, len(rows))
	for _, row := range rows {
		out = append(out, toAccountDomain(row))
	}
	if err := r.attachAccountLinks(ctx, out); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

func (r *AccountRepository) Summarize(ctx context.Context, now time.Time) ([]repository.AccountSummary, error) {
	var rows []repository.AccountSummary
	selectFields := `
		provider,
		COUNT(*) AS total,
		SUM(CASE WHEN enabled = ? AND auth_status = ? AND NOT ` + accountRecoveryPredicate + ` AND NOT ` + providerQuotaExhaustedPredicate + ` AND (cooldown_until IS NULL OR cooldown_until <= ?) THEN 1 ELSE 0 END) AS available,
		SUM(CASE WHEN enabled = ? AND auth_status = ? AND NOT ` + accountRecoveryPredicate + ` AND NOT ` + providerQuotaExhaustedPredicate + ` AND cooldown_until > ? THEN 1 ELSE 0 END) AS cooldown,
		SUM(CASE WHEN enabled = ? AND auth_status = ? AND (EXISTS (SELECT 1 FROM account_quota_recovery recovery WHERE recovery.account_id = provider_accounts.id AND recovery.status = 'exhausted') OR ` + providerQuotaExhaustedPredicate + `) THEN 1 ELSE 0 END) AS waiting_reset,
		SUM(CASE WHEN enabled = ? AND auth_status = ? AND EXISTS (SELECT 1 FROM account_quota_recovery recovery WHERE recovery.account_id = provider_accounts.id AND recovery.status = 'probing') THEN 1 ELSE 0 END) AS probing,
		SUM(CASE WHEN enabled = ? THEN 1 ELSE 0 END) AS disabled,
		SUM(CASE WHEN enabled = ? AND auth_status = ? THEN 1 ELSE 0 END) AS reauth_required`
	err := r.db.db.WithContext(ctx).Model(&accountModel{}).Select(
		selectFields,
		true, account.AuthStatusActive, now,
		true, account.AuthStatusActive, now,
		true, account.AuthStatusActive,
		true, account.AuthStatusActive,
		false,
		true, account.AuthStatusReauthRequired,
	).Group("provider").Scan(&rows).Error
	return rows, err
}


func (r *AccountRepository) SummarizeWebPools(ctx context.Context, now time.Time) (repository.WebPoolSummary, error) {
	type row struct {
		Tier      string
		Total     int64
		Available int64
	}
	var rows []row
	// Coalesce missing profiles to "auto" so unsynced Web accounts still appear in the pool view.
	err := r.db.db.WithContext(ctx).
		Table("provider_accounts AS account").
		Select(`
COALESCE(NULLIF(TRIM(profile.tier), ''), 'auto') AS tier,
COUNT(*) AS total,
SUM(CASE WHEN account.enabled = ? AND account.auth_status = ? AND NOT ` + accountRecoveryPredicateForAlias("account") + ` AND NOT ` + providerQuotaExhaustedPredicateForAlias("account") + ` AND (account.cooldown_until IS NULL OR account.cooldown_until <= ?) THEN 1 ELSE 0 END) AS available
`, true, account.AuthStatusActive, now).
		Joins("LEFT JOIN web_account_profiles AS profile ON profile.account_id = account.id").
		Where("account.provider = ?", account.ProviderWeb).
		Group("COALESCE(NULLIF(TRIM(profile.tier), ''), 'auto')").
		Scan(&rows).Error
	if err != nil {
		return repository.WebPoolSummary{}, err
	}
	var result repository.WebPoolSummary
	for _, value := range rows {
		bucket := repository.WebPoolBucket{Total: value.Total, Available: value.Available}
		switch account.WebTier(value.Tier) {
		case account.WebTierBasic:
			result.Basic = bucket
		case account.WebTierSuper:
			result.Super = bucket
		case account.WebTierHeavy:
			result.Heavy = bucket
		default:
			result.Auto = bucket
		}
	}
	return result, nil
}

func (r *AccountRepository) SummarizeConsoleQuota(ctx context.Context, now time.Time) (repository.ConsoleQuotaSummary, error) {
	type row struct {
		Total     int64
		Available int64
		Healthy   int64
		Rotating  int64
		Exhausted int64
		Remaining int64
		Capacity  int64
	}
	var value row
	// Console free-model rotation: healthy = remaining > threshold without timer;
	// rotating = timer started while still usable; exhausted = remaining depleted.
	err := r.db.db.WithContext(ctx).
		Table("provider_accounts AS account").
		Select(`
COUNT(*) AS total,
SUM(CASE WHEN account.enabled = ? AND account.auth_status = ? AND NOT ` + accountRecoveryPredicateForAlias("account") + ` AND NOT ` + providerQuotaExhaustedPredicateForAlias("account") + ` AND (account.cooldown_until IS NULL OR account.cooldown_until <= ?) THEN 1 ELSE 0 END) AS available,
SUM(CASE WHEN account.enabled = ? AND account.auth_status = ? AND COALESCE(quota.remaining, 0) > 0 AND quota.reset_at IS NULL THEN 1 ELSE 0 END) AS healthy,
SUM(CASE WHEN account.enabled = ? AND account.auth_status = ? AND COALESCE(quota.remaining, 0) > 0 AND quota.reset_at IS NOT NULL AND quota.reset_at > ? THEN 1 ELSE 0 END) AS rotating,
SUM(CASE WHEN account.enabled = ? AND account.auth_status = ? AND (quota.remaining IS NULL OR quota.remaining <= 0) THEN 1 ELSE 0 END) AS exhausted,
SUM(CASE WHEN account.enabled = ? AND account.auth_status = ? THEN COALESCE(quota.remaining, 0) ELSE 0 END) AS remaining,
SUM(CASE WHEN account.enabled = ? AND account.auth_status = ? THEN COALESCE(quota.total, 0) ELSE 0 END) AS capacity
`,
			true, account.AuthStatusActive, now,
			true, account.AuthStatusActive,
			true, account.AuthStatusActive, now,
			true, account.AuthStatusActive,
			true, account.AuthStatusActive,
			true, account.AuthStatusActive,
		).
		Joins("LEFT JOIN account_quota_windows AS quota ON quota.account_id = account.id AND quota.mode = ?", "console").
		Where("account.provider = ?", account.ProviderConsole).
		Scan(&value).Error
	if err != nil {
		return repository.ConsoleQuotaSummary{}, err
	}
	return repository.ConsoleQuotaSummary{
		Total: value.Total, Available: value.Available, Healthy: value.Healthy,
		Rotating: value.Rotating, Exhausted: value.Exhausted, Remaining: value.Remaining, Capacity: value.Capacity,
	}, nil
}

// accountRecoveryPredicateForAlias rewrites the recovery EXISTS predicate for a table alias.
func accountRecoveryPredicateForAlias(alias string) string {
	return `EXISTS (SELECT 1 FROM account_quota_recovery recovery WHERE recovery.account_id = ` + alias + `.id AND recovery.status IN ('exhausted', 'probing'))`
}

// providerQuotaExhaustedPredicateForAlias rewrites the provider quota gate for a table alias.
func providerQuotaExhaustedPredicateForAlias(alias string) string {
	return `((` + alias + `.provider = 'grok_web' AND ((EXISTS (SELECT 1 FROM account_quota_windows quota WHERE quota.account_id = ` + alias + `.id AND quota.mode = 'weekly') AND NOT EXISTS (SELECT 1 FROM account_quota_windows quota WHERE quota.account_id = ` + alias + `.id AND quota.mode = 'weekly' AND quota.remaining > 0)) OR (NOT EXISTS (SELECT 1 FROM account_quota_windows quota WHERE quota.account_id = ` + alias + `.id AND quota.mode = 'weekly') AND EXISTS (SELECT 1 FROM account_quota_windows quota WHERE quota.account_id = ` + alias + `.id) AND NOT EXISTS (SELECT 1 FROM account_quota_windows quota WHERE quota.account_id = ` + alias + `.id AND quota.remaining > 0)))) OR (` + alias + `.provider = 'grok_console' AND EXISTS (SELECT 1 FROM account_quota_windows quota WHERE quota.account_id = ` + alias + `.id) AND NOT EXISTS (SELECT 1 FROM account_quota_windows quota WHERE quota.account_id = ` + alias + `.id AND quota.remaining > 0)))`
}

// ListRoutingCandidates 批量加载账号、额度、恢复状态和目标模型能力，避免推理热路径按账号逐条查询。
func (r *AccountRepository) ListRoutingCandidates(ctx context.Context, provider account.Provider, upstreamModel, quotaMode string) ([]account.RoutingCandidate, error) {
	values, err := r.ListEnabled(ctx, provider)
	if err != nil {
		return nil, err
	}
	bound := make(map[uint64]bool)
	if strings.TrimSpace(upstreamModel) != "" {
		var boundIDs []uint64
		if err := r.db.db.WithContext(ctx).
			Table("model_route_accounts AS binding").
			Select("binding.account_id").
			Joins("JOIN model_routes AS route ON route.id = binding.model_route_id").
			Where("route.provider = ? AND route.upstream_model = ?", provider, upstreamModel).
			Scan(&boundIDs).Error; err != nil {
			return nil, err
		}
		if len(boundIDs) > 0 {
			for _, id := range boundIDs {
				bound[id] = true
			}
			filtered := values[:0]
			for _, value := range values {
				if bound[value.ID] {
					filtered = append(filtered, value)
				}
			}
			values = filtered
		}
	}
	ids := make([]uint64, 0, len(values))
	for _, value := range values {
		ids = append(ids, value.ID)
	}
	billings, err := r.GetBillings(ctx, ids)
	if err != nil {
		return nil, err
	}
	recoveries, err := r.GetQuotaRecoveries(ctx, ids)
	if err != nil {
		return nil, err
	}
	quotaWindows := make(map[uint64]account.QuotaWindow, len(ids))
	if len(ids) > 0 && (provider == account.ProviderWeb || quotaMode != "") {
		var rows []quotaWindowModel
		modes := make([]string, 0, 2)
		if provider == account.ProviderWeb {
			modes = append(modes, "weekly")
		}
		if quotaMode != "" {
			modes = append(modes, quotaMode)
		}
		if err := r.db.db.WithContext(ctx).Where("account_id IN ? AND mode IN ?", ids, modes).Order("CASE WHEN mode = 'weekly' THEN 0 ELSE 1 END").Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			if _, exists := quotaWindows[row.AccountID]; !exists {
				quotaWindows[row.AccountID] = toQuotaWindowDomain(row)
			}
		}
	}
	known := make(map[uint64]bool, len(ids))
	supported := make(map[uint64]bool, len(ids))
	modelQuotaBlocks := make(map[uint64]account.ModelQuotaBlock, len(ids))
	if strings.TrimSpace(upstreamModel) != "" && len(ids) > 0 {
		var states []accountModelSyncStateModel
		if err := r.db.db.WithContext(ctx).Where("account_id IN ? AND last_success_at IS NOT NULL", ids).Find(&states).Error; err != nil {
			return nil, err
		}
		for _, state := range states {
			known[state.AccountID] = true
		}
		var capabilities []accountModelCapabilityModel
		if err := r.db.db.WithContext(ctx).Where("account_id IN ? AND upstream_model = ?", ids, upstreamModel).Find(&capabilities).Error; err != nil {
			return nil, err
		}
		for _, capability := range capabilities {
			supported[capability.AccountID] = true
		}
		var blockRows []accountModelQuotaBlockModel
		if err := r.db.db.WithContext(ctx).Where("account_id IN ? AND upstream_model = ? AND cooldown_until > ?", ids, upstreamModel, time.Now().UTC()).Find(&blockRows).Error; err != nil {
			return nil, err
		}
		for _, row := range blockRows {
			modelQuotaBlocks[row.AccountID] = account.ModelQuotaBlock{AccountID: row.AccountID, UpstreamModel: row.UpstreamModel, Reason: row.Reason, CooldownUntil: row.CooldownUntil.UTC(), UpdatedAt: row.UpdatedAt.UTC()}
		}
	}
	result := make([]account.RoutingCandidate, 0, len(values))
	for _, value := range values {
		capabilityKnown, supportsModel := known[value.ID], supported[value.ID]
		if len(bound) > 0 {
			capabilityKnown, supportsModel = true, true
		}
		candidate := account.RoutingCandidate{Credential: value, ModelCapabilityKnown: capabilityKnown, SupportsModel: supportsModel}
		if billing, ok := billings[value.ID]; ok {
			candidate.Billing = &billing
		}
		if recovery, ok := recoveries[value.ID]; ok {
			candidate.QuotaRecovery = &recovery
		}
		if window, ok := quotaWindows[value.ID]; ok {
			candidate.QuotaWindow = &window
		}
		if block, ok := modelQuotaBlocks[value.ID]; ok {
			candidate.ModelQuotaBlock = &block
		}
		result = append(result, candidate)
	}
	return result, nil
}

func (r *AccountRepository) ListRoutingAccountBases(ctx context.Context, provider account.Provider, quotaMode string) ([]account.RoutingAccountBase, error) {
	values, err := r.ListEnabled(ctx, provider)
	if err != nil {
		return nil, err
	}
	ids := make([]uint64, 0, len(values))
	for _, value := range values {
		ids = append(ids, value.ID)
	}
	billings, err := r.GetBillings(ctx, ids)
	if err != nil {
		return nil, err
	}
	recoveries, err := r.GetQuotaRecoveries(ctx, ids)
	if err != nil {
		return nil, err
	}
	quotaWindows := make(map[uint64]account.QuotaWindow, len(ids))
	if len(ids) > 0 && (provider == account.ProviderWeb || quotaMode != "") {
		modes := make([]string, 0, 2)
		if provider == account.ProviderWeb {
			modes = append(modes, "weekly")
		}
		if quotaMode != "" {
			modes = append(modes, quotaMode)
		}
		var rows []quotaWindowModel
		if err := r.db.db.WithContext(ctx).Where("account_id IN ? AND mode IN ?", ids, modes).Order("CASE WHEN mode = 'weekly' THEN 0 ELSE 1 END").Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			if _, exists := quotaWindows[row.AccountID]; !exists {
				quotaWindows[row.AccountID] = toQuotaWindowDomain(row)
			}
		}
	}
	result := make([]account.RoutingAccountBase, 0, len(values))
	for _, value := range values {
		base := account.RoutingAccountBase{Credential: value}
		if billing, ok := billings[value.ID]; ok {
			base.Billing = &billing
		}
		if recovery, ok := recoveries[value.ID]; ok {
			base.QuotaRecovery = &recovery
		}
		if window, ok := quotaWindows[value.ID]; ok {
			base.QuotaWindow = &window
		}
		result = append(result, base)
	}
	return result, nil
}

func (r *AccountRepository) ListRoutingAccountOverlays(ctx context.Context, provider account.Provider, upstreamModel string) (account.RoutingOverlaySnapshot, error) {
	upstreamModel = strings.TrimSpace(upstreamModel)
	if upstreamModel == "" {
		return account.RoutingOverlaySnapshot{}, nil
	}
	var boundIDs []uint64
	if err := r.db.db.WithContext(ctx).
		Table("model_route_accounts AS binding").
		Select("binding.account_id").
		Joins("JOIN model_routes AS route ON route.id = binding.model_route_id").
		Where("route.provider = ? AND route.upstream_model = ?", provider, upstreamModel).
		Scan(&boundIDs).Error; err != nil {
		return account.RoutingOverlaySnapshot{}, err
	}
	values := make(map[uint64]account.RoutingAccountOverlay)
	for _, id := range boundIDs {
		values[id] = account.RoutingAccountOverlay{AccountID: id, Bound: true, ModelCapabilityKnown: true, SupportsModel: true}
	}
	var states []accountModelSyncStateModel
	if err := r.db.db.WithContext(ctx).
		Table("account_model_sync_states AS state").
		Select("state.*").
		Joins("JOIN provider_accounts AS account ON account.id = state.account_id").
		Where("account.provider = ? AND account.enabled = TRUE AND state.last_success_at IS NOT NULL", provider).
		Find(&states).Error; err != nil {
		return account.RoutingOverlaySnapshot{}, err
	}
	for _, state := range states {
		overlay := values[state.AccountID]
		overlay.AccountID = state.AccountID
		overlay.ModelCapabilityKnown = true
		values[state.AccountID] = overlay
	}
	var capabilities []accountModelCapabilityModel
	if err := r.db.db.WithContext(ctx).
		Table("account_model_capabilities AS capability").
		Select("capability.*").
		Joins("JOIN provider_accounts AS account ON account.id = capability.account_id").
		Where("account.provider = ? AND account.enabled = TRUE AND capability.upstream_model = ?", provider, upstreamModel).
		Find(&capabilities).Error; err != nil {
		return account.RoutingOverlaySnapshot{}, err
	}
	for _, capability := range capabilities {
		overlay := values[capability.AccountID]
		overlay.AccountID = capability.AccountID
		overlay.SupportsModel = true
		values[capability.AccountID] = overlay
	}
	var blockRows []accountModelQuotaBlockModel
	if err := r.db.db.WithContext(ctx).
		Table("account_model_quota_blocks AS block").
		Select("block.*").
		Joins("JOIN provider_accounts AS account ON account.id = block.account_id").
		Where("account.provider = ? AND account.enabled = TRUE AND block.upstream_model = ? AND block.cooldown_until > ?", provider, upstreamModel, time.Now().UTC()).
		Find(&blockRows).Error; err != nil {
		return account.RoutingOverlaySnapshot{}, err
	}
	for _, row := range blockRows {
		overlay := values[row.AccountID]
		overlay.AccountID = row.AccountID
		overlay.ModelQuotaBlock = &account.ModelQuotaBlock{AccountID: row.AccountID, UpstreamModel: row.UpstreamModel, Reason: row.Reason, CooldownUntil: row.CooldownUntil.UTC(), UpdatedAt: row.UpdatedAt.UTC()}
		values[row.AccountID] = overlay
	}
	result := account.RoutingOverlaySnapshot{HasBindings: len(boundIDs) > 0, Values: make([]account.RoutingAccountOverlay, 0, len(values))}
	for _, value := range values {
		result.Values = append(result.Values, value)
	}
	return result, nil
}

func (r *AccountRepository) ListEnabled(ctx context.Context, provider account.Provider) ([]account.Credential, error) {
	var rows []accountModel
	// Prefer healthy accounts first so bulk sync / routing snapshots hit good tokens before
	// recently failed ones (failure_count ASC). Priority still wins within the same health band.
	err := r.db.db.WithContext(ctx).Preload("Credential").Preload("WebProfile").Where("provider = ? AND enabled = ? AND auth_status = ?", provider, true, account.AuthStatusActive).Order("failure_count ASC, priority DESC, id ASC").Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]account.Credential, 0, len(rows))
	for _, row := range rows {
		out = append(out, toAccountDomain(row))
	}
	return out, nil
}

func (r *AccountRepository) ListEnabledAccountIDs(ctx context.Context, provider account.Provider, refreshableOnly bool) ([]uint64, error) {
	query := r.db.db.WithContext(ctx).
		Table("provider_accounts AS account").
		Select("account.id").
		Where("account.provider = ? AND account.enabled = ? AND account.auth_status = ?", provider, true, account.AuthStatusActive)
	if refreshableOnly {
		query = query.
			Joins("JOIN account_credentials AS credential ON credential.account_id = account.id").
			Where("credential.encrypted_refresh <> ''")
	}
	var ids []uint64
	// Slow auto-sync and bulk quota jobs process healthy accounts first; failed ones last.
	err := query.Order("account.failure_count ASC, account.priority DESC, account.id ASC").Scan(&ids).Error
	return ids, err
}

func (r *AccountRepository) ListSSOAccountsForDedup(ctx context.Context, provider account.Provider) ([]account.Credential, error) {
	var rows []accountModel
	err := r.db.db.WithContext(ctx).
		Preload("Credential").
		Preload("WebProfile").
		Joins("JOIN account_credentials AS credential ON credential.account_id = provider_accounts.id").
		Where("provider_accounts.provider = ? AND credential.auth_type = ?", provider, account.AuthTypeSSO).
		Order("provider_accounts.id ASC").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]account.Credential, 0, len(rows))
	for _, row := range rows {
		out = append(out, toAccountDomain(row))
	}
	return out, nil
}

func (r *AccountRepository) ListFailedAccountIDs(ctx context.Context, provider account.Provider, includeDisabled bool, limit int) ([]uint64, error) {
	if limit < 1 {
		return []uint64{}, nil
	}
	// Use the model table directly (no alias) so Postgres/SQLite both scan IDs reliably.
	query := r.db.db.WithContext(ctx).Model(&accountModel{}).Select("id").Where("provider = ?", string(provider))
	if includeDisabled {
		query = query.Where("auth_status = ? OR enabled = ?", string(account.AuthStatusReauthRequired), false)
	} else {
		query = query.Where("auth_status = ?", string(account.AuthStatusReauthRequired))
	}
	var ids []uint64
	err := query.Order("id ASC").Limit(limit).Scan(&ids).Error
	if ids == nil {
		ids = []uint64{}
	}
	return ids, err
}

func (r *AccountRepository) ListProviderAccountIDs(ctx context.Context, provider account.Provider, limit int) ([]uint64, error) {
	if limit < 1 {
		return []uint64{}, nil
	}
	var ids []uint64
	err := r.db.db.WithContext(ctx).Model(&accountModel{}).Select("id").Where("provider = ?", string(provider)).Order("id ASC").Limit(limit).Scan(&ids).Error
	if ids == nil {
		ids = []uint64{}
	}
	return ids, err
}

func (r *AccountRepository) FilterMissingBuildConversionIDs(ctx context.Context, ids []uint64) ([]uint64, error) {
	if len(ids) == 0 {
		return []uint64{}, nil
	}
	var linkedIDs []uint64
	if err := r.db.db.WithContext(ctx).Model(&accountProviderLinkModel{}).
		Where("web_account_id IN ?", ids).Pluck("web_account_id", &linkedIDs).Error; err != nil {
		return nil, err
	}
	linked := make(map[uint64]struct{}, len(linkedIDs))
	for _, id := range linkedIDs {
		linked[id] = struct{}{}
	}
	values := make([]uint64, 0, len(ids)-len(linked))
	for _, id := range ids {
		if _, exists := linked[id]; !exists {
			values = append(values, id)
		}
	}
	return values, nil
}

func (r *AccountRepository) ListUnlinkedWebAccountIDs(ctx context.Context, afterID uint64, limit int) ([]uint64, int64, error) {
	if limit < 1 {
		return []uint64{}, 0, nil
	}
	query := func() *gorm.DB {
		return r.db.db.WithContext(ctx).
			Table("provider_accounts AS account").
			Joins("LEFT JOIN account_provider_links AS link ON link.web_account_id = account.id").
			Where("account.provider = ? AND link.web_account_id IS NULL", account.ProviderWeb)
	}
	var total int64
	if afterID == 0 {
		if err := query().Count(&total).Error; err != nil {
			return nil, 0, err
		}
	}
	var ids []uint64
	err := query().
		Select("account.id").
		Where("account.id > ?", afterID).
		Order("account.id ASC").
		Limit(limit).
		Scan(&ids).Error
	return ids, total, err
}

func (r *AccountRepository) ListMissingConsoleSyncAccounts(ctx context.Context, ids []uint64) ([]account.Credential, error) {
	if len(ids) == 0 {
		return []account.Credential{}, nil
	}
	var existing int64
	if err := r.db.db.WithContext(ctx).Model(&accountModel{}).
		Where("id IN ? AND provider = ?", ids, account.ProviderWeb).Count(&existing).Error; err != nil {
		return nil, err
	}
	if existing != int64(len(ids)) {
		return nil, repository.ErrNotFound
	}
	var rows []accountModel
	if err := r.db.db.WithContext(ctx).
		Preload("Credential").Preload("WebProfile").
		Where("id IN ? AND provider = ?", ids, account.ProviderWeb).
		Where(missingConsoleAccountPredicate, account.ProviderConsole).
		Order("id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	values := make([]account.Credential, 0, len(rows))
	for _, row := range rows {
		values = append(values, toAccountDomain(row))
	}
	return values, nil
}

func (r *AccountRepository) ListMissingConsoleSyncBatch(ctx context.Context, afterID uint64, limit int) ([]account.Credential, int64, int64, error) {
	if limit < 1 {
		return []account.Credential{}, 0, 0, nil
	}
	query := func() *gorm.DB {
		return r.db.db.WithContext(ctx).Model(&accountModel{}).
			Where("provider = ?", account.ProviderWeb).
			Where(missingConsoleAccountPredicate, account.ProviderConsole)
	}
	var total, skipped int64
	if afterID == 0 {
		if err := query().Count(&total).Error; err != nil {
			return nil, 0, 0, err
		}
		var all int64
		if err := r.db.db.WithContext(ctx).Model(&accountModel{}).Where("provider = ?", account.ProviderWeb).Count(&all).Error; err != nil {
			return nil, 0, 0, err
		}
		skipped = max(0, all-total)
	}
	var rows []accountModel
	if err := query().Preload("Credential").Preload("WebProfile").
		Where("id > ?", afterID).Order("id ASC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, 0, 0, err
	}
	values := make([]account.Credential, 0, len(rows))
	for _, row := range rows {
		values = append(values, toAccountDomain(row))
	}
	return values, total, skipped, nil
}

func (r *AccountRepository) HasActive(ctx context.Context, provider account.Provider) (bool, error) {
	var row struct{ ID uint64 }
	err := r.db.db.WithContext(ctx).Model(&accountModel{}).Select("id").Where("provider = ? AND enabled = ? AND auth_status = ?", provider, true, account.AuthStatusActive).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	return row.ID > 0, err
}

func (r *AccountRepository) Get(ctx context.Context, id uint64) (account.Credential, error) {
	var row accountModel
	if err := r.db.db.WithContext(ctx).Preload("Credential").Preload("WebProfile").First(&row, id).Error; err != nil {
		return account.Credential{}, mapError(err)
	}
	value := toAccountDomain(row)
	values := []account.Credential{value}
	if err := r.attachAccountLinks(ctx, values); err != nil {
		return account.Credential{}, err
	}
	return values[0], nil
}

func (r *AccountRepository) GetMany(ctx context.Context, ids []uint64) ([]account.Credential, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var rows []accountModel
	if err := r.db.db.WithContext(ctx).Preload("Credential").Preload("WebProfile").Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, mapError(err)
	}
	out := make([]account.Credential, 0, len(rows))
	for _, row := range rows {
		out = append(out, toAccountDomain(row))
	}
	if err := r.attachAccountLinks(ctx, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *AccountRepository) LinkWebToBuild(ctx context.Context, webAccountID, buildAccountID uint64) error {
	if webAccountID == 0 || buildAccountID == 0 || webAccountID == buildAccountID {
		return repository.ErrConflict
	}
	err := r.db.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var webAccount, buildAccount accountModel
		if err := tx.Select("id", "provider").First(&webAccount, webAccountID).Error; err != nil {
			return err
		}
		if err := tx.Select("id", "provider").First(&buildAccount, buildAccountID).Error; err != nil {
			return err
		}
		if webAccount.Provider != string(account.ProviderWeb) || buildAccount.Provider != string(account.ProviderBuild) {
			return repository.ErrConflict
		}
		var existing accountProviderLinkModel
		err := tx.Where("web_account_id = ? OR build_account_id = ?", webAccountID, buildAccountID).First(&existing).Error
		if err == nil {
			if existing.WebAccountID == webAccountID && existing.BuildAccountID == buildAccountID {
				return nil
			}
			return repository.ErrConflict
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		return tx.Create(&accountProviderLinkModel{WebAccountID: webAccountID, BuildAccountID: buildAccountID, CreatedAt: time.Now().UTC()}).Error
	})
	return mapError(err)
}

func (r *AccountRepository) attachAccountLinks(ctx context.Context, values []account.Credential) error {
	if len(values) == 0 {
		return nil
	}
	ids := make([]uint64, 0, len(values))
	positions := make(map[uint64]int, len(values))
	for index := range values {
		ids = append(ids, values[index].ID)
		positions[values[index].ID] = index
	}
	var rows []struct {
		WebAccountID   uint64
		BuildAccountID uint64
		WebName        string
		BuildName      string
	}
	err := r.db.db.WithContext(ctx).Table("account_provider_links AS link").
		Select("link.web_account_id, link.build_account_id, web.name AS web_name, build.name AS build_name").
		Joins("JOIN provider_accounts AS web ON web.id = link.web_account_id").
		Joins("JOIN provider_accounts AS build ON build.id = link.build_account_id").
		Where("link.web_account_id IN ? OR link.build_account_id IN ?", ids, ids).
		Scan(&rows).Error
	if err != nil {
		return err
	}
	for _, row := range rows {
		if index, ok := positions[row.WebAccountID]; ok {
			values[index].LinkedAccountID = row.BuildAccountID
			values[index].LinkedAccountName = row.BuildName
			values[index].LinkedProvider = account.ProviderBuild
		}
		if index, ok := positions[row.BuildAccountID]; ok {
			values[index].LinkedAccountID = row.WebAccountID
			values[index].LinkedAccountName = row.WebName
			values[index].LinkedProvider = account.ProviderWeb
		}
	}
	return nil
}

func (r *AccountRepository) UpsertByIdentity(ctx context.Context, value account.Credential) (account.Credential, bool, error) {
	var result repository.AccountUpsertResult
	err := r.db.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		result, err = upsertAccountByIdentity(tx, value)
		return err
	})
	if err != nil {
		return account.Credential{}, false, mapError(err)
	}
	stored, err := r.Get(ctx, result.ID)
	return stored, result.Created, err
}

func (r *AccountRepository) UpsertManyByIdentity(ctx context.Context, values []account.Credential) ([]repository.AccountUpsertResult, error) {
	if len(values) == 0 {
		return []repository.AccountUpsertResult{}, nil
	}
	results := make([]repository.AccountUpsertResult, len(values))
	err := r.db.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		identityKeys := make([]string, 0, len(values))
		for _, value := range values {
			identityKeys = append(identityKeys, fromAccountDomain(value).IdentityKey)
		}
		var existingRows []accountModel
		if err := tx.Where("identity_key IN ?", identityKeys).Find(&existingRows).Error; err != nil {
			return err
		}
		existingByIdentity := make(map[string]accountModel, len(values))
		for _, row := range existingRows {
			existingByIdentity[row.IdentityKey] = row
		}
		for index, value := range values {
			identityKey := fromAccountDomain(value).IdentityKey
			existing, found := existingByIdentity[identityKey]
			var current *accountModel
			if found {
				current = &existing
			}
			result, stored, err := upsertKnownAccountByIdentity(tx, value, current)
			if err != nil {
				return err
			}
			results[index] = result
			existingByIdentity[stored.IdentityKey] = stored
		}
		return nil
	})
	if err != nil {
		return nil, mapError(err)
	}
	return results, nil
}

func upsertAccountByIdentity(tx *gorm.DB, value account.Credential) (repository.AccountUpsertResult, error) {
	row := fromAccountDomain(value)
	var existing accountModel
	err := tx.Where("identity_key = ?", row.IdentityKey).First(&existing).Error
	if err == nil {
		result, _, err := upsertKnownAccountByIdentity(tx, value, &existing)
		return result, err
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return repository.AccountUpsertResult{}, err
	}
	result, _, err := upsertKnownAccountByIdentity(tx, value, nil)
	return result, err
}

func upsertKnownAccountByIdentity(tx *gorm.DB, value account.Credential, existing *accountModel) (repository.AccountUpsertResult, accountModel, error) {
	row := fromAccountDomain(value)
	if existing != nil {
		if value.EncryptedCloudflareCookie == "" {
			var storedCredential accountCredentialModel
			if err := tx.Where("account_id = ?", existing.ID).First(&storedCredential).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return repository.AccountUpsertResult{}, accountModel{}, err
			}
			value.EncryptedCloudflareCookie = storedCredential.EncryptedCloudflareCookie
		}
		row.ID = existing.ID
		row.CreatedAt = existing.CreatedAt
		row.Enabled = existing.Enabled
		row.Priority = existing.Priority
		row.MaxConcurrent = existing.MaxConcurrent
		row.MinimumRemaining = existing.MinimumRemaining
		row.FailureCount = existing.FailureCount
		row.CooldownUntil = existing.CooldownUntil
		row.LastError = existing.LastError
		row.LastUsedAt = existing.LastUsedAt
		row.ObservedModel = existing.ObservedModel
		row.ObservedModelAt = existing.ObservedModelAt
		// 回退标记不得被普通 upsert/token 刷新清掉。
		row.BuildAPIFallback = existing.BuildAPIFallback
		if err := tx.Save(&row).Error; err != nil {
			return repository.AccountUpsertResult{}, accountModel{}, err
		}
		if err := saveAccountRelations(tx, value, row.ID); err != nil {
			return repository.AccountUpsertResult{}, accountModel{}, err
		}
		return repository.AccountUpsertResult{ID: row.ID}, row, nil
	}
	if row.AuthStatus == "" {
		row.AuthStatus = string(account.AuthStatusActive)
	}
	if row.Priority == 0 {
		row.Priority = account.DefaultPriority
	}
	if row.MaxConcurrent == 0 {
		row.MaxConcurrent = account.DefaultMaxConcurrent
	}
	row.Enabled = true
	if err := tx.Create(&row).Error; err != nil {
		return repository.AccountUpsertResult{}, accountModel{}, err
	}
	if err := saveAccountRelations(tx, value, row.ID); err != nil {
		return repository.AccountUpsertResult{}, accountModel{}, err
	}
	return repository.AccountUpsertResult{ID: row.ID, Created: true}, row, nil
}

func (r *AccountRepository) Update(ctx context.Context, value account.Credential) (account.Credential, error) {
	row := fromAccountDomain(value)
	if err := r.db.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(&row).Error; err != nil {
			return err
		}
		return saveAccountRelations(tx, value, row.ID)
	}); err != nil {
		return account.Credential{}, mapError(err)
	}
	return r.Get(ctx, row.ID)
}

func saveAccountRelations(tx *gorm.DB, value account.Credential, accountID uint64) error {
	value.ID = accountID
	credential := fromAccountCredentialDomain(value)
	if err := tx.Save(&credential).Error; err != nil {
		return err
	}
	if profile := fromWebProfileDomain(value); profile != nil {
		return tx.Save(profile).Error
	}
	return tx.Where("account_id = ?", accountID).Delete(&webAccountProfileModel{}).Error
}

func (r *AccountRepository) UpdateMany(ctx context.Context, ids []uint64, updates repository.AccountUpdates) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	values := make(map[string]any, 4)
	if updates.Enabled != nil {
		values["enabled"] = *updates.Enabled
	}
	if updates.Priority != nil {
		values["priority"] = *updates.Priority
	}
	if updates.MaxConcurrent != nil {
		values["max_concurrent"] = *updates.MaxConcurrent
	}
	if updates.MinimumRemaining != nil {
		values["minimum_remaining"] = *updates.MinimumRemaining
	}
	if len(values) == 0 {
		return 0, nil
	}
	result := r.db.db.WithContext(ctx).Model(&accountModel{}).Where("id IN ?", ids).Updates(values)
	return result.RowsAffected, result.Error
}

func (r *AccountRepository) Delete(ctx context.Context, id uint64) error {
	result := r.db.db.WithContext(ctx).Delete(&accountModel{}, id)
	if result.Error != nil {
		return mapError(result.Error)
	}
	if result.RowsAffected == 0 {
		return repository.ErrNotFound
	}
	return nil
}

func (r *AccountRepository) DeleteMany(ctx context.Context, ids []uint64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	result := r.db.db.WithContext(ctx).Where("id IN ?", ids).Delete(&accountModel{})
	return result.RowsAffected, result.Error
}

func (r *AccountRepository) UpdateTokens(ctx context.Context, id uint64, accessToken, refreshToken string, expiresAt time.Time) (account.Credential, error) {
	now := time.Now().UTC()
	refreshDueAt := account.CredentialRefreshDueAt(id, expiresAt)
	updates := map[string]any{
		"encrypted_primary": accessToken, "expires_at": expiresAt, "refresh_due_at": refreshDueAt,
		"last_refresh_at": now, "refresh_failures": 0, "last_refresh_error": "", "refresh_permanent": false, "updated_at": now,
	}
	if refreshToken != "" {
		updates["encrypted_refresh"] = refreshToken
	}
	if err := r.db.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&accountCredentialModel{}).Where("account_id = ?", id).Updates(updates).Error; err != nil {
			return err
		}
		return tx.Model(&accountModel{}).Where("id = ?", id).Updates(map[string]any{"auth_status": string(account.AuthStatusActive), "last_error": ""}).Error
	}); err != nil {
		return account.Credential{}, err
	}
	return r.Get(ctx, id)
}

// BackfillCredentialRefreshSchedules 为升级前凭据分批补齐调度时间，不解密 Token，也不发起 OAuth 请求。
func (r *AccountRepository) BackfillCredentialRefreshSchedules(ctx context.Context, now time.Time, limit int) (int, error) {
	if limit < 1 {
		return 0, nil
	}
	var rows []struct {
		AccountID        uint64
		ExpiresAt        *time.Time
		EncryptedPrimary string
	}
	err := r.db.db.WithContext(ctx).
		Table("account_credentials AS credential").
		Select("credential.account_id, credential.expires_at, credential.encrypted_primary").
		Joins("JOIN provider_accounts AS account ON account.id = credential.account_id").
		Where("account.provider = ? AND account.enabled = ? AND account.auth_status = ?", account.ProviderBuild, true, account.AuthStatusActive).
		Where("credential.auth_type = ? AND credential.encrypted_refresh <> '' AND credential.refresh_due_at IS NULL", account.AuthTypeOAuth).
		Where("credential.expires_at IS NOT NULL OR credential.encrypted_primary = ''").
		Order("credential.account_id ASC").Limit(limit).Scan(&rows).Error
	if err != nil || len(rows) == 0 {
		return 0, err
	}
	err = r.db.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, row := range rows {
			dueAt := now
			if row.EncryptedPrimary != "" && row.ExpiresAt != nil && !row.ExpiresAt.IsZero() {
				dueAt = account.CredentialRefreshDueAt(row.AccountID, *row.ExpiresAt)
			}
			if err := tx.Model(&accountCredentialModel{}).Where("account_id = ? AND refresh_due_at IS NULL", row.AccountID).Update("refresh_due_at", dueAt).Error; err != nil {
				return err
			}
		}
		return nil
	})
	return len(rows), err
}

// ListCriticalCredentialRefreshIDs 只返回重启后必须优先恢复的凭据，避免启动时刷新整个账号池。
func (r *AccountRepository) ListCriticalCredentialRefreshIDs(ctx context.Context, now, expiresBefore time.Time, limit int) ([]uint64, error) {
	if limit < 1 {
		return []uint64{}, nil
	}
	var ids []uint64
	err := r.db.db.WithContext(ctx).
		Table("account_credentials AS credential").
		Select("credential.account_id").
		Joins("JOIN provider_accounts AS account ON account.id = credential.account_id").
		Where("account.provider = ? AND account.enabled = ? AND account.auth_status = ?", account.ProviderBuild, true, account.AuthStatusActive).
		Where("credential.auth_type = ? AND credential.encrypted_refresh <> ''", account.AuthTypeOAuth).
		Where("credential.encrypted_primary = '' OR credential.expires_at <= ? OR (credential.refresh_failures > 0 AND credential.refresh_due_at IS NOT NULL AND credential.refresh_due_at <= ?)", expiresBefore.UTC(), now.UTC()).
		Order(gorm.Expr("CASE WHEN credential.encrypted_primary = '' THEN 0 WHEN credential.expires_at <= ? THEN 1 ELSE 2 END, credential.expires_at ASC, credential.account_id ASC", now.UTC())).
		Limit(limit).
		Scan(&ids).Error
	return ids, err
}

func (r *AccountRepository) ListDueCredentialRefreshIDs(ctx context.Context, now time.Time, limit int) ([]uint64, error) {
	if limit < 1 {
		return []uint64{}, nil
	}
	var ids []uint64
	err := r.db.db.WithContext(ctx).
		Table("account_credentials AS credential").
		Select("credential.account_id").
		Joins("JOIN provider_accounts AS account ON account.id = credential.account_id").
		Where("account.provider = ? AND account.enabled = ? AND account.auth_status = ?", account.ProviderBuild, true, account.AuthStatusActive).
		Where("credential.auth_type = ? AND credential.encrypted_refresh <> '' AND credential.refresh_due_at IS NOT NULL AND credential.refresh_due_at <= ?", account.AuthTypeOAuth, now).
		Order("credential.refresh_due_at ASC, credential.account_id ASC").Limit(limit).Scan(&ids).Error
	return ids, err
}

func (r *AccountRepository) NextCredentialRefreshDueAt(ctx context.Context) (*time.Time, error) {
	var rows []struct{ RefreshDueAt time.Time }
	err := r.db.db.WithContext(ctx).
		Table("account_credentials AS credential").
		Select("credential.refresh_due_at").
		Joins("JOIN provider_accounts AS account ON account.id = credential.account_id").
		Where("account.provider = ? AND account.enabled = ? AND account.auth_status = ?", account.ProviderBuild, true, account.AuthStatusActive).
		Where("credential.auth_type = ? AND credential.encrypted_refresh <> '' AND credential.refresh_due_at IS NOT NULL", account.AuthTypeOAuth).
		Order("credential.refresh_due_at ASC, credential.account_id ASC").Limit(1).Scan(&rows).Error
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	value := rows[0].RefreshDueAt.UTC()
	return &value, nil
}

func (r *AccountRepository) UpdateCredentialRefreshFailure(ctx context.Context, id uint64, failureCount int, retryAt time.Time, errorCode string, permanent bool) error {
	return r.db.db.WithContext(ctx).Model(&accountCredentialModel{}).Where("account_id = ?", id).Updates(map[string]any{
		"refresh_due_at": retryAt.UTC(), "refresh_failures": max(0, failureCount),
		"last_refresh_error": truncate(errorCode, 100), "refresh_permanent": permanent, "updated_at": time.Now().UTC(),
	}).Error
}

// MarkBuildAPIFallback 仅对 grok_build 账号幂等设置/清除 XAI 推理回退标记。
func (r *AccountRepository) MarkBuildAPIFallback(ctx context.Context, id uint64, enabled bool) error {
	result := r.db.db.WithContext(ctx).Model(&accountModel{}).
		Where("id = ? AND provider = ?", id, account.ProviderBuild).
		Update("build_api_fallback", enabled)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		var count int64
		if err := r.db.db.WithContext(ctx).Model(&accountModel{}).Where("id = ?", id).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return repository.ErrNotFound
		}
		return fmt.Errorf("仅 grok_build 账号支持 Build API 降级标记")
	}
	return nil
}

func (r *AccountRepository) UpdateObservedModel(ctx context.Context, id uint64, model string, observedAt time.Time) error {
	return r.db.db.WithContext(ctx).Model(&accountModel{}).Where("id = ?", id).Updates(map[string]any{"observed_model": truncate(model, 255), "observed_model_at": observedAt}).Error
}

func (r *AccountRepository) UpdateHealth(ctx context.Context, id uint64, failureCount int, cooldownUntil *time.Time, lastError string, success bool) error {
	updates := map[string]any{"failure_count": failureCount, "cooldown_until": cooldownUntil, "last_error": truncate(lastError, 512)}
	if success {
		now := time.Now().UTC()
		updates["last_used_at"] = &now
	}
	return r.db.db.WithContext(ctx).Model(&accountModel{}).Where("id = ?", id).Updates(updates).Error
}

func (r *AccountRepository) UpsertModelQuotaBlock(ctx context.Context, value account.ModelQuotaBlock) error {
	value.UpstreamModel = strings.TrimSpace(value.UpstreamModel)
	value.Reason = strings.TrimSpace(value.Reason)
	if value.AccountID == 0 || value.UpstreamModel == "" || value.Reason == "" || value.CooldownUntil.IsZero() {
		return repository.ErrConflict
	}
	now := time.Now().UTC()
	row := accountModelQuotaBlockModel{
		AccountID: value.AccountID, UpstreamModel: truncate(value.UpstreamModel, 255), Reason: truncate(value.Reason, 100),
		CooldownUntil: value.CooldownUntil.UTC(), UpdatedAt: now,
	}
	return r.db.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "account_id"}, {Name: "upstream_model"}},
		DoUpdates: clause.Assignments(map[string]any{
			"reason":         gorm.Expr("CASE WHEN cooldown_until > ? THEN reason ELSE ? END", row.CooldownUntil, row.Reason),
			"cooldown_until": gorm.Expr("CASE WHEN cooldown_until > ? THEN cooldown_until ELSE ? END", row.CooldownUntil, row.CooldownUntil), "updated_at": now,
		}),
	}).Create(&row).Error
}

func (r *AccountRepository) PruneExpiredModelQuotaBlocks(ctx context.Context, now time.Time, limit int) (int64, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	var rows []accountModelQuotaBlockModel
	if err := r.db.db.WithContext(ctx).Select("account_id", "upstream_model").Where("cooldown_until <= ?", now.UTC()).Order("cooldown_until ASC").Limit(limit).Find(&rows).Error; err != nil || len(rows) == 0 {
		return 0, err
	}
	var deleted int64
	err := r.db.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, row := range rows {
			result := tx.Where("account_id = ? AND upstream_model = ? AND cooldown_until <= ?", row.AccountID, row.UpstreamModel, now.UTC()).Delete(&accountModelQuotaBlockModel{})
			if result.Error != nil {
				return result.Error
			}
			deleted += result.RowsAffected
		}
		return nil
	})
	return deleted, err
}

func (r *AccountRepository) SaveBilling(ctx context.Context, value account.Billing) error {
	history, err := json.Marshal(value.History)
	if err != nil {
		return err
	}
	row := billingModel{AccountID: value.AccountID, PlanCode: truncate(value.PlanCode, 100), PlanName: truncate(value.PlanName, 160), MonthlyLimit: value.MonthlyLimit, Used: value.Used, OnDemandCap: value.OnDemandCap, OnDemandUsed: value.OnDemandUsed, PrepaidBalance: value.PrepaidBalance, CreditUsagePercent: value.CreditUsagePercent, IsUnifiedBillingUser: value.IsUnifiedBillingUser, OnDemandEnabled: value.OnDemandEnabled, TopUpMethod: truncate(value.TopUpMethod, 100), UsagePeriodType: truncate(value.UsagePeriodType, 100), UsagePeriodStart: truncate(value.UsagePeriodStart, 64), UsagePeriodEnd: truncate(value.UsagePeriodEnd, 64), BillingPeriodStart: truncate(value.BillingPeriodStart, 64), BillingPeriodEnd: truncate(value.BillingPeriodEnd, 64), HistoryJSON: string(history), SyncedAt: value.SyncedAt}
	return r.db.db.WithContext(ctx).Save(&row).Error
}

func (r *AccountRepository) GetBilling(ctx context.Context, accountID uint64) (account.Billing, error) {
	var row billingModel
	if err := r.db.db.WithContext(ctx).First(&row, "account_id = ?", accountID).Error; err != nil {
		return account.Billing{}, mapError(err)
	}
	return toBillingDomain(row), nil
}

func (r *AccountRepository) GetBillings(ctx context.Context, accountIDs []uint64) (map[uint64]account.Billing, error) {
	result := make(map[uint64]account.Billing, len(accountIDs))
	if len(accountIDs) == 0 {
		return result, nil
	}
	var rows []billingModel
	if err := r.db.db.WithContext(ctx).Where("account_id IN ?", accountIDs).Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		result[row.AccountID] = toBillingDomain(row)
	}
	return result, nil
}

func (r *AccountRepository) GetQuotaRecovery(ctx context.Context, accountID uint64) (account.QuotaRecovery, error) {
	var row quotaRecoveryModel
	if err := r.db.db.WithContext(ctx).First(&row, "account_id = ?", accountID).Error; err != nil {
		return account.QuotaRecovery{}, mapError(err)
	}
	return account.QuotaRecovery{
		AccountID: row.AccountID, Kind: account.QuotaRecoveryKind(row.Kind), Status: account.QuotaRecoveryStatus(row.Status), ConfirmedUsed: row.ConfirmedUsed,
		ConfirmedLimit: row.ConfirmedLimit, ExhaustedAt: row.ExhaustedAt, NextProbeAt: row.NextProbeAt,
		LastConfirmedAt: row.LastConfirmedAt, UpdatedAt: row.UpdatedAt,
	}, nil
}

func (r *AccountRepository) GetQuotaRecoveries(ctx context.Context, accountIDs []uint64) (map[uint64]account.QuotaRecovery, error) {
	result := make(map[uint64]account.QuotaRecovery, len(accountIDs))
	if len(accountIDs) == 0 {
		return result, nil
	}
	var rows []quotaRecoveryModel
	if err := r.db.db.WithContext(ctx).Where("account_id IN ?", accountIDs).Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		result[row.AccountID] = account.QuotaRecovery{
			AccountID: row.AccountID, Kind: account.QuotaRecoveryKind(row.Kind), Status: account.QuotaRecoveryStatus(row.Status), ConfirmedUsed: row.ConfirmedUsed,
			ConfirmedLimit: row.ConfirmedLimit, ExhaustedAt: row.ExhaustedAt, NextProbeAt: row.NextProbeAt,
			LastConfirmedAt: row.LastConfirmedAt, UpdatedAt: row.UpdatedAt,
		}
	}
	return result, nil
}

func (r *AccountRepository) SaveQuotaRecovery(ctx context.Context, value account.QuotaRecovery) error {
	row := quotaRecoveryModel{
		AccountID: value.AccountID, Kind: string(value.Kind), Status: string(value.Status), ConfirmedUsed: value.ConfirmedUsed,
		ConfirmedLimit: value.ConfirmedLimit, ExhaustedAt: value.ExhaustedAt, NextProbeAt: value.NextProbeAt,
		LastConfirmedAt: value.LastConfirmedAt, UpdatedAt: value.UpdatedAt,
	}
	return r.db.db.WithContext(ctx).Save(&row).Error
}

func (r *AccountRepository) ClaimQuotaProbe(ctx context.Context, accountID uint64, now, leaseUntil time.Time) (bool, error) {
	result := r.db.db.WithContext(ctx).Model(&quotaRecoveryModel{}).
		Where("account_id = ? AND status IN ? AND next_probe_at IS NOT NULL AND next_probe_at <= ?", accountID, []string{string(account.QuotaRecoveryStatusExhausted), string(account.QuotaRecoveryStatusProbing)}, now).
		Updates(map[string]any{"status": string(account.QuotaRecoveryStatusProbing), "next_probe_at": leaseUntil, "updated_at": now})
	return result.RowsAffected == 1, result.Error
}

func (r *AccountRepository) ClearQuotaRecovery(ctx context.Context, accountID uint64) error {
	return r.db.db.WithContext(ctx).Delete(&quotaRecoveryModel{}, "account_id = ?", accountID).Error
}

func (r *AccountRepository) HasQuotaWindows(ctx context.Context, accountID uint64) (bool, error) {
	var count int64
	err := r.db.db.WithContext(ctx).Model(&quotaWindowModel{}).Where("account_id = ? AND synced_at IS NOT NULL", accountID).Count(&count).Error
	return count > 0, err
}

func (r *AccountRepository) GetQuotaWindows(ctx context.Context, accountIDs []uint64) (map[uint64][]account.QuotaWindow, error) {
	result := make(map[uint64][]account.QuotaWindow, len(accountIDs))
	if len(accountIDs) == 0 {
		return result, nil
	}
	var rows []quotaWindowModel
	if err := r.db.db.WithContext(ctx).Where("account_id IN ?", accountIDs).Order("account_id ASC, mode ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		result[row.AccountID] = append(result[row.AccountID], toQuotaWindowDomain(row))
	}
	return result, nil
}

func (r *AccountRepository) SaveQuotaWindows(ctx context.Context, accountID uint64, tier account.WebTier, syncedAt time.Time, values []account.QuotaWindow) error {
	return r.saveQuotaWindows(ctx, accountID, tier, syncedAt, values, false)
}

func (r *AccountRepository) ReplaceQuotaWindows(ctx context.Context, accountID uint64, tier account.WebTier, syncedAt time.Time, values []account.QuotaWindow) error {
	return r.saveQuotaWindows(ctx, accountID, tier, syncedAt, values, true)
}

func (r *AccountRepository) saveQuotaWindows(ctx context.Context, accountID uint64, tier account.WebTier, syncedAt time.Time, values []account.QuotaWindow, replace bool) error {
	return r.db.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if tier != "" {
			profile := webAccountProfileModel{AccountID: accountID, Tier: string(tier), SyncedAt: &syncedAt}
			if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "account_id"}}, DoUpdates: clause.AssignmentColumns([]string{"tier", "synced_at"})}).Create(&profile).Error; err != nil {
				return err
			}
		}
		if replace {
			if err := tx.Where("account_id = ?", accountID).Delete(&quotaWindowModel{}).Error; err != nil {
				return err
			}
		}
		for _, value := range values {
			serializedBreakdown := make([]quotaBreakdownJSON, 0, len(value.Breakdown))
			for _, item := range value.Breakdown {
				serializedBreakdown = append(serializedBreakdown, quotaBreakdownJSON{ProductCode: item.ProductCode, UsagePercent: item.UsagePercent})
			}
			breakdownJSON, err := json.Marshal(serializedBreakdown)
			if err != nil {
				return err
			}
			row := quotaWindowModel{
				AccountID: accountID, Mode: truncate(strings.TrimSpace(value.Mode), 64), Remaining: max(0, value.Remaining), Total: max(0, value.Total),
				UsagePercent: min(100, max(0, value.UsagePercent)), BreakdownJSON: string(breakdownJSON),
				WindowSeconds: max(0, value.WindowSeconds), ResetAt: value.ResetAt, SyncedAt: value.SyncedAt, Source: string(value.Source), UpdatedAt: syncedAt,
			}
			if row.Source == "" {
				row.Source = string(account.QuotaSourceUpstream)
			}
			if err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "account_id"}, {Name: "mode"}},
				DoUpdates: clause.AssignmentColumns([]string{"remaining", "total", "usage_percent", "breakdown_json", "window_seconds", "reset_at", "synced_at", "source", "updated_at"}),
			}).Create(&row).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *AccountRepository) DecrementQuotaWindow(ctx context.Context, accountID uint64, mode string, now time.Time) (bool, error) {
	return r.DecrementQuotaWindowBy(ctx, accountID, mode, 1, now)
}

// DecrementQuotaWindowBy atomically reduces remaining quota. For console mode it
// also starts the delayed recovery timer once remaining falls to the rotate
// threshold, matching the multi-account pool rotation policy used by grok2api.
func (r *AccountRepository) DecrementQuotaWindowBy(ctx context.Context, accountID uint64, mode string, amount int, now time.Time) (bool, error) {
	if amount <= 0 {
		amount = 1
	}
	now = now.UTC()
	var updated bool
	err := r.db.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row quotaWindowModel
		query := tx.Where("account_id = ? AND mode = ?", accountID, mode)
		if r.db.dialect == "postgres" {
			query = query.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		if err := query.First(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		// Local console windows whose recovery timer already elapsed are refreshed
		// inline so a single request both restores and consumes one unit.
		if shouldResetExpiredLocalWindow(row, now) {
			row.Remaining = max(row.Total, 0)
			row.ResetAt = nil
			row.Source = string(account.QuotaSourceDefault)
		}
		if row.Remaining <= 0 {
			return nil
		}
		row.Remaining = max(0, row.Remaining-amount)
		row.UpdatedAt = now
		if shouldStartConsoleRotateTimer(row) {
			resetAt := now.Add(time.Duration(max(row.WindowSeconds, 0)) * time.Second)
			row.ResetAt = &resetAt
		}
		// Use Expr("NULL") so GORM actually clears reset_at; a bare nil is skipped.
		var resetAtValue any
		if row.ResetAt == nil {
			resetAtValue = gorm.Expr("NULL")
		} else {
			resetAtValue = *row.ResetAt
		}
		if err := tx.Model(&quotaWindowModel{}).
			Where("account_id = ? AND mode = ?", accountID, mode).
			Updates(map[string]any{
				"remaining":  row.Remaining,
				"reset_at":   resetAtValue,
				"source":     row.Source,
				"updated_at": row.UpdatedAt,
			}).Error; err != nil {
			return err
		}
		updated = true
		return nil
	})
	return updated, err
}

// ResetExpiredLocalQuotaWindows restores console windows whose delayed recovery
// timer has elapsed. Returns the number of restored rows.
func (r *AccountRepository) ResetExpiredLocalQuotaWindows(ctx context.Context, now time.Time) (int, error) {
	now = now.UTC()
	result := r.db.db.WithContext(ctx).Model(&quotaWindowModel{}).
		Where("mode = ? AND total > 0 AND reset_at IS NOT NULL AND reset_at <= ?", "console", now).
		Updates(map[string]any{
			"remaining":  gorm.Expr("total"),
			"reset_at":   gorm.Expr("NULL"),
			"source":     string(account.QuotaSourceDefault),
			"updated_at": now,
		})
	return int(result.RowsAffected), result.Error
}

// shouldStartConsoleRotateTimer implements delayed console quota rotation:
// start the recovery window only after remaining drops to the threshold.
func shouldStartConsoleRotateTimer(row quotaWindowModel) bool {
	if row.Mode != "console" || row.ResetAt != nil || row.WindowSeconds <= 0 {
		return false
	}
	const consoleRotateThreshold = 12
	return row.Remaining <= consoleRotateThreshold
}

func shouldResetExpiredLocalWindow(row quotaWindowModel, now time.Time) bool {
	if row.Mode != "console" || row.Total <= 0 || row.ResetAt == nil {
		return false
	}
	return !row.ResetAt.After(now)
}

func (r *AccountRepository) ExhaustQuotaWindow(ctx context.Context, accountID uint64, mode string, resetAt *time.Time, now time.Time) error {
	return r.db.db.WithContext(ctx).Model(&quotaWindowModel{}).Where("account_id = ? AND mode = ?", accountID, mode).
		Updates(map[string]any{"remaining": 0, "reset_at": resetAt, "updated_at": now}).Error
}

func (r *AccountRepository) ListDueQuotaWindows(ctx context.Context, now time.Time, limit int) ([]account.QuotaWindow, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	var rows []quotaWindowModel
	if err := r.db.db.WithContext(ctx).Where("remaining = 0 AND reset_at IS NOT NULL AND reset_at <= ?", now).Order("reset_at ASC, account_id ASC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	values := make([]account.QuotaWindow, 0, len(rows))
	for _, row := range rows {
		values = append(values, toQuotaWindowDomain(row))
	}
	return values, nil
}

func (r *AccountRepository) ListQuotaRecoveryWindows(ctx context.Context, limit int) ([]account.QuotaWindow, error) {
	if limit <= 0 || limit > 100000 {
		limit = 100000
	}
	var rows []quotaWindowModel
	if err := r.db.db.WithContext(ctx).Where("remaining = 0 AND reset_at IS NOT NULL").Order("reset_at ASC, account_id ASC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	values := make([]account.QuotaWindow, 0, len(rows))
	for _, row := range rows {
		values = append(values, toQuotaWindowDomain(row))
	}
	return values, nil
}

// ListStaleWebQuotaAccountIDs 返回缺失或长期未同步额度的 Web 账号，供重启后的低优先级追赶任务使用。
func (r *AccountRepository) ListStaleWebQuotaAccountIDs(ctx context.Context, before time.Time, limit int) ([]uint64, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	var ids []uint64
	err := r.db.db.WithContext(ctx).
		Table("provider_accounts AS account").
		Select("account.id").
		Joins("LEFT JOIN account_quota_windows AS quota ON quota.account_id = account.id").
		Where("account.provider = ? AND account.enabled = ? AND account.auth_status = ?", account.ProviderWeb, true, account.AuthStatusActive).
		Group("account.id").
		Having("MAX(quota.synced_at) IS NULL OR MAX(quota.synced_at) < ?", before.UTC()).
		// Healthy accounts first so slow catch-up spends budget on usable tokens.
		Order("MIN(account.failure_count) ASC, MIN(quota.synced_at) ASC, account.id ASC").
		Limit(limit).
		Scan(&ids).Error
	return ids, err
}

func toQuotaWindowDomain(row quotaWindowModel) account.QuotaWindow {
	var serializedBreakdown []quotaBreakdownJSON
	_ = json.Unmarshal([]byte(row.BreakdownJSON), &serializedBreakdown)
	breakdown := make([]account.QuotaBreakdown, 0, len(serializedBreakdown))
	for _, item := range serializedBreakdown {
		breakdown = append(breakdown, account.QuotaBreakdown{ProductCode: item.ProductCode, UsagePercent: item.UsagePercent})
	}
	return account.QuotaWindow{
		AccountID: row.AccountID, Mode: row.Mode, Remaining: row.Remaining, Total: row.Total,
		UsagePercent: row.UsagePercent, Breakdown: breakdown, WindowSeconds: row.WindowSeconds,
		ResetAt: row.ResetAt, SyncedAt: row.SyncedAt, Source: account.QuotaSource(row.Source), UpdatedAt: row.UpdatedAt,
	}
}
