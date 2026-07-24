package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/chenyme/grok2api/backend/internal/domain/account"
	"github.com/chenyme/grok2api/backend/internal/repository"
	"golang.org/x/sync/singleflight"
)

type accountLease struct {
	Credential          account.Credential
	Billing             *account.Billing
	QuotaProbe          bool
	QuotaProbeKind      account.QuotaRecoveryKind
	QuotaMode           string
	selectorObservation *selectorLeaseObservation
	release             func()
}

const quotaProbeLease = 5 * time.Minute
const successPersistInterval = 30 * time.Second

// candidateCacheTTL trades a little routing freshness for fewer ListRoutingCandidates
// DB hits under high RPS; sticky/prompt-cache still pin accounts independently.
// Shorter TTL helps admin bulk deletes / pool changes reflect in scheduling faster.
const candidateCacheTTL = 1 * time.Second

type candidateSnapshot struct {
	values    []account.RoutingCandidate
	expiresAt time.Time
}

type candidateCacheKey struct {
	provider      account.Provider
	upstreamModel string
	quotaMode     string
}

type SelectionUnavailableReason string

const (
	SelectionNoAccounts       SelectionUnavailableReason = "no_accounts"
	SelectionUnsupportedModel SelectionUnavailableReason = "unsupported_model"
	SelectionCooling          SelectionUnavailableReason = "cooling"
	SelectionModelCooling     SelectionUnavailableReason = "model_cooling"
	SelectionQuotaExhausted   SelectionUnavailableReason = "quota_exhausted"
	SelectionSaturated        SelectionUnavailableReason = "saturated"
)

// SelectionUnavailableError 保留选号失败的真实原因，避免所有情况都退化成模糊的 503。
type SelectionUnavailableError struct {
	Reason     SelectionUnavailableReason
	RetryAfter time.Duration
}

func (e *SelectionUnavailableError) Error() string {
	if e == nil {
		return "没有可用上游账号"
	}
	switch e.Reason {
	case SelectionUnsupportedModel:
		return "当前账号池不支持该模型"
	case SelectionCooling:
		return "可用上游账号正在冷却"
	case SelectionModelCooling:
		return "可用上游账号的目标模型正在冷却"
	case SelectionQuotaExhausted:
		return "可用上游账号额度等待恢复"
	case SelectionSaturated:
		return "可用上游账号均达到并发上限"
	default:
		return "没有可用上游账号"
	}
}

func (l *accountLease) Release() {
	if l == nil {
		return
	}
	if l.selectorObservation != nil {
		l.selectorObservation.completeRelease()
	}
	if l.release != nil {
		l.release()
		l.release = nil
	}
}

func (l *accountLease) markSelectorUpstreamStarted() {
	if l != nil && l.selectorObservation != nil {
		l.selectorObservation.upstreamStarted.Store(true)
	}
}

func (l *accountLease) completeSelectorObservation(success bool) {
	if l != nil && l.selectorObservation != nil {
		l.selectorObservation.complete(success)
	}
}

// Selector 实现可替换的 balanced 账号选择策略。
type Selector struct {
	accounts                   repository.AccountRepository
	concurrency                repository.ConcurrencyLimiter
	sticky                     repository.StickySessionRepository
	logger                     *slog.Logger
	stickyTTL                  time.Duration
	cooldownBase               time.Duration
	cooldownMax                time.Duration
	capacityWait               time.Duration
	deprioritizeFailedAccounts bool
	preferFreeBuild            bool
	segmentedConfig            segmentedSelectorConfig
	segmentedState             segmentedSelectorState
	configMu                   sync.RWMutex
	mu                         sync.Mutex
	leaseWakeMu                sync.Mutex
	leaseWake                  chan struct{}
	lastSelectedAt             map[uint64]time.Time
	lastSuccessAt              map[uint64]time.Time
	candidates                 map[candidateCacheKey]candidateSnapshot
	candidateLoads             singleflight.Group
	tierOrders                 interface {
		TierOrder(account.Provider, string) []account.WebTier
	}
}

func NewSelector(accounts repository.AccountRepository, concurrency repository.ConcurrencyLimiter, sticky repository.StickySessionRepository, tierOrders interface {
	TierOrder(account.Provider, string) []account.WebTier
}, stickyTTL, cooldownBase, cooldownMax time.Duration, capacityWait ...time.Duration) *Selector {
	wait := time.Duration(0)
	if len(capacityWait) > 0 && capacityWait[0] > 0 {
		wait = capacityWait[0]
	}
	return &Selector{
		accounts: accounts, concurrency: concurrency, sticky: sticky, logger: slog.Default(),
		tierOrders: tierOrders, stickyTTL: stickyTTL, cooldownBase: cooldownBase, cooldownMax: cooldownMax,
		capacityWait: wait, deprioritizeFailedAccounts: true,
		segmentedConfig: normalizeSegmentedSelectorConfig(segmentedSelectorConfig{}),
		leaseWake: make(chan struct{}), lastSelectedAt: make(map[uint64]time.Time), lastSuccessAt: make(map[uint64]time.Time),
		candidates: make(map[candidateCacheKey]candidateSnapshot),
	}
}

