package config

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	clientkeydomain "github.com/chenyme/grok2api/backend/internal/domain/clientkey"
	"github.com/chenyme/grok2api/backend/internal/pkg/signerurl"
	"gopkg.in/yaml.v3"
)

const (
	// StatsigModeLocal signs x-statsig-id in-process (pure Go, no remote signer).
	StatsigModeLocal              = "local"
	StatsigModeManual             = "manual"
	StatsigModeURL                = "url"
	DefaultStatsigSignerURL       = "https://grok.wodf.de/sign"
	RecommendedBuildClientVersion = "0.2.101"
	RecommendedBuildUserAgent     = "grok-shell/0.2.101 (linux; x86_64)"

	maxServerBodyBytes    = 256 << 20
	maxRequestTimeout     = 24 * time.Hour
	maxReadTimeout        = time.Hour
	maxRoutingTTL         = 30 * 24 * time.Hour
	maxRoutingCooldown    = 24 * time.Hour
	minAuditFlushInterval = 10 * time.Millisecond
	maxAuditFlushInterval = time.Minute
	maxAuditBufferSize    = 262144
	maxAuditBatchSize     = 4096
)

// Config 表示后端运行配置。
type Config struct {
	Server            ServerConfig            `yaml:"server"`
	Frontend          FrontendConfig          `yaml:"frontend"`
	Database          DatabaseConfig          `yaml:"database"`
	RuntimeStore      RuntimeStoreConfig      `yaml:"runtimeStore"`
	Auth              AuthConfig              `yaml:"auth"`
	Secrets           Secrets                 `yaml:"secrets"`
	BootstrapAdmin    BootstrapAdminConfig    `yaml:"bootstrapAdmin"`
	Provider          ProviderConfig          `yaml:"provider"`
	Batch             BatchConfig             `yaml:"-"`
	Media             MediaConfig             `yaml:"media"`
	Routing           RoutingConfig           `yaml:"routing"`
	Audit             AuditConfig             `yaml:"audit"`
	ClientKeyDefaults ClientKeyDefaultsConfig `yaml:"clientKeyDefaults"`
}

type ServerConfig struct {
	Listen                string   `yaml:"listen"`
	MaxBodyBytes          int64    `yaml:"maxBodyBytes"`
	MaxConcurrentRequests int      `yaml:"maxConcurrentRequests"`
	ReadTimeout           Duration `yaml:"readTimeout"`
	RequestTimeout        Duration `yaml:"requestTimeout"`
	SwaggerEnabled        bool     `yaml:"swaggerEnabled"`
	// TrustedProxies lists reverse-proxy CIDRs/IPs trusted for X-Forwarded-For / X-Real-IP.
	// Empty means ClientIP uses the direct remote address only (no client-supplied spoofing).
	TrustedProxies []string `yaml:"trustedProxies"`
}

type FrontendConfig struct {
	PublicAPIBaseURL         string `yaml:"publicApiBaseURL"`
	PublicAPIBaseURLOverride string `yaml:"-"`
	StaticPath               string `yaml:"staticPath"`
}

const DefaultPublicAPIBaseURL = "http://127.0.0.1:8000"

// EffectivePublicAPIBaseURL 按运行设置、配置文件、内置默认值的顺序解析公开地址。
func (c FrontendConfig) EffectivePublicAPIBaseURL() string {
	for _, value := range []string{c.PublicAPIBaseURLOverride, c.PublicAPIBaseURL} {
		if value = strings.TrimRight(strings.TrimSpace(value), "/"); value != "" {
			return value
		}
	}
	return DefaultPublicAPIBaseURL
}

type DatabaseConfig struct {
	Driver   string                 `yaml:"driver"`
	SQLite   SQLiteDatabaseConfig   `yaml:"sqlite"`
	Postgres PostgresDatabaseConfig `yaml:"postgres"`
}

type SQLiteDatabaseConfig struct {
	Path string `yaml:"path"`
}

type PostgresDatabaseConfig struct {
	DSN          string `yaml:"dsn"`
	MaxOpenConns int    `yaml:"maxOpenConns"`
	MaxIdleConns int    `yaml:"maxIdleConns"`
}

type RuntimeStoreConfig struct {
	Driver string             `yaml:"driver"`
	Redis  RedisRuntimeConfig `yaml:"redis"`
}

type RedisRuntimeConfig struct {
	Address   string `yaml:"address"`
	Username  string `yaml:"username"`
	Password  string `yaml:"password"`
	Database  int    `yaml:"database"`
	KeyPrefix string `yaml:"keyPrefix"`
	TLS       bool   `yaml:"tls"`
}

type AuthConfig struct {
	AccessTokenTTL  Duration `yaml:"accessTokenTTL"`
	RefreshTokenTTL Duration `yaml:"refreshTokenTTL"`
	SecureCookies   bool     `yaml:"secureCookies"`
	// APIKeyHeaders lists extra HTTP headers that may carry the client API key value
	// (e.g. "congyu_15fc"). Authorization: Bearer and X-API-Key are always accepted.
	APIKeyHeaders []string `yaml:"apiKeyHeaders"`
}

