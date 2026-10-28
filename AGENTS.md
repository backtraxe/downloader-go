# AGENTS.md

本文件是仓库的编码与协作指南。设计细节见
`docs/superpowers/specs/2026-10-28-go-rewrite-design.md`。

## 项目结构

```text
cmd/downloader/        子命令入口（run / bot / upload / cookie / version）
internal/config        config.json 加载与旧 telegram.json 兼容
internal/logging       请求级 [rXXXX] 日志
internal/result        Status / Result / Post 契约
internal/sites         URL→站点名、Cookie 文件定位
internal/urlutil       链接提取、URL 归一化、短链展开
internal/cookies       Cookie 格式识别、导入、体检
internal/fsutil        文件名清洗、原子去重、.part 原子替换、目录构造
internal/media         扩展名推断、图片尺寸、装饰图判定
internal/httpclient    utls Chrome 客户端、Cookie jar、重试、multipart
internal/backend/xhs       小红书解析与下载
internal/backend/douyin    抖音图文/视频解析与下载
internal/backend/generic   通用网页抓取
internal/backend/ytdlp     yt-dlp 探测与下载
internal/douyinsign    a_bogus / SM3 / web signature 移植（Apache-2.0）
internal/dispatch      路由
internal/telegram      bot 长轮询、白名单、上传规划、转码
internal/tgapi         Telegram Bot API multipart 客户端
```

## 常用命令

```bash
# 构建与测试（所有 Go 命令都使用 CGO_ENABLED=0）
CGO_ENABLED=0 go build -o dl-go ./cmd/downloader
CGO_ENABLED=0 go test ./...
CGO_ENABLED=0 go vet ./...

# 静态检查
golangci-lint run

# 本地验证发布配置（需安装 goreleaser v2）
goreleaser check
goreleaser release --snapshot --clean

# 安装脚本语法检查
bash -n install_termux.sh
```

单元测试必须离线：使用 `httptest.Server` 与 `testdata/` 中的脱敏 fixture，
不访问真实站点。

## 关键约束

- **Go 版本**：`go 1.27`；构建、测试、发布一律 `CGO_ENABLED=0`。
- **平台**：`linux/arm64`（Termux 主目标）、`linux/amd64`、`darwin/arm64`、
  `darwin/amd64`、`windows/amd64`；不新增需要 CGO 的依赖。
- **主流程不依赖 Python**：`yt-dlp`、`ffmpeg` 只能通过
  `exec.CommandContext` 调用；缺失时降级或返回 `failed`，不得 panic。
- **数据目录**：`--data-dir` > `DOWNLOADER_HOME` > 当前工作目录；
  `download/`、`cookies/`、`config.json` 都相对于数据目录。`--data-dir`
  只接受写在子命令之前。
- **输出布局**：`download/<site>[/<author>]/<标题>_<ID>/` 或
  `download/<site>[/<author>]/<标题>.<ext>`。
- **Telegram 限制**：文件 ≤50MB、照片 ≤10MB、相册每组 ≤10 项；边界值要
  降级或跳过，不能让单条消息失败中断整批。
- **凭据保密**：Cookie、`config.json`、`download/` 不提交、不打印 Cookie
  值或 bot token（错误信息也要脱敏）。
- **许可证**：整体 MIT；`internal/douyinsign/` 保留 Apache-2.0 文件头，
  根目录维护 `NOTICE`。
- **行为基线**：与 Python 仓库 commit `1a4f129` 对齐；改变用户可见行为时
  在 README 或设计文档中说明。
