// Package tokencount provides a lightweight, dependency-free token estimator
// for providers that do not return official usage (notably Grok Web).
//
// The heuristic intentionally overestimates slightly rather than under-counting:
// Latin text ≈ 1 token / 4 runes, CJK ≈ 1 token / rune, punctuation/digits ≈ 1.
// This is good enough for billing reservation, audit dashboards, and OpenAI-
// compatible usage fields when the upstream stream has no usage block.
package tokencount

import (
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf8"
)

// EstimateText estimates tokens for free-form text.
func EstimateText(value string) int64 {
	if value == "" {
		return 0
	}
	var latin, cjk, other int64
	for _, r := range value {
		switch {
		case r <= 0x7F:
			if unicode.IsSpace(r) {
				continue
			}
			latin++
		case unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul):
			cjk++
		default:
			other++
		}
	}
	// ~4 Latin chars per token; CJK denser; keep a small floor for non-empty text.
	tokens := (latin+3)/4 + cjk + (other+1)/2
	if tokens < 1 {
		return 1
	}
	return tokens
}

// EstimateBytes estimates tokens for opaque byte payloads without full UTF-8 decode cost on huge bodies.
func EstimateBytes(value []byte) int64 {
	if len(value) == 0 {
		return 0
	}
	if utf8.Valid(value) {
		return EstimateText(string(value))
	}
	return max(1, int64((len(value)+2)/3))
}

// EstimateJSON walks a decoded JSON value and estimates tokens, collapsing
// data:image / data:video blobs to a fixed cost so base64 media does not explode counts.
func EstimateJSON(value any) int64 {
	switch typed := value.(type) {
	case map[string]any:
		var total int64
		for key, child := range typed {
			total += EstimateText(key) + 1 + EstimateJSON(child)
		}
		return total
	case []any:
		var total int64
		for _, child := range typed {
			total += 1 + EstimateJSON(child)
		}
		return total
	case string:
		trimmed := strings.TrimSpace(typed)
		if strings.HasPrefix(trimmed, "data:image/") || strings.HasPrefix(trimmed, "data:video/") {
			return 256
		}
		return max(1, EstimateText(typed))
	case json.Number, float64, bool:
		return 1
	case nil:
		return 0
	default:
		encoded, _ := json.Marshal(typed)
		return max(1, EstimateBytes(encoded))
	}
}

// EstimateRequestBody estimates input tokens from a raw JSON request body.
func EstimateRequestBody(body []byte) int64 {
	var payload any
	if json.Unmarshal(body, &payload) != nil {
		return max(256, EstimateBytes(body))
	}
	return max(256, EstimateJSON(payload)+128)
}
