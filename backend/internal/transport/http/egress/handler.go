package egress

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	egressapp "github.com/chenyme/grok2api/backend/internal/application/egress"
	egressdomain "github.com/chenyme/grok2api/backend/internal/domain/egress"
	"github.com/chenyme/grok2api/backend/internal/repository"
	"github.com/chenyme/grok2api/backend/internal/shared/response"
	"github.com/gin-gonic/gin"
)

type Handler struct{ service *egressapp.Service }

func NewHandler(service *egressapp.Service) *Handler { return &Handler{service: service} }

func (h *Handler) Register(router *gin.RouterGroup) {
	router.GET("/egress-nodes", h.list)
	router.GET("/egress-nodes/report", h.report)
	router.POST("/egress-nodes/test", h.testAll)
	router.POST("/egress-nodes/:id/test", h.testOne)
	router.POST("/egress-nodes", h.create)
	router.PUT("/egress-nodes/:id", h.update)
	router.DELETE("/egress-nodes/:id", h.delete)
}

type nodeRequest struct {
	Name              string  `json:"name"`
	Scope             string  `json:"scope"`
	Enabled           bool    `json:"enabled"`
	ProxyURL          *string `json:"proxyURL"`
	ClearProxyURL     bool    `json:"clearProxyURL"`
	UserAgent         string  `json:"userAgent"`
	CloudflareCookies *string `json:"cloudflareCookies"`
	ClearCookies      bool    `json:"clearCookies"`
}

type nodeResponse struct {
	ID               uint64     `json:"id,string"`
	Name             string     `json:"name"`
	Scope            string     `json:"scope"`
	Enabled          bool       `json:"enabled"`
	ProxyConfigured  bool       `json:"proxyConfigured"`
	UserAgent        string     `json:"userAgent"`
	CookieConfigured bool       `json:"cookieConfigured"`
	Health           float64    `json:"health"`
	FailureCount     int        `json:"failureCount"`
	CooldownUntil    *time.Time `json:"cooldownUntil,omitempty"`
	LastError        string     `json:"lastError,omitempty"`
	SuccessCount     int64      `json:"successCount"`
	RequestCount     int64      `json:"requestCount"`
	SuccessRate      float64    `json:"successRate"`
	FailureRate      float64    `json:"failureRate"`
	Inflight         int        `json:"inflight"`
	LastProbeAt      *time.Time `json:"lastProbeAt,omitempty"`
	LastProbeOK      *bool      `json:"lastProbeOK,omitempty"`
	LastProbeMs      int64      `json:"lastProbeMs,omitempty"`
	LastProbeError   string     `json:"lastProbeError,omitempty"`
}

type reportResponse struct {
	TotalNodes   int            `json:"totalNodes"`
	EnabledNodes int            `json:"enabledNodes"`
	ProxyNodes   int            `json:"proxyNodes"`
	HealthyNodes int            `json:"healthyNodes"`
	SuccessCount int64          `json:"successCount"`
	FailureCount int64          `json:"failureCount"`
	RequestCount int64          `json:"requestCount"`
	SuccessRate  float64        `json:"successRate"`
	FailureRate  float64        `json:"failureRate"`
	Items        []nodeResponse `json:"items"`
}

type probeResponse struct {
	NodeID    uint64    `json:"nodeId,string"`
	Name      string    `json:"name"`
	Scope     string    `json:"scope"`
	OK        bool      `json:"ok"`
	LatencyMs int64     `json:"latencyMs"`
	Status    int       `json:"status,omitempty"`
	Error     string    `json:"error,omitempty"`
	ProxyUsed bool      `json:"proxyUsed"`
	CheckedAt time.Time `json:"checkedAt"`
}

func (value nodeRequest) input() egressapp.Input {
	return egressapp.Input{
		Name: value.Name, Scope: egressdomain.Scope(value.Scope), Enabled: value.Enabled,
		ProxyURL: value.ProxyURL, ClearProxyURL: value.ClearProxyURL, UserAgent: value.UserAgent,
		CloudflareCookies: value.CloudflareCookies, ClearCookies: value.ClearCookies,
	}
}

func (h *Handler) list(c *gin.Context) {
	scope, ok := parseScope(c, true)
	if !ok {
		return
	}
	values, err := h.service.List(c.Request.Context(), scope, repository.SortQuery{Field: c.Query("sortBy"), Direction: repository.SortDirection(c.Query("sortOrder"))})
	if errors.Is(err, egressapp.ErrInvalidSort) {
		response.Error(c, http.StatusBadRequest, "invalidSort", err.Error())
		return
	}
	if err != nil {
		response.Error(c, http.StatusInternalServerError, "egressNodeListFailed", "读取代理节点失败")
		return
	}
	items := make([]nodeResponse, 0, len(values))
	for _, value := range values {
		items = append(items, newNodeResponse(value))
	}
	response.Success(c, http.StatusOK, gin.H{"items": items, "defaultUserAgents": h.service.DefaultUserAgents()})
}

