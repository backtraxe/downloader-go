package fsutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSanitizeFilename(t *testing.T) {
	got := SanitizeFilename("a/b\\c:d*e?f\"g<h>i|j\x00中文", "untitled", 80)
	if got != "abcdefghij中文" {
		t.Fatalf("%q", got)
	}
	if SanitizeFilename("   ", "untitled", 80) != "untitled" {
		t.Fatal("blank")
	}
	if SanitizeFilename("", "unknown", 80) != "unknown" {
		t.Fatal("empty")
	}
}

func TestSanitizeFilenameTruncatesByRunes(t *testing.T) {
	// maxLen 按字符（rune）计，截断不产生半个 UTF-8 序列。
	if got := SanitizeFilename("中文字符", "untitled", 2); got != "中文" {
		t.Fatalf("%q", got)
	}
	if got := SanitizeFilename("abcdef", "untitled", 3); got != "abc" {
		t.Fatalf("%q", got)
	}
	// maxLen <= 0 表示不截断。
	if got := SanitizeFilename("abcdef", "untitled", 0); got != "abcdef" {
		t.Fatalf("%q", got)
	}
}

func TestSanitizeFilenameTrimsWhitespace(t *testing.T) {
	if got := SanitizeFilename("  title  ", "untitled", 80); got != "title" {
		t.Fatalf("%q", got)
	}
}

func TestBuildDownloadDir(t *testing.T) {
	got := BuildDownloadDir("data", "douyin", "标题", "作者")
	want := filepath.Join("data", "douyin", "作者", "标题")
	if got != want {
		t.Fatalf("%q want %q", got, want)
	}
	got = BuildDownloadDir("data", "douyin", "标题", "")
	want = filepath.Join("data", "douyin", "标题")
	if got != want {
		t.Fatalf("%q want %q", got, want)
	}
	// 纯空白作者视为无作者。
	got = BuildDownloadDir("data", "douyin", "标题", "   ")
	want = filepath.Join("data", "douyin", "标题")
	if got != want {
		t.Fatalf("%q want %q", got, want)
	}
}

func TestBuildDownloadDirSanitizes(t *testing.T) {
	got := BuildDownloadDir("data", "site", "a/b:c", "x/y")
	want := filepath.Join("data", "site", "xy", "abc")
	if got != want {
		t.Fatalf("%q want %q", got, want)
	}
	// 空标题/作者回退到默认名。
	got = BuildDownloadDir("data", "site", "  ", "")
	want = filepath.Join("data", "site", "untitled")
	if got != want {
		t.Fatalf("%q want %q", got, want)
	}
}

func TestUniquePathAndExistingByStem(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "001.jpg")
	first, err := UniquePath(p)
	if err != nil || first != p {
		t.Fatalf("%q %v", first, err)
	}
	second, _ := UniquePath(p)
	if second != filepath.Join(dir, "001_1.jpg") {
		t.Fatalf("%q", second)
	}
	if _, ok := ExistingByStem(filepath.Join(dir, "001.png")); !ok {
		t.Fatal("stem match")
	}
	os.WriteFile(filepath.Join(dir, "002.part"), nil, 0o600)
	if _, ok := ExistingByStem(filepath.Join(dir, "002.jpg")); ok {
		t.Fatal(".part counted")
	}
}

func TestUniquePathReturnsExistingPlaceholder(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.txt")
	got, err := UniquePath(p)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(got)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 0 {
		t.Fatalf("placeholder size %d", info.Size())
	}
	if _, err := UniquePath(filepath.Join(dir, "missing", "a.txt")); err == nil {
		t.Fatal("expected error for missing parent dir")
	}
}

func TestExistingByStemIgnoresYTDLAndPartFragments(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "v.ytdl"), nil, 0o600)
	os.WriteFile(filepath.Join(dir, "v.mp4.part-Frag1"), nil, 0o600)
	if _, ok := ExistingByStem(filepath.Join(dir, "v.mp4")); ok {
		t.Fatal("partial counted")
	}
	os.WriteFile(filepath.Join(dir, "v.mp4"), nil, 0o600)
	got, ok := ExistingByStem(filepath.Join(dir, "v.mp4"))
	if !ok || got != filepath.Join(dir, "v.mp4") {
		t.Fatalf("%q %v", got, ok)
	}
}

func TestBuildDownloadDirEmptySite(t *testing.T) {
	// 纯函数，仅校验空站点名不会 panic（仅产生一层）。
	got := BuildDownloadDir("data", "", "t", "a")
	if got != filepath.Join("data", "", "a", "t") {
		t.Fatalf("%q", got)
	}
}

func TestIsPartialDownload(t *testing.T) {
	partial := []string{"a.part", "a.ytdl", "a.mp4.part", "a.mp4.part-Frag12", "x.part-3"}
	for _, name := range partial {
		if !IsPartialDownload(name) {
			t.Fatalf("%q should be partial", name)
		}
	}
	complete := []string{"a.jpg", "a.mp4", "partial.jpg", "a.partx"}
	for _, name := range complete {
		if IsPartialDownload(name) {
			t.Fatalf("%q should not be partial", name)
		}
	}
}

func TestExtractURLNumericID(t *testing.T) {
	cases := map[string]string{
		"https://www.coolapk.com/feed/74208176":       "74208176",
		"https://x.com/a/123456/":                     "123456",
		"https://x.com/a/1234":                        "",
		"https://x.com/a/123456?from=1":               "123456",
		"https://x.com/a/12ab56":                      "",
		"https://x.com/":                              "",
		"not a url":                                   "",
		"https://x.com/a/12345#frag":                  "12345",
		"https://x.com/a/%31%32%33%34%35":             "",
		"https://x.com/a/000000000000000000000000001": "000000000000000000000000001",
	}
	for raw, want := range cases {
		if got := ExtractURLNumericID(raw); got != want {
			t.Fatalf("%s: %q want %q", raw, got, want)
		}
	}
}
