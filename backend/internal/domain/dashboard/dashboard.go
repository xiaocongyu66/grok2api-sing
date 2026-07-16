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
// RPM/TPM use float64 so low-traffic long ranges (e.g. 2039 req / 30d ≈ 0.05 RPM)
// are not rounded to zero.
type LiveRates struct {
	// RPM is requests/min (or raw request count when WindowSeconds ≤ 120).
	RPM float64
	// TPM is tokens/min (or raw token count when WindowSeconds ≤ 120).
	TPM float64
	// WindowSeconds is the observation window length in seconds.
	WindowSeconds int
}

// Connections is live gateway concurrency (not period-scoped).
type Connections struct {
	// Active is in-flight authenticated /v1 requests right now.
	Active int64
	// Peak is the highest Active since process start (or shared Redis peak).
	Peak int64
	// Total is cumulative accepted API connections since start.
	Total int64
	// Clients is live per-client active counts (e.g. codex:50).
	Clients []ClientUsage
}

// ClientUsage is request count for one detected downstream client type in the period.
type ClientUsage struct {
	Client string // stable id: codex, claude_code, hermes, …
	Label  string // short display label: Codex, Claude Code, …
	Count  int64
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
	Clients      []ClientUsage
}
