package httpclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/backtraxe/downloader-go/internal/fsutil"
	"github.com/backtraxe/downloader-go/internal/media"
)

const (
	// defaultMaxAttempts 是未指定 MaxRetries 时的总尝试次数（首次 + 重试）。
	defaultMaxAttempts = 3
	// defaultMinDelay / defaultMaxDelay 是指数退避的默认起点与上限。
	defaultMinDelay = 100 * time.Millisecond
	defaultMaxDelay = 2 * time.Second
)

// DownloadOptions 控制单文件下载的请求头、幂等、重试与落盘行为。
type DownloadOptions struct {
	// Header 随每次尝试发送，可为 nil。
	Header http.Header

	// Force 为 true 时跳过“已存在即跳过”的判断，写 <目标>.part 后原子替换；
	// 中途失败不会破坏旧文件。
	Force bool

	// MaxRetries 是总尝试次数（含首次请求），<= 0 时使用默认值 3。
	MaxRetries int

	// MinDelay / MaxDelay 是退避起点与上限，零值分别表示 100ms / 2s。
	MinDelay time.Duration
	MaxDelay time.Duration

	// Filter 在状态码校验之后、写盘之前调用，用于拦截防盗链 HTML 等
	// 非媒体响应。返回错误即放弃本次尝试且不重试。
	Filter func(contentType string) error

	// OnRetry 在每次重试睡眠前调用，attempt 从 1 开始。
	OnRetry func(attempt int, err error)

	// Sleep 替换 time.Sleep，便于测试；nil 时使用 time.Sleep。
	Sleep func(time.Duration)
}

// DownloadFile 把 rawURL 流式下载到 filepath，返回实际写入的路径。
//
// 与 Python 版 download_file 口径一致：
//   - !Force 时先按主文件名匹配已有文件（跨扩展名变体），命中即跳过；
//   - 成功后按 URL/Content-Type 修正扩展名（如 webp 存成 .jpg）；
//   - !Force 用 fsutil.UniquePath 原子去重，绝不覆盖；Force 写 .part 后
//     原子替换目标文件；
//   - 任一次尝试失败都会清理本次创建的临时/占位文件，不删除原有目标；
//   - 仅对瞬时网络错误与 429/5xx 重试，其余错误立即返回。
func (c *Client) DownloadFile(ctx context.Context, rawURL, target string, opts DownloadOptions) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	if !opts.Force {
		if existing, ok := fsutil.ExistingByStem(target); ok {
			return existing, nil
		}
	}

	attempts := opts.MaxRetries
	if attempts <= 0 {
		attempts = defaultMaxAttempts
	}

	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		saved, err, retryable := c.downloadOnce(ctx, rawURL, target, opts)
		if err == nil {
			return saved, nil
		}
		lastErr = err
		if !retryable || attempt == attempts-1 {
			break
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		if opts.OnRetry != nil {
			opts.OnRetry(attempt+1, err)
		}
		sleepFor(opts.Sleep, backoffDelay(attempt, opts.MinDelay, opts.MaxDelay))
	}
	return "", lastErr
}

// downloadOnce 执行一次下载尝试。retryable 表示该错误是否值得重试。
func (c *Client) downloadOnce(ctx context.Context, rawURL, target string, opts DownloadOptions) (saved string, err error, retryable bool) {
	resp, err := c.Get(ctx, Request{URL: rawURL, Header: opts.Header})
	if err != nil {
		return "", err, IsTransient(err)
	}
	defer resp.Close()

	if resp.StatusCode >= 400 {
		// 状态错误只看状态码：错误文本里带最终 URL，若再用 IsTransient
		// 扫关键词，URL 中的 503/eof/timeout 会被误判为可重试。
		return "", statusError(resp), IsTransientStatus(resp.StatusCode)
	}

	contentType := ""
	if resp.Header != nil {
		contentType = resp.Header.Get("Content-Type")
	}
	if opts.Filter != nil {
		// 过滤命中说明被拦截，换重试也没用，直接放弃。
		if filterErr := opts.Filter(contentType); filterErr != nil {
			return "", filterErr, false
		}
	}

	path := correctedPath(target, rawURL, contentType)

	// created 是本次尝试实际创建的文件（.part 或 UniquePath 占位）；
	// 失败时 defer 清理，成功时置空保留。
	created := ""
	defer func() {
		if err != nil && created != "" {
			_ = os.Remove(created)
		}
	}()

	if opts.Force {
		created = path + ".part"
	} else {
		unique, uniqueErr := fsutil.UniquePath(path)
		if uniqueErr != nil {
			return "", uniqueErr, IsTransient(uniqueErr)
		}
		path = unique
		created = unique
	}

	f, err := os.Create(created)
	if err != nil {
		return "", err, IsTransient(err)
	}
	if _, copyErr := io.Copy(f, resp.Body); copyErr != nil {
		f.Close()
		return "", copyErr, IsTransient(copyErr)
	}
	if closeErr := f.Close(); closeErr != nil {
		return "", closeErr, IsTransient(closeErr)
	}

	if opts.Force {
		if renameErr := os.Rename(created, path); renameErr != nil {
			return "", renameErr, IsTransient(renameErr)
		}
	}
	created = ""
	return path, nil, false
}

