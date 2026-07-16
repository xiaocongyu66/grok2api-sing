package dashboard

import (
	"errors"
	"net/http"
	"time"

	dashboardapp "github.com/chenyme/grok2api/backend/internal/application/dashboard"
	"github.com/chenyme/grok2api/backend/internal/shared/response"
	"github.com/gin-gonic/gin"
)

type Handler struct{ service *dashboardapp.Service }

func NewHandler(service *dashboardapp.Service) *Handler { return &Handler{service: service} }

func (h *Handler) Register(router *gin.RouterGroup) { router.GET("/dashboard", h.get) }

type responseDTO struct {
	Period      string           `json:"period"`
	GeneratedAt time.Time        `json:"generatedAt"`
	Range       rangeDTO         `json:"range"`
	Resources   resourcesDTO     `json:"resources"`
	Usage       usageDTO         `json:"usage"`
	LiveRates   liveRatesDTO     `json:"liveRates"`
	Today       dayUsageDTO      `json:"today"`
	Series      []seriesDTO      `json:"series"`
	TopModels   []modelUsageDTO  `json:"topModels"`
	Clients     []clientUsageDTO `json:"clients"`
	Connections connectionsDTO   `json:"connections"`
}

type liveRatesDTO struct {
	RPM           float64 `json:"rpm"`
	TPM           float64 `json:"tpm"`
	WindowSeconds int     `json:"windowSeconds"`
}

// connectionsDTO is live gateway concurrency (not period-scoped).
type connectionsDTO struct {
	Active  int64            `json:"active"`
	Peak    int64            `json:"peak"`
	Total   int64            `json:"total"`
	Clients []clientUsageDTO `json:"clients"`
}

type clientUsageDTO struct {
	Client string `json:"client"`
	Label  string `json:"label"`
	Count  int64  `json:"count"`
}

type dayUsageDTO struct {
	Requests int64  `json:"requests"`
	Tokens   int64  `json:"tokens"`
	Start    string `json:"start"`
	End      string `json:"end"`
}

type rangeDTO struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

type resourcesDTO struct {
	ActiveAccounts   int64 `json:"activeAccounts"`
	TotalAccounts    int64 `json:"totalAccounts"`
	EnabledModels    int64 `json:"enabledModels"`
	TotalModels      int64 `json:"totalModels"`
	ActiveClientKeys int64 `json:"activeClientKeys"`
	TotalClientKeys  int64 `json:"totalClientKeys"`
	AllTimeRequests  int64 `json:"allTimeRequests"`
}

type usageDTO struct {
	Requests           int64   `json:"requests"`
	SuccessfulRequests int64   `json:"successfulRequests"`
	FailedRequests     int64   `json:"failedRequests"`
	InputTokens        int64   `json:"inputTokens"`
	CachedInputTokens  int64   `json:"cachedInputTokens"`
	OutputTokens       int64   `json:"outputTokens"`
	ReasoningTokens    int64   `json:"reasoningTokens"`
	Tokens             int64   `json:"tokens"`
	BilledCostUSDTicks int64   `json:"billedCostUsdTicks"`
	SuccessRate        float64 `json:"successRate"`
}

type seriesDTO struct {
	Start              time.Time        `json:"start"`
	End                time.Time        `json:"end"`
	Requests           int64            `json:"requests"`
	InputTokens        int64            `json:"inputTokens"`
	CachedInputTokens  int64            `json:"cachedInputTokens"`
	OutputTokens       int64            `json:"outputTokens"`
	ReasoningTokens    int64            `json:"reasoningTokens"`
	Tokens             int64            `json:"tokens"`
	BilledCostUSDTicks int64            `json:"billedCostUsdTicks"`
	Models             []modelBucketDTO `json:"models"`
}

type modelBucketDTO struct {
	Model              string `json:"model"`
	Tokens             int64  `json:"tokens"`
	BilledCostUSDTicks int64  `json:"billedCostUsdTicks"`
}

type modelUsageDTO struct {
	Model              string `json:"model"`
	Requests           int64  `json:"requests"`
	InputTokens        int64  `json:"inputTokens"`
	CachedInputTokens  int64  `json:"cachedInputTokens"`
	OutputTokens       int64  `json:"outputTokens"`
	ReasoningTokens    int64  `json:"reasoningTokens"`
	Tokens             int64  `json:"tokens"`
	BilledCostUSDTicks int64  `json:"billedCostUsdTicks"`
}

