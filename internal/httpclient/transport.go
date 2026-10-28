package httpclient

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"

	utls "github.com/refraction-networking/utls"
	"golang.org/x/net/http2"
)

// errALPNNotH2 表示 utls 握手成功但服务端没有协商出 h2，需要回退 HTTP/1.1。
var errALPNNotH2 = errors.New("httpclient: server did not negotiate h2")

// schemeRouter 在每一跳按 URL scheme 选择传输层。http.Client 在重定向时
// 会对新 URL 重新调用 RoundTrip，因此跨协议重定向也会走对 transport。
type schemeRouter struct {
	plain  http.RoundTripper
	secure http.RoundTripper
}

func (r *schemeRouter) RoundTrip(req *http.Request) (*http.Response, error) {
	switch req.URL.Scheme {
	case "http":
		return r.plain.RoundTrip(req)
	case "https":
		return r.secure.RoundTrip(req)
	default:
		return nil, fmt.Errorf("httpclient: unsupported url scheme %q", req.URL.Scheme)
	}
}

// newUTLSDialer 返回一个使用 utls 指纹完成 TLS 握手的拨号函数。
//
// https 连接不走 Go 标准库的 crypto/tls，而是先建立 TCP，再用 utls 按
// Profile 指定的 ClientHello 指纹握手，以绕过按 JA3/JA4 识别的风控。
// requireH2 为 true 时，握手后校验 ALPN；不是 h2 就返回 errALPNNotH2，
// 由上层改用 HTTP/1.1 transport 重试。
func newUTLSDialer(profile Profile, timeouts Timeouts, insecure bool, nextProtos []string, requireH2 bool) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		dialer := &net.Dialer{Timeout: timeouts.Dial}
		raw, err := dialer.DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}

		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			raw.Close()
			return nil, fmt.Errorf("httpclient: split host: %w", err)
		}

		cfg := &utls.Config{
			ServerName:         host,
			NextProtos:         nextProtos,
			InsecureSkipVerify: insecure,
		}
		conn := utls.UClient(raw, cfg, profile.Hello)

		if timeouts.TLS > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, timeouts.TLS)
			defer cancel()
		}
		if err := conn.HandshakeContext(ctx); err != nil {
			raw.Close()
			return nil, fmt.Errorf("httpclient: tls handshake: %w", err)
		}
		if requireH2 && conn.ConnectionState().NegotiatedProtocol != http2.NextProtoTLS {
			conn.Close()
			return nil, errALPNNotH2
		}
		return conn, nil
	}
}

// buildSecureTransport 构造用于 https 的 RoundTripper。
//
// 代理或 ForceHTTP1 时固定 HTTP/1.1；否则优先 HTTP/2，服务端未协商 h2 时
// 自动回退一次 HTTP/1.1。回退只对无 body 的 GET/HEAD 生效，本包的 Get
// 只发 GET，因此安全。
func buildSecureTransport(opts Options, proxyURL *url.URL) http.RoundTripper {
	if proxyURL != nil || opts.ForceHTTP1 {
		return buildHTTP1Transport(opts, proxyURL)
	}
	return &alpnFallbackTransport{
		h2: buildHTTP2Transport(opts),
		h1: buildHTTP1Transport(opts, nil),
	}
}

// buildHTTP1Transport 构造 utls + HTTP/1.1 传输层。
//
// ALPN 只 advertise http/1.1，避免服务端选中 h2 而连接上却按 HTTP/1.1 说话。
// 代理场景由 net/http 负责 CONNECT，隧道内的 TLS 走标准库而非 utls。
func buildHTTP1Transport(opts Options, proxyURL *url.URL) *http.Transport {
	t := opts.Timeouts
	dial := newUTLSDialer(opts.Profile, t, opts.InsecureSkipVerify, []string{"http/1.1"}, false)
	return &http.Transport{
		DialContext:           (&net.Dialer{Timeout: t.Dial}).DialContext,
		DialTLSContext:        dial,
		Proxy:                 http.ProxyURL(proxyURL),
		TLSHandshakeTimeout:   t.TLS,
		ResponseHeaderTimeout: t.Header,
		IdleConnTimeout:       t.Idle,
	}
}

// buildHTTP2Transport 构造 utls + HTTP/2 传输层。握手后强制校验 ALPN，
// 因为自定义 DialTLSContext 时 http2.Transport 不再自行检查。
func buildHTTP2Transport(opts Options) *http2.Transport {
	t := opts.Timeouts
	dial := newUTLSDialer(opts.Profile, t, opts.InsecureSkipVerify, []string{"h2", "http/1.1"}, true)
	return &http2.Transport{
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			return dial(ctx, network, addr)
		},
		ReadIdleTimeout: t.Idle,
		PingTimeout:     t.Idle,
	}
}

// alpnFallbackTransport 先尝试 h2；若 ALPN 不是 h2，则用 h1 重试一次。
type alpnFallbackTransport struct {
	h2 http.RoundTripper
	h1 http.RoundTripper
}

func (t *alpnFallbackTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.h2.RoundTrip(req)
	if err == nil {
		return resp, nil
	}
	if !errors.Is(err, errALPNNotH2) {
		return nil, err
	}
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		return nil, err
	}
	return t.h1.RoundTrip(req)
}

// buildPlainTransport 构造用于 http:// 的标准传输层。明文请求不涉及 TLS，
// 因此完全不经过 utls。
func buildPlainTransport(opts Options, proxyURL *url.URL) *http.Transport {
	t := opts.Timeouts
	tr := &http.Transport{
		DialContext:           (&net.Dialer{Timeout: t.Dial}).DialContext,
		TLSHandshakeTimeout:   t.TLS,
		ResponseHeaderTimeout: t.Header,
		IdleConnTimeout:       t.Idle,
	}
	if proxyURL != nil {
		tr.Proxy = http.ProxyURL(proxyURL)
	}
	return tr
}
