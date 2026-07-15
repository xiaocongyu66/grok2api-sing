package config

import "testing"

func TestIsRetryableStatusDefaults(t *testing.T) {
	if !IsRetryableStatus(429, nil, true) || !IsRetryableStatus(503, nil, true) {
		t.Fatal("429/503 should retry by default")
	}
	if !IsRetryableStatus(502, nil, true) {
		t.Fatal("5xx should retry when retryServerErrors")
	}
	if IsRetryableStatus(502, []int{429, 503}, false) {
		t.Fatal("502 should not retry without server-errors flag")
	}
	if !IsRetryableStatus(429, []int{429}, false) {
		t.Fatal("explicit 429 should retry")
	}
	if IsRetryableStatus(401, []int{429, 503}, true) {
		t.Fatal("401 should not retry")
	}
}

func TestNormalizeRoutingRetry(t *testing.T) {
	cfg := Config{}
	NormalizeRoutingRetry(&cfg)
	if len(cfg.Routing.RetryStatusCodes) == 0 {
		t.Fatal("expected defaults")
	}
	cfg.Routing.RetryStatusCodes = []int{429, 429, 503}
	NormalizeRoutingRetry(&cfg)
	if len(cfg.Routing.RetryStatusCodes) != 2 {
		t.Fatalf("dedupe failed: %#v", cfg.Routing.RetryStatusCodes)
	}
}
