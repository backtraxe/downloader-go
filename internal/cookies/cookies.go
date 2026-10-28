// Package cookies 负责 Cookie 文本的格式识别、键值解析、格式转换、导入与
// 离线体检。
//
// 支持的输入形态与 Python 版 core/tools/cookie_import.py、cookie_check.py
// 保持一致：
//   - Header：浏览器复制的 `Cookie:` 请求头整段（行首标签可有可无）；
//   - Netscape：yt-dlp 使用的 7 列 Tab 文件；
//   - JSONCookies：Cookie-Editor 导出的 JSON（顶层数组、{"cookies":[...]}
//     包裹或 name → value 对象）。
//
// 解析与体检只关心键名，绝不打印、记录或返回 Cookie 值以外的调试信息；
// 导入写出的文件权限固定 0600。
package cookies

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Format 标识 Cookie 文本的三种输入形态。
type Format int

const (
	// Header 是 `name=value; name=value` 形式的 Cookie 请求头整段。
	Header Format = iota
	// Netscape 是 yt-dlp 使用的 7 列 Tab 分隔文件。
	Netscape
	// JSONCookies 是 Cookie-Editor 一类扩展导出的 JSON 对象。
	JSONCookies
)

const (
	// netscapeExpiry 是 Netscape 行的固定过期时间（4070908800，约 2098 年），
	// 与 Python 版 core/utils.py 的 _NETSCAPE_EXPIRE 一致：会话 Cookie 没有
	// 真实过期时间，用远期固定值兜底。
	netscapeExpiry = "4070908800"
	// cookieLabel 是浏览器复制 Cookie 时可能带上的行首标签。
	cookieLabel = "cookie:"
	// httpOnlyPrefix 是 Netscape 文件里 HttpOnly cookie 的行首标记。
	// 浏览器与 yt-dlp 导出会写成 `#HttpOnly_.example.com\tTRUE\t...`，
	// 去掉该前缀后仍是标准 7 列数据行，不能当注释丢弃。
	httpOnlyPrefix = "#HttpOnly_"
	// xhsA1MinLen 是小红书 a1 的足量长度下限（生态口径正常约 52 位）。
	xhsA1MinLen = 30
)

// xhsCookieDomains 是导入小红书 JSON 时保留的 cookie 域。rednote.com 是
// 国际版域名，与 xiaohongshu.com 同引擎、同 Cookie。
var xhsCookieDomains = []string{"xiaohongshu.com", "rednote.com"}

// netscapeSites 是 cookies 文件需写成 Netscape 格式的站点及其 cookie 域；
// yt-dlp 只认 Netscape 文件，其余站点写请求头。
var netscapeSites = map[string]string{
	"douyin":    ".douyin.com",
	"bilibili":  ".bilibili.com",
	"youtube":   ".youtube.com",
	"instagram": ".instagram.com",
	"twitter":   ".x.com",
}

// jsonEntry 是 Cookie-Editor 一类扩展导出的单个 cookie 对象。
type jsonEntry struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Domain string `json:"domain"`
}

// Detect 识别 Cookie 文本格式。
//
// 判定顺序：先尝试 JSON（顶层数组、顶层对象、{"cookies":[...]} 包裹都算
// JSONCookies，Cookie-Editor 默认导出是数组）；否则任一非注释行按 Tab 切分
// 后有至少 7 个字段即 Netscape；其余一律按 Header。空文本视为 Header，由
// ParseMap 返回空映射。
func Detect(text string) Format {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return Header
	}
	if _, _, ok := parseJSON(trimmed); ok {
		return JSONCookies
	}
	for _, line := range nonCommentLines(trimmed) {
		if len(strings.Split(line, "\t")) >= 7 {
			return Netscape
		}
	}
	return Header
}

