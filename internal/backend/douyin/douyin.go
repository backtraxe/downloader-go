package douyin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/backtraxe/downloader-go/internal/cookies"
	"github.com/backtraxe/downloader-go/internal/douyinsign"
	"github.com/backtraxe/downloader-go/internal/fsutil"
	"github.com/backtraxe/downloader-go/internal/httpclient"
	"github.com/backtraxe/downloader-go/internal/media"
	"github.com/backtraxe/downloader-go/internal/result"
)

const (
	douyinSite = "douyin"
	// downloadRoot 与 Python 版 build_download_dir 的默认根目录一致。
	downloadRoot = "download"

	// douyinUserAgent 必须与 httpclient 实际的 Chrome133 指纹版本一致；
	// a_bogus 的摘要覆盖 UA，query 里的指纹段也会被服务端交叉核对。
	douyinUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
		"(KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36"

	// maxPageBytes 限制单次页面/详情响应体，避免异常响应打满内存。
	maxPageBytes = 16 << 20

	// downloadConcurrency 与 Python 版 ThreadPoolExecutor(max_workers=5) 一致。
	downloadConcurrency = 5

	// cookieRefreshHint 是面向用户的 Cookie 刷新提示。
	cookieRefreshHint = "请刷新 Cookie（cookies/douyin.txt）后重试"
)

// msTokenAlphabet 与 Python 版伪造 msToken 的字符表一致。
const msTokenAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+-"

// detailAPIBase 是签名 detail API 地址；测试替换为 httptest 地址以离线运行。
var detailAPIBase = "https://www.douyin.com/aweme/v1/web/aweme/detail/"

// apiBaseParams 是 detail API 的基础 query 参数，逐项与发出去的 Chrome133
// UA 指纹对齐（字段名与 Python 版一致；版本号跟随实际 utls 指纹 133）。
var apiBaseParams = [][2]string{
	{"device_platform", "webapp"},
	{"aid", "6383"},
	{"channel", "channel_pc_web"},
	{"pc_client_type", "1"},
	{"version_code", "290100"},
	{"version_name", "29.1.0"},
	{"cookie_enabled", "true"},
	{"screen_width", "1920"},
	{"screen_height", "1080"},
	{"browser_language", "zh-CN"},
	{"browser_platform", "Win32"},
	{"browser_name", "Chrome"},
	{"browser_version", "133.0.0.0"},
	{"browser_online", "true"},
	{"engine_name", "Blink"},
	{"engine_version", "133.0.0.0"},
	{"os_name", "Windows"},
	{"os_version", "10"},
	{"cpu_core_num", "8"},
	{"device_memory", "8"},
	{"platform", "PC"},
	{"downlink", "10"},
	{"effective_type", "4g"},
	{"round_trip_time", "0"},
	{"update_version_code", "170400"},
}

// Downloader 是抖音后端的配置与入口。
//
// Now 与 RNG 可注入以便测试签名时间戳与随机噪声；为零值时分别使用
// time.Now 与 douyinsign.NewABogus 的默认随机源。
type Downloader struct {
	HTTP    *httpclient.Client
	DataDir string
	Now     func() time.Time
	RNG     douyinsign.RNG
}

// DownloadNote 下载抖音图文笔记的图片、实况视频与笔记级视频。
func (d *Downloader) DownloadNote(ctx context.Context, rawURL, cookie string, force bool) result.Result {
	if ctx == nil {
		ctx = context.Background()
	}
	if d == nil || d.HTTP == nil {
		return failed("下载客户端未配置")
	}

	resp, err := d.HTTP.Get(ctx, httpclient.Request{
		URL:    rawURL,
		Header: pageHeaders(),
		Cookie: cookie,
	})
	if err != nil {
		return failed("请求失败（网络异常）")
	}
	body, readErr := resp.ReadAll(maxPageBytes)
	statusCode := resp.StatusCode
	_ = resp.Close()
	if readErr != nil {
		return failed("读取页面失败（网络异常）")
	}
	if statusCode >= 400 {
		return failed(fmt.Sprintf("请求失败（HTTP %d）", statusCode))
	}
	html := string(body)

	switch DetectChallenge(html) {
	case CaptchaChallenge:
		return failed("触发抖音验证码中间页（IP 高频或网络被限流）")
	case JSVMChallenge:
		return failed("抖音返回反爬挑战页（Cookie 缺失或失效）。" + cookieRefreshHint)
	}

	awemeID := noteIDFromURL(rawURL)
	aweme, parseErr := ParsePaceF(html)
	if parseErr != nil {
		// 页面结构变更时退回签名 detail API（签名链路独立于 HTML 链路）。
		if awemeID == "" {
			return failed("未定位到笔记详情（页面结构变更或 Cookie 失效）")
		}
		fallback, _, apiErr := d.fetchDetail(ctx, awemeID, cookie)
		if apiErr != nil {
			return failed(apiErr.Error())
		}
		aweme = fallback
	}
	if awemeID == "" {
		awemeID = aweme.ID
	}

	baseTitle := resolveBasename(aweme.Desc)
	title := baseTitle
	if awemeID != "" {
		title = baseTitle + "_" + awemeID
	}
	root := filepath.Join(d.DataDir, downloadRoot)
	dir := fsutil.BuildDownloadDir(root, douyinSite, title, aweme.Author)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return failed("创建目录失败: " + err.Error())
	}

	tasks := buildNoteTasks(aweme, dir)
	counts, files := d.downloadTasks(ctx, tasks, rawURL, force)

	res := result.FromCounts(counts)
	res.Posts = []result.Post{{
		Site:   douyinSite,
		Author: aweme.Author,
		Title:  baseTitle,
		Status: res.Status,
		Files:  files,
	}}
	return res
}

