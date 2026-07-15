package account

import (
	"errors"
	"fmt"
	"testing"

	accountdomain "github.com/chenyme/grok2api/backend/internal/domain/account"
	"github.com/chenyme/grok2api/backend/internal/infra/provider"
)

func TestIsRateLimitOrTransient(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "401", err: provider.ErrUnauthorized, want: false},
		{name: "429 text", err: errors.New("upstream returned 429"), want: true},
		{name: "rate limit", err: errors.New("Rate Limit exceeded"), want: true},
		{name: "timeout", err: errors.New("context deadline exceeded"), want: true},
		{name: "refresh 429", err: &provider.CredentialRefreshError{Status: 429, Code: "slow_down"}, want: true},
		{name: "refresh permanent", err: &provider.CredentialRefreshError{Status: 401, Permanent: true, Code: "bad"}, want: false},
		{name: "refresh soft", err: &provider.CredentialRefreshError{Status: 503, Permanent: false, Code: "busy"}, want: true},
		{name: "hard fail", err: fmt.Errorf("credential rejected"), want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := isRateLimitOrTransient(tc.err); got != tc.want {
				t.Fatalf("isRateLimitOrTransient(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestUniqueSSOBySourceKey(t *testing.T) {
	t.Parallel()
	items := []accountdomain.Credential{
		{ID: 1, SourceKey: "sso:aaa", Enabled: false, AuthStatus: accountdomain.AuthStatusReauthRequired},
		{ID: 2, SourceKey: "sso:aaa", Enabled: true, AuthStatus: accountdomain.AuthStatusActive},
		{ID: 3, SourceKey: "sso:bbb", Enabled: true, AuthStatus: accountdomain.AuthStatusActive},
		{ID: 4, SourceKey: "", Enabled: true, AuthStatus: accountdomain.AuthStatusActive},
		{ID: 5, SourceKey: "", Enabled: true, AuthStatus: accountdomain.AuthStatusActive},
	}
	out := uniqueSSOBySourceKey(items)
	if len(out) != 4 {
		t.Fatalf("unique count = %d, want 4 (same token collapsed, empty keys kept separate)", len(out))
	}
	// Prefer enabled+active for sso:aaa
	var aaa accountdomain.Credential
	for _, item := range out {
		if item.SourceKey == "sso:aaa" {
			aaa = item
		}
	}
	if aaa.ID != 2 {
		t.Fatalf("collapsed sso:aaa kept id=%d, want 2", aaa.ID)
	}
}

func TestBetterSSODuplicate(t *testing.T) {
	t.Parallel()
	good := accountdomain.Credential{Enabled: true, AuthStatus: accountdomain.AuthStatusActive}
	bad := accountdomain.Credential{Enabled: false, AuthStatus: accountdomain.AuthStatusReauthRequired}
	if !betterSSODuplicate(good, bad) {
		t.Fatal("enabled+active should beat reauth/disabled")
	}
	if betterSSODuplicate(bad, good) {
		t.Fatal("reauth should not beat enabled+active")
	}
}
