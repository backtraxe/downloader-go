package telegram

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCaptionAndTag(t *testing.T) {
	if got := BuildCaption("douyin", "作者 甲", "标题"); got != "#作者_甲 #douyin\n标题" {
		t.Fatalf("%q", got)
	}
	if got := MakeTag("a b/c"); got != "#a_b_c" {
		t.Fatal(got)
	}
}

func TestMakeTagEdgeCases(t *testing.T) {
	cases := map[string]string{
		"":          "",
		"///":       "",
		"abc":       "#abc",
		"a__b":      "#a__b",
		" a  b ":    "#a_b",
		"标题 123_x":  "#标题_123_x",
		"emoji 😀 x": "#emoji_emoji_1f600_x",
		"こんにちは":     "#こんにちは",
		"안녕하세요":     "#안녕하세요",
		"Иван":      "#Иван",
		"😀":         "#emoji_1f600",
	}
	for in, want := range cases {
		if got := MakeTag(in); got != want {
			t.Errorf("MakeTag(%q)=%q want %q", in, got, want)
		}
	}
}

func TestMakeTagEncodesEmojiOnly(t *testing.T) {
	cases := map[string]string{
		"😀":     "#emoji_1f600",
		"小明😀":   "#小明emoji_1f600",
		"小😀明":   "#小emoji_1f600_明",
		"a😀b":   "#aemoji_1f600_b",
		"👩‍💻":   "#emoji_1f469_200d_1f4bb",
		"🧑🏽‍🚀":  "#emoji_1f9d1_1f3fd_200d_1f680",
		"1️⃣":   "#1emoji_fe0f_20e3",
		"🇨🇳":    "#emoji_1f1e8_1f1f3",
		"a/b 😀": "#a_b_emoji_1f600",
	}
	for input, want := range cases {
		if got := MakeTag(input); got != want {
			t.Errorf("MakeTag(%q)=%q want %q", input, got, want)
		}
	}
}

func TestBuildCaptionPreservesEmojiAuthor(t *testing.T) {
	cases := []struct {
		author string
		want   string
	}{
		{"😀", "#emoji_1f600 #xiaohongshu\n作者：😀\n标题"},
		{"小明😀", "#小明emoji_1f600 #xiaohongshu\n作者：小明😀\n标题"},
		{"小明", "#小明 #xiaohongshu\n标题"},
	}
	for _, tc := range cases {
		if got := BuildCaption("xiaohongshu", tc.author, "标题"); got != tc.want {
			t.Errorf("author=%q caption=%q want %q", tc.author, got, tc.want)
		}
	}
}

func TestMakeTagPrefixesNumericAuthors(t *testing.T) {
	cases := map[string]string{
		"170":    "#author_170",
		"0":      "#author_0",
		"１２３":    "#author_１２３",
		"170abc": "#170abc",
		"小明170":  "#小明170",
	}
	for input, want := range cases {
		if got := MakeTag(input); got != want {
			t.Errorf("MakeTag(%q)=%q want %q", input, got, want)
		}
	}
}

func TestBuildCaptionShowsNumericAuthor(t *testing.T) {
	want := "#author_170 #xiaohongshu\n作者：170\n标题"
	if got := BuildCaption("xiaohongshu", "170", "标题"); got != want {
		t.Fatalf("caption=%q want %q", got, want)
	}
}

func TestBuildCaptionOmitsEmptyTags(t *testing.T) {
	cases := []struct {
		site, author, title, want string
	}{
		{"douyin", "", "标题", "#douyin\n标题"},
		{"", "甲", "标题", "#甲\n标题"},
		{"douyin", "", "", "#douyin"},
		{"", "", "标题", "标题"},
		{"", "", "", ""},
	}
	for _, tc := range cases {
		if got := BuildCaption(tc.site, tc.author, tc.title); got != tc.want {
			t.Errorf("BuildCaption(%q,%q,%q)=%q want %q", tc.site, tc.author, tc.title, got, tc.want)
		}
	}
}

// TestBuildCaptionTruncatesOnRuneBoundary 验证 caption 在 1024 个 rune 处
// 截断，且不会把多字节 UTF-8 字符切成半个。
func TestBuildCaptionTruncatesOnRuneBoundary(t *testing.T) {
	title := strings.Repeat("界", maxCaptionRunes+100)
	got := BuildCaption("douyin", "作者", title)
	if n := utf8.RuneCountInString(got); n != maxCaptionRunes {
		t.Fatalf("rune count=%d want %d", n, maxCaptionRunes)
	}
	if !utf8.ValidString(got) {
		t.Fatal("truncated caption is not valid UTF-8")
	}
	if !strings.HasPrefix(got, "#作者 #douyin\n界") {
		t.Fatalf("caption prefix lost: %q", string([]rune(got)[:12]))
	}

	if got := BuildCaption("", "", "标题"); got != "标题" {
		t.Fatalf("short caption changed: %q", got)
	}
}

func TestClassify(t *testing.T) {
	cases := map[string]FileKind{
		"a.jpg":  Photo,
		"a.JPEG": Photo,
		"a.png":  Photo,
		"a.webp": Photo,
		"a.heic": Photo,
		"a.heif": Photo,
		"a.mp4":  Video,
		"a.MOV":  Video,
		"a.webm": Video,
		"a.mkv":  Video,
		"a.gif":  Document,
		"a.pdf":  Document,
		"a":      Document,
	}
	for in, want := range cases {
		if got := Classify(in); got != want {
			t.Errorf("Classify(%q)=%v want %v", in, got, want)
		}
	}
}

func TestPlanGroupsMaxTenAndOrder(t *testing.T) {
	files := make([]FileInfo, 0, 12)
	for i := 0; i < 12; i++ {
		files = append(files, FileInfo{Path: "x", Kind: Photo})
	}
	groups := PlanGroups(files, 10)
	if len(groups) != 2 || len(groups[0]) != 10 || len(groups[1]) != 2 {
		t.Fatalf("%v", groups)
	}
}

func TestPlanGroupsPreservesOrderAndSkipsDocuments(t *testing.T) {
	files := []FileInfo{
		{Path: "p0.jpg", Kind: Photo},
		{Path: "d.pdf", Kind: Document},
		{Path: "v.mp4", Kind: Video},
		{Path: "p1.jpg", Kind: Photo},
	}
	groups := PlanGroups(files, 10)
	if len(groups) != 1 || len(groups[0]) != 3 {
		t.Fatalf("%v", groups)
	}
	want := []string{"p0.jpg", "v.mp4", "p1.jpg"}
	for i, got := range groups[0] {
		if got.Path != want[i] {
			t.Fatalf("group[%d]=%s want %s", i, got.Path, want[i])
		}
	}
}

func TestPlanGroupsDefaultMax(t *testing.T) {
	files := make([]FileInfo, 11)
	for i := range files {
		files[i] = FileInfo{Path: "x", Kind: Video}
	}
	groups := PlanGroups(files, 0)
	if len(groups) != 2 || len(groups[0]) != 10 || len(groups[1]) != 1 {
		t.Fatalf("%v", groups)
	}
}

func TestPlanGroupsEmpty(t *testing.T) {
	if got := PlanGroups(nil, 10); len(got) != 0 {
		t.Fatalf("%v", got)
	}
}
