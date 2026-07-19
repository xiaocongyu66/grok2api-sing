package settings

import (
	"context"
	"testing"
	"time"
)

func TestMergeEditablePreservesServerConcurrencyWhenOmitted(t *testing.T) {
	cfg := testConfig(t)
	service := NewService(cfg, time.Time{}, 0, &runtimeSettingsRepositoryStub{}, nil, nil)
	input := service.Get().Config
	// Admin UI does not send server.maxConcurrentRequests — keep zero like JSON omitempty.
	input.Server = ServerConfig{}
	// Common HF form values that previously failed with enabled+none buffer.
	input.Batch.DBBuffer = DBBufferConfig{Enabled: true, Driver: "none", Path: ""}
	if input.PromptCacheAffinity.TTL == "" {
		input.PromptCacheAffinity.TTL = "24h"
	}

	next, err := mergeEditable(cfg, input)
	if err != nil {
		t.Fatalf("mergeEditable: %v", err)
	}
	if next.Server.MaxConcurrentRequests != cfg.Server.MaxConcurrentRequests {
		t.Fatalf("server concurrency wiped: got %d want %d", next.Server.MaxConcurrentRequests, cfg.Server.MaxConcurrentRequests)
	}
	if next.Batch.DBBuffer.Enabled {
		t.Fatalf("enabled+none buffer should normalize off: %#v", next.Batch.DBBuffer)
	}
}

func TestMergeEditableEmptyPromptCacheTTLKeepsCurrent(t *testing.T) {
	cfg := testConfig(t)
	service := NewService(cfg, time.Time{}, 0, &runtimeSettingsRepositoryStub{}, nil, nil)
	input := service.Get().Config
	input.PromptCacheAffinity.TTL = ""
	next, err := mergeEditable(cfg, input)
	if err != nil {
		t.Fatalf("mergeEditable empty ttl: %v", err)
	}
	if next.Routing.PromptCacheAffinity.TTL.Value() <= 0 {
		t.Fatalf("ttl not preserved: %v", next.Routing.PromptCacheAffinity.TTL.Value())
	}
}

func TestUpdateSucceedsWhenServerOmittedLikeAdminUI(t *testing.T) {
	cfg := testConfig(t)
	repo := &runtimeSettingsRepositoryStub{}
	service := NewService(cfg, time.Time{}, 0, repo, nil, nil)
	input := service.Get().Config
	input.Server = ServerConfig{}
	input.Batch.DBBuffer = DBBufferConfig{Enabled: true, Driver: "none"}
	input.Routing.MaxAttempts = 4
	if _, err := service.Update(context.Background(), service.Get().Revision, input); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if service.Get().Config.Routing.MaxAttempts != 4 {
		t.Fatalf("maxAttempts not saved")
	}
	if service.Get().Config.Server.MaxConcurrentRequests < 1 {
		t.Fatalf("server concurrency lost on update")
	}
}
