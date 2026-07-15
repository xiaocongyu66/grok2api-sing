package egress

import "testing"

func TestIsRandomUserAgent(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"random", "RANDOM", " auto ", "randomize"} {
		if !IsRandomUserAgent(raw) {
			t.Fatalf("%q should be random mode", raw)
		}
	}
	for _, raw := range []string{"", "Mozilla/5.0", "chrome"} {
		if IsRandomUserAgent(raw) {
			t.Fatalf("%q should not be random mode", raw)
		}
	}
}

func TestResolveBrowserUserAgent(t *testing.T) {
	t.Parallel()
	if got := ResolveBrowserUserAgent(""); got != DefaultUserAgent {
		t.Fatalf("empty = %q", got)
	}
	fixed := "Mozilla/5.0 fixed"
	if got := ResolveBrowserUserAgent(fixed); got != fixed {
		t.Fatalf("fixed = %q", got)
	}
	seen := map[string]struct{}{}
	for range 50 {
		got := ResolveBrowserUserAgent(RandomUserAgentToken)
		if got == "" {
			t.Fatal("random returned empty")
		}
		seen[got] = struct{}{}
	}
	if len(seen) < 2 {
		t.Fatalf("expected multiple UAs from pool, got %d unique", len(seen))
	}
}

func TestNormalizeStoredUserAgent(t *testing.T) {
	t.Parallel()
	if got := NormalizeStoredUserAgent(" auto "); got != RandomUserAgentToken {
		t.Fatalf("normalize auto = %q", got)
	}
	if got := NormalizeStoredUserAgent("  Mozilla/5.0  "); got != "Mozilla/5.0" {
		t.Fatalf("normalize fixed = %q", got)
	}
}
