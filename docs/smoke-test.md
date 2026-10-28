# 本机冒烟测试记录（Task 22 Step 1）

- 日期：2026-10-28
- 分支：`feat/v0.1`
- 环境：Linux amd64，Go 1.27.3，`CGO_ENABLED=0`
- 范围：仅本机通用网页抓取链路；Termux、真实站点、yt-dlp、Telegram 与 Release 未在本轮验证（见文末）。

## 1. 单元测试

```bash
cd /code/downloader-go
go test ./...
```

结果：全部包通过。

```text
ok  	github.com/backtraxe/downloader-go/cmd/downloader	0.009s
ok  	github.com/backtraxe/downloader-go/internal/backend/douyin	(cached)
ok  	github.com/backtraxe/downloader-go/internal/backend/generic	(cached)
ok  	github.com/backtraxe/downloader-go/internal/backend/xhs	(cached)
ok  	github.com/backtraxe/downloader-go/internal/backend/ytdlp	(cached)
ok  	github.com/backtraxe/downloader-go/internal/config	(cached)
ok  	github.com/backtraxe/downloader-go/internal/cookies	(cached)
ok  	github.com/backtraxe/downloader-go/internal/dispatch	(cached)
ok  	github.com/backtraxe/downloader-go/internal/douyinsign	(cached)
ok  	github.com/backtraxe/downloader-go/internal/fsutil	(cached)
ok  	github.com/backtraxe/downloader-go/internal/httpclient	(cached)
ok  	github.com/backtraxe/downloader-go/internal/logging	(cached)
ok  	github.com/backtraxe/downloader-go/internal/media	(cached)
ok  	github.com/backtraxe/downloader-go/internal/result	(cached)
ok  	github.com/backtraxe/downloader-go/internal/sites	(cached)
ok  	github.com/backtraxe/downloader-go/internal/telegram	0.028s
ok  	github.com/backtraxe/downloader-go/internal/tgapi	0.015s
ok  	github.com/backtraxe/downloader-go/internal/urlutil	(cached)
```

## 2. 构建与版本

```bash
CGO_ENABLED=0 go build -trimpath -o /tmp/downloader ./cmd/downloader
/tmp/downloader version
```

输出（未注入 `-ldflags`，因此为开发默认值）：

```text
downloader dev (commit none, built unknown)
```

退出码：`0`。发布构建需通过 `-ldflags "-X main.version=... -X main.commit=... -X main.date=..."` 注入真实版本信息。

## 3. 本地冒烟站点

在 `/tmp/dl-smoke-site` 下构造：

- `index.html`：`<title>测试页</title>`，正文含 `<img src="pic.png">`。
- `pic.png`：400x400 有效 PNG，使用一次性 Go 程序（`image/png`）生成后删除该程序。1x1 图会被当作装饰图过滤，故使用 400x400。

启动服务：

```bash
python3 -m http.server 18080 --bind 127.0.0.1 --directory /tmp/dl-smoke-site
```

`GET http://localhost:18080/` 返回 `200`。

## 4. 通用抓取

```bash
printf 'http://localhost:18080/\nq\n' | /tmp/downloader --data-dir /tmp/dl-smoke run
```

输出：

```text
downloaded: 新增 1 项
```

退出码：`0`。

实际落盘路径（与预期一致）：

```text
/tmp/dl-smoke/download/localhost_18080/测试页/001.png
```

目录树：

```text
/tmp/dl-smoke
└── download
    └── localhost_18080
        └── 测试页
            └── 001.png
```

文件校验：下载文件为 400x400 PNG，SHA-256 与原图一致。

```text
44089ad95bed97bdd5348ede5824116d319994995762693f08bf827599ddf48f  /tmp/dl-smoke/download/localhost_18080/测试页/001.png
44089ad95bed97bdd5348ede5824116d319994995762693f08bf827599ddf48f  /tmp/dl-smoke-site/pic.png
```

## 5. Cookie 体检

```bash
/tmp/downloader --data-dir /tmp/dl-smoke cookie check
```

输出：

```text
cookies/ 下没有可检查的 .txt 文件（目录不存在或为空）。
```

退出码：`0`。全新数据目录下无 Cookie 文件属预期。

## 6. 收尾

HTTP 服务已停止，端口 18080 不再响应。

## 未验证项（不在本轮范围内）

以下链路本轮**未**运行、**未**验证，不能视为通过：

- Termux（aarch64/Android）上的安装与运行，包括 `install-dl-go.sh`。
- 真实小红书（xhs）与抖音（douyin）下载，包括 Cookie 鉴权与签名逻辑。
- `yt-dlp` 后端（未安装 `yt-dlp`，未测第三方站点）。
- Telegram 机器人 `bot`、`upload` 与相册/视频回传、caption 标签。
- Release 资产：`v0.1.0` tag、5 平台包与 `checksums.txt` 均未创建或下载验证。
- Windows/macOS 等其他平台二进制。