type ProviderConfig struct {
	ProactiveUpstreamSync ProactiveUpstreamSyncConfig `yaml:"proactiveUpstreamSync"`
	Build                 BuildProviderConfig         `yaml:"build"`
	Web                   WebProviderConfig           `yaml:"web"`
	Console               ConsoleProviderConfig       `yaml:"console"`
}

type BuildProviderConfig struct {
	BaseURL string `yaml:"baseURL"`
	// FallbackBaseURL is the XAI API root used when a Super Build account is
	// marked for automatic inference fallback (default https://api.x.ai/v1).
	FallbackBaseURL  string `yaml:"fallbackBaseURL"`
	ClientVersion    string `yaml:"clientVersion"`
	ClientIdentifier string `yaml:"clientIdentifier"`
	TokenAuth        string `yaml:"tokenAuth"`
	UserAgent        string `yaml:"userAgent"`
}

// DefaultBuildFallbackBaseURL 是主 Build API 对可回退推理操作 403 时探测的 XAI API 根地址。
const DefaultBuildFallbackBaseURL = "https://api.x.ai/v1"

// NormalizeBuildFallbackBaseURL 在旧配置缺字段时填入默认 XAI 备用地址。
func NormalizeBuildFallbackBaseURL(value string) string {
	if strings.TrimSpace(value) == "" {
		return DefaultBuildFallbackBaseURL
	}
	return strings.TrimSpace(value)
}

type WebProviderConfig struct {
	BaseURL             string   `yaml:"baseURL"`
	StatsigMode         string   `yaml:"-"`
	StatsigManualValue  string   `yaml:"-"`
	StatsigSignerURL    string   `yaml:"-"`
	QuotaTimeout        Duration `yaml:"quotaTimeout"`
	ChatTimeout         Duration `yaml:"chatTimeout"`
	ImageTimeout        Duration `yaml:"imageTimeout"`
	VideoTimeout        Duration `yaml:"videoTimeout"`
	MediaConcurrency    int      `yaml:"mediaConcurrency"`
	AllowNSFW           bool     `yaml:"allowNSFW"`
	RecoveryBackoffBase Duration `yaml:"recoveryBackoffBase"`
	RecoveryBackoffMax  Duration `yaml:"recoveryBackoffMax"`
}

type ConsoleProviderConfig struct {
	BaseURL     string   `yaml:"baseURL"`
	UserAgent   string   `yaml:"userAgent"`
	ChatTimeout Duration `yaml:"chatTimeout"`
}

// BatchConfig 定义可热加载的账号批量任务并发上限。
type BatchConfig struct {
	ImportConcurrency     int
	ConversionConcurrency int
	SyncConcurrency       int
	RefreshConcurrency    int
	RandomDelay           Duration
	// DBBuffer enables optional buffering for bulk DB-heavy operations (e.g. account conversion,
	// batch updates). Data is pulled from main DB into the buffer (Redis or local SQLite),
	// processed there to reduce main DB load, then batched back to main DB.
	// Driver: "none" (default), "redis", "sqlite".
	DBBuffer DBBufferConfig `yaml:"dbBuffer"`
}

type DBBufferConfig struct {
	Enabled bool   `yaml:"enabled"`
	Driver  string `yaml:"driver"` // "redis" or "sqlite"
	Path    string `yaml:"path"`   // for sqlite buffer file, e.g. "./data/bulk-buffer.db"
}

type MediaConfig struct {
	// Driver selects object storage: "local" or "r2" (Cloudflare R2 / S3-compatible).
	Driver                  string           `yaml:"driver"`
	MaxImageBytes           int64            `yaml:"-"`
	MaxTotalBytes           int64            `yaml:"-"`
	CleanupThresholdPercent int              `yaml:"-"`
	CleanupInterval         Duration         `yaml:"-"`
	Local                   LocalMediaConfig `yaml:"local"`
	R2                      R2MediaConfig    `yaml:"r2"`
}

type LocalMediaConfig struct {
	Path string `yaml:"path"`
}

// R2MediaConfig is Cloudflare R2 (S3 API) storage for generated images.
type R2MediaConfig struct {
	// Endpoint e.g. https://<accountid>.r2.cloudflarestorage.com
	Endpoint        string `yaml:"endpoint"`
	AccessKeyID     string `yaml:"accessKeyId"`
	SecretAccessKey string `yaml:"secretAccessKey"`
	Bucket          string `yaml:"bucket"`
	// Region is accepted by the SDK; R2 uses "auto".
	Region string `yaml:"region"`
	// Prefix is an optional key prefix inside the bucket (e.g. grok2api).
	Prefix string `yaml:"prefix"`
	// PublicBaseURL optional custom domain for objects (informational; gateway still proxies reads).
	PublicBaseURL string `yaml:"publicBaseURL"`
}

