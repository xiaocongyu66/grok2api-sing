package dashboard

import (
	"context"
	"testing"
	"time"

	dashboarddomain "github.com/chenyme/grok2api/backend/internal/domain/dashboard"
)

func TestGetBuildsStableBucketsAndSuccessRate(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 34, 56, 0, time.UTC)
	repository := &dashboardRepositoryStub{aggregate: dashboarddomain.Aggregate{
		Usage:        dashboarddomain.Usage{Requests: 4, SuccessfulRequests: 3, FailedRequests: 1, Tokens: 120},
		Buckets:      []dashboarddomain.Bucket{{Index: 0, Requests: 1, Tokens: 20}, {Index: 23, Requests: 3, Tokens: 100}},
		TopModels:    []dashboarddomain.ModelUsage{{Model: "grok-test", Requests: 4, Tokens: 120}},
		ModelBuckets: []dashboarddomain.ModelBucket{{Index: 23, Model: "grok-test", Tokens: 100, BilledCostUSDTicks: 20}},
	}}
	service := NewService(repository)
	service.now = func() time.Time { return now }

	result, err := service.Get(context.Background(), "24h", "UTC", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Period != Period24Hours || result.SuccessRate != 75 || len(result.Series) != 24 {
		t.Fatalf("result = %#v", result)
	}
	if result.Series[0].Requests != 1 || result.Series[23].Tokens != 100 {
		t.Fatalf("series = %#v", result.Series)
	}
	if len(result.TopModels) != 1 || result.TopModels[0].Model != "grok-test" || result.TopModels[0].Requests != 4 {
		t.Fatalf("top models = %#v", result.TopModels)
	}
	if len(result.Series[23].Models) != 1 || result.Series[23].Models[0].Model != "grok-test" || result.Series[23].Models[0].Tokens != 100 {
		t.Fatalf("model series = %#v", result.Series[23].Models)
	}
	if !result.Range.Start.Equal(time.Date(2026, 7, 10, 13, 0, 0, 0, time.UTC)) || !result.Range.End.Equal(time.Date(2026, 7, 11, 13, 0, 0, 0, time.UTC)) {
		t.Fatalf("range = %#v", result.Range)
	}
	if !result.GeneratedAt.Equal(now) || !result.Series[23].Start.Equal(time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)) || !result.Series[23].End.Equal(time.Date(2026, 7, 11, 13, 0, 0, 0, time.UTC)) {
		t.Fatalf("current hour = %#v", result.Series[23])
	}
}

func TestGetUsesPeriodSpecificBucketCounts(t *testing.T) {
	for period, expected := range map[string]int{"24h": 24, "7d": 7, "30d": 30, "90d": 6} {
		repository := &dashboardRepositoryStub{}
		service := NewService(repository)
		if _, err := service.Get(context.Background(), period, "UTC", "", ""); err != nil {
			t.Fatalf("period %s: %v", period, err)
		}
		if repository.bucketCount != expected {
			t.Fatalf("period %s bucket count = %d, want %d", period, repository.bucketCount, expected)
		}
	}
}

func TestGetCachesRepeatedAggregate(t *testing.T) {
	repository := &dashboardRepositoryStub{}
	service := NewService(repository)
	service.now = func() time.Time { return time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC) }

	if _, err := service.Get(context.Background(), "24h", "UTC", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Get(context.Background(), "24h", "UTC", "", ""); err != nil {
		t.Fatal(err)
	}
	if repository.calls != 1 {
		t.Fatalf("snapshot calls = %d", repository.calls)
	}
}

func TestRefreshBypassesAggregateCache(t *testing.T) {
	repository := &dashboardRepositoryStub{}
	service := NewService(repository)
	service.now = func() time.Time { return time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC) }

	if _, err := service.Get(context.Background(), "24h", "UTC", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Refresh(context.Background(), "24h", "UTC", "", ""); err != nil {
		t.Fatal(err)
	}
	if repository.calls != 2 {
		t.Fatalf("snapshot calls = %d", repository.calls)
	}
}

func TestGetAlignsDailyBucketsToCalendarDays(t *testing.T) {
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	now := time.Date(2026, 7, 11, 12, 34, 56, 0, location)
	repository := &dashboardRepositoryStub{}
	service := NewService(repository)
	service.now = func() time.Time { return now }

	result, err := service.Get(context.Background(), "7d", "Asia/Shanghai", "", "")
	if err != nil {
		t.Fatal(err)
	}
	wantStart := time.Date(2026, 7, 5, 0, 0, 0, 0, location).UTC()
	wantEnd := time.Date(2026, 7, 12, 0, 0, 0, 0, location).UTC()
	if !result.Range.Start.Equal(wantStart) || !result.Range.End.Equal(wantEnd) {
		t.Fatalf("range = %#v", result.Range)
	}
	if !result.Series[6].Start.Equal(time.Date(2026, 7, 11, 0, 0, 0, 0, location).UTC()) || !result.Series[6].End.Equal(wantEnd) {
		t.Fatalf("today bucket = %#v", result.Series[6])
	}
}

func TestGetUsesFifteenDayBucketsFor90Days(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 34, 56, 0, time.UTC)
	service := NewService(&dashboardRepositoryStub{})
	service.now = func() time.Time { return now }

	result, err := service.Get(context.Background(), "90d", "UTC", "", "")
	if err != nil {
		t.Fatal(err)
	}
	for index, bucket := range result.Series {
		if bucket.End.Sub(bucket.Start) != 15*24*time.Hour {
			t.Fatalf("bucket %d duration = %s", index, bucket.End.Sub(bucket.Start))
		}
	}
}

