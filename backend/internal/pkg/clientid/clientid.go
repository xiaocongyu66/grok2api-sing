// Package clientid classifies downstream API callers from User-Agent and session headers.
package clientid

import (
	"strings"
)

// Known client type IDs stored on request audits and shown on the dashboard.
const (
	ClaudeCode = "claude_code"
	Codex      = "codex"
	GrokCLI    = "grok_cli"
	Hermes     = "hermes"
	OpenCode   = "opencode"
	Cline      = "cline"
	Cursor     = "cursor"
	Continue   = "continue"
	Aider      = "aider"
	RooCode    = "roo_code"
	Windsurf   = "windsurf"
	OpenAISDK  = "openai_sdk"
	Anthropic  = "anthropic_sdk"
	NodeHTTP   = "node"
	PythonHTTP = "python"
	GoHTTP     = "go"
	JavaHTTP   = "java"
	RustHTTP   = "rust"
	Curl       = "curl"
	// Legacy is empty client_type on audits written before client detection existed.
	Legacy  = "legacy"
	Unknown = "unknown"
)

// Labels maps stable IDs to short dashboard labels (codex:60 style).
var Labels = map[string]string{
	ClaudeCode: "Claude Code",
	Codex:      "Codex",
	GrokCLI:    "Grok CLI",
	Hermes:     "Hermes",
	OpenCode:   "OpenCode",
	Cline:      "Cline",
	Cursor:     "Cursor",
	Continue:   "Continue",
	Aider:      "Aider",
	RooCode:    "Roo Code",
	Windsurf:   "Windsurf",
	OpenAISDK:  "OpenAI SDK",
	Anthropic:  "Anthropic SDK",
	NodeHTTP:   "Node",
	PythonHTTP: "Python",
	GoHTTP:     "Go",
	JavaHTTP:   "Java",
	RustHTTP:   "Rust",
	Curl:       "curl",
	Legacy:     "历史请求",
	Unknown:    "Unknown",
}

