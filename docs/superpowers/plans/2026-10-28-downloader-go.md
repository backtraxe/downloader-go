# downloader-go Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在 `github.com/backtraxe/downloader-go` 交付一个 Go 编写的、静态编译的下载器与 Telegram 回传机器人，覆盖小红书、抖音、通用网页与 yt-dlp 站点，并能在 Termux 上不依赖 pip 稳定运行。

**Architecture:** 单二进制 + 子命令（`run` / `bot` / `upload` / `cookie` / `version`）。业务代码全部在 `internal/`，HTTP 指纹用纯 Go 的 `utls`，yt-dlp 与 ffmpeg 作为外部命令调用。后端统一返回 `result.Result`，Telegram 直接消费 `Result.Posts` 上传，不再扫描目录做 diff。

**Tech Stack:** Go 1.27、`utls`、`golang.org/x/net/http2`、`goquery`、`golang.org/x/image/webp`、GoReleaser、GitHub Actions。

**Spec:** `docs/superpowers/specs/2026-10-28-go-rewrite-design.md`

## Global Constraints

- Go 版本：`go 1.27`；所有构建 `CGO_ENABLED=0`。
- 目标平台：`linux/arm64`（Termux 主目标）、`linux/amd64`、`darwin/arm64`、`darwin/amd64`、`windows/amd64`。
- 数据根目录解析：`--data-dir` > `DOWNLOADER_HOME` > 当前工作目录；`download/`、`cookies/`、`config.json` 都相对于它。
- 主流程不得依赖 Python；`yt-dlp` 与 `ffmpeg` 只通过 `exec.CommandContext` 调用，缺失时降级/提示。
- 输出布局：`download/<site>[/<author>]/<标题>_<ID>/` 或 `download/<site>[/<author>]/<标题>.<ext>`。
- Telegram 限制：文件 ≤50MB、照片 ≤10MB、相册每组 ≤10 项。
- Cookie、`config.json`、`download/` 不提交、不打印 Cookie 值。
- 许可证：整体 MIT；`internal/douyinsign/` 文件保留 Apache-2.0 头注，根目录放 `NOTICE`。
- 行为对齐基线：Python 仓库 commit `1a4f129`。

## Review Focus

- **yt-dlp 不存在或 PATH 错误**：必须返回 `failed` 并提示 `pkg install yt-dlp`，不能 panic 或返回空成功；测试在 Task 9。
- **重复链接/已存在文件**：必须跳过且不重新上传；测试在 Task 5、Task 8、Task 19。
- **Telegram 边界**：照片恰好 10MB、视频恰好 50MB 时走 document 或跳过，而不是让 Bot API 报错中断整条消息；测试在 Task 18。
- **失效的 xhslink 短链**：HTTP 200 落到首页且 `noteDetailMap` 为空时，必须提示“分享链接已失效”，不能报“页面结构变更”；测试在 Task 16。
- **抖音 Cookie 失效**：jsvm 挑战页或 detail API 200 空响应必须归类为 `failed` 并给出刷新 Cookie 的提示；测试在 Task 15。
- **`--data-dir` 含空格/相对路径**：所有路径必须一致地基于该目录解析；测试在 Task 1、Task 3。

---

### Task 0: uTLS 真机验证 spike（不进入正式仓库）

**Files:**
- Create (临时，勿提交): `/tmp/utls-spike/main.go`、`/tmp/utls-spike/go.mod`

**Interfaces:**
- Consumes: 无
- Produces: 一个 go/no-go 结论：utls 是否能通过小红书笔记页与抖音笔记页的风控。

- [ ] **Step 1: 写一个最小请求程序**

```go
// /tmp/utls-spike/main.go
package main

import (
    "context"
    "crypto/tls"
    "fmt"
    "io"
    "net"
    "net/http"
    "os"
    "time"

    "github.com/refraction-networking/utls"
    "golang.org/x/net/http2"
)

func main() {
    target, cookie := os.Args[1], os.Args[2]
    dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
        raw, err := (&net.Dialer{Timeout: 15 * time.Second}).DialContext(ctx, network, addr)
        if err != nil { return nil, err }
        host, _, _ := net.SplitHostPort(addr)
        u := utls.UClient(raw, &utls.Config{ServerName: host, NextProtos: []string{"h2", "http/1.1"}}, utls.HelloChrome_133)
        if err := u.HandshakeContext(ctx); err != nil { raw.Close(); return nil, err }
        return u, nil
    }
    tr := &http2.Transport{DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) { return dial(ctx, network, addr) }}
    req, _ := http.NewRequest("GET", target, nil)
    req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36")
    req.Header.Set("Cookie", cookie)
    resp, err := (&http.Client{Transport: tr}).Do(req)
    if err != nil { panic(err) }
    defer resp.Body.Close()
    body, _ := io.ReadAll(resp.Body)
    fmt.Printf("HTTP %d, proto=%s, bytes=%d\n", resp.StatusCode, resp.Proto, len(body))
}
```

- [ ] **Step 2: 在 Termux 上编译并运行**

```bash
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o /tmp/utls-spike/spike .
# 把 spike 与 cookies/xiaohongshu.txt、cookies/douyin.txt 拷到手机
./spike 'https://www.xiaohongshu.com/explore/<id>?xsec_token=...' "$(cat cookies/xiaohongshu.txt)"
./spike 'https://www.douyin.com/note/<id>' "$(cat cookies/douyin.txt)"
```

Expected: 小红书返回笔记页（状态 200、正文包含笔记正文而非 `/login`）；抖音返回笔记页而非 `_$jsvmprt` 挑战页。

- [ ] **Step 3: 记录结论**

在任务执行记录里写明：通过 → Task 7 只实现 utls；失败 → Task 7 同时实现系统 curl 兜底，并在 README 标注。spike 目录在任务完成后删除。

---

### Task 1: 仓库骨架与 `version` / `--data-dir`

**Files:**
- Create: `go.mod`、`cmd/downloader/main.go`、`cmd/downloader/main_test.go`
- Create: `.gitignore`、`LICENSE`、`NOTICE`、`README.md`
- Create: `docs/superpowers/specs/2026-10-28-go-rewrite-design.md`、`docs/superpowers/plans/2026-10-28-downloader-go.md`（从旧仓库复制）

**Interfaces:**
- Produces: `func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int`
- Produces: `func runWith(args []string, stdin io.Reader, stdout, stderr io.Writer, deps dependencies) int`
- Produces: `func resolveDataDir(flagValue string) string`
- Produces CLI 约定：`downloader [--data-dir DIR] version`

- [ ] **Step 1: 创建仓库与工具链**

```bash
gh repo create backtraxe/downloader-go --public --description "Go rewrite of downloader-bot: static CLI + Telegram bot for Termux"
mkdir -p /code/downloader-go && cd /code/downloader-go
git init -b main
git config user.name backtraxe
git config user.email backtraxe@gmail.com
git remote add origin git@github.com:backtraxe/downloader-go.git
mkdir -p docs/superpowers/specs docs/superpowers/plans
cp /code/downloader-bot/docs/superpowers/specs/2026-10-28-go-rewrite-design.md docs/superpowers/specs/
cp /code/downloader-bot/docs/superpowers/plans/2026-10-28-downloader-go.md docs/superpowers/plans/
curl -fsSL https://go.dev/dl/go1.27.3.linux-amd64.tar.gz -o /tmp/go.tgz
mkdir -p ~/opt && tar -C ~/opt -xzf /tmp/go.tgz
export PATH=$HOME/opt/go/bin:$PATH
go version
```

Expected: 输出 `go version go1.27.3 linux/amd64`。

- [ ] **Step 2: 写失败测试**

```go
// cmd/downloader/main_test.go
package main

import (
    "bytes"
    "strings"
    "testing"
)

func TestRunVersion(t *testing.T) {
    var out, errOut bytes.Buffer
    code := run([]string{"version"}, strings.NewReader(""), &out, &errOut)
    if code != 0 { t.Fatalf("exit=%d stderr=%s", code, errOut.String()) }
    if !strings.Contains(out.String(), "downloader dev") {
        t.Fatalf("unexpected version output: %q", out.String())
    }
}

func TestResolveDataDir(t *testing.T) {
    t.Setenv("DOWNLOADER_HOME", "/env/data")
    if got := resolveDataDir("/flag/data"); got != "/flag/data" { t.Fatalf("flag: %q", got) }
    if got := resolveDataDir(""); got != "/env/data" { t.Fatalf("env: %q", got) }
    t.Setenv("DOWNLOADER_HOME", "")
    if got := resolveDataDir(""); got != "." { t.Fatalf("cwd fallback: %q", got) }
}
```

- [ ] **Step 3: 运行测试确认失败**

Run: `go test ./cmd/downloader -run 'TestRunVersion|TestResolveDataDir' -v`
Expected: FAIL，`undefined: run` / `undefined: resolveDataDir`。

- [ ] **Step 4: 实现骨架**

`go.mod`：

```text
module github.com/backtraxe/downloader-go

go 1.27
```

`cmd/downloader/main.go` 定义 `var version, commit, date = "dev", "none", "unknown"`；Task 1 的 `dependencies` 先定义为空结构体 `type dependencies struct{}`（`result` 包要到 Task 2 才存在），`runWith` 用 `strings.TrimPrefix` 手动剥离全局 `--data-dir`，再按 `version` 分派；其他命令先打印“尚未接入”并返回 2。Task 11 再把 `dispatch func(context.Context, string, bool) result.Result` 加进 `dependencies`。`version` 输出：

```text
downloader dev (commit none, built unknown)
```

- [ ] **Step 5: 运行测试确认通过**

Run: `go test ./cmd/downloader -run 'TestRunVersion|TestResolveDataDir' -v`
Expected: PASS。

- [ ] **Step 6: 提交**

```bash
git add go.mod cmd/downloader .gitignore LICENSE NOTICE README.md docs
git commit -m "chore: bootstrap downloader-go with version command"
```

---

### Task 2: 结果契约 `internal/result`

**Files:**
- Create: `internal/result/result.go`、`internal/result/result_test.go`

**Interfaces:**
- Produces: `type Status string`，常量 `Downloaded`/`Partial`/`Skipped`/`Failed`/`NoMedia`
- Produces: `type Post struct { Site, Author, Title string; Status Status; Error string; Files []string }`
- Produces: `type Result struct { Status Status; Message string; Posts []Post }`
- Produces: `func (r Result) OK() bool`
- Produces: `type Counts struct { Downloaded, Skipped, Failed, Filtered int }`
- Produces: `func FromCounts(c Counts) Result`

- [ ] **Step 1: 写失败测试**

```go
// internal/result/result_test.go
package result

import "testing"

func TestFromCounts(t *testing.T) {
    cases := []struct {
        name string
        in   Counts
        want Status
    }{
        {"downloaded", Counts{Downloaded: 3}, Downloaded},
        {"partial", Counts{Downloaded: 2, Failed: 1}, Partial},
        {"failed", Counts{Failed: 2}, Failed},
        {"skipped", Counts{Skipped: 1}, Skipped},
        {"filtered", Counts{Filtered: 2}, NoMedia},
        {"empty", Counts{}, NoMedia},
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            if got := FromCounts(tc.in); got.Status != tc.want {
                t.Fatalf("status=%s want %s", got.Status, tc.want)
            }
        })
    }
}

func TestOK(t *testing.T) {
    for _, s := range []Status{Downloaded, Partial, Skipped} {
        if !(Result{Status: s}).OK() { t.Fatalf("%s should be ok", s) }
    }
    for _, s := range []Status{Failed, NoMedia} {
        if (Result{Status: s}).OK() { t.Fatalf("%s should not be ok", s) }
    }
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/result -v`
Expected: FAIL，`undefined: Counts`。

