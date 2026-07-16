package clientid

import "testing"

func TestDetectClaudeCode(t *testing.T) {
	if got := Detect("claude-code/2.1.0", nil); got != ClaudeCode {
		t.Fatalf("ua = %s", got)
	}
	if got := Detect("Mozilla/5.0", map[string]string{"x-claude-code-session-id": "sess"}); got != ClaudeCode {
		t.Fatalf("header = %s", got)
	}
	if got := Detect("", map[string]string{"anthropic-version": "2023-06-01"}); got != ClaudeCode {
		t.Fatalf("anthropic-version empty ua = %s", got)
	}
	if got := Detect("node", map[string]string{"anthropic-version": "2023-06-01", "anthropic-beta": "claude-code-20250219"}); got != ClaudeCode {
		t.Fatalf("beta = %s", got)
	}
}

func TestDetectCodex(t *testing.T) {
	if got := Detect("openai-codex/0.1", nil); got != Codex {
		t.Fatalf("ua = %s", got)
	}
	if got := Detect("codex_cli_rs/0.50.0", nil); got != Codex {
		t.Fatalf("codex_cli_rs = %s", got)
	}
	if got := Detect("", map[string]string{"x-codex-session-id": "x"}); got != Codex {
		t.Fatalf("header = %s", got)
	}
	if got := Detect("reqwest/0.11", map[string]string{"originator": "codex_cli_rs"}); got != Codex {
		t.Fatalf("originator = %s", got)
	}
}

func TestDetectHermesOpenCodeGrok(t *testing.T) {
	if got := Detect("hermes-agent/1.0", nil); got != Hermes {
		t.Fatalf("hermes = %s", got)
	}
	if got := Detect("opencode/0.9", nil); got != OpenCode {
		t.Fatalf("opencode = %s", got)
	}
	if got := Detect("opencode/1.0.0 linux", nil); got != OpenCode {
		t.Fatalf("opencode full = %s", got)
	}
	if got := Detect("grok-cli/1.0", nil); got != GrokCLI {
		t.Fatalf("grok = %s", got)
	}
}

func TestDetectUnknownAndLegacy(t *testing.T) {
	if got := Detect("", nil); got != Unknown {
		t.Fatalf("empty = %s", got)
	}
	if got := Detect("SomeCustomBot/1.0", nil); got != Unknown {
		t.Fatalf("custom = %s", got)
	}
	if got := NormalizeStored(""); got != Legacy {
		t.Fatalf("normalize empty = %s", got)
	}
	if Label(Legacy) != "历史请求" {
		t.Fatalf("legacy label = %s", Label(Legacy))
	}
}

func TestDetectLanguageRuntimes(t *testing.T) {
	if got := Detect("python-requests/2.31.0", nil); got != PythonHTTP {
		t.Fatalf("python = %s", got)
	}
	if got := Detect("Go-http-client/2.0", nil); got != GoHTTP {
		t.Fatalf("go = %s", got)
	}
	if got := Detect("axios/1.6.0", nil); got != NodeHTTP {
		t.Fatalf("axios = %s", got)
	}
}

func TestDetectProductHeadersAndGo(t *testing.T) {
	if got := Detect("", map[string]string{"x-client-name": "sharkey"}); got != OpenAISDK {
		t.Fatalf("x-client-name sharkey = %s", got)
	}
	if got := Detect("", map[string]string{"x-client-name": "grok-cli"}); got != GrokCLI {
		t.Fatalf("x-client-name grok = %s", got)
	}
	if got := Detect("Go-http-client/2.0", nil); got != GoHTTP {
		t.Fatalf("go ua = %s", got)
	}
	if got := Detect("Misskey/2025.5.2", nil); got != OpenAISDK {
		t.Fatalf("misskey = %s", got)
	}
	if got := Detect("SomeBot/1.0", nil); got != Unknown {
		t.Fatalf("unknown = %s", got)
	}
}
