// Package connections tracks site-wide in-flight API requests for the admin dashboard.
package connections

import (
	"context"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/chenyme/grok2api/backend/internal/pkg/clientid"
)

// ClientCount is one client type's in-flight request count.
type ClientCount struct {
	Client string // stable id: codex, claude_code, …
	Label  string // display: Codex, Claude Code, …
	Active int64
}

// Stats is a point-in-time view of gateway request concurrency.
type Stats struct {
	// Active is authenticated /v1 requests currently in flight.
	Active int64
	// Peak is the highest Active observed since process start (or shared Redis peak).
	Peak int64
	// Total is cumulative Begin() calls.
	Total int64
	// Clients is live per-client active counts (sorted by Active desc).
	Clients []ClientCount
}

// Tracker records concurrent downstream API connections, optionally by client type.
type Tracker interface {
	// Begin increments totals and the optional client bucket; End must be called once.
	Begin(clientType string) (end func())
	// Snapshot returns current active/peak/total and per-client live counts.
	Snapshot(ctx context.Context) Stats
}

// Local is a process-local atomic tracker (single instance or per-replica view).
type Local struct {
	active atomic.Int64
	peak   atomic.Int64
	total  atomic.Int64

	mu      sync.Mutex
	clients map[string]int64
}

// NewLocal returns a process-local connection tracker.
func NewLocal() *Local {
	return &Local{clients: make(map[string]int64)}
}

// Begin implements Tracker.
func (t *Local) Begin(clientType string) func() {
	clientType = normalizeClient(clientType)
	t.total.Add(1)
	current := t.active.Add(1)
	for {
		peak := t.peak.Load()
		if current <= peak || t.peak.CompareAndSwap(peak, current) {
			break
		}
	}
	t.mu.Lock()
	t.clients[clientType]++
	t.mu.Unlock()

	var ended atomic.Bool
	return func() {
		if ended.Swap(true) {
			return
		}
		if t.active.Add(-1) < 0 {
			t.active.Store(0)
		}
		t.mu.Lock()
		if n := t.clients[clientType] - 1; n <= 0 {
			delete(t.clients, clientType)
		} else {
			t.clients[clientType] = n
		}
		t.mu.Unlock()
	}
}

// Snapshot implements Tracker.
func (t *Local) Snapshot(context.Context) Stats {
	t.mu.Lock()
	clients := make([]ClientCount, 0, len(t.clients))
	for id, n := range t.clients {
		if n <= 0 {
			continue
		}
		clients = append(clients, ClientCount{Client: id, Label: clientid.Label(id), Active: n})
	}
	t.mu.Unlock()
	sortClients(clients)
	return Stats{Active: t.active.Load(), Peak: t.peak.Load(), Total: t.total.Load(), Clients: clients}
}

func normalizeClient(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return clientid.Unknown
	}
	return id
}

func sortClients(clients []ClientCount) {
	sort.Slice(clients, func(i, j int) bool {
		if clients[i].Active != clients[j].Active {
			return clients[i].Active > clients[j].Active
		}
		return clients[i].Client < clients[j].Client
	})
}

// Ensure Local satisfies Tracker.
var _ Tracker = (*Local)(nil)
