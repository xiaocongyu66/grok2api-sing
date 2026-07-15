package dashboard

import (
	"context"
	"errors"
	"strings"
	"time"

	dashboarddomain "github.com/chenyme/grok2api/backend/internal/domain/dashboard"
	"github.com/chenyme/grok2api/backend/internal/pkg/resultcache"
	"github.com/chenyme/grok2api/backend/internal/repository"
)

var ErrInvalidPeriod = errors.New("Dashboard 时间范围无效")
var ErrInvalidTimezone = errors.New("Dashboard 时区无效")
var ErrInvalidRange = errors.New("Dashboard 自定义时间范围无效")

const dashboardCacheTTL = 15 * time.Second
const liveRateWindow = time.Minute

type Period string

const (
	Period24Hours Period = "24h"
	Period7Days   Period = "7d"
	Period30Days  Period = "30d"
	Period90Days  Period = "90d"
	PeriodCustom  Period = "custom"
)

type Range struct {
	Start time.Time
	End   time.Time
}

type SeriesPoint struct {
	Start              time.Time
	End                time.Time
	Requests           int64
	InputTokens        int64
	CachedInputTokens  int64
	OutputTokens       int64
	ReasoningTokens    int64
	Tokens             int64
	BilledCostUSDTicks int64
	Models             []ModelBucket
}

type ModelBucket struct {
	Model              string
	Tokens             int64
	BilledCostUSDTicks int64
}

type ModelUsage struct {
	Model              string
	Requests           int64
	InputTokens        int64
	CachedInputTokens  int64
	OutputTokens       int64
	ReasoningTokens    int64
	Tokens             int64
	BilledCostUSDTicks int64
}

type Result struct {
	Period      Period
	GeneratedAt time.Time
	Range       Range
	Resources   dashboarddomain.Resources
	Usage       dashboarddomain.Usage
	SuccessRate float64
	LiveRates   dashboarddomain.LiveRates
	Today       dashboarddomain.DayUsage
	Series      []SeriesPoint
	TopModels   []ModelUsage
}

// Service 负责 Dashboard 时间范围校验和固定时间桶编排。
type Service struct {
	dashboard repository.DashboardRepository
	now       func() time.Time
	cache     *resultcache.Cache[string, Result]
}

func NewService(dashboard repository.DashboardRepository) *Service {
	return &Service{dashboard: dashboard, now: time.Now, cache: resultcache.New[string, Result](32, dashboardCacheTTL)}
}

// Get 返回指定时间范围的 Dashboard 聚合快照。
// customStart/customEnd are RFC3339 timestamps used when period=custom.
func (s *Service) Get(ctx context.Context, rawPeriod, rawTimezone, customStart, customEnd string) (Result, error) {
	return s.get(ctx, rawPeriod, rawTimezone, customStart, customEnd, true)
}

// Refresh 绕过短缓存，供管理员显式刷新时读取最新聚合数据。
func (s *Service) Refresh(ctx context.Context, rawPeriod, rawTimezone, customStart, customEnd string) (Result, error) {
	return s.get(ctx, rawPeriod, rawTimezone, customStart, customEnd, false)
}

func (s *Service) get(ctx context.Context, rawPeriod, rawTimezone, customStart, customEnd string, useCache bool) (Result, error) {
	period, bucketCount, bucketDays, err := parsePeriod(rawPeriod)
	if err != nil {
		return Result{}, err
	}
	location, err := parseTimezone(rawTimezone)
	if err != nil {
		return Result{}, err
	}
	rawNow := s.now()
	var customRange *Range
	if period == PeriodCustom {
		r, rangeErr := parseCustomRange(customStart, customEnd, location, rawNow)
		if rangeErr != nil {
			return Result{}, rangeErr
		}
		customRange = &r
	}
	if !useCache {
		return s.load(ctx, period, bucketCount, bucketDays, location, rawNow, customRange)
	}
	cacheKey := string(period) + "\x00" + location.String()
	if customRange != nil {
		cacheKey += "\x00" + customRange.Start.UTC().Format(time.RFC3339) + "\x00" + customRange.End.UTC().Format(time.RFC3339)
	}
	return s.cache.Load(ctx, cacheKey, rawNow, func() (Result, error) {
		return s.load(ctx, period, bucketCount, bucketDays, location, rawNow, customRange)
	})
}

