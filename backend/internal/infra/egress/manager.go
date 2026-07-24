package egress

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	application "github.com/chenyme/grok2api/backend/internal/application/egress"
	accountdomain "github.com/chenyme/grok2api/backend/internal/domain/account"
	domain "github.com/chenyme/grok2api/backend/internal/domain/egress"
	settingsdomain "github.com/chenyme/grok2api/backend/internal/domain/settings"
	"github.com/chenyme/grok2api/backend/internal/infra/security"
	"github.com/chenyme/grok2api/backend/internal/pkg/neterror"
	"github.com/chenyme/grok2api/backend/internal/repository"
	"golang.org/x/sync/singleflight"
)

// softAffinityBaseSkew is the minimum inflight gap allowed before rebalancing away
// from a sticky proxy (keeps multi-turn sessions on the same node when load is even).
const softAffinityBaseSkew = 2

// softAffinitySkew returns how far above min inflight a sticky node may sit before
// traffic is rebalanced. Scales slowly with pool size so 64 concurrent / 6 proxies
// stays near ~10–12 per node without thrashing sticky sessions.
func softAffinitySkew(poolSize, minInflight int) int {
	if poolSize < 1 {
		poolSize = 1
	}
	// +1 per 4 nodes beyond the first, and +1 when baseline load is already high.
	skew := softAffinityBaseSkew + (poolSize-1)/4
	if minInflight >= 8 {
		skew++
	}
	if skew > 6 {
		return 6
	}
	return skew
}

const DefaultUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36"
const nodeSnapshotTTL = time.Second
const stickyProxyRetryLimit = 2

type Lease struct {
	NodeID    uint64
	NodeName  string
	Scope     domain.Scope
	ProxyURL  string
	UserAgent string
	CFCookies string
	client    requestClient
	browser   *browserClient
	sticky    bool
	release   func()
}

type requestClient interface {
	Do(*http.Request) (*http.Response, error)
	CloseIdleConnections()
}

func (l *Lease) Do(request *http.Request) (*http.Response, error) {
	if l == nil || l.client == nil {
		return nil, errors.New("出口客户端未初始化")
	}
	return l.do(request)
}
func (l *Lease) Release() {
	if l != nil && l.release != nil {
		l.release()
		l.release = nil
	}
}

// defaultProbeURL targets xAI Build edge so one-click tests exercise the same path
// clients use for inference (not a third-party IP echo).
const defaultProbeURL = "https://cli-chat-proxy.grok.com/"

// fallbackProbeURLs are tried when the primary returns transport errors.
var fallbackProbeURLs = []string{
	"https://api.x.ai/",
	"https://www.gstatic.com/generate_204",
}

type nodeRuntimeStats struct {
	successCount   int64
	failureCount   int64
	lastProbeAt    time.Time
	lastProbeOK    bool
	lastProbeMs    int64
	lastProbeError string
	probed         bool
}

const operationsConfigSnapshotTTL = 5 * time.Second

type Manager struct {
	repository           repository.EgressRepository
	cipher               *security.Cipher
	logger               *slog.Logger
	mu                   sync.Mutex
	clients              map[clientCacheKey]cachedClient
	inflight             map[uint64]int
	nodes                map[domain.Scope]cachedNodeSnapshot
	nodeLoads            singleflight.Group
	stats                map[uint64]*nodeRuntimeStats
	probeURL             string
	buildHeaderTimeout   atomic.Int64
	operationsMu         sync.RWMutex
	operationsConfig     cachedOperationsConfig
	operationsConfigLoad singleflight.Group
	operationsConfigVer  uint64
}

// operationsConfigRepository is optional so lightweight repos keep a narrow contract.
type operationsConfigRepository interface {
	GetEgressOperationsConfig(context.Context) (domain.OperationsConfig, error)
}

type cachedOperationsConfig struct {
	value     domain.OperationsConfig
	expiresAt time.Time
}

type cachedClient struct {
	client  requestClient
	browser *browserClient
}

type clientCacheKey struct {
	nodeID      uint64
	scope       domain.Scope
	fingerprint string
}

type cachedNodeSnapshot struct {
	values    []domain.Node
	expiresAt time.Time
}

func NewManager(repository repository.EgressRepository, cipher *security.Cipher) *Manager {
	manager := &Manager{
		repository: repository,
		cipher:     cipher,
		logger:     slog.Default(),
		clients:    make(map[clientCacheKey]cachedClient),
		inflight:   make(map[uint64]int),
		stats:      make(map[uint64]*nodeRuntimeStats),
		nodes:      make(map[domain.Scope]cachedNodeSnapshot),
		probeURL:   defaultProbeURL,
	}
	manager.buildHeaderTimeout.Store(int64(settingsdomain.DefaultBuildResponseHeaderTimeout))
	return manager
}