// SetLogger attaches schedule diagnostics logger (account pick, capacity wait, sticky).
func (s *Selector) SetLogger(logger *slog.Logger) {
	if s == nil {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}
	s.logger = logger
}

func (s *Selector) log(level slog.Level, msg string, args ...any) {
	if s == nil || s.logger == nil {
		return
	}
	s.logger.Log(context.Background(), level, msg, args...)
}

func (s *Selector) UpdateConfig(stickyTTL, cooldownBase, cooldownMax time.Duration, capacityWait ...time.Duration) {
	s.mu.Lock()
	s.stickyTTL = stickyTTL
	s.cooldownBase = cooldownBase
	s.cooldownMax = cooldownMax
	if len(capacityWait) > 0 {
		s.capacityWait = max(time.Duration(0), capacityWait[0])
	}
	s.mu.Unlock()
}

// SetDeprioritizeFailedAccounts toggles ranking accounts with higher failure_count last.
func (s *Selector) SetDeprioritizeFailedAccounts(enabled bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.deprioritizeFailedAccounts = enabled
	s.mu.Unlock()
}

// UpdatePreferFreeBuild toggles preferring known free Build accounts when scoring.
func (s *Selector) UpdatePreferFreeBuild(value bool) {
	if s == nil {
		return
	}
	s.configMu.Lock()
	s.preferFreeBuild = value
	s.configMu.Unlock()
}

// UpdateSegmentedSelector configures large-pool windowed selection (disabled by default).
func (s *Selector) UpdateSegmentedSelector(enabled bool, minCandidates, windowSize int) {
	if s == nil {
		return
	}
	s.configMu.Lock()
	s.segmentedConfig = normalizeSegmentedSelectorConfig(segmentedSelectorConfig{
		enabled: enabled, minCandidates: minCandidates, windowSize: windowSize,
	})
	s.configMu.Unlock()
}

func (s *Selector) routingConfig() (time.Duration, time.Duration, time.Duration, time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stickyTTL, s.cooldownBase, s.cooldownMax, s.capacityWait
}

