package urlutil

import (
	"reflect"
	"testing"
)

func TestExtractURLsTrimsCJK(t *testing.T) {
	got := ExtractURLs("看这个 https://a.com/x，还有 https://a.com/x 和 https://b.com/y。")
	want := []string{"https://a.com/x", "https://b.com/y"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%v want %v", got, want)
	}
}

func TestExtractURLsUnicodeWhitespace(t *testing.T) {
	// Python 的 \s 是 Unicode 语义：NBSP（U+00A0）与表意空格（U+3000）
	// 都必须分隔 URL，否则会把多条链接或后续拉丁文字并进一条。
	got := ExtractURLs("https://a.com/x https://b.com/y　hello")
	want := []string{"https://a.com/x", "https://b.com/y"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%v want %v", got, want)
	}

	// NEL（U+0085）同样属于 Unicode 空白。
	got = ExtractURLs("https://a.com/x\u0085https://b.com/y")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%v want %v", got, want)
	}
}

func TestExtractURLsEmpty(t *testing.T) {
	if got := ExtractURLs(""); got != nil {
		t.Fatalf("got %v, want nil", got)
	}
	if got := ExtractURLs("没有链接，只有文字。"); got != nil {
		t.Fatalf("got %v, want nil", got)
	}
}

func TestExtractURLsTrailingPunctuation(t *testing.T) {
	// 与 Python 版一致：剔除全角括号、英文引号与尖括号，但不剔除 ASCII 右括号。
	got := ExtractURLs(`（https://a.com/x） "https://b.com/y" <https://c.com/z>`)
	want := []string{"https://a.com/x", "https://b.com/y", "https://c.com/z"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%v want %v", got, want)
	}
}

func TestExtractURLsKeepsLeftBracket(t *testing.T) {
	// Python 的尾部剔除集不含 "["，这里保持一致。
	got := ExtractURLs("见 https://a.com/x[")
	want := []string{"https://a.com/x["}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%v want %v", got, want)
	}
}

func TestExtractURLsKeepsQuery(t *testing.T) {
	got := ExtractURLs("https://a.com/x?u=1&v=2，然后结束")
	want := []string{"https://a.com/x?u=1&v=2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%v want %v", got, want)
	}
}

func TestNormalizeURL(t *testing.T) {
	if got := NormalizeURL("//cdn.x/a.jpg", "https://x.com/p"); got != "https://cdn.x/a.jpg" {
		t.Fatal(got)
	}
	if got := NormalizeURL("/a.jpg", "https://x.com/p/q"); got != "https://x.com/a.jpg" {
		t.Fatal(got)
	}
}

func TestNormalizeURLPassThrough(t *testing.T) {
	cases := map[string]string{
		"":                            "",
		"data:image/png;base64,AAAA":  "data:image/png;base64,AAAA",
		"https://cdn.x/a.jpg":         "https://cdn.x/a.jpg",
		"http://cdn.x/a.jpg":          "http://cdn.x/a.jpg",
		"  https://cdn.x/a.jpg  ":     "https://cdn.x/a.jpg",
		"https://cdn.x/a.jpg?w=1&h=2": "https://cdn.x/a.jpg?w=1&h=2",
	}
	for raw, want := range cases {
		if got := NormalizeURL(raw, "https://x.com/p/q"); got != want {
			t.Fatalf("%q: got %q want %q", raw, got, want)
		}
	}
}

func TestNormalizeURLRelative(t *testing.T) {
	cases := map[string]string{
		"a.jpg":    "https://x.com/p/a.jpg",
		"../a.jpg": "https://x.com/a.jpg",
		"?page=2":  "https://x.com/p/q?page=2",
	}
	for raw, want := range cases {
		if got := NormalizeURL(raw, "https://x.com/p/q"); got != want {
			t.Fatalf("%q: got %q want %q", raw, got, want)
		}
	}
}

func TestIsShortLink(t *testing.T) {
	for _, raw := range []string{"https://b23.tv/x", "https://v.douyin.com/x", "https://xhslink.com/a"} {
		if !IsShortLink(raw) {
			t.Fatalf("%s", raw)
		}
	}
	if IsShortLink("https://www.douyin.com/note/1") {
		t.Fatal("long url")
	}
}

func TestIsShortLinkHostSet(t *testing.T) {
	for _, raw := range []string{
		"https://youtu.be/x",
		"https://t.co/x",
		"https://instagr.am/x",
		"https://xhslink.cn/a",
		"https://B23.TV/x",
		"https://v.douyin.com/x/",
	} {
		if !IsShortLink(raw) {
			t.Fatalf("%s should be short", raw)
		}
	}
	for _, raw := range []string{
		"https://b23.tv.example.com/x",
		"https://notb23.tv/x",
		"https://www.youtube.com/watch?v=x",
		"",
	} {
		if IsShortLink(raw) {
			t.Fatalf("%q should not be short", raw)
		}
	}
}
