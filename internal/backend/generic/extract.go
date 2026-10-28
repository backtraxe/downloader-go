package generic

import (
	"net/url"
	"path"
	"regexp"
	"strings"

	"github.com/PuerkitoBio/goquery"

	"github.com/backtraxe/downloader-go/internal/media"
	"github.com/backtraxe/downloader-go/internal/urlutil"
)

// Candidate 是页面上发现的一个媒体直链。
//
// Kind 是该候选的默认媒体扩展名（如 ".jpg"、".mp4"），仅用于描述与
// 调试；实际落盘扩展名由 httpclient.DownloadFile 按 URL/Content-Type
// 校正。
type Candidate struct {
	URL  string
	Kind string
}

const (
	defaultImageKind = ".jpg"
	defaultVideoKind = ".mp4"
)

var (
	// titleAuthorPattern 匹配 <title> 里的“正文 来自 作者 - 站点”尾巴。
	titleAuthorPattern = regexp.MustCompile(`正文\s*来自\s*(.+?)\s*-\s*`)

	// backgroundImagePattern 匹配行内样式中的 background-image: url(...)，
	// 兼容冒号/括号周围空白与单双引号。
	backgroundImagePattern = regexp.MustCompile(
		`(?i)background-image\s*:\s*url\(\s*["']?([^"')]+)["']?\s*\)`,
	)

	// mediaAnchorExtensions 是 <a href> 被视为媒体直链的路径后缀。
	mediaAnchorExtensions = []string{
		".jpg", ".jpeg", ".png", ".gif", ".webp", ".heic", ".heif",
		".mp4", ".mov", ".webm", ".avi", ".mkv",
	}
)

// ExtractCandidates 按 DOM 顺序扫描页面的媒体候选，去重并保持首次出现顺序。
//
// 扫描范围：
//   - img/source/video 的 data-src、data-original、ess-data、src 属性；
//   - 同一批标签的 srcset（只取第一个候选 URL，即首个空白之前的内容）；
//   - <a href> 路径以媒体后缀结尾的直链；
//   - 行内 style 里的 background-image:url(...)。
//
// 相对链接以 pageURL 为基准归一化；空值与 data: URI 被丢弃。
func ExtractCandidates(doc *goquery.Document, pageURL string) []Candidate {
	if doc == nil {
		return nil
	}

	seen := make(map[string]struct{})
	var candidates []Candidate

	add := func(raw, tag string) {
		link := urlutil.NormalizeURL(raw, pageURL)
		if link == "" || strings.HasPrefix(strings.ToLower(link), "data:") {
			return
		}
		if _, exists := seen[link]; exists {
			return
		}
		seen[link] = struct{}{}
		candidates = append(candidates, Candidate{URL: link, Kind: candidateKind(tag, link)})
	}

	// 1. 图片/视频标签的直链与懒加载属性。属性顺序固定，保证同一元素
	//    的多个候选在结果中的位置稳定。
	doc.Find("img, source, video").Each(func(_ int, tag *goquery.Selection) {
		name := goquery.NodeName(tag)
		for _, attr := range []string{"data-src", "data-original", "ess-data", "src"} {
			value, ok := tag.Attr(attr)
			if !ok {
				continue
			}
			add(value, name)
		}
		if value, ok := tag.Attr("srcset"); ok {
			// srcset 形如 "a.jpg 1x, b.jpg 2x"：首个字符串就是第一个 URL。
			if fields := strings.Fields(value); len(fields) > 0 {
				add(fields[0], name)
			}
		}
	})

	// 2. 包裹媒体直链的 A 标签。只看路径后缀，避免 ?image=foo.jpg 这类
	//    查询参数被误判。
	doc.Find("a[href]").Each(func(_ int, tag *goquery.Selection) {
		href, _ := tag.Attr("href")
		if !hasMediaPath(href) {
			return
		}
		add(href, "a")
	})

	// 3. 行内背景图。
	doc.Find("[style]").Each(func(_ int, tag *goquery.Selection) {
		style, _ := tag.Attr("style")
		for _, match := range backgroundImagePattern.FindAllStringSubmatch(style, -1) {
			if len(match) > 1 {
				add(match[1], "img")
			}
		}
	})

	return candidates
}

// ExtractTitle 返回清理过站点后缀的页面标题。
//
// 取 <title> 文本并去首尾空白；含 " - " 时保留最后一个 " - " 之前的
// 部分（站点名/品牌后缀通常在末尾）。空标题回退 "untitled"。
func ExtractTitle(doc *goquery.Document) string {
	if doc == nil {
		return "untitled"
	}
	title := strings.TrimSpace(doc.Find("title").First().Text())
	if idx := strings.LastIndex(title, " - "); idx >= 0 {
		title = strings.TrimSpace(title[:idx])
	}
	if title == "" {
		return "untitled"
	}
	return title
}

// ExtractAuthor 从“正文 来自 作者 - 站点”标题尾巴中提取作者。
//
// 未匹配时返回空串。site 保留用于接口对称与未来按站点校验尾巴；当前
// 口径下只依赖标题文本。
func ExtractAuthor(title, site string) string {
	_ = site
	match := titleAuthorPattern.FindStringSubmatch(title)
	if len(match) < 2 {
		return ""
	}
	return strings.TrimSpace(match[1])
}

// candidateKind 按标签与 URL 后缀推断候选的默认扩展名。
func candidateKind(tag, link string) string {
	if tag == "video" {
		return defaultVideoKind
	}
	if ext := strings.ToLower(media.GuessExtension(link, "", "")); ext != "" {
		return ext
	}
	return defaultImageKind
}

// hasMediaPath 判断 href 的路径部分是否以媒体扩展名结尾。
func hasMediaPath(href string) bool {
	href = strings.TrimSpace(href)
	if href == "" {
		return false
	}
	pathPart := href
	if u, err := url.Parse(href); err == nil {
		pathPart = u.Path
	}
	pathPart = strings.ToLower(pathPart)
	for _, ext := range mediaAnchorExtensions {
		if strings.HasSuffix(pathPart, ext) {
			return true
		}
	}
	return false
}

// candidateName 返回候选 URL 的远端文件名，用于下载前的装饰资源过滤。
func candidateName(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Path == "" {
		return ""
	}
	return path.Base(u.Path)
}

// isVideoExtension 判断扩展名是否属于视频容器；视频不做尺寸/体积过滤。
func isVideoExtension(ext string) bool {
	switch strings.ToLower(ext) {
	case ".mp4", ".mov", ".webm", ".avi", ".mkv":
		return true
	default:
		return false
	}
}