// UpdateBuildResponseHeaderTimeout rebuilds only cached Build clients.
// Active requests keep their current transport and are not interrupted.
func (m *Manager) UpdateBuildResponseHeaderTimeout(value time.Duration) {
	if m == nil {
		return
	}
	if value <= 0 {
		value = settingsdomain.DefaultBuildResponseHeaderTimeout
	}
	if previous := time.Duration(m.buildHeaderTimeout.Swap(int64(value))); previous == value {
		return
	}
	m.mu.Lock()
	var stale []requestClient
	for key, cached := range m.clients {
		if key.scope != domain.ScopeBuild {
			continue
		}
		if cached.client != nil {
			stale = append(stale, cached.client)
		}
		delete(m.clients, key)
	}
	m.mu.Unlock()
	for _, client := range stale {
		client.CloseIdleConnections()
	}
}

// InvalidateOperationsConfig drops the cached fallback policy so the next acquire reloads it.
func (m *Manager) InvalidateOperationsConfig() {
	if m == nil {
		return
	}
	m.operationsMu.Lock()
	m.operationsConfig = cachedOperationsConfig{}
	m.operationsConfigVer++
	m.operationsMu.Unlock()
}

// SetLogger attaches a structured logger for egress selection diagnostics.
func (m *Manager) SetLogger(logger *slog.Logger) {
	if m == nil {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}
	m.logger = logger
}

func (m *Manager) Acquire(ctx context.Context, scope domain.Scope, affinity string) (*Lease, error) {
	lease, _, err := m.acquire(ctx, scope, affinity, true, "")
	return lease, err
}

// AcquireCredential binds the outbound proxy identity to one persisted
// Provider credential. Resin templates use this identity as their Account.
func (m *Manager) AcquireCredential(ctx context.Context, scope domain.Scope, credential accountdomain.Credential) (*Lease, error) {
	identity := string(credential.Provider) + "_" + strconv.FormatUint(credential.ID, 10)
	credentialCookies := ""
	if scope != domain.ScopeBuild && strings.TrimSpace(credential.EncryptedCloudflareCookie) != "" {
		cookies, decryptErr := m.cipher.Decrypt(credential.EncryptedCloudflareCookie)
		if decryptErr != nil {
			return nil, decryptErr
		}
		credentialCookies = application.SanitizeCloudflareCookies(cookies)
	}
	// Web and Console accounts can be two database projections of the same SSO
	// login.  Resin must see one stable account identity across both channels;
	// otherwise the proxy rotates the IP while the clearance remains bound to
	// the other lease.  The digest is non-reversible and is only used as a proxy
	// template account label.
	if credential.AuthType == accountdomain.AuthTypeSSO && strings.TrimSpace(credential.EncryptedAccessToken) != "" {
		token, decryptErr := m.cipher.Decrypt(credential.EncryptedAccessToken)
		if decryptErr != nil {
			return nil, decryptErr
		}
		identity = "sso_" + security.HashToken(token)[:32]
	}
	ctx = WithAccountIdentity(ctx, identity)
	lease, _, err := m.acquire(ctx, scope, strconv.FormatUint(credential.ID, 10), true, credentialCookies)
	return lease, err
}

func (m *Manager) AcquireIfConfigured(ctx context.Context, scope domain.Scope, affinity string) (*Lease, bool, error) {
	return m.acquire(ctx, scope, affinity, false, "")
}

