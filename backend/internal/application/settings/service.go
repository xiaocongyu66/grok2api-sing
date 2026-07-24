package settings

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	settingsdomain "github.com/chenyme/grok2api/backend/internal/domain/settings"
	"github.com/chenyme/grok2api/backend/internal/infra/config"
	"github.com/chenyme/grok2api/backend/internal/repository"
)

var (
	ErrInvalidInput = errors.New("运行设置参数无效")
	ErrConflict     = errors.New("运行设置已被其他会话更新")
)

// ProviderBuildConfig 是管理接口使用的 Provider 可编辑输入。
type ProviderBuildConfig struct {
	BaseURL               string
	ClientVersion         string
	ClientIdentifier      string
	TokenAuth             string
	UserAgent             string
	ResponseHeaderTimeout string
}

// ProviderBuildRecommendation 表示当前网关已完成兼容回归的 Grok Build 协议基线。
type ProviderBuildRecommendation struct {
	ClientVersion string
	UserAgent     string
}

type ProviderWebConfig struct {
	BaseURL                     string
	StatsigMode                 string
	StatsigManualValue          string
	StatsigManualConfigured     bool
	StatsigSignerURL            string
	QuotaTimeout                string
	ChatTimeout                 string
	ImageTimeout                string
	VideoTimeout                string
	MediaConcurrency            int
	AllowNSFW                   bool
	RecoveryBackoffBase         string
	RecoveryBackoffMax          string
	FlareSolverrEnabled         bool
	FlareSolverrURL             string
	FlareSolverrTargetURL       string
	FlareSolverrTimeout         string
	FlareSolverrRefreshInterval string
}

type ProviderConsoleConfig struct {
	BaseURL     string
	UserAgent   string
	ChatTimeout string
}

// BatchConfig 是管理接口使用的批量任务并发输入。
type BatchConfig struct {
	ImportConcurrency     int
	ConversionConcurrency int
	SyncConcurrency       int
	RefreshConcurrency    int
	RandomDelay           string
	DBBuffer              DBBufferConfig
}

// DBBufferConfig for admin input.
type DBBufferConfig struct {
	Enabled bool   `json:"enabled"`
	Driver  string `json:"driver"`
	Path    string `json:"path"`
}

type MediaConfig struct {
	MaxImageBytes           int64
	MaxTotalBytes           int64
	CleanupThresholdPercent int
	CleanupInterval         string
}

// RoutingConfig 是管理接口使用的路由可编辑输入。
type RoutingConfig struct {
	StickyTTL                  string
	CooldownBase               string
	CooldownMax                string
	CapacityWait               string
	MaxAttempts                int
	RetryStatusCodes           []int
	RetryServerErrors          bool
	DeprioritizeFailedAccounts bool
}

// PromptCacheAffinityConfig is the admin-editable prompt-cache affinity policy.
type PromptCacheAffinityConfig struct {
	Enabled     bool
	Fingerprint bool
	Expire      bool
	TTL         string
}

// AuditConfig 是管理接口使用的审计可编辑输入。
type AuditConfig struct {
	BufferSize    int
	BatchSize     int
	FlushInterval string
}

// ClientKeyDefaultsConfig 是管理接口使用的密钥默认限制输入。
type ClientKeyDefaultsConfig struct {
	RPMLimit      int
	MaxConcurrent int
}

// ProactiveUpstreamSyncConfig is the admin-editable proactive xAI sync policy.
type ProactiveUpstreamSyncConfig struct {
	Billing                   bool
	WebQuota                  bool
	ModelCatalogCatchup       bool
	AllowManualBillingRefresh bool
	AllowManualQuotaRefresh   bool
}

// EditableConfig 聚合管理端允许修改的运行参数。
type ServerConfig struct {
	MaxConcurrentRequests int
}

type FrontendConfig struct {
	PublicAPIBaseURL string
}

type EditableConfig struct {
	Server                ServerConfig
	ProviderBuild         ProviderBuildConfig
	ProviderWeb           ProviderWebConfig
	ProviderConsole       ProviderConsoleConfig
	ProactiveUpstreamSync ProactiveUpstreamSyncConfig
	Batch                 BatchConfig
	Media                 MediaConfig
	Frontend              FrontendConfig
	Routing               RoutingConfig
	PromptCacheAffinity   PromptCacheAffinityConfig
	Audit                 AuditConfig
	ClientKeyDefaults     ClientKeyDefaultsConfig
}

// Snapshot 表示当前运行设置和需要重启才能生效的字段。
type Snapshot struct {
	Config                   EditableConfig
	RecommendedProviderBuild ProviderBuildRecommendation
	UpdatedAt                time.Time
	Revision                 uint64
	RestartRequired          []string
}

// Service 管理允许在线修改的配置，并向后台任务广播配置变更。
type Service struct {
	mu                     sync.RWMutex
	updateMu               sync.Mutex
	cfg                    config.Config
	updatedAt              time.Time
	revision               uint64
	activeBufferSize       int
	activeMediaConcurrency int
	repository             repository.RuntimeSettingsRepository
	notify                 func(context.Context)
	apply                  func(config.Config)
}

