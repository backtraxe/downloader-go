// Package ytdlp 以子进程方式封装 Termux 管理的 yt-dlp，仅负责探测元数据与
// 取回文件路径，不内嵌 Python 或 yt-dlp 本体。
//
// 口径沿用 Python 版 core/downloader.py：先 -J 探测 uploader，再按
// download/<site>[/<author>]/%(title).200B.%(ext)s 下载；Cookie 文件存在且
// 非空时才传 --cookies。所有产物路径只从 yt-dlp 的 stdout 读取并校验存在。
package ytdlp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/backtraxe/downloader-go/internal/fsutil"
	"github.com/backtraxe/downloader-go/internal/result"
	"github.com/backtraxe/downloader-go/internal/sites"
)

// Runner 执行一次 yt-dlp 调用，分别返回 stdout 与 stderr。
//
// 抽成函数类型便于测试注入，生产实现见 New。err 为子进程退出错误。
type Runner func(ctx context.Context, args ...string) (stdout, stderr []byte, err error)

// Downloader 是 yt-dlp 后端的配置与执行入口。
type Downloader struct {
	Bin     string
	DataDir string
	Run     Runner
}

// New 返回使用 PATH 中 yt-dlp 的生产实例。
func New(dataDir string) *Downloader {
	d := &Downloader{Bin: "yt-dlp", DataDir: dataDir}
	d.Run = func(ctx context.Context, args ...string) ([]byte, []byte, error) {
		var stdout, stderr bytes.Buffer
		cmd := exec.CommandContext(ctx, d.Bin, args...)
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		return stdout.Bytes(), stderr.Bytes(), err
	}
	return d
}

// probeInfo 是 -J 输出中本后端需要的字段。
//
// Formats 用于区分普通视频与 Instagram 图文帖：图文帖没有视频格式，原图
// 只出现在 Thumbnails（图集则在每个 Entries 元素里）。
type probeInfo struct {
	Title      string            `json:"title"`
	Uploader   string            `json:"uploader"`
	Ext        string            `json:"ext"`
	Formats    []json.RawMessage `json:"formats"`
	Thumbnails []json.RawMessage `json:"thumbnails"`
	Entries    []probeInfo       `json:"entries"`
}

// hasVideoFormats 递归判断探测结果是否含可下载的视频格式。
func (p probeInfo) hasVideoFormats() bool {
	if len(p.Formats) > 0 {
		return true
	}
	for _, entry := range p.Entries {
		if entry.hasVideoFormats() {
			return true
		}
	}
	return false
}

// hasImages 递归判断探测结果是否含可下载的图片（缩略图即原图）。
func (p probeInfo) hasImages() bool {
	if len(p.Thumbnails) > 0 {
		return true
	}
	for _, entry := range p.Entries {
		if entry.hasImages() {
			return true
		}
	}
	return false
}

// mtimeGuard 吸收文件系统时间戳精度（部分文件系统只有秒级），避免刚写入
// 的文件因为 mtime 被截断而误判为“早于下载开始”。
const mtimeGuard = 2 * time.Second

