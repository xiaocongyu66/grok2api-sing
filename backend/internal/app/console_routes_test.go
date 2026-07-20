package app

import (
	"strings"
	"testing"

	"github.com/chenyme/grok2api/backend/internal/domain/account"
	consoleprovider "github.com/chenyme/grok2api/backend/internal/infra/provider/console"
)

func TestConsoleRoutesUseStableProviderNamespace(t *testing.T) {
	routes := consoleprovider.Routes()
	if len(routes) == 0 {
		t.Fatal("console catalog is empty")
	}
	seen := make(map[string]bool, len(routes))
	for _, route := range routes {
		if route.Provider != account.ProviderConsole || !strings.HasPrefix(route.PublicID, "Console/") {
			t.Fatalf("non-canonical console route = %#v", route)
		}
		if seen[route.PublicID] {
			t.Fatalf("duplicate console public id %q", route.PublicID)
		}
		seen[route.PublicID] = true
	}
	// Base catalog + thinking/console aliases are real rows (own ids for client-key ACL).
	if !seen["Console/grok-4.3"] {
		t.Fatal("canonical Console/grok-4.3 route is missing")
	}
	if !seen["Console/grok-4.3-console"] {
		t.Fatal("console alias route Console/grok-4.3-console is missing")
	}
	if !seen["Console/grok-4.20-multi-agent-xhigh"] {
		t.Fatal("effort alias route Console/grok-4.20-multi-agent-xhigh is missing")
	}
	// Multiple public IDs may share the same upstream (effort shortcuts).
	upstreams := make(map[string]int)
	for _, route := range routes {
		upstreams[route.UpstreamModel]++
	}
	if upstreams["grok-4.3"] < 2 {
		t.Fatalf("expected shared upstream grok-4.3 across base+aliases, got %d", upstreams["grok-4.3"])
	}
}