- [ ] **Step 3: 实现**

`FromCounts` 判定顺序：`Downloaded > Failed > Skipped > Filtered`；`Downloaded` 且有 `Failed` 时降为 `Partial`。消息包含数量，例如 `新增 2 项，失败 1 项`。

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/result -v`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/result
git commit -m "feat: add download result contract"
```

---

### Task 3: 配置与请求日志 `internal/config`、`internal/logging`

**Files:**
- Create: `internal/config/config.go`、`internal/config/config_test.go`
- Create: `internal/logging/logging.go`、`internal/logging/logging_test.go`

**Interfaces:**
- Produces: `type ChatID struct`，支持 JSON 数字与字符串；`func (ChatID) String() string`
- Produces: `type Telegram struct { BotToken string; ChatID ChatID; AllowedUsers []int64; ProcessMessageTTLSeconds int }`
- Produces: `func (t Telegram) TTL() int`（缺省 300，显式 0 返回 0）
- Produces: `type Config struct { Telegram Telegram }`
- Produces: `type LoadOptions struct { Path, LegacyPath string }`
- Produces: `var ErrNotConfigured = errors.New("config not found")`
- Produces: `func Load(opts LoadOptions) (*Config, error)`
- Produces: `func WriteTemplate(path string) error`
- Produces: `func New(w io.Writer, level slog.Level) *slog.Logger`
- Produces: `func WithRequest(ctx context.Context) (context.Context, string)`
- Produces: `func RequestID(ctx context.Context) string`
- Produces: `func NewID() string`

- [ ] **Step 1: 写配置测试**

```go
// internal/config/config_test.go
package config

import (
    "encoding/json"
    "errors"
    "os"
    "path/filepath"
    "testing"
)

func TestLoadWritesTemplateWhenMissing(t *testing.T) {
    dir := t.TempDir()
    path := filepath.Join(dir, "config.json")
    _, err := Load(LoadOptions{Path: path, LegacyPath: filepath.Join(dir, "telegram.json")})
    if !errors.Is(err, ErrNotConfigured) { t.Fatalf("err=%v", err) }
    if _, statErr := os.Stat(path); statErr != nil { t.Fatalf("template missing: %v", statErr) }
}

func TestLoadLegacyTelegram(t *testing.T) {
    dir := t.TempDir()
    legacy := filepath.Join(dir, "telegram.json")
    os.WriteFile(legacy, []byte(`{"bot_token":"t","chat_id":42,"allowed_users":[7]}`), 0o600)
    cfg, err := Load(LoadOptions{Path: filepath.Join(dir, "none.json"), LegacyPath: legacy})
    if err != nil { t.Fatal(err) }
    if cfg.Telegram.BotToken != "t" || cfg.Telegram.ChatID.String() != "42" { t.Fatalf("%+v", cfg) }
}

func TestChatIDBothForms(t *testing.T) {
    for _, raw := range []string{`123`, `"@channel"`} {
        var c ChatID
        if err := json.Unmarshal([]byte(raw), &c); err != nil { t.Fatalf("%s: %v", raw, err) }
        if c.String() == "" { t.Fatalf("empty for %s", raw) }
    }
}

func TestTTLDefaultAndZero(t *testing.T) {
    if got := (Telegram{}).TTL(); got != 300 { t.Fatalf("default=%d", got) }
    if got := (Telegram{ProcessMessageTTLSeconds: 0}).TTL(); got != 0 { t.Fatalf("zero=%d", got) }
}
```

- [ ] **Step 2: 写日志测试**

```go
// internal/logging/logging_test.go
package logging

import (
    "bytes"
    "context"
    "log/slog"
    "regexp"
    "testing"
)

func TestRequestIDFormatAndPrefix(t *testing.T) {
    var buf bytes.Buffer
    logger := New(&buf, slog.LevelInfo)
    ctx, id := WithRequest(context.Background())
    if !regexp.MustCompile(`^r[a-z0-9]{4}$`).MatchString(id) { t.Fatalf("id=%q", id) }
    logger.InfoContext(ctx, "hello")
    if !bytes.Contains(buf.Bytes(), []byte("["+id+"] hello")) { t.Fatalf("log=%q", buf.String()) }
}

func TestNestedScopeReusesID(t *testing.T) {
    ctx, first := WithRequest(context.Background())
    ctx2, second := WithRequest(ctx)
    if first != second { t.Fatalf("%s != %s", first, second) }
    if RequestID(ctx2) != first { t.Fatalf("lost id") }
}
```

- [ ] **Step 3: 运行测试确认失败**

Run: `go test ./internal/config ./internal/logging -v`
Expected: FAIL，包内符号未定义。

- [ ] **Step 4: 实现**

`Load` 顺序：`config.json` → `telegram.json`（内容视为 `telegram` 段）→ 写模板并返回 `ErrNotConfigured`。`New` 返回带 handler 的 `*slog.Logger`，handler 在 `Handle` 里把 `[id] ` 前缀拼到 `record.Message`；`WithRequest` 已有 ID 时复用。

- [ ] **Step 5: 运行测试确认通过**

Run: `go test ./internal/config ./internal/logging -v`
Expected: PASS。

- [ ] **Step 6: 提交**

```bash
git add internal/config internal/logging
git commit -m "feat: add config loading and request-scoped logging"
```

---

### Task 4: 站点识别与 URL 工具 `internal/sites`、`internal/urlutil`

**Files:**
- Create: `internal/sites/sites.go`、`internal/sites/sites_test.go`
- Create: `internal/urlutil/urlutil.go`、`internal/urlutil/urlutil_test.go`

**Interfaces:**
- Produces: `func Detect(rawURL string) string`
- Produces: `func CookiePath(dataDir, site string) string`
- Produces: `var ErrNoCookie = errors.New("cookie missing or empty")`
- Produces: `func LoadCookie(dataDir, site string) (string, error)`
- Produces: `func ExtractURLs(text string) []string`
- Produces: `func IsShortLink(rawURL string) bool`
- Produces: `func NormalizeURL(rawURL, pageURL string) string`

- [ ] **Step 1: 写站点识别测试**

```go
// internal/sites/sites_test.go
package sites

import "testing"

func TestDetect(t *testing.T) {
    cases := map[string]string{
        "https://www.xiaohongshu.com/explore/1": "xiaohongshu",
        "https://xhslink.cn/a":                  "xiaohongshu",
        "https://www.rednote.com/explore/1":     "xiaohongshu",
        "https://b23.tv/x":                      "bilibili",
        "https://v.douyin.com/x":                "douyin",
        "https://youtu.be/x":                    "youtube",
        "https://instagr.am/x":                  "instagram",
        "https://t.co/x":                        "twitter",
        "https://x.com/a":                       "twitter",
        "https://netflix.com/a":                 "netflix.com",
        "https://next.com/a":                    "next.com",
        "https://example.com/a":                 "example.com",
        "http://localhost:8080/a":               "localhost_8080",
    }
    for raw, want := range cases {
        if got := Detect(raw); got != want { t.Fatalf("%s: %s want %s", raw, got, want) }
    }
}
```

- [ ] **Step 2: 写 URL 工具测试**

```go
// internal/urlutil/urlutil_test.go
package urlutil

import (
    "reflect"
    "testing"
)

func TestExtractURLsTrimsCJK(t *testing.T) {
    got := ExtractURLs("看这个 https://a.com/x，还有 https://a.com/x 和 https://b.com/y。")
    want := []string{"https://a.com/x", "https://b.com/y"}
    if !reflect.DeepEqual(got, want) { t.Fatalf("%v want %v", got, want) }
}

func TestExtractURLsUnicodeWhitespace(t *testing.T) {
    got := ExtractURLs("https://a.com/x https://b.com/y　hello")
    want := []string{"https://a.com/x", "https://b.com/y"}
    if !reflect.DeepEqual(got, want) { t.Fatalf("%v want %v", got, want) }
}

func TestNormalizeURL(t *testing.T) {
    if got := NormalizeURL("//cdn.x/a.jpg", "https://x.com/p"); got != "https://cdn.x/a.jpg" { t.Fatal(got) }
    if got := NormalizeURL("/a.jpg", "https://x.com/p/q"); got != "https://x.com/a.jpg" { t.Fatal(got) }
}

func TestIsShortLink(t *testing.T) {
    for _, raw := range []string{"https://b23.tv/x", "https://v.douyin.com/x", "https://xhslink.com/a"} {
        if !IsShortLink(raw) { t.Fatalf("%s", raw) }
    }
    if IsShortLink("https://www.douyin.com/note/1") { t.Fatal("long url") }
}
```

- [ ] **Step 3: 运行测试确认失败**

Run: `go test ./internal/sites ./internal/urlutil -v`
Expected: FAIL。

- [ ] **Step 4: 实现**

`Detect` 用 host 精确匹配或 `.` 后缀匹配（修正 Python 版的子串误匹配：`netflix.com` 不得命中 `x.com`，`next.com` 不得命中 `t.co`）；未知域名取最后两段，端口替换为 `_`。`ExtractURLs` 的分隔类必须覆盖 Python 的 Unicode `\s`（RE2 的 `\s` 只有 ASCII，需加 `\p{Z}`、`\x{0085}`、`\v`），去重保序，剔除尾部 CJK/全角标点/引号括号（不含 `[`）。`LoadCookie` 读取 `cookies/<site>.txt`，缺失或空白返回 `ErrNoCookie`。

- [ ] **Step 5: 运行测试确认通过**

Run: `go test ./internal/sites ./internal/urlutil -v`
Expected: PASS。

- [ ] **Step 6: 提交**

```bash
git add internal/sites internal/urlutil
git commit -m "feat: add site routing and URL helpers"
```

---

### Task 5: 文件系统与媒体工具 `internal/fsutil`、`internal/media`

**Files:**
- Create: `internal/fsutil/fsutil.go`、`internal/fsutil/fsutil_test.go`
- Create: `internal/media/media.go`、`internal/media/media_test.go`
- Create: `internal/media/testdata/tiny.png`（测试内生成亦可）

**Interfaces:**
- Produces: `func SanitizeFilename(name, defaultName string, maxLen int) string`
- Produces: `func BuildDownloadDir(dataDir, site, title, author string) string`
- Produces: `func UniquePath(path string) (string, error)`
- Produces: `func ExistingByStem(path string) (string, bool)`
- Produces: `func IsPartialDownload(name string) bool`
- Produces: `func ExtractURLNumericID(rawURL string) string`
- Produces: `func GuessExtension(rawURL, contentType, defaultExt string) string`
- Produces: `func DecodeDimensions(r io.Reader) (width, height int, ok bool)`
- Produces: `func IsHTMLContent(contentType string) bool`
- Produces: `func IsDecorativeName(name string) bool`
- Produces: `func IsDecorativeFile(path string, minDim int, minBytes int64) bool`

- [ ] **Step 1: 写 fsutil 测试**

