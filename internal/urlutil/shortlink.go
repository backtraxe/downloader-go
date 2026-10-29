package urlutil

import (
	"context"
	"time"

	"github.com/backtraxe/downloader-go/internal/httpclient"
)

// shortLinkBackoff 是短链重试的基础等待时间；第 i 次失败后等待
// (i+1)*shortLinkBackoff（1.5s、3s……），与 Python 参考实现的线性退避
// 一致，但去掉了随机抖动以保证可测试。
const shortLinkBackoff = 1500 * time.Millisecond

// ResolveShortLink 用 HTTP 客户端展开短链，返回跟随重定向后的最终 URL。
//
// 非短链直接原样返回，不产生额外请求。请求失败、客户端未配置或拿不到
// 最终地址时也原样返回——调用方据此回退到原始链接。响应体只关闭不读取，
// 展开短链不需要页面内容。
func ResolveShortLink(ctx context.Context, c *httpclient.Client, rawURL string) string {
	if !IsShortLink(rawURL) {
		return rawURL
	}
	if c == nil {
		return rawURL
	}
	resp, err := c.Get(ctx, httpclient.Request{URL: rawURL})
	if err != nil {
		return rawURL
	}
	defer resp.Close()
	if resp.URL == nil {
		return rawURL
	}
	if final := resp.URL.String(); final != "" {
		return final
	}
	return rawURL
}

// ResolveShortLinkWithRetry 是 ResolveShortLink 的带重试版本。
//
// 短链服务端偶发返回 200 页面而不做 302，因此最多尝试 attempts 次；
// 首次得到与原始链接不同的 URL 即返回。sleep 在两次尝试之间调用（nil 时
// 使用 time.Sleep）。非短链、attempts <= 0、客户端未配置或全部失败时原样返回。
func ResolveShortLinkWithRetry(ctx context.Context, c *httpclient.Client, rawURL string, attempts int, sleep func(time.Duration)) string {
	if !IsShortLink(rawURL) || attempts <= 0 || c == nil {
		return rawURL
	}
	if sleep == nil {
		sleep = time.Sleep
	}
	for i := 0; i < attempts; i++ {
		resolved := ResolveShortLink(ctx, c, rawURL)
		if resolved != rawURL {
			return resolved
		}
		if i+1 < attempts {
			sleep(shortLinkBackoff * time.Duration(i+1))
		}
	}
	return rawURL
}