func (m *Manager) acquire(ctx context.Context, scope domain.Scope, affinity string, allowDirect bool, credentialCookies string) (*Lease, bool, error) {
	now := time.Now().UTC()
	configured := false
	var available []domain.Node
	var cooling []domain.Node
	fallbackConfig, fallbackSupported, fallbackConfigErr := m.loadOperationsConfig(ctx, now)
	fallback := domain.FallbackConfig{Mode: domain.FallbackModeNone}
	reservedFallbackNodes := make(map[uint64]struct{}, 4)
	if fallbackConfigErr == nil && fallbackSupported {
		fallback = fallbackConfig.FallbackFor(scope)
		for _, fallbackScope := range []domain.Scope{domain.ScopeBuild, domain.ScopeWeb, domain.ScopeConsole, domain.ScopeWebAsset} {
			configuredFallback := fallbackConfig.FallbackFor(fallbackScope)
			if configuredFallback.Mode == domain.FallbackModeFixed && configuredFallback.NodeID != 0 {
				reservedFallbackNodes[configuredFallback.NodeID] = struct{}{}
			}
		}
	}
	for _, candidateScope := range fallbackScopes(scope) {
		nodes, err := m.listNodes(ctx, candidateScope, now)
		if err != nil {
			return nil, false, err
		}
		configured = configured || len(nodes) > 0
		candidateAvailable := make([]domain.Node, 0, len(nodes))
		candidateCooling := make([]domain.Node, 0, len(nodes))
		for _, node := range nodes {
			if !node.Enabled {
				continue
			}
			// Fixed fallback nodes are last-resort only; exclude from primary pool.
			if _, reserved := reservedFallbackNodes[node.ID]; reserved {
				continue
			}
			if node.CooldownUntil == nil || !now.Before(*node.CooldownUntil) {
				candidateAvailable = append(candidateAvailable, node)
			} else {
				candidateCooling = append(candidateCooling, node)
			}
		}
		if len(candidateAvailable) > 0 {
			available = candidateAvailable
			break
		}
		if len(cooling) == 0 && len(candidateCooling) > 0 {
			// Keep first non-empty cooling set across fallback scopes for degraded pick.
			cooling = candidateCooling
		}
	}
	if len(available) == 0 {
		if configured {
			// Prefer cooling proxies over configured-pool outage, then configured fallback modes.
			if len(cooling) == 0 {
				primaryErr := fmt.Errorf("当前没有可用的 %s 出口节点", scope)
				lease, fallbackConfigured, applied, err := m.applyFallback(ctx, scope, affinity, allowDirect, credentialCookies, primaryErr, fallback, fallbackSupported, fallbackConfigErr)
				if err != nil {
					return nil, fallbackConfigured, err
				}
				if applied {
					return lease, fallbackConfigured, nil
				}
				return nil, false, primaryErr
			}
			available = cooling
		} else {
			lease, fallbackConfigured, applied, err := m.applyFallback(ctx, scope, affinity, allowDirect, credentialCookies, nil, fallback, fallbackSupported, fallbackConfigErr)
			if err != nil {
				return nil, fallbackConfigured, err
			}
			if applied {
				return lease, fallbackConfigured, nil
			}
			if !allowDirect {
				recordSelection(ctx, Selection{NodeName: "direct", Scope: scope})
				return nil, false, nil
			}
			available = []domain.Node{{ID: 0, Name: "direct", Scope: scope, Enabled: true, Health: 1}}
		}
	}
	sort.SliceStable(available, func(i, j int) bool {
		// Prefer sooner-available cooling nodes, then higher health, then stable ID order.
		ai, aj := available[i], available[j]
		if ai.CooldownUntil != nil && aj.CooldownUntil != nil && !ai.CooldownUntil.Equal(*aj.CooldownUntil) {
			return ai.CooldownUntil.Before(*aj.CooldownUntil)
		}
		if (ai.CooldownUntil == nil) != (aj.CooldownUntil == nil) {
			return ai.CooldownUntil == nil
		}
		if ai.Health != aj.Health {
			return ai.Health > aj.Health
		}
		return ai.ID < aj.ID
	})
	selected := m.selectNode(available, affinity)
	proxyURL, err := m.cipher.Decrypt(selected.EncryptedProxyURL)
	if err != nil {
		return nil, false, err
	}
	proxyURL, err = application.NormalizeProxyURL(proxyURL)
	if err != nil {
		return nil, false, err
	}
	sticky := strings.Contains(proxyURL, application.ProxyAccountPlaceholder)
	if sticky {
		accountKey := accountFromContext(ctx)
		if accountKey == "" && strings.TrimSpace(affinity) != "" {
			accountKey = string(scope) + "_" + strings.TrimSpace(affinity)
		}
		proxyURL, err = renderAccountProxyURL(proxyURL, accountKey)
		if err != nil {
			return nil, false, err
		}
	}
	cookies := ""
	if scope != domain.ScopeBuild {
		cookies, err = m.cipher.Decrypt(selected.EncryptedCloudflareCookie)
		if err != nil {
			return nil, false, err
		}
		cookies = application.SanitizeCloudflareCookies(cookies)
		if credentialCookies != "" {
			cookies = credentialCookies
		}
	}
	userAgent := ""
	if scope != domain.ScopeBuild {
		userAgent = strings.TrimSpace(selected.UserAgent)
	}
	if scope != domain.ScopeBuild && userAgent == "" {
		userAgent = DefaultUserAgent
	}
	client, err := m.clientFor(selected.ID, scope, proxyURL, userAgent, cookies, sticky)
	if err != nil {
		return nil, false, err
	}
	m.mu.Lock()
	m.inflight[selected.ID]++
	m.mu.Unlock()
	recordSelection(ctx, Selection{NodeID: selected.ID, NodeName: selected.Name, Scope: scope, Proxied: proxyURL != ""})
	var once sync.Once
	return &Lease{NodeID: selected.ID, NodeName: selected.Name, Scope: scope, ProxyURL: proxyURL, UserAgent: userAgent, CFCookies: cookies, client: client.client, browser: client.browser, sticky: sticky, release: func() {
		once.Do(func() {
			m.mu.Lock()
			m.inflight[selected.ID]--
			if m.inflight[selected.ID] <= 0 {
				delete(m.inflight, selected.ID)
			}
			m.mu.Unlock()
		})
	}}, true, nil
}

func renderAccountProxyURL(template, accountKey string) (string, error) {
	if !strings.Contains(template, application.ProxyAccountPlaceholder) {
		return template, nil
	}
	accountKey = normalizeProxyAccount(accountKey)
	if accountKey == "" {
		return "", errors.New("粘性代理需要有效的账号身份")
	}
	return strings.ReplaceAll(template, application.ProxyAccountPlaceholder, accountKey), nil
}

func normalizeProxyAccount(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = strings.Map(func(character rune) rune {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '_' || character == '-' {
			return character
		}
		return '_'
	}, value)
	if len(value) <= 128 {
		return value
	}
	digest := sha256.Sum256([]byte(value))
	return value[:95] + "_" + fmt.Sprintf("%x", digest[:16])
}