```go
// internal/fsutil/fsutil_test.go
package fsutil

import (
    "os"
    "path/filepath"
    "testing"
)

func TestSanitizeFilename(t *testing.T) {
    got := SanitizeFilename("a/b\\c:d*e?f\"g<h>i|j\x00中文", "untitled", 80)
    if got != "abcdefghij中文" { t.Fatalf("%q", got) }
    if SanitizeFilename("   ", "untitled", 80) != "untitled" { t.Fatal("blank") }
    if SanitizeFilename("..", "untitled", 80) != "untitled" { t.Fatal("dot-dot") }
    if SanitizeFilename(".", "untitled", 80) != "untitled" { t.Fatal("dot") }
}

func TestBuildDownloadDir(t *testing.T) {
    got := BuildDownloadDir("data", "douyin", "标题", "作者")
    want := filepath.Join("data", "douyin", "作者", "标题")
    if got != want { t.Fatalf("%q want %q", got, want) }
    got = BuildDownloadDir("data", "douyin", "标题", "")
    want = filepath.Join("data", "douyin", "标题")
    if got != want { t.Fatalf("%q want %q", got, want) }
}

func TestUniquePathAndExistingByStem(t *testing.T) {
    dir := t.TempDir()
    p := filepath.Join(dir, "001.jpg")
    first, err := UniquePath(p)
    if err != nil || first != p { t.Fatalf("%q %v", first, err) }
    second, _ := UniquePath(p)
    if second != filepath.Join(dir, "001_1.jpg") { t.Fatalf("%q", second) }
    if _, ok := ExistingByStem(filepath.Join(dir, "001.png")); !ok { t.Fatal("stem match") }
    os.WriteFile(filepath.Join(dir, "002.part"), nil, 0o600)
    if _, ok := ExistingByStem(filepath.Join(dir, "002.jpg")); ok { t.Fatal(".part counted") }
    os.WriteFile(filepath.Join(dir, "[合集].jpg"), []byte("x"), 0o600)
    if _, ok := ExistingByStem(filepath.Join(dir, "[合集].png")); !ok { t.Fatal("bracket stem") }
    if _, ok := ExistingByStem(filepath.Join(dir, "[unmatched.png")); ok { t.Fatal("bad pattern should not match") }
}
```

- [ ] **Step 2: 写 media 测试**

```go
// internal/media/media_test.go
package media

import (
    "bytes"
    "image"
    "image/png"
    "testing"
)

func TestGuessExtension(t *testing.T) {
    if got := GuessExtension("https://x/a.webp?x=1", "text/html", ".jpg"); got != ".webp" { t.Fatal(got) }
    if got := GuessExtension("https://x/a", "image/heic", ".jpg"); got != ".heic" { t.Fatal(got) }
    if got := GuessExtension("https://x/a", "text/html", ".jpg"); got != ".jpg" { t.Fatal(got) }
}

func TestDecodeDimensions(t *testing.T) {
    var buf bytes.Buffer
    if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 10, 20))); err != nil { t.Fatal(err) }
    w, h, ok := DecodeDimensions(bytes.NewReader(buf.Bytes()))
    if !ok || w != 10 || h != 20 { t.Fatalf("%d %d %v", w, h, ok) }
}

func TestDecorativeNameWordBoundary(t *testing.T) {
    if !IsDecorativeName("site-logo.png") { t.Fatal("logo") }
    if IsDecorativeName("iconic-photo.jpg") { t.Fatal("iconic false positive") }
}
```

- [ ] **Step 3: 运行测试确认失败**

Run: `go test ./internal/fsutil ./internal/media -v`
Expected: FAIL。

- [ ] **Step 4: 实现**

先 `go get golang.org/x/image`。`ExistingByStem` 用 `os.ReadDir(filepath.Dir(stem))` + `strings.TrimSuffix(name, filepath.Ext(name))` 与 `filepath.Base(stem)` 比较，忽略 `.part`/`.ytdl`/目录；不要用 `filepath.Glob`（`[`/`]` 会被当成字符类）。`SanitizeFilename` 清理后若结果为 `.` 或 `..`，返回 `defaultName`。`DecodeDimensions` 注册 `image/jpeg`、`image/png`、`image/gif` 与 `golang.org/x/image/webp`，用 `image.DecodeConfig`。`IsDecorativeName` 用词边界正则，避免 `iconic` 命中 `icon`。

- [ ] **Step 5: 运行测试确认通过**

Run: `go test ./internal/fsutil ./internal/media -v`
Expected: PASS。

- [ ] **Step 6: 提交**

```bash
git add internal/fsutil internal/media
git commit -m "feat: add filesystem and media helpers"
```

---

### Task 6: Cookie 格式与体检 `internal/cookies`

**Files:**
- Create: `internal/cookies/cookies.go`、`internal/cookies/cookies_test.go`

**Interfaces:**
- Produces: `type Format int`，常量 `Header`/`Netscape`/`JSONCookies`
- Produces: `func Detect(text string) Format`
- Produces: `func ParseMap(text string) (map[string]string, Format, error)`
- Produces: `func ToHeader(text string) string`
- Produces: `func ToNetscape(text, domain string) string`
- Produces: `func Import(dataDir, site string, r io.Reader) (string, error)`
- Produces: `type Issue struct { Key, Message string; Missing bool }`
- Produces: `func Check(text, site string) []Issue`

- [ ] **Step 1: 写测试**

```go
// internal/cookies/cookies_test.go
package cookies

import (
    "os"
    "strings"
    "testing"
)

func TestDetectAndParse(t *testing.T) {
    header := "a1=abc; web_session=def"
    if Detect(header) != Header { t.Fatal("header") }
    m, _, err := ParseMap(header)
    if err != nil || m["a1"] != "abc" { t.Fatalf("%v %v", m, err) }

    netscape := "# Netscape HTTP Cookie File\n.douyin.com\tTRUE\t/\tTRUE\t4070908800\tUIFID\tu1\n"
    m, _, err = ParseMap(netscape)
    if err != nil || m["UIFID"] != "u1" { t.Fatalf("%v %v", m, err) }

    if ToHeader(netscape) != "UIFID=u1" { t.Fatal(ToHeader(netscape)) }
    if !strings.Contains(ToNetscape(header, ".x.com"), "a1\tabc") { t.Fatal(ToNetscape(header, ".x.com")) }
}

func TestCheckDouyinGate(t *testing.T) {
    if len(Check("ttwid=x; UIFID=y", "douyin")) != 0 { t.Fatalf("complete cookie reported: %+v", Check("ttwid=x; UIFID=y", "douyin")) }
    if len(Check("ttwid=x", "douyin")) != 1 { t.Fatalf("missing UIFID not reported") }
    if len(Check("UIFID=y", "douyin")) != 1 { t.Fatalf("missing ttwid/sessionid not reported") }
    if len(Check("UIFID=; ttwid=", "douyin")) == 0 { t.Fatal("empty values must count as gaps") }
    if len(Check("a1=short; webId=x", "xiaohongshu")) == 0 { t.Fatal("short a1 must be reported") }
}

func TestJSONArrays(t *testing.T) {
    for _, raw := range []string{
        `[{"name":"a1","value":"abc","domain":".xiaohongshu.com"}]`,
        `{"cookies":[{"name":"a1","value":"abc","domain":".xiaohongshu.com"}]}`,
    } {
        m, f, err := ParseMap(raw)
        if err != nil || f != JSONCookies || m["a1"] != "abc" { t.Fatalf("%s: %v %v %v", raw, m, f, err) }
    }
}

func TestImportPerSiteFormat(t *testing.T) {
    dir := t.TempDir()
    p, err := Import(dir, "bilibili", strings.NewReader("Cookie: SESSDATA=abc"))
    if err != nil { t.Fatal(err) }
    data, _ := os.ReadFile(p)
    if !strings.HasPrefix(string(data), "# Netscape HTTP Cookie File") { t.Fatalf("%s", data) }

    p, err = Import(dir, "xiaohongshu", strings.NewReader(
        `[{"name":"a1","value":"abc","domain":".xiaohongshu.com"},{"name":"third","value":"x","domain":".evil.com"}]`))
    if err != nil { t.Fatal(err) }
    data, _ = os.ReadFile(p)
    if !strings.Contains(string(data), "a1=abc") || strings.Contains(string(data), "third=") { t.Fatalf("%s", data) }
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/cookies -v`
Expected: FAIL。

- [ ] **Step 3: 实现**

`Detect` 先尝试 JSON：顶层对象、`{"cookies":[...]}` 或顶层数组都算 `JSONCookies`（Cookie-Editor 默认导出是数组）；再按 Tab ≥7 字段判 Netscape，否则 Header。`ParseMap` 返回键值（Netscape 取第 6/7 字段；JSON 数组取每个元素的 name/value，空值保留为空串）。`Check` 只检查键名（不打印值），但空值视为缺失：抖音 `UIFID`/`UIFID_TEMP` 与 `ttwid`/`sessionid` 都需非空；小红书 `a1` 非空且长度 ≥30，`webId`/`web_session` 至少一个非空。`Import` 按站点输出所需格式：`douyin`/`bilibili`/`youtube`/`instagram`/`twitter` 写 Netscape（域分别为 `.douyin.com`/`.bilibili.com`/`.youtube.com`/`.instagram.com`/`.x.com`），`xiaohongshu` 写请求头并只保留 `domain` 含 `xiaohongshu.com` 的 JSON 条目；未知站点保留 Header。写入 0600，先 `MkdirAll`，不打印任何值。

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/cookies -v`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/cookies
git commit -m "feat: add cookie parsing, import and checks"
```

---

### Task 7: HTTP 核心（utls + HTTP/2）`internal/httpclient`

**Files:**
- Create: `internal/httpclient/client.go`、`internal/httpclient/transport.go`、`internal/httpclient/client_test.go`

**Interfaces:**
- Produces: `type Profile struct { Name string; Hello utls.ClientHelloID; UserAgent string }`
- Produces: `var Chrome133, Chrome120 Profile`（`Chrome133` 用 `utls.HelloChrome_133` + UA Chrome/133；utls v1.8.2 没有 155 预设）
- Produces: `type Timeouts struct { Dial, TLS, Header, Idle time.Duration }`、`func DefaultTimeouts() Timeouts`
- Produces: `type Options struct { Profile Profile; Timeouts Timeouts; Proxy string; InsecureSkipVerify bool }`
- Produces: `type Request struct { URL string; Header http.Header; Cookie string }`
- Produces: `type Response struct { StatusCode int; Header http.Header; Body io.ReadCloser; URL *url.URL }`
- Produces: `func (r *Response) ReadAll(limit int64) ([]byte, error)`、`JSON(v any) error`、`Close() error`、`RaiseForStatus() error`
- Produces: `type Client struct`、`func New(opts Options) *Client`、`func (c *Client) Get(ctx context.Context, req Request) (*Response, error)`

- [ ] **Step 1: 写测试**

