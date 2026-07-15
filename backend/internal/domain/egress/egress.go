package egress

import "time"

type Mode string

const (
	ModeDirect Mode = "direct"
	ModeSingle Mode = "single"
	ModePool   Mode = "pool"
)

type Scope string

const (
	ScopeBuild    Scope = "grok_build"
	ScopeWeb      Scope = "grok_web"
	ScopeConsole  Scope = "grok_console"
	ScopeWebAsset Scope = "grok_web_asset"
)

// AllScopes is the full set of assignable egress scopes.
var AllScopes = []Scope{ScopeBuild, ScopeWeb, ScopeConsole, ScopeWebAsset}

func (s Scope) IsValid() bool {
	switch s {
	case ScopeBuild, ScopeWeb, ScopeConsole, ScopeWebAsset:
		return true
	default:
		return false
	}
}

type Node struct {
	ID   uint64
	Name string
	// Scope is the primary scope (first of Scopes) kept for sorting/index compatibility.
	Scope Scope
	// Scopes lists every provider this node may serve (multi-select). Empty means {Scope}.
	Scopes                    []Scope
	Enabled                   bool
	EncryptedProxyURL         string
	UserAgent                 string
	EncryptedCloudflareCookie string
	Health                    float64
	FailureCount              int
	CooldownUntil             *time.Time
	LastError                 string
	CreatedAt                 time.Time
	UpdatedAt                 time.Time
}

// MatchesScope reports whether the node is eligible for the given provider scope.
func (n Node) MatchesScope(scope Scope) bool {
	if scope == "" {
		return true
	}
	for _, item := range n.EffectiveScopes() {
		if item == scope {
			return true
		}
	}
	return false
}

// EffectiveScopes returns Scopes, or [Scope] when Scopes is empty.
func (n Node) EffectiveScopes() []Scope {
	if len(n.Scopes) > 0 {
		return n.Scopes
	}
	if n.Scope != "" {
		return []Scope{n.Scope}
	}
	return nil
}

type PublicNode struct {
	ID              uint64
	Name            string
	Scope           Scope   // primary (first) scope
	Scopes          []Scope // all assigned scopes
	Enabled         bool
	ProxyConfigured bool
	// ProxyProtocol is a safe label (e.g. socks5, vmess, sing-box) without host/credentials.
	ProxyProtocol    string
	UserAgent        string
	CookieConfigured bool
	Health           float64
	FailureCount     int
	CooldownUntil    *time.Time
	LastError        string
	// Runtime request counters (in-memory; reset on process restart).
	SuccessCount   int64
	RequestCount   int64
	SuccessRate    float64
	FailureRate    float64
	Inflight       int
	LastProbeAt    *time.Time
	LastProbeOK    *bool
	LastProbeMs    int64
	LastProbeError string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Report summarizes pool-wide proxy usage for the admin UI.
type Report struct {
	TotalNodes   int
	EnabledNodes int
	ProxyNodes   int
	HealthyNodes int
	SuccessCount int64
	FailureCount int64
	RequestCount int64
	SuccessRate  float64
	FailureRate  float64
	Nodes        []PublicNode
}

// ProbeResult is the outcome of a one-click connectivity test through a node.
type ProbeResult struct {
	NodeID    uint64
	Name      string
	Scope     Scope
	OK        bool
	LatencyMs int64
	Status    int
	Error     string
	ProxyUsed bool
	CheckedAt time.Time
}