type ProactiveUpstreamSyncConfig struct {
	Billing                   bool `yaml:"billing"`
	WebQuota                  bool `yaml:"webQuota"`
	ModelCatalogCatchup       bool `yaml:"modelCatalogCatchup"`
	AllowManualBillingRefresh bool `yaml:"allowManualBillingRefresh"`
	AllowManualQuotaRefresh   bool `yaml:"allowManualQuotaRefresh"`
}

type PromptCacheAffinityConfig struct {
	// Enabled master switch (default true).
	Enabled bool `yaml:"enabled"`
	// Fingerprint derives ids from IP/User-Agent/client key when no session header is present.
	Fingerprint bool `yaml:"fingerprint"`
	// Expire enables TTL on fingerprint mappings; false keeps mappings until manual clear.
	Expire bool `yaml:"expire"`
	// TTL is the mapping lifetime when Expire is true (default 24h).
	TTL Duration `yaml:"ttl"`
}

// DefaultRetryStatusCodes: 403 is intentionally excluded so permanent bans do not cascade across the pool.
// Web egress anti-bot 403 is still handled separately via RetryForbiddenAsEgress.
var DefaultRetryStatusCodes = []int{402, 429, 503}

type RoutingConfig struct {
	StickyTTL    Duration `yaml:"stickyTTL"`
	CooldownBase Duration `yaml:"cooldownBase"`
	CooldownMax  Duration `yaml:"cooldownMax"`
	CapacityWait Duration `yaml:"capacityWait"`
	MaxAttempts  int      `yaml:"maxAttempts"`
	// RetryStatusCodes lists exact upstream HTTP codes that trigger account failover.
	// Empty falls back to DefaultRetryStatusCodes after load/validate normalization.
	RetryStatusCodes []int `yaml:"retryStatusCodes"`
	// RetryServerErrors retries any status >= 500 when true (default).
	RetryServerErrors bool `yaml:"retryServerErrors"`
	// PromptCacheAffinity stabilizes x-grok-conv-id for upstream prompt-cache hits.
	PromptCacheAffinity PromptCacheAffinityConfig `yaml:"promptCacheAffinity"`
	// ReasoningReplay holds previous-turn encrypted reasoning for multi-turn Build
	// when clients omit thinking signatures (upstream CLIProxyAPI-compatible).
	ReasoningReplayEnabled    bool     `yaml:"reasoningReplayEnabled"`
	ReasoningReplayTTL        Duration `yaml:"reasoningReplayTTL"`
	ReasoningReplayMaxEntries int      `yaml:"reasoningReplayMaxEntries"`
}

type AuditConfig struct {
	BufferSize    int      `yaml:"bufferSize"`
	BatchSize     int      `yaml:"batchSize"`
	FlushInterval Duration `yaml:"flushInterval"`
}

type ClientKeyDefaultsConfig struct {
	RPMLimit      int `yaml:"rpmLimit"`
	MaxConcurrent int `yaml:"maxConcurrent"`
}

type Secrets struct {
	JWTSecret               string `yaml:"jwtSecret"`
	CredentialEncryptionKey string `yaml:"credentialEncryptionKey"`
}

type BootstrapAdminConfig struct {
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

// Duration 支持在 YAML 中使用 10m、1h 等可读时间格式。
type Duration time.Duration

func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	parsed, err := time.ParseDuration(node.Value)
	if err != nil {
		return err
	}
	*d = Duration(parsed)
	return nil
}

func (d Duration) MarshalYAML() (any, error) { return d.String(), nil }

func (d Duration) Value() time.Duration { return time.Duration(d) }

func (d Duration) String() string {
	value := d.Value().String()
	if strings.HasSuffix(value, "m0s") {
		value = strings.TrimSuffix(value, "0s")
	}
	if strings.HasSuffix(value, "h0m") {
		value = strings.TrimSuffix(value, "0m")
	}
	return value
}

// Load 从 YAML 加载启动配置，并为非敏感运行参数补充代码默认值。
func Load(path string) (Config, error) {
	cfg := defaultConfig()
	loadedFrom := ""
	if strings.TrimSpace(path) != "" {
		data, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return Config{}, fmt.Errorf("读取配置文件: %w", err)
		}
		if err == nil {
			loadedFrom = path
			decoder := yaml.NewDecoder(bytes.NewReader(data))
			decoder.KnownFields(true)
			if err := decoder.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
				return Config{}, fmt.Errorf("解析配置文件: %w", err)
			}
			var extra any
			if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
				if err != nil {
					return Config{}, fmt.Errorf("解析配置文件: %w", err)
				}
				return Config{}, errors.New("配置文件只能包含一个 YAML 文档")
			}
		}
	}
	if loadedFrom != "" {
		if err := resolveRelativePaths(&cfg, loadedFrom); err != nil {
			return Config{}, err
		}
	}
	NormalizeRoutingRetry(&cfg)
	NormalizeLegacyStatsig(&cfg)
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// DefaultLocalStatsigSignerURL is retained only for migration of historical configs.
// New installs default to StatsigModeLocal (in-process pure-Go signing).
const DefaultLocalStatsigSignerURL = "http://127.0.0.1:8788/sign"