```go
// internal/httpclient/client_test.go
package httpclient

import (
    "context"
    "net/http"
    "net/http/httptest"
    "net/url"
    "strings"
    "testing"
)

func TestGetOverHTTP2WithUTLS(t *testing.T) {
    srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.ProtoMajor != 2 { t.Errorf("proto=%s", r.Proto) }
        if r.Header.Get("X-Test") != "1" { t.Errorf("header missing") }
        w.Header().Set("Content-Type", "application/json")
        w.Write([]byte(`{"ok":true}`))
    }))
    srv.EnableHTTP2 = true
    srv.StartTLS()
    defer srv.Close()

    c := New(Options{Profile: Chrome133, Timeouts: DefaultTimeouts(), InsecureSkipVerify: true})
    h := http.Header{}
    h.Set("X-Test", "1")
    resp, err := c.Get(context.Background(), Request{URL: srv.URL, Header: h})
    if err != nil { t.Fatal(err) }
    defer resp.Close()
    if resp.StatusCode != 200 { t.Fatalf("status=%d", resp.StatusCode) }
    var out map[string]bool
    if err := resp.JSON(&out); err != nil || !out["ok"] { t.Fatalf("%v %v", out, err) }
}

func TestGetFollowsRedirectAndKeepsCookies(t *testing.T) {
    var gotCookie string
    mux := http.NewServeMux()
    mux.HandleFunc("/a", func(w http.ResponseWriter, r *http.Request) {
        http.SetCookie(w, &http.Cookie{Name: "sid", Value: "v"})
        http.Redirect(w, r, "/b", http.StatusFound)
    })
    mux.HandleFunc("/b", func(w http.ResponseWriter, r *http.Request) {
        gotCookie = r.Header.Get("Cookie")
        w.Write([]byte("ok"))
    })
    srv := httptest.NewServer(mux)
    defer srv.Close()
    c := New(Options{Profile: Chrome120, Timeouts: DefaultTimeouts()})
    resp, err := c.Get(context.Background(), Request{URL: srv.URL + "/a"})
    if err != nil { t.Fatal(err) }
    resp.Close()
    if !strings.Contains(gotCookie, "sid=v") { t.Fatalf("cookie=%q", gotCookie) }
    u, _ := url.Parse(srv.URL + "/b")
    if resp.URL.Path != u.Path { t.Fatalf("final=%s", resp.URL) }
}

func TestProfilesAlignUAWithHello(t *testing.T) {
    for _, p := range []Profile{Chrome133, Chrome120} {
        if !strings.Contains(p.UserAgent, "/"+p.Hello.Version+".") {
            t.Fatalf("%s: UA %q does not match Hello %s", p.Name, p.UserAgent, p.Hello.Version)
        }
    }
}

func TestCrossSchemeRedirects(t *testing.T) {
    tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Write([]byte("tls"))
    }))
    defer tlsSrv.Close()
    plainSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path == "/to-https" { http.Redirect(w, r, tlsSrv.URL, http.StatusFound); return }
        w.Write([]byte("plain"))
    }))
    defer plainSrv.Close()
    tlsToPlain := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        http.Redirect(w, r, plainSrv.URL, http.StatusFound)
    }))
    defer tlsToPlain.Close()

    c := New(Options{Profile: Chrome120, Timeouts: DefaultTimeouts(), InsecureSkipVerify: true})
    for _, raw := range []string{plainSrv.URL + "/to-https", tlsToPlain.URL} {
        resp, err := c.Get(context.Background(), Request{URL: raw})
        if err != nil { t.Fatalf("%s: %v", raw, err) }
        resp.Close()
        if resp.StatusCode != 200 { t.Fatalf("%s: status=%d", raw, resp.StatusCode) }
    }
}

func TestFallsBackToHTTP1WhenALPNIsNotH2(t *testing.T) {
    srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.ProtoMajor != 1 { t.Errorf("proto=%s", r.Proto) }
        w.Write([]byte("h1"))
    }))
    srv.EnableHTTP2 = false
    srv.StartTLS()
    defer srv.Close()
    c := New(Options{Profile: Chrome120, Timeouts: DefaultTimeouts(), InsecureSkipVerify: true})
    resp, err := c.Get(context.Background(), Request{URL: srv.URL})
    if err != nil { t.Fatal(err) }
    resp.Close()
    if resp.StatusCode != 200 { t.Fatalf("status=%d", resp.StatusCode) }
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/httpclient -v`
Expected: FAIL。

- [ ] **Step 3: 实现 transport**

先 `go get github.com/refraction-networking/utls golang.org/x/net`。`newHTTPClient(profile, timeouts, insecure)`：

```go
dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
    raw, err := (&net.Dialer{Timeout: t.Dial}).DialContext(ctx, network, addr)
    if err != nil { return nil, err }
    host, _, _ := net.SplitHostPort(addr)
    u := utls.UClient(raw, &utls.Config{ServerName: host, NextProtos: []string{"h2", "http/1.1"}, InsecureSkipVerify: insecure}, p.Hello)
    if err := u.HandshakeContext(ctx); err != nil { raw.Close(); return nil, err }
    return u, nil
}
```

用 `http2.Transport{DialTLSContext: ...}`，设置 `ReadIdleTimeout`/`PingTimeout`；`http.Client` 带 `cookiejar.New(nil)`，重定向上限 10。实现要点：

- 只构建一个 `http.Client`，其 `Transport` 是自定义 `RoundTripper`：按 `req.URL.Scheme` 逐跳选择 `http` → 标准 transport、`https` → utls transport。这样跨协议重定向（http→https、https→http）不会丢指纹或把 h2 帧发给 h1 服务端。
- utls 握手后检查 `ConnectionState().NegotiatedProtocol == "h2"`；不是 h2 时用 HTTP/1.1 transport（utls + `NextProtos: ["http/1.1"]`）重试一次（仅 GET/HEAD 无 body 的请求需要支持）。
- `Timeouts.Header` 在 `Get` 里用 `context.WithCancel` + `time.AfterFunc` 实现；成功拿到响应头后停止定时器，并在响应 body `Close` 时 `cancel`，避免泄漏。
- `Options.Proxy` 只保证 HTTP/1.1 + 标准库 TLS（Go 的 `DialTLSContext` 对代理 HTTPS 不生效）；在 `Options.Proxy` 的文档注释里明确写出这个限制，不要声称代理路径仍使用 utls。
- `ForceHTTP1` 可保留，但默认走 h2 + 自动回退。

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/httpclient -v`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/httpclient
git commit -m "feat: add utls HTTP client with HTTP/2 support"
```

---

### Task 8: 流式下载、重试与跳过 `internal/httpclient`

**Files:**
- Modify: `internal/httpclient/client.go`
- Create: `internal/httpclient/download.go`、`internal/httpclient/download_test.go`

**Interfaces:**
- Produces: `type DownloadOptions struct { Header http.Header; Force bool; MaxRetries int; MinDelay, MaxDelay time.Duration; Filter func(contentType string) error; OnRetry func(attempt int, err error); Sleep func(time.Duration) }`
- Produces: `func (c *Client) DownloadFile(ctx context.Context, rawURL, filepath string, opts DownloadOptions) (string, error)`
- Produces: `func IsTransient(err error) bool`、`func IsTransientStatus(code int) bool`

- [ ] **Step 1: 写测试**

```go
// internal/httpclient/download_test.go
package httpclient

import (
    "context"
    "net/http"
    "net/http/httptest"
    "os"
    "path/filepath"
    "sync/atomic"
    "testing"
    "time"
)

func TestDownloadSkipsExisting(t *testing.T) {
    var hits int32
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        atomic.AddInt32(&hits, 1)
        w.Write([]byte("DATA"))
    }))
    defer srv.Close()
    p := filepath.Join(t.TempDir(), "001.jpg")
    os.WriteFile(p, []byte("OLD"), 0o600)
    c := New(Options{Profile: Chrome120, Timeouts: DefaultTimeouts()})
    got, err := c.DownloadFile(context.Background(), srv.URL, p, DownloadOptions{})
    if err != nil || got != p { t.Fatalf("%s %v", got, err) }
    if atomic.LoadInt32(&hits) != 0 { t.Fatal("downloaded existing file") }
}

func TestDownloadRetriesTransient(t *testing.T) {
    var hits int32
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if atomic.AddInt32(&hits, 1) == 1 { http.Error(w, "boom", 503); return }
        w.Write([]byte("DATA"))
    }))
    defer srv.Close()
    p := filepath.Join(t.TempDir(), "001.jpg")
    c := New(Options{Profile: Chrome120, Timeouts: DefaultTimeouts()})
    _, err := c.DownloadFile(context.Background(), srv.URL, p, DownloadOptions{MaxRetries: 3, Sleep: func(time.Duration) {}})
    if err != nil { t.Fatal(err) }
    if atomic.LoadInt32(&hits) != 2 { t.Fatalf("hits=%d", hits) }
    data, _ := os.ReadFile(p)
    if string(data) != "DATA" { t.Fatalf("%q", data) }
}

func TestDownloadForceUsesPartThenRename(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("NEW")) }))
    defer srv.Close()
    p := filepath.Join(t.TempDir(), "001.jpg")
    os.WriteFile(p, []byte("OLD"), 0o600)
    c := New(Options{Profile: Chrome120, Timeouts: DefaultTimeouts()})
    if _, err := c.DownloadFile(context.Background(), srv.URL, p, DownloadOptions{Force: true}); err != nil { t.Fatal(err) }
    data, _ := os.ReadFile(p)
    if string(data) != "NEW" { t.Fatalf("%q", data) }
    if _, err := os.Stat(p + ".part"); !os.IsNotExist(err) { t.Fatal("part left") }
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/httpclient -run TestDownload -v`
Expected: FAIL。

- [ ] **Step 3: 实现**

`DownloadFile` 流程：`!Force` 时先 `fsutil.ExistingByStem` 命中则直接返回；请求成功后按 `Content-Type`/URL 修正扩展名；`Force` 写 `path+".part"` 后 `os.Rename`，否则 `fsutil.UniquePath`；失败删除部分文件。`Filter` 在写盘前调用（用于 HTML 拦截）。重试仅对 `IsTransient`/`IsTransientStatus(429,5xx)`；默认 3 次，退避 `min(maxDelay, minDelay*2^attempt)` 加抖动。

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/httpclient -v`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/httpclient
git commit -m "feat: add streaming download with retry and skip semantics"
```

---

### Task 9: yt-dlp 后端 `internal/backend/ytdlp`

**Files:**
- Create: `internal/backend/ytdlp/ytdlp.go`、`internal/backend/ytdlp/ytdlp_test.go`

**Interfaces:**
- Produces: `type Runner func(ctx context.Context, args ...string) (stdout, stderr []byte, err error)`
- Produces: `type Downloader struct { Bin, DataDir string; Run Runner }`
- Produces: `func New(dataDir string) *Downloader`
- Produces: `func (d *Downloader) Download(ctx context.Context, rawURL string, force bool) result.Result`
- Consumes: `result.Result`、`sites.Detect`、`sites.CookiePath`、`fsutil.SanitizeFilename`

- [ ] **Step 1: 写测试**

```go
// internal/backend/ytdlp/ytdlp_test.go
package ytdlp

import (
    "context"
    "errors"
    "os"
    "path/filepath"
    "strings"
    "testing"
)

func TestDownloadProbesThenDownloads(t *testing.T) {
    dir := t.TempDir()
    var calls [][]string
    run := func(ctx context.Context, args ...string) ([]byte, []byte, error) {
        calls = append(calls, args)
        if contains(args, "-J") {
            return []byte(`{"title":"标题","uploader":"作者","ext":"mp4"}`), nil, nil
        }
        p := filepath.Join(dir, "download", "youtube", "作者", "标题.mp4")
        os.MkdirAll(filepath.Dir(p), 0o755)
        os.WriteFile(p, []byte("V"), 0o600)
        return []byte(p + "\n"), nil, nil
    }
    d := &Downloader{DataDir: dir, Bin: "yt-dlp", Run: run}
    res := d.Download(context.Background(), "https://youtu.be/x", false)
    if res.Status != "downloaded" || len(res.Posts) != 1 || res.Posts[0].Author != "作者" {
        t.Fatalf("%+v", res)
    }
    if len(calls) != 2 { t.Fatalf("calls=%v", calls) }
    if !contains(calls[1], "--print") { t.Fatalf("missing print: %v", calls[1]) }
}

func TestMissingBinaryFailsWithHint(t *testing.T) {
    d := &Downloader{DataDir: t.TempDir(), Bin: "yt-dlp", Run: func(context.Context, ...string) ([]byte, []byte, error) {
        return nil, nil, errors.New("exec: \"yt-dlp\": executable file not found in $PATH")
    }}
    res := d.Download(context.Background(), "https://youtu.be/x", false)
    if res.Status != "failed" || !strings.Contains(res.Message, "yt-dlp") { t.Fatalf("%+v", res) }
}

func contains(args []string, want string) bool {
    for _, a := range args { if a == want { return true } }
    return false
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/backend/ytdlp -v`
Expected: FAIL。

