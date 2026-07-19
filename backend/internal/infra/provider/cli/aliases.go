package cli

import (
	"github.com/chenyme/grok2api/backend/internal/domain/account"
	modeldomain "github.com/chenyme/grok2api/backend/internal/domain/model"
	"github.com/chenyme/grok2api/backend/internal/infra/provider"
)

// Build reasoning-effort aliases (client cannot always pass reasoning fields).
// Same pattern as Console multi-agent-low/medium/high/xhigh: one upstream model,
// fixed effort via gateway rewriteAliasedModel.
//
// Only appear in GET /v1/models when the target upstream exists in model_routes
// (after Build account sync discovers grok-4.5).
var buildAliases = []provider.ModelAlias{
	buildAlias("grok-4.5-low", "grok-4.5", "grok-4.5", "low"),
	buildAlias("grok-4.5-medium", "grok-4.5", "grok-4.5", "medium"),
	buildAlias("grok-4.5-high", "grok-4.5", "grok-4.5", "high"),
	buildAlias("grok-4.5-xhigh", "grok-4.5", "grok-4.5", "xhigh"),
}

func buildAlias(alias, publicModel, upstreamModel, effort string) provider.ModelAlias {
	canonical, _ := modeldomain.NormalizePublicID(account.ProviderBuild, publicModel)
	return provider.ModelAlias{
		Alias: alias, PublicModel: canonical, Provider: account.ProviderBuild,
		UpstreamModel: upstreamModel, ReasoningEffort: effort,
	}
}

// Aliases returns Build client-facing effort shortcuts.
func Aliases() []provider.ModelAlias {
	return append([]provider.ModelAlias(nil), buildAliases...)
}

// ModelAliases implements provider.ModelAliasAdapter.
func (a *Adapter) ModelAliases() []provider.ModelAlias { return Aliases() }
