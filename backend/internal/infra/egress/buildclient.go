package egress

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	settingsdomain "github.com/chenyme/grok2api/backend/internal/domain/settings"
	xproxy "golang.org/x/net/proxy"
)

// newBuildClient keeps Grok Build on the standard Go HTTP/TLS stack used by
// the official CLI-facing transport. When a proxy URL is set, dials go through
// an in-process sing-box outbound (no extra process, no local mixed inbound).
// responseHeaderTimeout bounds wait for first response headers (not body).
func newBuildClient(proxyURL string, responseHeaderTimeout time.Duration) (requestClient, error) {
	if responseHeaderTimeout <= 0 {
		responseHeaderTimeout = settingsdomain.DefaultBuildResponseHeaderTimeout
	}
	transport := &http.Transport{
		Proxy:                 nil,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          256,
		MaxIdleConnsPerHost:   128,
		MaxConnsPerHost:       256,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: responseHeaderTimeout,
		ExpectContinueTimeout: time.Second,
	}
	if strings.TrimSpace(proxyURL) == "" {
		direct := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
		transport.DialContext = direct.DialContext
		return &http.Client{
			Transport: transport,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}, nil
	}
	dialer, err := openProxyDialer(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("创建 Grok Build 内置 sing-box 出口: %w", err)
	}
	transport.DialContext = dialer.DialContext
	return &closingClient{
		Client: &http.Client{
			Transport: transport,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		close: dialer.Close,
	}, nil
}

// closingClient closes the embedded sing-box instance when idle connections are cleared.
type closingClient struct {
	*http.Client
	close func()
}

func (c *closingClient) CloseIdleConnections() {
	if c.Client != nil {
		c.Client.CloseIdleConnections()
	}
	if c.close != nil {
		c.close()
		c.close = nil
	}
}

func (c *closingClient) Do(request *http.Request) (*http.Response, error) {
	if c == nil || c.Client == nil {
		return nil, fmt.Errorf("出口客户端未初始化")
	}
	return c.Client.Do(request)
}

func dialContext(dialer xproxy.Dialer) func(context.Context, string, string) (net.Conn, error) {
	if contextual, ok := dialer.(xproxy.ContextDialer); ok {
		return contextual.DialContext
	}
	type result struct {
		connection net.Conn
		err        error
	}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		completed := make(chan result, 1)
		go func() {
			connection, err := dialer.Dial(network, address)
			completed <- result{connection: connection, err: err}
		}()
		select {
		case value := <-completed:
			return value.connection, value.err
		case <-ctx.Done():
			go func() {
				value := <-completed
				if value.connection != nil {
					_ = value.connection.Close()
				}
			}()
			return nil, ctx.Err()
		}
	}
}