// NormalizeLegacyStatsig migrates empty / third-party / placeholder signer setups to local
// pure-Go signing so upgrades boot without a remote Statsig service.
func NormalizeLegacyStatsig(cfg *Config) {
	if cfg == nil {
		return
	}
	mode := strings.TrimSpace(cfg.Provider.Web.StatsigMode)
	if mode == "" {
		cfg.Provider.Web.StatsigMode = StatsigModeLocal
		cfg.Provider.Web.StatsigSignerURL = ""
		return
	}
	if mode != StatsigModeURL {
		return
	}
	signer := strings.TrimSpace(cfg.Provider.Web.StatsigSignerURL)
	// Placeholder or banned third-party defaults → pure-Go local signing.
	if signer == "" ||
		strings.EqualFold(signer, DefaultStatsigSignerURL) ||
		strings.EqualFold(signer, DefaultLocalStatsigSignerURL) {
		cfg.Provider.Web.StatsigMode = StatsigModeLocal
		cfg.Provider.Web.StatsigSignerURL = ""
	}
}

func resolveRelativePaths(cfg *Config, configPath string) error {
	absoluteConfigPath, err := filepath.Abs(configPath)
	if err != nil {
		return fmt.Errorf("解析配置文件路径: %w", err)
	}
	baseDir := filepath.Dir(absoluteConfigPath)
	if cfg.Database.Driver == "sqlite" {
		path := strings.TrimSpace(cfg.Database.SQLite.Path)
		if path != "" && !filepath.IsAbs(path) {
			cfg.Database.SQLite.Path = filepath.Clean(filepath.Join(baseDir, path))
		}
	}
	mediaPath := strings.TrimSpace(cfg.Media.Local.Path)
	if mediaPath != "" && !filepath.IsAbs(mediaPath) {
		cfg.Media.Local.Path = filepath.Clean(filepath.Join(baseDir, mediaPath))
	}
	staticPath := strings.TrimSpace(cfg.Frontend.StaticPath)
	if staticPath != "" && !filepath.IsAbs(staticPath) {
		cfg.Frontend.StaticPath = filepath.Clean(filepath.Join(baseDir, staticPath))
	}
	return nil
}

// NormalizeRoutingRetry fills defaults and deduplicates retry status codes.
func NormalizeRoutingRetry(cfg *Config) {
	if cfg == nil {
		return
	}
	if len(cfg.Routing.RetryStatusCodes) == 0 {
		cfg.Routing.RetryStatusCodes = append([]int(nil), DefaultRetryStatusCodes...)
		return
	}
	seen := make(map[int]struct{}, len(cfg.Routing.RetryStatusCodes))
	normalized := make([]int, 0, len(cfg.Routing.RetryStatusCodes))
	for _, code := range cfg.Routing.RetryStatusCodes {
		if _, ok := seen[code]; ok {
			continue
		}
		seen[code] = struct{}{}
		normalized = append(normalized, code)
	}
	cfg.Routing.RetryStatusCodes = normalized
}

// IsRetryableStatus reports whether an upstream status should trigger account failover.
func IsRetryableStatus(status int, codes []int, retryServerErrors bool) bool {
	if retryServerErrors && status >= 500 {
		return true
	}
	if len(codes) == 0 {
		codes = DefaultRetryStatusCodes
	}
	for _, code := range codes {
		if status == code {
			return true
		}
	}
	return false
}

func validateRetryStatusCodes(codes []int) error {
	if len(codes) == 0 {
		return nil
	}
	for _, code := range codes {
		if code < 100 || code > 599 {
			return errors.New("routing.retryStatusCodes 必须是 100-599 的 HTTP 状态码")
		}
	}
	return nil
}

