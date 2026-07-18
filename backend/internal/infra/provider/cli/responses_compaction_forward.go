package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/chenyme/grok2api/backend/internal/domain/account"
	infraegress "github.com/chenyme/grok2api/backend/internal/infra/egress"
	"github.com/chenyme/grok2api/backend/internal/infra/provider"
)

const gatewayCompactionRetryDelay = 3 * time.Second

var (
	errGatewayCompactionStreamClosed = errors.New("compaction stream closed before response.completed")
	errGatewayCompactionDegenerate   = errors.New("compaction model returned an empty or degenerate summary")
)

type gatewayCompactionSample struct {
	response map[string]any
	summary  string
}

type gatewayCompactionStreamError struct {
	message   string
	transient bool
}

func (e *gatewayCompactionStreamError) Error() string {
	return e.message
}

func (a *Adapter) forwardGatewayCompaction(
	ctx context.Context,
	request provider.ResponseResourceRequest,
	accessToken string,
	body []byte,
	warnings string,
) (*provider.Response, error) {
	return a.forwardGatewayCompactionWithPolicy(ctx, request, accessToken, body, warnings, gatewayCompactionMaxAttempts, gatewayCompactionRetryDelay)
}

func (a *Adapter) forwardGatewayCompactionWithPolicy(
	ctx context.Context,
	request provider.ResponseResourceRequest,
	accessToken string,
	body []byte,
	warnings string,
	maxAttempts int,
	retryDelay time.Duration,
) (*provider.Response, error) {
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	upstreamRequest := request
	upstreamRequest.Streaming = true
	// Compaction sampling always hits /responses (not /responses/compact).
	upstreamRequest.Path = "/responses"
	primaryBase := a.primaryBaseURL()
	base := a.apiBaseForOperation(ctx, request.Credential, request.Method, upstreamRequest.Path)
	var lastErr error

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		resp, reqURL, err := a.doResponseRequest(ctx, upstreamRequest, accessToken, body, base)
		if err != nil {
			lastErr = err
			if attempt < maxAttempts && waitGatewayCompactionRetry(ctx, retryDelay) {
				continue
			}
			return nil, err
		}

		if a.shouldProbeXAIInferenceFallback(ctx, request.Credential, request.Method, upstreamRequest.Path, resp.StatusCode) && strings.EqualFold(base, primaryBase) {
			primaryBody, primaryTruncated, readErr := provider.ReadDiagnosticBody(resp.Body)
			_ = resp.Body.Close()
			if readErr != nil {
				return nil, readErr
			}
			primaryResp := cloneBufferedResponse(resp, primaryBody, primaryTruncated)
			fallbackBase := a.fallbackBaseURL()
			if fallbackBase != "" && !strings.EqualFold(fallbackBase, base) {
				fallbackResp, fallbackURL, fallbackErr := a.doResponseRequest(ctx, upstreamRequest, accessToken, body, fallbackBase)
				if fallbackErr == nil && isHTTPSuccess(fallbackResp.StatusCode) {
					cred := request.Credential
					a.activateBuildAPIFallback(ctx, &cred)
					resp, reqURL, base = fallbackResp, fallbackURL, fallbackBase
				} else {
					if fallbackErr == nil {
						_ = fallbackResp.Body.Close()
					}
					resp = primaryResp
				}
			} else {
				resp = primaryResp
			}
		}

		if err := normalizeGzipResponse(resp); err != nil {
			_ = resp.Body.Close()
			lastErr = err
			if attempt < maxAttempts && waitGatewayCompactionRetry(ctx, retryDelay) {
				continue
			}
			return nil, err
		}
		modelCatalogChanged := a.modelCatalogChanged(request.Credential.ID, resp.Header.Get("x-models-etag"))
		if !isHTTPSuccess(resp.StatusCode) {
			result, transient, err := gatewayCompactionHTTPFailure(resp, reqURL, modelCatalogChanged, warnings)
			if err != nil {
				return nil, err
			}
			if transient && attempt < maxAttempts && waitGatewayCompactionRetry(ctx, retryDelay) {
				continue
			}
			return result, nil
		}

		data, readErr := io.ReadAll(io.LimitReader(resp.Body, maxCompatibleResponseBytes+1))
		_ = resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			if attempt < maxAttempts && waitGatewayCompactionRetry(ctx, retryDelay) {
				continue
			}
			return nil, readErr
		}
		if len(data) > maxCompatibleResponseBytes {
			return gatewayCompactionFailureProviderResponse(resp.Header, reqURL, modelCatalogChanged, warnings, "上游 compaction 响应超过 128 MiB"), nil
		}
		sample, sampleErr := parseGatewayCompactionStream(data)
		if sampleErr == nil && isDegenerateGatewayCompactionSummary(sample.summary) {
			sampleErr = errGatewayCompactionDegenerate
		}
		if sampleErr != nil {
			lastErr = sampleErr
			if gatewayCompactionErrorIsTransient(sampleErr) && attempt < maxAttempts && waitGatewayCompactionRetry(ctx, retryDelay) {
				continue
			}
			return gatewayCompactionFailureProviderResponse(resp.Header, reqURL, modelCatalogChanged, warnings, sampleErr.Error()), nil
		}

		continuation := gatewayCompactionContinuation(sample.summary)
		blob, encodeErr := a.compaction.encode(request.PromptCacheKey, continuation)
		if encodeErr != nil {
			return gatewayCompactionFailureProviderResponse(resp.Header, reqURL, modelCatalogChanged, warnings, "服务端 compaction 编码失败"), nil
		}
		converted, contentType, convertErr := buildGatewayCompactionResponse(sample.response, blob, request.Model, request.Streaming)
		if convertErr != nil {
			return gatewayCompactionFailureProviderResponse(resp.Header, reqURL, modelCatalogChanged, warnings, "服务端 compaction 响应编码失败"), nil
		}
		// Remember synthetic response id → summary so previous_response_id continuations
		// do not hit Grok with a compact state it never stored.
		if a.compactRecall != nil {
			if responseID := gatewayCompactResponseID(sample.response, converted); responseID != "" {
				a.compactRecall.remember(responseID, request.PromptCacheKey, continuation)
			}
		}
		headers := resp.Header.Clone()
		headers.Del("Content-Encoding")
		headers.Set("Content-Length", strconv.Itoa(len(converted)))
		headers.Set("Content-Type", contentType)
		if warnings != "" {
			headers.Set("X-Grok2API-Compatibility-Warnings", warnings)
		}
		return &provider.Response{
			StatusCode: resp.StatusCode, Status: resp.Status, Header: headers,
			Body: io.NopCloser(bytes.NewReader(converted)), UpstreamURL: reqURL,
			ModelCatalogChanged: modelCatalogChanged,
		}, nil
	}
	return nil, lastErr
}

