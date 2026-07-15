package egress

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	domain "github.com/chenyme/grok2api/backend/internal/domain/egress"
	"github.com/chenyme/grok2api/backend/internal/infra/security"
	"github.com/chenyme/grok2api/backend/internal/repository"
)

func TestSanitizeCloudflareCookiesDropsControlsAndNonCloudflareValues(t *testing.T) {
	value := SanitizeCloudflareCookies("CF_CLEARANCE=valid; __cf_bm=bad\r\nX-Leak: yes; sso=secret; cf_chl_test=ok")
	if value != "cf_clearance=valid; cf_chl_test=ok" {
		t.Fatalf("sanitized cookies = %q", value)
	}
	if strings.Contains(strings.ToLower(value), "sso") || strings.Contains(value, "\r") || strings.Contains(value, "\n") {
		t.Fatalf("unsafe cookie value = %q", value)
	}
}

func TestNormalizeProxyURLValidatesStructure(t *testing.T) {
	for _, raw := range []string{
		"http://user:password@127.0.0.1:8080", "https://proxy.example:8443",
		"socks4://127.0.0.1:1080", "socks4a://proxy.example:1080",
		"socks5://user:password@127.0.0.1:1080", "socks5h://user:password@proxy.example:1080",
		"vless://11111111-1111-1111-1111-111111111111@1.2.3.4:443?security=tls&type=ws&path=%2F",
		"trojan://secret@1.2.3.4:443?sni=example.com",
		"hysteria2://secret@1.2.3.4:443?sni=example.com",
		"hy2://secret@1.2.3.4:443",
		"tuic://11111111-1111-1111-1111-111111111111:pass@1.2.3.4:443?sni=example.com",
		"ss://YWVzLTI1Ni1nY206cGFzc3dvcmQ@1.2.3.4:8388",
		"vmess://eyJhZGQiOiIxLjIuMy40In0=",
		`{"type":"socks","tag":"p","server":"127.0.0.1","server_port":1080}`,
	} {
		value, err := NormalizeProxyURL(raw)
		if err != nil || value == "" {
			t.Fatalf("valid proxy %q = %q, err = %v", raw, value, err)
		}
	}
	for _, invalid := range []string{"file:///tmp/proxy", "https://", "http://proxy.example/path", "http://proxy.example\r\nX-Leak: yes", "vmess://"} {
		if _, err := NormalizeProxyURL(invalid); err == nil {
			t.Fatalf("invalid proxy accepted: %q", invalid)
		}
	}
}

func TestServiceRejectsRemovedAllScope(t *testing.T) {
	service := &Service{}
	_, err := service.applyInput(domain.Node{}, Input{Name: "legacy", Scope: domain.Scope("all"), Enabled: true}, true)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("all scope error = %v", err)
	}
}

func TestBuildNodeAlwaysUsesProviderUserAgent(t *testing.T) {
	cipher, err := security.NewCipher("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(nil, cipher, "web-agent", "console-agent")
	value, err := service.applyInput(domain.Node{UserAgent: "legacy-build-agent"}, Input{
		Name: "build", Scope: domain.ScopeBuild, Enabled: true, UserAgent: "custom-build-agent",
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if value.UserAgent != "" || publicNode(value).UserAgent != "" {
		t.Fatalf("build node userAgent = %q", value.UserAgent)
	}
	if defaults := service.DefaultUserAgents(); defaults[string(domain.ScopeBuild)] != "" || defaults[string(domain.ScopeWeb)] != "web-agent" || defaults[string(domain.ScopeConsole)] != "console-agent" {
		t.Fatalf("default user agents = %#v", defaults)
	}
}

func TestConsoleNodeUsesConsoleDefaultUserAgent(t *testing.T) {
	cipher, err := security.NewCipher("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(nil, cipher, "web-agent", "console-agent")
	value, err := service.applyInput(domain.Node{}, Input{Name: "console", Scope: domain.ScopeConsole, Enabled: true}, true)
	if err != nil {
		t.Fatal(err)
	}
	if value.UserAgent != "console-agent" {
		t.Fatalf("console node userAgent = %q", value.UserAgent)
	}
}

type stubRuntime struct {
	success, failure int64
	inflight         int
}

func (s stubRuntime) RuntimeStats(uint64) (int64, int64, int, *time.Time, *bool, int64, string) {
	return s.success, s.failure, s.inflight, nil, nil, 0, ""
}
func (stubRuntime) ProbeNode(context.Context, uint64) (domain.ProbeResult, error) {
	return domain.ProbeResult{}, nil
}
func (stubRuntime) ProbeAll(context.Context, domain.Scope) ([]domain.ProbeResult, error) {
	return nil, nil
}

func TestPublicNodeEnrichesRuntimeRates(t *testing.T) {
	service := NewService(nil, nil, "", "")
	service.SetRuntime(stubRuntime{success: 8, failure: 2, inflight: 1})
	node := service.publicNode(domain.Node{ID: 7, Name: "p", Scope: domain.ScopeBuild, Enabled: true, Health: 1})
	if node.SuccessCount != 8 || node.RequestCount != 10 || node.Inflight != 1 {
		t.Fatalf("counts = success=%d request=%d inflight=%d", node.SuccessCount, node.RequestCount, node.Inflight)
	}
	if node.SuccessRate != 0.8 || node.FailureRate != 0.2 {
		t.Fatalf("rates = success=%v failure=%v", node.SuccessRate, node.FailureRate)
	}
}

func TestSortPublicNodesBySuccessRate(t *testing.T) {
	values := []domain.PublicNode{
		{ID: 1, SuccessRate: 0.2},
		{ID: 2, SuccessRate: 0.9},
		{ID: 3, SuccessRate: 0.5},
	}
	sortPublicNodes(values, repository.SortQuery{Field: "successRate", Direction: repository.SortDescending})
	if values[0].ID != 2 || values[2].ID != 1 {
		t.Fatalf("sorted = %#v", values)
	}
}
