package xhs

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/backtraxe/downloader-go/internal/fsutil"
	"github.com/backtraxe/downloader-go/internal/httpclient"
	"github.com/backtraxe/downloader-go/internal/media"
	"github.com/backtraxe/downloader-go/internal/result"
)

const (
	xhsSite      = "xiaohongshu"
	downloadRoot = "download"

	// maxPageBytes 限制单次页面响应体，避免异常响应打满内存。
	maxPageBytes = 16 << 20

	// downloadConcurrency 与 Python 版 ThreadPoolExecutor(max_workers=5) 一致。
	downloadConcurrency = 5

	// pageAttempts 是页面请求的总尝试次数（含首次）；瞬时网络错误与
	// 429/5xx 按 pageBackoff 退避。
	pageAttempts = 3
)

// pageBackoff 是页面请求失败后每次重试前的等待时间。
var pageBackoff = []time.Duration{300 * time.Millisecond, 600 * time.Millisecond}

// directNoteHosts 是小红书直链站点根域名。短链域名（xhslink.com/.cn）
// 不在此列：短链的 token 只存在于 302 目标里，不能做事前 token 预检。
var directNoteHosts = []string{"xiaohongshu.com", "rednote.com"}

// isDirectNoteURL 判断 rawURL 是否为小红书直链站点。
func isDirectNoteURL(rawURL string) bool {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, root := range directNoteHosts {
		if host == root || strings.HasSuffix(host, "."+root) {
			return true
		}
	}
	return false
}

// isNotePageURL 判断 URL 路径是否为带作品 ID 的笔记页。
func isNotePageURL(rawURL string) bool {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return false
	}
	return notePathPattern.MatchString(strings.TrimRight(u.Path, "/"))
}

// Downloader 是小红书后端的配置与入口。
type Downloader struct {
	HTTP    *httpclient.Client
	DataDir string
}

// Download 下载小红书笔记的全部媒体（图片、实况视频、笔记级视频）。
//
// 链接必须带 xsec_token：裸 /explore/<id> 会被风控落地到 404，报“笔记已
// 删除”会误导用户。失效短链（重定向到首页且无笔记数据）单独提示。
func (d *Downloader) Download(ctx context.Context, rawURL, cookie string, force bool) result.Result {
	if ctx == nil {
		ctx = context.Background()
	}
	if d == nil || d.HTTP == nil {
		return failed("下载客户端未配置")
	}

	// token 门禁只对直链生效：裸 /explore/<id> 的 404 落地页与“笔记已删除”
	// 共用页面与文案，先请求再判断会把缺参数误报成笔记不存在。
	// xhslink 短链的 token 只存在于 302 目标里，必须跳过本地预检先跟随跳转。
	if isDirectNoteURL(rawURL) && !HasXsecToken(rawURL) {
		return failed("链接必须带 xsec_token（请在 App 内重新分享该笔记获取完整链接）")
	}

	body, finalURL, status, err := d.fetchPage(ctx, rawURL, cookie)
	if err != nil {
		return failed("请求失败（网络异常或 Cookie/风控问题）")
	}
	html := string(body)
	if finalURL == "" {
		finalURL = rawURL
	}

	if status == 461 || status == 471 || detectRiskControl(html, finalURL) {
		return failed("触发风控拦截或 Cookie 已失效（请在浏览器完成验证或刷新 Cookie 后重试）")
	}
	// 缺 token/过期 token 被风控落地（error_code=300031）必须先于
	// “笔记不存在”判定，否则会把链接问题误报为笔记已删除。
	if detectMissingXsecToken(html, finalURL) {
		return failed("链接缺少 xsec_token（请用 App 重新分享）")
	}
	if detectNoteNotFound(html, finalURL) {
		return failed("笔记已被删除或不存在")
	}
	// 其余 4xx/5xx 不进入首页/结构判定，避免把服务端错误误报成链接失效。
	if status >= http.StatusBadRequest {
		return failed(fmt.Sprintf("请求失败（HTTP %d）", status))
	}
	// 失效短链被 307 到首页：页面 200 正常、noteDetailMap 为空。
	// 首页判定必须先于 token 判定，否则过期短链会被误报成缺 token。
	if IsHomepageLanding(finalURL, html) {
		return failed("分享链接已失效（重定向到了首页）——短链会过期，请在 App 内重新分享该笔记获取新链接")
	}
	// 短链跳到笔记页但 302 目标仍没有 token：此时才提示 token。
	if isNotePageURL(finalURL) && !HasXsecToken(finalURL) {
		return failed("链接缺少 xsec_token（请用 App 重新分享）")
	}

	note, err := ParseInitialState(html)
	if err != nil {
		switch {
		case errors.Is(err, ErrNoNoteData):
			return failed("提取详情失败（页面结构可能已更新）")
		case errors.Is(err, ErrNoInitialState):
			return failed("未找到页面数据（页面结构变更或 Cookie 失效）")
		default:
			return failed("解析页面数据失败: " + err.Error())
		}
	}

	baseTitle := resolveBasename(note)
	safeTitle := baseTitle
	if note.ID != "" {
		safeTitle = baseTitle + "_" + note.ID
	}
	root := filepath.Join(d.DataDir, downloadRoot)
	dir := fsutil.BuildDownloadDir(root, xhsSite, safeTitle, note.Author)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return failed("创建目录失败: " + err.Error())
	}

	tasks := buildTasks(note, dir)
	counts, files := d.downloadTasks(ctx, tasks, finalURL, cookie, force)

	res := result.FromCounts(counts)
	res.Posts = []result.Post{{
		Site:   xhsSite,
		Author: note.Author,
		Title:  baseTitle,
		Status: res.Status,
		Files:  files,
	}}
	return res
}