// Detect classifies a caller from User-Agent and optional request headers.
// Headers keys should be lower-case. Empty input yields Unknown (not Legacy —
// Legacy is only for persisted empty client_type rows).
func Detect(userAgent string, headers map[string]string) string {
	ua := strings.ToLower(strings.TrimSpace(userAgent))
	if headers == nil {
		headers = map[string]string{}
	}
	originator := strings.ToLower(strings.TrimSpace(headerValue(headers, "originator")))
	xApp := strings.ToLower(strings.TrimSpace(headerValue(headers, "x-app")))
	stainlessPkg := strings.ToLower(strings.TrimSpace(headerValue(headers, "x-stainless-package-version")))
	_ = stainlessPkg

	// Explicit product / session headers (most reliable).
	if hasHeader(headers, "x-claude-code-session-id") ||
		matchAny(xApp, "claude-code", "claude_code") ||
		matchAny(headerValue(headers, "anthropic-beta"), "claude-code") {
		return ClaudeCode
	}
	if hasHeader(headers, "x-codex-window-id") || hasHeader(headers, "x-codex-session-id") ||
		matchAny(originator, "codex") ||
		matchAny(xApp, "codex") {
		return Codex
	}
	if hasHeader(headers, "x-grok-conv-id") || hasHeader(headers, "x-grok-conversation-id") {
		if matchAny(ua, "grok-cli", "grok cli", "xai-grok", "grok-shell", "xai-sdk") || ua == "" || matchAny(originator, "grok") {
			return GrokCLI
		}
	}

	// User-Agent / originator product tokens (multi-agent and IDE clients first).
	switch {
	case matchAny(ua, "claude-code", "claude-cli", "claude_code", "claude code", "@anthropic-ai/claude-code"):
		return ClaudeCode
	case matchAny(ua, "codex_cli_rs", "codex-cli", "openai-codex", "gpt-codex", "openai codex", "codex/") ||
		matchAny(originator, "codex_cli_rs", "codex-cli"):
		return Codex
	case matchAny(ua, "hermes-agent", "hermes-cli", "hermes/", "nous-hermes", "openhermes", "hermes agent"):
		return Hermes
	case matchAny(ua, "opencode", "open-code", "sst/opencode", "anomalyco/opencode"):
		return OpenCode
	case matchAny(ua, "grok-cli", "grok cli", "xai-grok", "grok-shell", "xai-sdk", "xai/"):
		return GrokCLI
	case matchAny(ua, "cline", "claude-dev", "roo-cline"):
		return Cline
	case matchAny(ua, "roo-code", "roocode", "roo code"):
		return RooCode
	case matchAny(ua, "cursor/", "cursor-", "cursor "):
		return Cursor
	case matchAny(ua, "continue/", "continue.dev", "continuedev"):
		return Continue
	case matchAny(ua, "windsurf", "codeium"):
		return Windsurf
	case matchAny(ua, "aider"):
		return Aider
	case matchAny(ua, "openai-python", "openai-node", "openai-go", "openai-java", "openai-php", "openai/") ||
		matchAny(headerValue(headers, "x-stainless-lang"), "python", "js", "node", "go", "java") && hasHeader(headers, "x-stainless-package-version"):
		// Stainless-generated OpenAI SDKs set x-stainless-* headers.
		if lang := strings.ToLower(headerValue(headers, "x-stainless-lang")); lang != "" {
			switch {
			case matchAny(lang, "python"):
				return OpenAISDK
			case matchAny(lang, "js", "javascript", "node", "typescript"):
				return OpenAISDK
			default:
				return OpenAISDK
			}
		}
		return OpenAISDK
	case matchAny(ua, "anthropic/", "anthropic-sdk", "anthropic-python", "anthropic-typescript", "@anthropic-ai/sdk"):
		return Anthropic
	// Path-ish hints from anthropic-version alone: Claude Code and many agents hit Messages API.
	// Prefer Claude Code when UA is empty/generic node and anthropic-version is present —
	// pure Anthropic SDK usually sets anthropic/ or @anthropic-ai/sdk in UA.
	case hasHeader(headers, "anthropic-version") && (ua == "" || matchAny(ua, "node", "undici", "axios", "fetch")):
		return ClaudeCode
	case matchAny(ua, "python-httpx", "python-requests", "aiohttp/", "httpx/", "python-urllib"):
		return PythonHTTP
	case matchAny(ua, "node-fetch", "undici", "axios/", "got/", "node.js", "nodejs"):
		return NodeHTTP
	case matchAny(ua, "go-http-client", "go-resty", "fasthttp"):
		return GoHTTP
	case matchAny(ua, "okhttp", "apache-httpclient", "java/"):
		return JavaHTTP
	case matchAny(ua, "reqwest", "rustls", "ureq/"):
		return RustHTTP
	case matchAny(ua, "curl/"):
		return Curl
	}
	if ua == "" {
		return Unknown
	}
	return Unknown
}

// Label returns a short human label for a client type id.
func Label(id string) string {
	if label, ok := Labels[id]; ok {
		return label
	}
	if id == "" {
		return Labels[Legacy]
	}
	return id
}

// NormalizeStored maps persisted client_type values for aggregation.
// Empty means audits written before client detection → Legacy.
func NormalizeStored(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return Legacy
	}
	return id
}

func hasHeader(headers map[string]string, name string) bool {
	return headerValue(headers, name) != ""
}

func headerValue(headers map[string]string, name string) string {
	if value := strings.TrimSpace(headers[name]); value != "" {
		return value
	}
	return strings.TrimSpace(headers[strings.ToLower(name)])
}

func matchAny(haystack string, needles ...string) bool {
	haystack = strings.ToLower(haystack)
	for _, needle := range needles {
		if needle != "" && strings.Contains(haystack, strings.ToLower(needle)) {
			return true
		}
	}
	return false
}