func (c Config) Validate() error {
	if strings.TrimSpace(c.Server.Listen) == "" {
		return errors.New("server.listen 不能为空")
	}
	if c.Server.MaxBodyBytes <= 0 || c.Server.MaxBodyBytes > maxServerBodyBytes {
		return fmt.Errorf("server.maxBodyBytes 必须在 1 到 %d 字节之间", maxServerBodyBytes)
	}
	if c.Server.ReadTimeout.Value() <= 0 || c.Server.ReadTimeout.Value() > maxReadTimeout {
		return errors.New("server.readTimeout 必须大于零且不超过 1 小时")
	}
	if c.Server.RequestTimeout.Value() <= 0 || c.Server.RequestTimeout.Value() > maxRequestTimeout {
		return errors.New("server.requestTimeout 必须大于零且不超过 24 小时")
	}
	if c.Server.MaxConcurrentRequests < 1 || c.Server.MaxConcurrentRequests > 100000 {
		return errors.New("server.maxConcurrentRequests 必须在 1 到 100000 之间")
	}
	for _, item := range []struct {
		name  string
		value string
	}{
		{name: "frontend.publicApiBaseURL", value: c.Frontend.PublicAPIBaseURL},
		{name: "frontend.publicApiBaseURL 运行设置", value: c.Frontend.PublicAPIBaseURLOverride},
	} {
		if publicBase := strings.TrimSpace(item.value); publicBase != "" {
			publicAPIURL, err := url.ParseRequestURI(publicBase)
			if err != nil || (publicAPIURL.Scheme != "http" && publicAPIURL.Scheme != "https") || publicAPIURL.Host == "" || publicAPIURL.User != nil || publicAPIURL.RawQuery != "" || publicAPIURL.Fragment != "" {
				return fmt.Errorf("%s 必须是不含凭据、查询参数和片段的 HTTP(S) URL", item.name)
			}
		}
	}
	switch c.Database.Driver {
	case "sqlite":
		if strings.TrimSpace(c.Database.SQLite.Path) == "" {
			return errors.New("database.sqlite.path 不能为空")
		}
	case "postgres":
		if strings.TrimSpace(c.Database.Postgres.DSN) == "" {
			return errors.New("database.postgres.dsn 不能为空")
		}
		if c.Database.Postgres.MaxOpenConns < 1 || c.Database.Postgres.MaxOpenConns > 1000 || c.Database.Postgres.MaxIdleConns < 0 || c.Database.Postgres.MaxIdleConns > c.Database.Postgres.MaxOpenConns {
			return errors.New("database.postgres 连接池配置无效")
		}
	default:
		return errors.New("database.driver 必须是 sqlite 或 postgres")
	}
	switch c.RuntimeStore.Driver {
	case "memory":
	case "redis":
		if strings.TrimSpace(c.RuntimeStore.Redis.Address) == "" {
			return errors.New("runtimeStore.redis.address 不能为空")
		}
		if c.RuntimeStore.Redis.Database < 0 || c.RuntimeStore.Redis.Database > 1024 {
			return errors.New("runtimeStore.redis.database 必须在 0 到 1024 之间")
		}
		if prefix := strings.TrimSpace(c.RuntimeStore.Redis.KeyPrefix); prefix == "" || len(prefix) > 128 {
			return errors.New("runtimeStore.redis.keyPrefix 必须在 1 到 128 个字符之间")
		}
	default:
		return errors.New("runtimeStore.driver 必须是 memory 或 redis")
	}
	switch strings.ToLower(strings.TrimSpace(c.Media.Driver)) {
	case "", "local":
		c.Media.Driver = "local"
		if strings.TrimSpace(c.Media.Local.Path) == "" {
			return errors.New("media.local.path 不能为空")
		}
	case "r2":
		c.Media.Driver = "r2"
		if strings.TrimSpace(c.Media.R2.Endpoint) == "" || strings.TrimSpace(c.Media.R2.AccessKeyID) == "" ||
			strings.TrimSpace(c.Media.R2.SecretAccessKey) == "" || strings.TrimSpace(c.Media.R2.Bucket) == "" {
			return errors.New("media.driver=r2 时必须配置 endpoint、accessKeyId、secretAccessKey、bucket")
		}
		if _, err := url.ParseRequestURI(strings.TrimSpace(c.Media.R2.Endpoint)); err != nil {
			return errors.New("media.r2.endpoint 必须是有效 URL")
		}
		if pub := strings.TrimSpace(c.Media.R2.PublicBaseURL); pub != "" {
			if _, err := url.ParseRequestURI(pub); err != nil {
				return errors.New("media.r2.publicBaseURL 必须是有效 URL")
			}
		}
	default:
		return errors.New("media.driver 仅支持 local 或 r2")
	}
	if c.Media.MaxImageBytes < 1<<20 || c.Media.MaxImageBytes > 32<<20 {
		return errors.New("media.maxImageBytes 必须在 1 MiB 到 32 MiB 之间")
	}
	if c.Media.MaxTotalBytes < c.Media.MaxImageBytes || c.Media.MaxTotalBytes > 1<<40 {
		return errors.New("media.maxTotalBytes 必须不小于单图上限且不超过 1 TiB")
	}
	if c.Media.CleanupThresholdPercent < 50 || c.Media.CleanupThresholdPercent > 95 {
		return errors.New("media.cleanupThresholdPercent 必须在 50 到 95 之间")
	}
	if c.Media.CleanupInterval.Value() < time.Minute || c.Media.CleanupInterval.Value() > 24*time.Hour {
		return errors.New("media.cleanupInterval 必须在 1 分钟到 24 小时之间")
	}
	if len(c.Secrets.JWTSecret) < 32 {
		return errors.New("secrets.jwtSecret 至少需要 32 个字符")
	}
	if isExampleSecret(c.Secrets.JWTSecret) {
		return errors.New("secrets.jwtSecret 不能使用示例占位值")
	}
	if !validCredentialEncryptionKey(c.Secrets.CredentialEncryptionKey) {
		return errors.New("secrets.credentialEncryptionKey 必须是 Base64 编码的 32 字节密钥")
	}
	if isExampleSecret(c.BootstrapAdmin.Password) {
		return errors.New("bootstrapAdmin.password 不能使用示例占位值")
	}
	if c.Auth.AccessTokenTTL.Value() <= 0 || c.Auth.RefreshTokenTTL.Value() <= 0 {
		return errors.New("JWT 有效期必须大于零")
	}
	providerURL, err := url.ParseRequestURI(strings.TrimSpace(c.Provider.Build.BaseURL))
	if err != nil || providerURL.Scheme == "" || providerURL.Host == "" {
		return errors.New("provider.build.baseURL 必须是有效 URL")
	}
	if strings.TrimSpace(c.Provider.Build.ClientVersion) == "" || strings.TrimSpace(c.Provider.Build.ClientIdentifier) == "" || strings.TrimSpace(c.Provider.Build.TokenAuth) == "" || strings.TrimSpace(c.Provider.Build.UserAgent) == "" {
		return errors.New("provider.build 客户端标识不能为空")
	}
	webURL, err := url.ParseRequestURI(strings.TrimSpace(c.Provider.Web.BaseURL))
	if err != nil || webURL.Scheme != "https" || webURL.Host == "" || webURL.User != nil {
		return errors.New("provider.web.baseURL 必须是无凭据的 HTTPS URL")
	}
	switch c.Provider.Web.StatsigMode {
	case StatsigModeLocal:
		// Pure-Go in-process signer; no remote endpoint or manual value required.
	case StatsigModeManual:
		if !validStatsigID(c.Provider.Web.StatsigManualValue) {
			return errors.New("provider.web 手动 x-statsig-id 格式无效")
		}
	case StatsigModeURL:
		signer := strings.TrimSpace(c.Provider.Web.StatsigSignerURL)
		if signer == "" {
			return errors.New("provider.web.statsigSignerURL 不能为空（url 模式须显式配置自建或受信签名服务）")
		}
		if strings.EqualFold(signer, DefaultStatsigSignerURL) {
			return fmt.Errorf("provider.web.statsigSignerURL 不能使用内置第三方默认地址 %s，请使用 local 模式或自建签名服务", DefaultStatsigSignerURL)
		}
		if err := signerurl.Validate(signer); err != nil {
			return fmt.Errorf("provider.web Statsig 签名 URL 无效: %w", err)
		}
	default:
		return errors.New("provider.web Statsig 模式必须是 local、manual 或 url")
	}
	if c.Provider.Web.QuotaTimeout.Value() < time.Second || c.Provider.Web.QuotaTimeout.Value() > 2*time.Minute ||
		c.Provider.Web.ChatTimeout.Value() < 5*time.Second || c.Provider.Web.ChatTimeout.Value() > 30*time.Minute ||
		c.Provider.Web.ImageTimeout.Value() < 5*time.Second || c.Provider.Web.ImageTimeout.Value() > 30*time.Minute ||
		c.Provider.Web.VideoTimeout.Value() < time.Minute || c.Provider.Web.VideoTimeout.Value() > 2*time.Hour {
		return errors.New("provider.web 上游超时配置无效")
	}
	if c.Provider.Web.MediaConcurrency < 1 || c.Provider.Web.MediaConcurrency > 64 {
		return errors.New("provider.web 媒体并发必须在 1 到 64 之间")
	}
	consoleURL, err := url.ParseRequestURI(strings.TrimSpace(c.Provider.Console.BaseURL))
	if err != nil || consoleURL.Scheme != "https" || consoleURL.Host == "" || consoleURL.User != nil {
		return errors.New("provider.console.baseURL 必须是无凭据的 HTTPS URL")
	}
	if userAgent := strings.TrimSpace(c.Provider.Console.UserAgent); len(userAgent) < 1 || len(userAgent) > 512 {
		return errors.New("provider.console.userAgent 长度必须在 1 到 512 个字符之间")
	}
	if c.Provider.Console.ChatTimeout.Value() < 5*time.Second || c.Provider.Console.ChatTimeout.Value() > 30*time.Minute {
		return errors.New("provider.console.chatTimeout 必须在 5 秒到 30 分钟之间")
	}
	if c.Batch.ImportConcurrency < 1 || c.Batch.ImportConcurrency > 50 ||
		c.Batch.ConversionConcurrency < 1 || c.Batch.ConversionConcurrency > 50 ||
		c.Batch.SyncConcurrency < 1 || c.Batch.SyncConcurrency > 50 ||
		c.Batch.RefreshConcurrency < 1 || c.Batch.RefreshConcurrency > 50 {
		return errors.New("批量任务并发必须在 1 到 50 之间")
	}
	if c.Batch.DBBuffer.Enabled {
		d := strings.ToLower(strings.TrimSpace(c.Batch.DBBuffer.Driver))
		if d != "redis" && d != "sqlite" {
			return errors.New("batch.dbBuffer.driver 必须是 redis 或 sqlite")
		}
		if d == "sqlite" && strings.TrimSpace(c.Batch.DBBuffer.Path) == "" {
			return errors.New("batch.dbBuffer.path 开启 sqlite buffer 时不能为空")
		}
	}
	if c.Batch.RandomDelay.Value() < 0 || c.Batch.RandomDelay.Value() > 5*time.Second {
		return errors.New("批量任务随机延迟必须在 0 到 5 秒之间")
	}
	if c.Provider.Web.RecoveryBackoffBase.Value() < 5*time.Second || c.Provider.Web.RecoveryBackoffMax.Value() < c.Provider.Web.RecoveryBackoffBase.Value() || c.Provider.Web.RecoveryBackoffMax.Value() > 6*time.Hour {
		return errors.New("provider.web 恢复退避配置无效")
	}
	if c.Routing.StickyTTL.Value() <= 0 || c.Routing.StickyTTL.Value() > maxRoutingTTL || c.Routing.CooldownBase.Value() <= 0 || c.Routing.CooldownMax.Value() < c.Routing.CooldownBase.Value() || c.Routing.CooldownMax.Value() > maxRoutingCooldown || c.Routing.CapacityWait.Value() <= 0 || c.Routing.CapacityWait.Value() > 5*time.Second || c.Routing.MaxAttempts < 1 || c.Routing.MaxAttempts > 10 {
		return fmt.Errorf("routing 参数无效")
	}
	if c.Routing.PromptCacheAffinity.TTL.Value() < 0 || c.Routing.PromptCacheAffinity.TTL.Value() > 30*24*time.Hour {
		return errors.New("routing 配置无效")
	}
	if c.Routing.ReasoningReplayTTL.Value() <= 0 || c.Routing.ReasoningReplayTTL.Value() > 24*time.Hour {
		return errors.New("routing.reasoningReplayTTL 必须在 1 纳秒到 24 小时之间")
	}
	if c.Routing.ReasoningReplayMaxEntries < 100 || c.Routing.ReasoningReplayMaxEntries > 1000000 {
		return errors.New("routing.reasoningReplayMaxEntries 必须在 100 到 1000000 之间")
	}
	if c.Audit.BufferSize < 1 || c.Audit.BufferSize > maxAuditBufferSize || c.Audit.BatchSize < 1 || c.Audit.BatchSize > maxAuditBatchSize || c.Audit.BatchSize > c.Audit.BufferSize || c.Audit.FlushInterval.Value() < minAuditFlushInterval || c.Audit.FlushInterval.Value() > maxAuditFlushInterval {
		return errors.New("audit 队列和批量写入配置无效")
	}
	if c.ClientKeyDefaults.RPMLimit < 1 || c.ClientKeyDefaults.RPMLimit > clientkeydomain.MaxRPMLimit || c.ClientKeyDefaults.MaxConcurrent < 1 || c.ClientKeyDefaults.MaxConcurrent > clientkeydomain.MaxConcurrent {
		return errors.New("clientKeyDefaults 超出允许范围")
	}
	if err := validateAPIKeyHeaders(c.Auth.APIKeyHeaders); err != nil {
		return err
	}
	return nil
}

