package relational

import (
	"context"
	"fmt"
	"strings"

	"github.com/chenyme/grok2api/backend/internal/domain/egress"
	"github.com/chenyme/grok2api/backend/internal/repository"
)

type EgressRepository struct{ db *Database }

func NewEgressRepository(db *Database) *EgressRepository { return &EgressRepository{db: db} }

func (r *EgressRepository) ListEgressNodes(ctx context.Context, scope egress.Scope, sort repository.SortQuery) ([]egress.Node, error) {
	query := r.db.db.WithContext(ctx).Model(&egressNodeModel{})
	if scope != "" {
		// scope column may be multi-value: "grok_web" or "grok_web,grok_build".
		s := string(scope)
		query = query.Where(
			"scope = ? OR scope LIKE ? OR scope LIKE ? OR scope LIKE ?",
			s, s+",%", "%,"+s+",%", "%,"+s,
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
	if err := validateEgressNodeScopes(value); err != nil {
		return egress.Node{}, err
	}
	row := fromEgressDomain(value)
	if err := r.db.db.WithContext(ctx).Create(&row).Error; err != nil {
		return egress.Node{}, mapError(err)
	}
	return toEgressDomain(row), nil
}

func (r *EgressRepository) UpdateEgressNode(ctx context.Context, value egress.Node) (egress.Node, error) {
	if err := validateEgressNodeScopes(value); err != nil {
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

func validateEgressNodeScopes(value egress.Node) error {
	// Reject legacy "all" and any other non-provider scope at the repository boundary.
	// Multi-scope is stored as comma-separated values; each segment must be valid.
	scopes := value.EffectiveScopes()
	if len(scopes) == 0 {
		// Also catch raw Scope="all" when Scopes is empty and Scope is not a valid enum.
		raw := strings.TrimSpace(string(value.Scope))
		if raw == "" {
			return fmt.Errorf("egress scope is required")
		}
		if parsed := parseStoredScopes(raw); len(parsed) > 0 {
			scopes = parsed
		} else {
			return fmt.Errorf("invalid egress scope %q", raw)
		}
	}
	for _, scope := range scopes {
		if !scope.IsValid() {
			return fmt.Errorf("invalid egress scope %q", scope)
		}
	}
	return nil
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
	scopes := parseStoredScopes(row.Scope)
	primary := egress.Scope("")
	if len(scopes) > 0 {
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
	return egressNodeModel{
		ID: value.ID, Name: value.Name, Scope: encodeStoredScopes(value.EffectiveScopes()), Enabled: value.Enabled,
		EncryptedProxyURL: value.EncryptedProxyURL, UserAgent: value.UserAgent, EncryptedCloudflareCookie: value.EncryptedCloudflareCookie,
		Health: health, FailureCount: value.FailureCount, CooldownUntil: value.CooldownUntil, LastError: value.LastError,
		CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
	}
}

func parseStoredScopes(raw string) []egress.Scope {
	parts := strings.Split(raw, ",")
	out := make([]egress.Scope, 0, len(parts))
	seen := make(map[egress.Scope]struct{}, len(parts))
	for _, part := range parts {
		scope := egress.Scope(strings.TrimSpace(part))
		if !scope.IsValid() {
			continue
		}
		if _, ok := seen[scope]; ok {
			continue
		}
		seen[scope] = struct{}{}
		out = append(out, scope)
	}
	return out
}

func encodeStoredScopes(scopes []egress.Scope) string {
	if len(scopes) == 0 {
		return string(egress.ScopeBuild)
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
	if len(parts) == 0 {
		return string(egress.ScopeBuild)
	}
	return strings.Join(parts, ",")
}