// DownloadVideo 下载抖音视频（/video/<id>），走签名 detail API。
func (d *Downloader) DownloadVideo(ctx context.Context, rawURL, cookie string, force bool) result.Result {
	if ctx == nil {
		ctx = context.Background()
	}
	if d == nil || d.HTTP == nil {
		return failed("下载客户端未配置")
	}

	awemeID := videoIDFromURL(rawURL)
	if awemeID == "" {
		return failed("无法从 URL 提取视频 ID")
	}
	jar, _, err := cookies.ParseMap(cookie)
	if err != nil {
		return failed("解析 Cookie 失败")
	}
	if douyinsign.PickUIFID(jar) == "" {
		return failed("缺少 UIFID（或 UIFID_TEMP），无法生成抖音签名，" + cookieRefreshHint)
	}

	aweme, detail, err := d.fetchDetail(ctx, awemeID, cookie)
	if err != nil {
		return failed(err.Error())
	}
	if aweme.ID != "" {
		awemeID = aweme.ID
	}

	video, _ := detail["video"].(map[string]any)
	playURLs := videoPlayURLs(video)
	if len(playURLs) == 0 && aweme.VideoURL != "" && !strings.Contains(aweme.VideoURL, playWatermarkMark) {
		playURLs = []string{aweme.VideoURL}
	}
	if len(playURLs) == 0 {
		return postResult(result.Failed, aweme, "详情中未找到可播放直链", nil)
	}

	baseTitle := resolveBasename(aweme.Desc)
	safeTitle := baseTitle
	if awemeID != "" {
		safeTitle = baseTitle + "_" + awemeID
	}
	// 单视频直接落在站点/作者层，与 yt-dlp 后端同构。
	root := filepath.Join(d.DataDir, downloadRoot)
	parent := filepath.Dir(fsutil.BuildDownloadDir(root, douyinSite, safeTitle, aweme.Author))
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return postResult(result.Failed, aweme, "创建目录失败: "+err.Error(), nil)
	}
	target := filepath.Join(parent, safeTitle+".mp4")
	header := mediaHeaders("https://www.douyin.com/")

	if !force {
		if _, exists := fsutil.ExistingByStem(target); exists {
			return postResult(result.Skipped, aweme, "视频已存在", nil)
		}
	}

	for _, candidate := range playURLs {
		saved, downloadErr := d.HTTP.DownloadFile(ctx, ensureHTTPS(candidate), target, httpclient.DownloadOptions{
			Header: header,
			Force:  force,
			Filter: rejectHTML,
		})
		if downloadErr != nil {
			continue
		}
		return postResult(result.Downloaded, aweme, "视频已下载", []string{saved})
	}
	return postResult(result.Failed, aweme, fmt.Sprintf("%d 个播放地址候选全部下载失败", len(playURLs)), nil)
}

// postResult 构造带单个 Post 的失败/成功结果。
func postResult(status result.Status, aweme *Aweme, message string, files []string) result.Result {
	post := result.Post{
		Site:   douyinSite,
		Author: aweme.Author,
		Title:  resolveBasename(aweme.Desc),
		Status: status,
		Files:  files,
	}
	if status == result.Failed {
		post.Error = message
	}
	return result.Result{Status: status, Message: message, Posts: []result.Post{post}}
}

