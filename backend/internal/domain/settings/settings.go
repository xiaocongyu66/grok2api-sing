package settings

import "time"

// Config 表示可跨重启持久化并支持热加载的网关运行参数。
type Config struct {
	Server                ServerConfig
	ProviderBuild         ProviderBuildConfig
	ProviderWeb           ProviderWebConfig
	ProviderConsole       ProviderConsoleConfig
	ProactiveUpstreamSync ProactiveUpstreamSyncConfig
	Batch                 BatchConfig
	Media                 MediaConfig
	Frontend              FrontendConfig
	Routing               RoutingConfig
	Audit                 AuditConfig
	ClientKeyDefaults     ClientKeyDefaultsConfig
	PromptCacheAffinity   PromptCacheAffinityConfig
}

// PromptCacheAffinityConfig is hot-reloadable xAI prompt-cache affinity policy.
type PromptCacheAffinityConfig struct {
	Enabled     bool
	Fingerprint bool
	Expire      bool
	TTL         time.Duration
}

// ProactiveUpstreamSyncConfig toggles optional xAI billing/quota/model polling.
// Default all-false matches CLIProxy (inference + token refresh only).
type ProactiveUpstreamSyncConfig struct {
	Billing                   bool
	WebQuota                  bool
	ModelCatalogCatchup       bool
	AllowManualBillingRefresh bool
	AllowManualQuotaRefresh   bool
}

// ServerConfig 定义可热更新的推理入口容量参数。
type ServerConfig struct {
	MaxConcurrentRequests int
}

// FrontendConfig 定义公开 API 地址的运行时覆盖值；留空时使用配置文件值。
type FrontendConfig struct {
	PublicAPIBaseURL string
}

type ProviderConsoleConfig struct {
	BaseURL     string
	UserAgent   string
	ChatTimeout time.Duration
}

type MediaConfig struct {
	MaxImageBytes           int64
	MaxTotalBytes           int64
	CleanupThresholdPercent int
	CleanupInterval         time.Duration
}

type ProviderWebConfig struct {
	BaseURL             string
	StatsigMode         string
	StatsigManualValue  string
	StatsigSignerURL    string
	QuotaTimeout        time.Duration
	ChatTimeout         time.Duration
	ImageTimeout        time.Duration
	VideoTimeout        time.Duration
	MediaConcurrency    int
	AllowNSFW           bool
	RecoveryBackoffBase time.Duration
	RecoveryBackoffMax  time.Duration
}

// BatchConfig 定义账号导入、转换、同步和凭据刷新的并发上限。
type BatchConfig struct {
	ImportConcurrency     int
	ConversionConcurrency int
	SyncConcurrency       int
	RefreshConcurrency    int
	RandomDelay           *time.Duration
	DBBuffer              DBBufferConfig
}

// DBBufferConfig enables optional Redis/SQLite buffering for bulk DB operations to reduce main DB pressure.
type DBBufferConfig struct {
	Enabled bool
	Driver  string // "redis" or "sqlite"
	Path    string // for sqlite
}

// ProviderBuildConfig 定义 Grok Build CLI 上游协议标识。
type ProviderBuildConfig struct {
	BaseURL          string
	ClientVersion    string
	ClientIdentifier string
	TokenAuth        string
	UserAgent        string
}

// RoutingConfig 定义会话粘性、冷却和故障切换边界。
type RoutingConfig struct {
	StickyTTL                  time.Duration
	CooldownBase               time.Duration
	CooldownMax                time.Duration
	CapacityWait               time.Duration
	MaxAttempts                int
	RetryStatusCodes           []int
	RetryServerErrors          bool
	// DeprioritizeFailedAccounts puts accounts with higher failure_count last when selecting
	// and when batch-syncing quotas (switchable; default on).
	DeprioritizeFailedAccounts bool
}

// AuditConfig 定义请求审计异步写入参数。
type AuditConfig struct {
	BufferSize    int
	BatchSize     int
	FlushInterval time.Duration
}

// ClientKeyDefaultsConfig 定义新建客户端密钥的默认限制。
type ClientKeyDefaultsConfig struct {
	RPMLimit      int
	MaxConcurrent int
}
