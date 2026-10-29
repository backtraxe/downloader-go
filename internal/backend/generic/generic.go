// Package generic 实现通用网页媒体抓取后端：请求任意网页，按 DOM 顺序
// 提取图片/视频直链，按序号落盘并过滤站点装饰资源。
//
// 口径沿用 Python 版 core/1024_downloader.py：标题取 <title> 并去掉站点
// 后缀，标题中的“正文 来自 作者 - 站点”尾巴用于提取作者；媒体文件命名为
// 001、002……，下载阶段按 URL/Content-Type 校正扩展名；落盘后短边 <300px
// 或体积 <20KB 的图片判为装饰图删除，并写隐藏的 .<序号>.skip 标记避免
// 重跑时重复下载。所有请求都走调用方注入的 httpclient.Client。
package generic

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/PuerkitoBio/goquery"

	"github.com/backtraxe/downloader-go/internal/fsutil"
	"github.com/backtraxe/downloader-go/internal/httpclient"
	"github.com/backtraxe/downloader-go/internal/media"
	"github.com/backtraxe/downloader-go/internal/result"
	"github.com/backtraxe/downloader-go/internal/sites"
)

const (
	// totalConcurrency 是单页媒体的总并发下载上限。
	totalConcurrency = 5
	// hostConcurrency 是同一图床的并发上限。CDN 会间歇拒绝突发 TLS 握手，
	// 按 host 排队后跨 host 仍可并行。
	hostConcurrency = 2

	// 与 Python 版保持一致的装饰图阈值：尺寸可解析时短边 <300px，
	// 解析失败时体积 <20KB。
	minImageDimension = 300
	minImageBytes     = 20 * 1024

	// maxPageBytes 限制单个页面响应体，避免异常站点把内存打满。
	maxPageBytes = 16 << 20
)

// Downloader 是通用网页后端的配置与入口。
type Downloader struct {
	HTTP    *httpclient.Client
	DataDir string
}

// Download 请求网页、解析媒体候选并并发下载，返回统一的 result.Result。
//
// force=true 时绕过“已存在/已判定为装饰图”的幂等判断，由
// httpclient.DownloadFile 原子覆盖旧产物。
func (d *Downloader) Download(ctx context.Context, rawURL string, force bool) result.Result {
	if ctx == nil {
		ctx = context.Background()
	}
	if d == nil || d.HTTP == nil {
		return result.Result{Status: result.Failed, Message: "下载客户端未配置"}
	}

	site := sites.Detect(rawURL)
	resp, err := d.HTTP.Get(ctx, httpclient.Request{URL: rawURL})
	if err != nil {
		return result.Result{Status: result.Failed, Message: "获取页面失败: " + err.Error()}
	}
	defer resp.Close()
	if err := resp.RaiseForStatus(); err != nil {
		return result.Result{Status: result.Failed, Message: "获取页面失败: " + err.Error()}
	}
	body, err := resp.ReadAll(maxPageBytes)
	if err != nil {
		return result.Result{Status: result.Failed, Message: "读取页面失败: " + err.Error()}
	}
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return result.Result{Status: result.Failed, Message: "解析页面失败: " + err.Error()}
	}

	// 作者需要原始标题里的“- 站点”尾巴，因此单独取一次；ExtractTitle
	// 返回的已清理标题用于目录名，避免作者信息在路径里重复一层。
	rawTitle := strings.TrimSpace(doc.Find("title").First().Text())
	title := ExtractTitle(doc)
	author := ExtractAuthor(rawTitle, site)

	dirTitle := title
	if id := fsutil.ExtractURLNumericID(rawURL); id != "" {
		dirTitle = title + "_" + id
	}
	// 全站统一布局：<dataDir>/download/<site>[/<author>]/<title>。
	root := filepath.Join(d.DataDir, "download")
	dir := fsutil.BuildDownloadDir(root, site, dirTitle, author)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return result.Result{Status: result.Failed, Message: "创建目录失败: " + err.Error()}
	}

	// 装饰资源在编号前剔除：序号必须连续，不能先按 DOM 顺序编号再过滤。
	candidates := ExtractCandidates(doc, rawURL)
	kept := make([]Candidate, 0, len(candidates))
	filtered := 0
	for _, candidate := range candidates {
		if media.IsDecorativeName(candidateName(candidate.URL)) {
			filtered++
			continue
		}
		kept = append(kept, candidate)
	}
	candidates = kept
	if len(candidates) == 0 {
		return result.FromCounts(result.Counts{Filtered: filtered})
	}

	counts, files := d.downloadAll(ctx, candidates, rawURL, dir, force)
	counts.Filtered += filtered
	res := result.FromCounts(counts)
	res.Posts = []result.Post{{
		Site:   site,
		Author: author,
		Title:  title,
		Status: res.Status,
		Files:  files,
	}}
	return res
}

// outcome 是单个候选的下载结论。
type outcome int