// mediaTask 是一个待下载的媒体直链与目标路径。
type mediaTask struct {
	url    string
	target string
}

// buildNoteTasks 按 Python 版顺序构造下载任务：
// 图片按 001.ext 编号，实况视频沿用图片序号，笔记级视频为 video.mp4。
func buildNoteTasks(aweme *Aweme, dir string) []mediaTask {
	tasks := make([]mediaTask, 0, len(aweme.Images)+len(aweme.LivePairs)+1)

	liveByImage := make(map[string]string, len(aweme.LivePairs))
	for _, pair := range aweme.LivePairs {
		if pair.ImageURL == "" {
			continue
		}
		if _, exists := liveByImage[pair.ImageURL]; !exists {
			liveByImage[pair.ImageURL] = pair.VideoURL
		}
	}

	for index, image := range aweme.Images {
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
		if live := liveByImage[image.URL]; live != "" {
			tasks = append(tasks, mediaTask{
				url:    ensureHTTPS(live),
				target: filepath.Join(dir, fmt.Sprintf("%03d.mp4", number)),
			})
		}
	}
	if aweme.VideoURL != "" {
		tasks = append(tasks, mediaTask{
			url:    ensureHTTPS(aweme.VideoURL),
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
func (d *Downloader) downloadTasks(ctx context.Context, tasks []mediaTask, referer string, force bool) (result.Counts, []string) {
	var counts result.Counts
	if len(tasks) == 0 {
		return counts, nil
	}

	type taskResult struct {
		index   int
		outcome outcome
		saved   string
	}

	header := mediaHeaders(referer)
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
		// 同一序号已有任意扩展名产物（001.jpg / 001.png）即视为已下载。
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

// fetchDetail 调签名 detail API 取 aweme detail。
//
// 返回解析后的 Aweme 与原始 detail 字典（视频后端需要完整的播放地址
// 候选列表）。错误信息为可直接展示给用户的中文提示。
func (d *Downloader) fetchDetail(ctx context.Context, awemeID, cookieText string) (*Aweme, map[string]any, error) {
	jar, _, err := cookies.ParseMap(cookieText)
	if err != nil {
		return nil, nil, errors.New("解析 Cookie 失败")
	}
	uifid := douyinsign.PickUIFID(jar)
	if uifid == "" {
		return nil, nil, errors.New("缺少 UIFID（或 UIFID_TEMP），无法生成抖音签名，" + cookieRefreshHint)
	}

	now := d.now()
	signer := d.signer()
	params := detailParams(awemeID, jar, signer.RNG)
	query := encodeQuery(params)

	bogus, err := signer.GetValue(query, douyinsign.SignOptions{NowMS: now.UnixMilli()})
	if err != nil {
		return nil, nil, fmt.Errorf("生成 a_bogus 失败: %w", err)
	}

	// websign 的 preimage 与发送的字节必须一致：这里直接复用未二次编码的
	// 原始参数值，效果与 Python 版 unquote(urlencode) 回环一致。
	pairs := make([][2]string, 0, len(params)+3)
	pairs = append(pairs, params...)
	pairs = append(pairs, [2]string{"a_bogus", bogus})
	if fp := jar["s_v_web_id"]; fp != "" {
		pairs = append(pairs, [2]string{"verifyFp", fp}, [2]string{"fp", fp})
	}
	signedQuery, _, signedHeaders := douyinsign.Sign(pairs, uifid, now.Unix())

	header := http.Header{}
	header.Set("User-Agent", douyinUserAgent)
	header.Set("Accept", "application/json, text/plain, */*")
	header.Set("Referer", "https://www.douyin.com/")
	for name, value := range signedHeaders {
		header.Set(name, value)
	}

	resp, err := d.HTTP.Get(ctx, httpclient.Request{
		URL:    detailAPIBase + "?" + signedQuery,
		Header: header,
		Cookie: cookieText,
	})
	if err != nil {
		return nil, nil, errors.New("获取视频详情失败（网络异常）")
	}
	defer resp.Close()

	switch {
	case resp.StatusCode == http.StatusForbidden:
		return nil, nil, errors.New("抖音详情接口被风控拒绝（403，UIFID 缺失或失效）。" + cookieRefreshHint)
	case resp.StatusCode >= 400:
		return nil, nil, fmt.Errorf("获取抖音详情失败（HTTP %d）", resp.StatusCode)
	}

	body, err := resp.ReadAll(maxPageBytes)
	if err != nil {
		return nil, nil, errors.New("读取详情响应失败（网络异常）")
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, nil, errors.New("抖音 Cookie 已失效（详情接口返回空响应）。" + cookieRefreshHint)
	}

	var top map[string]any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&top); err != nil {
		return nil, nil, errors.New("抖音详情接口返回非 JSON 响应")
	}
	detail, ok := top["aweme_detail"].(map[string]any)
	if !ok || len(detail) == 0 {
		return nil, nil, fmt.Errorf("抖音详情接口返回异常（status_code=%s）", valueString(top["status_code"]))
	}
	return awemeFromDetail(detail), detail, nil
}

// detailParams 构造签名前的 query 参数：基础指纹段 + aweme_id + msToken。
func detailParams(awemeID string, jar map[string]string, rng douyinsign.RNG) [][2]string {
	params := make([][2]string, 0, len(apiBaseParams)+2)
	params = append(params, apiBaseParams...)
	params = append(params,
		[2]string{"aweme_id", awemeID},
		[2]string{"msToken", msToken(jar, rng)},
	)
	return params
}

// msToken 优先使用 Cookie 里的现值；缺失时伪造 126 位随机串 + "=="
// （detail 端点对伪造值不校验，对齐 Python 版策略）。
func msToken(jar map[string]string, rng douyinsign.RNG) string {
	if value := jar["msToken"]; value != "" {
		return value
	}
	var b strings.Builder
	for index := 0; index < 126; index++ {
		position := int(rng.Float64() * float64(len(msTokenAlphabet)))
		position %= len(msTokenAlphabet)
		if position < 0 {
			position = 0
		}
		b.WriteByte(msTokenAlphabet[position])
	}
	b.WriteString("==")
	return b.String()
}

// encodeQuery 按 urlencode 语义序列化参数（与 Python urlencode 一致）。
func encodeQuery(pairs [][2]string) string {
	parts := make([]string, len(pairs))
	for index, pair := range pairs {
		parts[index] = url.QueryEscape(pair[0]) + "=" + url.QueryEscape(pair[1])
	}
	return strings.Join(parts, "&")
}

// signer 构造 a_bogus 签名器；注入 RNG 时使用注入的随机源。
func (d *Downloader) signer() *douyinsign.ABogus {
	signer := douyinsign.NewABogus(douyinUserAgent)
	if d.RNG != nil {
		signer.RNG = d.RNG
	}
	return signer
}

// now 返回当前时间，未注入 Now 时使用 time.Now。
func (d *Downloader) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// pageHeaders 是笔记页请求头（UA 与指纹版本一致，Cookie 由调用方携带）。
func pageHeaders() http.Header {
	header := http.Header{}
	header.Set("User-Agent", douyinUserAgent)
	header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8")
	header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	header.Set("Referer", "https://www.douyin.com/")
	return header
}

// mediaHeaders 是媒体下载请求头，Referer 固定为来源页面。
func mediaHeaders(referer string) http.Header {
	header := http.Header{}
	header.Set("User-Agent", douyinUserAgent)
	header.Set("Referer", referer)
	return header
}

// ensureHTTPS 把协议相对（//）的媒体链接升级为 https；绝对 http URL
// 原样保留（本地测试与少数 CDN 节点依赖 http）。
func ensureHTTPS(rawURL string) string {
	if strings.HasPrefix(rawURL, "//") {
		return "https:" + rawURL
	}
	return rawURL
}

// resolveBasename 取目录基名：desc 前 30 个字符清洗后仍空则兜底 "douyin"。
func resolveBasename(desc string) string {
	desc = strings.TrimSpace(desc)
	if desc == "" {
		return "douyin"
	}
	runes := []rune(desc)
	if len(runes) > 30 {
		runes = runes[:30]
	}
	if cleaned := fsutil.SanitizeFilename(string(runes), "", 0); cleaned != "" {
		return cleaned
	}
	return "douyin"
}

// noteIDFromURL 提取 /note/<数字ID>，不匹配返回空串。
func noteIDFromURL(rawURL string) string {
	match := noteURLPattern.FindStringSubmatch(rawURL)
	if len(match) < 2 {
		return ""
	}
	return match[1]
}

// videoIDFromURL 提取 /video/<数字ID>，不匹配返回空串。
func videoIDFromURL(rawURL string) string {
	match := videoURLPattern.FindStringSubmatch(rawURL)
	if len(match) < 2 {
		return ""
	}
	return match[1]
}

// failed 生成统一的失败结果。
func failed(message string) result.Result {
	return result.Result{Status: result.Failed, Message: message}
}
