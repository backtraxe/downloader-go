package httpclient

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"

	utls "github.com/refraction-networking/utls"
	"golang.org/x/net/http2"
)

// newUTLSDialer 返回一个使用 utls 指纹完成 TLS 握手的拨号函数。
//
// https 连接不走 Go 标准库的 crypto/tls，而是先建立 TCP，再用 utls 按
// Profile 指定的 ClientHello 指纹握手，以绕过按 JA3/JA4 识别的风控。
func newUTLSDialer(profile Profile, timeouts Timeouts, insecure bool, nextProtos []string) func(ctx context.Context, network, addr string) (net.Conn, error) {
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
		return conn, nil
	}
}

// buildSecureTransport 构造用于 https 的 RoundTripper。
//
// 默认使用 HTTP/2；以下两种情况退回 HTTP/1.1 的 http.Transport：
// 配置了代理（http2.Transport 不支持代理），或显式要求 ForceHTTP1。
func buildSecureTransport(opts Options, proxyURL *url.URL) http.RoundTripper {
	t := opts.Timeouts

	if proxyURL != nil || opts.ForceHTTP1 {
		// HTTP/1.1 传输层只应协商 http/1.1；若仍 advertise h2，服务端可能
		// 选中 h2 而连接上却按 HTTP/1.1 说话，导致请求失败。
		dial := newUTLSDialer(opts.Profile, t, opts.InsecureSkipVerify, []string{"http/1.1"})
		dialTLS := func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dial(ctx, network, addr)
		}
		tr := &http.Transport{
			DialTLSContext:        dialTLS,
			Proxy:                 http.ProxyURL(proxyURL),
			TLSHandshakeTimeout:   t.TLS,
			ResponseHeaderTimeout: t.Header,
			IdleConnTimeout:       t.Idle,
		}
		return tr
	}

	dial := newUTLSDialer(opts.Profile, t, opts.InsecureSkipVerify, []string{"h2", "http/1.1"})
	return &http2.Transport{
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			return dial(ctx, network, addr)
		},
		TLSClientConfig: &tls.Config{InsecureSkipVerify: opts.InsecureSkipVerify},
		ReadIdleTimeout: t.Idle,
		PingTimeout:     t.Idle,
	}
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