// fetchPage 请求笔记页并跟随重定向，返回响应体、最终 URL 与状态码。
func (d *Downloader) fetchPage(ctx context.Context, rawURL, cookie string) ([]byte, string, int, error) {
	var lastErr error
	status := 0
	for attempt := 0; attempt < pageAttempts; attempt++ {
		resp, err := d.HTTP.Get(ctx, httpclient.Request{
			URL:    ensureHTTPS(rawURL),
			Header: pageHeaders(),
			Cookie: cookie,
		})
		if err != nil {
			lastErr = err
			if d.canRetry(ctx, attempt) && httpclient.IsTransient(err) {
				time.Sleep(backoffFor(attempt))
				continue
			}
			return nil, "", 0, err
		}

		body, readErr := resp.ReadAll(maxPageBytes)
		status = resp.StatusCode
		finalURL := ""
		if resp.URL != nil {
			finalURL = resp.URL.String()
		}
		_ = resp.Close()
		if readErr != nil {
			lastErr = readErr
			if d.canRetry(ctx, attempt) && httpclient.IsTransient(readErr) {
				time.Sleep(backoffFor(attempt))
				continue
			}
			return nil, finalURL, status, readErr
		}
		if d.canRetry(ctx, attempt) && httpclient.IsTransientStatus(status) {
			time.Sleep(backoffFor(attempt))
			continue
		}
		return body, finalURL, status, nil
	}
	return nil, "", status, lastErr
}

// canRetry 判断当前失败是否还有下一次尝试机会（上下文取消即停止）。
func (d *Downloader) canRetry(ctx context.Context, attempt int) bool {
	return attempt < pageAttempts-1 && ctx.Err() == nil
}

// backoffFor 取第 attempt 次失败后的等待时间；表耗尽时沿用最后一档。
func backoffFor(attempt int) time.Duration {
	if len(pageBackoff) == 0 {
		return 0
	}
	if attempt < 0 {
		attempt = 0
	}
	if attempt >= len(pageBackoff) {
		attempt = len(pageBackoff) - 1
	}
	return pageBackoff[attempt]
}

// mediaTask 是一个待下载的媒体直链与目标路径。
type mediaTask struct {
	url    string
	target string
}

// buildTasks 按 Python 版顺序构造下载任务：
// 图片按 001.ext 编号，实况视频沿用图片序号，笔记级视频为 video.mp4。
func buildTasks(note *Note, dir string) []mediaTask {
	tasks := make([]mediaTask, 0, len(note.Images)+len(note.LivePairs)+1)
	for index, image := range note.Images {
		number := index + 1
		if image.URL != "" {
			ext := image.Ext
			if ext == "" {
				ext = ".jpg"
			}
			tasks = append(tasks, mediaTask{
				url:    ensureHTTPS(image.URL),
				target: filepath.Join(dir, fmt.Sprintf("%03d%s", number, ext)),
			})
		}
		if index < len(note.liveByIndex) && note.liveByIndex[index] != "" {
			tasks = append(tasks, mediaTask{
				url:    ensureHTTPS(note.liveByIndex[index]),
				target: filepath.Join(dir, fmt.Sprintf("%03d.mp4", number)),
			})
		}
	}
	if note.VideoURL != "" {
		tasks = append(tasks, mediaTask{
			url:    ensureHTTPS(note.VideoURL),
			target: filepath.Join(dir, "video.mp4"),
		})
	}
	return tasks
}

// outcome 是单个媒体任务的下载结论。
type outcome int

const (
	outcomeDownloaded outcome = iota
	outcomeSkipped
	outcomeFailed
)

// downloadTasks 以固定并发下载全部任务，结果按任务原始顺序汇总。
func (d *Downloader) downloadTasks(ctx context.Context, tasks []mediaTask, referer, cookie string, force bool) (result.Counts, []string) {
	var counts result.Counts
	if len(tasks) == 0 {
		return counts, nil
	}

	type taskResult struct {
		index   int
		outcome outcome
		saved   string
	}

	header := mediaHeaders(referer, cookie)
	jobs := make(chan int)
	results := make(chan taskResult, len(tasks))

	workers := downloadConcurrency
	if len(tasks) < workers {
		workers = len(tasks)
	}
	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				got, saved := d.downloadOne(ctx, tasks[index], header, force)
				results <- taskResult{index: index, outcome: got, saved: saved}
			}
		}()
	}
	for index := range tasks {
		jobs <- index
	}
	close(jobs)
	wg.Wait()
	close(results)

	saved := make([]string, len(tasks))
	kept := make([]bool, len(tasks))
	for item := range results {
		switch item.outcome {
		case outcomeDownloaded:
			counts.Downloaded++
			saved[item.index] = item.saved
			kept[item.index] = true
		case outcomeSkipped:
			counts.Skipped++
		case outcomeFailed:
			counts.Failed++
		}
	}

	files := make([]string, 0, counts.Downloaded)
	for index, ok := range kept {
		if ok {
			files = append(files, saved[index])
		}
	}
	return counts, files
}

