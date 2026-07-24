package relational

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/chenyme/grok2api/backend/internal/domain/egress"
	"github.com/chenyme/grok2api/backend/internal/repository"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type EgressRepository struct{ db *Database }

func NewEgressRepository(db *Database) *EgressRepository { return &EgressRepository{db: db} }

func (r *EgressRepository) ListEgressNodes(ctx context.Context, scope egress.Scope, sort repository.SortQuery) ([]egress.Node, error) {
	query := r.db.db.WithContext(ctx).Model(&egressNodeModel{})
	if scope != "" {
		// Match primary scope or any multi-select entry stored in scopes CSV.
		// scopes is comma-separated; bound with commas so "grok_web" does not match "grok_web_asset".
		query = query.Where(
			"scope = ? OR scopes = ? OR scopes LIKE ? OR scopes LIKE ? OR scopes LIKE ?",
			scope, string(scope),
			string(scope)+",%",
			"%,"+string(scope),
			"%,"+string(scope)+",%",
		)
	}
	var rows []egressNodeModel
	query = applyStableSort(query, sort, map[string]sortSpec{
		"name":      {expression: "LOWER(egress_nodes.name)"},
		"scope":     {expression: "egress_nodes.scope"},
		"proxy":     {expression: "CASE WHEN egress_nodes.encrypted_proxy_url <> '' THEN 0 ELSE 1 END"},
		"clearance": {expression: "CASE WHEN egress_nodes.encrypted_cloudflare_cookie <> '' THEN 0 ELSE 1 END"},
		"health":    {expression: "egress_nodes.health", defaultDirection: repository.SortDescending},
	}, sortSpec{expression: "egress_nodes.scope"}, "egress_nodes.id")
	if err := query.Find(&rows).Error; err != nil {
		return nil, err
	}
	values := make([]egress.Node, 0, len(rows))
	for _, row := range rows {
		values = append(values, toEgressDomain(row))
	}
	return values, nil
}

func (r *EgressRepository) GetEgressNode(ctx context.Context, id uint64) (egress.Node, error) {
	var row egressNodeModel
	if err := r.db.db.WithContext(ctx).First(&row, id).Error; err != nil {
		return egress.Node{}, mapError(err)
	}
	return toEgressDomain(row), nil
}

func (r *EgressRepository) CreateEgressNode(ctx context.Context, value egress.Node) (egress.Node, error) {
	if err := validateEgressNode(value); err != nil {
		return egress.Node{}, err
	}
	row := fromEgressDomain(value)
	if err := r.db.db.WithContext(ctx).Create(&row).Error; err != nil {
		return egress.Node{}, mapError(err)
	}
	return toEgressDomain(row), nil
}

func (r *EgressRepository) UpdateEgressNode(ctx context.Context, value egress.Node) (egress.Node, error) {
	if err := validateEgressNode(value); err != nil {
		return egress.Node{}, err
	}
	row := fromEgressDomain(value)
	result := r.db.db.WithContext(ctx).Save(&row)
	if result.Error != nil {
		return egress.Node{}, mapError(result.Error)
	}
	if result.RowsAffected == 0 {
		return egress.Node{}, repository.ErrNotFound
	}
	return toEgressDomain(row), nil
}

func (r *EgressRepository) DeleteEgressNode(ctx context.Context, id uint64) error {
	result := r.db.db.WithContext(ctx).Delete(&egressNodeModel{}, id)
	if result.Error != nil {
		return mapError(result.Error)
	}
	if result.RowsAffected == 0 {
		return repository.ErrNotFound
	}
	return nil
}