// parseJSON 识别并解析三种 JSON 形态：顶层数组、{"cookies":[...]} 包裹与
// 顶层 name → value 对象。数组形态返回 entries（空数组返回非 nil 空切片），
// 对象映射形态返回 pairs；ok 为 false 表示输入不是合法 JSON。
func parseJSON(text string) (map[string]string, []jsonEntry, bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || (trimmed[0] != '{' && trimmed[0] != '[') {
		return nil, nil, false
	}
	if !json.Valid([]byte(trimmed)) {
		return nil, nil, false
	}

	if trimmed[0] == '[' {
		var entries []jsonEntry
		if err := json.Unmarshal([]byte(trimmed), &entries); err != nil {
			// 合法 JSON 数组但元素不是 cookie 对象：识别为 JSON，取不到键值。
			entries = []jsonEntry{}
		}
		return nil, entries, true
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &top); err != nil {
		return nil, nil, true
	}
	if raw, exists := top["cookies"]; exists {
		var entries []jsonEntry
		if err := json.Unmarshal(raw, &entries); err == nil {
			return nil, entries, true
		}
	}

	pairs := make(map[string]string)
	for name, raw := range top {
		var value string
		if err := json.Unmarshal(raw, &value); err == nil {
			pairs[name] = value
		}
	}
	return pairs, nil, true
}

// ParseMap 把任意格式的 Cookie 文本解析成 name → value 映射，同时返回识别
// 出的格式。同名键以首次出现为准（请求头语义），空名与畸形片段忽略。
//
// Netscape 行取第 6 列（name）与第 7 列（value）；JSON 数组取每个元素的
// name/value（同名首个生效，空值保留为空串）；Header 先按行归一成
// `name=value; ...` 再按分号切分。空输入或纯注释输入返回空映射而非错误。
func ParseMap(text string) (map[string]string, Format, error) {
	format := Detect(text)
	pairs := make(map[string]string)

	switch format {
	case JSONCookies:
		jsonPairs, entries, ok := parseJSON(text)
		if !ok {
			return nil, format, errors.New("解析 JSON Cookie: 无法识别 JSON 结构")
		}
		if entries != nil {
			pairs = make(map[string]string)
			for _, entry := range entries {
				addPair(pairs, entry.Name, entry.Value)
			}
			return pairs, format, nil
		}
		return jsonPairs, format, nil
	case Netscape:
		for _, line := range nonCommentLines(text) {
			fields := strings.Split(line, "\t")
			if len(fields) < 7 {
				continue
			}
			addPair(pairs, fields[5], fields[6])
		}
	default:
		// ToHeader 已完成行归一、注释剔除与行首标签剥离，这里只需按分号切分。
		for _, piece := range strings.Split(ToHeader(text), ";") {
			name, value, ok := splitPair(piece)
			if !ok {
				continue
			}
			if _, exists := pairs[name]; !exists {
				pairs[name] = value
			}
		}
	}

	return pairs, format, nil
}

// ToHeader 把 Cookie 文本转成 `Cookie:` 请求头内容。
//
// 注释行与空行忽略；Netscape 行取 name/value 两列重组成 name=value；其余行
// 原样保留（多行输入用 "; " 连接）。行首的 `Cookie:` 标签会被剥离——它是
// 传输语法，不是 cookie 名。
func ToHeader(text string) string {
	var pairs []string
	for _, line := range nonCommentLines(stripCookieLabel(text)) {
		fields := strings.Split(line, "\t")
		if len(fields) >= 7 {
			name := strings.TrimSpace(fields[5])
			if name == "" {
				continue
			}
			pairs = append(pairs, name+"="+strings.TrimSpace(fields[6]))
			continue
		}
		pairs = append(pairs, line)
	}
	return strings.Join(pairs, "; ")
}

// ToNetscape 把 Cookie 文本转成 yt-dlp 可读的 Netscape 文件内容。
//
// 固定输出两行头注释，随后每个 cookie 一行 7 列：
// domain / TRUE / / / TRUE / 4070908800 / name / value。domain 由调用方按
// 站点给出（如 ".douyin.com"）；结尾带换行，与 Python 版一致。
func ToNetscape(text, domain string) string {
	var b strings.Builder
	// strings.Builder 的写入永远不返回错误；显式赋值以满足 errcheck。
	_, _ = b.WriteString("# Netscape HTTP Cookie File\n")
	_, _ = b.WriteString("# This is generated by downloader-bot\n")
	for _, piece := range strings.Split(ToHeader(text), ";") {
		name, value, ok := splitPair(piece)
		if !ok {
			continue
		}
		_, _ = b.WriteString(domain)
		_, _ = b.WriteString("\tTRUE\t/\tTRUE\t")
		_, _ = b.WriteString(netscapeExpiry)
		_ = b.WriteByte('\t')
		_, _ = b.WriteString(name)
		_ = b.WriteByte('\t')
		_, _ = b.WriteString(value)
		_ = b.WriteByte('\n')
	}
	return b.String()
}

