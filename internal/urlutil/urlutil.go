// Package urlutil 提供与站点无关的 URL 处理工具：
// 从分享文案中提取链接、归一化相对链接、识别短链域名。
package urlutil

import (
	"net/url"
	"regexp"
	"strings"
)

// urlPattern 匹配 http(s):// 开头的完整 URL，直到首个空白字符。
var urlPattern = regexp.MustCompile(`https?://[^\s]+`)

// shortLinkHosts 是各站点用于跳转的短链域名（精确匹配，忽略大小写）。
var shortLinkHosts = map[string]struct{}{
	"b23.tv":       {},
	"v.douyin.com": {},
	"youtu.be":     {},
	"t.co":         {},
	"instagr.am":   {},
	"xhslink.com":  {},
	"xhslink.cn":   {},
}

// ExtractURLs 从任意文本中提取所有 http(s) URL，去重并保持首次出现顺序。
//
// 用户常粘贴分享文案而非纯 URL，因此需要剔除 URL 尾部的中日韩文字、
// 全角标点与英文引号/括号等上下文。无匹配时返回 nil。
func ExtractURLs(text string) []string {
	if text == "" {
		return nil
	}
	matches := urlPattern.FindAllString(text, -1)
	if len(matches) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(matches))
	result := make([]string, 0, len(matches))
	for _, raw := range matches {
		link := strings.TrimRightFunc(raw, isTrailingChar)
		if link == "" {
			continue
		}
		if _, ok := seen[link]; ok {
			continue
		}
		seen[link] = struct{}{}
		result = append(result, link)
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// isTrailingChar 判断字符是否属于 URL 尾部应剔除的上下文：
// CJK 统一表意文字与符号区（　-鿿）、全角字符区
// （＀-￯，含中文标点），以及英文引号、右括号等。
func isTrailingChar(r rune) bool {
	if r >= 0x3000 && r <= 0x9fff {
		return true
	}
	if r >= 0xff00 && r <= 0xffef {
		return true
	}
	return strings.ContainsRune("\"'[]>}", r)
}

// IsShortLink 判断 URL 的 host 是否属于已知短链域名（不校验路径）。
func IsShortLink(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	_, ok := shortLinkHosts[strings.ToLower(u.Hostname())]
	return ok
}

// NormalizeURL 把相对/协议相对 URL 归一化为绝对 URL。
//
// 空值与 data: URI 原样返回（仅去除首尾空白）；"//host/x" 补 https:；
// 已是 http(s) 绝对地址的直接返回；其余以 pageURL 为基准解析。
func NormalizeURL(rawURL, pageURL string) string {
	link := strings.TrimSpace(rawURL)
	if link == "" {
		return link
	}
	if strings.HasPrefix(link, "data:") {
		return link
	}
	if strings.HasPrefix(link, "//") {
		return "https:" + link
	}
	if strings.HasPrefix(link, "http://") || strings.HasPrefix(link, "https://") {
		return link
	}

	base, err := url.Parse(pageURL)
	if err != nil {
		return link
	}
	ref, err := url.Parse(link)
	if err != nil {
		return link
	}
	return base.ResolveReference(ref).String()
}
