// Package httpclient 提供全站共用的 HTTP 客户端。
//
// https 直连请求使用 utls 模拟浏览器 TLS ClientHello 指纹，默认走 HTTP/2，
// 服务端不支持 h2 时自动回退到 HTTP/1.1；http:// 请求使用标准 net/http
// 传输层。按 URL scheme 逐跳选择传输层，因此跨协议重定向不会丢失指纹。
//
// 经 Options.Proxy 的请求例外：Go 的 DialTLSContext 对代理 CONNECT 隧道内的
// HTTPS 不生效，代理路径使用 HTTP/1.1 + 标准库 TLS，不使用 utls 指纹。
//
// 所有请求共享一个 CookieJar，重定向与后续请求会自动携带服务端下发的 Cookie。
package httpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"

	utls "github.com/refraction-networking/utls"
)

// maxRedirects 是单次请求允许跟随的最大重定向次数。
const maxRedirects = 10

// Profile 描述一套 TLS 指纹与配套的 User-Agent。
//
// Name 与 UserAgent 的版本号必须保持一致：UA 与指纹版本错配会被站点
// 识别为异常流量（Python 参考实现中曾因此被 302 到 /login）。
type Profile struct {
	Name      string
	Hello     utls.ClientHelloID
	UserAgent string
}

const (
	chrome133UserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
		"(KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36"
	chrome120UserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
		"(KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
)

var (
	// Chrome133 是默认配置，UA 与 utls 指纹版本严格一致
	// （utls v1.8.2 尚不提供 HelloChrome_155）。
	Chrome133 = Profile{
		Name:      "chrome133",
		Hello:     utls.HelloChrome_133,
		UserAgent: chrome133UserAgent,
	}

	// Chrome120 对 TLS 版本要求更保守的站点使用。
	Chrome120 = Profile{
		Name:      "chrome120",
		Hello:     utls.HelloChrome_120,
		UserAgent: chrome120UserAgent,
	}
)

// Timeouts 汇总客户端各阶段的超时。
type Timeouts struct {
	Dial   time.Duration // TCP 建连
	TLS    time.Duration // TLS 握手
	Header time.Duration // 等待响应头（HTTP/1.1）
	Idle   time.Duration // 空闲连接 / HTTP/2 读空闲
}

// DefaultTimeouts 返回默认超时：拨号 15s、TLS 15s、响应头 30s、空闲 30s。
func DefaultTimeouts() Timeouts {
	return Timeouts{
		Dial:   15 * time.Second,
		TLS:    15 * time.Second,
		Header: 30 * time.Second,
		Idle:   30 * time.Second,
	}
}

// Options 是创建 Client 的配置。
type Options struct {
	Profile  Profile
	Timeouts Timeouts

	// Proxy 可选，http(s)://host:port；空串表示直连。
	//
	// 配置代理后 https 固定走 HTTP/1.1。注意 Go 的 DialTLSContext 对代理
	// CONNECT 隧道内的 HTTPS 不生效，因此经代理的 HTTPS 使用标准库 TLS，
	// 不套用 utls 指纹；只有直连 HTTPS 才会模拟浏览器 fingerprint。
	Proxy string

	InsecureSkipVerify bool // 仅测试/自签证书场景使用
	ForceHTTP1         bool // 强制 https 走 HTTP/1.1（部分站点拒绝 h2 时使用）
}

// Request 描述一次 GET 请求。
type Request struct {
	URL    string
	Header http.Header
	Cookie string // 整段 Cookie 请求头，会追加到已有 Cookie 之后
}

// Response 是 Get 的结果。Body 由调用方负责 Close。
type Response struct {
	StatusCode int
	Header     http.Header
	Body       io.ReadCloser
	URL        *url.URL // 跟随重定向后的最终 URL
}

