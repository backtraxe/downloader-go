# downloader-go 设计文档

- 日期：2026-10-28
- 状态：待评审
- 目标仓库：`github.com/backtraxe/downloader-go`（公开，MIT）
- 参考实现：当前 Python 仓库 `downloader-bot`

## 1. 意图与原因

当前 Python 版在 Termux 上的核心痛点是运行时不稳定：Termux 的 `python` 随软件源滚动升级，升级后旧 `site-packages` 整体失效；纯 Python 依赖可以重装，`curl_cffi` 因 PyPI 没有 Android/Bionic wheel、Termux 源也没有对应包，只能源码编译，经常失败。

目标是用 Go 重写业务逻辑，做到：

- 单个静态二进制，Termux 系统升级不影响自身运行；
- 只保留两个外部命令：`yt-dlp`（负责 bilibili/youtube/instagram/twitter）与 `ffmpeg`（HEIC 预览、>50MB 视频压缩），两者都由 Termux 包管理器维护；
- TLS 指纹用纯 Go 的 `utls` 模拟 Chrome，替代 `curl_cffi`；
- 保持现有 Cookie 文件、`config.json`、`download/` 目录布局兼容，用户可平滑切换；
- GitHub Actions 自动测试、交叉编译并在打 tag 时发布 Release，Termux 一条命令安装/升级。

## 2. 目标与非目标

### 目标

1. 下载能力与现有版本对齐：小红书、抖音图文、抖音视频、通用网页抓取、yt-dlp 站点。
2. Telegram 闭环：长轮询 bot 接收链接 → 下载 → 相册/视频/文件回传；另有 CLI 直发入口。
3. Cookie 维护：Netscape / `Cookie:` 请求头 / JSON 自动识别导入，离线体检。
4. 输出目录、文件名、`--force`、重复链接跳过等行为与 Python 版一致。
5. CI 覆盖单元测试与静态检查；Release 产出多平台二进制和校验和。
6. Termux 安装脚本：自动识别架构、下载对应 Release、校验 SHA256、检查 `yt-dlp`/`ffmpeg`。

### 非目标

- 不重写 yt-dlp 的站点提取器；不打包 yt-dlp/ffmpeg。
- 不做 live photo 后处理（现有 `core/tools/live_photo.py` 的 Motion Photo / iOS 功能不迁移）。
- 不做 Playwright 半自动登录（`cookie_login.py` 不迁移）。
- 不做 Web UI、常驻服务端、多用户队列、2GB 自建 Bot API。
- 不覆盖老设备架构（`linux/armv7`、Windows ARM）；Windows 只构建 amd64，且不作为主要验证平台。
- 不追求与 Python 版逐行一致，只保证用户可见行为一致。

### 反目标

- 为了“复用”而把 Python 重新引入主流程（例如 Go 调 Python 做解析）——会重新引入原问题。
- 把所有站点提取器自己重写——维护成本不可接受。
- 依赖 CGO 或平台特定编译产物——会破坏单二进制部署。

## 3. 约束

- 主目标平台：Termux aarch64，构建目标 `GOOS=linux GOARCH=arm64 CGO_ENABLED=0`。
- 数据根目录解析顺序：`--data-dir` 参数 > `DOWNLOADER_HOME` 环境变量 > 当前工作目录。`download/`、`cookies/`、`config.json` 都相对于该根目录；不传任何参数时行为与 Python 版一致（CWD 相对）。
- Telegram 官方 Bot API：单文件 ≤50MB，照片 ≤10MB，相册每组 ≤10 项。
- `utls` 为纯 Go 依赖，可静态交叉编译；不引入任何 CGO 依赖。
- Cookie 与 `config.json` 属于敏感数据，不提交、不打印值。
- 运行环境可能没有 `ffmpeg`：HEIC/大图/大视频相关能力降级为按文件发送或跳过。

## 4. 方案选择

### 4.1 构建形态

- **A. 单二进制 + 子命令（已选）**：`downloader run|bot|upload|cookie|version`。部署、升级、文档只维护一个产物。
- B. 双二进制 `dl` / `tgbot`：职责更清晰，但重复发布与文档，收益不足。
- C. 分两期（先 CLI 后 bot）：风险低，但闭环交付晚；当前已确认要下载 + TG 上传，直接按 A 做。

