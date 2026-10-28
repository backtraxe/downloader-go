// Package douyin 实现抖音图文笔记与视频的后端解析下载。
//
// 口径沿用 Python 版 core/douyin_downloader.py：
//   - 图文笔记带登录 Cookie 请求页面，从 React Flight 的 pace_f 数据块
//     （主路径）或 RENDER_DATA/_ROUTER_DATA（旧版）提取 aweme detail；
//     两者都失败时（调用方）改走签名 detail API 兜底；
//   - 视频走签名 detail API，签名三件套为 a_bogus + msToken +
//     x-secsdk-web-signature；
//   - 图片优先选无水印最高档，实况图视频与笔记级视频一并下载。
//
// 本文件只负责纯解析：URL 判定、反爬挑战识别与页面/详情 JSON 到 Aweme
// 的转换，不发起任何网络请求。
package douyin

import (
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/backtraxe/downloader-go/internal/media"
)

// Challenge 是页面被反爬拦截的形态。
type Challenge int

const (
	// NoChallenge 表示页面正常。
	NoChallenge Challenge = iota
	// JSVMChallenge 表示空 body + _$jsvmprt 混淆脚本的挑战页。
	JSVMChallenge
	// CaptchaChallenge 表示 title 为“验证码中间页”的限流页。
	CaptchaChallenge
)

// Image 是笔记中的一张图片，Ext 为带前导点的落盘扩展名（如 ".jpg"）。
type Image struct {
	URL string
	Ext string
}

// Pair 是一张实况图（图片 + 配套视频直链）。
type Pair struct {
	ImageURL string
	VideoURL string
}

// Aweme 是从页面或 detail API 提取出的作品信息。
type Aweme struct {
	ID       string
	Desc     string
	Author   string
	Images   []Image
	VideoURL string
	// LivePairs 与 Images 中的条目按 ImageURL 对应，实况视频沿用图片序号。
	LivePairs []Pair
}

const (
	jsvmMarker        = "_$jsvmprt"
	captchaMarker     = "<title>验证码中间页</title>"
	watermarkMarker   = "dy-water"
	playWatermarkMark = "playwm"
)

var (
	// noteURLPattern 与 Python 的 _NOTE_URL_RE 一致：/note/<数字ID>。
	noteURLPattern = regexp.MustCompile(`(?i)https?://(?:www\.)?douyin\.com/note/(\d+)`)
	// videoURLPattern 与 Python 的 _VIDEO_URL_RE 一致：/video/<数字ID>。
	videoURLPattern = regexp.MustCompile(`(?i)https?://(?:www\.)?douyin\.com/video/(\d+)`)

	// pathNoteIDPattern / pathVideoIDPattern 仅按路径提取 ID，用于页面地址
	// 不是 douyin.com 的场景（离线测试、短链展开后的代理地址）。公开的
	// IsNoteURL/IsVideoURL 仍严格要求 douyin.com 域名。
	pathNoteIDPattern  = regexp.MustCompile(`(?i)/note/(\d+)`)
	pathVideoIDPattern = regexp.MustCompile(`(?i)/video/(\d+)`)

	// paceFPattern 匹配 self.__pace_f.push([1,"<chunk>"])，捕获 JS 字符串
	// 字面量内容（不含外层引号）。
	paceFPattern = regexp.MustCompile(`(?s)self\.__pace_f\.push\(\[1,"((?:[^"\\]|\\.)*)"\]\)`)
	// renderDataPattern 匹配 <script id="RENDER_DATA">URL 编码的 JSON</script>。
	renderDataPattern = regexp.MustCompile(`(?s)<script[^>]+id=["']RENDER_DATA["'][^>]*>(.*?)</script>`)
	// routerDataPattern 匹配 window._ROUTER_DATA = {...}</script>。
	routerDataPattern = regexp.MustCompile(`(?s)window\._ROUTER_DATA\s*=\s*(\{.*?\})\s*</script>`)

	// playPriorityKeys 是查找播放直链的优先键，顺序与 Python 一致。
	playPriorityKeys = []string{
		"play_addr",
		"download_addr",
		"playAddr",
		"playAddrH265",
		"play_addr_h265",
		"bitRate",
		"bitRateList",
		"playApi",
	}
)

// IsNoteURL 判断 URL 是否为抖音图文笔记页（/note/<数字ID>）。
func IsNoteURL(rawURL string) bool {
	return rawURL != "" && noteURLPattern.MatchString(rawURL)
}