func (s *Service) load(ctx context.Context, period Period, bucketCount, bucketDays int, location *time.Location, rawNow time.Time, custom *Range) (Result, error) {
	now := rawNow.In(location)
	var series []SeriesPoint
	if period == PeriodCustom && custom != nil {
		series = buildCustomSeries(custom.Start.In(location), custom.End.In(location))
	} else {
		series = buildSeries(now, period, bucketCount, bucketDays)
	}
	if len(series) == 0 {
		return Result{}, ErrInvalidPeriod
	}
	boundaries := make([]time.Time, 0, len(series)+1)
	for _, point := range series {
		boundaries = append(boundaries, point.Start)
	}
	boundaries = append(boundaries, series[len(series)-1].End)
	generatedAt := rawNow.UTC()
	// Calendar day in admin timezone: local midnight → now (or custom end if still today).
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, location)
	dayEnd := now
	if !dayEnd.After(dayStart) {
		dayEnd = dayStart.Add(time.Second)
	}
	aggregate, err := s.dashboard.Snapshot(ctx, boundaries, generatedAt, dayStart.UTC(), dayEnd.UTC(), liveRateWindow)
	if err != nil {
		return Result{}, err
	}
	for _, bucket := range aggregate.Buckets {
		if bucket.Index < 0 || bucket.Index >= len(series) {
			continue
		}
		series[bucket.Index].Requests = bucket.Requests
		series[bucket.Index].InputTokens = bucket.InputTokens
		series[bucket.Index].CachedInputTokens = bucket.CachedInputTokens
		series[bucket.Index].OutputTokens = bucket.OutputTokens
		series[bucket.Index].ReasoningTokens = bucket.ReasoningTokens
		series[bucket.Index].Tokens = bucket.Tokens
		series[bucket.Index].BilledCostUSDTicks = bucket.BilledCostUSDTicks
	}
	for _, bucket := range aggregate.ModelBuckets {
		if bucket.Index < 0 || bucket.Index >= len(series) {
			continue
		}
		series[bucket.Index].Models = append(series[bucket.Index].Models, ModelBucket{Model: bucket.Model, Tokens: bucket.Tokens, BilledCostUSDTicks: bucket.BilledCostUSDTicks})
	}
	successRate := 0.0
	if aggregate.Usage.Requests > 0 {
		successRate = float64(aggregate.Usage.SuccessfulRequests) / float64(aggregate.Usage.Requests) * 100
	}
	topModels := make([]ModelUsage, 0, len(aggregate.TopModels))
	for _, item := range aggregate.TopModels {
		topModels = append(topModels, ModelUsage{Model: item.Model, Requests: item.Requests, InputTokens: item.InputTokens, CachedInputTokens: item.CachedInputTokens, OutputTokens: item.OutputTokens, ReasoningTokens: item.ReasoningTokens, Tokens: item.Tokens, BilledCostUSDTicks: item.BilledCostUSDTicks})
	}
	return Result{
		Period: period, GeneratedAt: generatedAt,
		Range:     Range{Start: boundaries[0], End: boundaries[len(boundaries)-1]},
		Resources: aggregate.Resources, Usage: aggregate.Usage, SuccessRate: successRate,
		LiveRates: aggregate.LiveRates, Today: aggregate.Today,
		Series: series, TopModels: topModels,
	}, nil
}

func parseTimezone(value string) (*time.Location, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		value = "UTC"
	}
	if value == "Local" || len(value) > 128 {
		return nil, ErrInvalidTimezone
	}
	location, err := time.LoadLocation(value)
	if err != nil {
		return nil, ErrInvalidTimezone
	}
	return location, nil
}

func parsePeriod(value string) (Period, int, int, error) {
	if value == "" {
		value = string(Period24Hours)
	}
	switch Period(value) {
	case Period24Hours:
		return Period24Hours, 24, 0, nil
	case Period7Days:
		return Period7Days, 7, 1, nil
	case Period30Days:
		return Period30Days, 30, 1, nil
	case Period90Days:
		return Period90Days, 6, 15, nil
	case PeriodCustom:
		return PeriodCustom, 0, 0, nil
	default:
		return "", 0, 0, ErrInvalidPeriod
	}
}

