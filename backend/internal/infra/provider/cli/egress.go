package cli

import (
	"context"
	"io"
	"net/http"

	domainegress "github.com/chenyme/grok2api/backend/internal/domain/egress"
	infraegress "github.com/chenyme/grok2api/backend/internal/infra/egress"
)

type egressAffinityContextKey struct{}

// WithEgressAffinity attaches a soft sticky key used when selecting among equally loaded proxies.
func WithEgressAffinity(ctx context.Context, affinity string) context.Context {
	if affinity == "" {
		return ctx
	}
	return context.WithValue(ctx, egressAffinityContextKey{}, affinity)
}

func egressAffinityFrom(ctx context.Context) string {
	value, _ := ctx.Value(egressAffinityContextKey{}).(string)
	return value
}

type egressTransport struct {
	manager  *infraegress.Manager
	fallback http.RoundTripper
}

func (t *egressTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	affinity := egressAffinityFrom(request.Context())
	lease, configured, err := t.manager.AcquireIfConfigured(request.Context(), domainegress.ScopeBuild, affinity)
	if err != nil {
		return nil, err
	}
	if !configured {
		return t.fallback.RoundTrip(request)
	}
	if lease.UserAgent != "" {
		request.Header.Set("User-Agent", lease.UserAgent)
	}
	response, err := lease.Do(request)
	if err != nil {
		t.manager.FeedbackForScope(context.WithoutCancel(request.Context()), domainegress.ScopeBuild, lease.NodeID, 0, err)
		lease.Release()
		return nil, err
	}
	t.manager.FeedbackForScope(context.WithoutCancel(request.Context()), domainegress.ScopeBuild, lease.NodeID, response.StatusCode, nil)
	if response.Body == nil {
		lease.Release()
		return response, nil
	}
	response.Body = &egressResponseBody{ReadCloser: response.Body, release: lease.Release}
	return response, nil
}

type egressResponseBody struct {
	io.ReadCloser
	release func()
}

func (b *egressResponseBody) Close() error {
	err := b.ReadCloser.Close()
	if b.release != nil {
		b.release()
		b.release = nil
	}
	return err
}