// IsVideoURL 判断 URL 是否为抖音视频页（/video/<数字ID>）。
func IsVideoURL(rawURL string) bool {
	return rawURL != "" && videoURLPattern.MatchString(rawURL)
}

// DetectChallenge 识别抖音反爬拦截页。
//
// 优先判定 jsvm 挑战页（空 body + _$jsvmprt），其次按 title 判定验证码
// 中间页——正常笔记页的防盗脚本里也会出现 sec_sdk_build/captcha 路径，
// 不能用它们匹配。
func DetectChallenge(body string) Challenge {
	if body == "" {
		return NoChallenge
	}
	if strings.Contains(body, jsvmMarker) {
		return JSVMChallenge
	}
	if strings.Contains(body, captchaMarker) {
		return CaptchaChallenge
	}
	return NoChallenge
}

// ParsePaceF 从页面 HTML 提取笔记详情。
//
// 主路径扫 self.__pace_f.push([1,"..."]) 流式数据块：chunk 是 JSON 转义
// 两层的 JS 字符串，解开后是 Flight 行数据，其中含
// `"aweme":{"statusCode":0,"detail":{...}}` 包装段。pace_f 无结果时
// 回退 RENDER_DATA / _ROUTER_DATA 内嵌 JSON，递归定位 aweme detail
// （兼容 camelCase 与 snake_case）。
func ParsePaceF(html string) (*Aweme, error) {
	if html == "" {
		return nil, errors.New("空页面")
	}

	for _, match := range paceFPattern.FindAllStringSubmatch(html, -1) {
		if len(match) < 2 {
			continue
		}
		// 捕获组是 JS 字符串字面量的内容，包一层引号交给 JSON 解码恢复转义。
		var payload string
		if err := json.Unmarshal([]byte(`"`+match[1]+`"`), &payload); err != nil {
			continue
		}
		if detail, ok := awemeDetailFromFlight(payload); ok {
			return awemeFromDetail(detail), nil
		}
	}

	if data, ok := parseRenderData(html); ok {
		if detail := findAwemeDetail(data); detail != nil {
			return awemeFromDetail(detail), nil
		}
	}
	return nil, errors.New("页面数据中未找到笔记详情")
}

// ParseDetailJSON 解析 detail API 响应体为 Aweme。
//
// 同时接受完整响应（{"status_code":0,"aweme_detail":{...}}）、包装后的
// detail（{"detail":{...}}）与裸 detail 对象，字段兼容 camelCase 与
// snake_case。
func ParseDetailJSON(body []byte) (*Aweme, error) {
	var top map[string]any
	if err := decodeJSON(body, &top); err != nil {
		return nil, err
	}
	detail := top
	if nested, ok := top["aweme_detail"].(map[string]any); ok {
		detail = nested
	} else if nested, ok := top["detail"].(map[string]any); ok {
		detail = nested
	}
	aweme := awemeFromDetail(detail)
	if aweme.ID == "" && len(aweme.Images) == 0 && aweme.VideoURL == "" {
		return nil, errors.New("详情数据中没有作品信息")
	}
	return aweme, nil
}

// awemeDetailFromFlight 在一条 Flight 行数据中定位 "aweme":{...}。
func awemeDetailFromFlight(payload string) (map[string]any, bool) {
	const marker = `"aweme":{`
	idx := strings.Index(payload, marker)
	if idx < 0 {
		return nil, false
	}
	start := idx + len(`"aweme":`)
	dec := json.NewDecoder(strings.NewReader(payload[start:]))
	dec.UseNumber()
	var obj any
	if err := dec.Decode(&obj); err != nil {
		return nil, false
	}
	wrapper, ok := obj.(map[string]any)
	if !ok {
		return nil, false
	}
	if detail, ok := wrapper["detail"].(map[string]any); ok {
		return detail, true
	}
	if firstString(wrapper, "aweme_id", "awemeId") != "" {
		return wrapper, true
	}
	return nil, false
}