func (m *Manager) applyFallback(ctx context.Context, scope domain.Scope, affinity string, allowDirect bool, credentialCookies string, primaryErr error, fallback domain.FallbackConfig, supported bool, configErr error) (*Lease, bool, bool, error) {
	if configErr != nil {
		return nil, false, false, fallbackError(primaryErr, fmt.Errorf("读取出口回退配置: %w", configErr))
	}
	if !supported {
		return nil, false, false, nil
	}
	switch fallback.Mode {
	case domain.FallbackModeNone:
		return nil, false, false, nil
	case domain.FallbackModeDirect:
		if !allowDirect {
			recordSelection(ctx, Selection{NodeName: "direct", Scope: scope})
			return nil, false, true, nil
		}
		// Re-enter acquire with empty pool path for direct node via temporary allow.
		lease, configured, err := m.acquireDirect(ctx, scope, affinity, credentialCookies)
		if err != nil {
			return nil, configured, false, fallbackError(primaryErr, fmt.Errorf("获取本地直连回退: %w", err))
		}
		return lease, configured, true, nil
	case domain.FallbackModeFixed:
		selected, err := m.fixedFallbackNode(ctx, scope, fallback.NodeID)
		if err != nil {
			return nil, false, false, fallbackError(primaryErr, err)
		}
		// Build lease using the same path as primary selection for this node.
		return m.leaseFromSelectedNode(ctx, scope, affinity, selected, credentialCookies, true)
	default:
		return nil, false, false, fallbackError(primaryErr, fmt.Errorf("出口回退模式 %q 无效", fallback.Mode))
	}
}

func (m *Manager) acquireDirect(ctx context.Context, scope domain.Scope, affinity, credentialCookies string) (*Lease, bool, error) {
	selected := domain.Node{ID: 0, Name: "direct", Scope: scope, Enabled: true, Health: 1}
	lease, configured, _, err := m.leaseFromSelectedNode(ctx, scope, affinity, selected, credentialCookies, false)
	return lease, configured, err
}

func (m *Manager) loadOperationsConfig(ctx context.Context, now time.Time) (domain.OperationsConfig, bool, error) {
	configRepository, ok := m.repository.(operationsConfigRepository)
	if !ok {
		return domain.OperationsConfig{}, false, nil
	}
	m.operationsMu.RLock()
	cached := m.operationsConfig
	m.operationsMu.RUnlock()
	if !cached.expiresAt.IsZero() && now.Before(cached.expiresAt) {
		return cached.value, true, nil
	}
	loaded, err, _ := m.operationsConfigLoad.Do("operations", func() (any, error) {
		checkTime := time.Now().UTC()
		m.operationsMu.RLock()
		cached := m.operationsConfig
		m.operationsMu.RUnlock()
		if !cached.expiresAt.IsZero() && checkTime.Before(cached.expiresAt) {
			return cached.value, nil
		}
		m.operationsMu.RLock()
		version := m.operationsConfigVer
		m.operationsMu.RUnlock()
		value, err := configRepository.GetEgressOperationsConfig(ctx)
		if err != nil {
			return domain.OperationsConfig{}, err
		}
		m.operationsMu.Lock()
		if version == m.operationsConfigVer {
			m.operationsConfig = cachedOperationsConfig{value: value, expiresAt: checkTime.Add(operationsConfigSnapshotTTL)}
		}
		m.operationsMu.Unlock()
		return value, nil
	})
	if err != nil {
		return domain.OperationsConfig{}, true, err
	}
	return loaded.(domain.OperationsConfig), true, nil
}

func (m *Manager) fixedFallbackNode(ctx context.Context, scope domain.Scope, nodeID uint64) (domain.Node, error) {
	if nodeID == 0 {
		return domain.Node{}, errors.New("固定回退节点未指定")
	}
	selected, err := m.repository.GetEgressNode(ctx, nodeID)
	if err != nil {
		return domain.Node{}, fmt.Errorf("读取固定回退节点 %d: %w", nodeID, err)
	}
	if !selected.MatchesScope(scope) && !domain.SupportsScope(selected.Scope, scope) {
		return domain.Node{}, fmt.Errorf("固定回退节点 %d 与 %s 作用域不兼容", nodeID, scope)
	}
	if !selected.Enabled {
		return domain.Node{}, fmt.Errorf("固定回退节点 %d 已禁用", nodeID)
	}
	if strings.TrimSpace(selected.EncryptedProxyURL) == "" {
		return domain.Node{}, fmt.Errorf("固定回退节点 %d 未配置代理地址", nodeID)
	}
	if selected.CooldownUntil != nil && time.Now().UTC().Before(*selected.CooldownUntil) {
		return domain.Node{}, fmt.Errorf("固定回退节点 %d 正在冷却", nodeID)
	}
	proxyURL, err := m.cipher.Decrypt(selected.EncryptedProxyURL)
	if err != nil {
		return domain.Node{}, fmt.Errorf("读取固定回退节点 %d 代理配置: %w", nodeID, err)
	}
	proxyURL, err = application.NormalizeProxyURL(proxyURL)
	if err != nil || proxyURL == "" {
		return domain.Node{}, fmt.Errorf("固定回退节点 %d 代理地址无效", nodeID)
	}
	if strings.Contains(proxyURL, application.ProxyAccountPlaceholder) {
		return domain.Node{}, fmt.Errorf("固定回退节点 %d 使用账号代理模板", nodeID)
	}
	return selected, nil
}