- [ ] **Step 3: 实现**

探测参数：`-J --skip-download --no-warnings`；有非空 Cookie 文件时加 `--cookies <path>`。下载参数：`--no-warnings --newline -o <outtmpl> --merge-output-format mp4 --concurrent-fragment-downloads 3 --retries 10 --fragment-retries 10 --print after_move:filepath`；`force` 加 `--force-overwrites`。outtmpl 为 `download/<site>[/<author>]/%(title).200B.%(ext)s`。stdout 按行取最后一条存在的文件路径；无输出则 `failed`。错误信息含 `Fresh cookies` 时提示签名拦截，含 `Sign in`/`login` 时提示 Cookie。

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/backend/ytdlp -v`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/backend/ytdlp
git commit -m "feat: add yt-dlp backend"
```

---

### Task 10: 通用网页后端 `internal/backend/generic`

**Files:**
- Create: `internal/backend/generic/generic.go`、`internal/backend/generic/extract.go`
- Create: `internal/backend/generic/generic_test.go`、`internal/backend/generic/testdata/page.html`

**Interfaces:**
- Produces: `type Downloader struct { HTTP *httpclient.Client; DataDir string }`
- Produces: `func (d *Downloader) Download(ctx context.Context, rawURL string, force bool) result.Result`
- Produces: `type Candidate struct { URL, Kind string }`
- Produces: `func ExtractCandidates(doc *goquery.Document, pageURL string) []Candidate`
- Produces: `func ExtractTitle(doc *goquery.Document) string`
- Produces: `func ExtractAuthor(title, site string) string`

- [ ] **Step 1: 写解析测试**

```go
// internal/backend/generic/generic_test.go
package generic

import (
    "strings"
    "testing"

    "github.com/PuerkitoBio/goquery"
)

func TestExtractCandidates(t *testing.T) {
    html := `<html><head><title>正文 来自 作者甲 - 示例站</title></head><body>
      <img src="/a.jpg"><img data-src="//cdn.x/b.png"><img data-original="https://cdn.x/logo.png">
      <video src="/v.mp4"></video><a href="https://cdn.x/c.webp">x</a>
      <div style="background-image:url('/d.jpg')"></div></body></html>`
    doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
    if err != nil { t.Fatal(err) }
    got := ExtractCandidates(doc, "https://site/p/1")
    if len(got) != 6 { t.Fatalf("%+v", got) }
    if got[1].URL != "https://cdn.x/b.png" { t.Fatalf("%+v", got) }
}

func TestExtractAuthor(t *testing.T) {
    if got := ExtractAuthor("正文 来自 作者甲 - 示例站", "example.com"); got != "作者甲" { t.Fatal(got) }
    if got := ExtractAuthor("普通标题", "example.com"); got != "" { t.Fatal(got) }
}
```

- [ ] **Step 2: 写下载测试**

```go
func TestDownloadFiltersDecorativeAndWritesNumbered(t *testing.T) {
    // httptest 提供 /page.html、/a.jpg（10x10 PNG）、/logo.png（10x10 PNG）
    // 断言只写入 001.png，且目录名包含标题；logo 被文件名过滤。
}
```

（测试代码按上面注释实现：用 `image.NewRGBA` + `png.Encode` 生成响应体；`New(Options{...})` 不需要 TLS。）

- [ ] **Step 3: 运行测试确认失败**

Run: `go test ./internal/backend/generic -v`
Expected: FAIL。

- [ ] **Step 4: 实现**

先 `go get github.com/PuerkitoBio/goquery`。扫描 `img/source/video` 的 `src`/`data-src`/`data-original`/`ess-data`/`srcset`（取第一个 URL）、`a[href]` 的媒体后缀、行内 `background-image`。去重保序；`data:` 丢弃。下载前 `media.IsDecorativeName` 过滤；落盘后 `media.IsDecorativeFile`（短边 <300px 或 <20KB）删除并写 `.skip`。总并发 5，同 host 用 `sync.Map` + 带缓冲 channel 限 2。标题取 `<title>` 并清理站点后缀；作者按 `正文 来自 X - 站点` 提取。目录用 `fsutil.BuildDownloadDir`。

- [ ] **Step 5: 运行测试确认通过**

Run: `go test ./internal/backend/generic -v`
Expected: PASS。

- [ ] **Step 6: 提交**

```bash
git add internal/backend/generic
git commit -m "feat: add generic web scraper backend"
```

---

### Task 11: 分发与 CLI `run` `internal/dispatch`、`cmd/downloader`

**Files:**
- Create: `internal/dispatch/dispatch.go`、`internal/dispatch/dispatch_test.go`
- Create: `internal/urlutil/shortlink.go`、`internal/urlutil/shortlink_test.go`
- Modify: `cmd/downloader/main.go`、`cmd/downloader/main_test.go`

**Interfaces:**
- Produces: `type Handlers struct { XHS, DouyinNote, DouyinVideo func(ctx context.Context, url, cookie string, force bool) result.Result; YTDLP, Generic func(ctx context.Context, url string, force bool) result.Result }`
- Produces: `type Dispatcher struct { HTTP *httpclient.Client; DataDir string; Handlers Handlers }`
- Produces: `func (d *Dispatcher) Dispatch(ctx context.Context, rawURL string, force bool) result.Result`
- Produces: `func ResolveShortLink(ctx context.Context, c *httpclient.Client, rawURL string) string`
- Produces: `func ResolveShortLinkWithRetry(ctx context.Context, c *httpclient.Client, rawURL string, attempts int, sleep func(time.Duration)) string`
- Produces: `run` 支持 `run [-f]` 与 stdin `q`。

- [ ] **Step 1: 写分发测试**

```go
// internal/dispatch/dispatch_test.go
package dispatch

import (
    "context"
    "testing"

    "github.com/backtraxe/downloader-go/internal/result"
)

func TestRoutesAndCookieGate(t *testing.T) {
    calls := []string{}
    h := Handlers{
        XHS: func(context.Context, string, string, bool) result.Result { calls = append(calls, "xhs"); return result.Result{Status: result.Downloaded} },
        YTDLP: func(context.Context, string, bool) result.Result { calls = append(calls, "ytdlp"); return result.Result{Status: result.Downloaded} },
        Generic: func(context.Context, string, bool) result.Result { calls = append(calls, "generic"); return result.Result{Status: result.Downloaded} },
    }
    d := &Dispatcher{DataDir: t.TempDir(), Handlers: h}
    if got := d.Dispatch(context.Background(), "https://www.xiaohongshu.com/explore/1?xsec_token=x", false); got.Status != result.Failed {
        t.Fatalf("missing cookie should fail: %+v", got)
    }
    if got := d.Dispatch(context.Background(), "https://youtu.be/x", false); got.Status != result.Downloaded { t.Fatalf("%+v", got) }
    if len(calls) != 1 || calls[0] != "ytdlp" { t.Fatalf("%v", calls) }
}
```

- [ ] **Step 2: 写 CLI 测试**

```go
// cmd/downloader/main_test.go（追加）
func TestRunCLIHandlesQAndURLs(t *testing.T) {
    var out bytes.Buffer
    seen := []string{}
    deps := dependencies{dispatch: func(_ context.Context, u string, _ bool) result.Result {
        seen = append(seen, u)
        return result.Result{Status: result.Downloaded, Message: "ok"}
    }}
    code := runWith(nil, strings.NewReader("https://a.com/x\nq\n"), &out, &out, deps)
    if code != 0 || len(seen) != 1 || seen[0] != "https://a.com/x" { t.Fatalf("code=%d seen=%v", code, seen) }
}
```

- [ ] **Step 3: 运行测试确认失败**

Run: `go test ./internal/dispatch ./cmd/downloader -v`
Expected: FAIL。

- [ ] **Step 4: 实现**

`Dispatch` 顺序：`sites.Detect` → xiaohongshu（读 Cookie，缺失 `failed`；用 `cookies.ToHeader` 转成请求头）→ douyin（短链展开后判 note/video，读 Cookie 并转请求头）→ `bilibili/youtube/instagram/twitter` 走 YTDLP → 其他 Generic。`ResolveShortLink` 只对 `urlutil.IsShortLink` 发 GET，取 `resp.URL.String()`；失败原样返回。`runWith` 读取行，`q` 退出，`extractURLs` 后逐条 dispatch；`-f` 传 force。

Task 11 把 `dispatch` 字段加进 `dependencies`，并在 `main.go` 里只接 YTDLP/Generic；`Handlers.XHS`/`DouyinNote`/`DouyinVideo` 保持 nil，`Dispatch` 遇到 nil 返回 `failed`（“后端尚未接入”），等 Task 15/16 再接。

- [ ] **Step 5: 运行测试确认通过**

Run: `go test ./internal/dispatch ./cmd/downloader -v`
Expected: PASS。

- [ ] **Step 6: 提交**

```bash
git add internal/dispatch internal/urlutil cmd/downloader
git commit -m "feat: add dispatcher and interactive CLI"
```

---

### Task 12: SM3 移植 `internal/douyinsign`

**Files:**
- Create: `internal/douyinsign/sm3.go`、`internal/douyinsign/sm3_test.go`
- Create: `internal/douyinsign/LICENSE.apache`（上游 Apache-2.0 文本）

**Interfaces:**
- Produces: `func SM3Hash(msg []byte) [32]byte`
- Produces: `func SM3HexDigest(msg []byte) string`
- Produces: `func SM3ToArray(msg []byte) []int`

- [ ] **Step 1: 写测试**

```go
package douyinsign

import "testing"

func TestSM3ABC(t *testing.T) {
    const want = "66c7f0f462eeedd9d1f2d46bdc10e4e24167c4875cf2f7a2297da02b8f4ba8e0"
    if got := SM3HexDigest([]byte("abc")); got != want { t.Fatalf("%s", got) }
    arr := SM3ToArray([]byte("abc"))
    if len(arr) != 32 || arr[0] != 0x66 || arr[31] != 0xE0 { t.Fatalf("%v", arr) }
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/douyinsign -run TestSM3 -v`
Expected: FAIL。

- [ ] **Step 3: 实现**

按 `core/douyin_sign/douyin_sm3.py` 移植，保持文件头 Apache-2.0 声明与上游链接。

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/douyinsign -run TestSM3 -v`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/douyinsign
git commit -m "feat: port douyin SM3 implementation"
```

---

### Task 13: Web 签名 `internal/douyinsign`

**Files:**
- Create: `internal/douyinsign/websign.go`、`internal/douyinsign/websign_test.go`

