package cli

import (
	"strings"
	"testing"

	"github.com/chenyme/grok2api/backend/internal/domain/account"
	"github.com/chenyme/grok2api/backend/internal/infra/provider"
)

func TestBuildReasoningEffortAliasesRegistered(t *testing.T) {
	registry := provider.NewRegistry(NewAdapter(Config{}, nil))
	for _, name := range []string{"grok-4.5-low", "grok-4.5-medium", "grok-4.5-high", "grok-4.5-xhigh"} {
		alias, ok := registry.ResolveModelAlias(name)
		if !ok {
			t.Fatalf("alias %q missing", name)
		}
		if alias.Provider != account.ProviderBuild || alias.UpstreamModel != "grok-4.5" {
			t.Fatalf("alias %q = %#v", name, alias)
		}
		if !strings.HasSuffix(name, alias.ReasoningEffort) {
			t.Fatalf("alias %q effort %q mismatch", name, alias.ReasoningEffort)
		}
	}
}
