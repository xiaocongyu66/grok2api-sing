package connections

import (
	"context"
	"sync"
	"testing"
)

func TestLocalTracksActivePeakTotalAndClients(t *testing.T) {
	tracker := NewLocal()
	endCodex1 := tracker.Begin("codex")
	endCodex2 := tracker.Begin("codex")
	endClaude := tracker.Begin("claude_code")
	stats := tracker.Snapshot(context.Background())
	if stats.Active != 3 || stats.Peak != 3 || stats.Total != 3 {
		t.Fatalf("mid = %#v", stats)
	}
	if len(stats.Clients) != 2 {
		t.Fatalf("clients = %#v", stats.Clients)
	}
	if stats.Clients[0].Client != "codex" || stats.Clients[0].Active != 2 {
		t.Fatalf("top client = %#v", stats.Clients[0])
	}
	if stats.Clients[1].Client != "claude_code" || stats.Clients[1].Active != 1 {
		t.Fatalf("second client = %#v", stats.Clients[1])
	}
	endCodex1()
	endClaude()
	stats = tracker.Snapshot(context.Background())
	if stats.Active != 1 || stats.Peak != 3 || stats.Total != 3 {
		t.Fatalf("after ends = %#v", stats)
	}
	if len(stats.Clients) != 1 || stats.Clients[0].Client != "codex" || stats.Clients[0].Active != 1 {
		t.Fatalf("remaining = %#v", stats.Clients)
	}
	endCodex2()
	endCodex2() // idempotent
	stats = tracker.Snapshot(context.Background())
	if stats.Active != 0 || stats.Peak != 3 || len(stats.Clients) != 0 {
		t.Fatalf("done = %#v", stats)
	}
}

func TestLocalConcurrentPeak(t *testing.T) {
	tracker := NewLocal()
	var wg sync.WaitGroup
	ends := make(chan func(), 64)
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			client := "codex"
			if i%3 == 0 {
				client = "grok_cli"
			}
			ends <- tracker.Begin(client)
		}(i)
	}
	wg.Wait()
	close(ends)
	stats := tracker.Snapshot(context.Background())
	if stats.Active != 64 || stats.Peak < 64 || stats.Total != 64 {
		t.Fatalf("concurrent = %#v", stats)
	}
	var sum int64
	for _, c := range stats.Clients {
		sum += c.Active
	}
	if sum != 64 {
		t.Fatalf("client sum = %d, clients=%#v", sum, stats.Clients)
	}
	for end := range ends {
		end()
	}
	if got := tracker.Snapshot(context.Background()).Active; got != 0 {
		t.Fatalf("active after drain = %d", got)
	}
}