**Interfaces:**
- Produces: `const UIFIDParam = "uifid"`、`SignatureParam = "x-secsdk-web-signature"`、`ExpireHeader = "x-secsdk-web-expire"`、`Salt`
- Produces: `func PickUIFID(cookies map[string]string) string`
- Produces: `func EncodePairs(pairs [][2]string) string`
- Produces: `func Sign(pairs [][2]string, uifid string, timestamp int64) (query, sig string, headers map[string]string)`

- [ ] **Step 1: 写测试**

```go
package douyinsign

import (
    "crypto/md5"
    "encoding/hex"
    "strings"
    "testing"
)

func TestEncodePairs(t *testing.T) {
    if got := EncodePairs([][2]string{{"a", "1 2"}}); got != "a=1%202" { t.Fatal(got) }
    if got := EncodePairs([][2]string{{"a", "*-._x"}}); got != "a=*-._x" { t.Fatal(got) }
}

func TestSignFormula(t *testing.T) {
    pairs := [][2]string{{"aweme_id", "1"}, {"a_bogus", "xyz+/="}}
    query, sig, headers := Sign(pairs, "uid_123", 1791914678)
    body := query[:strings.Index(query, "&"+SignatureParam)]
    sum := md5.Sum([]byte("uid_123_1791914678_" + Salt + "_" + body))
    if want := hex.EncodeToString(sum[:]); sig != want { t.Fatalf("%s want %s", sig, want) }
    if headers[UIFIDParam] != "uid_123" { t.Fatalf("%v", headers) }
}

func TestPickUIFID(t *testing.T) {
    if got := PickUIFID(map[string]string{"UIFID_TEMP": "t"}); got != "t" { t.Fatal(got) }
    if got := PickUIFID(map[string]string{"foo": "bar"}); got != "" { t.Fatal(got) }
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/douyinsign -run 'TestSign|TestEncodePairs|TestPickUIFID' -v`
Expected: FAIL。

- [ ] **Step 3: 实现**

按 `core/douyin_sign/douyin_websign.py`：`url.QueryEscape` 会把空格编成 `+`，必须自己按 `*-._` 安全集编码；`Sign` 不重复追加已存在的 `uifid`，总是追加 `timestamp`。

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/douyinsign -v`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/douyinsign
git commit -m "feat: port douyin web signature"
```

---

### Task 14: a_bogus 移植 `internal/douyinsign`

**Files:**
- Create: `internal/douyinsign/abogus.go`、`internal/douyinsign/abogus_test.go`
- Create: `internal/douyinsign/testdata/py_random_42.json`
- Create: `internal/douyinsign/rng.go`（`RNG` 接口与随机源）

**Interfaces:**
- Produces: `type RNG interface { Float64() float64 }`
- Produces: `const AID = 6383`、`PageID = 6241`、`DefaultBrowserInfo`
- Produces: `type ABogus struct { UserAgent, BrowserInfo string; PageID, AID int; RNG RNG }`
- Produces: `func NewABogus(userAgent string) *ABogus`
- Produces: `type SignOptions struct { Body, ContentType string; NowMS int64 }`
- Produces: `func (a *ABogus) GetValue(query string, opts SignOptions) (string, error)`

- [ ] **Step 1: 生成 Python 随机序列 fixture**

```bash
python -I - <<'PY' > internal/douyinsign/testdata/py_random_42.json
import json, random
r = random.Random(42)
print(json.dumps([r.random() for _ in range(20000)]))
PY
```

- [ ] **Step 2: 写黄金测试**

```go
package douyinsign

import (
    "encoding/json"
    "os"
    "testing"
)

const goldenABogus = "QJMcvtyjEoRcPVFG8Kn3et3lhy9ANPSymtT/WuFPHxYfL10GSRPGTPGvGxol4qI0HWBsie3HZdMAbdncpUU0pqHkomkvSPhWvU5CV80LhqwRGMJQLrbTezXFuwMn054qe55UiARI8Urq6VxAkrQD/B59SKLFQbSBMpxbk2YcT9sg1MyAE1cHPBtdNhGbUn54"

type fixedRNG struct{ vals []float64; i int }

func (f *fixedRNG) Float64() float64 { v := f.vals[f.i]; f.i++; return v }

func TestABogusGolden(t *testing.T) {
    raw, err := os.ReadFile("testdata/py_random_42.json")
    if err != nil { t.Fatal(err) }
    var vals []float64
    if err := json.Unmarshal(raw, &vals); err != nil { t.Fatal(err) }
    a := &ABogus{UserAgent: "test-ua", BrowserInfo: DefaultBrowserInfo, PageID: PageID, AID: AID, RNG: &fixedRNG{vals: vals}}
    got, err := a.GetValue("aweme_id=1&aid=6383", SignOptions{NowMS: 1791914678000})
    if err != nil || got != goldenABogus { t.Fatalf("err=%v got=%s", err, got) }
}

func TestABogusRejectsPreEpochClock(t *testing.T) {
    a := NewABogus("ua")
    if _, err := a.GetValue("k=1", SignOptions{NowMS: 0}); err == nil { t.Fatal("expected error") }
}
```

- [ ] **Step 3: 运行测试确认失败**

Run: `go test ./internal/douyinsign -run TestABogus -v`
Expected: FAIL。

- [ ] **Step 4: 实现**

逐段移植 `core/douyin_sign/douyin_abogus.py`：常量表、`digest_of`、`user_agent_digest`、`jsBytes`、`leBytes`、`maskPair`、`expandNoise`、RC4、base64（alphabet `s4`）、字段表与校验位、`GetValue`。`NewABogus` 使用 `math/rand/v2` 的 `rand.Float64` 作为默认 `RNG`；测试注入 `fixedRNG`。`ContentType` 含 `multipart/form-data` 时 body 置空。文件头保留 Apache-2.0 声明。

- [ ] **Step 5: 运行测试确认通过**

Run: `go test ./internal/douyinsign -v`
Expected: PASS，黄金值逐字节一致。

- [ ] **Step 6: 提交**

```bash
git add internal/douyinsign
git commit -m "feat: port douyin a_bogus signing"
```

---

### Task 15: 抖音后端 `internal/backend/douyin`

**Files:**
- Create: `internal/backend/douyin/douyin.go`、`internal/backend/douyin/parse.go`、`internal/backend/douyin/douyin_test.go`

**Interfaces:**
- Produces: `func IsNoteURL(rawURL string) bool`、`func IsVideoURL(rawURL string) bool`
- Produces: `type Challenge int`，常量 `NoChallenge`/`JSVMChallenge`/`CaptchaChallenge`
- Produces: `func DetectChallenge(body string) Challenge`
- Produces: `type Image struct { URL, Ext string }`、`type Pair struct { ImageURL, VideoURL string }`
- Produces: `type Aweme struct { ID, Desc, Author string; Images []Image; VideoURL string; LivePairs []Pair }`
- Produces: `func ParsePaceF(html string) (*Aweme, error)`、`func ParseDetailJSON(body []byte) (*Aweme, error)`
- Produces: `type Downloader struct { HTTP *httpclient.Client; DataDir string; Now func() time.Time; RNG douyinsign.RNG }`
- Produces: `func (d *Downloader) DownloadNote(ctx context.Context, rawURL, cookie string, force bool) result.Result`
- Produces: `func (d *Downloader) DownloadVideo(ctx context.Context, rawURL, cookie string, force bool) result.Result`
- Consumes: `httpclient`、`douyinsign`、`fsutil`、`media`、`cookies`

- [ ] **Step 1: 写解析测试**

```go
// internal/backend/douyin/douyin_test.go
package douyin

import (
    "encoding/json"
    "testing"
)

func TestURLChecks(t *testing.T) {
    if !IsNoteURL("https://www.douyin.com/note/123") { t.Fatal("note") }
    if !IsVideoURL("https://www.douyin.com/video/123") { t.Fatal("video") }
    if IsNoteURL("https://www.douyin.com/user/x") { t.Fatal("user") }
}

func TestDetectChallenge(t *testing.T) {
    if DetectChallenge("<html>_$jsvmprt</html>") != JSVMChallenge { t.Fatal("jsvm") }
    if DetectChallenge("<title>验证码中间页</title>") != CaptchaChallenge { t.Fatal("captcha") }
    if DetectChallenge("<html>ok</html>") != NoChallenge { t.Fatal("normal") }
}

func TestParsePaceF(t *testing.T) {
    // 构造与 Python tests/test_douyin_downloader.py::_make_pace_f_html 等价的 HTML：
    // self.__pace_f.push([1,"<json>"])，JSON 为 {"statusCode":0,"detail":{...}}
    // 断言 Aweme.ID/Desc/Author/Images。
}
```

- [ ] **Step 2: 写下载测试**

```go
func TestDownloadNoteWritesNumberedImages(t *testing.T) {
    // httptest 页面返回 pace_f HTML；图片端点返回 PNG；Cookie 传 "UIFID=x; ttwid=y"
    // 断言 download/douyin/<author>/<title>_<id>/001.png 存在，Result.Posts[0].Files 长度 1。
}

func TestDownloadNoteJSVMFails(t *testing.T) {
    // httptest 返回 "_$jsvmprt"；断言 Status==failed 且 Message 含 "Cookie"。
}
```

（测试用 `httptest.NewServer`，不依赖真实网络；图片用 `image/png` 生成。）

- [ ] **Step 3: 运行测试确认失败**

Run: `go test ./internal/backend/douyin -v`
Expected: FAIL。

- [ ] **Step 4: 实现**

- 笔记：GET 页面，`DetectChallenge` 非空直接 `failed`；`ParsePaceF` 失败再尝试 `RENDER_DATA`/`_ROUTER_DATA`；仍失败则调签名 detail API 兜底。
- 视频：拼 detail API 参数，`douyinsign.NewABogus(ua).GetValue(query, ...)` + `websign.Sign(...)`；Cookie 解析出 `UIFID`，缺失返回 `failed` 并提示；HTTP 403 / 200 空响应分别给出对应文案。
- 媒体下载用 `httpclient.DownloadFile`，Referer 用原页面；图片按 `001.ext`，实况视频同序号；笔记级视频 `video.mp4`。
- 输出目录 `fsutil.BuildDownloadDir(dataDir, "douyin", title, author)`，标题加 `_<awemeId>`。

- [ ] **Step 5: 运行测试确认通过**

Run: `go test ./internal/backend/douyin -v`
Expected: PASS。

- [ ] **Step 6: 接入分发并提交**

Modify `cmd/downloader/main.go`：用 `douyin.Downloader` 填充 `Handlers.DouyinNote` / `DouyinVideo`。

```bash
git add internal/backend/douyin cmd/downloader
git commit -m "feat: add douyin note and video backend"
```

---

### Task 16: 小红书后端 `internal/backend/xhs`

**Files:**
- Create: `internal/backend/xhs/xhs.go`、`internal/backend/xhs/parse.go`、`internal/backend/xhs/xhs_test.go`

**Interfaces:**
- Produces: `func HasXsecToken(rawURL string) bool`
- Produces: `func NormalizeUndefined(raw string) string`
- Produces: `type Image struct { URL, Ext string }`、`type Pair struct { ImageURL, VideoURL string }`
- Produces: `type Note struct { ID, Title, Desc, Author string; Images []Image; VideoURL string; LivePairs []Pair }`
- Produces: `func ParseInitialState(html string) (*Note, error)`
- Produces: `func IsHomepageLanding(finalURL, body string) bool`
- Produces: `type Downloader struct { HTTP *httpclient.Client; DataDir string }`
- Produces: `func (d *Downloader) Download(ctx context.Context, rawURL, cookie string, force bool) result.Result`
- Consumes: `httpclient`、`fsutil`、`media`