const (
	outcomeDownloaded outcome = iota
	outcomeSkipped
	outcomeFiltered
	outcomeFailed
)

// downloadAll 并发下载全部候选。总并发 5，同一 host 2 路；结果按候选
// 原始顺序汇总，保证同一次运行内 Post.Files 的次序稳定。
func (d *Downloader) downloadAll(
	ctx context.Context,
	candidates []Candidate,
	pageURL, dir string,
	force bool,
) (result.Counts, []string) {
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		counts result.Counts
		files  = make([]string, len(candidates))
		kept   = make([]bool, len(candidates))

		total    = make(chan struct{}, totalConcurrency)
		hostSems sync.Map
	)

	// 不覆盖 User-Agent：httpclient.Client 会补上与 TLS 指纹同版本的
	// profile UA，后端口径覆盖会造成 UA/指纹错配。
	header := http.Header{}
	header.Set("Referer", pageURL)

	for index, candidate := range candidates {
		wg.Add(1)
		go func(index int, candidate Candidate) {
			defer wg.Done()

			// 先按 host 排队，再占用总并发名额：等待同一图床时不会占着
			// 全局名额，其他 host 仍可继续下载。
			host := ""
			if u, err := url.Parse(candidate.URL); err == nil {
				host = u.Host
			}
			hostSem := hostSemaphore(&hostSems, host)
			hostSem <- struct{}{}
			defer func() { <-hostSem }()

			total <- struct{}{}
			defer func() { <-total }()

			got, saved := d.downloadOne(ctx, candidate, index+1, dir, force, header)

			mu.Lock()
			defer mu.Unlock()
			switch got {
			case outcomeDownloaded:
				counts.Downloaded++
				files[index] = saved
				kept[index] = true
			case outcomeSkipped:
				counts.Skipped++
			case outcomeFiltered:
				counts.Filtered++
			case outcomeFailed:
				counts.Failed++
			}
		}(index, candidate)
	}
	wg.Wait()

	paths := make([]string, 0, len(files))
	for index, ok := range kept {
		if ok {
			paths = append(paths, files[index])
		}
	}
	return counts, paths
}

// downloadOne 处理单个候选：幂等判断、下载与落盘后装饰图判定。
func (d *Downloader) downloadOne(
	ctx context.Context,
	candidate Candidate,
	number int,
	dir string,
	force bool,
	header http.Header,
) (outcome, string) {
	target := filepath.Join(dir, fmt.Sprintf("%03d.jpg", number))
	marker := filepath.Join(dir, fmt.Sprintf(".%03d.skip", number))

	if !force {
		// 同一序号已有任意扩展名产物（001.jpg / 001.png）即视为已下载。
		if _, exists := fsutil.ExistingByStem(target); exists {
			return outcomeSkipped, ""
		}
		// 上一次判定为装饰图的标记：只有标记里的 URL 与当前候选一致才
		// 跳过；序号被新图复用时旧结论失效，删除标记后重新下载。
		if data, err := os.ReadFile(marker); err == nil {
			if strings.TrimSpace(string(data)) == candidate.URL {
				return outcomeSkipped, ""
			}
			_ = os.Remove(marker)
		}
	}

	saved, err := d.HTTP.DownloadFile(ctx, candidate.URL, target, httpclient.DownloadOptions{
		Header: header,
		Force:  force,
		Filter: func(contentType string) error {
			if media.IsHTMLContent(contentType) {
				return fmt.Errorf("响应为 HTML 页面（非媒体直链或被风控拦截）")
			}
			return nil
		},
	})
	if err != nil {
		return outcomeFailed, ""
	}

	// 视频不过滤：短视频也可能很小，尺寸判定只适用于图片。
	if isVideoExtension(filepath.Ext(saved)) {
		clearSkipMarker(marker)
		return outcomeDownloaded, saved
	}
	if media.IsDecorativeFile(saved, minImageDimension, minImageBytes) {
		if err := os.Remove(saved); err != nil {
			return outcomeFailed, ""
		}
		// 标记写入失败不影响本次结论；下次运行会重新下载并再判定。
		_ = os.WriteFile(marker, []byte(candidate.URL), 0o600)
		return outcomeFiltered, ""
	}
	// 序号被新图复用且新图通过判定时，旧的装饰图标记必须失效。
	clearSkipMarker(marker)
	return outcomeDownloaded, saved
}

// clearSkipMarker 删除跳过标记；标记不存在时静默忽略。
func clearSkipMarker(marker string) {
	_ = os.Remove(marker)
}

// hostSemaphore 返回 host 对应的并发信号量，首次访问时创建。
func hostSemaphore(sems *sync.Map, host string) chan struct{} {
	if value, ok := sems.Load(host); ok {
		return value.(chan struct{})
	}
	value, _ := sems.LoadOrStore(host, make(chan struct{}, hostConcurrency))
	return value.(chan struct{})
}
