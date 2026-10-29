// Package dispatch 负责把链接按站点路由到对应下载后端。
//
// 口径沿用 Python 版 core/downloader.py 的 dispatch_url：小红书、抖音走
// 专用后端（需要 Cookie），bilibili/youtube/instagram/twitter 交给 yt-dlp，
// 其余站点交给通用网页抓取。抖音短链先展开再判断图文/视频类型，yt-dlp 站点
// 的短链也先展开再交给后端；短链服务端偶发不返回 302，因此带 3 次重试。
// 后端函数由调用方注入，未接入的后端（如 Task 15/16 完成前的小红书、抖音）
// 保持 nil，Dispatch 返回明确失败。
package dispatch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"

	"github.com/backtraxe/downloader-go/internal/cookies"
	"github.com/backtraxe/downloader-go/internal/httpclient"
	"github.com/backtraxe/downloader-go/internal/logging"
	"github.com/backtraxe/downloader-go/internal/result"
	"github.com/backtraxe/downloader-go/internal/sites"
	"github.com/backtraxe/downloader-go/internal/urlutil"
)

// shortLinkAttempts 是抖音短链展开的最大尝试次数，与 Python 版一致。
const shortLinkAttempts = 3

// 抖音作品页路由规则，与 Python 版 core/douyin_downloader.py 保持一致：
// /note/<数字ID> 为图文，/video/<数字ID> 为视频。短链展开后才会命中。
var (
	douyinNotePattern  = regexp.MustCompile(`(?i)^https?://(?:www\.)?douyin\.com/note/\d+`)
	douyinVideoPattern = regexp.MustCompile(`(?i)^https?://(?:www\.)?douyin\.com/video/\d+`)
)

// Handlers 汇总各下载后端的入口函数。
//
// 需要 Cookie 的后端接收已转换成请求头格式的 Cookie（name=value; ...）。
// 任一字段为 nil 表示该后端尚未接入，Dispatch 会返回 failed 而不是 panic。
type Handlers struct {
	XHS         func(ctx context.Context, url, cookie string, force bool) result.Result
	DouyinNote  func(ctx context.Context, url, cookie string, force bool) result.Result
	DouyinVideo func(ctx context.Context, url, cookie string, force bool) result.Result
	YTDLP       func(ctx context.Context, url string, force bool) result.Result
	Generic     func(ctx context.Context, url string, force bool) result.Result
}

// Dispatcher 按站点把链接分发给 Handlers。
//
// HTTP 用于展开抖音短链；DataDir 决定 Cookie 文件位置
// （<DataDir>/cookies/<site>.txt）。
type Dispatcher struct {
	HTTP     *httpclient.Client
	DataDir  string
	Handlers Handlers
}

// Dispatch 识别站点并调用对应后端，整个调用共用一个请求日志作用域。
//
// 未接入的后端先于 Cookie 检查返回 failed，避免缺 Cookie 掩盖真实原因。
// 小红书与抖音缺少 Cookie 时返回 failed 并给出 Cookie 路径提示；抖音短链
// 先展开（3 次重试）再判断图文/视频，展开失败或非作品页时沿用已展开的
// URL 回落到 yt-dlp。yt-dlp 站点同样先展开短链再交给后端。force 原样转发。
func (d *Dispatcher) Dispatch(ctx context.Context, rawURL string, force bool) result.Result {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, _ = logging.WithRequest(ctx)
	if d == nil {
		return result.Result{Status: result.Failed, Message: "分发器未配置"}
	}

	site := sites.Detect(rawURL)
	slog.InfoContext(ctx, "开始处理链接", "site", site, "force", force)
	res := d.dispatchSite(ctx, site, rawURL, force)
	slog.InfoContext(ctx, "链接处理结束", "site", site, "status", res.Status)
	return res
}

