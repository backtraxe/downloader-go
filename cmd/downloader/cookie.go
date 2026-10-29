package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/backtraxe/downloader-go/internal/cookies"
	"github.com/backtraxe/downloader-go/internal/sites"
)

// cookieSitePattern 限定归一化后的站点名只能是安全的文件名片段，避免拼
// 路径时越出 cookies/ 目录。
var cookieSitePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// normalizeCookieSite 归一化站点名：去空白、转小写、去掉末尾 .txt，并校验
// 只含安全字符。import 与 check 共用，保证 Douyin/douyin.txt 命中同一文件。
func normalizeCookieSite(raw string) (string, error) {
	site := strings.ToLower(strings.TrimSpace(raw))
	site = strings.TrimSuffix(site, ".txt")
	if site == "" {
		return "", fmt.Errorf("%q 不合法：站点名不能为空", raw)
	}
	if !cookieSitePattern.MatchString(site) {
		return "", fmt.Errorf("%q 不合法：只允许小写字母、数字、点、下划线、连字符，且不能以符号开头", raw)
	}
	return site, nil
}

// cookieUsage 是 cookie 子命令的用法提示。
const cookieUsage = `用法：
  dl-go cookie import <site>     从 stdin 读取 Cookie 文本，写入 cookies/<site>.txt
  dl-go cookie check [sites...]  离线体检 Cookie 必需键（缺省检查 cookies/*.txt）`

// runCookieCommand 分派 cookie 子命令。所有输出只包含文件路径与键名，
// 绝不回显 Cookie 值。
func runCookieCommand(args []string, dataDir string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, cookieUsage)
		return 2
	}
	switch args[0] {
	case "import":
		return runCookieImport(args[1:], dataDir, stdin, stdout, stderr)
	case "check":
		return runCookieCheck(args[1:], dataDir, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "未知 cookie 子命令 %q\n%s\n", args[0], cookieUsage)
		return 2
	}
}

// runCookieImport 把 stdin 中的 Cookie 文本按站点格式写入
// <dataDir>/cookies/<site>.txt，只打印路径，不打印内容。
func runCookieImport(args []string, dataDir string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "用法：dl-go cookie import <site>")
		return 2
	}
	site, err := normalizeCookieSite(args[0])
	if err != nil {
		fmt.Fprintf(stderr, "站点名非法：%v\n", err)
		return 1
	}
	path, err := cookies.Import(dataDir, site, stdin)
	if err != nil {
		fmt.Fprintf(stderr, "导入 Cookie 失败：%v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "已写入 %s（请勿提交 cookies/）\n", path)
	return 0
}

// runCookieCheck 体检指定站点；未指定站点时扫描 cookies/*.txt。任一文件
// 缺失或存在必需键缺口时返回 1，否则返回 0。
func runCookieCheck(args []string, dataDir string, stdout, stderr io.Writer) int {
	siteNames := make([]string, 0, len(args))
	seen := make(map[string]bool, len(args))
	for _, raw := range args {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		site, err := normalizeCookieSite(raw)
		if err != nil {
			fmt.Fprintf(stderr, "站点名非法：%v\n", err)
			return 1
		}
		if seen[site] {
			continue
		}
		seen[site] = true
		siteNames = append(siteNames, site)
	}
	if len(siteNames) == 0 {
		discovered, err := discoverCookieSites(dataDir)
		if err != nil {
			fmt.Fprintf(stderr, "读取 Cookie 目录失败：%v\n", err)
			return 1
		}
		if len(discovered) == 0 {
			fmt.Fprintln(stdout, "cookies/ 下没有可检查的 .txt 文件（目录不存在或为空）。")
			return 0
		}
		siteNames = discovered
	}

	hasGap := false
	for _, site := range siteNames {
		path := sites.CookiePath(dataDir, site)
		text, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				fmt.Fprintf(stdout, "✗ %s: 文件不存在\n", site)
			} else {
				fmt.Fprintf(stdout, "✗ %s: 读取失败\n", site)
				fmt.Fprintf(stderr, "读取 %s 失败：%v\n", path, err)
			}
			hasGap = true
			continue
		}

		issues := cookies.Check(string(text), site)
		if len(issues) == 0 {
			fmt.Fprintf(stdout, "✓ %s\n", site)
			continue
		}
		keys := make([]string, 0, len(issues))
		for _, issue := range issues {
			keys = append(keys, issue.Key)
		}
		fmt.Fprintf(stdout, "✗ %s: %s\n", site, strings.Join(keys, ", "))
		hasGap = true
	}

	if hasGap {
		return 1
	}
	return 0
}

// discoverCookieSites 返回 cookies/ 下现有 *.txt 对应的站点名（排序保证输出
// 稳定）；目录不存在时返回空列表。
func discoverCookieSites(dataDir string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(dataDir, "cookies"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var names []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name, ok := strings.CutSuffix(entry.Name(), ".txt")
		if !ok || name == "" {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}
