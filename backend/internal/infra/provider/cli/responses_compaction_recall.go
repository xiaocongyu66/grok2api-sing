package cli

import (
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// gatewayCompactRecall remembers gateway-emulated compact responses so a later
// previous_response_id can be rewritten into portable summary input instead of
// being forwarded to Grok (which never stored the synthetic compact response).
type gatewayCompactRecall struct {
	mu      sync.Mutex
	entries map[string]gatewayCompactEntry
	ttl     time.Duration
}

type gatewayCompactEntry struct {
	summary string
	session string
	expires time.Time
}

func newGatewayCompactRecall() *gatewayCompactRecall {
	return &gatewayCompactRecall{
		entries: make(map[string]gatewayCompactEntry),
		ttl:     24 * time.Hour,
	}
}

func (r *gatewayCompactRecall) remember(responseID, session, summary string) {
	if r == nil {
		return
	}
	responseID = strings.TrimSpace(responseID)
	summary = strings.TrimSpace(summary)
	if responseID == "" || summary == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.entries == nil {
		r.entries = make(map[string]gatewayCompactEntry)
	}
	// Bound memory: drop expired entries opportunistically.
	now := time.Now().UTC()
	if len(r.entries) > 4096 {
		for id, entry := range r.entries {
			if now.After(entry.expires) {
				delete(r.entries, id)
			}
		}
	}
	r.entries[responseID] = gatewayCompactEntry{
		summary: summary,
		session: strings.TrimSpace(session),
		expires: now.Add(r.ttl),
	}
}

func (r *gatewayCompactRecall) lookup(responseID string) (summary, session string, ok bool) {
	if r == nil {
		return "", "", false
	}
	responseID = strings.TrimSpace(responseID)
	if responseID == "" {
		return "", "", false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, found := r.entries[responseID]
	if !found {
		return "", "", false
	}
	if time.Now().UTC().After(entry.expires) {
		delete(r.entries, responseID)
		return "", "", false
	}
	return entry.summary, entry.session, true
}

// resolveGatewayPreviousResponse rewrites previous_response_id that points at a
// gateway-emulated compact response into a portable user summary message.
// Grok Build never saw that response id (store=false sampling + synthetic body).
func resolveGatewayPreviousResponse(body []byte, recall *gatewayCompactRecall) ([]byte, bool) {
	if len(body) == 0 || recall == nil {
		return body, false
	}
	var payload map[string]any
	if json.Unmarshal(body, &payload) != nil {
		return body, false
	}
	prev, _ := payload["previous_response_id"].(string)
	prev = strings.TrimSpace(prev)
	if prev == "" {
		prev, _ = payload["previousResponseId"].(string)
		prev = strings.TrimSpace(prev)
	}
	if prev == "" {
		return body, false
	}
	summary, _, ok := recall.lookup(prev)
	if !ok {
		return body, false
	}
	delete(payload, "previous_response_id")
	delete(payload, "previousResponseId")
	items, _ := payload["input"].([]any)
	prefix := gatewayCompactionSummaryMessage(summary)
	if items == nil {
		if raw, ok := payload["input"].(string); ok && strings.TrimSpace(raw) != "" {
			items = []any{prefix, map[string]any{"type": "message", "role": "user", "content": raw}}
		} else {
			items = []any{prefix}
		}
	} else {
		items = append([]any{prefix}, items...)
	}
	payload["input"] = items
	encoded, err := json.Marshal(payload)
	if err != nil {
		return body, false
	}
	slog.Warn("compaction_previous_response_expanded", "response_id_set", true)
	return encoded, true
}

// stripPreviousResponseID removes previous_response_id for a one-shot retry
// after Grok rejects compact state.
func stripPreviousResponseID(body []byte) ([]byte, bool) {
	if len(body) == 0 {
		return body, false
	}
	var payload map[string]any
	if json.Unmarshal(body, &payload) != nil {
		return body, false
	}
	_, a := payload["previous_response_id"]
	_, b := payload["previousResponseId"]
	if !a && !b {
		return body, false
	}
	delete(payload, "previous_response_id")
	delete(payload, "previousResponseId")
	encoded, err := json.Marshal(payload)
	if err != nil {
		return body, false
	}
	return encoded, true
}

func isCompactionBlobDecodeError(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	lower := strings.ToLower(string(body))
	return strings.Contains(lower, "compaction blob") ||
		strings.Contains(lower, "could not decode the compaction") ||
		(strings.Contains(lower, "invalid-argument") && strings.Contains(lower, "compaction"))
}
