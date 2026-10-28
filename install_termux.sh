#!/usr/bin/env bash
# downloader-go 的 Termux 安装脚本：下载对应 Release、校验 SHA256、
# 安装二进制，并按需配置数据目录或迁移旧数据。
set -euo pipefail

REPO="backtraxe/downloader-go"
BASE_URL="https://github.com/${REPO}/releases"
DATA_DIR=""
MIGRATE_FROM=""
DATA_DIR_SET=0
MIGRATE_FROM_SET=0

usage() {
  cat <<'EOF'
用法: bash install_termux.sh [选项]

选项:
  --data-dir DIR       设置数据目录，并幂等写入 ~/.bashrc 与 ~/.zshrc
                       的 DOWNLOADER_HOME。
  --migrate-from DIR   从旧数据目录复制 cookies/ 与 config.json；新数据目录
                       没有 download/ 时，将其符号链接到旧目录的 download/。
                       需与 --data-dir 一起使用。
  -h, --help           显示本帮助。

环境变量:
  DOWNLOADER_VERSION   指定要安装的版本（如 v0.1.0）；缺省安装 latest。
EOF
}

log() { printf '%s\n' "$*"; }
die() {
  printf '错误: %s\n' "$*" >&2
  exit 1
}

while [ $# -gt 0 ]; do
  case "$1" in
    --data-dir)
      [ $# -ge 2 ] || die "--data-dir 缺少参数"
      DATA_DIR="$2"
      DATA_DIR_SET=1
      shift 2
      ;;
    --data-dir=*)
      DATA_DIR="${1#*=}"
      DATA_DIR_SET=1
      shift
      ;;
    --migrate-from)
      [ $# -ge 2 ] || die "--migrate-from 缺少参数"
      MIGRATE_FROM="$2"
      MIGRATE_FROM_SET=1
      shift 2
      ;;
    --migrate-from=*)
      MIGRATE_FROM="${1#*=}"
      MIGRATE_FROM_SET=1
      shift
      ;;
    -h | --help)
      usage
      exit 0
      ;;
    *)
      die "未知参数: $1（--help 查看用法）"
      ;;
  esac
done

# 显式传入但值为空也算参数错误：空目录会被当成当前目录，后续写 rc 文件
# 时还会产生错误的 DOWNLOADER_HOME。
if [ "$DATA_DIR_SET" -eq 1 ] && [ -z "$DATA_DIR" ]; then
  die "--data-dir 不能为空。"
fi
if [ "$MIGRATE_FROM_SET" -eq 1 ] && [ -z "$MIGRATE_FROM" ]; then
  die "--migrate-from 不能为空。"
fi

# 换行会破坏 rc 文件里的单行 export，直接拒绝而不是尝试转义。
case "$DATA_DIR" in
  *$'\n'* | *$'\r'*)
    die "--data-dir 不能包含换行符。"
    ;;
esac

# Termux 的 PREFIX 形如 /data/data/com.termux/files/usr。
if [ -z "${PREFIX:-}" ]; then
  die "未检测到 PREFIX，请在 Termux 中运行本脚本。"
fi
case "$PREFIX" in
  *com.termux*) ;;
  *) die "未检测到 Termux（PREFIX=$PREFIX），请在 Termux 中运行本脚本。" ;;
esac

case "$(uname -m)" in
  aarch64 | arm64) ARCH="arm64" ;;
  *) die "不支持的架构: $(uname -m)（Release 仅提供 linux/arm64）" ;;
esac

# 迁移参数在下载前校验，避免装完二进制才发现参数不完整。
if [ -n "$MIGRATE_FROM" ]; then
  [ -n "$DATA_DIR" ] || die "--migrate-from 需要同时指定 --data-dir。"
  [ -d "$MIGRATE_FROM" ] || die "旧数据目录不存在: $MIGRATE_FROM"
fi

if [ -n "${DOWNLOADER_VERSION:-}" ]; then
  RELEASE_URL="${BASE_URL}/download/${DOWNLOADER_VERSION}"
else
  RELEASE_URL="${BASE_URL}/latest/download"
fi

fetch() {
  local url="$1" out="$2"
  if command -v curl >/dev/null 2>&1; then
    curl -fL --retry 3 --connect-timeout 15 -o "$out" "$url"
  elif command -v wget >/dev/null 2>&1; then
    wget -O "$out" "$url"
  else
    die "需要 curl 或 wget 下载 Release：pkg install curl"
  fi
}

TMP_DIR="$(mktemp -d "${TMPDIR:-$PREFIX/tmp}/dl-go-install.XXXXXX")"
trap 'rm -rf "$TMP_DIR"' EXIT

log "下载 ${RELEASE_URL}/checksums.txt ..."
fetch "${RELEASE_URL}/checksums.txt" "$TMP_DIR/checksums.txt" ||
  die "下载 checksums.txt 失败（版本或网络是否正常？）"

checksum_line="$(grep -E "_linux_${ARCH}\\.tar\\.gz\$" "$TMP_DIR/checksums.txt" | head -n 1 || true)"
[ -n "$checksum_line" ] || die "checksums.txt 中没有 linux/${ARCH} 的 tar.gz 产物。"