// parseCustomRange accepts RFC3339 (or date-only YYYY-MM-DD) in [2009-01-01, 2030-12-31].
func parseCustomRange(rawStart, rawEnd string, location *time.Location, now time.Time) (Range, error) {
	start, err := parseFlexibleTime(rawStart, location, false)
	if err != nil {
		return Range{}, ErrInvalidRange
	}
	end, err := parseFlexibleTime(rawEnd, location, true)
	if err != nil {
		return Range{}, ErrInvalidRange
	}
	if !end.After(start) {
		return Range{}, ErrInvalidRange
	}
	// Guardrails: allow historical from 2009 and through end of 2030 (exclusive end may be 2031-01-01).
	minStart := time.Date(2009, 1, 1, 0, 0, 0, 0, location)
	maxEnd := time.Date(2031, 1, 1, 0, 0, 0, 0, location)
	if start.Before(minStart) || end.After(maxEnd) {
		return Range{}, ErrInvalidRange
	}
	// Cap span to the full allowed window (2009–2030 inclusive, leap years).
	if end.Sub(start) > 23*365*24*time.Hour {
		return Range{}, ErrInvalidRange
	}
	_ = now
	return Range{Start: start.UTC(), End: end.UTC()}, nil
}

func parseFlexibleTime(raw string, location *time.Location, endOfDay bool) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, ErrInvalidRange
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.In(location), nil
	}
	if t, err := time.ParseInLocation("2006-01-02T15:04:05", raw, location); err == nil {
		return t, nil
	}
	if t, err := time.ParseInLocation("2006-01-02", raw, location); err == nil {
		if endOfDay {
			// Exclusive end: next day 00:00 so full calendar day is included.
			return t.AddDate(0, 0, 1), nil
		}
		return t, nil
	}
	return time.Time{}, ErrInvalidRange
}

func alignedRange(now time.Time, period Period) (time.Time, time.Time) {
	location := now.Location()
	if period == Period24Hours {
		currentHour := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 0, 0, 0, location)
		return currentHour.Add(-23 * time.Hour), currentHour.Add(time.Hour)
	}
	tomorrow := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, location)
	switch period {
	case Period7Days:
		return tomorrow.AddDate(0, 0, -7), tomorrow
	case Period30Days:
		return tomorrow.AddDate(0, 0, -30), tomorrow
	default:
		return tomorrow.AddDate(0, 0, -90), tomorrow
	}
}

func buildSeries(now time.Time, period Period, bucketCount, bucketDays int) []SeriesPoint {
	start, _ := alignedRange(now, period)
	series := make([]SeriesPoint, bucketCount)
	for index := range series {
		bucketStart := start.AddDate(0, 0, index*bucketDays)
		bucketEnd := bucketStart.AddDate(0, 0, bucketDays)
		if period == Period24Hours {
			bucketStart = start.Add(time.Duration(index) * time.Hour)
			bucketEnd = bucketStart.Add(time.Hour)
		}
		series[index] = SeriesPoint{Start: bucketStart.UTC(), End: bucketEnd.UTC()}
	}
	return series
}

// buildCustomSeries picks a bucket size so charts stay under ~120 points
// while always covering the full [start, end) range (usage totals stay correct).
func buildCustomSeries(start, end time.Time) []SeriesPoint {
	if !end.After(start) {
		return nil
	}
	const maxBuckets = 120
	duration := end.Sub(start)
	var step time.Duration
	switch {
	case duration <= 48*time.Hour:
		step = time.Hour
	case duration <= 90*24*time.Hour:
		step = 24 * time.Hour
	case duration <= 3*365*24*time.Hour:
		step = 7 * 24 * time.Hour
	case duration <= 10*365*24*time.Hour:
		step = 30 * 24 * time.Hour
	default:
		step = 90 * 24 * time.Hour
	}
	// Ensure we never exceed maxBuckets by widening the step if needed.
	if step > 0 {
		needed := int(duration/step) + 1
		if needed > maxBuckets {
			step = duration / time.Duration(maxBuckets-1)
			if step < time.Hour {
				step = time.Hour
			}
		}
	}
	series := make([]SeriesPoint, 0, maxBuckets)
	cursor := start
	for cursor.Before(end) {
		next := cursor.Add(step)
		if next.After(end) || len(series) == maxBuckets-1 {
			next = end
		}
		if !next.After(cursor) {
			break
		}
		series = append(series, SeriesPoint{Start: cursor.UTC(), End: next.UTC()})
		cursor = next
		if len(series) >= maxBuckets {
			break
		}
	}
	// Safety: if loop stopped early, force a final bucket to the real end.
	if len(series) > 0 && series[len(series)-1].End.Before(end.UTC()) {
		last := &series[len(series)-1]
		if len(series) < maxBuckets {
			series = append(series, SeriesPoint{Start: last.End, End: end.UTC()})
		} else {
			last.End = end.UTC()
		}
	}
	return series
}