// Import 从 r 读取完整 Cookie 文本，按站点所需格式标准化后写入
// <dataDir>/cookies/<site>.txt（权限 0600），返回文件路径。
//
// douyin/bilibili/youtube/instagram/twitter 写 Netscape（yt-dlp 可读），
// xiaohongshu 写请求头并只保留 domain 含 xiaohongshu.com 的 JSON 条目，未知
// 站点保留请求头。Netscape 输入写 Netscape 站点时原样保存。空输入返回
// 错误。函数不打印、不记录任何 Cookie 值。
func Import(dataDir, site string, r io.Reader) (string, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return "", fmt.Errorf("读取 Cookie 输入: %w", err)
	}
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return "", errors.New("输入为空：请确认已复制完整 Cookie")
	}

	format := Detect(text)
	var content string
	switch site {
	case "xiaohongshu":
		content = xiaohongshuContent(text, format)
	default:
		if domain, ok := netscapeSites[site]; ok {
			content = netscapeContent(text, format, domain)
		} else {
			content = headerContent(text, format)
		}
	}

	path := filepath.Join(dataDir, "cookies", site+".txt")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("创建 Cookie 目录: %w", err)
	}
	if err := os.WriteFile(path, []byte(content+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("写入 Cookie %s: %w", path, err)
	}
	// WriteFile 不会收紧已存在文件的权限，显式 chmod 保证旧文件也是 0600。
	if err := os.Chmod(path, 0o600); err != nil {
		return "", fmt.Errorf("设置 Cookie 权限 %s: %w", path, err)
	}
	return path, nil
}

// Issue 描述一项 Cookie 体检缺口。
//
// Key 是缺失的键名（任一候选即满足时用斜杠连接，如 "UIFID/UIFID_TEMP"）；
// Message 是简短中文修复提示，绝不包含 Cookie 值；Missing 表示该键缺失。
type Issue struct {
	Key     string
	Message string
	Missing bool
}

// Check 按站点已知规则体检 Cookie 文本，返回缺口列表；无缺口返回 nil。
//
// 只检查键名与值是否非空，不打印、不回显值。空值视为缺失。抖音要求
// UIFID/UIFID_TEMP 与 ttwid/sessionid 两组门禁至少其一非空；小红书要求
// a1 非空且长度 >=30，webId/web_session 至少其一非空。其他站点没有已知
// 必需键，一律通过。
func Check(text, site string) []Issue {
	pairs, _, err := ParseMap(text)
	if err != nil {
		pairs = map[string]string{}
	}

	switch site {
	case "douyin":
		var issues []Issue
		if !hasNonEmpty(pairs, "UIFID", "UIFID_TEMP") {
			issues = append(issues, Issue{
				Key:     "UIFID/UIFID_TEMP",
				Message: "缺少 UIFID（或 UIFID_TEMP），抖音签名会被风控拦截，请重新完整导出 Cookie。",
				Missing: true,
			})
		}
		if !hasNonEmpty(pairs, "ttwid", "sessionid") {
			issues = append(issues, Issue{
				Key:     "ttwid/sessionid",
				Message: "缺少 ttwid 或 sessionid 登录态，接口会返回 200 空响应，请刷新 Cookie。",
				Missing: true,
			})
		}
		return issues
	case "xiaohongshu":
		var issues []Issue
		if len(strings.TrimSpace(pairs["a1"])) < xhsA1MinLen {
			issues = append(issues, Issue{
				Key:     "a1",
				Message: "缺少 a1 或长度不足 30 位，会被 302 到登录页，请重新导入完整 Cookie。",
				Missing: true,
			})
		}
		if !hasNonEmpty(pairs, "webId", "web_session") {
			issues = append(issues, Issue{
				Key:     "webId/web_session",
				Message: "缺少 webId 或 web_session，登录态可能不完整，请重新导入完整 Cookie。",
				Missing: true,
			})
		}
		return issues
	default:
		return nil
	}
}