// ReadAll 读取响应体。limit <= 0 表示读到 EOF，否则最多读取 limit 字节。
func (r *Response) ReadAll(limit int64) ([]byte, error) {
	if r.Body == nil {
		return nil, errors.New("httpclient: response body is nil")
	}
	if limit <= 0 {
		return io.ReadAll(r.Body)
	}
	return io.ReadAll(io.LimitReader(r.Body, limit))
}

// JSON 把响应体解码为 v。
func (r *Response) JSON(v any) error {
	if r.Body == nil {
		return errors.New("httpclient: response body is nil")
	}
	return json.NewDecoder(r.Body).Decode(v)
}

// Close 关闭响应体。可重复调用。
func (r *Response) Close() error {
	if r.Body == nil {
		return nil
	}
	return r.Body.Close()
}

// RaiseForStatus 在状态码 >= 400 时返回错误，错误信息包含状态码与最终 URL。
func (r *Response) RaiseForStatus() error {
	if r.StatusCode < 400 {
		return nil
	}
	target := ""
	if r.URL != nil {
		target = r.URL.String()
	}
	return fmt.Errorf("httpclient: HTTP %d %s: %s", r.StatusCode, http.StatusText(r.StatusCode), target)
}

// Client 是并发安全的 HTTP 客户端。
type Client struct {
	opts    Options
	http    *http.Client
	initErr error
}

// New 创建 Client。Profile 为零值时使用 Chrome133，Timeouts 为零值时使用默认值。
//
// 代理地址非法时不会 panic，错误会由首次 Get 返回。
func New(opts Options) *Client {
	if opts.Profile.Name == "" {
		opts.Profile = Chrome133
	}
	if opts.Timeouts == (Timeouts{}) {
		opts.Timeouts = DefaultTimeouts()
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		// cookiejar.New(nil) 目前不会失败，保留兜底以免未来行为变化。
		return &Client{opts: opts, initErr: fmt.Errorf("httpclient: create cookie jar: %w", err)}
	}

	proxyURL, err := parseProxy(opts.Proxy)
	if err != nil {
		return &Client{opts: opts, initErr: err}
	}

	// 只创建一个 http.Client，由 schemeRouter 在每一跳按 scheme 选择
	// 传输层；这样跨协议重定向（http↔https）不会走错 transport。
	router := &schemeRouter{
		plain:  buildPlainTransport(opts, proxyURL),
		secure: buildSecureTransport(opts, proxyURL),
	}

	return &Client{
		opts: opts,
		http: &http.Client{
			Transport: router,
			Jar:       jar,
			CheckRedirect: func(_ *http.Request, via []*http.Request) error {
				if len(via) >= maxRedirects {
					return fmt.Errorf("httpclient: stopped after %d redirects", maxRedirects)
				}
				return nil
			},
		},
	}
}