func fallbackError(primaryErr, fallbackErr error) error {
	if primaryErr == nil {
		return fallbackErr
	}
	return fmt.Errorf("%w；出口回退不可用: %v", primaryErr, fallbackErr)
}

// leaseFromSelectedNode builds a lease for an already-chosen node (primary or fallback).
func (m *Manager) leaseFromSelectedNode(ctx context.Context, scope domain.Scope, affinity string, selected domain.Node, credentialCookies string, configured bool) (*Lease, bool, bool, error) {
	proxyURL, err := m.cipher.Decrypt(selected.EncryptedProxyURL)
	if err != nil {
		return nil, configured, false, err
	}
	proxyURL, err = application.NormalizeProxyURL(proxyURL)
	if err != nil {
		return nil, configured, false, err
	}
	sticky := strings.Contains(proxyURL, application.ProxyAccountPlaceholder)
	if sticky {
		accountKey := accountFromContext(ctx)
		if accountKey == "" && strings.TrimSpace(affinity) != "" {
			accountKey = string(scope) + "_" + strings.TrimSpace(affinity)
		}
		proxyURL, err = renderAccountProxyURL(proxyURL, accountKey)
		if err != nil {
			return nil, configured, false, err
		}
	}
	cookies := ""
	if scope != domain.ScopeBuild {
		cookies, err = m.cipher.Decrypt(selected.EncryptedCloudflareCookie)
		if err != nil {
			return nil, configured, false, err
		}
		if cookies == "" {
			cookies = credentialCookies
		}
	}
	userAgent := strings.TrimSpace(selected.UserAgent)
	if userAgent == "" {
		userAgent = DefaultUserAgent
	}
	client, err := m.clientFor(selected.ID, scope, proxyURL, userAgent, cookies, sticky)
	if err != nil {
		return nil, configured, false, err
	}
	recordSelection(ctx, Selection{NodeID: selected.ID, NodeName: selected.Name, Scope: scope, Proxied: proxyURL != ""})
	m.mu.Lock()
	m.inflight[selected.ID]++
	m.mu.Unlock()
	return &Lease{
		NodeID: selected.ID, NodeName: selected.Name, Scope: scope, ProxyURL: proxyURL,
		UserAgent: userAgent, CFCookies: cookies, client: client.client, browser: client.browser, sticky: sticky,
		release: func() {
			m.mu.Lock()
			if m.inflight[selected.ID] > 0 {
				m.inflight[selected.ID]--
			}
			m.mu.Unlock()
		},
	}, configured, true, nil
}

func (m *Manager) listNodes(ctx context.Context, scope domain.Scope, now time.Time) ([]domain.Node, error) {
	m.mu.Lock()
	if snapshot, ok := m.nodes[scope]; ok && now.Before(snapshot.expiresAt) {
		values := append([]domain.Node(nil), snapshot.values...)
		m.mu.Unlock()
		return values, nil
	}
	m.mu.Unlock()
	loaded, err, _ := m.nodeLoads.Do(string(scope), func() (any, error) {
		checkTime := time.Now().UTC()
		m.mu.Lock()
		if snapshot, ok := m.nodes[scope]; ok && checkTime.Before(snapshot.expiresAt) {
			values := append([]domain.Node(nil), snapshot.values...)
			m.mu.Unlock()
			return values, nil
		}
		m.mu.Unlock()
		values, err := m.repository.ListEgressNodes(ctx, scope, repository.SortQuery{})
		if err != nil {
			return nil, err
		}
		m.mu.Lock()
		m.nodes[scope] = cachedNodeSnapshot{values: append([]domain.Node(nil), values...), expiresAt: checkTime.Add(nodeSnapshotTTL)}
		m.mu.Unlock()
		return values, nil
	})
	if err != nil {
		return nil, err
	}
	return append([]domain.Node(nil), loaded.([]domain.Node)...), nil
}

func (m *Manager) invalidateNodes(scope domain.Scope) {
	m.mu.Lock()
	delete(m.nodes, scope)
	m.mu.Unlock()
}

func fallbackScopes(scope domain.Scope) []domain.Scope {
	if scope == domain.ScopeWebAsset {
		return []domain.Scope{domain.ScopeWebAsset, domain.ScopeWeb}
	}
	if scope == domain.ScopeConsole {
		// Console uses the same browser/clearance surface as Grok Web.  A
		// dedicated Console node is preferred, but a Web node is a safe and
		// expected fallback for deployments that configure one shared pool.
		return []domain.Scope{domain.ScopeConsole, domain.ScopeWeb}
	}
	return []domain.Scope{scope}
}

