// Package fsutil 提供下载后端共用的文件系统工具：文件名清洗、下载目录
// 构造、原子去重、断点残留识别与 URL 数字 ID 提取。
//
// 口径与 Python 版 core/utils.py 保持一致，避免各后端各写一份导致产物
// 命名不一致。所有函数均为纯函数或只做最小的原子文件操作，不依赖配置。
package fsutil

import (
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// defaultNameMaxLen 与 Python 版 sanitize_filename 的默认 max_len 一致。
const defaultNameMaxLen = 80

// SanitizeFilename 清洗文件名：剔除控制字符与跨平台非法字符，去除首尾
// 空白；空串/纯空白返回 defaultName；超过 maxLen 个字符（rune）时截断。
//
// 保留中文等非 ASCII 字符。截断按 rune 而非字节，避免切断 UTF-8 序列；
// maxLen <= 0 表示不截断。path 分隔符被直接删除而非替换，防止来源不可信
// 的标题/作者名越层（目录穿越）。
func SanitizeFilename(name, defaultName string, maxLen int) string {
	cleaned := strings.TrimSpace(strings.Map(func(r rune) rune {
		if isIllegalFilenameRune(r) {
			return -1
		}
		return r
	}, name))
	if cleaned == "" {
		return defaultName
	}
	if maxLen > 0 {
		if runes := []rune(cleaned); len(runes) > maxLen {
			cleaned = string(runes[:maxLen])
		}
	}
	return cleaned
}

// isIllegalFilenameRune 判断字符是否为文件名中必须剔除的字符：
// 控制字符、路径分隔符与 Windows 保留字符。
func isIllegalFilenameRune(r rune) bool {
	if r <= 0x1f || r == 0x7f {
		return true
	}
	return strings.ContainsRune(`\/*?:"<>|`, r)
}

// BuildDownloadDir 构造按站点归类的下载目录：dataDir/site[/author]/title。
//
// 作者非空白时才多一层作者目录，避免产生 unknown/ 占位目录；作者与标题
// 均来自网页、不可信，统一经 SanitizeFilename 清洗。空标题兜底 "untitled"。
// 返回相对或绝对路径（取决于 dataDir），由调用方负责创建目录。
func BuildDownloadDir(dataDir, site, title, author string) string {
	parts := []string{dataDir, site}
	if strings.TrimSpace(author) != "" {
		parts = append(parts, SanitizeFilename(author, "unknown", defaultNameMaxLen))
	}
	parts = append(parts, SanitizeFilename(title, "untitled", defaultNameMaxLen))
	return filepath.Join(parts...)
}

// UniquePath 对给定路径做原子去重：已存在时依次尝试 stem_1.ext、
// stem_2.ext……返回的路径已被独占创建（空占位文件），调用方直接写入即可。
//
// 通过 O_CREATE|O_EXCL 保证并发下不会撞名、绝不覆盖已有文件。父目录不
// 存在等非“已存在”错误原样返回。
func UniquePath(path string) (string, error) {
	created, err := createExclusive(path)
	if err == nil {
		return created, nil
	}
	if !errors.Is(err, fs.ErrExist) {
		return "", err
	}

	stem, ext := splitExt(path)
	for idx := 1; ; idx++ {
		candidate := fmt.Sprintf("%s_%d%s", stem, idx, ext)
		created, err = createExclusive(candidate)
		if err == nil {
			return created, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", err
		}
	}
}

// createExclusive 以 O_EXCL 创建空占位文件，路径已存在时返回 fs.ErrExist。
func createExclusive(path string) (string, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return path, nil
}

// splitExt 按最后一个扩展名拆分路径（与 Python os.path.splitext 口径一致）。
// ".hidden" 这类整体以点开头的文件名视为无扩展名，避免生成 ".hidden_1" 时
// 把点当成扩展名起点。注意 filepath.Ext 对 "/a/.hidden" 返回 ".hidden"，
// 因此用 Base 比较判断。
func splitExt(path string) (stem, ext string) {
	ext = filepath.Ext(path)
	if ext == "" || filepath.Base(path) == ext {
		return path, ""
	}
	return strings.TrimSuffix(path, ext), ext
}

// ExistingByStem 在 path 所在目录查找同主文件名（stem）的任意扩展名文件，
// 命中即返回该路径。用于“重复链接已存在即跳过”：下载前不知道最终扩展名，
// 按 stem 匹配可跨 .jpg/.webp 等变体识别同一张图。
//
// glob 模式为 stem.*；.part/.ytdl/.part-<n> 等下载残留不算已完成文件。
// 目录与无法 stat 的条目跳过；glob 模式非法（文件名含 [ 等元字符）时返回 false。
func ExistingByStem(path string) (string, bool) {
	stem, _ := splitExt(path)
	matches, err := filepath.Glob(stem + ".*")
	if err != nil {
		return "", false
	}
	for _, match := range matches {
		if IsPartialDownload(filepath.Base(match)) {
			continue
		}
		info, err := os.Stat(match)
		if err != nil || info.IsDir() {
			continue
		}
		return match, true
	}
	return "", false
}

// IsPartialDownload 判断文件名是否为下载中断的残留临时文件。
//
// yt-dlp 落 .part / .part-Frag<n> / .ytdl，各后端 force 覆盖走 .part 临时
// 文件——中断时残留的不是完整媒体，上传/回传与幂等判断都应跳过。
func IsPartialDownload(name string) bool {
	return strings.HasSuffix(name, ".part") ||
		strings.HasSuffix(name, ".ytdl") ||
		strings.Contains(name, ".part-")
}

// ExtractURLNumericID 从 URL 路径末段提取纯数字 ID（如酷安 /feed/74208176）。
//
// 末段为至少 5 位纯数字才采信：位数太短多为页码/日期片段，含其他字符则
// 不是作品 ID。不满足时返回空串，调用方据此决定是否在目录名追加 _<id>。
// 路径取 EscapedPath（不做百分号解码），与 Python urlparse 的口径一致。
func ExtractURLNumericID(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	path := strings.TrimRight(u.EscapedPath(), "/")
	last := path[strings.LastIndex(path, "/")+1:]
	if len(last) < 5 {
		return ""
	}
	for _, r := range last {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return last
}