// correctedPath 按 URL 后缀与 Content-Type 修正扩展名；猜不出时保留原扩展名。
func correctedPath(target, rawURL, contentType string) string {
	ext := filepath.Ext(target)
	guessed := media.GuessExtension(rawURL, contentType, ext)
	if guessed == "" || strings.EqualFold(guessed, ext) {
		return target
	}
	return strings.TrimSuffix(target, ext) + guessed
}

// statusError 构造 HTTP 状态错误。只包含状态码与（脱敏后的）最终 URL，
// 不包含任何请求头或 Cookie 内容。
func statusError(resp *Response) error {
	target := ""
	if resp.URL != nil {
		target = resp.URL.Redacted()
	}
	return fmt.Errorf("httpclient: HTTP %d %s: %s", resp.StatusCode, http.StatusText(resp.StatusCode), target)
}

// transientKeywords 是可重试错误的子串（大小写不敏感）。同时覆盖
// requests/urllib3、curl 与 Go 原生 net/http 三族消息格式。
var transientKeywords = []string{
	"timeout",
	"timed out",
	"no address associated",
	"name or service not known",
	"nodename nor servname",
	"connection reset",
	"connection was reset",
	"connection aborted",
	"connection broken",
	"connection refused",
	"max retries exceeded",
	"temporary failure",
	"no such host",
	"server misbehaving",
	"http2: server sent goaway",
	"http2: client connection lost",
	"eof",
	"broken pipe",
	"503",
	"502",
}

// IsTransient 判断错误是否为值得重试的瞬时网络错误。
//
// 优先用 errors.As / errors.Is 识别结构化的 Go 原生错误（DNS 临时失败、
// 连接重置/拒绝、响应体意外截断），再回退到跨语言的关键词匹配。
func IsTransient(err error) bool {
	if err == nil {
		return false
	}

	var dnsErr *net.DNSError
	// Temporary() 已废弃；等价地直接看结构体上的 IsTimeout / IsTemporary。
	if errors.As(err, &dnsErr) && (dnsErr.IsTimeout || dnsErr.IsTemporary) {
		return true
	}
	if errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}

	msg := strings.ToLower(err.Error())
	for _, keyword := range transientKeywords {
		if strings.Contains(msg, keyword) {
			return true
		}
	}
	return false
}

// IsTransientStatus 判断 HTTP 状态码是否值得重试：429 与 5xx。
func IsTransientStatus(code int) bool {
	return code == http.StatusTooManyRequests || (code >= 500 && code <= 599)
}

// backoffDelay 计算第 attempt 次（0 起）失败后的退避时长：
// minDelay<<attempt，封顶 maxDelay，并叠加至多 20% 的抖动。
func backoffDelay(attempt int, minDelay, maxDelay time.Duration) time.Duration {
	if minDelay <= 0 {
		minDelay = defaultMinDelay
	}
	if maxDelay <= 0 {
		maxDelay = defaultMaxDelay
	}
	if maxDelay < minDelay {
		maxDelay = minDelay
	}

	delay := minDelay
	for i := 0; i < attempt && delay < maxDelay; i++ {
		delay *= 2
	}
	if delay <= 0 || delay > maxDelay {
		delay = maxDelay
	}
	if jitterCap := delay / 5; jitterCap > 0 {
		delay += rand.N(jitterCap)
	}
	return delay
}

// sleepFor 调用注入的 Sleep，未注入时回退到 time.Sleep。
func sleepFor(sleep func(time.Duration), d time.Duration) {
	if sleep != nil {
		sleep(d)
		return
	}
	time.Sleep(d)
}