// selectNode load-balances by least inflight. Soft affinity keeps a sticky node when
// its load is within softAffinitySkew of the least-loaded peer so multi-session traffic
// spreads across proxies (64 concurrent / 6 nodes ≈ 10–12 each) without thrashing.
func (m *Manager) selectNode(nodes []domain.Node, affinity string) domain.Node {
	if len(nodes) == 1 {
		return nodes[0]
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	minInflight := m.inflight[nodes[0].ID]
	for _, node := range nodes[1:] {
		if cur := m.inflight[node.ID]; cur < minInflight {
			minInflight = cur
		}
	}

	skew := softAffinitySkew(len(nodes), minInflight)
	reason := "least_inflight"
	var selected domain.Node
	if affinity != "" {
		digest := sha256.Sum256([]byte(affinity))
		preferred := nodes[int(binary.BigEndian.Uint64(digest[:8])%uint64(len(nodes)))]
		prefLoad := m.inflight[preferred.ID]
		if preferred.Health >= 0.5 && prefLoad <= minInflight+skew {
			selected = preferred
			reason = "soft_affinity"
		}
	}
	if reason != "soft_affinity" {
		// No usable sticky node (missing affinity, overloaded, or unhealthy): least inflight.
		selected = nodes[0]
		for _, node := range nodes[1:] {
			ni, bi := m.inflight[node.ID], m.inflight[selected.ID]
			if ni < bi || (ni == bi && node.Health > selected.Health) || (ni == bi && node.Health == selected.Health && node.ID < selected.ID) {
				selected = node
			}
		}
		if affinity != "" {
			reason = "rebalance_least_inflight"
		}
	}

	if m.logger != nil {
		loads := make([]string, 0, len(nodes))
		for _, node := range nodes {
			loads = append(loads, fmt.Sprintf("%s=%d", node.Name, m.inflight[node.ID]))
		}
		m.logger.Info("egress_node_selected",
			"scope", selected.Scope,
			"node_id", selected.ID,
			"node_name", selected.Name,
			"reason", reason,
			"inflight", m.inflight[selected.ID],
			"min_inflight", minInflight,
			"skew", skew,
			"pool", len(nodes),
			"loads", strings.Join(loads, ","),
			"affinity_set", affinity != "",
		)
	}
	return selected
}

func (m *Manager) clientFor(id uint64, scope domain.Scope, proxyURL, userAgent, cookies string, sticky bool) (cachedClient, error) {
	clientKind := "browser"
	buildHeaderTimeout := time.Duration(0)
	if scope == domain.ScopeBuild {
		clientKind = "build"
		buildHeaderTimeout = time.Duration(m.buildHeaderTimeout.Load())
		if buildHeaderTimeout <= 0 {
			buildHeaderTimeout = settingsdomain.DefaultBuildResponseHeaderTimeout
		}
		clientKind += "\x00" + strconv.FormatInt(int64(buildHeaderTimeout), 10)
	}
	fingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte(clientKind+"\x00"+proxyURL+"\x00"+userAgent+"\x00"+cookies)))
	cacheScope := scope
	if cacheScope == domain.ScopeWebAsset {
		cacheScope = domain.ScopeWeb
	}
	key := clientCacheKey{nodeID: id, scope: cacheScope, fingerprint: fingerprint}
	m.mu.Lock()
	defer m.mu.Unlock()
	if cached, ok := m.clients[key]; ok {
		return cached, nil
	}
	var value cachedClient
	if scope == domain.ScopeBuild {
		client, err := newBuildClient(proxyURL, buildHeaderTimeout)
		if err != nil {
			return cachedClient{}, err
		}
		value.client = client
	} else {
		client, err := newBrowserClient(proxyURL)
		if err != nil {
			return cachedClient{}, err
		}
		value.client = client
		value.browser = client
	}
	// 固定代理同节点出现新指纹说明配置已更新，旧连接池应淘汰。
	// 账号模板代理的指纹会随 Resin Account 变化，必须并存才能维持各账号的粘性连接池。
	// 直连节点统一使用 ID 0，不同 Provider 的传输必须并存，避免 Build 与 Web 互相重建客户端。
	if id != 0 && !sticky {
		for previousKey, previous := range m.clients {
			if previousKey.nodeID != id {
				continue
			}
			if previous.client != nil {
				previous.client.CloseIdleConnections()
			}
			delete(m.clients, previousKey)
		}
	}
	m.clients[key] = value
	return value, nil
}

func (m *Manager) Feedback(ctx context.Context, nodeID uint64, status int, transportErr error) {
	m.FeedbackForScope(ctx, domain.ScopeWeb, nodeID, status, transportErr)
}