### 4.2 HTTP / 指纹

- **utls + net/http（已选）**：纯 Go，可静态编译，TLS ClientHello 可模拟 Chrome；HTTP/2 通过 `x/net/http2` 自定义 DialTLS。
- 系统 curl 子进程：可作为后续逃生通道，但会重新引入响应头解析、流式与超时语义问题，v1 不做。
- 纯 `net/http`：无指纹伪装，放弃。

### 4.3 抖音签名

- **直接移植为 Go（已选）**：现有 Python 实现已有测试向量，移植后可用同一批向量做黄金测试；无额外运行时。
- 内嵌 `goja` 跑原始混淆 JS：体积大、原始 JS 依赖浏览器环境，脆弱，放弃。

### 4.4 Telegram 客户端

- **手写最小 Bot API 客户端（已选）**：只需 `getUpdates`、`sendMessage`、`deleteMessage`、`sendPhoto`、`sendVideo`、`sendDocument`、`sendMediaGroup`；手写 multipart 可精确控制相册混排、文件上传与 50MB 边界。
- 第三方 bot 库：省事，但混合相册与自定义转码/上传路径容易受限，增加依赖，收益不足。

## 5. 架构

### 5.1 目录结构

```text
cmd/downloader/main.go          子命令入口
internal/config                 config.json + 旧 telegram.json 兼容
internal/logging                请求级 [rXXXX] 日志
internal/result                 Status / Result / Post 契约
internal/sites                  URL→站点名、Cookie 文件定位
internal/cookies                格式识别、导入、体检、Netscape 生成
internal/fsutil                 文件名清洗、原子去重、.part 原子替换、目录构造
internal/media                  扩展名推断、图片尺寸、装饰图判定
internal/httpclient             utls Chrome 客户端、Cookie jar、重试、multipart
internal/backend/ytdlp          yt-dlp 探测与下载
internal/backend/xhs            小红书解析与下载
internal/backend/douyin         抖音图文/视频解析与下载
internal/backend/generic        通用网页抓取
internal/douyinsign             a_bogus / SM3 / web signature 移植
internal/dispatch               路由
internal/telegram               bot 长轮询、白名单、上传规划、转码
internal/tgapi                  Bot API multipart 客户端
```

### 5.2 结果契约

后端统一返回：

```go
type Status string // downloaded | partial | skipped | failed | no_media

type Post struct {
    Site    string
    Author  string
    Title   string
    Status  Status   // 可选：多 Post（播放列表）时逐 Post 判定；单 Post 与顶层一致
    Error   string   // 可选：该 Post 的失败原因
    Files   []string // 仅本次调用写入或覆盖的文件；已存在而跳过的文件不放入
}

type Result struct {
    Status  Status
    Message string
    Posts   []Post
}
```

- 状态判定沿用现有优先级：新增 > 失败 > 已存在 > 被过滤。
- 缺 Cookie、请求失败、解析失败必须是 `failed`，不能静默成功。
- **与 Python 版的关键差异**：bot 不再对 `download/` 做前后快照 diff，而是直接消费 `Result.Posts`。这消除了“同一目录旧文件被重发”“目录结构猜测作者”的问题；后端在落盘时就知道站点、作者、标题。
- `Result` 允许多个 Post（yt-dlp 播放列表），bot 逐 Post 回传。
- `Post.Files` 只表达“本次调用新写入或覆盖的文件”：`SKIPPED` 的 Post 不填 Files（bot 只提示“已下载过”）；`PARTIAL` 的 Post 只填成功项，失败原因放 `Post.Error` / 顶层 `Message`；单 Post 场景仍以顶层 `Status` 为准。

### 5.3 分发链路

```text
URL → sites.Detect → dispatch
  xiaohongshu          → xhs
  douyin /note/        → douyin note
  douyin /video/       → douyin video（签名 API）
  bilibili/youtube/... → ytdlp
  其他                 → generic
```

- 站点识别只改 `internal/sites`。
- 抖音短链先展开（重试 3 次）再判断 `/note/` 或 `/video/`；`/user/` 等仍交给 yt-dlp。
- `--force` 逐层下传。

