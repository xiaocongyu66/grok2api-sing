package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	dashboardapp "github.com/chenyme/grok2api/backend/internal/application/dashboard"
	dashboarddomain "github.com/chenyme/grok2api/backend/internal/domain/dashboard"
	"github.com/gin-gonic/gin"
)

func TestHandlerReturnsDashboardContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := dashboardapp.NewService(&dashboardRepositoryStub{aggregate: dashboarddomain.Aggregate{
		Resources: dashboarddomain.Resources{ActiveAccounts: 2, TotalAccounts: 3, EnabledModels: 4, TotalModels: 5, ActiveClientKeys: 6, TotalClientKeys: 7, AllTimeRequests: 8},
		Usage:     dashboarddomain.Usage{Requests: 10, SuccessfulRequests: 9, FailedRequests: 1, Tokens: 1200},
		Buckets:   []dashboarddomain.Bucket{{Index: 0, Requests: 10, Tokens: 1200}},
	}})
	router := gin.New()
	NewHandler(service).Register(router.Group("/api/admin/v1"))

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/admin/v1/dashboard?period=7d", nil)
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data struct {
			Period string `json:"period"`
			Usage  struct {
				Requests    int64   `json:"requests"`
				SuccessRate float64 `json:"successRate"`
			} `json:"usage"`
			Series []seriesDTO `json:"series"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.Period != "7d" || envelope.Data.Usage.Requests != 10 || envelope.Data.Usage.SuccessRate != 90 || len(envelope.Data.Series) != 7 {
		t.Fatalf("response = %#v", envelope.Data)
	}
}

func TestHandlerRejectsInvalidPeriod(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	NewHandler(dashboardapp.NewService(&dashboardRepositoryStub{})).Register(router.Group("/api/admin/v1"))
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/admin/v1/dashboard?period=365d", nil))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestHandlerRejectsInvalidTimezone(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	NewHandler(dashboardapp.NewService(&dashboardRepositoryStub{})).Register(router.Group("/api/admin/v1"))
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/admin/v1/dashboard?period=24h&timezone=invalid", nil))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestHandlerReturnsLiveRatesAndToday(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := dashboardapp.NewService(&dashboardRepositoryStub{aggregate: dashboarddomain.Aggregate{
		Usage:     dashboarddomain.Usage{Requests: 10, SuccessfulRequests: 9, FailedRequests: 1, Tokens: 1200},
		LiveRates: dashboarddomain.LiveRates{RPM: 5, TPM: 700, WindowSeconds: 60},
		Today:     dashboarddomain.DayUsage{Requests: 42, Tokens: 9000, Start: "2026-07-15T00:00:00Z", End: "2026-07-15T12:00:00Z"},
		Buckets:   []dashboarddomain.Bucket{{Index: 0, Requests: 10, Tokens: 1200}},
	}})
	router := gin.New()
	NewHandler(service).Register(router.Group("/api/admin/v1"))
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/admin/v1/dashboard?period=24h&timezone=UTC", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data struct {
			LiveRates struct {
				RPM           int64 `json:"rpm"`
				TPM           int64 `json:"tpm"`
				WindowSeconds int   `json:"windowSeconds"`
			} `json:"liveRates"`
			Today struct {
				Requests int64 `json:"requests"`
				Tokens   int64 `json:"tokens"`
			} `json:"today"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.LiveRates.RPM != 5 || envelope.Data.LiveRates.TPM != 700 || envelope.Data.LiveRates.WindowSeconds != 60 {
		t.Fatalf("liveRates = %#v", envelope.Data.LiveRates)
	}
	if envelope.Data.Today.Requests != 42 || envelope.Data.Today.Tokens != 9000 {
		t.Fatalf("today = %#v", envelope.Data.Today)
	}
}

func TestHandlerCustomPeriod(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	NewHandler(dashboardapp.NewService(&dashboardRepositoryStub{})).Register(router.Group("/api/admin/v1"))
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/admin/v1/dashboard?period=custom&timezone=UTC&start=2015-01-01&end=2015-01-31", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data struct {
			Period string `json:"period"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.Period != "custom" {
		t.Fatalf("period = %s", envelope.Data.Period)
	}
}

func TestHandlerRejectsInvalidCustomRange(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	NewHandler(dashboardapp.NewService(&dashboardRepositoryStub{})).Register(router.Group("/api/admin/v1"))
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/admin/v1/dashboard?period=custom&start=2000-01-01&end=2001-01-01", nil))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

type dashboardRepositoryStub struct {
	aggregate dashboarddomain.Aggregate
}

func (s *dashboardRepositoryStub) Snapshot(context.Context, []time.Time, time.Time, time.Time, time.Time, time.Duration) (dashboarddomain.Aggregate, error) {
	return s.aggregate, nil
}
