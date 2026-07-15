package dashboard

// Resources 表示 Dashboard 所需的资源总量与可用量。
type Resources struct {
	ActiveAccounts   int64
	TotalAccounts    int64
	EnabledModels    int64
	TotalModels      int64
	ActiveClientKeys int64
	TotalClientKeys  int64
	AllTimeRequests  int64
}

// Usage 表示指定时间窗口内的请求聚合。
type Usage struct {
	Requests           int64
	SuccessfulRequests int64
	FailedRequests     int64
	InputTokens        int64
	CachedInputTokens  int64
	OutputTokens       int64
	ReasoningTokens    int64
	Tokens             int64
	BilledCostUSDTicks int64
}

// LiveRates is site-wide traffic over the selected dashboard period.
// For short windows (≤2 minutes) values are raw counts (new-api style).
// For longer ranges RPM/TPM are average per-minute rates across the period.
type LiveRates struct {
	// RPM is requests/min (or raw request count when WindowSeconds ≤ 120).
	RPM int64
	// TPM is tokens/min (or raw token count when WindowSeconds ≤ 120).
	TPM int64
	// WindowSeconds is the observation window length in seconds.
	WindowSeconds int
}

// DayUsage is totals for the selected dashboard period (same window as Usage).
type DayUsage struct {
	Requests int64
	Tokens   int64
	Start    string // RFC3339 for clarity in API
	End      string
}

// Bucket 表示一个固定时间桶内的请求和 token 数量。
type Bucket struct {
	Index              int
	Requests           int64
	InputTokens        int64
	CachedInputTokens  int64
	OutputTokens       int64
	ReasoningTokens    int64
	Tokens             int64
	BilledCostUSDTicks int64
}

// ModelUsage 表示指定时间范围内按公开模型聚合的调用量。
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

// ModelBucket 表示单个时间桶内某个模型的用量。
type ModelBucket struct {
	Index              int
	Model              string
	Tokens             int64
	BilledCostUSDTicks int64
}

// Aggregate 表示持久化层返回的 Dashboard 聚合快照。
type Aggregate struct {
	Resources    Resources
	Usage        Usage
	LiveRates    LiveRates
	Today        DayUsage
	Buckets      []Bucket
	TopModels    []ModelUsage
	ModelBuckets []ModelBucket
}