func (m *Manager) FeedbackForScope(ctx context.Context, scope domain.Scope, nodeID uint64, status int, transportErr error) {
	// Build header timeouts are request-path configuration, not egress health.
	if scope == domain.ScopeBuild && neterror.IsResponseHeaderTimeout(transportErr) {
		return
	}
	if nodeID == 0 {
		if transportErr != nil || status >= 500 || (scope != domain.ScopeBuild && status == http.StatusForbidden) {
			m.mu.Lock()
			m.invalidateClientForScopeLocked(0, scope)
			m.mu.Unlock()
		}
		return
	}
	// Account/auth limits are not proxy outcomes.
	if status == http.StatusUnauthorized || status == http.StatusTooManyRequests {
		return
	}
	// Build 403 可能是账号权限、额度、Token 或出口策略，响应体由网关层
	// 分类；仅凭状态码不能把标准 CLI 出口误判为 Web anti-bot。
	if scope == domain.ScopeBuild && status == http.StatusForbidden {
		return
	}

	// Caller cancellation is not a proxy outcome; do not cool healthy nodes.
	if transportErr != nil && (errors.Is(transportErr, context.Canceled) || errors.Is(transportErr, context.DeadlineExceeded)) {
		return
	}

	success := transportErr == nil && status >= 200 && status < 400
	value, err := m.repository.GetEgressNode(ctx, nodeID)
	if err != nil {
		// DB pressure must not skip process-local rates or client invalidation;
		// otherwise failing proxies stay selected and model calls keep 502-ing.
		if success {
			m.recordRequest(nodeID, true)
		} else {
			m.recordRequest(nodeID, false)
			m.mu.Lock()
			m.invalidateClientLocked(nodeID)
			m.mu.Unlock()
		}
		return
	}
	if status == http.StatusForbidden && m.isStickyProxyNode(value) {
		// A 403 on an account-bound Resin lease usually means that account's
		// clearance is stale. Do not cool or invalidate the shared node for
		// unrelated accounts.
		return
	}

	now := time.Now().UTC()
	switch {
	case success:
		// Process-local admin rates (restart clears).
		m.recordRequest(nodeID, true)
		value.Health = min(1, value.Health+0.1)
		value.FailureCount = 0
		value.CooldownUntil = nil
		value.LastError = ""
	case status == http.StatusForbidden:
		m.recordRequest(nodeID, false)
		value.FailureCount++
		value.Health = max(0.05, value.Health*0.7)
		value.CooldownUntil = nil
		value.LastError = "anti-bot rejection"
		m.mu.Lock()
		m.invalidateClientLocked(nodeID)
		m.mu.Unlock()
	default:
		m.recordRequest(nodeID, false)
		value.FailureCount++
		value.Health = max(0.05, value.Health*0.7)
		// Build proxies often flap under HF/Aiven load; cap cooldown so the pool
		// cannot enter a multi-node multi-minute blackout.
		cooldownCap := 10 * time.Minute
		base := 30 * time.Second
		if scope == domain.ScopeBuild {
			cooldownCap = 90 * time.Second
			base = 10 * time.Second
		}
		cooldown := min(cooldownCap, base*time.Duration(1<<min(value.FailureCount-1, 4)))
		until := now.Add(cooldown)
		value.CooldownUntil = &until
		if transportErr != nil {
			value.LastError = "transport error"
		} else {
			value.LastError = fmt.Sprintf("upstream status %d", status)
		}
		m.mu.Lock()
		m.invalidateClientLocked(nodeID)
		m.mu.Unlock()
	}
	if _, err := m.repository.UpdateEgressNode(ctx, value); err == nil {
		m.invalidateNodes(value.Scope)
	}
}

func (m *Manager) isStickyProxyNode(value domain.Node) bool {
	if m == nil || m.cipher == nil || strings.TrimSpace(value.EncryptedProxyURL) == "" {
		return false
	}
	proxyURL, err := m.cipher.Decrypt(value.EncryptedProxyURL)
	return err == nil && strings.Contains(proxyURL, application.ProxyAccountPlaceholder)
}

func (m *Manager) invalidateClientLocked(nodeID uint64) {
	for key, cached := range m.clients {
		if key.nodeID != nodeID {
			continue
		}
		if cached.client != nil {
			cached.client.CloseIdleConnections()
		}
		delete(m.clients, key)
	}
}

func (m *Manager) invalidateClientForScopeLocked(nodeID uint64, scope domain.Scope) {
	if scope == domain.ScopeWebAsset {
		scope = domain.ScopeWeb
	}
	for key, cached := range m.clients {
		if key.nodeID != nodeID || key.scope != scope {
			continue
		}
		if cached.client != nil {
			cached.client.CloseIdleConnections()
		}
		delete(m.clients, key)
	}
}

func BuildSSOCookie(token, cloudflareCookies string) string {
	token = strings.TrimSpace(token)
	if strings.HasPrefix(strings.ToLower(token), "sso=") {
		token = strings.TrimSpace(token[len("sso="):])
	}
	if value, _, found := strings.Cut(token, ";"); found {
		token = strings.TrimSpace(value)
	}
	token = strings.NewReplacer("\r", "", "\n", "", "\x00", "").Replace(token)
	cookies := "sso=" + token + "; sso-rw=" + token
	if sanitized := application.SanitizeCloudflareCookies(cloudflareCookies); sanitized != "" {
		cookies += "; " + sanitized
	}
	return cookies
}

func (m *Manager) RuntimeStats(nodeID uint64) (success, failure int64, inflight int, lastProbeAt *time.Time, lastOK *bool, lastMs int64, lastErr string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	inflight = m.inflight[nodeID]
	st := m.stats[nodeID]
	if st == nil {
		return 0, 0, inflight, nil, nil, 0, ""
	}
	success, failure = st.successCount, st.failureCount
	lastMs, lastErr = st.lastProbeMs, st.lastProbeError
	if st.probed {
		t := st.lastProbeAt
		lastProbeAt = &t
		ok := st.lastProbeOK
		lastOK = &ok
	}
	return success, failure, inflight, lastProbeAt, lastOK, lastMs, lastErr
}

