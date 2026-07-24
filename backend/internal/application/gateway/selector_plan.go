package gateway

import (
	"container/heap"
	"context"
	"fmt"
	"time"

	"github.com/chenyme/grok2api/backend/internal/domain/account"
	"github.com/chenyme/grok2api/backend/internal/repository"
)

type candidateScore struct {
	index           int
	tier            int
	preferFreeBuild bool
	billingFresh    bool
	failureCount    int
	inFlight        int
	remaining       float64
	lastSelected    time.Time
}

// candidatePlan 使用线性建堆保留完整路由优先级，并允许 claim 失败后按顺序取下一账号。
type candidatePlan struct {
	values []account.RoutingCandidate
	scores []candidateScore
}

func (p *candidatePlan) Len() int { return len(p.scores) }

func (p *candidatePlan) Less(left, right int) bool {
	return candidateScoreBetter(p.values, p.scores[left], p.scores[right])
}

func (p *candidatePlan) Swap(left, right int) {
	p.scores[left], p.scores[right] = p.scores[right], p.scores[left]
}

func (p *candidatePlan) Push(value any) {
	p.scores = append(p.scores, value.(candidateScore))
}

func (p *candidatePlan) Pop() any {
	last := len(p.scores) - 1
	value := p.scores[last]
	p.scores = p.scores[:last]
	return value
}

func (p *candidatePlan) Next() (account.RoutingCandidate, bool) {
	if p == nil || p.Len() == 0 {
		return account.RoutingCandidate{}, false
	}
	score := heap.Pop(p).(candidateScore)
	return p.values[score.index], true
}

func candidateScoreBetter(values []account.RoutingCandidate, leftScore, rightScore candidateScore) bool {
	leftCandidate, rightCandidate := values[leftScore.index], values[rightScore.index]
	left, right := leftCandidate.Credential, rightCandidate.Credential
	if leftCandidate.SupportsModel != rightCandidate.SupportsModel {
		return leftCandidate.SupportsModel
	}
	if leftCandidate.ModelCapabilityKnown != rightCandidate.ModelCapabilityKnown {
		return leftCandidate.ModelCapabilityKnown
	}
	if leftScore.preferFreeBuild != rightScore.preferFreeBuild {
		return leftScore.preferFreeBuild
	}
	if leftScore.tier != rightScore.tier {
		return leftScore.tier < rightScore.tier
	}
	if left.Priority != right.Priority {
		return left.Priority > right.Priority
	}
	// Optional: push recently-failed accounts to the back of the pool.
	if leftScore.failureCount != rightScore.failureCount {
		return leftScore.failureCount < rightScore.failureCount
	}
	if leftScore.billingFresh != rightScore.billingFresh {
		return leftScore.billingFresh
	}
	if leftScore.inFlight != rightScore.inFlight {
		return leftScore.inFlight < rightScore.inFlight
	}
	if leftScore.remaining != rightScore.remaining {
		return leftScore.remaining > rightScore.remaining
	}
	if !leftScore.lastSelected.Equal(rightScore.lastSelected) {
		return leftScore.lastSelected.Before(rightScore.lastSelected)
	}
	return left.ID < right.ID
}

// planCandidates 批量读取动态并发状态，并以 O(n) 建堆生成保持原比较规则的候选计划。
func (s *Selector) planCandidates(ctx context.Context, values []account.RoutingCandidate, now time.Time, tierOrder []account.WebTier) (*candidatePlan, error) {
	return s.planCandidateIndexes(ctx, values, nil, now, tierOrder)
}

// planCandidateIndexes plans over immutable snapshot indexes (nil = all values).
func (s *Selector) planCandidateIndexes(ctx context.Context, values []account.RoutingCandidate, indexes []int, now time.Time, tierOrder []account.WebTier) (*candidatePlan, error) {
	return s.planCandidateIndexesWithHints(ctx, values, indexes, now, tierOrder, nil, s.preferFreeBuildEnabled())
}

