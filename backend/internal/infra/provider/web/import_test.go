package web

import (
	"strings"
	"testing"

	"github.com/chenyme/grok2api/backend/internal/domain/account"
)

func TestParseImportedCredentialsAcceptsOneSSOTokenPerLine(t *testing.T) {
	adapter := &Adapter{}
	values, err := adapter.ParseImportedCredentials([]byte("token-one\nsso=token-two; other=drop\n\ntoken-one\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 2 {
		t.Fatalf("credentials = %#v", values)
	}
	if values[0].AccessToken != "token-one" || values[1].AccessToken != "token-two" {
		t.Fatalf("tokens = %q, %q", values[0].AccessToken, values[1].AccessToken)
	}
	for _, value := range values {
		if value.Provider != account.ProviderWeb || value.AuthType != account.AuthTypeSSO || value.WebTier != account.WebTierAuto {
			t.Fatalf("credential = %#v", value)
		}
	}
}

func TestParseImportedCredentialsAcceptsEmailColonToken(t *testing.T) {
	adapter := &Adapter{}
	// Matches grok-free-register keys/sso.txt: email:eyJ...
	line := "user@example.com:eyJ0eXAiOiJKV1QiLCJhbGciOiJIUzI1NiJ9.payload.sig\n" +
		"other@example.com:sso=token-with-cookie\n" +
		"eyJraw.jwt.without.email\n"
	values, err := adapter.ParseImportedCredentials([]byte(line))
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 3 {
		t.Fatalf("credentials = %#v", values)
	}
	if values[0].Email != "user@example.com" || values[0].Name != "user@example.com" {
		t.Fatalf("email row0 = %#v", values[0])
	}
	if !strings.HasPrefix(values[0].AccessToken, "eyJ") {
		t.Fatalf("token0 = %q", values[0].AccessToken)
	}
	if values[1].Email != "other@example.com" || values[1].AccessToken != "token-with-cookie" {
		t.Fatalf("email row1 = %#v", values[1])
	}
	if values[2].Email != "" || values[2].AccessToken != "eyJraw.jwt.without.email" {
		t.Fatalf("raw jwt row = %#v", values[2])
	}
}

func TestParseImportedCredentialsRejectsOversizedPlainToken(t *testing.T) {
	adapter := &Adapter{}
	_, err := adapter.ParseImportedCredentials([]byte(strings.Repeat("x", maxSSOTokenBytes+1)))
	if err == nil {
		t.Fatal("expected oversized token error")
	}
}

func TestWebCredentialJSONUsesCurrentDocumentShape(t *testing.T) {
	adapter := &Adapter{}
	values, err := adapter.ParseImportedCredentials([]byte(`{"provider":"grok_web","accounts":[{"name":"primary","sso_token":"token-one","tier":"super"}]}`))
	if err != nil || len(values) != 1 || values[0].WebTier != account.WebTierSuper {
		t.Fatalf("credentials = %#v, err = %v", values, err)
	}
	data, err := adapter.MarshalCredentials(values)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"version"`) {
		t.Fatalf("export contains version metadata: %s", data)
	}
	if _, err := adapter.ParseImportedCredentials([]byte(`{"basic":["token-one"]}`)); err == nil {
		t.Fatal("legacy tier pools were accepted")
	}
}