// Download 先探测元数据拿作者，再下载并返回实际写入的文件路径。
//
// force=true 时追加 --force-overwrites 覆盖已存在文件。重复链接时 yt-dlp
// 可能不重写文件、只打印旧路径，因此记录下载开始时刻：全部输出文件都早于
// 该时刻即判定为此前已下载，返回 Skipped 且不填 Files。多次输出（如分片
// 合并）仍归入同一个 Post；播放列表的多 Post 语义不在本任务范围内。
func (d *Downloader) Download(ctx context.Context, rawURL string, force bool) result.Result {
	site := sites.Detect(rawURL)
	cookies := d.cookieArgs(site)

	probeArgs := []string{"-J", "--skip-download", "--no-warnings"}
	// Instagram 图文帖没有视频格式，提取器默认直接报“There is no video in
	// this post”。放行该错误才能拿到 thumbnails 元数据，供下面的图片分支
	// 使用；其他站点保持严格探测。
	if site == "instagram" {
		probeArgs = append(probeArgs, "--ignore-no-formats-error")
	}
	probeArgs = append(probeArgs, cookies...)
	probeArgs = append(probeArgs, rawURL)
	stdout, stderr, err := d.Run(ctx, probeArgs...)
	if err != nil {
		return failed(err, stderr)
	}

	var info probeInfo
	if err := json.Unmarshal(stdout, &info); err != nil {
		return result.Result{
			Status:  result.Failed,
			Message: fmt.Sprintf("解析 yt-dlp 探测结果失败: %v", err),
		}
	}

	// Instagram 图文帖（单图或图集）没有视频格式，改走缩略图通道下载原图。
	if site == "instagram" && !info.hasVideoFormats() && info.hasImages() {
		return d.downloadImages(ctx, rawURL, site, info, force)
	}

	// 作者为空时省略目录层，避免产生 untitled/ 之类占位目录。
	author := fsutil.SanitizeFilename(info.Uploader, "", 80)
	title := strings.TrimSpace(info.Title)
	if title == "" {
		title = "untitled"
	}

	parts := []string{d.DataDir, "download", site}
	if author != "" {
		parts = append(parts, author)
	}
	parts = append(parts, "%(title).200B.%(ext)s")
	outtmpl := filepath.Join(parts...)

	args := []string{
		"--no-warnings",
		"--newline",
		"-o", outtmpl,
		"--merge-output-format", "mp4",
		"--concurrent-fragment-downloads", "3",
		"--retries", "10",
		"--fragment-retries", "10",
		"--print", "after_move:filepath",
	}
	if force {
		args = append(args, "--force-overwrites")
	}
	args = append(args, cookies...)
	args = append(args, rawURL)

	// 下载开始时刻用于区分“本次新写入”与“此前已存在”。
	start := time.Now()
	stdout, stderr, err = d.Run(ctx, args...)
	if err != nil {
		return failed(err, stderr)
	}

	files := existingFiles(stdout)
	if len(files) == 0 {
		msg := tail(string(stderr), 500)
		if msg == "" {
			msg = "yt-dlp 未输出已下载文件路径"
		}
		return result.Result{Status: result.Failed, Message: "下载失败: " + msg}
	}

	return buildResult(site, author, title, files, start, force)
}

// downloadImages 用 yt-dlp 的缩略图通道下载 Instagram 图文帖。
//
// Instagram 的图片帖没有视频格式，yt-dlp 默认会以 “There is no video in
// this post” 失败；但探测结果里带 thumbnails（图集在每个 entry 里）。
// --ignore-no-formats-error 让流程继续，--skip-download + --write-thumbnail
// 把每张原图落盘，--print before_dl:thumbnails.-1.filepath 取回实际路径。
//
// 图集所有 entry 共用同一个标题，因此文件名追加 %(id)s，避免互相覆盖。
func (d *Downloader) downloadImages(ctx context.Context, rawURL, site string, info probeInfo, force bool) result.Result {
	author := fsutil.SanitizeFilename(info.Uploader, "", 80)
	title := strings.TrimSpace(info.Title)
	if title == "" {
		title = "untitled"
	}

	parts := []string{d.DataDir, "download", site}
	if author != "" {
		parts = append(parts, author)
	}
	parts = append(parts, "%(title).200B_%(id)s.%(ext)s")
	outtmpl := filepath.Join(parts...)

	args := []string{
		"--no-warnings",
		"--newline",
		"--ignore-no-formats-error",
		"--skip-download",
		"--write-thumbnail",
		"-o", outtmpl,
		"--print", "before_dl:thumbnails.-1.filepath",
	}
	if force {
		args = append(args, "--force-overwrites")
	}
	args = append(args, d.cookieArgs(site)...)
	args = append(args, rawURL)

	start := time.Now()
	stdout, stderr, err := d.Run(ctx, args...)
	if err != nil {
		return failed(err, stderr)
	}

	files := existingFiles(stdout)
	if len(files) == 0 {
		msg := tail(string(stderr), 500)
		if msg == "" {
			msg = "yt-dlp 未输出已下载图片路径"
		}
		return result.Result{Status: result.Failed, Message: "下载失败: " + msg}
	}

	return buildResult(site, author, title, files, start, force)
}