func (s *Selector) planCandidateIndexesWithHints(ctx context.Context, values []account.RoutingCandidate, indexes []int, now time.Time, tierOrder []account.WebTier, concurrencyHints []int, preferFreeBuild bool) (*candidatePlan, error) {
	length := len(indexes)
	if indexes == nil {
		length = len(values)
	}
	inFlight := make([]int, length)
	if concurrencyHints == nil {
		keys := make([]string, length)
		for position := range length {
			index := position
			if indexes != nil {
				index = indexes[position]
			}
			keys[position] = accountConcurrencyKey(values[index].Credential.ID)
		}
		concurrencySnapshot, err := s.loadConcurrencySnapshot(ctx, keys)
		if err != nil {
			return nil, err
		}
		for position := range length {
			inFlight[position] = concurrencySnapshot[keys[position]]
		}
	} else {
		missingIndexes := make([]int, 0, length)
		keys := make([]string, 0, length)
		for position := range length {
			index := position
			if indexes != nil {
				index = indexes[position]
			}
			if concurrencyHints[index] != 0 {
				continue
			}
			missingIndexes = append(missingIndexes, index)
			keys = append(keys, accountConcurrencyKey(values[index].Credential.ID))
		}
		if len(keys) > 0 {
			concurrencySnapshot, err := s.loadConcurrencySnapshot(ctx, keys)
			if err != nil {
				return nil, err
			}
			for position, index := range missingIndexes {
				concurrencyHints[index] = concurrencySnapshot[keys[position]] + 1
			}
		}
		for position := range length {
			index := position
			if indexes != nil {
				index = indexes[position]
			}
			inFlight[position] = concurrencyHints[index] - 1
		}
	}

	s.mu.Lock()
	deprioritizeFailed := s.deprioritizeFailedAccounts
	scores := make([]candidateScore, length)
	for position := range length {
		index := position
		if indexes != nil {
			index = indexes[position]
		}
		candidate := values[index]
		score := candidateScore{
			index: index, tier: tierOrderRank(tierOrder, candidate.Credential.WebTier),
			preferFreeBuild: preferFreeBuild && candidate.IsKnownFreeBuild(),
			inFlight:        inFlight[position], lastSelected: s.lastSelectedAt[candidate.Credential.ID],
		}
		if deprioritizeFailed {
			score.failureCount = candidate.Credential.FailureCount
		}
		// Prefer accounts with more local window quota (Web/Console); Billing remaining for Build.
		if candidate.QuotaWindow != nil {
			score.remaining = float64(candidate.QuotaWindow.Remaining)
			if candidate.QuotaWindow.SyncedAt != nil {
				score.billingFresh = now.Sub(*candidate.QuotaWindow.SyncedAt) <= 30*time.Minute
			} else {
				score.billingFresh = now.Sub(candidate.QuotaWindow.UpdatedAt) <= 30*time.Minute
			}
		} else if candidate.Billing != nil {
			score.remaining = candidate.Billing.Remaining()
			score.billingFresh = now.Sub(candidate.Billing.SyncedAt) <= 30*time.Minute
		}
		scores[position] = score
	}
	s.mu.Unlock()

	plan := &candidatePlan{values: values, scores: scores}
	heap.Init(plan)
	return plan, nil
}

func (s *Selector) loadConcurrencySnapshot(ctx context.Context, keys []string) (map[string]int, error) {
	values := make(map[string]int, len(keys))
	if batchReader, ok := s.concurrency.(repository.ConcurrencySnapshotReader); ok {
		var err error
		values, err = batchReader.CurrentMany(ctx, keys)
		if err != nil {
			return nil, fmt.Errorf("批量读取账号并发租约: %w", err)
		}
		return values, nil
	}
	for _, key := range keys {
		current, err := s.concurrency.Current(ctx, key)
		if err != nil {
			return nil, fmt.Errorf("读取账号并发租约: %w", err)
		}
		values[key] = current
	}
	return values, nil
}

func accountConcurrencyKey(accountID uint64) string {
	return repository.AccountConcurrencyKey(accountID)
}