// nonCommentLines 返回去除首尾空白后的非空数据行。
//
// 以 `#HttpOnly_` 开头的行是 HttpOnly cookie 数据，剥离该精确前缀后
// 保留；其余 `#` 开头的行才是注释。
func nonCommentLines(text string) []string {
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, httpOnlyPrefix) {
			line = strings.TrimSpace(strings.TrimPrefix(line, httpOnlyPrefix))
			if line == "" {
				continue
			}
			lines = append(lines, line)
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

// stripCookieLabel 剥离浏览器复制时可能带上的行首 `Cookie:` 标签。
func stripCookieLabel(text string) string {
	trimmed := strings.TrimSpace(text)
	if len(trimmed) >= len(cookieLabel) && strings.EqualFold(trimmed[:len(cookieLabel)], cookieLabel) {
		return strings.TrimSpace(trimmed[len(cookieLabel):])
	}
	return trimmed
}

// splitPair 把 `name=value` 片段拆成去空白的键值；无等号或空名返回 false。
func splitPair(piece string) (name, value string, ok bool) {
	name, value, found := strings.Cut(piece, "=")
	if !found {
		return "", "", false
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "", "", false
	}
	return name, strings.TrimSpace(value), true
}

// addPair 以首次出现为准写入映射，忽略空名。
func addPair(pairs map[string]string, name, value string) {
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	if _, exists := pairs[name]; exists {
		return
	}
	pairs[name] = strings.TrimSpace(value)
}

// headerFromMap 按键名排序生成 `name=value; ...`，保证导入结果可复现。
func headerFromMap(pairs map[string]string) string {
	names := make([]string, 0, len(pairs))
	for name := range pairs {
		if name != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, name+"="+pairs[name])
	}
	return strings.Join(parts, "; ")
}

// hasNonEmpty 报告映射中是否至少有一个给定键名对应非空值。
func hasNonEmpty(pairs map[string]string, names ...string) bool {
	for _, name := range names {
		if strings.TrimSpace(pairs[name]) != "" {
			return true
		}
	}
	return false
}

// headerContent 把任意格式输入转成请求头字符串，供 Import 统一调用。
func headerContent(text string, format Format) string {
	if format != JSONCookies {
		if format == Netscape {
			return ToHeader(text)
		}
		return stripCookieLabel(text)
	}
	pairs, entries, ok := parseJSON(text)
	if !ok {
		return ""
	}
	if entries != nil {
		return headerFromEntries(entries, nil)
	}
	return headerFromMap(pairs)
}

// xiaohongshuContent 生成小红书 cookies 文件内容：请求头格式，JSON 数组按
// 站点域过滤第三方 cookie（对象映射形态没有域信息，全部保留）。
func xiaohongshuContent(text string, format Format) string {
	if format != JSONCookies {
		return headerContent(text, format)
	}
	pairs, entries, ok := parseJSON(text)
	if !ok {
		return ""
	}
	if entries != nil {
		return headerFromEntries(entries, xhsCookieDomains)
	}
	return headerFromMap(pairs)
}

// netscapeContent 生成 Netscape 文件内容。输入已是 Netscape 时原样保留，
// 其余格式先归一成请求头再转换，避免把 JSON/Header 直接塞进 yt-dlp 文件。
func netscapeContent(text string, format Format, domain string) string {
	if format == Netscape {
		return text
	}
	return ToNetscape(headerContent(text, format), domain)
}

// headerFromEntries 按输入顺序把 JSON 条目拼成请求头；同名首个生效。
// domainSuffixes 非空时只保留 domain 命中其中之一（大小写不敏感）的条目。
func headerFromEntries(entries []jsonEntry, domainSuffixes []string) string {
	parts := make([]string, 0, len(entries))
	seen := make(map[string]bool)
	for _, entry := range entries {
		name := strings.TrimSpace(entry.Name)
		if name == "" || seen[name] {
			continue
		}
		if len(domainSuffixes) > 0 && !domainMatches(entry.Domain, domainSuffixes) {
			continue
		}
		seen[name] = true
		parts = append(parts, name+"="+strings.TrimSpace(entry.Value))
	}
	return strings.Join(parts, "; ")
}

// domainMatches 报告 domain 是否包含任一给定站点域。
func domainMatches(domain string, suffixes []string) bool {
	lowered := strings.ToLower(strings.TrimSpace(domain))
	for _, suffix := range suffixes {
		if strings.Contains(lowered, suffix) {
			return true
		}
	}
	return false
}
