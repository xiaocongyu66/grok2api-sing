package config

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
	"time"

	clientkeydomain "github.com/chenyme/grok2api/backend/internal/domain/clientkey"
)

func TestLoadDurationAndSecretsFromYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	data := []byte(`server:
  requestTimeout: 2m
secrets:
  jwtSecret: "12345678901234567890123456789012"
  credentialEncryptionKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
bootstrapAdmin:
  username: "admin"
  password: "password123"
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.RequestTimeout.Value() != 2*time.Minute {
		t.Fatalf("requestTimeout = %s", cfg.Server.RequestTimeout.Value())
	}
	if cfg.Server.ReadTimeout.Value() != 15*time.Minute {
		t.Fatalf("readTimeout = %s", cfg.Server.ReadTimeout.Value())
	}
	if cfg.Secrets.JWTSecret != "12345678901234567890123456789012" {
		t.Fatalf("jwtSecret = %q", cfg.Secrets.JWTSecret)
	}
	if cfg.Secrets.CredentialEncryptionKey != "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=" {
		t.Fatalf("credentialEncryptionKey = %q", cfg.Secrets.CredentialEncryptionKey)
	}
	if cfg.BootstrapAdmin.Username != "admin" || cfg.BootstrapAdmin.Password != "password123" {
		t.Fatalf("bootstrapAdmin = %#v", cfg.BootstrapAdmin)
	}
	if cfg.Batch.ImportConcurrency != 10 || cfg.Batch.ConversionConcurrency != 10 || cfg.Batch.SyncConcurrency != 10 || cfg.Batch.RefreshConcurrency != 8 || cfg.Batch.RandomDelay.Value() != 500*time.Millisecond {
		t.Fatalf("batch defaults = %#v", cfg.Batch)
	}
	expectedDatabasePath := filepath.Join(dir, "data", "backend.db")
	if cfg.Database.SQLite.Path != expectedDatabasePath {
		t.Fatalf("database path = %q, want %q", cfg.Database.SQLite.Path, expectedDatabasePath)
	}
	expectedMediaPath := filepath.Join(dir, "data", "media")
	if cfg.Media.Local.Path != expectedMediaPath {
		t.Fatalf("media path = %q, want %q", cfg.Media.Local.Path, expectedMediaPath)
	}
	expectedFrontendPath := filepath.Join(dir, "frontend", "dist")
	if cfg.Frontend.StaticPath != expectedFrontendPath {
		t.Fatalf("frontend static path = %q, want %q", cfg.Frontend.StaticPath, expectedFrontendPath)
	}
}

func TestDefaultGrokBuildClientVersionMatchesLocalBaseline(t *testing.T) {
	build := defaultConfig().Provider.Build
	if RecommendedBuildClientVersion != "0.2.101" {
		t.Fatalf("recommended clientVersion = %q", RecommendedBuildClientVersion)
	}
	if build.ClientVersion != RecommendedBuildClientVersion {
		t.Fatalf("clientVersion = %q", build.ClientVersion)
	}
	if RecommendedBuildUserAgent != "grok-shell/0.2.101 (linux; x86_64)" {
		t.Fatalf("recommended userAgent = %q", RecommendedBuildUserAgent)
	}
	if build.UserAgent != RecommendedBuildUserAgent {
		t.Fatalf("userAgent = %q", build.UserAgent)
	}
}

func TestDefaultConsoleProviderConfig(t *testing.T) {
	console := defaultConfig().Provider.Console
	if console.BaseURL != "https://console.x.ai" || console.UserAgent == "" || console.ChatTimeout.Value() != 5*time.Minute {
		t.Fatalf("console defaults = %#v", console)
	}
}

func TestLoadAcceptsRuntimeDefaultsAndRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	data := []byte(`secrets:
  jwtSecret: "12345678901234567890123456789012"
  credentialEncryptionKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
routing:
  maxAttempts: 9
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil || cfg.Routing.MaxAttempts != 9 {
		t.Fatalf("runtime defaults = %#v, err = %v", cfg.Routing, err)
	}
	data = append(data, []byte("unknownField: true\n")...)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("unknown field was accepted")
	}
}