// validateAPIKeyHeaders checks optional custom client-key header names.
// Built-in Authorization / X-API-Key need not be listed.
func validateAPIKeyHeaders(headers []string) error {
	if len(headers) > 16 {
		return errors.New("auth.apiKeyHeaders 最多 16 个")
	}
	seen := make(map[string]struct{}, len(headers))
	for _, raw := range headers {
		name := strings.TrimSpace(raw)
		if name == "" {
			return errors.New("auth.apiKeyHeaders 不能包含空名称")
		}
		if len(name) > 64 {
			return errors.New("auth.apiKeyHeaders 名称过长")
		}
		lower := strings.ToLower(name)
		if lower == "authorization" || lower == "x-api-key" {
			return errors.New("auth.apiKeyHeaders 无需重复配置 Authorization 或 X-API-Key")
		}
		if !isHTTPHeaderToken(name) {
			// Do not echo the raw header name into errors (may surface in logs).
			return errors.New("auth.apiKeyHeaders 含非法头名")
		}
		if _, ok := seen[lower]; ok {
			return errors.New("auth.apiKeyHeaders 存在重复项")
		}
		seen[lower] = struct{}{}
	}
	return nil
}

// isHTTPHeaderToken matches RFC 7230 token characters for header field-names.
func isHTTPHeaderToken(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		// tchar = "!" / "#" / "$" / "%" / "&" / "'" / "*" / "+" / "-" / "." /
		//  "^" / "_" / "`" / "|" / "~" / DIGIT / ALPHA
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '!' || c == '#' || c == '$' || c == '%' || c == '&' || c == '\'' ||
			c == '*' || c == '+' || c == '-' || c == '.' || c == '^' || c == '_' ||
			c == '`' || c == '|' || c == '~':
		default:
			return false
		}
	}
	return true
}