func (s *Selector) deprioritizeFailed() bool {
	if s == nil {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deprioritizeFailedAccounts
}

func (s *Selector) preferFreeBuildEnabled() bool {
	if s == nil {
		return false
	}
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	return s.preferFreeBuild
}

func routingCandidateByID(values []account.RoutingCandidate, indexes []int, accountID uint64) (account.RoutingCandidate, bool) {
	if indexes == nil {
		for _, candidate := range values {
			if candidate.Credential.ID == accountID {
				return candidate, true
			}
		}
		return account.RoutingCandidate{}, false
	}
	for _, index := range indexes {
		if index < 0 || index >= len(values) {
			continue
		}
		if values[index].Credential.ID == accountID {
			return values[index], true
		}
	}
	return account.RoutingCandidate{}, false
}

func (s *Selector) Acquire(ctx context.Context, provider account.Provider, upstreamModel, quotaMode, affinityKey string, excluded map[uint64]bool, allowQuotaProbe bool) (*accountLease, error) {
	now := time.Now().UTC()
	stickyKey := stickySessionKey(affinityKey)
	values, err := s.loadCandidates(ctx, provider, upstreamModel, quotaMode, now)
	if err != nil {
		return nil, err
	}
	// Index-only pools avoid copying full credential/billing structs per request.
	normalCandidates := make([]int, 0, len(values))
	probeCandidates := make([]int, 0, len(values))
	supportedCandidates := 0
	consideredCandidates := 0
	coolingCandidates := 0
	modelCoolingCandidates := 0
	quotaCandidates := 0
	var earliestRetry time.Time
	for index, candidate := range values {
		value := candidate.Credential
		if excluded[value.ID] || value.AuthStatus != account.AuthStatusActive {
			continue
		}
		consideredCandidates++
		if candidate.ModelCapabilityKnown && !candidate.SupportsModel {
			continue
		}
		supportedCandidates++
		if candidate.ModelQuotaBlock != nil && now.Before(candidate.ModelQuotaBlock.CooldownUntil) {
			modelCoolingCandidates++
			earliestRetry = earlierFuture(earliestRetry, candidate.ModelQuotaBlock.CooldownUntil, now)
			continue
		}
		if value.CooldownUntil != nil && now.Before(*value.CooldownUntil) {
			coolingCandidates++
			earliestRetry = earlierFuture(earliestRetry, *value.CooldownUntil, now)
			continue
		}
		quotaRecovery := candidate.QuotaRecovery
		if quotaRecovery != nil && quotaRecovery.Status != account.QuotaRecoveryStatusActive {
			if allowQuotaProbe && quotaRecovery.NextProbeAt != nil && !now.Before(*quotaRecovery.NextProbeAt) {
				probeCandidates = append(probeCandidates, index)
			} else {
				quotaCandidates++
				if quotaRecovery.NextProbeAt != nil {
					earliestRetry = earlierFuture(earliestRetry, *quotaRecovery.NextProbeAt, now)
				}
			}
			continue
		}
		if candidate.Billing != nil && candidate.Billing.IsExhausted(value.MinimumRemaining) {
			quotaCandidates++
			continue
		}
		if candidate.QuotaWindow != nil && candidate.QuotaWindow.Remaining <= 0 {
			quotaCandidates++
			if candidate.QuotaWindow.ResetAt != nil {
				earliestRetry = earlierFuture(earliestRetry, *candidate.QuotaWindow.ResetAt, now)
			}
			continue
		}
		normalCandidates = append(normalCandidates, index)
	}
	if len(normalCandidates) == 0 && len(probeCandidates) == 0 {
		reason := SelectionNoAccounts
		switch {
		case consideredCandidates > 0 && supportedCandidates == 0:
			reason = SelectionUnsupportedModel
		case modelCoolingCandidates > 0:
			reason = SelectionModelCooling
		case coolingCandidates > 0:
			reason = SelectionCooling
		case quotaCandidates > 0:
			reason = SelectionQuotaExhausted
		}
		return nil, &SelectionUnavailableError{Reason: reason, RetryAfter: retryDelay(now, earliestRetry)}
	}
	if len(probeCandidates) > 0 {
		plan, err := s.planCandidateIndexes(ctx, values, probeCandidates, now, s.resolveTierOrder(provider, upstreamModel))
		if err != nil {
			return nil, err
		}
		for candidate, ok := plan.Next(); ok; candidate, ok = plan.Next() {
			lease, err := s.claimAccountSlot(ctx, candidate.Credential)
			if err != nil {
				return nil, err
			}
			if lease == nil {
				continue
			}
			claimed, err := s.accounts.ClaimQuotaProbe(ctx, candidate.Credential.ID, now, now.Add(quotaProbeLease))
			if err != nil || !claimed {
				lease.Release()
				if err != nil {
					return nil, err
				}
				continue
			}
			lease.QuotaProbe = true
			lease.QuotaProbeKind = candidate.QuotaRecovery.Kind
			lease.Billing = candidate.Billing
			return lease, nil
		}
	}
	var saturatedStickyID uint64
	if stickyKey != "" {
		stickyID, ok, err := s.sticky.Get(ctx, stickyKey, now)
		if err != nil {
			return nil, fmt.Errorf("读取会话粘滞状态: %w", err)
		}
		if ok {
			candidate, found := routingCandidateByID(values, normalCandidates, stickyID)
			if found {
				// Refresh/claim sticky binding atomically so concurrent turns cannot thrash accounts.
				stickyTTL, _, _, _ := s.routingConfig()
				boundID, bindErr := s.sticky.Bind(ctx, stickyKey, stickyID, now, now.Add(stickyTTL))
				if bindErr != nil {
					return nil, fmt.Errorf("刷新会话粘滞状态: %w", bindErr)
				}
				if boundID != stickyID {
					if rebound, ok := routingCandidateByID(values, normalCandidates, boundID); ok {
						candidate = rebound
						stickyID = boundID
					}
				}
				lease, acquireErr := s.acquirePinnedCapacity(ctx, candidate.Credential)
				if acquireErr == nil && lease != nil {
					lease.Billing = candidate.Billing
					lease.QuotaMode = effectiveQuotaMode(candidate, quotaMode)
					s.log(slog.LevelInfo, "schedule_account_selected",
						"provider", provider, "model", upstreamModel, "account_id", candidate.Credential.ID,
						"reason", "sticky_prompt_cache", "max_concurrent", accountConcurrencyLimit(candidate.Credential),
						"normal_pool", len(normalCandidates), "probe_pool", len(probeCandidates),
					)
					return lease, nil
				}
				if acquireErr != nil && !isSelectionUnavailable(acquireErr, SelectionSaturated) {
					return nil, acquireErr
				}
				saturatedStickyID = stickyID
				s.log(slog.LevelWarn, "schedule_sticky_wait_exhausted",
					"provider", provider, "model", upstreamModel, "account_id", stickyID,
					"max_concurrent", accountConcurrencyLimit(candidate.Credential),
					"normal_pool", len(normalCandidates),
					"hint", "sticky account saturated after capacityWait; temporary borrow allowed without overwriting bind",
				)
			} else {
				s.log(slog.LevelInfo, "schedule_sticky_unavailable",
					"provider", provider, "model", upstreamModel, "account_id", stickyID,
					"hint", "sticky account not in normal pool (cooldown/quota/disabled); prompt cache may miss",
				)
			}
		}
	}
	// Sticky account only concurrency-saturated: borrow another account for this request
	// without overwriting the sticky binding (upstream sticky optimize).
	if saturatedStickyID != 0 {
		plan, err := s.planCandidateIndexes(ctx, values, normalCandidates, time.Now().UTC(), s.resolveTierOrder(provider, upstreamModel))
		if err != nil {
			return nil, err
		}
		for candidate, ok := plan.Next(); ok; candidate, ok = plan.Next() {
			if candidate.Credential.ID == saturatedStickyID {
				continue
			}
			lease, claimErr := s.claimAccountSlot(ctx, candidate.Credential)
			if claimErr != nil {
				return nil, claimErr
			}
			if lease == nil {
				continue
			}
			lease.Billing = candidate.Billing
			lease.QuotaMode = effectiveQuotaMode(candidate, quotaMode)
			s.log(slog.LevelInfo, "schedule_account_selected",
				"provider", provider, "model", upstreamModel, "account_id", candidate.Credential.ID,
				"reason", "sticky_borrow", "sticky_account_id", saturatedStickyID,
				"max_concurrent", accountConcurrencyLimit(candidate.Credential),
			)
			return lease, nil
		}
		return nil, &SelectionUnavailableError{Reason: SelectionSaturated, RetryAfter: time.Second}
	}
	// Large pools: windowed cohort selection when sticky is not required.
	if stickyKey == "" {
		activeRequest := s.nextSegmentedActiveRequest(provider, upstreamModel, quotaMode, len(normalCandidates))
		if activeRequest != nil {
			return s.acquireSegmentedCandidates(ctx, values, normalCandidates, quotaMode, s.resolveTierOrder(provider, upstreamModel), *activeRequest)
		}
	}
	_, _, _, capacityWait := s.routingConfig()
	waitDeadline := time.Now().Add(capacityWait)
	waitRounds := 0
	for {
		currentTime := time.Now().UTC()
		plan, err := s.planCandidateIndexes(ctx, values, normalCandidates, currentTime, s.resolveTierOrder(provider, upstreamModel))
		if err != nil {
			return nil, err
		}
		tried := 0
		for candidate, ok := plan.Next(); ok; candidate, ok = plan.Next() {
			tried++
			lease, err := s.claimAccountSlot(ctx, candidate.Credential)
			if err != nil {
				return nil, err
			}
			if lease == nil {
				continue
			}
			// Re-validate time-sensitive exclusions from the (possibly slightly stale) snapshot
			// using the planning time. Prevents handing out a lease for an account that entered
			// cooldown / probe window / model block during the capacityWait loop.
			if candidate.ModelQuotaBlock != nil && currentTime.Before(candidate.ModelQuotaBlock.CooldownUntil) {
				lease.Release()
				continue
			}
			if c := candidate.Credential; c.CooldownUntil != nil && currentTime.Before(*c.CooldownUntil) {
				lease.Release()
				continue
			}
			if rec := candidate.QuotaRecovery; rec != nil && rec.Status != account.QuotaRecoveryStatusActive {
				if rec.NextProbeAt != nil && currentTime.Before(*rec.NextProbeAt) {
					lease.Release()
					continue
				}
			}
			// Billing and QuotaWindow can legitimately go stale due to concurrent usage by other
			// instances/requests; the call path and response handling will detect and mark exhausted.
			if stickyKey != "" {
				stickyTTL, _, _, _ := s.routingConfig()
				boundID, bindErr := s.sticky.Bind(ctx, stickyKey, candidate.Credential.ID, currentTime, currentTime.Add(stickyTTL))
				if bindErr != nil {
					lease.Release()
					return nil, fmt.Errorf("写入会话粘滞状态: %w", bindErr)
				}
				if boundID != candidate.Credential.ID {
					if boundCandidate, eligible := routingCandidateByID(values, normalCandidates, boundID); eligible {
						boundLease, boundErr := s.acquirePinnedCapacity(ctx, boundCandidate.Credential)
						if boundErr == nil && boundLease != nil {
							lease.Release()
							boundLease.Billing = boundCandidate.Billing
							boundLease.QuotaMode = effectiveQuotaMode(boundCandidate, quotaMode)
							s.log(slog.LevelInfo, "schedule_account_selected",
								"provider", provider, "model", upstreamModel, "account_id", boundCandidate.Credential.ID,
								"reason", "sticky_bind_race", "max_concurrent", accountConcurrencyLimit(boundCandidate.Credential),
							)
							return boundLease, nil
						}
						if boundErr != nil && !isSelectionUnavailable(boundErr, SelectionSaturated) {
							lease.Release()
							return nil, boundErr
						}
					}
				}
			}
			lease.Billing = candidate.Billing
			lease.QuotaMode = effectiveQuotaMode(candidate, quotaMode)
			s.log(slog.LevelInfo, "schedule_account_selected",
				"provider", provider, "model", upstreamModel, "account_id", candidate.Credential.ID,
				"reason", "balanced", "max_concurrent", accountConcurrencyLimit(candidate.Credential),
				"normal_pool", len(normalCandidates), "tried", tried, "wait_rounds", waitRounds,
				"cooling", coolingCandidates, "quota_blocked", quotaCandidates, "model_cooling", modelCoolingCandidates,
			)
			return lease, nil
		}
		if capacityWait <= 0 {
			s.log(slog.LevelWarn, "schedule_saturated",
				"provider", provider, "model", upstreamModel, "normal_pool", len(normalCandidates),
				"cooling", coolingCandidates, "quota_blocked", quotaCandidates, "wait_disabled", true,
			)
			return nil, &SelectionUnavailableError{Reason: SelectionSaturated, RetryAfter: time.Second}
		}
		if waitRounds == 0 {
			s.log(slog.LevelInfo, "schedule_capacity_wait_start",
				"provider", provider, "model", upstreamModel, "normal_pool", len(normalCandidates),
				"capacity_wait_ms", capacityWait.Milliseconds(),
				"hint", "all candidate accounts at MaxConcurrent; waiting for a free slot (blocks other sessions if pool is tiny)",
			)
		}
		waitRounds++
		retry, err := s.awaitLeaseRetry(ctx, waitDeadline)
		if err != nil {
			return nil, err
		}
		if !retry {
			s.log(slog.LevelWarn, "schedule_saturated",
				"provider", provider, "model", upstreamModel, "normal_pool", len(normalCandidates),
				"wait_rounds", waitRounds, "capacity_wait_ms", capacityWait.Milliseconds(),
			)
			return nil, &SelectionUnavailableError{Reason: SelectionSaturated, RetryAfter: time.Second}
		}
	}
}

func accountConcurrencyLimit(value account.Credential) int {
	if value.MaxConcurrent > 0 {
		return value.MaxConcurrent
	}
	return account.DefaultMaxConcurrent
}

// stickySessionKey 将调用方粘滞 identity 压缩为固定长度，仅用于账号粘滞索引。
func stickySessionKey(value string) string {
	if value == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

// promptCacheStickyKey keeps the historical name for tests/callers.
func promptCacheStickyKey(value string) string { return stickySessionKey(value) }

func isSelectionUnavailable(err error, reason SelectionUnavailableReason) bool {
	var unavailable *SelectionUnavailableError
	return errors.As(err, &unavailable) && unavailable != nil && unavailable.Reason == reason
}

// AcquirePinned 为 previous_response_id 等账号归属请求获取指定账号租约。
func (s *Selector) AcquirePinned(ctx context.Context, provider account.Provider, accountID uint64, upstreamModel, quotaMode string, inference bool) (*accountLease, error) {
	now := time.Now().UTC()
	values, err := s.loadCandidates(ctx, provider, upstreamModel, quotaMode, now)
	if err != nil {
		return nil, err
	}
	for _, candidate := range values {
		value := candidate.Credential
		if value.ID != accountID {
			continue
		}
		if !value.Enabled || value.AuthStatus != account.AuthStatusActive {
			return nil, &SelectionUnavailableError{Reason: SelectionNoAccounts}
		}
		if inference {
			if candidate.ModelCapabilityKnown && !candidate.SupportsModel {
				return nil, &SelectionUnavailableError{Reason: SelectionUnsupportedModel}
			}
			if candidate.ModelQuotaBlock != nil && now.Before(candidate.ModelQuotaBlock.CooldownUntil) {
				return nil, &SelectionUnavailableError{Reason: SelectionModelCooling, RetryAfter: retryDelay(now, candidate.ModelQuotaBlock.CooldownUntil)}
			}
			if value.CooldownUntil != nil && now.Before(*value.CooldownUntil) {
				return nil, &SelectionUnavailableError{Reason: SelectionCooling, RetryAfter: retryDelay(now, *value.CooldownUntil)}
			}
			if recovery := candidate.QuotaRecovery; recovery != nil && recovery.Status != account.QuotaRecoveryStatusActive {
				if recovery.NextProbeAt == nil || now.Before(*recovery.NextProbeAt) {
					var retryAfter time.Duration
					if recovery.NextProbeAt != nil {
						retryAfter = retryDelay(now, *recovery.NextProbeAt)
					}
					return nil, &SelectionUnavailableError{Reason: SelectionQuotaExhausted, RetryAfter: retryAfter}
				}
				lease, err := s.acquirePinnedCapacity(ctx, value)
				if err != nil {
					return nil, err
				}
				claimed, err := s.accounts.ClaimQuotaProbe(ctx, value.ID, now, now.Add(quotaProbeLease))
				if err != nil || !claimed {
					lease.Release()
					if err != nil {
						return nil, err
					}
					return nil, fmt.Errorf("绑定的上游账号恢复探测已被占用")
				}
				lease.QuotaProbe = true
				lease.QuotaProbeKind = recovery.Kind
				lease.Billing = candidate.Billing
				return lease, nil
			}
			if candidate.Billing != nil && candidate.Billing.IsExhausted(value.MinimumRemaining) {
				return nil, &SelectionUnavailableError{Reason: SelectionQuotaExhausted}
			}
			if candidate.QuotaWindow != nil && candidate.QuotaWindow.Remaining <= 0 {
				var retryAfter time.Duration
				if candidate.QuotaWindow.ResetAt != nil {
					retryAfter = retryDelay(now, *candidate.QuotaWindow.ResetAt)
				}
				return nil, &SelectionUnavailableError{Reason: SelectionQuotaExhausted, RetryAfter: retryAfter}
			}
		}
		lease, err := s.acquirePinnedCapacity(ctx, value)
		if err != nil {
			return nil, err
		}
		lease.Billing = candidate.Billing
		lease.QuotaMode = effectiveQuotaMode(candidate, quotaMode)
		return lease, nil
	}
	return nil, &SelectionUnavailableError{Reason: SelectionNoAccounts}
}

func effectiveQuotaMode(candidate account.RoutingCandidate, fallback string) string {
	if candidate.QuotaWindow != nil && candidate.QuotaWindow.Mode == "weekly" {
		return "weekly"
	}
	return fallback
}

func (s *Selector) MarkSuccess(ctx context.Context, credential account.Credential) {
	s.markSuccess(ctx, credential, true)
}

func (s *Selector) markSuccess(ctx context.Context, credential account.Credential, quotaProbe bool) {
	now := time.Now().UTC()
	persist := credential.FailureCount > 0 || credential.CooldownUntil != nil || credential.LastError != ""
	s.mu.Lock()
	if last := s.lastSuccessAt[credential.ID]; last.IsZero() || now.Sub(last) >= successPersistInterval {
		persist = true
	}
	if persist {
		s.lastSuccessAt[credential.ID] = now
	}
	s.mu.Unlock()
	if persist {
		_ = s.accounts.UpdateHealth(ctx, credential.ID, 0, nil, "", true)
	}
	if quotaProbe {
		_ = s.accounts.ClearQuotaRecovery(ctx, credential.ID)
	}
	if quotaProbe || credential.FailureCount > 0 || credential.CooldownUntil != nil || credential.LastError != "" {
		s.invalidateCandidates(credential.Provider)
	}
}

func (s *Selector) MarkFreeQuotaExhausted(ctx context.Context, credential account.Credential, used, limit int64) {
	now := time.Now().UTC()
	nextProbeAt := now.Add(24 * time.Hour)
	_ = s.accounts.SaveQuotaRecovery(ctx, account.QuotaRecovery{
		AccountID: credential.ID, Kind: account.QuotaRecoveryKindFree, Status: account.QuotaRecoveryStatusExhausted,
		ConfirmedUsed: used, ConfirmedLimit: limit, ExhaustedAt: &now,
		NextProbeAt: &nextProbeAt, LastConfirmedAt: &now, UpdatedAt: now,
	})
	_ = s.sticky.DeleteByAccount(ctx, credential.ID)
	s.invalidateCandidates(credential.Provider)
}

func (s *Selector) MarkModelQuotaExhausted(ctx context.Context, credential account.Credential, upstreamModel string, retryAfter time.Duration) {
	upstreamModel = strings.TrimSpace(upstreamModel)
	if upstreamModel == "" {
		s.MarkFreeQuotaExhausted(ctx, credential, 0, 0)
		return
	}
	if retryAfter <= 0 {
		retryAfter = 24 * time.Hour
	}
	until := time.Now().UTC().Add(retryAfter)
	_ = s.accounts.UpsertModelQuotaBlock(ctx, account.ModelQuotaBlock{
		AccountID: credential.ID, UpstreamModel: upstreamModel, Reason: "model_quota_depleted", CooldownUntil: until, UpdatedAt: time.Now().UTC(),
	})
	s.invalidateCandidates(credential.Provider)
}

// MarkPaidQuotaExhausted 使用已知真实账期将付费账号移出号池，到期后才允许 Billing 探测。
func (s *Selector) MarkPaidQuotaExhausted(ctx context.Context, credential account.Credential, billing *account.Billing) bool {
	if billing == nil || (billing.MonthlyLimit <= 0 && billing.OnDemandCap <= 0 && billing.OnDemandUsed <= 0 && billing.PrepaidBalance <= 0 && billing.CreditUsagePercent <= 0) {
		return false
	}
	periodEnd, ok := billing.PeriodEnd()
	if !ok {
		return false
	}
	now := time.Now().UTC()
	_ = s.accounts.SaveQuotaRecovery(ctx, account.QuotaRecovery{
		AccountID: credential.ID, Kind: account.QuotaRecoveryKindPaid, Status: account.QuotaRecoveryStatusExhausted,
		ExhaustedAt: &now, NextProbeAt: &periodEnd, LastConfirmedAt: &now, UpdatedAt: now,
	})
	_ = s.sticky.DeleteByAccount(ctx, credential.ID)
	s.invalidateCandidates(credential.Provider)
	return true
}

// MarkQuotaStateChanged 在 Billing 探测改变持久化额度状态后立即失效候选快照。
func (s *Selector) MarkQuotaStateChanged(provider account.Provider) { s.invalidateCandidates(provider) }

// ConsumeQuota 将成功请求的本地额度变化应用到候选快照，避免为单账号变化清空整个 Provider 缓存。
// Console 模式还会在 remaining 落到轮换阈值时启动恢复计时器，与持久化层延迟轮换策略一致。
func (s *Selector) ConsumeQuota(provider account.Provider, accountID uint64, mode string, amount int) {
	if accountID == 0 || mode == "" || mode == "weekly" || amount <= 0 {
		return
	}
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, snapshot := range s.candidates {
		if key.provider != provider {
			continue
		}
		for index := range snapshot.values {
			candidate := &snapshot.values[index]
			if candidate.Credential.ID != accountID || candidate.QuotaWindow == nil || candidate.QuotaWindow.Mode != mode {
				continue
			}
			window := *candidate.QuotaWindow
			window.Remaining = max(0, window.Remaining-amount)
			window.UpdatedAt = now
			// Mirror console delayed rotation in the hot-path cache (threshold=12).
			const consoleRotateThreshold = 12
			if mode == "console" && window.ResetAt == nil && window.WindowSeconds > 0 && window.Remaining <= consoleRotateThreshold {
				resetAt := now.Add(time.Duration(window.WindowSeconds) * time.Second)
				window.ResetAt = &resetAt
			}
			candidate.QuotaWindow = &window
		}
		s.candidates[key] = snapshot
	}
}

func (s *Selector) MarkFailure(ctx context.Context, credential account.Credential, status int, retryAfter time.Duration) {
	failureCount := credential.FailureCount + 1
	_, cooldownBase, cooldownMax, _ := s.routingConfig()
	cooldown := cooldownBase
	for i := 1; i < failureCount && cooldown < cooldownMax; i++ {
		cooldown *= 2
	}
	if cooldown > cooldownMax {
		cooldown = cooldownMax
	}
	if retryAfter > cooldown {
		cooldown = retryAfter
	}
	until := time.Now().UTC().Add(cooldown)
	_ = s.accounts.UpdateHealth(ctx, credential.ID, failureCount, &until, fmt.Sprintf("upstream status %d", status), false)
	s.invalidateCandidates(credential.Provider)
	if status == 401 || status == 402 || status == 403 || status == 429 {
		_ = s.sticky.DeleteByAccount(ctx, credential.ID)
	}
}

func (s *Selector) loadCandidates(ctx context.Context, provider account.Provider, upstreamModel, quotaMode string, now time.Time) ([]account.RoutingCandidate, error) {
	key := candidateCacheKey{provider: provider, upstreamModel: upstreamModel, quotaMode: quotaMode}
	s.mu.Lock()
	if snapshot, ok := s.candidates[key]; ok && now.Before(snapshot.expiresAt) {
		values := append([]account.RoutingCandidate(nil), snapshot.values...)
		s.mu.Unlock()
		return values, nil
	}
	s.mu.Unlock()
	loadKey := string(provider) + "\x00" + upstreamModel + "\x00" + quotaMode
	loaded, err, _ := s.candidateLoads.Do(loadKey, func() (any, error) {
		checkTime := time.Now().UTC()
		s.mu.Lock()
		if snapshot, ok := s.candidates[key]; ok && checkTime.Before(snapshot.expiresAt) {
			values := append([]account.RoutingCandidate(nil), snapshot.values...)
			s.mu.Unlock()
			return values, nil
		}
		s.mu.Unlock()
		values, err := s.accounts.ListRoutingCandidates(ctx, provider, upstreamModel, quotaMode)
		if err != nil {
			return nil, err
		}
		s.mu.Lock()
		s.candidates[key] = candidateSnapshot{values: append([]account.RoutingCandidate(nil), values...), expiresAt: checkTime.Add(candidateCacheTTL)}
		s.mu.Unlock()
		return values, nil
	})
	if err != nil {
		return nil, err
	}
	return append([]account.RoutingCandidate(nil), loaded.([]account.RoutingCandidate)...), nil
}

func (s *Selector) invalidateCandidates(provider account.Provider) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key := range s.candidates {
		if key.provider == provider {
			delete(s.candidates, key)
		}
	}
}

// InvalidateProvider drops the in-memory routing candidate snapshot for the provider.
// Useful after bulk account operations (delete all, import, priority changes) so the
// scheduler immediately sees the updated pool instead of waiting out the cache TTL.
func (s *Selector) InvalidateProvider(provider account.Provider) {
	s.invalidateCandidates(provider)
}

func (s *Selector) claimAccountSlot(ctx context.Context, value account.Credential) (*accountLease, error) {
	limit := accountConcurrencyLimit(value)
	release, acquired, err := s.concurrency.Acquire(ctx, accountConcurrencyKey(value.ID), limit)
	if err != nil {
		return nil, fmt.Errorf("获取账号并发租约: %w", err)
	}
	if !acquired {
		return nil, nil
	}
	s.mu.Lock()
	s.lastSelectedAt[value.ID] = time.Now().UTC()
	s.mu.Unlock()
	return &accountLease{Credential: value, release: func() {
		release()
		s.announceLeaseReturn()
	}}, nil
}

func (s *Selector) acquirePinnedCapacity(ctx context.Context, value account.Credential) (*accountLease, error) {
	_, _, _, capacityWait := s.routingConfig()
	deadline := time.Now().Add(capacityWait)
	for {
		lease, err := s.claimAccountSlot(ctx, value)
		if err != nil || lease != nil {
			return lease, err
		}
		if capacityWait <= 0 {
			return nil, &SelectionUnavailableError{Reason: SelectionSaturated, RetryAfter: time.Second}
		}
		retry, err := s.awaitLeaseRetry(ctx, deadline)
		if err != nil {
			return nil, err
		}
		if !retry {
			return nil, &SelectionUnavailableError{Reason: SelectionSaturated, RetryAfter: time.Second}
		}
	}
}

func (s *Selector) leaseReturnNotice() <-chan struct{} {
	s.leaseWakeMu.Lock()
	defer s.leaseWakeMu.Unlock()
	if s.leaseWake == nil {
		s.leaseWake = make(chan struct{})
	}
	return s.leaseWake
}

func (s *Selector) announceLeaseReturn() {
	s.leaseWakeMu.Lock()
	if s.leaseWake != nil {
		close(s.leaseWake)
	}
	s.leaseWake = make(chan struct{})
	s.leaseWakeMu.Unlock()
}

// awaitLeaseRetry 在本实例归还租约时立即重试；短轮询用于感知其他实例释放的共享并发名额。
func (s *Selector) awaitLeaseRetry(ctx context.Context, deadline time.Time) (bool, error) {
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return false, nil
	}
	notice := s.leaseReturnNotice()
	timer := time.NewTimer(min(remaining, 100*time.Millisecond))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case <-notice:
		return true, nil
	case <-timer.C:
		return time.Now().Before(deadline), nil
	}
}

func earlierFuture(current, candidate, now time.Time) time.Time {
	if candidate.IsZero() || !now.Before(candidate) {
		return current
	}
	if current.IsZero() || candidate.Before(current) {
		return candidate
	}
	return current
}

func retryDelay(now, retryAt time.Time) time.Duration {
	if retryAt.IsZero() || !now.Before(retryAt) {
		return 0
	}
	return retryAt.Sub(now)
}

func (s *Selector) resolveTierOrder(provider account.Provider, upstreamModel string) []account.WebTier {
	if s.tierOrders == nil {
		return nil
	}
	return s.tierOrders.TierOrder(provider, upstreamModel)
}

func tierOrderRank(order []account.WebTier, tier account.WebTier) int {
	for index, value := range order {
		if value == tier {
			return index
		}
	}
	return len(order)
}