// Get 发起 GET 请求并返回响应（已跟随重定向）。
//
// User-Agent 默认取 Profile 的 UA；若 Request.Header 显式提供了
// User-Agent，则尊重调用方的值。
func (c *Client) Get(ctx context.Context, req Request) (*Response, error) {
	if c.initErr != nil {
		return nil, c.initErr
	}
	if ctx == nil {
		ctx = context.Background()
	}

	u, err := url.Parse(strings.TrimSpace(req.URL))
	if err != nil {
		return nil, fmt.Errorf("httpclient: parse url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("httpclient: unsupported url scheme %q", u.Scheme)
	}

	// Header 超时覆盖“发出请求到收到响应头”这段（HTTP/2 没有
	// ResponseHeaderTimeout 可用）。拿到响应头后停表；body 关闭时
	// cancel，避免请求上下文与定时器泄漏。
	reqCtx, cancel := context.WithCancel(ctx)
	if headerTimeout := c.opts.Timeouts.Header; headerTimeout > 0 {
		timer := time.AfterFunc(headerTimeout, cancel)
		defer timer.Stop()
	}

	hreq, err := http.NewRequestWithContext(reqCtx, http.MethodGet, u.String(), nil)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("httpclient: build request: %w", err)
	}
	for key, values := range req.Header {
		for _, value := range values {
			hreq.Header.Add(key, value)
		}
	}
	if hreq.Header.Get("User-Agent") == "" && c.opts.Profile.UserAgent != "" {
		hreq.Header.Set("User-Agent", c.opts.Profile.UserAgent)
	}
	if req.Cookie != "" {
		if existing := hreq.Header.Get("Cookie"); existing != "" {
			hreq.Header.Set("Cookie", existing+"; "+req.Cookie)
		} else {
			hreq.Header.Set("Cookie", req.Cookie)
		}
	}

	resp, err := c.http.Do(hreq)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("httpclient: GET %s: %w", u.Redacted(), err)
	}

	finalURL := u
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL
	}

	// HTTP/2 由 transport 的 ReadIdleTimeout 负责；HTTP/1.1 与明文连接没有
	// 等价机制，服务端可以在发完响应头后无限期停住，卡死串行 bot。这里按
	// 读取空闲时间（不是总时长）兜底，媒体下载因此没有总时长上限。
	body := resp.Body
	if resp.ProtoMajor != 2 {
		body = newIdleTimeoutBody(body, c.opts.Timeouts.Idle)
	}
	return &Response{
		StatusCode: resp.StatusCode,
		Header:     resp.Header,
		Body:       &cancelOnCloseBody{ReadCloser: body, cancel: cancel},
		URL:        finalURL,
	}, nil
}

// cancelOnCloseBody 在响应体关闭时取消请求上下文，释放 Header 超时定时器
// 所依赖的 context。
type cancelOnCloseBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *cancelOnCloseBody) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}

// errReadIdleTimeout 表示响应体在限定时间内没有读到任何数据。
var errReadIdleTimeout = errors.New("httpclient: body read idle timeout")

// idleTimeoutBody 为响应体增加“读取空闲超时”：每次 Read 开始前重置计时器，
// 超时后关闭底层 body，从而唤醒被阻塞的 Read。
//
// 计时器按单次 Read 的空闲时间计算，不是整个响应的总时长；持续有数据
// 到达的媒体下载不会因此被中断。
type idleTimeoutBody struct {
	rc       io.ReadCloser
	idle     time.Duration
	timer    *time.Timer
	mu       sync.Mutex
	timedOut bool
	closed   bool
}

// newIdleTimeoutBody 包装 rc；idle <= 0 或 rc 为空时原样返回。
func newIdleTimeoutBody(rc io.ReadCloser, idle time.Duration) io.ReadCloser {
	if rc == nil || idle <= 0 {
		return rc
	}
	b := &idleTimeoutBody{rc: rc, idle: idle}
	b.timer = time.AfterFunc(idle, b.onTimeout)
	return b
}

// onTimeout 标记超时并关闭底层连接，使阻塞中的 Read 立即返回。
func (b *idleTimeoutBody) onTimeout() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.timedOut = true
	b.mu.Unlock()
	_ = b.rc.Close()
}

// Read 在每次读取前重置空闲计时器，超时后返回 errReadIdleTimeout。
func (b *idleTimeoutBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return 0, errReadIdleTimeout
	}
	b.timer.Reset(b.idle)
	b.mu.Unlock()

	n, err := b.rc.Read(p)

	b.mu.Lock()
	timedOut := b.timedOut
	if !timedOut && !b.closed {
		b.timer.Reset(b.idle)
	}
	b.mu.Unlock()
	if timedOut {
		return n, errReadIdleTimeout
	}
	return n, err
}

// Close 停止计时器并关闭底层 body；可重复调用。
func (b *idleTimeoutBody) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	b.timer.Stop()
	b.mu.Unlock()
	return b.rc.Close()
}

// parseProxy 解析代理地址；缺少 scheme 时按 http:// 补全。
// 错误信息不包含原始地址，避免泄露代理凭据。
func parseProxy(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil, errors.New("httpclient: invalid proxy url")
	}
	return u, nil
}