### 5.4 HTTP 客户端

- `utls` 使用固定的 Chrome profile；每个站点定义 `{HelloID, UA}`，保证 UA 与指纹版本一致。受 utls v1.8.2 可用预设限制：xhs/douyin 用 `HelloChrome_133`（UA 同步为 Chrome/133，这是当前依赖里最新的 Chrome 预设），generic 用 `HelloChrome_120`（UA Chrome/120）；不再追求与 Python 版的 chrome150/chrome110 逐版本一致。
- **先用真机 spike 验证**：M2 开始前，用一个最小 Go 程序在 Termux 上请求小红书笔记页与抖音笔记页，确认 utls + HTTP/2 能通过风控；抖音视频详情 API 需要签名，留到签名移植后再验证。通过则 v1 只保留 utls；失败则把系统 curl 兜底提升为 v1 必做项（spike 代码不进入正式仓库）。
- 连接超时 15s、响应头超时 30s、读取空闲超时 30s；媒体下载不设总时长上限。
- 支持重定向、Cookie jar、gzip；媒体流式写入 `.part` 后 `os.Rename`。
- 瞬时错误分类与重试：DNS/连接重置/超时/429/502/503，指数退避 + 抖动。
- 不打印 Cookie；日志只记录键名与文件路径。
- 风险：`utls` 的 HTTP/2 SETTINGS 与 Chrome 仍有差异；若某站校验 HTTP/2 指纹，先用 HTTP/1.1 profile 验证，再评估是否增加系统 curl 逃生通道。

### 5.5 各后端要点

**yt-dlp**

- `exec.LookPath("yt-dlp")`；缺失时返回 `failed` 并提示 `pkg install yt-dlp`。
- 探测：`yt-dlp -J --skip-download --no-warnings --cookies <file> URL`，取 `uploader`、`title`、`ext`。
- 下载：`yt-dlp --no-warnings --newline -o <template> --merge-output-format mp4 --concurrent-fragment-downloads 3 --retries 10 --fragment-retries 10 --print after_move:filepath ...`；`--force` 时加 `--force-overwrites`。
- 用 `after_move:filepath` 拿最终文件，避免扫描目录。
- Cookie 只传非空 Netscape 文件。

**小红书**

- 必须带 Cookie；链接必须带 `xsec_token`。
- 解析 `__INITIAL_STATE__`，处理 JSON 值语境中的 `undefined`。
- 识别首页落地（失效短链）与风控 404，给出不同提示。
- 图片/实况/视频原图最高档；输出 `001.heic|jpg`、实况 `001.mp4`、视频 `video.mp4`。
- 并发 5，瞬时错误重试。

**抖音**

- 图文笔记：带登录 Cookie 抓页面，识别 jsvm 挑战页与验证码页，解析 `__pace_f` 流式数据块；被拦时改走签名 detail API。
- 视频：签名 detail API，签名参数 `a_bogus` + `msToken` + `x-secsdk-web-signature`；门禁为 Cookie 含 `UIFID` 且 `ttwid`/`sessionid` 至少一个。
- 区分 403（缺 UIFID）、200 空响应（凭据失效）、正常。
- 图片优先无水印最高档；实况配对、视频 `video.mp4`。

**通用抓取**

- goquery 扫描 `img/source/video` 的懒加载属性、`<a href>` 直链、行内 `background-image`。
- 排除伪媒体链接（如 MediaWiki 文件页）。
- 下载前按文件名关键词过滤装饰图；落盘后按短边 <300px 过滤（Go `image.DecodeConfig`，webp 用 `x/image/webp`；无法解码时回落 20KB 体积阈值）。
- 总并发 5，同一 host 最多 2 路握手。
- `.skip` 标记避免重跑重复下载装饰图；隐藏文件不参与上传。

### 5.6 Telegram

