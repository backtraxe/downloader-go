package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/backtraxe/downloader-go/internal/backend/douyin"
	"github.com/backtraxe/downloader-go/internal/backend/generic"
	"github.com/backtraxe/downloader-go/internal/backend/xhs"
	"github.com/backtraxe/downloader-go/internal/backend/ytdlp"
	"github.com/backtraxe/downloader-go/internal/config"
	"github.com/backtraxe/downloader-go/internal/dispatch"
	"github.com/backtraxe/downloader-go/internal/httpclient"
	"github.com/backtraxe/downloader-go/internal/logging"
	"github.com/backtraxe/downloader-go/internal/netdns"
	"github.com/backtraxe/downloader-go/internal/result"
	"github.com/backtraxe/downloader-go/internal/telegram"
	"github.com/backtraxe/downloader-go/internal/tgapi"
	"github.com/backtraxe/downloader-go/internal/urlutil"
)

// 版本信息，发布时通过 -ldflags "-X main.version=..." 注入。
var version, commit, date = "dev", "none", "unknown"

const (
	// telegramRequestTimeout 是文字类 Telegram 请求的上限（发消息、删除），
	// 避免半开连接让机器人永久阻塞。
	telegramRequestTimeout = 60 * time.Second
	// telegramMediaTimeout 是 HTTP 客户端的整体上限，按媒体上传设置：49MB
	// 在移动上行可能远超 60 秒。文字请求由各自的 60s context 收紧，长轮询
	// 仍保持 40s，因此放宽客户端超时不会拖长它们的等待。
	telegramMediaTimeout = 10 * time.Minute
)

// dependencies 汇总命令依赖，测试可注入替身。
type dependencies struct {
	dispatch func(context.Context, string, bool) result.Result
	bot      func(ctx context.Context, stdout, stderr io.Writer) int
	upload   func(ctx context.Context, args []string, stdout, stderr io.Writer) int
}

func main() {
	// Termux 没有 /etc/resolv.conf，纯 Go 解析器会退回 [::1]:53；在 Termux
	// 上换成直连公共 DNS 的解析器。其他平台是 no-op。
	netdns.InstallDefault()
	// 安装带请求作用域的默认 logger，让每条链接的 [rXXXX] 前缀真正出现在
	// 生产日志里；bot 常驻与 CLI 共用同一条 stderr 通道。
	slog.SetDefault(logging.New(os.Stderr, slog.LevelInfo))
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
	return dependencies{
		dispatch: d.Dispatch,
		bot: func(ctx context.Context, stdout, stderr io.Writer) int {
			return runBot(ctx, dataDir, d.Dispatch, stderr)
		},
		upload: func(ctx context.Context, args []string, stdout, stderr io.Writer) int {
			return runUpload(ctx, dataDir, args, stdout, stderr)
		},
	}
}