func (h *Handler) get(c *gin.Context) {
	period := c.Query("period")
	timezone := c.Query("timezone")
	start := c.Query("start")
	end := c.Query("end")
	var (
		result dashboardapp.Result
		err    error
	)
	if c.Query("refresh") == "1" {
		result, err = h.service.Refresh(c.Request.Context(), period, timezone, start, end)
	} else {
		result, err = h.service.Get(c.Request.Context(), period, timezone, start, end)
	}
	if errors.Is(err, dashboardapp.ErrInvalidPeriod) {
		response.Error(c, http.StatusBadRequest, "invalidDashboardPeriod", "period 仅支持 24h、7d、30d、90d、custom")
		return
	}
	if errors.Is(err, dashboardapp.ErrInvalidRange) {
		response.Error(c, http.StatusBadRequest, "invalidDashboardRange", "自定义时间须在 2009-01-01 至 2030-12-31 之间，且结束时间晚于开始时间")
		return
	}
	if errors.Is(err, dashboardapp.ErrInvalidTimezone) {
		response.Error(c, http.StatusBadRequest, "invalidDashboardTimezone", "timezone 必须是有效的 IANA 时区")
		return
	}
	if err != nil {
		response.Error(c, http.StatusInternalServerError, "dashboardLoadFailed", "读取 Dashboard 失败")
		return
	}
	series := make([]seriesDTO, 0, len(result.Series))
	for _, point := range result.Series {
		models := make([]modelBucketDTO, 0, len(point.Models))
		for _, item := range point.Models {
			models = append(models, modelBucketDTO{Model: item.Model, Tokens: item.Tokens, BilledCostUSDTicks: item.BilledCostUSDTicks})
		}
		series = append(series, seriesDTO{Start: point.Start, End: point.End, Requests: point.Requests, InputTokens: point.InputTokens, CachedInputTokens: point.CachedInputTokens, OutputTokens: point.OutputTokens, ReasoningTokens: point.ReasoningTokens, Tokens: point.Tokens, BilledCostUSDTicks: point.BilledCostUSDTicks, Models: models})
	}
	topModels := make([]modelUsageDTO, 0, len(result.TopModels))
	for _, item := range result.TopModels {
		topModels = append(topModels, modelUsageDTO{Model: item.Model, Requests: item.Requests, InputTokens: item.InputTokens, CachedInputTokens: item.CachedInputTokens, OutputTokens: item.OutputTokens, ReasoningTokens: item.ReasoningTokens, Tokens: item.Tokens, BilledCostUSDTicks: item.BilledCostUSDTicks})
	}
	clients := make([]clientUsageDTO, 0, len(result.Clients))
	for _, item := range result.Clients {
		clients = append(clients, clientUsageDTO{Client: item.Client, Label: item.Label, Count: item.Count})
	}
	liveClients := make([]clientUsageDTO, 0, len(result.Connections.Clients))
	for _, item := range result.Connections.Clients {
		liveClients = append(liveClients, clientUsageDTO{Client: item.Client, Label: item.Label, Count: item.Count})
	}
	response.Success(c, http.StatusOK, responseDTO{
		Period:      string(result.Period),
		GeneratedAt: result.GeneratedAt,
		Range:       rangeDTO{Start: result.Range.Start, End: result.Range.End},
		Resources: resourcesDTO{
			ActiveAccounts:   result.Resources.ActiveAccounts,
			TotalAccounts:    result.Resources.TotalAccounts,
			EnabledModels:    result.Resources.EnabledModels,
			TotalModels:      result.Resources.TotalModels,
			ActiveClientKeys: result.Resources.ActiveClientKeys,
			TotalClientKeys:  result.Resources.TotalClientKeys,
			AllTimeRequests:  result.Resources.AllTimeRequests,
		},
		Usage:     usageDTO{Requests: result.Usage.Requests, SuccessfulRequests: result.Usage.SuccessfulRequests, FailedRequests: result.Usage.FailedRequests, InputTokens: result.Usage.InputTokens, CachedInputTokens: result.Usage.CachedInputTokens, OutputTokens: result.Usage.OutputTokens, ReasoningTokens: result.Usage.ReasoningTokens, Tokens: result.Usage.Tokens, BilledCostUSDTicks: result.Usage.BilledCostUSDTicks, SuccessRate: result.SuccessRate},
		LiveRates: liveRatesDTO{RPM: result.LiveRates.RPM, TPM: result.LiveRates.TPM, WindowSeconds: result.LiveRates.WindowSeconds},
		Today:     dayUsageDTO{Requests: result.Today.Requests, Tokens: result.Today.Tokens, Start: result.Today.Start, End: result.Today.End},
		Series:    series,
		TopModels: topModels,
		Clients:   clients,
		Connections: connectionsDTO{
			Active:  result.Connections.Active,
			Peak:    result.Connections.Peak,
			Total:   result.Connections.Total,
			Clients: liveClients,
		},
	})
}