- 配置沿用 `config.json` 的 `telegram` 段：`bot_token`、`chat_id`、`allowed_users`、`process_message_ttl_seconds`；旧 `telegram.json` 自动兜底。
- bot：长轮询 `getUpdates`，启动时跳过积压；支持 `message` 与 `channel_post`；频道不做用户白名单。
- 上传规划：
  - 图片（jpg/jpeg/png/webp）与视频（mp4/mov/webm/mkv）混排，每组 ≤10，发 `sendMediaGroup`；
  - 照片 >10MB 或尺寸和 >10000：ffmpeg 缩放/压到 ≤10MB；失败降级 `sendDocument`；
  - HEIC：ffmpeg 转 JPEG 预览，失败按文件发送；
  - 视频 >50MB：ffmpeg 按码率预算压到 ~49MB，失败则跳过并提示本地路径；
  - 单图/单视频走 `sendPhoto`/`sendVideo`；其他文件走 `sendDocument`。
- caption：`#作者 #站点` + 标题；相册只在第一项带 caption。
- 过程提示消息按 TTL 自动删除；媒体消息永不删除。
- 串行处理，一条消息内的多个链接顺序执行。

### 5.7 日志

- 每个链接一个随机 `[rXXXX]` 作用域；CLI、bot 单条消息、独立工具入口开启。
- 日志同时输出到 stderr；bot 常驻场景建议 tmux。

### 5.8 Cookie 与配置

- `cookies/<site>.txt`：自动识别 Netscape / `Cookie:` 头 / JSON（导入时）。
- `cookie import <site>` 从 stdin 读取并写文件；`cookie check [sites]` 只检查键名，有明确缺口时退出码 1。
- 门禁同现有版本：抖音 `UIFID`/`UIFID_TEMP` + `ttwid`/`sessionid` 其一；小红书 `a1` + `webId`/`web_session`。
- 不实现浏览器登录自动化。

### 5.9 文件与目录契约

```text
download/<site>[/<author>]/<标题>_<ID>/001.jpg, 002.mp4, video.mp4
download/<site>[/<author>]/<标题>.<ext>            # yt-dlp 单视频
```

- 目录根由数据根目录决定（默认 CWD；可用 `--data-dir` / `DOWNLOADER_HOME` 覆盖），其余相对布局不变。
- 文件名清洗规则与 Python 版一致（去控制字符/路径分隔符/Windows 保留字符，截断 80）。
- `unique_path` 使用 `O_CREATE|O_EXCL` 原子占位；`--force` 写 `.part` 后 `os.Rename`。
- 重复链接默认跳过；存在同 stem 的扩展名变体也视为已下载。

## 6. 测试策略

- 全部单元测试离线：`go test ./...`。
- HTTP 行为用 `httptest.Server` 与固定 fixture。
- 小红书 `__INITIAL_STATE__`、抖音 `pace_f`、通用页面解析使用从 Python 测试中脱敏后的 HTML fixture。
- 签名移植使用现有 Python 测试向量做黄金测试；随机量（msToken/时间戳）通过可注入 RNG/时钟固定。
- 输出目录、文件名、Cookie 格式、URL 提取、装饰图判定等纯函数参数化测试。
- 可选 `-tags=integration` 做真实网络冒烟，不进 CI。
- 静态检查：`go vet ./...` + `golangci-lint run`。

## 7. CI / Release

### 7.1 `ci.yml`

- 触发：push / PR。
- 步骤：`setup-go`（读取 `go.mod`）→ `go build ./...` → `go vet ./...` → `golangci-lint` → `go test ./...`。
- 缓存 Go modules 与 build cache。

### 7.2 `release.yml` + GoReleaser

- 触发：push tag `v*`。
- `CGO_ENABLED=0` 交叉编译：
  - `linux/arm64`（Termux 主目标）
  - `linux/amd64`
  - `darwin/arm64`、`darwin/amd64`
  - `windows/amd64`
- 产物：`tar.gz` / `zip`，附 `checksums.txt`。
- 版本注入：`-ldflags "-s -w -X main.version={{.Version}} -X main.commit={{.Commit}} -X main.date={{.Date}}"`。
- Release Notes 自动生成；无需额外 secret（使用内置 `GITHUB_TOKEN`）。

### 7.3 `install_termux.sh`

