package telegram

import "testing"

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
		"emoji 😀 x": "#emoji_x",
	}
	for in, want := range cases {
		if got := MakeTag(in); got != want {
			t.Errorf("MakeTag(%q)=%q want %q", in, got, want)
		}
	}
}

func TestBuildCaptionOmitsEmptyTags(t *testing.T) {
	cases := []struct {
		site, author, title, want string
	}{
		{"douyin", "", "标题", "#douyin\n标题"},
		{"", "甲", "标题", "#甲\n标题"},
		{"", "", "标题", "标题"},
		{"", "", "", ""},
	}
	for _, tc := range cases {
		if got := BuildCaption(tc.site, tc.author, tc.title); got != tc.want {
			t.Errorf("BuildCaption(%q,%q,%q)=%q want %q", tc.site, tc.author, tc.title, got, tc.want)
		}
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
