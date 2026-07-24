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

// FallbackMode controls what happens when no primary egress node can be acquired.
// Default is none so upgrades keep fail-closed behavior.
type FallbackMode string

const (
	FallbackModeNone   FallbackMode = "none"
	FallbackModeDirect FallbackMode = "direct"
	FallbackModeFixed  FallbackMode = "fixed"
)

func (value FallbackMode) IsValid() bool {
	switch value {
	case FallbackModeNone, FallbackModeDirect, FallbackModeFixed:
		return true
	default:
		return false
	}
}

// Normalized maps the zero value from pre-fallback rows to disabled mode.
func (value FallbackMode) Normalized() FallbackMode {
	if value == "" {
		return FallbackModeNone
	}
	return value
}

type FallbackConfig struct {
	Mode   FallbackMode
	NodeID uint64
}

// OperationsConfig controls egress fallback (and optional ops intervals).
type OperationsConfig struct {
	ProbeIntervalSeconds      int
	AutoAssignEnabled         bool
	AutoBalanceEnabled        bool
	AssignmentIntervalSeconds int
	Fallbacks                 map[Scope]FallbackConfig
	UpdatedAt                 time.Time
}

func DefaultOperationsConfig() OperationsConfig {
	return OperationsConfig{
		ProbeIntervalSeconds:      900,
		AssignmentIntervalSeconds: 300,
		Fallbacks: map[Scope]FallbackConfig{
			ScopeBuild:    {Mode: FallbackModeNone},
			ScopeWeb:      {Mode: FallbackModeNone},
			ScopeConsole:  {Mode: FallbackModeNone},
			ScopeWebAsset: {Mode: FallbackModeNone},
		},
	}
}

// FallbackFor returns a canonical fallback for scope (safe with sparse maps).
func (value OperationsConfig) FallbackFor(scope Scope) FallbackConfig {
	fallback := value.Fallbacks[scope]
	fallback.Mode = fallback.Mode.Normalized()
	if fallback.Mode != FallbackModeFixed {
		fallback.NodeID = 0
	}
	return fallback
}

// SupportsScope reports whether a node primary scope can serve requestScope.
// Local multi-scope nodes should prefer Node.MatchesScope; this mirrors upstream
// single-scope compatibility (Console/WebAsset may reuse Web).
func SupportsScope(nodeScope, requestScope Scope) bool {
	if nodeScope == requestScope {
		return true
	}
	return (requestScope == ScopeWebAsset || requestScope == ScopeConsole) && nodeScope == ScopeWeb
}