func (h *Handler) report(c *gin.Context) {
	scope, ok := parseScope(c, true)
	if !ok {
		return
	}
	value, err := h.service.Report(c.Request.Context(), scope)
	if err != nil {
		response.Error(c, http.StatusInternalServerError, "egressReportFailed", "读取代理报表失败")
		return
	}
	items := make([]nodeResponse, 0, len(value.Nodes))
	for _, node := range value.Nodes {
		items = append(items, newNodeResponse(node))
	}
	response.Success(c, http.StatusOK, reportResponse{
		TotalNodes: value.TotalNodes, EnabledNodes: value.EnabledNodes, ProxyNodes: value.ProxyNodes,
		HealthyNodes: value.HealthyNodes, SuccessCount: value.SuccessCount, FailureCount: value.FailureCount,
		RequestCount: value.RequestCount, SuccessRate: value.SuccessRate, FailureRate: value.FailureRate, Items: items,
	})
}

func (h *Handler) testOne(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	result, err := h.service.Probe(c.Request.Context(), id)
	if err != nil {
		h.writeError(c, err)
		return
	}
	response.Success(c, http.StatusOK, newProbeResponse(result))
}

func (h *Handler) testAll(c *gin.Context) {
	scope, ok := parseScope(c, true)
	if !ok {
		return
	}
	results, err := h.service.ProbeAll(c.Request.Context(), scope)
	if err != nil {
		h.writeError(c, err)
		return
	}
	items := make([]probeResponse, 0, len(results))
	okCount := 0
	for _, result := range results {
		if result.OK {
			okCount++
		}
		items = append(items, newProbeResponse(result))
	}
	response.Success(c, http.StatusOK, gin.H{
		"items": items, "total": len(items), "passed": okCount, "failed": len(items) - okCount,
	})
}

func (h *Handler) create(c *gin.Context) {
	var request nodeRequest
	if c.ShouldBindJSON(&request) != nil {
		response.Error(c, http.StatusBadRequest, "invalidRequest", "请求参数无效")
		return
	}
	value, err := h.service.Create(c.Request.Context(), request.input())
	if err != nil {
		h.writeError(c, err)
		return
	}
	response.Success(c, http.StatusCreated, newNodeResponse(value))
}

func (h *Handler) update(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var request nodeRequest
	if c.ShouldBindJSON(&request) != nil {
		response.Error(c, http.StatusBadRequest, "invalidRequest", "请求参数无效")
		return
	}
	value, err := h.service.Update(c.Request.Context(), id, request.input())
	if err != nil {
		h.writeError(c, err)
		return
	}
	response.Success(c, http.StatusOK, newNodeResponse(value))
}

func newNodeResponse(value egressdomain.PublicNode) nodeResponse {
	return nodeResponse{
		ID: value.ID, Name: value.Name, Scope: string(value.Scope), Enabled: value.Enabled,
		ProxyConfigured: value.ProxyConfigured, UserAgent: value.UserAgent, CookieConfigured: value.CookieConfigured,
		Health: value.Health, FailureCount: value.FailureCount, CooldownUntil: value.CooldownUntil, LastError: value.LastError,
		SuccessCount: value.SuccessCount, RequestCount: value.RequestCount, SuccessRate: value.SuccessRate, FailureRate: value.FailureRate,
		Inflight: value.Inflight, LastProbeAt: value.LastProbeAt, LastProbeOK: value.LastProbeOK, LastProbeMs: value.LastProbeMs,
		LastProbeError: value.LastProbeError,
	}
}

func newProbeResponse(value egressdomain.ProbeResult) probeResponse {
	return probeResponse{
		NodeID: value.NodeID, Name: value.Name, Scope: string(value.Scope), OK: value.OK,
		LatencyMs: value.LatencyMs, Status: value.Status, Error: value.Error, ProxyUsed: value.ProxyUsed, CheckedAt: value.CheckedAt,
	}
}

func (h *Handler) delete(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	if err := h.service.Delete(c.Request.Context(), id); err != nil {
		h.writeError(c, err)
		return
	}
	response.Success(c, http.StatusOK, gin.H{"deleted": true})
}

func (h *Handler) writeError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, egressapp.ErrInvalidInput):
		response.Error(c, http.StatusBadRequest, "invalidEgressNode", err.Error())
	case errors.Is(err, egressapp.ErrNotFound):
		response.Error(c, http.StatusNotFound, "egressNodeNotFound", err.Error())
	default:
		response.Error(c, http.StatusInternalServerError, "egressNodeOperationFailed", "代理节点操作失败")
	}
}

func parseScope(c *gin.Context, optional bool) (egressdomain.Scope, bool) {
	scope := egressdomain.Scope(c.Query("scope"))
	if scope == "" {
		if optional {
			return "", true
		}
		response.Error(c, http.StatusBadRequest, "invalidEgressScope", "scope 必须是 grok_build、grok_web、grok_console 或 grok_web_asset")
		return "", false
	}
	if scope != egressdomain.ScopeBuild && scope != egressdomain.ScopeWeb && scope != egressdomain.ScopeConsole && scope != egressdomain.ScopeWebAsset {
		response.Error(c, http.StatusBadRequest, "invalidEgressScope", "scope 必须是 grok_build、grok_web、grok_console 或 grok_web_asset")
		return "", false
	}
	return scope, true
}

func pathID(c *gin.Context) (uint64, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, "invalidId", "ID 无效")
		return 0, false
	}
	return id, true
}
