package middleware

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	clientkeydomain "github.com/chenyme/grok2api/backend/internal/domain/clientkey"
	"github.com/chenyme/grok2api/backend/internal/infra/security"
	"github.com/chenyme/grok2api/backend/internal/pkg/clientid"
	"github.com/gin-gonic/gin"
)

const RequestIDKey = "requestId"
const maxRequestIDLength = 64

// RequestID 为每个请求生成稳定关联 ID，并写入响应头。
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		requestID := strings.TrimSpace(c.GetHeader("X-Request-ID"))
		if !validRequestID(requestID) {
			requestID, _ = security.NewOpaqueToken(12)
			if requestID == "" {
				requestID = "req-" + strconv.FormatInt(time.Now().UnixNano(), 36)
			}
		}
		c.Set(RequestIDKey, requestID)
		c.Header("X-Request-ID", requestID)
		c.Next()
	}
}

// validRequestID 只接受适合写入日志和审计索引的短 ASCII 标识。
func validRequestID(value string) bool {
	if value == "" || len(value) > maxRequestIDLength {
		return false
	}
	for index := range len(value) {
		character := value[index]
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') {
			continue
		}
		switch character {
		case '-', '_', '.', ':':
		default:
			return false
		}
	}
	return true
}

// Timeout 为 HTTP 请求设置统一生命周期上限。
func Timeout(duration time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), duration)
		defer cancel()
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

// MaxBodyBytes 对所有请求体应用统一硬上限，避免管理端绑定无界读取。
func MaxBodyBytes(limit int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Body != nil && limit > 0 {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
		}
		c.Next()
	}
}

// SecurityHeaders 为 API 和媒体响应添加通用浏览器安全边界。
func SecurityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Header("Referrer-Policy", "no-referrer")
		c.Header("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		c.Header("Cross-Origin-Opener-Policy", "same-origin")
		c.Header("Cross-Origin-Resource-Policy", "same-site")
		// HSTS only when the request is already TLS (or terminated with HTTPS scheme).
		if c.Request.TLS != nil || strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https") {
			c.Header("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		c.Next()
	}
}

// safeLogToken keeps only a conservative token charset so log sinks cannot be split/injected.
func safeLogToken(value string, max int) string {
	if value == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(min(len(value), max))
	for _, r := range value {
		if b.Len() >= max {
			break
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.' || r == ':' || r == '/' || r == '*' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func safeClientIP(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	// Strip zone / port forms and accept only parseable IPs.
	host := value
	if h, _, err := net.SplitHostPort(value); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if ip := net.ParseIP(host); ip != nil {
		return ip.String()
	}
	return ""
}

// AccessLog 记录路径、状态、耗时与调用方元数据，不读取请求或响应正文。
func AccessLog(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		startedAt := time.Now()
		c.Next()
		requestID, _ := c.Get(RequestIDKey)
		userAgent := strings.TrimSpace(c.Request.UserAgent())
		headers := map[string]string{}
		for _, name := range []string{
			"x-claude-code-session-id", "x-codex-window-id", "x-codex-session-id",
			"x-grok-conv-id", "x-grok-conversation-id",
			"originator", "x-app", "anthropic-version", "anthropic-beta",
			"x-stainless-lang", "x-stainless-package-version",
		} {
			if value := strings.TrimSpace(c.GetHeader(name)); value != "" {
				headers[strings.ToLower(name)] = value
			}
		}
		clientType := clientid.Detect(userAgent, headers)
		// Only log Gin route templates (e.g. /v1/chat/completions), never raw URL.Path.
		logPath := c.FullPath()
		if logPath == "" {
			logPath = "-"
		}
		logMethod := c.Request.Method
		switch logMethod {
		case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodHead, http.MethodOptions:
		default:
			logMethod = "OTHER"
		}
		reqID := fmt.Sprint(requestID)
		if !validRequestID(reqID) {
			reqID = "-"
		}
		attrs := []any{
			"request_id", reqID,
			"method", logMethod,
			"path", logPath,
			"status", c.Writer.Status(),
			"duration_ms", time.Since(startedAt).Milliseconds(),
			"client_ip", safeClientIP(c.ClientIP()),
			"client_type", safeLogToken(clientType, 64),
			// Length only — avoid writing free-form User-Agent into logs (log-injection sink).
			"user_agent_len", len(userAgent),
			"bytes_out", c.Writer.Size(),
		}
		if keyValue, ok := c.Get(ClientKey); ok {
			if key, ok := keyValue.(clientkeydomain.Key); ok {
				attrs = append(attrs, "client_key_id", key.ID, "client_key_name", safeLogToken(key.Name, 64), "client_key_prefix", safeLogToken(key.Prefix, 32))
			}
		}
		logger.Info("http_request", attrs...)
	}
}