func TestGetRejectsUnknownPeriod(t *testing.T) {
	service := NewService(&dashboardRepositoryStub{})
	if _, err := service.Get(context.Background(), "365d", "UTC", "", ""); err != ErrInvalidPeriod {
		t.Fatalf("err = %v", err)
	}
}

func TestGetUsesCalendarBoundariesAcrossDST(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	service := NewService(&dashboardRepositoryStub{})
	service.now = func() time.Time { return now }

	result, err := service.Get(context.Background(), "7d", "America/New_York", "", "")
	if err != nil {
		t.Fatal(err)
	}
	var foundShortDay bool
	for _, bucket := range result.Series {
		if bucket.End.Sub(bucket.Start) == 23*time.Hour {
			foundShortDay = true
		}
	}
	if !foundShortDay {
		t.Fatalf("DST transition was not represented: %#v", result.Series)
	}
}

func TestGetRejectsInvalidTimezone(t *testing.T) {
	service := NewService(&dashboardRepositoryStub{})
	if _, err := service.Get(context.Background(), "24h", "Mars/Olympus", "", ""); err != ErrInvalidTimezone {
		t.Fatalf("err = %v", err)
	}
}

func TestGetCustomRangeBuildsSeriesAndPassesLiveWindow(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	repository := &dashboardRepositoryStub{
		aggregate: dashboarddomain.Aggregate{
			LiveRates: dashboarddomain.LiveRates{RPM: 3, TPM: 900, WindowSeconds: 60},
			Today:     dashboarddomain.DayUsage{Requests: 12, Tokens: 4000, Start: "2026-07-15T00:00:00Z", End: "2026-07-15T12:00:00Z"},
			Usage:     dashboarddomain.Usage{Requests: 100, SuccessfulRequests: 90, FailedRequests: 10, Tokens: 50000},
		},
	}
	service := NewService(repository)
	service.now = func() time.Time { return now }

	result, err := service.Get(context.Background(), "custom", "UTC", "2020-01-01", "2020-01-03")
	if err != nil {
		t.Fatal(err)
	}
	if result.Period != PeriodCustom {
		t.Fatalf("period = %s", result.Period)
	}
	if len(result.Series) == 0 {
		t.Fatal("expected custom series buckets")
	}
	if !result.Range.Start.Equal(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("start = %v", result.Range.Start)
	}
	// date-only end is exclusive next day
	if !result.Range.End.Equal(time.Date(2020, 1, 4, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("end = %v", result.Range.End)
	}
	if repository.liveWindow != time.Minute {
		t.Fatalf("liveWindow = %s", repository.liveWindow)
	}
	if result.LiveRates.RPM != 3 || result.LiveRates.TPM != 900 {
		t.Fatalf("liveRates = %#v", result.LiveRates)
	}
	if result.Today.Requests != 12 || result.Today.Tokens != 4000 {
		t.Fatalf("today = %#v", result.Today)
	}
	dayStart := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	if !repository.todayStart.Equal(dayStart) || !repository.todayEnd.Equal(now) {
		t.Fatalf("today window = %v .. %v", repository.todayStart, repository.todayEnd)
	}
}

func TestGetCustomRangeRejectsOutOfBounds(t *testing.T) {
	service := NewService(&dashboardRepositoryStub{})
	service.now = func() time.Time { return time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC) }
	cases := []struct {
		start, end string
	}{
		{"2008-12-31", "2010-01-01"},
		{"2020-01-01", "2031-01-01"},
		{"2020-01-02", "2020-01-01"},
		{"", "2020-01-01"},
	}
	for _, tc := range cases {
		if _, err := service.Get(context.Background(), "custom", "UTC", tc.start, tc.end); err != ErrInvalidRange {
			t.Fatalf("start=%s end=%s err=%v", tc.start, tc.end, err)
		}
	}
}

func TestGetCustomRangeAcceptsRFC3339AndBounds(t *testing.T) {
	service := NewService(&dashboardRepositoryStub{})
	service.now = func() time.Time { return time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC) }
	result, err := service.Get(context.Background(), "custom", "UTC", "2009-01-01T00:00:00Z", "2030-12-31T23:59:59Z")
	if err != nil {
		t.Fatal(err)
	}
	if result.Period != PeriodCustom || len(result.Series) == 0 {
		t.Fatalf("result = %#v", result)
	}
	// Date-only end of 2030-12-31 is exclusive 2031-01-01 and must cover full span.
	result, err = service.Get(context.Background(), "custom", "UTC", "2009-01-01", "2030-12-31")
	if err != nil {
		t.Fatal(err)
	}
	wantEnd := time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC)
	if !result.Range.Start.Equal(time.Date(2009, 1, 1, 0, 0, 0, 0, time.UTC)) || !result.Range.End.Equal(wantEnd) {
		t.Fatalf("range = %#v", result.Range)
	}
	if len(result.Series) == 0 || len(result.Series) > 120 {
		t.Fatalf("series len = %d", len(result.Series))
	}
	if !result.Series[len(result.Series)-1].End.Equal(wantEnd) {
		t.Fatalf("last series end = %v", result.Series[len(result.Series)-1].End)
	}
}

type dashboardRepositoryStub struct {
	aggregate   dashboarddomain.Aggregate
	bucketCount int
	calls       int
	liveWindow  time.Duration
	todayStart  time.Time
	todayEnd    time.Time
}

func (s *dashboardRepositoryStub) Snapshot(_ context.Context, boundaries []time.Time, _ time.Time, todayStart, todayEnd time.Time, liveWindow time.Duration) (dashboarddomain.Aggregate, error) {
	s.calls++
	s.bucketCount = len(boundaries) - 1
	s.liveWindow = liveWindow
	s.todayStart = todayStart
	s.todayEnd = todayEnd
	return s.aggregate, nil
}
