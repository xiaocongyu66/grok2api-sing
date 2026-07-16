package egress

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	application "github.com/chenyme/grok2api/backend/internal/application/egress"
	domain "github.com/chenyme/grok2api/backend/internal/domain/egress"
	"github.com/chenyme/grok2api/backend/internal/infra/security"
	"github.com/chenyme/grok2api/backend/internal/repository"
	"golang.org/x/sync/singleflight"
)

const DefaultUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36"
const nodeSnapshotTTL = time.Second

type Lease struct {
	NodeID    uint64
	NodeName  string
	Scope     domain.Scope
	ProxyURL  string
	UserAgent string
	CFCookies string
	client    requestClient
	browser   *browserClient
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
	return l.client.Do(request)
}
func (l *Lease) Release() {
	if l != nil && l.release != nil {
		l.release()
		l.release = nil
	}
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

type Manager struct {
	repository repository.EgressRepository
	cipher     *security.Cipher
	mu         sync.Mutex
	clients    map[uint64]cachedClient
	inflight   map[uint64]int
	stats      map[uint64]*nodeRuntimeStats
	nodes      map[domain.Scope]cachedNodeSnapshot
	nodeLoads  singleflight.Group
	probeURL   string
}

type cachedClient struct {
	fingerprint string
	client      requestClient
	browser     *browserClient
}

type cachedNodeSnapshot struct {
	values    []domain.Node
	expiresAt time.Time
}

const defaultProbeURL = "https://www.gstatic.com/generate_204"

func NewManager(repository repository.EgressRepository, cipher *security.Cipher) *Manager {
	return &Manager{
		repository: repository,
		cipher:     cipher,
		clients:    make(map[uint64]cachedClient),
		inflight:   make(map[uint64]int),
		stats:      make(map[uint64]*nodeRuntimeStats),
		nodes:      make(map[domain.Scope]cachedNodeSnapshot),
		probeURL:   defaultProbeURL,
	}
}

// RuntimeStats returns in-memory success/failure counters and last probe info for a node.
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

func (m *Manager) Acquire(ctx context.Context, scope domain.Scope, affinity string) (*Lease, error) {
	lease, _, err := m.acquire(ctx, scope, affinity, true)
	return lease, err
}

func (m *Manager) AcquireIfConfigured(ctx context.Context, scope domain.Scope, affinity string) (*Lease, bool, error) {
	return m.acquire(ctx, scope, affinity, false)
}

func (m *Manager) acquire(ctx context.Context, scope domain.Scope, affinity string, allowDirect bool) (*Lease, bool, error) {
	now := time.Now().UTC()
	configured := false
	var available []domain.Node
	for _, candidateScope := range fallbackScopes(scope) {
		nodes, err := m.listNodes(ctx, candidateScope, now)
		if err != nil {
			return nil, false, err
		}
		configured = configured || len(nodes) > 0
		candidateAvailable := make([]domain.Node, 0, len(nodes))
		for _, node := range nodes {
			if node.Enabled && (node.CooldownUntil == nil || !now.Before(*node.CooldownUntil)) {
				candidateAvailable = append(candidateAvailable, node)
			}
		}
		if len(candidateAvailable) > 0 {
			available = candidateAvailable
			break
		}
	}
	if len(available) == 0 {
		if configured {
			return nil, false, fmt.Errorf("当前没有可用的 %s 出口节点", scope)
		}
		if !allowDirect {
			recordSelection(ctx, Selection{NodeName: "direct", Scope: scope})
			return nil, false, nil
		}
		available = []domain.Node{{ID: 0, Name: "direct", Scope: scope, Enabled: true, Health: 1}}
	}
	sort.SliceStable(available, func(i, j int) bool { return available[i].ID < available[j].ID })
	selected := m.selectNode(available, affinity)
	proxyURL, err := m.cipher.Decrypt(selected.EncryptedProxyURL)
	if err != nil {
		return nil, false, err
	}
	proxyURL, err = application.NormalizeProxyURL(proxyURL)
	if err != nil {
		return nil, false, err
	}
	cookies := ""
	if scope != domain.ScopeBuild {
		cookies, err = m.cipher.Decrypt(selected.EncryptedCloudflareCookie)
		if err != nil {
			return nil, false, err
		}
		cookies = application.SanitizeCloudflareCookies(cookies)
	}
	userAgent := ""
	if scope != domain.ScopeBuild {
		// Fixed UA, empty→default, or "random"→pick from pool for this lease.
		userAgent = ResolveBrowserUserAgent(selected.UserAgent)
	}
	client, err := m.clientFor(selected.ID, scope, proxyURL, userAgent, cookies)
	if err != nil {
		return nil, false, err
	}
	m.mu.Lock()
	m.inflight[selected.ID]++
	m.mu.Unlock()
	recordSelection(ctx, Selection{NodeID: selected.ID, NodeName: selected.Name, Scope: scope, Proxied: proxyURL != ""})
	var once sync.Once
	return &Lease{NodeID: selected.ID, NodeName: selected.Name, Scope: scope, ProxyURL: proxyURL, UserAgent: userAgent, CFCookies: cookies, client: client.client, browser: client.browser, release: func() {
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
	return []domain.Scope{scope}
}

// selectNode spreads concurrent leases across proxies by least in-flight load.
// Affinity is only a soft preference among equally loaded nodes so that e.g.
// 64 concurrent requests with 6 proxies land ~10-11 each instead of pinning all
// traffic to a single hashed node.
func (m *Manager) selectNode(nodes []domain.Node, affinity string) domain.Node {
	if len(nodes) == 1 {
		return nodes[0]
	}
	preferredID := uint64(0)
	if affinity != "" {
		digest := sha256.Sum256([]byte(affinity))
		preferredID = nodes[int(binary.BigEndian.Uint64(digest[:8])%uint64(len(nodes)))].ID
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	best := nodes[0]
	bestLoad := m.inflight[best.ID]
	for _, node := range nodes[1:] {
		load := m.inflight[node.ID]
		switch {
		case load < bestLoad:
			best, bestLoad = node, load
		case load > bestLoad:
			continue
		case preferredID != 0 && node.ID == preferredID:
			best = node
		case preferredID != 0 && best.ID == preferredID:
			continue
		case node.Health > best.Health || (node.Health == best.Health && node.ID < best.ID):
			best = node
		}
	}
	return best
}

func (m *Manager) clientFor(id uint64, scope domain.Scope, proxyURL, userAgent, cookies string) (cachedClient, error) {
	clientKind := "browser"
	if scope == domain.ScopeBuild {
		clientKind = "build"
	}
	// Browser UA is applied per-request by callers from Lease.UserAgent; exclude it
	// from the TLS client fingerprint so random-UA mode does not thrash the cache.
	fingerprintMaterial := clientKind + "\x00" + proxyURL + "\x00" + cookies
	if scope == domain.ScopeBuild {
		fingerprintMaterial = clientKind + "\x00" + proxyURL + "\x00" + userAgent + "\x00" + cookies
	}
	fingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte(fingerprintMaterial)))
	m.mu.Lock()
	defer m.mu.Unlock()
	if cached, ok := m.clients[id]; ok && cached.fingerprint == fingerprint {
		return cached, nil
	}
	var value cachedClient
	value.fingerprint = fingerprint
	if scope == domain.ScopeBuild {
		client, err := newBuildClient(proxyURL)
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
	if previous, exists := m.clients[id]; exists && previous.client != nil {
		previous.client.CloseIdleConnections()
	}
	m.clients[id] = value
	return value, nil
}

func (m *Manager) Feedback(ctx context.Context, nodeID uint64, status int, transportErr error) {
	m.FeedbackForScope(ctx, domain.ScopeWeb, nodeID, status, transportErr)
}

func (m *Manager) FeedbackForScope(ctx context.Context, scope domain.Scope, nodeID uint64, status int, transportErr error) {
	if nodeID == 0 {
		if transportErr != nil || status >= 500 || (scope != domain.ScopeBuild && status == http.StatusForbidden) {
			m.mu.Lock()
			m.invalidateClientLocked(0)
			m.mu.Unlock()
		}
		return
	}
	value, err := m.repository.GetEgressNode(ctx, nodeID)
	if err != nil {
		return
	}
	now := time.Now().UTC()
	switch {
	case transportErr == nil && status >= 200 && status < 400:
		m.recordRequest(nodeID, true)
		value.Health = min(1, value.Health+0.1)
		value.FailureCount = 0
		value.CooldownUntil = nil
		value.LastError = ""
	case status == http.StatusUnauthorized || status == http.StatusTooManyRequests:
		// Account-level errors: do not punish the proxy node stats.
		return
	case scope == domain.ScopeBuild && status == http.StatusForbidden:
		// Build 403 可能是账号权限、额度、Token 或出口策略，响应体由网关层
		// 分类；仅凭状态码不能把标准 CLI 出口误判为 Web anti-bot。
		return
	case status == http.StatusForbidden:
		m.recordRequest(nodeID, false)
		value.FailureCount++
		value.Health = max(0.05, value.Health*0.7)
		value.CooldownUntil = nil
		value.LastError = "疑似反爬拒绝（403）"
		m.mu.Lock()
		m.invalidateClientLocked(nodeID)
		m.mu.Unlock()
	default:
		m.recordRequest(nodeID, false)
		value.FailureCount++
		value.Health = max(0.05, value.Health*0.7)
		cooldown := min(10*time.Minute, 30*time.Second*time.Duration(1<<min(value.FailureCount-1, 4)))
		until := now.Add(cooldown)
		value.CooldownUntil = &until
		if transportErr != nil {
			value.LastError = "传输/连通失败"
		} else {
			value.LastError = fmt.Sprintf("上游状态码 %d", status)
		}
		m.mu.Lock()
		m.invalidateClientLocked(nodeID)
		m.mu.Unlock()
	}
	if _, err := m.repository.UpdateEgressNode(ctx, value); err == nil {
		m.invalidateNodes(value.Scope)
	}
}

// ProbeNode dials through the node's configured proxy and hits a lightweight
// connectivity URL. It records latency into in-memory stats for the admin report.
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
	client, err := newBuildClient(proxyURL)
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
	probeCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(probeCtx, http.MethodGet, probeURL, nil)
	if err != nil {
		result.Error = err.Error()
		m.recordProbe(value.ID, false, 0, result.Error)
		return result, nil
	}
	start := time.Now()
	response, err := client.Do(request)
	latency := time.Since(start).Milliseconds()
	result.LatencyMs = latency
	if err != nil {
		result.Error = err.Error()
		m.recordProbe(value.ID, false, latency, result.Error)
		return result, nil
	}
	_ = response.Body.Close()
	result.Status = response.StatusCode
	// 204/200/3xx all mean the proxy path works.
	if response.StatusCode >= 200 && response.StatusCode < 400 {
		result.OK = true
		m.recordProbe(value.ID, true, latency, "")
		return result, nil
	}
	result.Error = fmt.Sprintf("probe status %d", response.StatusCode)
	m.recordProbe(value.ID, false, latency, result.Error)
	return result, nil
}

func (m *Manager) invalidateClientLocked(nodeID uint64) {
	if cached, exists := m.clients[nodeID]; exists && cached.client != nil {
		cached.client.CloseIdleConnections()
	}
	delete(m.clients, nodeID)
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