func toEgressDomain(row egressNodeModel) egress.Node {
	scopes := parseScopesCSV(row.ScopesCSV, row.Scope)
	primary := egress.Scope(row.Scope)
	if primary == "" && len(scopes) > 0 {
		primary = scopes[0]
	}
	return egress.Node{
		ID: row.ID, Name: row.Name, Scope: primary, Scopes: scopes, Enabled: row.Enabled,
		EncryptedProxyURL: row.EncryptedProxyURL, UserAgent: row.UserAgent, EncryptedCloudflareCookie: row.EncryptedCloudflareCookie,
		Health: row.Health, FailureCount: row.FailureCount, CooldownUntil: row.CooldownUntil, LastError: row.LastError,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func fromEgressDomain(value egress.Node) egressNodeModel {
	health := value.Health
	if health == 0 && value.ID == 0 {
		health = 1
	}
	scopes := value.EffectiveScopes()
	primary := string(value.Scope)
	if primary == "" && len(scopes) > 0 {
		primary = string(scopes[0])
	}
	return egressNodeModel{
		ID: value.ID, Name: value.Name, Scope: primary, ScopesCSV: encodeScopesCSV(scopes), Enabled: value.Enabled,
		EncryptedProxyURL: value.EncryptedProxyURL, UserAgent: value.UserAgent, EncryptedCloudflareCookie: value.EncryptedCloudflareCookie,
		Health: health, FailureCount: value.FailureCount, CooldownUntil: value.CooldownUntil, LastError: value.LastError,
		CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
	}
}

func parseScopesCSV(raw, primary string) []egress.Scope {
	seen := make(map[egress.Scope]struct{})
	out := make([]egress.Scope, 0, 4)
	push := func(item string) {
		item = strings.TrimSpace(item)
		if item == "" {
			return
		}
		scope := egress.Scope(item)
		if !scope.IsValid() {
			return
		}
		if _, ok := seen[scope]; ok {
			return
		}
		seen[scope] = struct{}{}
		out = append(out, scope)
	}
	if strings.TrimSpace(raw) != "" {
		for _, part := range strings.Split(raw, ",") {
			push(part)
		}
	}
	if len(out) == 0 {
		push(primary)
	}
	return out
}

func encodeScopesCSV(scopes []egress.Scope) string {
	if len(scopes) == 0 {
		return ""
	}
	parts := make([]string, 0, len(scopes))
	seen := make(map[egress.Scope]struct{}, len(scopes))
	for _, scope := range scopes {
		if !scope.IsValid() {
			continue
		}
		if _, ok := seen[scope]; ok {
			continue
		}
		seen[scope] = struct{}{}
		parts = append(parts, string(scope))
	}
	return strings.Join(parts, ",")
}

func validateEgressNode(value egress.Node) error {
	scopes := value.EffectiveScopes()
	if len(scopes) == 0 {
		return fmt.Errorf("%w: 出口节点作用域不能为空", repository.ErrInvalidInput)
	}
	for _, scope := range scopes {
		if !scope.IsValid() {
			return fmt.Errorf("%w: 出口节点作用域无效", repository.ErrInvalidInput)
		}
	}
	if strings.TrimSpace(value.Name) == "" {
		return fmt.Errorf("%w: 出口节点名称不能为空", repository.ErrInvalidInput)
	}
	return nil
}


func (r *EgressRepository) GetEgressOperationsConfig(ctx context.Context) (egress.OperationsConfig, error) {
	var row egressOperationsConfigModel
	if err := r.db.db.WithContext(ctx).First(&row, 1).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return egress.DefaultOperationsConfig(), nil
		}
		return egress.OperationsConfig{}, mapError(err)
	}
	return toEgressOperationsConfigDomain(row), nil
}

func (r *EgressRepository) SaveEgressOperationsConfig(ctx context.Context, value egress.OperationsConfig) (egress.OperationsConfig, error) {
	row := fromEgressOperationsConfigDomain(value)
	row.ID = 1
	if err := r.db.db.WithContext(ctx).Clauses(clause.OnConflict{UpdateAll: true}).Create(&row).Error; err != nil {
		return egress.OperationsConfig{}, mapError(err)
	}
	return toEgressOperationsConfigDomain(row), nil
}

func toEgressOperationsConfigDomain(row egressOperationsConfigModel) egress.OperationsConfig {
	return egress.OperationsConfig{
		ProbeIntervalSeconds: row.ProbeIntervalSeconds, AutoAssignEnabled: row.AutoAssignEnabled, AutoBalanceEnabled: row.AutoBalanceEnabled,
		AssignmentIntervalSeconds: row.AssignmentIntervalSeconds,
		Fallbacks: map[egress.Scope]egress.FallbackConfig{
			egress.ScopeBuild:    {Mode: egress.FallbackMode(row.BuildFallbackMode).Normalized(), NodeID: row.BuildFallbackNodeID},
			egress.ScopeWeb:      {Mode: egress.FallbackMode(row.WebFallbackMode).Normalized(), NodeID: row.WebFallbackNodeID},
			egress.ScopeConsole:  {Mode: egress.FallbackMode(row.ConsoleFallbackMode).Normalized(), NodeID: row.ConsoleFallbackNodeID},
			egress.ScopeWebAsset: {Mode: egress.FallbackMode(row.WebAssetFallbackMode).Normalized(), NodeID: row.WebAssetFallbackNodeID},
		},
		UpdatedAt: row.UpdatedAt,
	}
}

func fromEgressOperationsConfigDomain(value egress.OperationsConfig) egressOperationsConfigModel {
	buildFallback := value.FallbackFor(egress.ScopeBuild)
	webFallback := value.FallbackFor(egress.ScopeWeb)
	consoleFallback := value.FallbackFor(egress.ScopeConsole)
	webAssetFallback := value.FallbackFor(egress.ScopeWebAsset)
	return egressOperationsConfigModel{
		ID: 1, ProbeIntervalSeconds: value.ProbeIntervalSeconds, AutoAssignEnabled: value.AutoAssignEnabled,
		AutoBalanceEnabled: value.AutoBalanceEnabled, AssignmentIntervalSeconds: value.AssignmentIntervalSeconds,
		BuildFallbackMode: string(buildFallback.Mode), BuildFallbackNodeID: buildFallback.NodeID,
		WebFallbackMode: string(webFallback.Mode), WebFallbackNodeID: webFallback.NodeID,
		ConsoleFallbackMode: string(consoleFallback.Mode), ConsoleFallbackNodeID: consoleFallback.NodeID,
		WebAssetFallbackMode: string(webAssetFallback.Mode), WebAssetFallbackNodeID: webAssetFallback.NodeID,
		UpdatedAt: value.UpdatedAt,
	}
}
