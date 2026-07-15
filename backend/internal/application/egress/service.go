package egress

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	domain "github.com/chenyme/grok2api/backend/internal/domain/egress"
	"github.com/chenyme/grok2api/backend/internal/infra/security"
	"github.com/chenyme/grok2api/backend/internal/repository"
)

var (
	ErrInvalidInput = errors.New("代理节点参数无效")
	ErrInvalidSort  = errors.New("代理节点排序条件无效")
	ErrNotFound     = errors.New("代理节点不存在")
)

const (
	maxProxyURLBytes         = 8192
	maxCloudflareCookieBytes = 16 << 10
)

// RuntimeSource supplies in-memory request/probe stats from the egress manager.
type RuntimeSource interface {
	RuntimeStats(nodeID uint64) (success, failure int64, inflight int, lastProbeAt *time.Time, lastOK *bool, lastMs int64, lastErr string)
	ProbeNode(ctx context.Context, nodeID uint64) (domain.ProbeResult, error)
	ProbeAll(ctx context.Context, scope domain.Scope) ([]domain.ProbeResult, error)
}

type Input struct {
	Name              string
	Scope             domain.Scope
	Enabled           bool
	ProxyURL          *string
	ClearProxyURL     bool
	UserAgent         string
	CloudflareCookies *string
	ClearCookies      bool
}

type Service struct {
	repository repository.EgressRepository
	cipher     *security.Cipher
	runtime    RuntimeSource
	mu         sync.RWMutex
	webUA      string
	consoleUA  string
}

func NewService(repository repository.EgressRepository, cipher *security.Cipher, webUA, consoleUA string) *Service {
	return &Service{repository: repository, cipher: cipher, webUA: strings.TrimSpace(webUA), consoleUA: strings.TrimSpace(consoleUA)}
}

func (s *Service) SetRuntime(runtime RuntimeSource) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runtime = runtime
}

func (s *Service) UpdateDefaults(webUA, consoleUA string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.webUA = strings.TrimSpace(webUA)
	s.consoleUA = strings.TrimSpace(consoleUA)
}

func (s *Service) DefaultUserAgents() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return map[string]string{
		string(domain.ScopeBuild): "", string(domain.ScopeWeb): s.webUA, string(domain.ScopeConsole): s.consoleUA,
		string(domain.ScopeWebAsset): s.webUA,
	}
}

func (s *Service) List(ctx context.Context, scope domain.Scope, sortQuery repository.SortQuery) ([]domain.PublicNode, error) {
	if !repository.IsValidSort(sortQuery, "name", "scope", "proxy", "clearance", "health", "successRate", "failureRate", "requests") {
		return nil, ErrInvalidSort
	}
	// Runtime-only columns are sorted after enrichment; DB sort uses a safe field.
	dbSort := sortQuery
	switch sortQuery.Field {
	case "successRate", "failureRate", "requests":
		dbSort = repository.SortQuery{}
	}
	values, err := s.repository.ListEgressNodes(ctx, scope, dbSort)
	if err != nil {
		return nil, err
	}
	result := make([]domain.PublicNode, 0, len(values))
	for _, value := range values {
		result = append(result, s.publicNode(value))
	}
	sortPublicNodes(result, sortQuery)
	return result, nil
}

func sortPublicNodes(values []domain.PublicNode, sortQuery repository.SortQuery) {
	field := sortQuery.Field
	if field != "successRate" && field != "failureRate" && field != "requests" {
		return
	}
	desc := sortQuery.Direction == repository.SortDescending
	sort.SliceStable(values, func(i, j int) bool {
		var left, right float64
		switch field {
		case "successRate":
			left, right = values[i].SuccessRate, values[j].SuccessRate
		case "failureRate":
			left, right = values[i].FailureRate, values[j].FailureRate
		case "requests":
			left, right = float64(values[i].RequestCount), float64(values[j].RequestCount)
		}
		if left == right {
			return values[i].ID < values[j].ID
		}
		if desc {
			return left > right
		}
		return left < right
	})
}

func (s *Service) Report(ctx context.Context, scope domain.Scope) (domain.Report, error) {
	nodes, err := s.List(ctx, scope, repository.SortQuery{})
	if err != nil {
		return domain.Report{}, err
	}
	report := domain.Report{TotalNodes: len(nodes), Nodes: nodes}
	for _, node := range nodes {
		if node.Enabled {
			report.EnabledNodes++
		}
		if node.ProxyConfigured {
			report.ProxyNodes++
		}
		if node.Enabled && node.Health >= 0.5 && (node.CooldownUntil == nil || time.Now().UTC().After(*node.CooldownUntil)) {
			report.HealthyNodes++
		}
		report.SuccessCount += node.SuccessCount
		failures := node.RequestCount - node.SuccessCount
		if failures < 0 {
			failures = 0
		}
		report.FailureCount += failures
		report.RequestCount += node.RequestCount
	}
	if report.RequestCount > 0 {
		report.SuccessRate = float64(report.SuccessCount) / float64(report.RequestCount)
		report.FailureRate = float64(report.FailureCount) / float64(report.RequestCount)
	}
	return report, nil
}