// parseRenderData 提取 RENDER_DATA（URL 编码 JSON）或 _ROUTER_DATA 内嵌数据。
func parseRenderData(html string) (any, bool) {
	if match := renderDataPattern.FindStringSubmatch(html); len(match) >= 2 {
		if decoded, err := url.PathUnescape(match[1]); err == nil {
			var data any
			if decodeJSON([]byte(decoded), &data) == nil {
				return data, true
			}
		}
	}
	if match := routerDataPattern.FindStringSubmatch(html); len(match) >= 2 {
		var data any
		if decodeJSON([]byte(match[1]), &data) == nil {
			return data, true
		}
	}
	return nil, false
}

// findAwemeDetail 递归定位“长得像 aweme detail”的字典：有 aweme_id/
// awemeId 且带 images 或 video。RENDER_DATA 的路径随版本变动，按特征找
// 比固定路径更抗压。
func findAwemeDetail(data any) map[string]any {
	stack := []any{data}
	for len(stack) > 0 {
		node := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		switch value := node.(type) {
		case map[string]any:
			if firstString(value, "aweme_id", "awemeId") != "" {
				if _, hasImages := value["images"]; hasImages {
					return value
				}
				if _, hasVideo := value["video"]; hasVideo {
					return value
				}
			}
			for _, child := range value {
				stack = append(stack, child)
			}
		case []any:
			stack = append(stack, value...)
		}
	}
	return nil
}

// awemeFromDetail 把 aweme detail 字典转成 Aweme。
func awemeFromDetail(detail map[string]any) *Aweme {
	if detail == nil {
		return &Aweme{}
	}
	aweme := &Aweme{
		ID:     firstString(detail, "aweme_id", "awemeId"),
		Desc:   firstString(detail, "desc"),
		Author: extractAuthor(detail),
	}
	images, _ := detail["images"].([]any)
	for _, raw := range images {
		img, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		imageURL := extractImageURL(img)
		ext := ".jpg"
		if imageURL != "" {
			ext = media.GuessExtension(imageURL, "", ".jpg")
		}
		aweme.Images = append(aweme.Images, Image{URL: imageURL, Ext: ext})
		if live := extractLivePhotoURL(img); live != "" {
			aweme.LivePairs = append(aweme.LivePairs, Pair{ImageURL: imageURL, VideoURL: live})
		}
	}
	if video, ok := detail["video"].(map[string]any); ok {
		aweme.VideoURL = firstPlayURL(video)
	}
	return aweme
}

// extractAuthor 取作者昵称：authorInfo（新版）或 author（旧版），
// nickname / uniqueId / unique_id 依次兜底。
func extractAuthor(detail map[string]any) string {
	author, _ := detail["authorInfo"].(map[string]any)
	if author == nil {
		author, _ = detail["author"].(map[string]any)
	}
	return firstString(author, "nickname", "uniqueId", "unique_id")
}

// extractImageURL 从单张图片对象取直链，优先无水印最高档。
//
// downloadUrlList 实际是 ~tplv-dy-water-* 水印流水线，urlList 才是
// 无水印版；含 dy-water 的条目一律跳过。无水印候选里 jpeg 优先于 webp，
// 同格式时 downloadUrlList 优先于 urlList；全部含水印时才退回首个合法
// URL（宁可带水印也不漏图）。
func extractImageURL(img map[string]any) string {
	var ordered []string
	for _, names := range [][2]string{
		{"downloadUrlList", "download_url_list"},
		{"urlList", "url_list"},
	} {
		candidates := listField(img, names[0])
		if len(candidates) == 0 {
			candidates = listField(img, names[1])
		}
		ordered = append(ordered, candidates...)
	}

	valid := make([]string, 0, len(ordered))
	for _, candidate := range ordered {
		if isMediaURL(candidate) {
			valid = append(valid, candidate)
		}
	}
	if len(valid) == 0 {
		return ""
	}

	noWatermark := make([]string, 0, len(valid))
	for _, candidate := range valid {
		if !strings.Contains(candidate, watermarkMarker) {
			noWatermark = append(noWatermark, candidate)
		}
	}
	for _, candidate := range noWatermark {
		if isJPEG(candidate) {
			return candidate
		}
	}
	if len(noWatermark) > 0 {
		return noWatermark[0]
	}
	return valid[0]
}

// extractLivePhotoURL 取实况图挂载的视频直链。
func extractLivePhotoURL(img map[string]any) string {
	video, _ := img["video"].(map[string]any)
	return firstPlayURL(video)
}

