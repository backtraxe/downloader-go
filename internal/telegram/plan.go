// Package telegram 规划并执行下载产物到 Telegram 的上传。
//
// 与 Python 版 telegram/upload.py 的口径一致：可预览的照片/视频按 10 项
// 一组发相册，HEIC 与超标大图先转码/压缩成 JPEG，>50MB 视频在有
// ffmpeg 时压到约 49MB，仍超限或工具缺失则跳过；其余文件按 document
// 逐个发送。所有外部命令通过 Runner 注入，测试无需联网或安装 ffmpeg。
package telegram

import (
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
)

// FileKind 是上传规划中的文件类别。
type FileKind int

const (
	// Photo 是可作为 Telegram photo 预览发送的图片。
	Photo FileKind = iota
	// Video 是可作为 Telegram video 预览发送的视频。
	Video
	// Document 是只能按文件原样发送的内容。
	Document
)

// FileInfo 描述一个待上传文件。
type FileInfo struct {
	Path string
	Size int64
	Kind FileKind
}

// Classify 按扩展名判断文件的发送类别。
//
// HEIC/HEIF 归为 Photo，由 PreparePhoto 决定能否转码；转码失败时调用方
// 降级为 document。GIF 不进入相册，按 document 原样发送。
func Classify(path string) FileKind {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg", ".png", ".webp", ".heic", ".heif":
		return Photo
	case ".mp4", ".mov", ".webm", ".mkv":
		return Video
	default:
		return Document
	}
}

// MakeTag 把文本转换成 Telegram 话题标签。
//
// 普通文字、数字与下划线保留；其他普通标点折叠为一个下划线。emoji 部分
// 转为 emoji_<码点>，组合表情的每个码点也参与编码，避免把原始表情直接
// 当作可搜索标签。结果为空时返回空串，调用方据此省略该标签。
func MakeTag(text string) string {
	var b strings.Builder
	inEmoji := false
	inSeparator := false
	for _, r := range text {
		if isEmojiRune(r) || inEmoji && (unicode.IsMark(r) || r == '‍' || r >= 0xe0000 && r <= 0xe007f) {
			if !inEmoji {
				if inSeparator {
					_ = b.WriteByte('_')
				}
				_, _ = b.WriteString("emoji_")
				inEmoji = true
			} else {
				_ = b.WriteByte('_')
			}
			_, _ = b.WriteString(strconv.FormatInt(int64(r), 16))
			inSeparator = false
			continue
		}
		valid := isTagRune(r)
		if inEmoji && valid {
			_ = b.WriteByte('_')
		}
		inEmoji = false
		if valid {
			if inSeparator {
				_ = b.WriteByte('_')
			}
			_, _ = b.WriteRune(r)
			inSeparator = false
			continue
		}
		inSeparator = b.Len() > 0
	}
	tag := strings.Trim(b.String(), "_")
	if tag == "" {
		return ""
	}
	return "#" + tag
}

// isTagRune 判断 r 是否为标签中的普通文本字符。
func isTagRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r)
}

// isEmojiRune 识别 emoji 与其他图形符号。变体选择符单独出现时也编码。
func isEmojiRune(r rune) bool {
	return unicode.IsSymbol(r) || r >= 0xfe00 && r <= 0xfe0f
}

// maxCaptionRunes 是 Telegram caption 的字符上限（按 rune 计）。
const maxCaptionRunes = 1024

// BuildCaption 生成媒体说明：先 #作者 #站点，含 emoji 时另起一行保留原昵称，
// 最后换行接标题。空标签省略；没有任何标签时只返回标题，避免出现前导换行。
// 结果超过 Telegram 的 1024 字符上限时按 rune 边界截断，不会切坏 UTF-8。
func BuildCaption(site, author, title string) string {
	tags := make([]string, 0, 2)
	if tag := MakeTag(author); tag != "" {
		tags = append(tags, tag)
	}
	if tag := MakeTag(site); tag != "" {
		tags = append(tags, tag)
	}

	lines := make([]string, 0, 3)
	if len(tags) > 0 {
		lines = append(lines, strings.Join(tags, " "))
	}
	author = strings.TrimSpace(author)
	if author != "" && strings.IndexFunc(author, isEmojiRune) >= 0 {
		lines = append(lines, "作者："+author)
	}
	if title != "" {
		lines = append(lines, title)
	}
	return truncateCaption(strings.Join(lines, "\n"))
}

// truncateCaption 按 rune 边界把 caption 截到 maxCaptionRunes。
func truncateCaption(caption string) string {
	runes := []rune(caption)
	if len(runes) <= maxCaptionRunes {
		return caption
	}
	return string(runes[:maxCaptionRunes])
}

// PlanGroups 把可进相册的 Photo/Video 按输入顺序切成每组至多 max 项。
//
// Document 不返回，调用方逐个发送。max <= 0 时使用 Telegram 的相册
// 上限 10。
func PlanGroups(files []FileInfo, max int) [][]FileInfo {
	if max <= 0 {
		max = 10
	}
	eligible := make([]FileInfo, 0, len(files))
	for _, f := range files {
		if f.Kind == Photo || f.Kind == Video {
			eligible = append(eligible, f)
		}
	}
	groups := make([][]FileInfo, 0, (len(eligible)+max-1)/max)
	for len(eligible) > 0 {
		n := max
		if n > len(eligible) {
			n = len(eligible)
		}
		groups = append(groups, eligible[:n])
		eligible = eligible[n:]
	}
	return groups
}

// kindName 返回 Bot API sendMediaGroup 使用的媒体类型名。
func kindName(kind FileKind) string {
	if kind == Video {
		return "video"
	}
	return "photo"
}
