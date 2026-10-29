// Package sites 负责 URL 到站点名的归一化映射，以及站点 Cookie 文件定位。
//
// 站点识别口径与 Python 版 core/sites.py 保持一致：已知站点按根域名匹配，
// 未知域名取最后两段；Cookie 始终位于 <dataDir>/cookies/<site>.txt。
package sites

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// ErrNoCookie 表示 Cookie 文件不存在或内容为空白。
var ErrNoCookie = errors.New("cookie missing or empty")

// siteHosts 按优先级排列站点与根域名，命中即返回。
// 小红书短链有 .com / .cn 两种 TLD，rednote.com 是国际版，同引擎同 Cookie。
// X 历史上叫 Twitter，Cookie 文件沿用 twitter.txt。
var siteHosts = []struct {
	name  string
	hosts []string
}{
	{"xiaohongshu", []string{"xiaohongshu.com", "xhslink.com", "xhslink.cn", "rednote.com"}},
	{"bilibili", []string{"bilibili.com", "b23.tv"}},
	{"douyin", []string{"douyin.com", "v.douyin.com"}},
	{"youtube", []string{"youtube.com", "youtu.be"}},
	{"instagram", []string{"instagram.com", "instagr.am"}},
	{"twitter", []string{"twitter.com", "x.com", "t.co"}},
}

// Detect 解析链接并返回小写站点名。
//
// 已知站点用 host 精确匹配或 "." 后缀匹配（子域 www.xiaohongshu.com
// 命中 xiaohongshu.com），避免 Python 版子串匹配把 netflix.com 误判为
// twitter。未知域名取最后两段（如 a.b.example.co.uk → co.uk）；不足两段
// 时用完整 host 并把端口冒号替换为下划线（如 localhost:8080 →
// localhost_8080）；无法解析出 host 时返回 "unknown"。
func Detect(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return "unknown"
	}

	// Hostname 会去掉端口与 userinfo，匹配不受 :443 之类影响。
	hostname := strings.ToLower(u.Hostname())
	for _, site := range siteHosts {
		for _, h := range site.hosts {
			if hostname == h || strings.HasSuffix(hostname, "."+h) {
				return site.name
			}
		}
	}

	host := strings.ToLower(u.Host)
	parts := strings.Split(strings.SplitN(host, ":", 2)[0], ".")
	if len(parts) >= 2 {
		return parts[len(parts)-2] + "." + parts[len(parts)-1]
	}
	return strings.ReplaceAll(host, ":", "_")
}

// CookiePath 返回站点 Cookie 文件路径 <dataDir>/cookies/<site>.txt。
func CookiePath(dataDir, site string) string {
	return filepath.Join(dataDir, "cookies", site+".txt")
}

// LoadCookie 读取站点 Cookie 文件并去除首尾空白。
//
// 文件不存在或内容为空白时返回 ErrNoCookie；该函数不会创建文件。
func LoadCookie(dataDir, site string) (string, error) {
	path := CookiePath(dataDir, site)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", ErrNoCookie
		}
		return "", fmt.Errorf("读取 Cookie %s: %w", path, err)
	}
	cookie := strings.TrimSpace(string(data))
	if cookie == "" {
		return "", ErrNoCookie
	}
	return cookie, nil
}
