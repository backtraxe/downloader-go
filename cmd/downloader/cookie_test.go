package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCookieImportAndCheck 覆盖导入后文件落盘与体检通过的闭环，并确认输出
// 不包含任何 Cookie 值。
func TestCookieImportAndCheck(t *testing.T) {
	dir := t.TempDir()
	var out, errOut bytes.Buffer
	const secret = "secret-uifid-value"
	code := runWith([]string{"--data-dir", dir, "cookie", "import", "douyin"},
		strings.NewReader("Cookie: UIFID="+secret+"; ttwid=secret-ttwid-value"), &out, &errOut, dependencies{})
	if code != 0 {
		t.Fatalf("import code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
	p := filepath.Join(dir, "cookies", "douyin.txt")
	if _, err := os.Stat(p); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), secret) || strings.Contains(errOut.String(), secret) {
		t.Fatalf("output leaked cookie value: out=%q err=%q", out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), "已写入 "+p+"（请勿提交 cookies/）") {
		t.Fatalf("unexpected import output: %q", out.String())
	}

	out.Reset()
	errOut.Reset()
	code = runWith([]string{"--data-dir", dir, "cookie", "check", "douyin"}, strings.NewReader(""), &out, &errOut, dependencies{})
	if code != 0 {
		t.Fatalf("check code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), "✓ douyin") {
		t.Fatalf("unexpected check output: %q", out.String())
	}
	if strings.Contains(out.String(), secret) {
		t.Fatalf("check leaked cookie value: %q", out.String())
	}

	out.Reset()
	code = runWith([]string{"--data-dir", dir, "cookie", "check", "xiaohongshu"}, strings.NewReader(""), &out, &errOut, dependencies{})
	if code != 1 {
		t.Fatalf("missing should exit 1: %d %s", code, out.String())
	}
	if !strings.Contains(out.String(), "✗ xiaohongshu: 文件不存在") {
		t.Fatalf("unexpected missing output: %q", out.String())
	}
}

// TestCookieImportEmptyInput 确认空 stdin 报错且不创建文件。
func TestCookieImportEmptyInput(t *testing.T) {
	dir := t.TempDir()
	var out, errOut bytes.Buffer
	code := runWith([]string{"--data-dir", dir, "cookie", "import", "douyin"},
		strings.NewReader("  \n"), &out, &errOut, dependencies{})
	if code != 1 {
		t.Fatalf("code=%d out=%q err=%q", code, out.String(), errOut.String())
	}
	if !strings.Contains(errOut.String(), "输入为空") {
		t.Fatalf("stderr=%q", errOut.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "cookies", "douyin.txt")); !os.IsNotExist(err) {
		t.Fatalf("file should not exist, err=%v", err)
	}
}

// TestCookieCheckReportsMissingKeys 确认缺口只打印键名，不回显值。
func TestCookieCheckReportsMissingKeys(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "cookies"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cookies", "douyin.txt"), []byte("Cookie: foo=bar-secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	code := runWith([]string{"--data-dir", dir, "cookie", "check", "douyin"}, strings.NewReader(""), &out, &errOut, dependencies{})
	if code != 1 {
		t.Fatalf("code=%d out=%q", code, out.String())
	}
	got := out.String()
	for _, want := range []string{"✗ douyin:", "UIFID/UIFID_TEMP", "ttwid/sessionid"} {
		if !strings.Contains(got, want) {
			t.Fatalf("output %q missing %q", got, want)
		}
	}
	if strings.Contains(got, "bar-secret") {
		t.Fatalf("output leaked cookie value: %q", got)
	}
}

// TestCookieCheckScansDirectory 确认无参数时扫描 cookies/*.txt 并逐个报告。
func TestCookieCheckScansDirectory(t *testing.T) {
	dir := t.TempDir()
	cookieDir := filepath.Join(dir, "cookies")
	if err := os.MkdirAll(cookieDir, 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"bilibili.txt": "Cookie: buvid3=b",
		"douyin.txt":   "UIFID=u; ttwid=t",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(cookieDir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	var out, errOut bytes.Buffer
	code := runWith([]string{"--data-dir", dir, "cookie", "check"}, strings.NewReader(""), &out, &errOut, dependencies{})
	if code != 0 {
		t.Fatalf("code=%d out=%q err=%q", code, out.String(), errOut.String())
	}
	got := out.String()
	for _, want := range []string{"✓ bilibili", "✓ douyin"} {
		if !strings.Contains(got, want) {
			t.Fatalf("output %q missing %q", got, want)
		}
	}
}

// TestCookieCheckEmptyDirectory 确认目录不存在或为空时不做判定、退出 0。
func TestCookieCheckEmptyDirectory(t *testing.T) {
	var out, errOut bytes.Buffer
	code := runWith([]string{"--data-dir", t.TempDir(), "cookie", "check"}, strings.NewReader(""), &out, &errOut, dependencies{})
	if code != 0 {
		t.Fatalf("code=%d out=%q err=%q", code, out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), "没有可检查") {
		t.Fatalf("output=%q", out.String())
	}
}

// TestCookieCommandUsage 覆盖缺少子命令、未知子命令与 import 参数错误的用法提示。
func TestCookieCommandUsage(t *testing.T) {
	tests := [][]string{
		{"cookie"},
		{"cookie", "bogus"},
		{"cookie", "import"},
		{"cookie", "import", "a", "b"},
	}
	for _, args := range tests {
		var out, errOut bytes.Buffer
		if code := runWith(args, strings.NewReader(""), &out, &errOut, dependencies{}); code != 2 {
			t.Fatalf("args=%v code=%d out=%q err=%q", args, code, out.String(), errOut.String())
		}
		if !strings.Contains(errOut.String(), "用法") {
			t.Fatalf("args=%v stderr=%q", args, errOut.String())
		}
	}
}