- [ ] **Step 1: 写解析测试**

```go
// internal/backend/xhs/xhs_test.go
package xhs

import "testing"

func TestHasXsecToken(t *testing.T) {
    if !HasXsecToken("https://www.xiaohongshu.com/explore/1?xsec_token=abc") { t.Fatal("token") }
    if HasXsecToken("https://www.xiaohongshu.com/explore/1") { t.Fatal("no token") }
}

func TestNormalizeUndefined(t *testing.T) {
    in := `{"a":undefined,"b":["x",undefined],"c":"undefined"}`
    out := NormalizeUndefined(in)
    if out != `{"a":null,"b":["x",null],"c":"undefined"}` { t.Fatalf("%s", out) }
}

func TestParseInitialState(t *testing.T) {
    // 最小 HTML：<script>window.__INITIAL_STATE__ = {"note":{"noteDetailMap":{"1":{"note":{"noteId":"1","title":"t","desc":"d","user":{"nickname":"u"},"imageList":[{"urlDefault":"https://x/1.jpg"}]}}}}}</script>
    // 断言 Note.ID/Title/Author/Images。
}

func TestHomepageLanding(t *testing.T) {
    if !IsHomepageLanding("https://www.xiaohongshu.com/", `{"note":{"noteDetailMap":{}}}`) { t.Fatal("homepage") }
    if IsHomepageLanding("https://www.xiaohongshu.com/explore/1", `{"note":{"noteDetailMap":{"1":{}}}}`) { t.Fatal("note page") }
}
```

- [ ] **Step 2: 写下载测试**

```go
func TestDownloadWritesNumberedImages(t *testing.T) {
    // httptest 返回带 __INITIAL_STATE__ 的页面 + 两张 PNG；
    // 断言 001.png、002.png 与 Result.Posts[0].Files。
}

func TestExpiredShortLinkMessage(t *testing.T) {
    // 页面 200、最终 URL 为首页、noteDetailMap 为空；断言 failed 且 Message 含 "失效"。
}
```

- [ ] **Step 3: 运行测试确认失败**

Run: `go test ./internal/backend/xhs -v`
Expected: FAIL。

- [ ] **Step 4: 实现**

- 先 `HasXsecToken`，缺失直接 `failed` 并提示“链接必须带 xsec_token”。
- GET 页面（带 Cookie、Referer、UA），`IsHomepageLanding` 命中则提示“分享链接已失效”。
- 提取 `window.__INITIAL_STATE__`，`NormalizeUndefined` 后 `encoding/json` 解析；`noteDetailMap` 取第一个 note。
- 图片/实况/视频选最高档；`001.ext` 编号；视频 `video.mp4`；输出目录 `download/xiaohongshu[/<author>]/<title>_<noteId>`。
- 并发 5，瞬时错误重试 3 次。

- [ ] **Step 5: 运行测试确认通过**

Run: `go test ./internal/backend/xhs -v`
Expected: PASS。

- [ ] **Step 6: 接入分发并提交**

Modify `cmd/downloader/main.go`：用 `xhs.Downloader` 填充 `Handlers.XHS`。

```bash
git add internal/backend/xhs cmd/downloader
git commit -m "feat: add xiaohongshu backend"
```

---

### Task 17: Telegram Bot API 客户端 `internal/tgapi`

**Files:**
- Create: `internal/tgapi/client.go`、`internal/tgapi/types.go`、`internal/tgapi/client_test.go`

**Interfaces:**
- Produces: `type Client struct { Token, BaseURL string; HTTP *http.Client }`、`func New(token string) *Client`
- Produces: `func (c *Client) Call(ctx context.Context, method string, params url.Values) (json.RawMessage, error)`
- Produces: `func (c *Client) CallMultipart(ctx context.Context, method string, fields map[string]string, files map[string]string) (json.RawMessage, error)`
- Produces: `type Message struct { MessageID int64; Chat Chat; From User; Text, Caption string }`、`type Chat struct { ID json.Number; Type string }`、`type User struct { ID int64 }`
- Produces: `func (c *Client) GetUpdates(ctx context.Context, offset int64, timeout int) ([]Update, error)`
- Produces: `func (c *Client) SendMessage(ctx context.Context, chatID, text string) (Message, error)`
- Produces: `func (c *Client) DeleteMessage(ctx context.Context, chatID string, messageID int64) error`
- Produces: `type Media struct { Kind string; Path, Caption string }`（Kind: `photo`/`video`/`document`）
- Produces: `func (c *Client) SendMediaGroup(ctx context.Context, chatID string, media []Media) ([]Message, error)`

- [ ] **Step 1: 写测试**

```go
// internal/tgapi/client_test.go
package tgapi

import (
    "context"
    "net/http"
    "net/http/httptest"
    "net/url"
    "os"
    "path/filepath"
    "strings"
    "testing"
)

func TestCallUnwrapsResultAndErrors(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path != "/botTOKEN/sendMessage" { t.Errorf("path=%s", r.URL.Path) }
        if r.FormValue("chat_id") != "42" { t.Errorf("chat_id") }
        w.Write([]byte(`{"ok":true,"result":{"message_id":9,"chat":{"id":42,"type":"private"}}}`))
    }))
    defer srv.Close()
    c := New("TOKEN")
    c.BaseURL = srv.URL
    msg, err := c.SendMessage(context.Background(), "42", "hi")
    if err != nil || msg.MessageID != 9 { t.Fatalf("%+v %v", msg, err) }

    errSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Write([]byte(`{"ok":false,"description":"bad token"}`))
    }))
    defer errSrv.Close()
    c.BaseURL = errSrv.URL
    if _, err := c.SendMessage(context.Background(), "42", "hi"); err == nil || !strings.Contains(err.Error(), "bad token") {
        t.Fatalf("err=%v", err)
    }
}

func TestCallMultipartUploadsFile(t *testing.T) {
    dir := t.TempDir()
    p := filepath.Join(dir, "a.jpg")
    os.WriteFile(p, []byte("JPEG"), 0o600)
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if err := r.ParseMultipartForm(1 << 20); err != nil { t.Fatal(err) }
        f, hdr, err := r.FormFile("photo")
        if err != nil { t.Fatal(err) }
        defer f.Close()
        if hdr.Filename != "a.jpg" { t.Errorf("filename=%s", hdr.Filename) }
        w.Write([]byte(`{"ok":true,"result":{"message_id":1,"chat":{"id":1,"type":"private"}}}`))
    }))
    defer srv.Close()
    c := New("T")
    c.BaseURL = srv.URL
    _, err := c.CallMultipart(context.Background(), "sendPhoto", map[string]string{"chat_id": "1"}, map[string]string{"photo": p})
    if err != nil { t.Fatal(err) }
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/tgapi -v`
Expected: FAIL。

- [ ] **Step 3: 实现**

统一响应结构 `{"ok":bool,"result":...,"description":...}`；`Call` 用 `url.Values` 表单；`CallMultipart` 用 `mime/multipart`，文件字段按 `files` map 顺序写入（map 顺序无所谓，但测试只读一个文件）。`SendMediaGroup` 构造 `media` JSON，文件用 `attach://file0` 形式并在 multipart 中附带。默认 `BaseURL = "https://api.telegram.org"`。

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/tgapi -v`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/tgapi
git commit -m "feat: add minimal Telegram Bot API client"
```

---

### Task 18: 上传规划与转码 `internal/telegram`

**Files:**
- Create: `internal/telegram/plan.go`、`internal/telegram/transcode.go`、`internal/telegram/uploader.go`
- Create: `internal/telegram/plan_test.go`、`internal/telegram/transcode_test.go`、`internal/telegram/uploader_test.go`

**Interfaces:**
- Produces: `func MakeTag(text string) string`、`func BuildCaption(site, author, title string) string`
- Produces: `type FileKind int`，常量 `Photo`/`Video`/`Document`；`func Classify(path string) FileKind`
- Produces: `type FileInfo struct { Path string; Size int64; Kind FileKind }`；`func PlanGroups(files []FileInfo, max int) [][]FileInfo`
- Produces: `type Runner func(ctx context.Context, name string, args ...string) (stdout, stderr []byte, err error)`
- Produces: `type Uploader struct { API *tgapi.Client; FFmpeg, FFprobe string; Run Runner; WorkDir string }`
- Produces: `func (u *Uploader) Send(ctx context.Context, chatID string, post result.Post) (sent int, skipped []string, err error)`
- Produces: `func (u *Uploader) PreparePhoto(ctx context.Context, path string) (prepared string, asDocument bool, err error)`
- Produces: `func (u *Uploader) CompressVideo(ctx context.Context, path string, targetBytes int64) (string, error)`

- [ ] **Step 1: 写纯函数测试**

```go
// internal/telegram/plan_test.go
package telegram

import "testing"

func TestCaptionAndTag(t *testing.T) {
    if got := BuildCaption("douyin", "作者 甲", "标题"); got != "#作者_甲 #douyin\n标题" { t.Fatalf("%q", got) }
    if got := MakeTag("a b/c"); got != "#a_b_c" { t.Fatal(got) }
}

func TestPlanGroupsMaxTenAndOrder(t *testing.T) {
    files := make([]FileInfo, 0, 12)
    for i := 0; i < 12; i++ { files = append(files, FileInfo{Path: "x", Kind: Photo}) }
    groups := PlanGroups(files, 10)
    if len(groups) != 2 || len(groups[0]) != 10 || len(groups[1]) != 2 { t.Fatalf("%v", groups) }
}
```

- [ ] **Step 2: 写转码测试**

```go
func TestPreparePhotoHEICUsesFFmpeg(t *testing.T) {
    dir := t.TempDir()
    src := filepath.Join(dir, "a.heic")
    if err := os.WriteFile(src, []byte("heic"), 0o600); err != nil { t.Fatal(err) }
    calls := [][]string{}
    u := &Uploader{FFmpeg: "ffmpeg", Run: func(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
        calls = append(calls, append([]string{name}, args...))
        return nil, nil, nil
    }}
    got, asDoc, err := u.PreparePhoto(context.Background(), src)
    if err != nil || asDoc || !strings.HasSuffix(got, ".jpg") { t.Fatalf("%s %v %v", got, asDoc, err) }
    if len(calls) != 1 || !strings.Contains(strings.Join(calls[0], " "), "-frames:v") { t.Fatalf("%v", calls) }
}
```

- [ ] **Step 3: 运行测试确认失败**

Run: `go test ./internal/telegram -v`
Expected: FAIL。

- [ ] **Step 4: 实现**

- `PlanGroups` 只把 `Photo`/`Video` 编入相册，按输入顺序、每组 ≤10；`Document` 单独发送。
- `PreparePhoto`：HEIC 或 >10MB 或尺寸和 >10000 时用 ffmpeg 转/压 JPEG；ffmpeg 不存在或失败 → `asDocument=true`。
- `CompressVideo`：`ffprobe` 取时长，计算 `视频码率 = (49MB*8/duration) - 96kbps`，下限 200kbps；`ffmpeg -y -i in -c:v libx264 -b:v <rate> -c:a aac -b:a 96k out.mp4`；没有 ffmpeg/ffprobe 返回错误。
- `Send`：逐个 Post 分组；>50MB 视频尝试压缩，仍超限则加入 `skipped`；照片超限且转码失败按 document；caption 只放第一项。

- [ ] **Step 5: 运行测试确认通过**

Run: `go test ./internal/telegram -v`
Expected: PASS。

