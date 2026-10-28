package sites

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDetect(t *testing.T) {
	cases := map[string]string{
		"https://www.xiaohongshu.com/explore/1": "xiaohongshu",
		"https://xhslink.cn/a":                  "xiaohongshu",
		"https://www.rednote.com/explore/1":     "xiaohongshu",
		"https://b23.tv/x":                      "bilibili",
		"https://v.douyin.com/x":                "douyin",
		"https://youtu.be/x":                    "youtube",
		"https://instagr.am/x":                  "instagram",
		"https://t.co/x":                        "twitter",
		"https://example.com/a":                 "example.com",
		"http://localhost:8080/a":               "localhost_8080",
	}
	for raw, want := range cases {
		if got := Detect(raw); got != want {
			t.Fatalf("%s: %s want %s", raw, got, want)
		}
	}
}

func TestDetectUnknown(t *testing.T) {
	cases := map[string]string{
		"https://a.b.example.co.uk/path": "co.uk",
		"https://X.COM/foo":              "twitter",
		"https://www.bilibili.com/video": "bilibili",
		"https://sub.douyin.com/x":       "douyin",
		"not a url":                      "unknown",
		"":                               "unknown",
	}
	for raw, want := range cases {
		if got := Detect(raw); got != want {
			t.Fatalf("%q: %s want %s", raw, got, want)
		}
	}
}

func TestCookiePath(t *testing.T) {
	got := CookiePath("/data", "douyin")
	want := filepath.Join("/data", "cookies", "douyin.txt")
	if got != want {
		t.Fatalf("%s want %s", got, want)
	}
}

func TestLoadCookieMissing(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadCookie(dir, "xiaohongshu")
	if !errors.Is(err, ErrNoCookie) {
		t.Fatalf("err = %v, want ErrNoCookie", err)
	}
	// 缺失时绝不能顺手创建文件。
	if _, statErr := os.Stat(CookiePath(dir, "xiaohongshu")); !os.IsNotExist(statErr) {
		t.Fatalf("cookie file should not be created, stat err = %v", statErr)
	}
}

func TestLoadCookieWhitespaceOnly(t *testing.T) {
	dir := t.TempDir()
	path := CookiePath(dir, "douyin")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(" \n\t\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadCookie(dir, "douyin")
	if !errors.Is(err, ErrNoCookie) {
		t.Fatalf("err = %v, want ErrNoCookie", err)
	}
}

func TestLoadCookieReadsTrimmed(t *testing.T) {
	dir := t.TempDir()
	path := CookiePath(dir, "bilibili")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("  a1=abc; webId=def\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadCookie(dir, "bilibili")
	if err != nil {
		t.Fatal(err)
	}
	if got != "a1=abc; webId=def" {
		t.Fatalf("got %q", got)
	}
}