func (s *Service) Probe(ctx context.Context, id uint64) (domain.ProbeResult, error) {
	s.mu.RLock()
	runtime := s.runtime
	s.mu.RUnlock()
	if runtime == nil {
		return domain.ProbeResult{}, fmt.Errorf("%w: 出口运行时未就绪", ErrInvalidInput)
	}
	if _, err := s.repository.GetEgressNode(ctx, id); errors.Is(err, repository.ErrNotFound) {
		return domain.ProbeResult{}, ErrNotFound
	} else if err != nil {
		return domain.ProbeResult{}, err
	}
	return runtime.ProbeNode(ctx, id)
}

func (s *Service) ProbeAll(ctx context.Context, scope domain.Scope) ([]domain.ProbeResult, error) {
	s.mu.RLock()
	runtime := s.runtime
	s.mu.RUnlock()
	if runtime == nil {
		return nil, fmt.Errorf("%w: 出口运行时未就绪", ErrInvalidInput)
	}
	return runtime.ProbeAll(ctx, scope)
}

func (s *Service) Create(ctx context.Context, input Input) (domain.PublicNode, error) {
	value, err := s.applyInput(domain.Node{}, input, true)
	if err != nil {
		return domain.PublicNode{}, err
	}
	created, err := s.repository.CreateEgressNode(ctx, value)
	return s.publicNode(created), err
}

const maxBatchProxyNodes = 500

// BatchCreateInput creates many proxy nodes from a shared name prefix and URL list.
// Names are generated as "{prefix}#1", "{prefix}#2", … (1-based).
type BatchCreateInput struct {
	NamePrefix        string
	Scope             domain.Scope
	Enabled           bool
	ProxyURLs         []string
	UserAgent         string
	CloudflareCookies *string
}

// BatchCreateResult summarizes bulk import.
type BatchCreateResult struct {
	Created int
	Failed  int
	Skipped int
	Items   []domain.PublicNode
	Errors  []string
}

func (s *Service) CreateBatch(ctx context.Context, input BatchCreateInput) (BatchCreateResult, error) {
	prefix := strings.TrimSpace(input.NamePrefix)
	if prefix == "" {
		prefix = "代理"
	}
	if len(prefix) > 140 {
		return BatchCreateResult{}, fmt.Errorf("%w: 名称前缀不能超过 140 个字符", ErrInvalidInput)
	}
	urls := make([]string, 0, len(input.ProxyURLs))
	seen := make(map[string]struct{}, len(input.ProxyURLs))
	for _, raw := range input.ProxyURLs {
		for _, line := range strings.Split(raw, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			// Allow "name|url" optional override; default uses prefix#n.
			if _, exists := seen[line]; exists {
				continue
			}
			seen[line] = struct{}{}
			urls = append(urls, line)
		}
	}
	if len(urls) == 0 {
		return BatchCreateResult{}, fmt.Errorf("%w: 至少填写一个代理地址", ErrInvalidInput)
	}
	if len(urls) > maxBatchProxyNodes {
		return BatchCreateResult{}, fmt.Errorf("%w: 单次最多导入 %d 个代理", ErrInvalidInput, maxBatchProxyNodes)
	}
	result := BatchCreateResult{Items: make([]domain.PublicNode, 0, len(urls))}
	for index, proxyURL := range urls {
		name := fmt.Sprintf("%s#%d", prefix, index+1)
		urlCopy := proxyURL
		node, err := s.Create(ctx, Input{
			Name: name, Scope: input.Scope, Enabled: input.Enabled,
			ProxyURL: &urlCopy, UserAgent: input.UserAgent, CloudflareCookies: input.CloudflareCookies,
		})
		if err != nil {
			result.Failed++
			if len(result.Errors) < 20 {
				result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", name, err))
			}
			continue
		}
		result.Created++
		result.Items = append(result.Items, node)
	}
	if result.Created == 0 && result.Failed > 0 {
		msg := "批量导入全部失败"
		if len(result.Errors) > 0 {
			msg = result.Errors[0]
		}
		return result, fmt.Errorf("%w: %s", ErrInvalidInput, msg)
	}
	return result, nil
}