- 识别 `uname -m`（aarch64/armv7l）。
- 通过 GitHub Releases API 或固定 latest 下载 URL 获取对应 tarball。
- 校验 `checksums.txt` 中的 SHA256。
- 安装到 `$PREFIX/bin/downloader`（无写权限则 `~/bin`）。
- 检查 `yt-dlp`/`ffmpeg`，缺失时提示 `pkg install yt-dlp ffmpeg`。
- 支持 `DOWNLOADER_VERSION=v0.1.0` 指定版本。
- 支持 `--data-dir DIR`：幂等写入 `DOWNLOADER_HOME=DIR` 到 `~/.bashrc`/`~/.zshrc`，让命令在任意目录可用。
- 支持 `--migrate-from OLD_DIR`：复制 `cookies/` 与 `config.json`，并在新数据目录不存在 `download/` 时为其创建指向旧目录的符号链接（避免搬运大文件）。
- 默认不移动任何数据；不传 `--data-dir` 时保持 CWD 语义。
- 打印 `downloader version` 与下一步配置提示。

## 8. 里程碑

0. **M0 指纹验证 spike**：最小 Go 程序在 Termux 上验证 utls 能通过小红书笔记页与抖音笔记页（视频详情 API 待签名移植后验证）。通过则按纯 utls 进入 M1；失败则把系统 curl 兜底加入 v1 范围。spike 代码不进入正式仓库。
1. **M1 基础骨架**：`config`/`sites`/`result`/`fsutil`/`httpclient`/`dispatch`；`run` 子命令；yt-dlp 与 generic 后端；离线测试。
2. **M2 站点解析**：xhs、douyin 图文/视频、签名移植；fixture 与黄金测试。
3. **M3 Telegram**：上传规划、转码、Bot API 客户端、bot 长轮询、CLI 直发。
4. **M4 交付**：CI、GoReleaser、`install_termux.sh`、README/AGENTS.md、迁移说明。

每个里程碑以“离线测试通过 + 可手动跑通对应入口”为完成标准。

## 9. 风险与缓解

| 风险 | 缓解 |
|---|---|
| utls 的 HTTP/2 指纹与 Chrome 不完全一致 | M0 真机 spike 先验证；失败则 v1 增加系统 curl 兜底；必要时切换 HTTP/1.1 profile |
| 抖音签名随服务端更新失效 | 移植时保留测试向量；签名模块独立，便于按上游 diff 更新 |
| yt-dlp 输出/参数变化 | 只用稳定参数（`-J`、`after_move:filepath`），失败时回显 stderr |
| Termux 无 ffmpeg | HEIC/大图/大视频降级为 document 或跳过，核心下载不受影响 |
| 相册混排/50MB 边界 | 手写 multipart，按 Bot API 文档逐项校验；上传前统一读取文件大小 |
| 通用抓取误判装饰图 | 复用现有文件名关键词 + 尺寸阈值 + `.skip` 策略 |

## 10. 构建者需要自行决定的事项

- Go 版本与 `go.mod` 具体版本。
- `utls` 的 Chrome profile 选择与 UA 常量维护方式。
- SM3 使用移植实现还是引入 `gmsm`（优先移植，减少依赖）。
- HTTP/2 具体参数调优。
- Telegram 图片压缩的质量阶梯/缩放轮次（参考 Python 版）。
- yt-dlp 播放列表的 Post 拆分粒度。
- 日志库选择（`log/slog` 或轻量自封装）。

## 11. 切换与兼容

- **行为对齐基线**：Python 仓库 commit `1a4f129`（2026-10-28）。fixtures、签名测试向量、用户可见文案都以该 commit 为准；重写期间不跟踪 Python 仓库的后续提交。
- **许可证**：新仓库整体 MIT；`internal/douyinsign/` 移植自 Apache-2.0 的 Evil0ctal/Douyin_TikTok_Download_API，相关文件头保留 Apache-2.0 声明，仓库根增加 `NOTICE`，Release 包一并附带 `LICENSE` 与 `NOTICE`。
- 用户沿用现有 `cookies/`、`config.json` 与 `download/`。
- 已下载文件按同 stem 规则识别并跳过，不产生重复副本。
- Python 仓库保留一段时间；Go 版达到功能对齐后再在 README 标记维护状态。
- 新仓库初始提交包含本设计文档、README、LICENSE（MIT）、`go.mod` 与最小骨架。
