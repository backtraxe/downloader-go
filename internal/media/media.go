// Package media 提供媒体类型推断、图片尺寸解析与装饰性资源过滤。
//
// 口径与 Python 版 core/utils.py、core/1024_downloader.py 保持一致：
// 扩展名优先取 URL 后缀白名单（防止被防盗链 HTML 响应误导），其次
// Content-Type；尺寸解析基于标准库 image 与 x/image/webp。
package media

import (
	"image"
	_ "image/gif"  // 注册 GIF 解码器
	_ "image/jpeg" // 注册 JPEG 解码器
	_ "image/png"  // 注册 PNG 解码器
	"io"
	"os"
	"regexp"
	"strings"

	_ "golang.org/x/image/webp" // 注册 WebP 解码器（标准库不含）
)

// 媒体解码只读文件头：与 Python 版读取前 32KB 的口径一致，避免为判定
// 尺寸把大视频/大图整个读进内存。
const headerReadLimit = 32 << 10

// urlExtPattern 从 URL 中提取常见媒体扩展名。后缀后必须紧跟 ?、# 或结尾，
// 避免 "a.gifx" 被误判为 gif。与 Python 版 _EXT_BY_URL 等价。
var urlExtPattern = regexp.MustCompile(`(?i)\.(jpe?g|png|gif|webp|hei[cf]|mp4|mov|webm|avi|mkv)([?#]|$)`)

// extByContentType 是 Content-Type 到扩展名的映射；HTML 不在其中，
// 因为 HTML 响应通常意味着防盗链/风控拦截而非媒体。
var extByContentType = map[string]string{
	"image/jpeg":       ".jpg",
	"image/jpg":        ".jpg",
	"image/png":        ".png",
	"image/gif":        ".gif",
	"image/webp":       ".webp",
	"image/svg+xml":    ".svg",
	"image/avif":       ".avif",
	"image/heic":       ".heic",
	"image/heif":       ".heic",
	"video/mp4":        ".mp4",
	"video/quicktime":  ".mov",
	"video/webm":       ".webm",
	"video/x-msvideo":  ".avi",
	"video/x-matroska": ".mkv",
}

// htmlContentTypes 是被防盗链/风控拦截时常返回的 HTML Content-Type。
var htmlContentTypes = map[string]struct{}{
	"text/html":             {},
	"application/xhtml+xml": {},
}

// GuessExtension 由 URL 后缀或 Content-Type 推断扩展名（含前导点）。
//
// 顺序与 Python 版一致：URL 明确扩展名优先于 Content-Type（避免被防盗链
// HTML 响应误导）；否则用 Content-Type；HTML 与未知类型都返回 defaultExt。
func GuessExtension(rawURL, contentType, defaultExt string) string {
	if rawURL != "" {
		if m := urlExtPattern.FindStringSubmatch(rawURL); m != nil {
			return "." + strings.ToLower(m[1])
		}
	}
	if ct := normalizeContentType(contentType); ct != "" && !IsHTMLContent(ct) {
		if ext, ok := extByContentType[ct]; ok {
			return ext
		}
	}
	return defaultExt
}

// IsHTMLContent 判断 Content-Type 是否为 HTML（通常意味着被拦截）。
func IsHTMLContent(contentType string) bool {
	_, ok := htmlContentTypes[normalizeContentType(contentType)]
	return ok
}

// normalizeContentType 取 Content-Type 的分号前主类型，去空白并转小写。
func normalizeContentType(contentType string) string {
	ct := contentType
	if idx := strings.IndexByte(ct, ';'); idx >= 0 {
		ct = ct[:idx]
	}
	return strings.ToLower(strings.TrimSpace(ct))
}

// DecodeDimensions 从图片数据头部解析宽高，仅供判定装饰图使用。
//
// 已注册 JPEG/PNG/GIF 与 WebP；格式不识别、数据损坏或读取失败时
// 返回 ok=false，由调用方回退到体积阈值。读取多少字节由调用方控制
// （IsDecorativeFile 只读前 32KB）。
func DecodeDimensions(r io.Reader) (width, height int, ok bool) {
	cfg, _, err := image.DecodeConfig(r)
	if err != nil {
		return 0, 0, false
	}
	return cfg.Width, cfg.Height, true
}

// decorativeKeywords 是站点装饰资源（logo/头像/二维码/表情/备案图标）
// 的文件名关键词，与 Python 版 _DECORATIVE_KEYWORDS 一致。
var decorativeKeywords = []string{
	"logo",
	"avatar",
	"qrcode",
	"qr_code",
	"emotion",
	"emoji",
	"beian",
	"icon",
}

// decorativePatterns 为每个关键词预编译词边界正则。
//
// 用词边界而非子串匹配，避免 "iconic" 命中 "icon"、"avatars" 命中
// "avatar"——这些词只是以装饰关键词开头/结尾，语义上不是装饰词。
// 分隔符取 [^\p{L}\p{N}]（等价于 Python 的 [\W_]，Unicode 语义），
// 因此中文字符两侧不算边界，与 Python 行为一致。
var decorativePatterns = func() []*regexp.Regexp {
	patterns := make([]*regexp.Regexp, 0, len(decorativeKeywords))
	for _, kw := range decorativeKeywords {
		patterns = append(patterns, regexp.MustCompile(
			`(?i)(^|[^\p{L}\p{N}])`+regexp.QuoteMeta(kw)+`([^\p{L}\p{N}]|$)`,
		))
	}
	return patterns
}()

// IsDecorativeName 按文件名判断是否为站点装饰资源（logo/头像/二维码等）。
func IsDecorativeName(name string) bool {
	for _, pattern := range decorativePatterns {
		if pattern.MatchString(name) {
			return true
		}
	}
	return false
}

// IsDecorativeFile 落盘后判定文件是否为装饰图。
//
// 尺寸可解析时按短边过滤：短边小于 minDim 判为装饰图（头像/logo 通常
// ≤200px，正文照片通常 ≥1000px）；解析失败（视频、非图片、头损坏）回退
// 体积阈值：文件小于 minBytes 判为装饰图。文件不存在或无法打开时返回
// false——无法判定时不误删。
func IsDecorativeFile(path string, minDim int, minBytes int64) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	if width, height, ok := DecodeDimensions(io.LimitReader(f, headerReadLimit)); ok {
		return min(width, height) < minDim
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Size() < minBytes
}
