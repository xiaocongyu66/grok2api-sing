package promptcache

import (
	"context"
	"strings"
	"testing"
	"time"
)

type memStore struct {
	values map[string]string
}

func (m *memStore) GetOrCreate(_ context.Context, fingerprint, newID string, _ time.Duration, _ bool) (string, error) {
	if m.values == nil {
		m.values = map[string]string{}
	}
	if existing, ok := m.values[fingerprint]; ok {
		return existing, nil
	}
	m.values[fingerprint] = newID
	return newID, nil
}

func (m *memStore) Lookup(_ context.Context, fingerprint string, _ time.Time) (string, bool, error) {
	if m.values == nil {
		return "", false, nil
	}
	id, ok := m.values[fingerprint]
	return id, ok && id != "", nil
}

func TestResolvePrefersClientSessionHeaders(t *testing.T) {
	r := NewResolver(&memStore{}, DefaultPolicy())
	id, err := r.Resolve(context.Background(), Request{
		ClientKeyID: 1,
		ClientIP:    "1.2.3.4",
		UserAgent:   "ua",
		Headers:     map[string]string{"x-claude-code-session-id": "claude-sess-1"},
		Explicit:    "body-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	if id != "body-key" {
		t.Fatalf("explicit body should win, got %q", id)
	}
	id, err = r.Resolve(context.Background(), Request{
		ClientKeyID: 1,
		Headers:     map[string]string{"x-codex-window-id": "codex-win-9"},
	})
	if err != nil || id != "codex-win-9" {
		t.Fatalf("codex header = %q err=%v", id, err)
	}
}

func TestResolveFingerprintIsStable(t *testing.T) {
	store := &memStore{}
	r := NewResolver(store, DefaultPolicy())
	req := Request{ClientKeyID: 42, ClientIP: "10.0.0.8:51234", UserAgent: "Codex/1.0"}
	first, err := r.Resolve(context.Background(), req)
	if err != nil || first == "" || !strings.HasPrefix(first, "xai_") {
		t.Fatalf("first = %q err=%v", first, err)
	}
	second, err := r.Resolve(context.Background(), req)
	if err != nil || second != first {
		t.Fatalf("second = %q want %q", second, first)
	}
	// Different UA → different mapping.
	third, err := r.Resolve(context.Background(), Request{ClientKeyID: 42, ClientIP: "10.0.0.8", UserAgent: "Other/2.0"})
	if err != nil || third == first {
		t.Fatalf("third should differ: %q vs %q", third, first)
	}
}

func TestResolveDisabledSkipsFingerprint(t *testing.T) {
	r := NewResolver(&memStore{}, Policy{Enabled: false, Fingerprint: true, TTL: time.Hour})
	id, err := r.Resolve(context.Background(), Request{ClientKeyID: 1, ClientIP: "1.1.1.1", UserAgent: "ua"})
	if err != nil || id != "" {
		t.Fatalf("disabled fingerprint should return empty, got %q err=%v", id, err)
	}
}

func TestResolveConversationSeedIsStable(t *testing.T) {
	store := &memStore{}
	r := NewResolver(store, DefaultPolicy())
	seed := ConversationSeedFromMessages("You are helpful.", []MessageSeed{{Role: "user", Text: "hello world"}})
	if seed == "" {
		t.Fatal("expected seed")
	}
	first, err := r.Resolve(context.Background(), Request{ClientKeyID: 7, ConversationSeed: seed})
	if err != nil || first == "" {
		t.Fatalf("first = %q err=%v", first, err)
	}
	second, err := r.Resolve(context.Background(), Request{ClientKeyID: 7, ConversationSeed: seed})
	if err != nil || second != first {
		t.Fatalf("second = %q want %q", second, first)
	}
	// Different first user → different seed mapping.
	other := ConversationSeedFromMessages("You are helpful.", []MessageSeed{{Role: "user", Text: "other topic"}})
	third, err := r.Resolve(context.Background(), Request{ClientKeyID: 7, ConversationSeed: other})
	if err != nil || third == first {
		t.Fatalf("third should differ: %q vs %q", third, first)
	}
}

func TestRememberTurnLinksPreviousResponse(t *testing.T) {
	store := &memStore{}
	r := NewResolver(store, DefaultPolicy())
	affinity := "xai_session_abc"
	if err := r.RememberTurn(context.Background(), 9, "resp_123", affinity); err != nil {
		t.Fatal(err)
	}
	got, err := r.Resolve(context.Background(), Request{
		ClientKeyID: 9, PreviousResponseID: "resp_123",
		// No session headers — should reuse remembered turn mapping.
		ClientIP: "1.1.1.1", UserAgent: "ua",
	})
	if err != nil || got != affinity {
		t.Fatalf("got %q want %q err=%v", got, affinity, err)
	}
}

func TestConversationSeedFromChatBody(t *testing.T) {
	body := []byte(`{"messages":[{"role":"system","content":"sys"},{"role":"user","content":"hi there"}]}`)
	seed := ConversationSeedFromChatBody(body)
	if seed == "" {
		t.Fatal("expected seed from chat body")
	}
	again := ConversationSeedFromChatBody(body)
	if again != seed {
		t.Fatalf("unstable seed: %q vs %q", seed, again)
	}
}