// dispatchSite 按站点调用对应后端。日志与请求作用域由 Dispatch 统一负责。
func (d *Dispatcher) dispatchSite(ctx context.Context, site, rawURL string, force bool) result.Result {
	switch site {
	case "xiaohongshu":
		if d.Handlers.XHS == nil {
			return notWired("小红书")
		}
		cookie, failure, ok := d.loadCookie("xiaohongshu", "小红书")
		if !ok {
			return failure
		}
		return d.Handlers.XHS(ctx, rawURL, cookie, force)

	case "douyin":
		resolved := urlutil.ResolveShortLinkWithRetry(ctx, d.HTTP, rawURL, shortLinkAttempts, nil)
		switch {
		case douyinNotePattern.MatchString(resolved):
			if d.Handlers.DouyinNote == nil {
				return notWired("抖音图文")
			}
			cookie, failure, ok := d.loadCookie("douyin", "抖音")
			if !ok {
				return failure
			}
			return d.Handlers.DouyinNote(ctx, resolved, cookie, force)
		case douyinVideoPattern.MatchString(resolved):
			if d.Handlers.DouyinVideo == nil {
				return notWired("抖音视频")
			}
			cookie, failure, ok := d.loadCookie("douyin", "抖音")
			if !ok {
				return failure
			}
			return d.Handlers.DouyinVideo(ctx, resolved, cookie, force)
		}
		// 非图文/视频页（含短链展开失败）：与 Python 版一致交给 yt-dlp，
		// 沿用已经展开的 URL，避免重复请求短链服务。
		return d.ytdlpResolved(ctx, resolved, force)

	case "bilibili", "youtube", "instagram", "twitter":
		return d.ytdlp(ctx, rawURL, force)

	default:
		if d.Handlers.Generic == nil {
			return notWired("通用网页")
		}
		return d.Handlers.Generic(ctx, rawURL, force)
	}
}

// ytdlp 先展开短链再调用 yt-dlp 后端，未接入时返回明确失败。
//
// Termux 等环境下 yt-dlp 依赖的 Go/Python socket 解析对 b23.tv、youtu.be
// 一类短链域名可能失败，因此与 Python 版一致先由本包跟一遍重定向；展开
// 失败时保留原始 URL 交给 yt-dlp 兜底。
func (d *Dispatcher) ytdlp(ctx context.Context, rawURL string, force bool) result.Result {
	if d.Handlers.YTDLP == nil {
		return notWired("yt-dlp")
	}
	resolved := urlutil.ResolveShortLinkWithRetry(ctx, d.HTTP, rawURL, shortLinkAttempts, nil)
	return d.Handlers.YTDLP(ctx, resolved, force)
}

// ytdlpResolved 把已经展开过的 URL 直接交给 yt-dlp 后端。
func (d *Dispatcher) ytdlpResolved(ctx context.Context, resolvedURL string, force bool) result.Result {
	if d.Handlers.YTDLP == nil {
		return notWired("yt-dlp")
	}
	return d.Handlers.YTDLP(ctx, resolvedURL, force)
}

// loadCookie 读取站点 Cookie 并转换成请求头格式。
//
// 失败时返回的 result 已带可直接展示的中文提示：文件缺失/为空时提示
// Cookie 路径，其他读取错误带上底层原因。cookie 值本身不会进入提示。
func (d *Dispatcher) loadCookie(site, label string) (string, result.Result, bool) {
	cookie, err := sites.LoadCookie(d.DataDir, site)
	if err != nil {
		if errors.Is(err, sites.ErrNoCookie) {
			return "", result.Result{
				Status:  result.Failed,
				Message: fmt.Sprintf("缺少%s Cookie：%s 为空或不存在", label, sites.CookiePath(d.DataDir, site)),
			}, false
		}
		return "", result.Result{
			Status:  result.Failed,
			Message: fmt.Sprintf("读取%s Cookie 失败：%v", label, err),
		}, false
	}
	return cookies.ToHeader(cookie), result.Result{}, true
}

// notWired 生成后端尚未接入的统一失败结果。
func notWired(backend string) result.Result {
	return result.Result{Status: result.Failed, Message: backend + "后端尚未接入"}
}