// downloadOne 处理单个媒体任务：幂等判断、下载与 HTML 拦截。
func (d *Downloader) downloadOne(ctx context.Context, task mediaTask, header http.Header, force bool) (outcome, string) {
	if !force {
		// 同一序号已有任意扩展名产物（001.jpg / 001.png / 001.mp4）即视为
		// 已下载，避免重复链接产生 001_1 副本。
		if _, exists := fsutil.ExistingByStem(task.target); exists {
			return outcomeSkipped, ""
		}
	}
	saved, err := d.HTTP.DownloadFile(ctx, task.url, task.target, httpclient.DownloadOptions{
		Header: header,
		Force:  force,
		Filter: rejectHTML,
	})
	if err != nil {
		return outcomeFailed, ""
	}
	return outcomeDownloaded, saved
}

// rejectHTML 把防盗链/风控拦截返回的 HTML 响应判为失败（不重试）。
func rejectHTML(contentType string) error {
	if media.IsHTMLContent(contentType) {
		return errors.New("被拦截：响应为 HTML（非媒体直链）")
	}
	return nil
}

// pageHeaders 是笔记页请求头。UA 由注入的 httpclient Profile 提供，
// 不在此硬编码，避免与 TLS 指纹版本错配。
func pageHeaders() http.Header {
	header := http.Header{}
	header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8")
	header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	header.Set("Sec-Ch-Ua-Platform", `"Windows"`)
	header.Set("Referer", "https://www.xiaohongshu.com/")
	return header
}

// mediaHeaders 是媒体下载请求头：Referer 为页面最终 URL，Cookie 沿用
// 页面请求的同一段请求头。UA 同样交给 httpclient Profile。
func mediaHeaders(referer, cookie string) http.Header {
	header := http.Header{}
	if referer != "" {
		header.Set("Referer", referer)
	}
	if cookie != "" {
		header.Set("Cookie", cookie)
	}
	return header
}

// resolveBasename 解析目录基名：title → desc（前 30 字）→ tagList → "xhs"。
func resolveBasename(note *Note) string {
	if title := strings.TrimSpace(note.Title); title != "" {
		if cleaned := fsutil.SanitizeFilename(title, "", 0); cleaned != "" {
			return cleaned
		}
	}

	if desc := strings.TrimSpace(note.Desc); desc != "" {
		desc = tagMarkerPattern.ReplaceAllString(desc, "#$1")
		if runes := []rune(desc); len(runes) > 30 {
			desc = string(runes[:30])
		}
		if cleaned := fsutil.SanitizeFilename(desc, "", 0); cleaned != "" {
			return cleaned
		}
	}

	if len(note.tags) > 0 {
		parts := make([]string, len(note.tags))
		for index, tag := range note.tags {
			parts[index] = "#" + tag
		}
		if cleaned := fsutil.SanitizeFilename(strings.Join(parts, "_"), "", 0); cleaned != "" {
			return cleaned
		}
	}
	return "xhs"
}

// detectRiskControl 综合状态码、文案与重定向落地页判断是否触发风控。
//
// 重定向只看 host+path，不看 query：小红书 404 落地页的 query 带
// verifyMsg=，全串匹配会误报成验证页并掩盖真实原因。
func detectRiskControl(html, finalURL string) bool {
	if strings.Contains(html, "验证码") || strings.Contains(html, "访问过于频繁") {
		return true
	}
	u, err := url.Parse(strings.ToLower(finalURL))
	if err != nil {
		return false
	}
	hostpath := u.Host + u.Path
	return strings.Contains(hostpath, "verify") ||
		strings.Contains(hostpath, "/login") ||
		strings.Contains(hostpath, "captcha")
}

// detectMissingXsecToken 检测裸/过期 token 被风控落到 404 页（300031）。
func detectMissingXsecToken(html, finalURL string) bool {
	lower := strings.ToLower(finalURL)
	if strings.Contains(lower, "error_code=300031") {
		return true
	}
	return strings.Contains(html, "当前笔记暂时无法浏览")
}

// detectNoteNotFound 检测笔记已删除/不存在而落地到 404 页。
func detectNoteNotFound(html, finalURL string) bool {
	lower := strings.ToLower(finalURL)
	if strings.Contains(lower, "/404") ||
		strings.Contains(lower, "errorcode=-510001") ||
		strings.Contains(lower, "errorcode=-510000") {
		return true
	}
	return strings.Contains(html, "你访问的页面不见了")
}

// failed 生成统一的失败结果。
func failed(message string) result.Result {
	return result.Result{Status: result.Failed, Message: message}
}