archive="${checksum_line##* }"
archive="${archive#\*}"
[ -n "$archive" ] || die "无法从校验文件解析归档文件名。"

log "下载 ${RELEASE_URL}/${archive} ..."
fetch "${RELEASE_URL}/${archive}" "$TMP_DIR/$archive" || die "下载 ${archive} 失败。"

log "校验 SHA256 ..."
(
  cd "$TMP_DIR"
  printf '%s\n' "$checksum_line" | sha256sum -c -
)

tar -xzf "$TMP_DIR/$archive" -C "$TMP_DIR" dl-go ||
  die "解压 ${archive} 失败。"
[ -f "$TMP_DIR/dl-go" ] || die "归档中没有 dl-go 二进制。"

if [ -d "$PREFIX/bin" ] && [ -w "$PREFIX/bin" ]; then
  install -m 0755 "$TMP_DIR/dl-go" "$PREFIX/bin/dl-go"
  INSTALL_DIR="$PREFIX/bin"
else
  mkdir -p "$HOME/bin"
  install -m 0755 "$TMP_DIR/dl-go" "$HOME/bin/dl-go"
  INSTALL_DIR="$HOME/bin"
  case ":$PATH:" in
    *":$HOME/bin:"*) ;;
    *)
      log "提示: $HOME/bin 不在 PATH 中，请把下面一行加入 shell 配置："
      log 'export PATH="$HOME/bin:$PATH"'
      ;;
  esac
fi
log "已安装到 ${INSTALL_DIR}/dl-go"

# shell_escape 把值转义为可安全放进双引号内的形式。
shell_escape() {
  local value="$1"
  value="${value//\\/\\\\}"
  value="${value//\"/\\\"}"
  value="${value//\$/\\\$}"
  value="${value//\`/\\\`}"
  printf '%s' "$value"
}

if [ -n "$DATA_DIR" ]; then
  mkdir -p "$DATA_DIR"
  DATA_DIR="$(cd "$DATA_DIR" && pwd -P)"
  rc_line="export DOWNLOADER_HOME=\"$(shell_escape "$DATA_DIR")\""
  for rc in "$HOME/.bashrc" "$HOME/.zshrc"; do
    touch "$rc"
    if grep -qxF "$rc_line" "$rc"; then
      log "$rc 已包含 DOWNLOADER_HOME，跳过。"
    else
      printf '\n# downloader-go 数据目录\n%s\n' "$rc_line" >>"$rc"
      log "已写入 $rc"
    fi
  done
  log "数据目录: $DATA_DIR（新开终端或 source 对应 rc 文件后生效）"
fi

if [ -n "$MIGRATE_FROM" ]; then
  OLD_DIR="$(cd "$MIGRATE_FROM" && pwd -P)"
  if [ "$OLD_DIR" = "$DATA_DIR" ]; then
    # 同目录迁移是幂等空操作：安装本身已经成功，不应以非零码退出。
    log "新旧数据目录相同，跳过迁移。"
  else
    mkdir -p "$DATA_DIR/cookies"
    if [ -d "$OLD_DIR/cookies" ]; then
      for src in "$OLD_DIR"/cookies/*; do
        [ -e "$src" ] || continue
        name="$(basename "$src")"
        if [ -e "$DATA_DIR/cookies/$name" ]; then
          log "跳过已存在的 cookies/$name"
        else
          cp -R "$src" "$DATA_DIR/cookies/$name"
          log "已复制 cookies/$name"
        fi
      done
    fi

    if [ -f "$OLD_DIR/config.json" ]; then
      if [ -e "$DATA_DIR/config.json" ]; then
        log "跳过已存在的 config.json"
      else
        cp "$OLD_DIR/config.json" "$DATA_DIR/config.json"
        log "已复制 config.json"
      fi
    fi

    if [ ! -e "$DATA_DIR/download" ] && [ ! -L "$DATA_DIR/download" ]; then
      if [ -d "$OLD_DIR/download" ]; then
        ln -s "$OLD_DIR/download" "$DATA_DIR/download"
        log "已创建符号链接 download/ -> $OLD_DIR/download"
      else
        log "旧目录没有 download/，跳过符号链接。"
      fi
    else
      log "新数据目录已有 download/，跳过符号链接。"
    fi
  fi
fi

missing=""
for cmd in yt-dlp ffmpeg; do
  if ! command -v "$cmd" >/dev/null 2>&1; then
    missing="$missing $cmd"
  fi
done
if [ -n "$missing" ]; then
  log "缺少外部命令:${missing}"
  log "请运行: pkg install yt-dlp ffmpeg"
else
  log "yt-dlp 与 ffmpeg 已安装。"
fi

log "安装完成，当前版本："
"$INSTALL_DIR/dl-go" version

if [ -n "$DATA_DIR" ]; then
  log "配置文件: $DATA_DIR/config.json（首次运行 dl-go bot 会生成模板）"
else
  log "配置文件: 当前目录下的 config.json（首次运行 dl-go bot 会生成模板）"
fi
log "下一步: dl-go run（或 dl-go bot，需先填写 config.json）"
