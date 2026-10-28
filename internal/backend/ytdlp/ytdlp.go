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
type probeInfo struct {
	Title    string `json:"title"`
	Uploader string `json:"uploader"`
	Ext      string `json:"ext"`
}

// Download 先探测元数据拿作者，再下载并返回实际写入的文件路径。
//
// force=true 时追加 --force-overwrites 覆盖已存在文件。多次输出（如分片
// 合并）仍归入同一个 Post；播放列表的多 Post 语义不在本任务范围内。
func (d *Downloader) Download(ctx context.Context, rawURL string, force bool) result.Result {
	site := sites.Detect(rawURL)
	cookies := d.cookieArgs(site)

	probeArgs := []string{"-J", "--skip-download", "--no-warnings"}
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