func NewService(cfg config.Config, updatedAt time.Time, revision uint64, repository repository.RuntimeSettingsRepository, notify func(context.Context), apply func(config.Config)) *Service {
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	return &Service{cfg: cfg, updatedAt: updatedAt, revision: revision, activeBufferSize: cfg.Audit.BufferSize, activeMediaConcurrency: cfg.Provider.Web.MediaConcurrency, repository: repository, notify: notify, apply: apply}
}

// LoadPersisted 将数据库运行设置覆盖到代码默认配置，并执行完整边界校验。
func LoadPersisted(ctx context.Context, base config.Config, repository repository.RuntimeSettingsRepository) (config.Config, time.Time, uint64, error) {
	value, updatedAt, revision, found, err := repository.Get(ctx)
	if err != nil {
		return config.Config{}, time.Time{}, 0, err
	}
	if !found {
		return base, time.Time{}, 0, nil
	}
	// 持久化层使用强类型时长，避免数据库格式受 HTTP DTO 字符串影响。
	loaded := applyDomainConfig(base, value)
	// Migrate legacy third-party Statsig defaults before hard validation so upgrades boot.
	config.NormalizeRoutingRetry(&loaded)
	config.NormalizeLegacyStatsig(&loaded)
	if err := loaded.Validate(); err != nil {
		return config.Config{}, time.Time{}, 0, fmt.Errorf("校验运行设置: %w", err)
	}
	return loaded, updatedAt, revision, nil
}

// Get 返回当前生效的可编辑设置快照。
func (s *Service) PublicAPIBaseURL() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.Frontend.EffectivePublicAPIBaseURL()
}

func (s *Service) Get() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshotLocked()
}

