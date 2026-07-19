package egress

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
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
	router.POST("/egress-nodes/batch", h.createBatch)
	router.POST("/egress-nodes/batch-enabled", h.batchEnabled)
	router.POST("/egress-nodes/batch-clear-errors", h.batchClearErrors)
	router.POST("/egress-nodes/:id/test", h.testOne)
	router.POST("/egress-nodes/:id/refresh-clearance", h.refreshClearance)
	router.POST("/egress-nodes/refresh-clearance", h.refreshClearanceAll)
	router.POST("/egress-nodes", h.create)
	router.PUT("/egress-nodes/:id", h.update)
	router.DELETE("/egress-nodes/:id", h.delete)
}

type nodeRequest struct {
	Name              string   `json:"name"`
	Scope             string   `json:"scope"`
	Scopes            []string `json:"scopes"`
	Enabled           bool     `json:"enabled"`
	ProxyURL          *string  `json:"proxyURL"`
	ClearProxyURL     bool     `json:"clearProxyURL"`
	UserAgent         string   `json:"userAgent"`
	CloudflareCookies *string  `json:"cloudflareCookies"`
	ClearCookies      bool     `json:"clearCookies"`
}

type batchNodeRequest struct {
	NamePrefix        string   `json:"namePrefix"`
	Name              string   `json:"name"` // alias for namePrefix
	Scope             string   `json:"scope"`
	Scopes            []string `json:"scopes"`
	Enabled           *bool    `json:"enabled"`
	ProxyURLs         []string `json:"proxyURLs"`
	ProxyText         string   `json:"proxyText"` // multiline paste
	UserAgent         string   `json:"userAgent"`
	CloudflareCookies *string  `json:"cloudflareCookies"`
}

type batchIDsRequest struct {
	IDs     []string `json:"ids"`
	Enabled *bool    `json:"enabled"`
}

type nodeResponse struct {
	ID               uint64     `json:"id,string"`
	Name             string     `json:"name"`
	Scope            string     `json:"scope"`
	Scopes           []string   `json:"scopes"`
	Enabled          bool       `json:"enabled"`
	ProxyConfigured  bool       `json:"proxyConfigured"`
	ProxyProtocol    string     `json:"proxyProtocol,omitempty"`
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
		Name: value.Name, Scope: egressdomain.Scope(value.Scope), Scopes: parseScopeList(value.Scopes, value.Scope),
		Enabled: value.Enabled, ProxyURL: value.ProxyURL, ClearProxyURL: value.ClearProxyURL, UserAgent: value.UserAgent,
		CloudflareCookies: value.CloudflareCookies, ClearCookies: value.ClearCookies,
	}
}