func (s *Service) Update(ctx context.Context, id uint64, input Input) (domain.PublicNode, error) {
	value, err := s.repository.GetEgressNode(ctx, id)
	if errors.Is(err, repository.ErrNotFound) {
		return domain.PublicNode{}, ErrNotFound
	}
	if err != nil {
		return domain.PublicNode{}, err
	}
	value, err = s.applyInput(value, input, false)
	if err != nil {
		return domain.PublicNode{}, err
	}
	updated, err := s.repository.UpdateEgressNode(ctx, value)
	return s.publicNode(updated), err
}

func (s *Service) Delete(ctx context.Context, id uint64) error {
	err := s.repository.DeleteEgressNode(ctx, id)
	if errors.Is(err, repository.ErrNotFound) {
		return ErrNotFound
	}
	return err
}

func (s *Service) applyInput(value domain.Node, input Input, create bool) (domain.Node, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" || len(name) > 160 {
		return domain.Node{}, fmt.Errorf("%w: 名称必须在 1 到 160 个字符之间", ErrInvalidInput)
	}
	if input.Scope != domain.ScopeBuild && input.Scope != domain.ScopeWeb && input.Scope != domain.ScopeConsole && input.Scope != domain.ScopeWebAsset {
		return domain.Node{}, fmt.Errorf("%w: scope 必须是 grok_build、grok_web、grok_console 或 grok_web_asset", ErrInvalidInput)
	}
	value.Name, value.Scope, value.Enabled = name, input.Scope, input.Enabled
	if input.Scope == domain.ScopeBuild {
		// Build 请求始终沿用 Provider 生成的 CLI User-Agent，出口节点不得覆盖协议身份。
		value.UserAgent = ""
	} else {
		value.UserAgent = strings.TrimSpace(input.UserAgent)
	}
	if input.Scope != domain.ScopeBuild && value.UserAgent == "" {
		s.mu.RLock()
		value.UserAgent = s.defaultUserAgent(input.Scope)
		s.mu.RUnlock()
	}
	if len(value.UserAgent) > 512 {
		return domain.Node{}, fmt.Errorf("%w: User-Agent 过长", ErrInvalidInput)
	}
	if input.ClearProxyURL {
		value.EncryptedProxyURL = ""
	} else if input.ProxyURL != nil {
		normalized, err := NormalizeProxyURL(*input.ProxyURL)
		if err != nil {
			return domain.Node{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
		}
		if normalized != "" || create {
			value.EncryptedProxyURL, err = s.cipher.Encrypt(normalized)
			if err != nil {
				return domain.Node{}, err
			}
		}
	}
	if input.Scope == domain.ScopeBuild {
		value.EncryptedCloudflareCookie = ""
	} else if input.ClearCookies {
		value.EncryptedCloudflareCookie = ""
	} else if input.CloudflareCookies != nil {
		if len(*input.CloudflareCookies) > maxCloudflareCookieBytes {
			return domain.Node{}, fmt.Errorf("%w: Cloudflare Cookie 不能超过 16 KiB", ErrInvalidInput)
		}
		cookies := SanitizeCloudflareCookies(*input.CloudflareCookies)
		if cookies != "" || create {
			var err error
			value.EncryptedCloudflareCookie, err = s.cipher.Encrypt(cookies)
			if err != nil {
				return domain.Node{}, err
			}
		}
	}
	if create {
		value.Health = 1
	}
	return value, nil
}

func (s *Service) defaultUserAgent(scope domain.Scope) string {
	if scope == domain.ScopeConsole {
		return s.consoleUA
	}
	return s.webUA
}

func (s *Service) publicNode(value domain.Node) domain.PublicNode {
	userAgent := value.UserAgent
	if value.Scope == domain.ScopeBuild {
		userAgent = ""
	}
	proxyConfigured := value.EncryptedProxyURL != ""
	protocol := ""
	if proxyConfigured && s != nil && s.cipher != nil {
		if plain, err := s.cipher.Decrypt(value.EncryptedProxyURL); err == nil {
			protocol = ProxyProtocolLabel(plain)
		}
	}
	node := domain.PublicNode{
		ID: value.ID, Name: value.Name, Scope: value.Scope, Enabled: value.Enabled,
		ProxyConfigured: proxyConfigured, ProxyProtocol: protocol, UserAgent: userAgent, CookieConfigured: value.EncryptedCloudflareCookie != "",
		Health: value.Health, FailureCount: value.FailureCount, CooldownUntil: value.CooldownUntil, LastError: value.LastError,
		CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
	}
	s.mu.RLock()
	runtime := s.runtime
	s.mu.RUnlock()
	if runtime != nil {
		success, failure, inflight, lastProbeAt, lastOK, lastMs, lastErr := runtime.RuntimeStats(value.ID)
		node.SuccessCount = success
		node.RequestCount = success + failure
		node.Inflight = inflight
		node.LastProbeAt = lastProbeAt
		node.LastProbeOK = lastOK
		node.LastProbeMs = lastMs
		node.LastProbeError = lastErr
		if node.RequestCount > 0 {
			node.SuccessRate = float64(success) / float64(node.RequestCount)
			node.FailureRate = float64(failure) / float64(node.RequestCount)
		}
	}
	return node
}