// Update 校验并持久化运行设置，再原子替换进程内配置。
func (s *Service) Update(ctx context.Context, expectedRevision uint64, input EditableConfig) (Snapshot, error) {
	s.updateMu.Lock()
	defer s.updateMu.Unlock()

	s.mu.RLock()
	current := s.cfg
	currentRevision := s.revision
	s.mu.RUnlock()
	if expectedRevision != currentRevision {
		return Snapshot{}, ErrConflict
	}
	next, err := mergeEditable(current, input)
	if err != nil {
		return Snapshot{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	// Validate before persist so misconfigured fields return a clear 400 message
	// (e.g. batch.dbBuffer) instead of a generic "保存运行设置失败".
	if err := next.Validate(); err != nil {
		return Snapshot{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	updatedAt, revision, err := s.repository.Save(ctx, toDomainConfig(next), currentRevision)
	if err != nil {
		if errors.Is(err, repository.ErrConflict) {
			return Snapshot{}, ErrConflict
		}
		return Snapshot{}, err
	}

	s.mu.Lock()
	s.cfg = next
	s.updatedAt = updatedAt
	s.revision = revision
	result := s.snapshotLocked()
	apply := s.apply
	s.mu.Unlock()

	if apply != nil {
		apply(next)
	}
	if s.notify != nil {
		s.notify(ctx)
	}
	return result, nil
}

// ReloadPersisted 在收到其他实例的变更通知后，从主数据库重载并应用运行设置。
func (s *Service) ReloadPersisted(ctx context.Context) error {
	s.updateMu.Lock()
	defer s.updateMu.Unlock()
	value, updatedAt, revision, found, err := s.repository.Get(ctx)
	if err != nil || !found {
		return err
	}
	s.mu.RLock()
	current := s.cfg
	currentRevision := s.revision
	s.mu.RUnlock()
	if revision <= currentRevision {
		return nil
	}
	next := applyDomainConfig(current, value)
	if err := next.Validate(); err != nil {
		return fmt.Errorf("校验重载运行设置: %w", err)
	}
	s.mu.Lock()
	s.cfg = next
	s.updatedAt = updatedAt
	s.revision = revision
	apply := s.apply
	s.mu.Unlock()
	if apply != nil {
		apply(next)
	}
	return nil
}

func applyDomainConfig(base config.Config, value settingsdomain.Config) config.Config {
	// 旧版运行设置没有 Server 字段，反序列化后为零；升级时沿用当前配置默认值。
	if value.Server.MaxConcurrentRequests > 0 {
		base.Server.MaxConcurrentRequests = value.Server.MaxConcurrentRequests
	}
	base.Frontend.PublicAPIBaseURLOverride = strings.TrimSpace(value.Frontend.PublicAPIBaseURL)
	capacityWait := value.Routing.CapacityWait
	if capacityWait <= 0 {
		capacityWait = base.Routing.CapacityWait.Value()
	}
	base.Provider.Build = config.BuildProviderConfig{
		BaseURL: value.ProviderBuild.BaseURL, ClientVersion: value.ProviderBuild.ClientVersion,
		ClientIdentifier: value.ProviderBuild.ClientIdentifier, TokenAuth: value.ProviderBuild.TokenAuth,
		UserAgent: value.ProviderBuild.UserAgent,
		ResponseHeaderTimeout: config.Duration(value.ProviderBuild.ResponseHeaderTimeout),
	}
	if value.ProviderBuild.ResponseHeaderTimeout <= 0 {
		base.Provider.Build.ResponseHeaderTimeout = config.Duration(settingsdomain.DefaultBuildResponseHeaderTimeout)
	}
	base.Provider.Web = config.WebProviderConfig{
		BaseURL: value.ProviderWeb.BaseURL, QuotaTimeout: config.Duration(value.ProviderWeb.QuotaTimeout),
		StatsigMode: value.ProviderWeb.StatsigMode, StatsigManualValue: value.ProviderWeb.StatsigManualValue, StatsigSignerURL: value.ProviderWeb.StatsigSignerURL,
		ChatTimeout: config.Duration(value.ProviderWeb.ChatTimeout), ImageTimeout: config.Duration(value.ProviderWeb.ImageTimeout),
		VideoTimeout:     config.Duration(value.ProviderWeb.VideoTimeout),
		MediaConcurrency: value.ProviderWeb.MediaConcurrency, AllowNSFW: value.ProviderWeb.AllowNSFW,
		RecoveryBackoffBase: config.Duration(value.ProviderWeb.RecoveryBackoffBase), RecoveryBackoffMax: config.Duration(value.ProviderWeb.RecoveryBackoffMax),
		FlareSolverrEnabled: value.ProviderWeb.FlareSolverrEnabled, FlareSolverrURL: value.ProviderWeb.FlareSolverrURL,
		FlareSolverrTargetURL: value.ProviderWeb.FlareSolverrTargetURL,
		FlareSolverrTimeout:   config.Duration(value.ProviderWeb.FlareSolverrTimeout), FlareSolverrRefreshInterval: config.Duration(value.ProviderWeb.FlareSolverrRefreshInterval),
	}
	// Console 是后续版本新增的完整配置段；旧 JSON 整段缺失时沿用代码默认值。
	if value.ProviderConsole != (settingsdomain.ProviderConsoleConfig{}) {
		base.Provider.Console = config.ConsoleProviderConfig{
			BaseURL: value.ProviderConsole.BaseURL, UserAgent: value.ProviderConsole.UserAgent,
			ChatTimeout: config.Duration(value.ProviderConsole.ChatTimeout),
		}
	}
	randomDelay := time.Duration(-1)
	if value.Batch.RandomDelay != nil {
		randomDelay = *value.Batch.RandomDelay
	}
	base.Batch = config.BatchConfig{
		ImportConcurrency: value.Batch.ImportConcurrency, ConversionConcurrency: value.Batch.ConversionConcurrency,
		SyncConcurrency: value.Batch.SyncConcurrency, RefreshConcurrency: value.Batch.RefreshConcurrency,
		RandomDelay: config.Duration(randomDelay),
		DBBuffer: normalizeDBBuffer(config.DBBufferConfig{
			Enabled: value.Batch.DBBuffer.Enabled,
			Driver:  value.Batch.DBBuffer.Driver,
			Path:    value.Batch.DBBuffer.Path,
		}),
	}
	base.Media.MaxImageBytes = value.Media.MaxImageBytes
	base.Media.MaxTotalBytes = value.Media.MaxTotalBytes
	base.Media.CleanupThresholdPercent = value.Media.CleanupThresholdPercent
	base.Media.CleanupInterval = config.Duration(value.Media.CleanupInterval)
	// Reasoning replay is process/YAML config (not yet in admin editable domain).
	// Preserve base defaults when applying persisted runtime settings.
	replayEnabled := base.Routing.ReasoningReplayEnabled
	replayTTL := base.Routing.ReasoningReplayTTL
	replayMax := base.Routing.ReasoningReplayMaxEntries
	base.Routing = config.RoutingConfig{
		StickyTTL: config.Duration(value.Routing.StickyTTL), CooldownBase: config.Duration(value.Routing.CooldownBase),
		CooldownMax: config.Duration(value.Routing.CooldownMax), CapacityWait: config.Duration(capacityWait), MaxAttempts: value.Routing.MaxAttempts,
		RetryStatusCodes: append([]int(nil), value.Routing.RetryStatusCodes...), RetryServerErrors: value.Routing.RetryServerErrors,
		DeprioritizeFailedAccounts: value.Routing.DeprioritizeFailedAccounts,
		ReasoningReplayEnabled:     replayEnabled, ReasoningReplayTTL: replayTTL, ReasoningReplayMaxEntries: replayMax,
	}
	if base.Routing.ReasoningReplayTTL.Value() <= 0 {
		base.Routing.ReasoningReplayTTL = config.Duration(time.Hour)
	}
	if base.Routing.ReasoningReplayMaxEntries < 100 {
		base.Routing.ReasoningReplayMaxEntries = 10240
	}
	// Build fallback URL is also process/YAML (not admin domain).
	if strings.TrimSpace(base.Provider.Build.FallbackBaseURL) == "" {
		base.Provider.Build.FallbackBaseURL = config.DefaultBuildFallbackBaseURL
	}
	config.NormalizeRoutingRetry(&base)
	config.NormalizeLegacyStatsig(&base)
	base.Audit = config.AuditConfig{
		BufferSize: value.Audit.BufferSize, BatchSize: value.Audit.BatchSize, FlushInterval: config.Duration(value.Audit.FlushInterval),
	}
	base.ClientKeyDefaults = config.ClientKeyDefaultsConfig{
		RPMLimit: value.ClientKeyDefaults.RPMLimit, MaxConcurrent: value.ClientKeyDefaults.MaxConcurrent,
	}
	base.Provider.ProactiveUpstreamSync = config.ProactiveUpstreamSyncConfig{
		Billing:                   value.ProactiveUpstreamSync.Billing,
		WebQuota:                  value.ProactiveUpstreamSync.WebQuota,
		ModelCatalogCatchup:       value.ProactiveUpstreamSync.ModelCatalogCatchup,
		AllowManualBillingRefresh: value.ProactiveUpstreamSync.AllowManualBillingRefresh,
		AllowManualQuotaRefresh:   value.ProactiveUpstreamSync.AllowManualQuotaRefresh,
	}
	// Older persisted settings omit promptCacheAffinity (all-zero). Keep YAML/default policy.
	pca := value.PromptCacheAffinity
	if pca.TTL > 0 || pca.Enabled || pca.Fingerprint || pca.Expire {
		affinityTTL := pca.TTL
		if affinityTTL <= 0 {
			affinityTTL = 24 * time.Hour
		}
		base.Routing.PromptCacheAffinity = config.PromptCacheAffinityConfig{
			Enabled: pca.Enabled, Fingerprint: pca.Fingerprint,
			Expire: pca.Expire, TTL: config.Duration(affinityTTL),
		}
	}
	return base
}

func toDomainConfig(value config.Config) settingsdomain.Config {
	randomDelay := value.Batch.RandomDelay.Value()
	return settingsdomain.Config{
		Server:   settingsdomain.ServerConfig{MaxConcurrentRequests: value.Server.MaxConcurrentRequests},
		Frontend: settingsdomain.FrontendConfig{PublicAPIBaseURL: value.Frontend.PublicAPIBaseURLOverride},
		ProviderBuild: settingsdomain.ProviderBuildConfig{
			BaseURL: value.Provider.Build.BaseURL, ClientVersion: value.Provider.Build.ClientVersion,
			ClientIdentifier: value.Provider.Build.ClientIdentifier, TokenAuth: value.Provider.Build.TokenAuth,
			UserAgent: value.Provider.Build.UserAgent,
			ResponseHeaderTimeout: value.Provider.Build.ResponseHeaderTimeout.Value(),
		},
		ProviderWeb: settingsdomain.ProviderWebConfig{
			BaseURL: value.Provider.Web.BaseURL, QuotaTimeout: value.Provider.Web.QuotaTimeout.Value(),
			StatsigMode: value.Provider.Web.StatsigMode, StatsigManualValue: value.Provider.Web.StatsigManualValue,
			StatsigSignerURL: value.Provider.Web.StatsigSignerURL,
			ChatTimeout:      value.Provider.Web.ChatTimeout.Value(), ImageTimeout: value.Provider.Web.ImageTimeout.Value(),
			VideoTimeout:     value.Provider.Web.VideoTimeout.Value(),
			MediaConcurrency: value.Provider.Web.MediaConcurrency, AllowNSFW: value.Provider.Web.AllowNSFW,
			RecoveryBackoffBase: value.Provider.Web.RecoveryBackoffBase.Value(), RecoveryBackoffMax: value.Provider.Web.RecoveryBackoffMax.Value(),
			FlareSolverrEnabled: value.Provider.Web.FlareSolverrEnabled, FlareSolverrURL: value.Provider.Web.FlareSolverrURL,
			FlareSolverrTargetURL: value.Provider.Web.FlareSolverrTargetURL,
			FlareSolverrTimeout:   value.Provider.Web.FlareSolverrTimeout.Value(), FlareSolverrRefreshInterval: value.Provider.Web.FlareSolverrRefreshInterval.Value(),
		},
		ProviderConsole: settingsdomain.ProviderConsoleConfig{
			BaseURL: value.Provider.Console.BaseURL, UserAgent: value.Provider.Console.UserAgent,
			ChatTimeout: value.Provider.Console.ChatTimeout.Value(),
		},
		Batch: settingsdomain.BatchConfig{
			ImportConcurrency: value.Batch.ImportConcurrency, ConversionConcurrency: value.Batch.ConversionConcurrency,
			SyncConcurrency: value.Batch.SyncConcurrency, RefreshConcurrency: value.Batch.RefreshConcurrency,
			RandomDelay: &randomDelay,
			DBBuffer: settingsdomain.DBBufferConfig{
				Enabled: value.Batch.DBBuffer.Enabled,
				Driver:  value.Batch.DBBuffer.Driver,
				Path:    value.Batch.DBBuffer.Path,
			},
		},
		Media: settingsdomain.MediaConfig{
			MaxImageBytes: value.Media.MaxImageBytes, MaxTotalBytes: value.Media.MaxTotalBytes,
			CleanupThresholdPercent: value.Media.CleanupThresholdPercent, CleanupInterval: value.Media.CleanupInterval.Value(),
		},
		Routing: settingsdomain.RoutingConfig{
			StickyTTL: value.Routing.StickyTTL.Value(), CooldownBase: value.Routing.CooldownBase.Value(),
			CooldownMax: value.Routing.CooldownMax.Value(), CapacityWait: value.Routing.CapacityWait.Value(), MaxAttempts: value.Routing.MaxAttempts,
			RetryStatusCodes: append([]int(nil), value.Routing.RetryStatusCodes...), RetryServerErrors: value.Routing.RetryServerErrors,
			DeprioritizeFailedAccounts: value.Routing.DeprioritizeFailedAccounts,
		},
		Audit: settingsdomain.AuditConfig{
			BufferSize: value.Audit.BufferSize, BatchSize: value.Audit.BatchSize, FlushInterval: value.Audit.FlushInterval.Value(),
		},
		ClientKeyDefaults: settingsdomain.ClientKeyDefaultsConfig{
			RPMLimit: value.ClientKeyDefaults.RPMLimit, MaxConcurrent: value.ClientKeyDefaults.MaxConcurrent,
		},
		ProactiveUpstreamSync: settingsdomain.ProactiveUpstreamSyncConfig{
			Billing:                   value.Provider.ProactiveUpstreamSync.Billing,
			WebQuota:                  value.Provider.ProactiveUpstreamSync.WebQuota,
			ModelCatalogCatchup:       value.Provider.ProactiveUpstreamSync.ModelCatalogCatchup,
			AllowManualBillingRefresh: value.Provider.ProactiveUpstreamSync.AllowManualBillingRefresh,
			AllowManualQuotaRefresh:   value.Provider.ProactiveUpstreamSync.AllowManualQuotaRefresh,
		},
		PromptCacheAffinity: settingsdomain.PromptCacheAffinityConfig{
			Enabled: value.Routing.PromptCacheAffinity.Enabled, Fingerprint: value.Routing.PromptCacheAffinity.Fingerprint,
			Expire: value.Routing.PromptCacheAffinity.Expire, TTL: value.Routing.PromptCacheAffinity.TTL.Value(),
		},
	}
}

func (s *Service) snapshotLocked() Snapshot {
	restartRequired := []string{}
	if s.cfg.Audit.BufferSize != s.activeBufferSize {
		restartRequired = append(restartRequired, "audit.bufferSize")
	}
	if s.cfg.Provider.Web.MediaConcurrency != s.activeMediaConcurrency {
		restartRequired = append(restartRequired, "providerWeb.mediaConcurrency")
	}
	return Snapshot{
		Config: toEditable(s.cfg),
		RecommendedProviderBuild: ProviderBuildRecommendation{
			ClientVersion: config.RecommendedBuildClientVersion,
			UserAgent:     config.RecommendedBuildUserAgent,
		},
		UpdatedAt: s.updatedAt, Revision: s.revision, RestartRequired: restartRequired,
	}
}

func mergeEditable(current config.Config, input EditableConfig) (config.Config, error) {
	next := current
	// Admin UI historically omitted server capacity; zero must not wipe the live value
	// or Validate fails with "server.maxConcurrentRequests 必须在 1 到 100000 之间".
	if input.Server.MaxConcurrentRequests > 0 {
		next.Server.MaxConcurrentRequests = input.Server.MaxConcurrentRequests
	}
	next.Frontend.PublicAPIBaseURLOverride = strings.TrimSpace(input.Frontend.PublicAPIBaseURL)
	next.Provider.Build.BaseURL = strings.TrimSpace(input.ProviderBuild.BaseURL)
	next.Provider.Build.ClientVersion = strings.TrimSpace(input.ProviderBuild.ClientVersion)
	next.Provider.Build.ClientIdentifier = strings.TrimSpace(input.ProviderBuild.ClientIdentifier)
	if tokenAuth := strings.TrimSpace(input.ProviderBuild.TokenAuth); tokenAuth != "" {
		next.Provider.Build.TokenAuth = tokenAuth
	}
	next.Provider.Build.UserAgent = strings.TrimSpace(input.ProviderBuild.UserAgent)
	next.Provider.Web.BaseURL = strings.TrimSpace(input.ProviderWeb.BaseURL)
	next.Provider.Web.StatsigMode = strings.TrimSpace(input.ProviderWeb.StatsigMode)
	next.Provider.Web.StatsigSignerURL = strings.TrimSpace(input.ProviderWeb.StatsigSignerURL)
	if next.Provider.Web.StatsigMode == config.StatsigModeManual {
		if value := strings.TrimSpace(input.ProviderWeb.StatsigManualValue); value != "" {
			next.Provider.Web.StatsigManualValue = value
		}
	} else {
		next.Provider.Web.StatsigManualValue = ""
	}
	next.Provider.Web.MediaConcurrency = input.ProviderWeb.MediaConcurrency
	next.Provider.Web.AllowNSFW = input.ProviderWeb.AllowNSFW
	next.Provider.Web.FlareSolverrEnabled = input.ProviderWeb.FlareSolverrEnabled
	next.Provider.Web.FlareSolverrURL = strings.TrimSpace(input.ProviderWeb.FlareSolverrURL)
	next.Provider.Web.FlareSolverrTargetURL = strings.TrimSpace(input.ProviderWeb.FlareSolverrTargetURL)
	next.Provider.Console.BaseURL = strings.TrimSpace(input.ProviderConsole.BaseURL)
	next.Provider.Console.UserAgent = strings.TrimSpace(input.ProviderConsole.UserAgent)
	randomDelay, err := time.ParseDuration(strings.TrimSpace(input.Batch.RandomDelay))
	if err != nil {
		return config.Config{}, fmt.Errorf("batch.randomDelay 必须是有效时长")
	}
	next.Batch = config.BatchConfig{
		ImportConcurrency: input.Batch.ImportConcurrency, ConversionConcurrency: input.Batch.ConversionConcurrency,
		SyncConcurrency: input.Batch.SyncConcurrency, RefreshConcurrency: input.Batch.RefreshConcurrency,
		RandomDelay: config.Duration(randomDelay),
		DBBuffer: normalizeDBBuffer(config.DBBufferConfig{
			Enabled: input.Batch.DBBuffer.Enabled,
			Driver:  input.Batch.DBBuffer.Driver,
			Path:    input.Batch.DBBuffer.Path,
		}),
	}
	next.Media.MaxImageBytes = input.Media.MaxImageBytes
	next.Media.MaxTotalBytes = input.Media.MaxTotalBytes
	next.Media.CleanupThresholdPercent = input.Media.CleanupThresholdPercent
	next.Routing.MaxAttempts = input.Routing.MaxAttempts
	next.Routing.RetryStatusCodes = append([]int(nil), input.Routing.RetryStatusCodes...)
	next.Routing.RetryServerErrors = input.Routing.RetryServerErrors
	next.Routing.DeprioritizeFailedAccounts = input.Routing.DeprioritizeFailedAccounts
	next.Audit.BufferSize = input.Audit.BufferSize
	next.Audit.BatchSize = input.Audit.BatchSize
	next.ClientKeyDefaults.RPMLimit = input.ClientKeyDefaults.RPMLimit
	next.ClientKeyDefaults.MaxConcurrent = input.ClientKeyDefaults.MaxConcurrent
	next.Provider.ProactiveUpstreamSync = config.ProactiveUpstreamSyncConfig{
		Billing:                   input.ProactiveUpstreamSync.Billing,
		WebQuota:                  input.ProactiveUpstreamSync.WebQuota,
		ModelCatalogCatchup:       input.ProactiveUpstreamSync.ModelCatalogCatchup,
		AllowManualBillingRefresh: input.ProactiveUpstreamSync.AllowManualBillingRefresh,
		AllowManualQuotaRefresh:   input.ProactiveUpstreamSync.AllowManualQuotaRefresh,
	}

	durations := []struct {
		path  string
		value string
		set   func(config.Duration)
	}{
		{"routing.stickyTTL", input.Routing.StickyTTL, func(value config.Duration) { next.Routing.StickyTTL = value }},
		{"routing.cooldownBase", input.Routing.CooldownBase, func(value config.Duration) { next.Routing.CooldownBase = value }},
		{"routing.cooldownMax", input.Routing.CooldownMax, func(value config.Duration) { next.Routing.CooldownMax = value }},
		{"routing.capacityWait", input.Routing.CapacityWait, func(value config.Duration) { next.Routing.CapacityWait = value }},
		{"audit.flushInterval", input.Audit.FlushInterval, func(value config.Duration) { next.Audit.FlushInterval = value }},
		{"providerWeb.quotaTimeout", input.ProviderWeb.QuotaTimeout, func(value config.Duration) { next.Provider.Web.QuotaTimeout = value }},
		{"providerWeb.chatTimeout", input.ProviderWeb.ChatTimeout, func(value config.Duration) { next.Provider.Web.ChatTimeout = value }},
		{"providerWeb.imageTimeout", input.ProviderWeb.ImageTimeout, func(value config.Duration) { next.Provider.Web.ImageTimeout = value }},
		{"providerWeb.videoTimeout", input.ProviderWeb.VideoTimeout, func(value config.Duration) { next.Provider.Web.VideoTimeout = value }},
		{"providerWeb.recoveryBackoffBase", input.ProviderWeb.RecoveryBackoffBase, func(value config.Duration) { next.Provider.Web.RecoveryBackoffBase = value }},
		{"providerWeb.recoveryBackoffMax", input.ProviderWeb.RecoveryBackoffMax, func(value config.Duration) { next.Provider.Web.RecoveryBackoffMax = value }},
		{"providerWeb.flareSolverrTimeout", input.ProviderWeb.FlareSolverrTimeout, func(value config.Duration) { next.Provider.Web.FlareSolverrTimeout = value }},
		{"providerWeb.flareSolverrRefreshInterval", input.ProviderWeb.FlareSolverrRefreshInterval, func(value config.Duration) { next.Provider.Web.FlareSolverrRefreshInterval = value }},
		{"providerConsole.chatTimeout", input.ProviderConsole.ChatTimeout, func(value config.Duration) { next.Provider.Console.ChatTimeout = value }},
		{"media.cleanupInterval", input.Media.CleanupInterval, func(value config.Duration) { next.Media.CleanupInterval = value }},
		{"batch.randomDelay", input.Batch.RandomDelay, func(value config.Duration) { next.Batch.RandomDelay = value }},
		{"providerBuild.responseHeaderTimeout", input.ProviderBuild.ResponseHeaderTimeout, func(value config.Duration) {
			next.Provider.Build.ResponseHeaderTimeout = value
		}},
		{"promptCacheAffinity.ttl", input.PromptCacheAffinity.TTL, func(value config.Duration) {
			next.Routing.PromptCacheAffinity.TTL = value
		}},
	}
	for _, item := range durations {
		raw := strings.TrimSpace(item.value)
		if raw == "" {
			// Keep current value when admin payload omits optional duration fields
			// (e.g. older clients without promptCacheAffinity.ttl).
			continue
		}
		value, err := time.ParseDuration(raw)
		if err != nil {
			return config.Config{}, fmt.Errorf("%s 必须是有效时长", item.path)
		}
		item.set(config.Duration(value))
	}
	// Only overwrite prompt-cache flags when TTL is present or any flag is explicitly set
	// via non-zero payload; always apply flags from input (admin form always sends them).
	next.Routing.PromptCacheAffinity.Enabled = input.PromptCacheAffinity.Enabled
	next.Routing.PromptCacheAffinity.Fingerprint = input.PromptCacheAffinity.Fingerprint
	next.Routing.PromptCacheAffinity.Expire = input.PromptCacheAffinity.Expire
	if strings.TrimSpace(input.PromptCacheAffinity.TTL) == "" && next.Routing.PromptCacheAffinity.TTL.Value() <= 0 {
		next.Routing.PromptCacheAffinity.TTL = config.Duration(24 * time.Hour)
	}
	config.NormalizeRoutingRetry(&next)
	config.NormalizeLegacyStatsig(&next)
	if err := next.Validate(); err != nil {
		return config.Config{}, err
	}
	return next, nil
}

func toEditable(cfg config.Config) EditableConfig {
	return EditableConfig{
		Server:   ServerConfig{MaxConcurrentRequests: cfg.Server.MaxConcurrentRequests},
		Frontend: FrontendConfig{PublicAPIBaseURL: cfg.Frontend.PublicAPIBaseURLOverride},
		ProviderBuild: ProviderBuildConfig{
			BaseURL: cfg.Provider.Build.BaseURL, ClientVersion: cfg.Provider.Build.ClientVersion,
			ClientIdentifier: cfg.Provider.Build.ClientIdentifier, TokenAuth: cfg.Provider.Build.TokenAuth,
			UserAgent: cfg.Provider.Build.UserAgent,
			ResponseHeaderTimeout: cfg.Provider.Build.ResponseHeaderTimeout.String(),
		},
		ProviderWeb: ProviderWebConfig{
			BaseURL: cfg.Provider.Web.BaseURL, QuotaTimeout: cfg.Provider.Web.QuotaTimeout.String(),
			StatsigMode: cfg.Provider.Web.StatsigMode, StatsigManualConfigured: strings.TrimSpace(cfg.Provider.Web.StatsigManualValue) != "",
			StatsigSignerURL: cfg.Provider.Web.StatsigSignerURL,
			ChatTimeout:      cfg.Provider.Web.ChatTimeout.String(), ImageTimeout: cfg.Provider.Web.ImageTimeout.String(),
			VideoTimeout:     cfg.Provider.Web.VideoTimeout.String(),
			MediaConcurrency: cfg.Provider.Web.MediaConcurrency, AllowNSFW: cfg.Provider.Web.AllowNSFW,
			RecoveryBackoffBase: cfg.Provider.Web.RecoveryBackoffBase.String(), RecoveryBackoffMax: cfg.Provider.Web.RecoveryBackoffMax.String(),
			FlareSolverrEnabled: cfg.Provider.Web.FlareSolverrEnabled, FlareSolverrURL: cfg.Provider.Web.FlareSolverrURL,
			FlareSolverrTargetURL: cfg.Provider.Web.FlareSolverrTargetURL,
			FlareSolverrTimeout:   cfg.Provider.Web.FlareSolverrTimeout.String(), FlareSolverrRefreshInterval: cfg.Provider.Web.FlareSolverrRefreshInterval.String(),
		},
		ProviderConsole: ProviderConsoleConfig{
			BaseURL: cfg.Provider.Console.BaseURL, UserAgent: cfg.Provider.Console.UserAgent,
			ChatTimeout: cfg.Provider.Console.ChatTimeout.String(),
		},
		Batch: BatchConfig{
			ImportConcurrency: cfg.Batch.ImportConcurrency, ConversionConcurrency: cfg.Batch.ConversionConcurrency,
			SyncConcurrency: cfg.Batch.SyncConcurrency, RefreshConcurrency: cfg.Batch.RefreshConcurrency,
			RandomDelay: cfg.Batch.RandomDelay.String(),
			DBBuffer: func() DBBufferConfig {
				buf := normalizeDBBuffer(cfg.Batch.DBBuffer)
				return DBBufferConfig{Enabled: buf.Enabled, Driver: buf.Driver, Path: buf.Path}
			}(),
		},
		Media: MediaConfig{
			MaxImageBytes: cfg.Media.MaxImageBytes, MaxTotalBytes: cfg.Media.MaxTotalBytes,
			CleanupThresholdPercent: cfg.Media.CleanupThresholdPercent, CleanupInterval: cfg.Media.CleanupInterval.String(),
		},
		Routing: RoutingConfig{
			StickyTTL: cfg.Routing.StickyTTL.String(), CooldownBase: cfg.Routing.CooldownBase.String(),
			CooldownMax: cfg.Routing.CooldownMax.String(), CapacityWait: cfg.Routing.CapacityWait.String(), MaxAttempts: cfg.Routing.MaxAttempts,
			RetryStatusCodes: append([]int(nil), cfg.Routing.RetryStatusCodes...), RetryServerErrors: cfg.Routing.RetryServerErrors,
			DeprioritizeFailedAccounts: cfg.Routing.DeprioritizeFailedAccounts,
		},
		PromptCacheAffinity: PromptCacheAffinityConfig{
			Enabled: cfg.Routing.PromptCacheAffinity.Enabled, Fingerprint: cfg.Routing.PromptCacheAffinity.Fingerprint,
			Expire: cfg.Routing.PromptCacheAffinity.Expire, TTL: cfg.Routing.PromptCacheAffinity.TTL.String(),
		},
		Audit: AuditConfig{
			BufferSize: cfg.Audit.BufferSize, BatchSize: cfg.Audit.BatchSize, FlushInterval: cfg.Audit.FlushInterval.String(),
		},
		ClientKeyDefaults: ClientKeyDefaultsConfig{RPMLimit: cfg.ClientKeyDefaults.RPMLimit, MaxConcurrent: cfg.ClientKeyDefaults.MaxConcurrent},
		ProactiveUpstreamSync: ProactiveUpstreamSyncConfig{
			Billing:                   cfg.Provider.ProactiveUpstreamSync.Billing,
			WebQuota:                  cfg.Provider.ProactiveUpstreamSync.WebQuota,
			ModelCatalogCatchup:       cfg.Provider.ProactiveUpstreamSync.ModelCatalogCatchup,
			AllowManualBillingRefresh: cfg.Provider.ProactiveUpstreamSync.AllowManualBillingRefresh,
			AllowManualQuotaRefresh:   cfg.Provider.ProactiveUpstreamSync.AllowManualQuotaRefresh,
		},
	}
}

// normalizeDBBuffer fills defaults so admin JSON never returns an empty driver
// (frontend decoder requires none|redis|sqlite; legacy rows omit dbBuffer entirely).
// When enabled with driver "none" (or empty), force disabled so Validate does not
// reject a common admin UI default and surface as "保存运行设置失败".
func normalizeDBBuffer(value config.DBBufferConfig) config.DBBufferConfig {
	driver := strings.ToLower(strings.TrimSpace(value.Driver))
	switch driver {
	case "redis", "sqlite", "none":
		// ok
	case "memory":
		driver = "none"
	default:
		driver = "none"
	}
	enabled := value.Enabled
	path := strings.TrimSpace(value.Path)
	if enabled && (driver == "none" || driver == "") {
		enabled = false
		driver = "none"
	}
	if enabled && driver == "sqlite" && path == "" {
		// Keep enabled=false rather than hard-failing the whole settings save.
		enabled = false
	}
	if !enabled && driver != "redis" && driver != "sqlite" {
		driver = "none"
	}
	return config.DBBufferConfig{
		Enabled: enabled,
		Driver:  driver,
		Path:    path,
	}
}