func parseScopeList(scopes []string, primary string) []egressdomain.Scope {
	out := make([]egressdomain.Scope, 0, len(scopes)+1)
	for _, item := range scopes {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		out = append(out, egressdomain.Scope(item))
	}
	if len(out) == 0 && strings.TrimSpace(primary) != "" {
		out = append(out, egressdomain.Scope(strings.TrimSpace(primary)))
	}
	return out
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

func (h *Handler) createBatch(c *gin.Context) {
	var request batchNodeRequest
	if c.ShouldBindJSON(&request) != nil {
		response.Error(c, http.StatusBadRequest, "invalidRequest", "请求参数无效")
		return
	}
	enabled := true
	if request.Enabled != nil {
		enabled = *request.Enabled
	}
	urls := append([]string{}, request.ProxyURLs...)
	if text := strings.TrimSpace(request.ProxyText); text != "" {
		urls = append(urls, text)
	}
	prefix := strings.TrimSpace(request.NamePrefix)
	if prefix == "" {
		prefix = strings.TrimSpace(request.Name)
	}
	result, err := h.service.CreateBatch(c.Request.Context(), egressapp.BatchCreateInput{
		NamePrefix: prefix, Scope: egressdomain.Scope(request.Scope), Scopes: parseScopeList(request.Scopes, request.Scope),
		Enabled: enabled, ProxyURLs: urls, UserAgent: request.UserAgent, CloudflareCookies: request.CloudflareCookies,
	})
	if err != nil {
		h.writeError(c, err)
		return
	}
	items := make([]nodeResponse, 0, len(result.Items))
	for _, value := range result.Items {
		items = append(items, newNodeResponse(value))
	}
	response.Success(c, http.StatusCreated, gin.H{
		"created": result.Created, "failed": result.Failed, "skipped": result.Skipped,
		"errors": result.Errors, "items": items,
	})
}

func (h *Handler) batchEnabled(c *gin.Context) {
	var request batchIDsRequest
	if c.ShouldBindJSON(&request) != nil || request.Enabled == nil {
		response.Error(c, http.StatusBadRequest, "invalidRequest", "请求参数无效")
		return
	}
	ids, err := parseUintIDs(request.IDs)
	if err != nil {
		response.Error(c, http.StatusBadRequest, "invalidId", err.Error())
		return
	}
	updated, err := h.service.SetEnabledBatch(c.Request.Context(), ids, *request.Enabled)
	if err != nil {
		h.writeError(c, err)
		return
	}
	response.Success(c, http.StatusOK, gin.H{"updated": updated, "enabled": *request.Enabled})
}

func (h *Handler) batchClearErrors(c *gin.Context) {
	var request batchIDsRequest
	if c.ShouldBindJSON(&request) != nil {
		response.Error(c, http.StatusBadRequest, "invalidRequest", "请求参数无效")
		return
	}
	ids, err := parseUintIDs(request.IDs)
	if err != nil {
		response.Error(c, http.StatusBadRequest, "invalidId", err.Error())
		return
	}
	cleared, err := h.service.ClearErrorsBatch(c.Request.Context(), ids)
	if err != nil {
		h.writeError(c, err)
		return
	}
	response.Success(c, http.StatusOK, gin.H{"cleared": cleared})
}

func parseUintIDs(values []string) ([]uint64, error) {
	out := make([]uint64, 0, len(values))
	for _, raw := range values {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		id, err := strconv.ParseUint(raw, 10, 64)
		if err != nil || id == 0 {
			return nil, errors.New("账号 ID 无效")
		}
		out = append(out, id)
	}
	if len(out) == 0 {
		return nil, errors.New("至少选择一个节点")
	}
	return out, nil
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
	scopes := make([]string, 0, len(value.Scopes))
	for _, scope := range value.Scopes {
		scopes = append(scopes, string(scope))
	}
	if len(scopes) == 0 && value.Scope != "" {
		scopes = []string{string(value.Scope)}
	}
	primary := string(value.Scope)
	if primary == "" && len(scopes) > 0 {
		primary = scopes[0]
	}
	return nodeResponse{
		ID: value.ID, Name: value.Name, Scope: primary, Scopes: scopes, Enabled: value.Enabled,
		ProxyConfigured: value.ProxyConfigured, ProxyProtocol: value.ProxyProtocol, UserAgent: value.UserAgent, CookieConfigured: value.CookieConfigured,
		Health: value.Health, FailureCount: value.FailureCount, CooldownUntil: value.CooldownUntil, LastError: value.LastError,
		SuccessCount: value.SuccessCount, RequestCount: value.RequestCount, SuccessRate: value.SuccessRate, FailureRate: value.FailureRate,
		Inflight: value.Inflight, LastProbeAt: value.LastProbeAt, LastProbeOK: value.LastProbeOK, LastProbeMs: value.LastProbeMs,
		LastProbeError: egressapp.LocalizeEgressError(value.LastProbeError),
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

func (h *Handler) refreshClearance(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	value, err := h.service.RefreshClearanceNode(c.Request.Context(), id)
	if errors.Is(err, egressapp.ErrNotFound) {
		response.Error(c, http.StatusNotFound, "egressNotFound", "代理节点不存在")
		return
	}
	if errors.Is(err, egressapp.ErrInvalidInput) {
		response.Error(c, http.StatusBadRequest, "invalidRequest", err.Error())
		return
	}
	if err != nil {
		response.Error(c, http.StatusBadGateway, "flareSolverrFailed", err.Error())
		return
	}
	response.Success(c, http.StatusOK, newNodeResponse(value))
}

func (h *Handler) refreshClearanceAll(c *gin.Context) {
	err := h.service.RefreshClearanceAll(c.Request.Context())
	if err != nil {
		response.Error(c, http.StatusBadGateway, "flareSolverrFailed", err.Error())
		return
	}
	response.Success(c, http.StatusOK, gin.H{"refreshed": true})
}