// buildResult 判定本次是否真的写入文件，并组装统一的 Result/Post。
//
// 全部产物都早于下载开始时刻时视为此前已下载（yt-dlp 会打印旧路径），
// 返回 Skipped 且不填 Files。
func buildResult(site, author, title string, files []string, start time.Time, force bool) result.Result {
	if !force && filesPredate(files, start) {
		return result.Result{
			Status:  result.Skipped,
			Message: "已下载过: " + title,
			Posts: []result.Post{{
				Site:   site,
				Author: author,
				Title:  title,
				Status: result.Skipped,
			}},
		}
	}

	return result.Result{
		Status:  result.Downloaded,
		Message: "已下载: " + title,
		Posts: []result.Post{{
			Site:   site,
			Author: author,
			Title:  title,
			Status: result.Downloaded,
			Files:  files,
		}},
	}
}

// cookieArgs 返回 Cookie 参数；文件不存在、是目录或为空时返回 nil。
//
// 只传路径，绝不读取或输出 Cookie 内容。
func (d *Downloader) cookieArgs(site string) []string {
	path := sites.CookiePath(d.DataDir, site)
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Size() == 0 {
		return nil
	}
	return []string{"--cookies", path}
}

// existingFiles 从 stdout 逐行提取真实存在的文件路径，相对路径按当前工作
// 目录解析。yt-dlp 的进度/提示行会被 Stat 自然过滤掉。
func existingFiles(stdout []byte) []string {
	var files []string
	for _, line := range strings.Split(string(stdout), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		path := line
		if !filepath.IsAbs(path) {
			abs, err := filepath.Abs(path)
			if err != nil {
				continue
			}
			path = abs
		}
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			continue
		}
		files = append(files, path)
	}
	return files
}

// filesPredate 报告所有文件是否都明显早于下载开始时刻。
//
// 任一文件无法 stat 或 mtime 不早于 cutoff 都返回 false：宁可重复上传，
// 也不把本次真正写入的文件误报为“已下载过”。
func filesPredate(files []string, start time.Time) bool {
	if len(files) == 0 {
		return false
	}
	cutoff := start.Add(-mtimeGuard)
	for _, path := range files {
		info, err := os.Stat(path)
		if err != nil {
			return false
		}
		if !info.ModTime().Before(cutoff) {
			return false
		}
	}
	return true
}

// failed 把子进程错误映射成可读提示，保留 stderr 尾部便于排查。
func failed(err error, stderr []byte) result.Result {
	text := strings.TrimSpace(string(stderr))
	combined := strings.ToLower(err.Error() + "\n" + text)

	switch {
	case errors.Is(err, exec.ErrNotFound) || strings.Contains(combined, "executable file not found"):
		return result.Result{
			Status:  result.Failed,
			Message: "未找到 yt-dlp，请在 Termux 中运行 pkg install yt-dlp 安装",
		}
	case strings.Contains(combined, "fresh cookies"):
		// yt-dlp 的抖音详情接口缺少签名，文案像 Cookie 过期，实际刷新无效。
		return result.Result{
			Status:  result.Failed,
			Message: "抖音视频详情接口存在签名拦截（Argus/a_bogus），刷新 Cookie 无效",
		}
	case strings.Contains(combined, "sign in"),
		strings.Contains(combined, "login"),
		strings.Contains(combined, "cookies"):
		return result.Result{
			Status:  result.Failed,
			Message: "该内容可能需要登录或 Cookie 失效，请检查 cookies/<站点>.txt",
		}
	}

	if text == "" {
		text = err.Error()
	}
	return result.Result{Status: result.Failed, Message: "下载失败: " + tail(text, 500)}
}

// tail 按 rune 截取末尾 n 个字符，避免切断 UTF-8 序列。
func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[len(runes)-n:])
}
