# downloader-go

download-bot 的 Go 重写版：面向 Termux 的静态命令行下载器，附带可选的 Telegram 机器人。

当前处于 v0.1 开发早期，仅提供仓库骨架与 `version` 命令。

## 构建

```bash
go build -o downloader ./cmd/downloader
```

发布会通过 `-ldflags` 注入版本信息：

```bash
go build -ldflags "-X main.version=v0.1.0 -X main.commit=$(git rev-parse --short HEAD) -X main.date=$(date -u +%Y-%m-%dT%H:%M:%SZ)" -o downloader ./cmd/downloader
```

## 使用

```bash
./downloader version
./downloader --data-dir /path/to/data version
```

全局 `--data-dir` 只接受写在子命令之前。数据目录优先级：命令行 flag > `DOWNLOADER_HOME` 环境变量 > 当前目录。

## 安装

安装脚本与 Termux 一键配置将在后续版本提供。

## 许可证

MIT，见 LICENSE 与 NOTICE。