func defaultConfig() Config {
	return Config{
		Server: ServerConfig{
			Listen:                "127.0.0.1:8000",
			MaxBodyBytes:          32 << 20,
			MaxConcurrentRequests: 1024,
			ReadTimeout:           Duration(15 * time.Minute),
			RequestTimeout:        Duration(2 * time.Hour),
		},
		Frontend: FrontendConfig{PublicAPIBaseURL: DefaultPublicAPIBaseURL, StaticPath: "./frontend/dist"},
		Database: DatabaseConfig{
			Driver:   "sqlite",
			SQLite:   SQLiteDatabaseConfig{Path: "./data/backend.db"},
			Postgres: PostgresDatabaseConfig{MaxOpenConns: 12, MaxIdleConns: 4},
		},
		RuntimeStore: RuntimeStoreConfig{
			Driver: "memory",
			Redis:  RedisRuntimeConfig{Address: "127.0.0.1:6379", KeyPrefix: "grok2api:"},
		},
		Auth: AuthConfig{
			AccessTokenTTL:  Duration(15 * time.Minute),
			RefreshTokenTTL: Duration(30 * 24 * time.Hour),
			APIKeyHeaders:   nil,
		},
		Provider: ProviderConfig{
			Build: BuildProviderConfig{
				BaseURL: "https://cli-chat-proxy.grok.com/v1", FallbackBaseURL: DefaultBuildFallbackBaseURL,
				ClientVersion: RecommendedBuildClientVersion,
				ClientIdentifier: "grok-shell", TokenAuth: "xai-grok-cli",
				UserAgent: RecommendedBuildUserAgent,
			},
			Web: WebProviderConfig{
				// Default: pure-Go local x-statsig-id (no remote signer process).
				BaseURL: "https://grok.com", StatsigMode: StatsigModeLocal,
				QuotaTimeout: Duration(25 * time.Second),
				ChatTimeout:  Duration(2 * time.Minute), ImageTimeout: Duration(3 * time.Minute),
				VideoTimeout:     Duration(15 * time.Minute),
				MediaConcurrency: 4, RecoveryBackoffBase: Duration(30 * time.Second),
				RecoveryBackoffMax: Duration(30 * time.Minute),
			},
			Console: ConsoleProviderConfig{
				BaseURL: "https://console.x.ai", UserAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36",
				ChatTimeout: Duration(5 * time.Minute),
			},
		},
		Batch: BatchConfig{
			// Keep bulk workers modest so small Postgres plans (e.g. Aiven) still have headroom.
			// Conversion can be DB heavy (multiple Gets + links per account).
			ImportConcurrency: 5, ConversionConcurrency: 3, SyncConcurrency: 5,
			RefreshConcurrency: 5, RandomDelay: Duration(500 * time.Millisecond),
			DBBuffer: DBBufferConfig{Enabled: false, Driver: "none", Path: ""},
		},
		Media: MediaConfig{
			Driver: "local", MaxImageBytes: 32 << 20, MaxTotalBytes: 1 << 30,
			CleanupThresholdPercent: 80, CleanupInterval: Duration(10 * time.Minute),
			Local: LocalMediaConfig{Path: "./data/media"},
		},
		Routing: RoutingConfig{
			StickyTTL:         Duration(time.Hour),
			CooldownBase:      Duration(30 * time.Second),
			CooldownMax:       Duration(30 * time.Minute),
			CapacityWait:      Duration(500 * time.Millisecond),
			MaxAttempts:       3,
			RetryStatusCodes:  append([]int(nil), DefaultRetryStatusCodes...),
			RetryServerErrors: true,
			PromptCacheAffinity: PromptCacheAffinityConfig{
				Enabled:     true,
				Fingerprint: true,
				Expire:      true,
				TTL:         Duration(24 * time.Hour),
			},
			ReasoningReplayEnabled:    true,
			ReasoningReplayTTL:        Duration(time.Hour),
			ReasoningReplayMaxEntries: 10240,
		},
		Audit:             AuditConfig{BufferSize: 16384, BatchSize: 256, FlushInterval: Duration(250 * time.Millisecond)},
		ClientKeyDefaults: ClientKeyDefaultsConfig{RPMLimit: clientkeydomain.DefaultRPMLimit, MaxConcurrent: clientkeydomain.DefaultMaxConcurrent},
	}
}

func validStatsigID(value string) bool {
	value = strings.TrimSpace(value)
	decoded, err := base64.RawStdEncoding.DecodeString(value)
	if err != nil {
		decoded, err = base64.StdEncoding.DecodeString(value)
	}
	return err == nil && len(decoded) == 70
}

func validCredentialEncryptionKey(value string) bool {
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(value))
	return err == nil && len(decoded) == 32
}

func isExampleSecret(value string) bool {
	switch strings.TrimSpace(value) {
	case "replace-with-at-least-32-characters", "replace-with-base64-key", "replace-with-a-strong-password":
		return true
	default:
		return false
	}
}