// firstPlayURL 在视频字典中按优先级找首个可播放直链。
func firstPlayURL(node map[string]any) string {
	if node == nil {
		return ""
	}
	for _, key := range playPriorityKeys {
		sub, ok := node[key]
		if !ok || sub == nil {
			continue
		}
		if found, ok := firstURL(sub); ok {
			return found
		}
	}
	if found, ok := firstURL(node); ok {
		return found
	}
	return ""
}

// firstURL 递归找首个媒体直链；cover 键是图片不是播放地址，一律跳过。
func firstURL(node any) (string, bool) {
	switch value := node.(type) {
	case string:
		if isMediaURL(value) {
			return value, true
		}
	case []any:
		for _, item := range value {
			if found, ok := firstURL(item); ok {
				return found, true
			}
		}
	case map[string]any:
		keys := make([]string, 0, len(value))
		for key := range value {
			if strings.HasPrefix(key, "cover") {
				continue
			}
			keys = append(keys, key)
		}
		// map 遍历无序，排序保证同一输入每次选到同一候选。
		sort.Strings(keys)
		for _, key := range keys {
			if found, ok := firstURL(value[key]); ok {
				return found, true
			}
		}
	}
	return "", false
}

// videoPlayURLs 返回视频直链候选：优先 play_addr/playAddr 的 url_list，
// 其次 bit_rate 首个有效码率档；含 playwm 的水印地址一律剔除。
func videoPlayURLs(video map[string]any) []string {
	if video == nil {
		return nil
	}
	clean := func(urls []string) []string {
		kept := make([]string, 0, len(urls))
		for _, candidate := range urls {
			if candidate != "" && !strings.Contains(candidate, playWatermarkMark) {
				kept = append(kept, candidate)
			}
		}
		return kept
	}
	for _, key := range []string{"play_addr", "playAddr"} {
		if urls := clean(playAddrURLs(video[key])); len(urls) > 0 {
			return urls
		}
	}
	for _, tiersKey := range []string{"bit_rate", "bitRate"} {
		tiers, _ := video[tiersKey].([]any)
		for _, raw := range tiers {
			tier, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			for _, key := range []string{"play_addr", "playAddr"} {
				if urls := clean(playAddrURLs(tier[key])); len(urls) > 0 {
					return urls
				}
			}
		}
	}
	return nil
}

// playAddrURLs 从 play_addr/playAddr 节点取 url_list/urlList。
func playAddrURLs(node any) []string {
	addr, ok := node.(map[string]any)
	if !ok {
		return stringList(node)
	}
	urls := listField(addr, "url_list")
	if len(urls) == 0 {
		urls = listField(addr, "urlList")
	}
	return urls
}

// decodeJSON 用 UseNumber 解码，避免长数字 ID 丢精度。
func decodeJSON(body []byte, target any) error {
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.UseNumber()
	return dec.Decode(target)
}

// firstString 返回键列表中首个非空字符串值。
func firstString(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if raw, ok := values[key]; ok {
			if text := valueString(raw); text != "" {
				return text
			}
		}
	}
	return ""
}

// valueString 把 JSON 标量转成字符串。
func valueString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	case float64:
		if typed == float64(int64(typed)) {
			return strconv.FormatInt(int64(typed), 10)
		}
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	default:
		return ""
	}
}

// listField 取字典中键对应的字符串列表。
func listField(values map[string]any, key string) []string {
	raw, ok := values[key]
	if !ok {
		return nil
	}
	return stringList(raw)
}

// stringList 把 []any/[]string/单字符串归一成字符串列表。
func stringList(raw any) []string {
	switch typed := raw.(type) {
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok && text != "" {
				out = append(out, text)
			}
		}
		return out
	case []string:
		return typed
	case string:
		if typed == "" {
			return nil
		}
		return []string{typed}
	default:
		return nil
	}
}

// isMediaURL 判断字符串是否为可下载的媒体直链。
func isMediaURL(rawURL string) bool {
	return strings.HasPrefix(rawURL, "http://") ||
		strings.HasPrefix(rawURL, "https://") ||
		strings.HasPrefix(rawURL, "//")
}

// isJPEG 判断 URL 路径是否为 jpeg/jpg（忽略查询串）。
func isJPEG(rawURL string) bool {
	path := rawURL
	if idx := strings.IndexByte(path, '?'); idx >= 0 {
		path = path[:idx]
	}
	path = strings.ToLower(path)
	return strings.Contains(path, ".jpeg") || strings.Contains(path, ".jpg")
}