- [ ] **Step 6: 提交**

```bash
git add internal/telegram
git commit -m "feat: add Telegram upload planning and transcoding"
```

---

### Task 19: Bot 循环与 CLI 上传 `internal/telegram`、`cmd/downloader`

**Files:**
- Create: `internal/telegram/bot.go`、`internal/telegram/bot_test.go`
- Create: `internal/telegram/cli_upload.go`、`internal/telegram/cli_upload_test.go`
- Modify: `cmd/downloader/main.go`

**Interfaces:**
- Produces: `type Bot struct { API *tgapi.Client; Config config.Telegram; DataDir string; Dispatch func(ctx context.Context, url string, force bool) result.Result; Uploader *Uploader; AfterFunc func(time.Duration, func()) *time.Timer }`
- Produces: `func (b *Bot) HandleMessage(ctx context.Context, msg tgapi.Message) error`
- Produces: `func (b *Bot) Run(ctx context.Context) error`
- Produces: `func UploadPath(ctx context.Context, u *Uploader, chatID, path, caption string) (sent int, skipped []string, err error)`
- Consumes: `internal/logging.WithRequest` 作为单条链接的日志作用域。

- [ ] **Step 1: 写 Bot 测试**

```go
// internal/telegram/bot_test.go
package telegram

import (
    "context"
    "testing"

    "github.com/backtraxe/downloader-go/internal/config"
    "github.com/backtraxe/downloader-go/internal/result"
    "github.com/backtraxe/downloader-go/internal/tgapi"
)

func TestHandleMessageAllowlistAndChannel(t *testing.T) {
    // 构造 fake API（记录 sendMessage）、fake dispatch（返回 Downloaded + 一个 Post）
    // 1) allowed_users=[1]，from.id=2 → 回复包含 "未授权" 且不调用 dispatch
    // 2) chat.type="channel" → 跳过白名单
}

func TestHandleMessageSkippedAndFailed(t *testing.T) {
    // dispatch 返回 Skipped → 文案含 "已下载过"，不调用上传
    // dispatch 返回 Failed → 文案含失败原因，不调用上传
}

func TestHandleMessageUploadsOnlyNewFiles(t *testing.T) {
    // dispatch 返回 Post.Files 两项；断言上传两次，且没有扫描 download/ 目录
}
```

- [ ] **Step 2: 写 CLI 上传测试**

```go
func TestUploadPathDirectory(t *testing.T) {
    // 临时目录 3 个文件（2 图 1 视频），fake Uploader 记录分组；
    // 断言 1 个相册组、caption 正确。
}
```

- [ ] **Step 3: 运行测试确认失败**

Run: `go test ./internal/telegram -v`
Expected: FAIL。

- [ ] **Step 4: 实现**

- `HandleMessage`：`/start`/`/help` 回帮助；非频道校验白名单；`extractURLs`；逐条 `logging.WithRequest` + `Dispatch`；`Failed`/`NoMedia`/`Skipped` 分别回文案；`Posts` 逐个 `Uploader.Send`；完成提示带 PARTIAL 详情；文字提示用 `AfterFunc` 按 TTL 删除。
- `Run`：启动时 `GetUpdates(offset=0, timeout=0)` 跳过积压，然后长轮询；网络错误 5s 重试；`message` 与 `channel_post` 都处理。
- `UploadPath`：目录按 `PlanGroups` 发送，单文件直接分类发送。

- [ ] **Step 5: 接入主程序**

`cmd/downloader/main.go` 增加 `bot` 与 `upload` 子命令；`bot` 加载配置，未配置时打印指引并返回 1。

- [ ] **Step 6: 运行测试确认通过**

Run: `go test ./internal/telegram ./cmd/downloader -v`
Expected: PASS。

- [ ] **Step 7: 提交**

```bash
git add internal/telegram cmd/downloader
git commit -m "feat: add Telegram bot loop and CLI upload"
```

---

### Task 20: Cookie 子命令 `cmd/downloader`

**Files:**
- Create: `cmd/downloader/cookie.go`、`cmd/downloader/cookie_test.go`
- Modify: `cmd/downloader/main.go`

**Interfaces:**
- Produces: `downloader cookie import <site>`（stdin）
- Produces: `downloader cookie check [sites...]`（缺失时退出码 1）
- Consumes: `internal/cookies`、`internal/sites`

- [ ] **Step 1: 写测试**

```go
// cmd/downloader/cookie_test.go
package main

import (
    "bytes"
    "os"
    "path/filepath"
    "strings"
    "testing"
)

func TestCookieImportAndCheck(t *testing.T) {
    dir := t.TempDir()
    var out bytes.Buffer
    code := runWith([]string{"--data-dir", dir, "cookie", "import", "douyin"},
        strings.NewReader("Cookie: UIFID=u; ttwid=t"), &out, &out, dependencies{})
    if code != 0 { t.Fatalf("import code=%d out=%s", code, out.String()) }
    p := filepath.Join(dir, "cookies", "douyin.txt")
    if _, err := os.Stat(p); err != nil { t.Fatal(err) }

    out.Reset()
    code = runWith([]string{"--data-dir", dir, "cookie", "check", "douyin"}, strings.NewReader(""), &out, &out, dependencies{})
    if code != 0 { t.Fatalf("check code=%d out=%s", code, out.String()) }

    out.Reset()
    code = runWith([]string{"--data-dir", dir, "cookie", "check", "xiaohongshu"}, strings.NewReader(""), &out, &out, dependencies{})
    if code != 1 { t.Fatalf("missing should exit 1: %d %s", code, out.String()) }
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./cmd/downloader -run TestCookie -v`
Expected: FAIL。

- [ ] **Step 3: 实现**

`import` 读取全部 stdin，`cookies.Detect` 后按站点写 `cookies/<site>.txt`（Netscape 直接存，Header 也存原文，JSON 转 Header）；输出路径，不输出内容。`check` 遍历 `cookies/*.txt` 或指定站点，打印 `✓/✗` 与缺失键，有缺口返回 1。

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./cmd/downloader -v`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add cmd/downloader
git commit -m "feat: add cookie import and check subcommands"
```

---

### Task 21: CI、GoReleaser 与 Termux 安装脚本

**Files:**
- Create: `.github/workflows/ci.yml`、`.github/workflows/release.yml`、`.goreleaser.yaml`
- Create: `install_termux.sh`
- Modify: `README.md`、`AGENTS.md`、`.gitignore`

**Interfaces:**
- Produces: push/PR 跑测试与 lint；tag `v*` 产出 Release。
- Produces: `install_termux.sh` 支持 `--data-dir`、`--migrate-from`、`DOWNLOADER_VERSION`。

- [ ] **Step 1: 写 CI**

`ci.yml`：`actions/setup-go@v5`（`go-version-file: go.mod`）、`go build ./...`、`go vet ./...`、`golangci/golangci-lint-action`、`go test ./...`。

- [ ] **Step 2: 写 GoReleaser 配置**

`.goreleaser.yaml`：

```yaml
version: 2
builds:
  - env: [CGO_ENABLED=0]
    ldflags:
      - -s -w
      - -X main.version={{.Version}}
      - -X main.commit={{.Commit}}
      - -X main.date={{.Date}}
    goos: [linux, darwin, windows]
    goarch: [amd64, arm64]
    ignore:
      - goos: windows
        goarch: arm64
archives:
  - formats: [tar.gz]
    format_overrides:
      - goos: windows
        formats: [zip]
checksum:
  name_template: checksums.txt
```

上面只排除了 `windows/arm64`；`linux/arm64` 必须保留（Termux 主目标），`darwin/amd64` 与 `linux/amd64` 也必须保留。

- [ ] **Step 3: 写 release workflow**

`release.yml`：tag `v*` 触发，`permissions: contents: write`，`goreleaser/goreleaser-action` 执行 `release --clean`。

- [ ] **Step 4: 写安装脚本**

`install_termux.sh`：`set -euo pipefail`；识别 `uname -m`（`aarch64` → `linux_arm64`）；从 `https://api.github.com/repos/backtraxe/downloader-go/releases/latest` 取 asset URL；下载 tar.gz 与 `checksums.txt`，`sha256sum -c`；安装到 `$PREFIX/bin/downloader`；检查 `command -v yt-dlp ffmpeg`；处理 `--data-dir`（幂等写 shell rc）与 `--migrate-from`（复制 cookies/config、符号链接 download）。

- [ ] **Step 5: 写文档**

`README.md`：Termux 安装、`downloader run/bot/upload/cookie` 用法、数据目录、依赖、Release 下载。`AGENTS.md`：Go 项目结构、命令、约束（与设计文档一致）。`.gitignore`：`download/`、`cookies/`、`config.json`、`*.part`、`dist/`。

- [ ] **Step 6: 验证**

```bash
go test ./...
go vet ./...
bash -n install_termux.sh
goreleaser check   # 若本机安装 goreleaser
```

Expected: 全部通过。

- [ ] **Step 7: 提交**

```bash
git add .github .goreleaser.yaml install_termux.sh README.md AGENTS.md .gitignore
git commit -m "ci: add build, lint and release pipeline"
```

---

### Task 22: 端到端冒烟与 v0.1.0 发布

**Files:**
- Modify: `README.md`（如有安装问题）
- Create: `docs/smoke-test.md`（记录验证结果）

**Interfaces:**
- Consumes: 前 21 个任务的全部产物。
- Produces: `v0.1.0` tag 与可安装的 Release。

- [ ] **Step 1: 本机冒烟**

```bash
go test ./...
go build -trimpath -o /tmp/downloader ./cmd/downloader
/tmp/downloader version
printf 'https://example.com/\nq\n' | /tmp/downloader --data-dir /tmp/dl-smoke run
```

Expected: `version` 输出正确；通用抓取在 `/tmp/dl-smoke/download/example.com/` 下产生文件或明确 `no_media`。

- [ ] **Step 2: Termux 冒烟**

- 安装 `pkg install yt-dlp ffmpeg`。
- 用 `install_termux.sh` 安装二进制。
- 验证：`downloader run` 下载一个通用网页；带 Cookie 下载小红书/抖音各一条；`yt-dlp` 站点一条。
- 配置 `config.json` 的 `telegram` 段，`downloader bot` 发一条链接，确认相册/视频回传与 caption 标签。
- 记录每条链路的实际结果到 `docs/smoke-test.md`。

- [ ] **Step 3: 打 tag 并发布**

```bash
git tag v0.1.0
git push origin main
git push origin v0.1.0
gh release view v0.1.0
```

Expected: Release 包含 5 个平台包与 `checksums.txt`；下载 `linux_arm64` 包在 Termux 上可运行。

- [ ] **Step 4: 更新旧仓库说明**

在 Python 仓库 README 顶部加一行指向 `downloader-go`，说明新仓库用于 Termux 稳定运行；不删除旧实现。

- [ ] **Step 5: 提交**

```bash
git add docs/smoke-test.md README.md
git commit -m "docs: record v0.1.0 smoke test results"
git push
```

---

## 自检清单（执行前）

- [ ] 每个任务都能独立编译并通过自己的测试。
- [ ] 后续任务引用的类型/函数名与前面 `Interfaces` 一致。
- [ ] Review Focus 的 6 条都有对应测试。
- [ ] 没有把 Python 重新引入主流程。
- [ ] 所有新文件都在 `internal/` 或 `cmd/` 下，测试与被测包同目录。
- [ ] 提交信息使用约定式前缀，主题行简短。