// publicNode keeps tests that call the helper without a Service instance working.
func publicNode(value domain.Node) domain.PublicNode {
	return (&Service{}).publicNode(value)
}

// ProxyProtocolLabel returns a short safe protocol name for admin UI (no host/user/password).
func ProxyProtocolLabel(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "{") {
		return "sing-box"
	}
	lower := strings.ToLower(value)
	for _, prefix := range []struct {
		p string
		n string
	}{
		{"vmess://", "vmess"},
		{"ss://", "ss"},
		{"ssr://", "ssr"},
		{"vless://", "vless"},
		{"trojan://", "trojan"},
		{"hysteria2://", "hysteria2"},
		{"hy2://", "hysteria2"},
		{"hysteria://", "hysteria"},
		{"hy://", "hysteria"},
		{"tuic://", "tuic"},
		{"anytls://", "anytls"},
		{"wireguard://", "wireguard"},
		{"wg://", "wireguard"},
		{"shadowtls://", "shadowtls"},
		{"ssh://", "ssh"},
		{"socks5h://", "socks5h"},
		{"socks5://", "socks5"},
		{"socks4a://", "socks4a"},
		{"socks4://", "socks4"},
		{"https://", "https"},
		{"http://", "http"},
	} {
		if strings.HasPrefix(lower, prefix.p) {
			return prefix.n
		}
	}
	if parsed, err := url.Parse(value); err == nil {
		scheme := strings.ToLower(strings.TrimSpace(parsed.Scheme))
		if scheme != "" {
			return scheme
		}
	}
	return "proxy"
}

func NormalizeProxyURL(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if len(value) > maxProxyURLBytes || strings.IndexFunc(value, func(character rune) bool { return character < 0x20 || character == 0x7f }) >= 0 {
		return "", errors.New("代理地址过长或包含控制字符")
	}
	// sing-box outbound JSON (full options or single outbound object)
	if strings.HasPrefix(value, "{") {
		if !json.Valid([]byte(value)) {
			return "", errors.New("代理 JSON 无效")
		}
		return value, nil
	}
	lower := strings.ToLower(value)
	// Base64-style share links where url.Parse host may be empty.
	for _, prefix := range []string{"vmess://", "ss://", "ssr://"} {
		if strings.HasPrefix(lower, prefix) {
			if strings.TrimSpace(value[len(prefix):]) == "" {
				return "", errors.New("分享链接无效")
			}
			return value, nil
		}
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return "", errors.New("代理地址格式无效")
	}
	scheme := strings.ToLower(parsed.Scheme)
	switch scheme {
	case "http", "https", "socks4", "socks4a", "socks5", "socks5h":
		if parsed.Host == "" || parsed.Hostname() == "" {
			return "", errors.New("代理地址格式无效")
		}
		if parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
			return "", errors.New("代理地址不能包含路径、查询参数或片段")
		}
		return parsed.String(), nil
	case "vless", "trojan", "hysteria", "hysteria2", "hy", "hy2", "tuic", "anytls", "ssh", "wireguard", "wg", "shadowtls":
		if parsed.Hostname() == "" {
			return "", errors.New("代理地址格式无效")
		}
		// Keep original string so share-link query parameters survive.
		return value, nil
	default:
		return "", errors.New("代理地址协议必须是 HTTP/SOCKS/SS/VMess/VLESS/Trojan/Hysteria/TUIC/AnyTLS/SSH/WireGuard 或 sing-box outbound JSON")
	}
}

func SanitizeCloudflareCookies(value string) string {
	allowed := make([]string, 0, 4)
	seen := make(map[string]struct{})
	for part := range strings.SplitSeq(value, ";") {
		name, cookieValue, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		lower := strings.ToLower(name)
		if lower != "cf_clearance" && lower != "__cf_bm" && lower != "_cfuvid" && !strings.HasPrefix(lower, "cf_chl_") {
			continue
		}
		if _, exists := seen[lower]; exists {
			continue
		}
		cookieValue = strings.TrimSpace(cookieValue)
		if cookieValue == "" || len(cookieValue) > maxCloudflareCookieBytes || strings.IndexFunc(cookieValue, func(character rune) bool { return character < 0x20 || character == 0x7f }) >= 0 {
			continue
		}
		seen[lower] = struct{}{}
		allowed = append(allowed, lower+"="+cookieValue)
	}
	return strings.Join(allowed, "; ")
}
