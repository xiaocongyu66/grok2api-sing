package egress

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
	tlsclient "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
	"github.com/bogdanfinn/websocket"
)

type browserClient struct {
	inner tlsclient.HttpClient
	close func()
}

func (l *Lease) DialWebSocket(ctx context.Context, endpoint string, headers fhttp.Header, handshakeTimeout time.Duration) (*websocket.Conn, *fhttp.Response, error) {
	if l == nil || l.browser == nil {
		return nil, nil, errors.New("当前出口客户端不支持浏览器 WebSocket")
	}
	dialer := &websocket.Dialer{
		HandshakeTimeout:  handshakeTimeout,
		NetDialTLSContext: l.browser.inner.GetTLSDialer(),
		NetDialContext:    l.browser.inner.GetDialer().DialContext,
	}
	return dialer.DialContext(ctx, endpoint, headers)
}

func newBrowserClient(proxyURL string) (*browserClient, error) {
	options := []tlsclient.HttpClientOption{
		tlsclient.WithTimeoutSeconds(7200),
		tlsclient.WithClientProfile(profiles.Chrome_146),
		tlsclient.WithNotFollowRedirects(),
	}
	var closeFn func()
	if strings.TrimSpace(proxyURL) != "" {
		// Route browser TLS through in-process sing-box instead of tls-client's own proxy stack.
		dialer, err := openProxyDialer(proxyURL)
		if err != nil {
			return nil, err
		}
		closeFn = dialer.Close
		options = append(options, tlsclient.WithDialContext(dialer.DialContext))
	}
	client, err := tlsclient.NewHttpClient(tlsclient.NewNoopLogger(), options...)
	if err != nil {
		if closeFn != nil {
			closeFn()
		}
		return nil, err
	}
	return &browserClient{inner: client, close: closeFn}, nil
}

func (c *browserClient) Do(request *http.Request) (*http.Response, error) {
	frequest, err := toFHTTPRequest(request)
	if err != nil {
		return nil, err
	}
	fresponse, err := c.inner.Do(frequest)
	if err != nil {
		return nil, err
	}
	return fromFHTTPResponse(fresponse), nil
}

func fromFHTTPResponse(fresponse *fhttp.Response) *http.Response {
	header := http.Header(fresponse.Header)
	contentLength := fresponse.ContentLength
	if fresponse.Uncompressed {
		header = header.Clone()
		header.Del("Content-Encoding")
		header.Del("Content-Length")
		contentLength = -1
	}
	return &http.Response{
		Status: fresponse.Status, StatusCode: fresponse.StatusCode, Proto: fresponse.Proto,
		ProtoMajor: fresponse.ProtoMajor, ProtoMinor: fresponse.ProtoMinor, Header: header,
		Body: fresponse.Body, ContentLength: contentLength, TransferEncoding: fresponse.TransferEncoding,
		Close: fresponse.Close, Uncompressed: fresponse.Uncompressed, Trailer: http.Header(fresponse.Trailer),
	}
}

func (c *browserClient) CloseIdleConnections() {
	if c == nil {
		return
	}
	if c.inner != nil {
		c.inner.CloseIdleConnections()
	}
	if c.close != nil {
		c.close()
		c.close = nil
	}
}

func toFHTTPRequest(request *http.Request) (*fhttp.Request, error) {
	var body io.Reader
	if request.Body != nil {
		body = request.Body
	}
	result, err := fhttp.NewRequestWithContext(request.Context(), request.Method, request.URL.String(), body)
	if err != nil {
		return nil, err
	}
	result.ContentLength = request.ContentLength
	result.TransferEncoding = append([]string(nil), request.TransferEncoding...)
	result.Close = request.Close
	if request.Host != "" {
		result.Host = request.Host
	}
	if request.GetBody != nil {
		result.GetBody = request.GetBody
	}
	result.Trailer = fhttp.Header(request.Trailer.Clone())
	for name, values := range request.Header {
		for _, value := range values {
			result.Header.Add(name, value)
		}
	}
	return result, nil
}