func gatewayCompactResponseID(sample map[string]any, encoded []byte) string {
	if sample != nil {
		if id := strings.TrimSpace(stringField(sample, "id")); id != "" {
			return id
		}
	}
	// Fall back to parsing the encoded SSE/JSON we return to the client.
	if id := strings.TrimSpace(extractResponseIDFromCompactionPayload(encoded)); id != "" {
		return id
	}
	return ""
}

func extractResponseIDFromCompactionPayload(data []byte) string {
	// Non-stream JSON body.
	var payload map[string]any
	if json.Unmarshal(data, &payload) == nil {
		if id := strings.TrimSpace(stringField(payload, "id")); id != "" {
			return id
		}
	}
	// Stream: find "id":"resp_..." near response.completed.
	text := string(data)
	marker := `"id":"`
	for _, part := range strings.Split(text, marker) {
		if !strings.HasPrefix(part, "resp_") && !strings.HasPrefix(part, "cmp_") {
			// still accept any quoted id after marker in completed events
		}
		end := strings.IndexByte(part, '"')
		if end <= 0 {
			continue
		}
		id := part[:end]
		if strings.HasPrefix(id, "resp_") {
			return id
		}
	}
	return ""
}

// doResponseRequest posts a prepared Responses body to the given API base.
func (a *Adapter) doResponseRequest(
	ctx context.Context,
	request provider.ResponseResourceRequest,
	accessToken string,
	body []byte,
	base string,
) (*http.Response, string, error) {
	var bodyReader io.Reader
	if len(body) > 0 {
		bodyReader = bytes.NewReader(body)
	}
	requestCtx := infraegress.WithAccount(ctx, string(account.ProviderBuild), request.Credential.ID)
	url := a.urlWithBase(base, request.Path)
	req, err := http.NewRequestWithContext(requestCtx, request.Method, url, bodyReader)
	if err != nil {
		return nil, "", err
	}
	if err := a.applyHeaders(req, request.Credential, accessToken, request.Model, request.PromptCacheKey, true); err != nil {
		return nil, "", err
	}
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	if request.Streaming {
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("Accept-Encoding", "identity")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	if request.IdempotencyID != "" {
		req.Header.Set("Idempotency-Key", request.IdempotencyID)
	}
	resp, err := a.http.Do(req)
	if err != nil {
		return nil, "", err
	}
	return resp, url, nil
}

func parseGatewayCompactionStream(data []byte) (gatewayCompactionSample, error) {
	var completed map[string]any
	var streamedParts []string
	err := consumeCompatibleSSE(io.NopCloser(bytes.NewReader(data)), func(event compatibleSSEEvent) error {
		if !event.HasData() || bytes.Equal(bytes.TrimSpace(event.Data()), []byte("[DONE]")) {
			return nil
		}
		var payload map[string]any
		if json.Unmarshal(event.Data(), &payload) != nil {
			return nil
		}
		kind := strings.TrimSpace(stringField(payload, "type"))
		if kind == "" {
			kind = strings.TrimSpace(event.Event)
		}
		switch kind {
		case "response.output_item.done":
			if item, ok := payload["item"].(map[string]any); ok {
				streamedParts = append(streamedParts, gatewayCompactionItemText(item)...)
			}
		case "response.completed":
			if response, ok := payload["response"].(map[string]any); ok {
				completed = cloneJSONObject(response)
			}
		case "response.failed":
			response, _ := payload["response"].(map[string]any)
			errorValue, _ := response["error"].(map[string]any)
			return newGatewayCompactionStreamError(
				strings.TrimSpace(stringField(errorValue, "code")),
				strings.TrimSpace(stringField(errorValue, "message")),
			)
		case "error", "response.error":
			errorValue, _ := payload["error"].(map[string]any)
			if errorValue == nil {
				errorValue = payload
			}
			return newGatewayCompactionStreamError(
				strings.TrimSpace(stringField(errorValue, "code")),
				strings.TrimSpace(stringField(errorValue, "message")),
			)
		}
		return nil
	})
	if err != nil {
		return gatewayCompactionSample{}, err
	}
	if completed == nil {
		return gatewayCompactionSample{}, errGatewayCompactionStreamClosed
	}
	summary := extractCompactionSummary(completed)
	if summary == "" {
		summary = strings.Join(streamedParts, "\n")
	}
	return gatewayCompactionSample{response: completed, summary: summary}, nil
}

func newGatewayCompactionStreamError(code, message string) error {
	if message == "" {
		message = "upstream compaction stream failed"
	}
	detail := message
	if code != "" {
		detail = code + ": " + message
	}
	return &gatewayCompactionStreamError{
		message:   detail,
		transient: gatewayCompactionEventErrorIsTransient(code, message),
	}
}

func gatewayCompactionEventErrorIsTransient(code, message string) bool {
	lowerCode := strings.ToLower(strings.TrimSpace(code))
	lowerMessage := strings.ToLower(message)
	if lowerCode == "invalid_request_error" || strings.Contains(lowerMessage, "invalid_request_error") {
		return false
	}
	if status, err := strconv.Atoi(lowerCode); err == nil && status >= 400 && status < 500 && status != http.StatusRequestTimeout && status != http.StatusTooManyRequests {
		return false
	}
	for _, marker := range []string{"prompt is too long", "maximum prompt length", "maximum context length", "context_length_exceeded", "too long for this model"} {
		if strings.Contains(lowerMessage, marker) {
			return false
		}
	}
	return true
}

func gatewayCompactionErrorIsTransient(err error) bool {
	var streamErr *gatewayCompactionStreamError
	if errors.As(err, &streamErr) {
		return streamErr.transient
	}
	return true
}

func gatewayCompactionItemText(item map[string]any) []string {
	if stringField(item, "type") != "message" {
		return nil
	}
	content, _ := item["content"].([]any)
	parts := make([]string, 0, len(content))
	for _, raw := range content {
		value, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		switch stringField(value, "type") {
		case "output_text", "text":
			if text := strings.TrimSpace(stringField(value, "text")); text != "" {
				parts = append(parts, text)
			}
		}
	}
	return parts
}

func extractCompactionSummary(response map[string]any) string {
	if text, ok := response["output_text"].(string); ok && strings.TrimSpace(text) != "" {
		return strings.TrimSpace(text)
	}
	var parts []string
	output, _ := response["output"].([]any)
	for _, raw := range output {
		if item, ok := raw.(map[string]any); ok {
			parts = append(parts, gatewayCompactionItemText(item)...)
		}
	}
	return strings.Join(parts, "\n")
}

func buildGatewayCompactionResponse(response map[string]any, blob, model string, streaming bool) ([]byte, string, error) {
	response = cloneJSONObject(response)
	responseID := strings.TrimSpace(stringField(response, "id"))
	if responseID == "" {
		responseID = "resp_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	item := map[string]any{
		"id": "cmp_" + strings.TrimPrefix(responseID, "resp_"),
		"type": "compaction", "encrypted_content": blob,
	}
	response["id"] = responseID
	response["object"] = "response"
	response["status"] = "completed"
	response["model"] = model
	response["output"] = []any{item}
	delete(response, "output_text")
	if !streaming {
		encoded, err := json.Marshal(response)
		return encoded, "application/json", err
	}
	createdResponse := cloneJSONObject(response)
	createdResponse["status"] = "in_progress"
	createdResponse["output"] = []any{}
	inProgressResponse := cloneJSONObject(createdResponse)
	events := []struct {
		name string
		data map[string]any
	}{
		{"response.created", map[string]any{"type": "response.created", "sequence_number": 0, "response": createdResponse}},
		{"response.in_progress", map[string]any{"type": "response.in_progress", "sequence_number": 1, "response": inProgressResponse}},
		{"response.output_item.added", map[string]any{"type": "response.output_item.added", "sequence_number": 2, "output_index": 0, "item": item}},
		{"keepalive", map[string]any{"type": "keepalive", "sequence_number": 3}},
		{"response.output_item.done", map[string]any{"type": "response.output_item.done", "sequence_number": 4, "output_index": 0, "item": item}},
		{"response.completed", map[string]any{"type": "response.completed", "sequence_number": 5, "response": response}},
	}
	var output bytes.Buffer
	for _, event := range events {
		encoded, err := json.Marshal(event.data)
		if err != nil {
			return nil, "", err
		}
		fmt.Fprintf(&output, "event: %s\ndata: %s\n\n", event.name, encoded)
	}
	return output.Bytes(), "text/event-stream", nil
}

func gatewayCompactionHTTPFailure(resp *http.Response, reqURL string, modelCatalogChanged bool, warnings string) (*provider.Response, bool, error) {
	body, truncated, err := provider.ReadDiagnosticBody(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return nil, false, err
	}
	transient := gatewayCompactionStatusTransient(resp.StatusCode, string(body))
	headers := resp.Header.Clone()
	headers.Set("Content-Length", strconv.Itoa(len(body)))
	if warnings != "" {
		headers.Set("X-Grok2API-Compatibility-Warnings", warnings)
	}
	diagnostic := &provider.DiagnosticResponse{
		StatusCode: resp.StatusCode, Status: resp.Status, Header: headers.Clone(),
		Body: body, BodyTruncated: truncated,
	}
	return &provider.Response{
		StatusCode: resp.StatusCode, Status: resp.Status, Header: headers,
		Body: io.NopCloser(bytes.NewReader(body)), UpstreamURL: reqURL,
		Diagnostic: diagnostic, ModelCatalogChanged: modelCatalogChanged,
	}, transient, nil
}

func gatewayCompactionStatusTransient(status int, body string) bool {
	lower := strings.ToLower(body)
	for _, marker := range []string{"prompt is too long", "maximum prompt length", "maximum context length", "context_length_exceeded", "too long for this model"} {
		if strings.Contains(lower, marker) {
			return false
		}
	}
	return status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500
}

func gatewayCompactionFailureProviderResponse(headers http.Header, reqURL string, modelCatalogChanged bool, warnings, detail string) *provider.Response {
	body, _ := json.Marshal(map[string]any{"error": map[string]any{
		"type": "server_error", "code": "compaction_failed", "message": "Grok Build compaction 失败",
	}})
	headers = headers.Clone()
	headers.Set("Content-Type", "application/json")
	headers.Set("Content-Length", strconv.Itoa(len(body)))
	headers.Set("X-Should-Retry", "false")
	if warnings != "" {
		headers.Set("X-Grok2API-Compatibility-Warnings", warnings)
	}
	return &provider.Response{
		StatusCode: http.StatusBadGateway, Status: "502 Bad Gateway", Header: headers,
		Body: io.NopCloser(bytes.NewReader(body)), UpstreamURL: reqURL,
		Diagnostic: &provider.DiagnosticResponse{
			StatusCode: http.StatusBadGateway, Status: "502 Bad Gateway", Header: headers.Clone(),
			Body: []byte(detail),
		},
		ModelCatalogChanged: modelCatalogChanged,
	}
}

func waitGatewayCompactionRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
