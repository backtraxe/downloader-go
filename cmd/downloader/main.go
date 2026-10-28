package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/backtraxe/downloader-go/internal/backend/douyin"
	"github.com/backtraxe/downloader-go/internal/backend/generic"
	"github.com/backtraxe/downloader-go/internal/backend/xhs"
	"github.com/backtraxe/downloader-go/internal/backend/ytdlp"
	"github.com/backtraxe/downloader-go/internal/dispatch"
	"github.com/backtraxe/downloader-go/internal/httpclient"
	"github.com/backtraxe/downloader-go/internal/result"
	"github.com/backtraxe/downloader-go/internal/urlutil"
)

// 版本信息，发布时通过 -ldflags "-X main.version=..." 注入。
var version, commit, date = "dev", "none", "unknown"

// dependencies 汇总命令依赖，测试可注入替身。
type dependencies struct {
	dispatch func(context.Context, string, bool) result.Result
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// run 使用默认依赖执行命令，返回进程退出码。
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	dataDir, _ := splitDataDir(args)
	return runWith(args, stdin, stdout, stderr, defaultDependencies(resolveDataDir(dataDir)))
}

// defaultDependencies 构造生产依赖：分发与短链展开使用 Chrome133 客户端，
// 通用网页后端使用与自身 UA 指纹一致的 Chrome120 客户端。
func defaultDependencies(dataDir string) dependencies {
	secure := httpclient.New(httpclient.Options{
		Profile:  httpclient.Chrome133,
		Timeouts: httpclient.DefaultTimeouts(),
	})
	genericClient := httpclient.New(httpclient.Options{
		Profile:  httpclient.Chrome120,
		Timeouts: httpclient.DefaultTimeouts(),
	})
	// 抖音后端的签名 UA 与 secure 客户端的 Chrome133 指纹保持一致。
	douyinDownloader := &douyin.Downloader{HTTP: secure, DataDir: dataDir}
	// 小红书与抖音共用同一 Chrome133 客户端（页面与媒体请求）。
	xhsDownloader := &xhs.Downloader{HTTP: secure, DataDir: dataDir}
	d := &dispatch.Dispatcher{
		HTTP:    secure,
		DataDir: dataDir,
		Handlers: dispatch.Handlers{
			XHS:         xhsDownloader.Download,
			DouyinNote:  douyinDownloader.DownloadNote,
			DouyinVideo: douyinDownloader.DownloadVideo,
			YTDLP:       ytdlp.New(dataDir).Download,
			Generic:     (&generic.Downloader{HTTP: genericClient, DataDir: dataDir}).Download,
		},
	}
	return dependencies{dispatch: d.Dispatch}
}

// runWith 解析全局 --data-dir 后按子命令分派。无子命令时进入交互式下载，
// 与 Python 版“粘贴链接循环下载”的用法一致。
func runWith(args []string, stdin io.Reader, stdout, stderr io.Writer, deps dependencies) int {
	dataDir, args := splitDataDir(args)
	// dataDir 已由 run 用于构造依赖；runWith 本身暂不需要，保留解析以固定优先级。
	_ = dataDir

	if len(args) == 0 {
		return runInteractive(nil, stdin, stdout, stderr, deps)
	}

	switch args[0] {
	case "version":
		fmt.Fprintf(stdout, "downloader %s (commit %s, built %s)\n", version, commit, date)
		return 0
	case "run":
		return runInteractive(args[1:], stdin, stdout, stderr, deps)
	default:
		fmt.Fprintf(stderr, "命令 %q 尚未接入\n", args[0])
		return 2
	}
}

// runInteractive 逐行读取 stdin，提取链接后交给 dispatch，输入 q 退出。
//
// 每行可以是纯 URL，也可以是一段分享文案；同一行的多个链接依次处理。
// 结果以 "<status>: <message>" 打印到 stdout。
func runInteractive(flags []string, stdin io.Reader, stdout, stderr io.Writer, deps dependencies) int {
	force := false
	for _, arg := range flags {
		switch arg {
		case "-f", "--force":
			force = true
		default:
			fmt.Fprintf(stderr, "未知参数 %q\n", arg)
			return 2
		}
	}
	if deps.dispatch == nil {
		fmt.Fprintln(stderr, "下载分发未配置")
		return 1
	}

	scanner := bufio.NewScanner(stdin)
	// 分享文案可能很长，放宽单行上限（默认 64KiB）。
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.EqualFold(line, "q") {
			break
		}
		if line == "" {
			continue
		}
		for _, link := range urlutil.ExtractURLs(line) {
			res := deps.dispatch(context.Background(), link, force)
			fmt.Fprintf(stdout, "%s: %s\n", res.Status, res.Message)
		}
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintf(stderr, "读取输入失败: %v\n", err)
		return 1
	}
	return 0
}

// splitDataDir 剥离全局 --data-dir（支持 --data-dir=DIR 与 --data-dir DIR），
// 返回其值与剩余参数。
func splitDataDir(args []string) (string, []string) {
	if len(args) == 0 {
		return "", args
	}
	if value, ok := strings.CutPrefix(args[0], "--data-dir="); ok {
		return value, args[1:]
	}
	if args[0] == "--data-dir" && len(args) > 1 {
		return args[1], args[2:]
	}
	return "", args
}

// resolveDataDir 按 flag > 环境变量 > 当前目录的顺序确定数据目录。
func resolveDataDir(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if home := os.Getenv("DOWNLOADER_HOME"); home != "" {
		return home
	}
	return "."
}