// runWith 解析全局 --data-dir 后按子命令分派。无子命令时进入交互式下载，
// 与 Python 版“粘贴链接循环下载”的用法一致。
func runWith(args []string, stdin io.Reader, stdout, stderr io.Writer, deps dependencies) int {
	dataDir, args := splitDataDir(args)
	// 与 run 使用同一口径解析数据目录（flag > 环境变量 > 当前目录），
	// 供 cookie 等需要直接读写数据目录的子命令使用。
	dataDir = resolveDataDir(dataDir)

	if len(args) == 0 {
		return runInteractive(nil, stdin, stdout, stderr, deps)
	}

	switch args[0] {
	case "version":
		fmt.Fprintf(stdout, "dl-go %s (commit %s, built %s)\n", version, commit, date)
		return 0
	case "run":
		return runInteractive(args[1:], stdin, stdout, stderr, deps)
	case "bot":
		if deps.bot == nil {
			fmt.Fprintln(stderr, "机器人依赖未配置")
			return 1
		}
		return deps.bot(context.Background(), stdout, stderr)
	case "upload":
		if deps.upload == nil {
			fmt.Fprintln(stderr, "上传依赖未配置")
			return 1
		}
		return deps.upload(context.Background(), args[1:], stdout, stderr)
	case "cookie":
		return runCookieCommand(args[1:], dataDir, stdin, stdout, stderr)
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

// loadTelegramConfig 从数据目录读取 telegram 段配置，返回配置与文件路径。
func loadTelegramConfig(dataDir string) (*config.Telegram, string, error) {
	path := filepath.Join(dataDir, "config.json")
	cfg, err := config.Load(config.LoadOptions{
		Path:       path,
		LegacyPath: filepath.Join(dataDir, "telegram.json"),
	})
	if err != nil {
		return nil, path, err
	}
	return &cfg.Telegram, path, nil
}

// printConfigGuidance 提示用户填写配置；不输出 token 等敏感值。
func printConfigGuidance(w io.Writer, path string, err error) {
	fmt.Fprintf(w, "请先在 %s 的 telegram 段填入 bot_token / chat_id / allowed_users（@BotFather 拿 token；用户 id 会在首次私聊被拒时回显）。\n", path)
	if err != nil && !errors.Is(err, config.ErrNotConfigured) {
		fmt.Fprintf(w, "读取配置失败：%v\n", err)
	}
}

// telegramConfigured 判断配置是否已填入真实值。模板占位值（token 以
// “在此”开头、chat_id 为 0 或空）视为未配置，避免用占位凭据发网络请求。
func telegramConfigured(cfg *config.Telegram) bool {
	if cfg == nil {
		return false
	}
	if cfg.BotToken == "" || strings.HasPrefix(cfg.BotToken, "在此") {
		return false
	}
	chatID := cfg.ChatID.String()
	return chatID != "" && chatID != "0"
}

// newTelegramAPI 构造带请求超时的 Bot API 客户端。
//
// 客户端超时按媒体上传放宽到 10 分钟；文字调用的 60s 上限由调用方
// context 保证，二者互不替代。
func newTelegramAPI(token string) *tgapi.Client {
	api := tgapi.New(token)
	api.HTTP = &http.Client{Timeout: telegramMediaTimeout}
	return api
}

// runBot 加载配置并启动长轮询机器人。
func runBot(ctx context.Context, dataDir string, dispatchFn func(context.Context, string, bool) result.Result, stderr io.Writer) int {
	cfg, path, err := loadTelegramConfig(dataDir)
	if err != nil {
		printConfigGuidance(stderr, path, err)
		return 1
	}
	if !telegramConfigured(cfg) {
		printConfigGuidance(stderr, path, errors.New("telegram 段仍是模板占位值"))
		return 1
	}
	if dispatchFn == nil {
		fmt.Fprintln(stderr, "下载分发未配置")
		return 1
	}

	// 启动行只包含非敏感信息：数据目录、白名单人数与过程消息 TTL。
	slog.Info("机器人启动",
		"data_dir", dataDir,
		"allowed_users", len(cfg.AllowedUsers),
		"ttl_seconds", cfg.TTL(),
	)

	api := newTelegramAPI(cfg.BotToken)
	bot := &telegram.Bot{
		API:      api,
		Config:   *cfg,
		DataDir:  dataDir,
		Dispatch: dispatchFn,
		Uploader: &telegram.Uploader{API: api},
	}
	if err := bot.Run(ctx); err != nil {
		fmt.Fprintf(stderr, "机器人运行失败：%v\n", err)
		return 1
	}
	return 0
}

// runUpload 把本地目录或文件上传到配置中的 chat_id。
func runUpload(ctx context.Context, dataDir string, args []string, stdout, stderr io.Writer) int {
	path, caption, err := parseUploadArgs(args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}

	cfg, cfgPath, err := loadTelegramConfig(dataDir)
	if err != nil {
		printConfigGuidance(stderr, cfgPath, err)
		return 1
	}
	if !telegramConfigured(cfg) {
		printConfigGuidance(stderr, cfgPath, errors.New("telegram 段仍是模板占位值"))
		return 1
	}

	u := &telegram.Uploader{API: newTelegramAPI(cfg.BotToken)}
	sent, skipped, err := telegram.UploadPath(ctx, u, cfg.ChatID.String(), path, caption)
	if err != nil {
		fmt.Fprintf(stderr, "上传失败：%v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "完成：%d 项已回传", sent)
	if len(skipped) > 0 {
		fmt.Fprintf(stdout, "，%d 项超限跳过", len(skipped))
	}
	fmt.Fprintln(stdout)
	for _, file := range skipped {
		fmt.Fprintf(stdout, "超 50MB 已跳过（文件在 %s）\n", file)
	}
	return 0
}

// parseUploadArgs 解析 upload 子命令参数：<path> [--caption TEXT]。
func parseUploadArgs(args []string) (path, caption string, err error) {
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--caption":
			if i+1 >= len(args) {
				return "", "", errors.New("--caption 缺少参数")
			}
			caption = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--caption="):
			caption = strings.TrimPrefix(args[i], "--caption=")
		case strings.HasPrefix(args[i], "-"):
			return "", "", fmt.Errorf("未知参数 %q", args[i])
		default:
			if path != "" {
				return "", "", errors.New("只支持一个路径")
			}
			path = args[i]
		}
	}
	if path == "" {
		return "", "", errors.New("用法：dl-go upload <path> [--caption TEXT]")
	}
	return path, caption, nil
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