func (m *Manager) recordRequest(nodeID uint64, success bool) {
	if nodeID == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.stats[nodeID]
	if st == nil {
		st = &nodeRuntimeStats{}
		m.stats[nodeID] = st
	}
	if success {
		st.successCount++
	} else {
		st.failureCount++
	}
}

func (m *Manager) recordProbe(nodeID uint64, ok bool, latencyMs int64, errMsg string) {
	if nodeID == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.stats[nodeID]
	if st == nil {
		st = &nodeRuntimeStats{}
		m.stats[nodeID] = st
	}
	st.probed = true
	st.lastProbeAt = time.Now().UTC()
	st.lastProbeOK = ok
	st.lastProbeMs = latencyMs
	st.lastProbeError = errMsg
	if ok {
		st.successCount++
	} else {
		st.failureCount++
	}
}

func (m *Manager) ProbeNode(ctx context.Context, nodeID uint64) (domain.ProbeResult, error) {
	value, err := m.repository.GetEgressNode(ctx, nodeID)
	if err != nil {
		return domain.ProbeResult{}, err
	}
	return m.probeNode(ctx, value)
}

// ProbeAll tests every configured node (optionally filtered by scope).

func (m *Manager) ProbeAll(ctx context.Context, scope domain.Scope) ([]domain.ProbeResult, error) {
	values, err := m.repository.ListEgressNodes(ctx, scope, repository.SortQuery{})
	if err != nil {
		return nil, err
	}
	results := make([]domain.ProbeResult, 0, len(values))
	for _, value := range values {
		if ctx.Err() != nil {
			return results, ctx.Err()
		}
		result, probeErr := m.probeNode(ctx, value)
		if probeErr != nil {
			results = append(results, domain.ProbeResult{
				NodeID: value.ID, Name: value.Name, Scope: value.Scope,
				OK: false, Error: probeErr.Error(), CheckedAt: time.Now().UTC(),
			})
			continue
		}
		results = append(results, result)
	}
	return results, nil
}

func (m *Manager) probeNode(ctx context.Context, value domain.Node) (domain.ProbeResult, error) {
	checkedAt := time.Now().UTC()
	result := domain.ProbeResult{
		NodeID: value.ID, Name: value.Name, Scope: value.Scope, CheckedAt: checkedAt,
	}
	proxyURL, err := m.cipher.Decrypt(value.EncryptedProxyURL)
	if err != nil {
		result.Error = "decrypt proxy failed"
		m.recordProbe(value.ID, false, 0, result.Error)
		return result, nil
	}
	proxyURL, err = application.NormalizeProxyURL(proxyURL)
	if err != nil {
		result.Error = err.Error()
		m.recordProbe(value.ID, false, 0, result.Error)
		return result, nil
	}
	result.ProxyUsed = proxyURL != ""
	timeout := time.Duration(m.buildHeaderTimeout.Load())
	if timeout <= 0 {
		timeout = settingsdomain.DefaultBuildResponseHeaderTimeout
	}
	client, err := newBuildClient(proxyURL, timeout)
	if err != nil {
		result.Error = err.Error()
		m.recordProbe(value.ID, false, 0, result.Error)
		return result, nil
	}
	defer client.CloseIdleConnections()

	probeURL := m.probeURL
	if probeURL == "" {
		probeURL = defaultProbeURL
	}
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	urls := []string{probeURL}
	for _, fallback := range fallbackProbeURLs {
		if fallback != probeURL {
			urls = append(urls, fallback)
		}
	}
	var lastErr string
	var lastStatus int
	var lastLatency int64
	for _, target := range urls {
		req, reqErr := http.NewRequestWithContext(probeCtx, http.MethodGet, target, nil)
		if reqErr != nil {
			lastErr = reqErr.Error()
			continue
		}
		req.Header.Set("User-Agent", "grok2api-egress-probe/1.0")
		req.Header.Set("Accept", "*/*")
		start := time.Now()
		response, doErr := client.Do(req)
		latency := time.Since(start).Milliseconds()
		lastLatency = latency
		result.LatencyMs = latency
		if doErr != nil {
			lastErr = doErr.Error()
			continue
		}
		_ = response.Body.Close()
		lastStatus = response.StatusCode
		result.Status = response.StatusCode
		// Reachability via proxy: 2xx/3xx, and 401/403 mean xAI edge answered (auth/anti-bot).
		if response.StatusCode >= 200 && response.StatusCode < 500 {
			result.OK = true
			result.Error = ""
			m.recordProbe(value.ID, true, latency, "")
			return result, nil
		}
		lastErr = fmt.Sprintf("probe status %d", response.StatusCode)
	}
	result.Status = lastStatus
	result.LatencyMs = lastLatency
	if lastErr == "" {
		lastErr = "probe failed"
	}
	result.Error = lastErr
	m.recordProbe(value.ID, false, lastLatency, result.Error)
	return result, nil
}