func TestLoadRejectsMediaRuntimeSettingsInYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	data := []byte(`secrets:
  jwtSecret: "12345678901234567890123456789012"
  credentialEncryptionKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
media:
  driver: local
  maxTotalBytes: 1073741824
  local:
    path: "./data/media"
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("hot-reloadable media setting was accepted from YAML")
	}
}

func TestDurationStringUsesCompactYAMLForm(t *testing.T) {
	tests := map[time.Duration]string{
		250 * time.Millisecond: "250ms",
		30 * time.Second:       "30s",
		30 * time.Minute:       "30m",
		time.Hour:              "1h",
		90 * time.Minute:       "1h30m",
	}
	for value, expected := range tests {
		if actual := (Duration(value)).String(); actual != expected {
			t.Fatalf("Duration(%s).String() = %q, want %q", value, actual, expected)
		}
	}
}

func TestValidateRejectsUnsafeRuntimeLimits(t *testing.T) {
	tests := map[string]func(*Config){
		"request body": func(cfg *Config) { cfg.Server.MaxBodyBytes = maxServerBodyBytes + 1 },
		"audit buffer": func(cfg *Config) { cfg.Audit.BufferSize = maxAuditBufferSize + 1 },
		"client rpm":   func(cfg *Config) { cfg.ClientKeyDefaults.RPMLimit = clientkeydomain.MaxRPMLimit + 1 },
		"image size":   func(cfg *Config) { cfg.Media.MaxImageBytes = 33 << 20 },
		"media total":  func(cfg *Config) { cfg.Media.MaxTotalBytes = 1 },
		"batch limit":  func(cfg *Config) { cfg.Batch.SyncConcurrency = 51 },
		"batch jitter": func(cfg *Config) { cfg.Batch.RandomDelay = Duration(6 * time.Second) },
		"console url":  func(cfg *Config) { cfg.Provider.Console.BaseURL = "http://console.x.ai" },
		"console ua":   func(cfg *Config) { cfg.Provider.Console.UserAgent = "" },
		"console timeout": func(cfg *Config) {
			cfg.Provider.Console.ChatTimeout = Duration(time.Second)
		},
		"api key header empty": func(cfg *Config) { cfg.Auth.APIKeyHeaders = []string{" "} },
		"api key header auth":  func(cfg *Config) { cfg.Auth.APIKeyHeaders = []string{"Authorization"} },
		"api key header bad":   func(cfg *Config) { cfg.Auth.APIKeyHeaders = []string{"bad header"} },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := defaultConfig()
			cfg.Secrets.JWTSecret = "12345678901234567890123456789012"
			cfg.Secrets.CredentialEncryptionKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
			mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("unsafe configuration was accepted")
			}
		})
	}
}

func TestValidateRejectsExampleSecrets(t *testing.T) {
	base := defaultConfig()
	base.Secrets.JWTSecret = "12345678901234567890123456789012"
	base.Secrets.CredentialEncryptionKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

	tests := map[string]func(*Config){
		"example jwt":            func(cfg *Config) { cfg.Secrets.JWTSecret = "replace-with-at-least-32-characters" },
		"invalid encryption key": func(cfg *Config) { cfg.Secrets.CredentialEncryptionKey = "not-a-32-byte-base64-key" },
		"example admin password": func(cfg *Config) { cfg.BootstrapAdmin.Password = "replace-with-a-strong-password" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := base
			mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("unsafe configuration was accepted")
			}
		})
	}
}

func TestValidateStatsigModes(t *testing.T) {
	base := defaultConfig()
	base.Secrets.JWTSecret = "12345678901234567890123456789012"
	base.Secrets.CredentialEncryptionKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

	manual := base
	manual.Provider.Web.StatsigMode = StatsigModeManual
	manual.Provider.Web.StatsigManualValue = base64.RawStdEncoding.EncodeToString(make([]byte, 70))
	if err := manual.Validate(); err != nil {
		t.Fatalf("valid manual Statsig rejected: %v", err)
	}
	manual.Provider.Web.StatsigManualValue = "invalid"
	if err := manual.Validate(); err == nil {
		t.Fatal("invalid manual Statsig was accepted")
	}

	remote := base
	remote.Provider.Web.StatsigMode = StatsigModeURL
	remote.Provider.Web.StatsigSignerURL = "http://grok-signer-go:8788/sign"
	if err := remote.Validate(); err != nil {
		t.Fatalf("Docker internal Statsig signer rejected: %v", err)
	}
	remote.Provider.Web.StatsigSignerURL = "http://signer.example.com:8788/sign"
	if err := remote.Validate(); err == nil {
		t.Fatal("public plaintext Statsig signer URL was accepted")
	}
	remote.Provider.Web.StatsigSignerURL = DefaultStatsigSignerURL
	if err := remote.Validate(); err == nil {
		t.Fatal("third-party default Statsig signer URL was accepted without normalize")
	}
	remote.Provider.Web.StatsigSignerURL = ""
	if err := remote.Validate(); err == nil {
		t.Fatal("empty Statsig signer URL was accepted without normalize")
	}
	// Normalize migrates legacy/empty/placeholder URL modes to pure-Go local signing.
	legacy := base
	legacy.Provider.Web.StatsigMode = StatsigModeURL
	legacy.Provider.Web.StatsigSignerURL = DefaultStatsigSignerURL
	NormalizeLegacyStatsig(&legacy)
	if legacy.Provider.Web.StatsigMode != StatsigModeLocal {
		t.Fatalf("legacy mode not migrated to local: %q", legacy.Provider.Web.StatsigMode)
	}
	if err := legacy.Validate(); err != nil {
		t.Fatalf("normalized legacy Statsig rejected: %v", err)
	}
	empty := base
	empty.Provider.Web.StatsigMode = StatsigModeURL
	empty.Provider.Web.StatsigSignerURL = ""
	NormalizeLegacyStatsig(&empty)
	if empty.Provider.Web.StatsigMode != StatsigModeLocal {
		t.Fatalf("empty signer not migrated to local: %q", empty.Provider.Web.StatsigMode)
	}
	if err := empty.Validate(); err != nil {
		t.Fatalf("normalized empty Statsig rejected: %v", err)
	}
	local := base
	local.Provider.Web.StatsigMode = StatsigModeLocal
	if err := local.Validate(); err != nil {
		t.Fatalf("local Statsig rejected: %v", err)
	}
	blankMode := base
	blankMode.Provider.Web.StatsigMode = ""
	NormalizeLegacyStatsig(&blankMode)
	if blankMode.Provider.Web.StatsigMode != StatsigModeLocal {
		t.Fatalf("blank mode not defaulted to local: %q", blankMode.Provider.Web.StatsigMode)
	}
}

func TestValidateInfrastructureDrivers(t *testing.T) {
	base := defaultConfig()
	base.Secrets.JWTSecret = "12345678901234567890123456789012"
	base.Secrets.CredentialEncryptionKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

	postgresRedis := base
	postgresRedis.Database.Driver = "postgres"
	postgresRedis.Database.Postgres.DSN = "postgres://user:password@127.0.0.1:5432/grok2api"
	postgresRedis.RuntimeStore.Driver = "redis"
	if err := postgresRedis.Validate(); err != nil {
		t.Fatalf("valid postgres + redis configuration rejected: %v", err)
	}

	invalidDatabase := base
	invalidDatabase.Database.Driver = "mysql"
	if err := invalidDatabase.Validate(); err == nil {
		t.Fatal("unsupported database driver was accepted")
	}

	invalidRuntime := base
	invalidRuntime.RuntimeStore.Driver = "fallback"
	if err := invalidRuntime.Validate(); err == nil {
		t.Fatal("unsupported runtime store driver was accepted")
	}
}

func TestValidateFrontendPublicAPIBaseURL(t *testing.T) {
	cfg := defaultConfig()
	cfg.Secrets.JWTSecret = "12345678901234567890123456789012"
	cfg.Secrets.CredentialEncryptionKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	for _, value := range []string{"127.0.0.1:8000", "ftp://example.com", "https://user@example.com", "https://example.com?token=value"} {
		cfg.Frontend.PublicAPIBaseURL = value
		if err := cfg.Validate(); err == nil {
			t.Fatalf("frontend.publicApiBaseURL %q was accepted", value)
		}
	}
	cfg.Frontend.PublicAPIBaseURL = "https://api.example.com/grok2api"
	cfg.Auth.SecureCookies = false
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid frontend.publicApiBaseURL rejected: %v", err)
	}
}

func TestEffectivePublicAPIBaseURLPriority(t *testing.T) {
	cases := []struct {
		name     string
		frontend FrontendConfig
		want     string
	}{
		{name: "runtime override", frontend: FrontendConfig{PublicAPIBaseURL: "https://yaml.example/base", PublicAPIBaseURLOverride: "https://runtime.example/api/"}, want: "https://runtime.example/api"},
		{name: "yaml fallback", frontend: FrontendConfig{PublicAPIBaseURL: "https://yaml.example/base/"}, want: "https://yaml.example/base"},
		{name: "local fallback", frontend: FrontendConfig{}, want: DefaultPublicAPIBaseURL},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.frontend.EffectivePublicAPIBaseURL(); got != tc.want {
				t.Fatalf("EffectivePublicAPIBaseURL() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestValidateAcceptsCustomAPIKeyHeaders(t *testing.T) {
	cfg := defaultConfig()
	cfg.Secrets.JWTSecret = "12345678901234567890123456789012"
	cfg.Secrets.CredentialEncryptionKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	cfg.Auth.APIKeyHeaders = []string{"congyu_15fc", "My-Key"}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("custom apiKeyHeaders rejected: %v", err)
	}
}
