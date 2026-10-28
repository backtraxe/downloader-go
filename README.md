# downloader-go

`download-bot` 的 Go 重写版：单个静态二进制，面向 Termux，支持小红书、抖音、
通用网页与 yt-dlp 站点下载，并可选作为 Telegram 机器人把结果回传。

主流程不依赖 Python；`yt-dlp` 与 `ffmpeg` 作为外部命令按需调用。

## Termux 安装

```bash
pkg install curl tar
curl -fsSL -o install_termux.sh \
  https://raw.githubusercontent.com/backtraxe/downloader-go/main/install_termux.sh
bash install_termux.sh
```

脚本会识别 `aarch64`、下载 `linux/arm64` Release、校验 `checksums.txt` 中的
SHA256，并安装到 `$PREFIX/bin/downloader`（无写权限时安装到 `~/bin`）。

常用选项：

```bash
# 指定数据目录（写入 ~/.bashrc 与 ~/.zshrc 的 DOWNLOADER_HOME）
bash install_termux.sh --data-dir "$HOME/downloader"

# 指定版本
DOWNLOADER_VERSION=v0.1.0 bash install_termux.sh

# 从 Python 版迁移：复制 cookies/ 与 config.json，
# 新目录没有 download/ 时符号链接到旧目录的 download/
bash install_termux.sh --data-dir "$HOME/downloader" \
  --migrate-from "$HOME/old-downloader"
```

脚本最后会检查 `yt-dlp` 与 `ffmpeg`，缺失时提示
`pkg install yt-dlp ffmpeg`，并打印 `downloader version`。

## 命令

全局 `--data-dir` 必须写在子命令之前。

```bash
downloader version                    # 版本信息
downloader run [-f]                   # 从 stdin 逐行读取链接/分享文案；-f 强制重下
downloader                            # 不带子命令时等同 run
downloader bot                        # 启动 Telegram 长轮询机器人
downloader upload <path> [--caption TEXT]   # 把文件或目录回传到配置的 chat_id
downloader cookie import <site>       # 从 stdin 导入 Cookie 到 cookies/<site>.txt
downloader cookie check [sites...]    # 离线检查 Cookie 必需键
```

`bot` 与 `upload` 使用数据目录中的 `config.json`。首次运行 `bot` 时会生成模板，
填入 `telegram` 段的 `bot_token`、`chat_id`、`allowed_users` 后重新运行。

## 数据目录

解析顺序：`--data-dir` > `DOWNLOADER_HOME` 环境变量 > 当前工作目录。

```text
<data-dir>/
  config.json        Telegram 配置（不要提交）
  cookies/<site>.txt Cookie（不要提交）
  download/<site>[/<author>]/...   下载产物
```

`download/`、`cookies/` 与 `config.json` 均相对于数据目录解析。

## 外部依赖

| 命令 | 用途 | 缺失时 |
| --- | --- | --- |
| `yt-dlp` | bilibili / YouTube / Instagram / Twitter 等站点 | 返回 `failed` 并提示安装 |
| `ffmpeg` | HEIC 预览、超大视频压缩 | 降级为 document 或跳过 |

```bash
pkg install yt-dlp ffmpeg
```

## Release 平台

| 平台 | 归档 |
| --- | --- |
| linux/arm64（Termux 主目标） | `.tar.gz` |
| linux/amd64 | `.tar.gz` |
| darwin/arm64、darwin/amd64 | `.tar.gz` |
| windows/amd64 | `.zip` |

每个 Release 附带 `checksums.txt` 以及 `LICENSE`、`NOTICE`。可从
[Releases](https://github.com/backtraxe/downloader-go/releases) 手动下载。

## 开发

```bash
CGO_ENABLED=0 go build -o downloader ./cmd/downloader
CGO_ENABLED=0 go test ./...
CGO_ENABLED=0 go vet ./...
```

推送 `v*` tag 会触发 GitHub Actions 与 GoReleaser 发布多平台产物。

## 许可证

整体以 MIT 许可证发布，见 [LICENSE](LICENSE)。`internal/douyinsign/` 移植自
Apache-2.0 授权的 [Evil0ctal/Douyin_TikTok_Download_API](https://github.com/Evil0ctal/Douyin_TikTok_Download_API)，
相关文件保留原始声明；详见 [NOTICE](NOTICE)。
