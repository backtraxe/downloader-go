package cookies

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectAndParse(t *testing.T) {
	header := "a1=abc; web_session=def"
	if Detect(header) != Header {
		t.Fatal("header")
	}
	m, _, err := ParseMap(header)
	if err != nil || m["a1"] != "abc" {
		t.Fatalf("%v %v", m, err)
	}

	netscape := "# Netscape HTTP Cookie File\n.douyin.com\tTRUE\t/\tTRUE\t4070908800\tUIFID\tu1\n"
	m, _, err = ParseMap(netscape)
	if err != nil || m["UIFID"] != "u1" {
		t.Fatalf("%v %v", m, err)
	}

	if ToHeader(netscape) != "UIFID=u1" {
		t.Fatal(ToHeader(netscape))
	}
	if !strings.Contains(ToNetscape(header, ".x.com"), "a1\tabc") {
		t.Fatal(ToNetscape(header, ".x.com"))
	}
}

func TestCheckDouyinGate(t *testing.T) {
	if len(Check("ttwid=x; UIFID=y", "douyin")) != 0 {
		t.Fatalf("complete cookie reported: %+v", Check("ttwid=x; UIFID=y", "douyin"))
	}
	if len(Check("ttwid=x", "douyin")) != 1 {
		t.Fatalf("missing UIFID not reported")
	}
	if len(Check("UIFID=y", "douyin")) != 1 {
		t.Fatalf("missing ttwid/sessionid not reported")
	}
	if len(Check("UIFID=; ttwid=", "douyin")) == 0 {
		t.Fatal("empty values must count as gaps")
	}
	if len(Check("a1=short; webId=x", "xiaohongshu")) == 0 {
		t.Fatal("short a1 must be reported")
	}
}

func TestJSONArrays(t *testing.T) {
	for _, raw := range []string{
		`[{"name":"a1","value":"abc","domain":".xiaohongshu.com"}]`,
		`{"cookies":[{"name":"a1","value":"abc","domain":".xiaohongshu.com"}]}`,
	} {
		m, f, err := ParseMap(raw)
		if err != nil || f != JSONCookies || m["a1"] != "abc" {
			t.Fatalf("%s: %v %v %v", raw, m, f, err)
		}
	}
}

func TestImportPerSiteFormat(t *testing.T) {
	dir := t.TempDir()
	p, err := Import(dir, "bilibili", strings.NewReader("Cookie: SESSDATA=abc"))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(p)
	if !strings.HasPrefix(string(data), "# Netscape HTTP Cookie File") {
		t.Fatalf("%s", data)
	}
	if !strings.Contains(string(data), ".bilibili.com\tTRUE\t/\tTRUE\t4070908800\tSESSDATA\tabc") {
		t.Fatalf("%s", data)
	}

	p, err = Import(dir, "xiaohongshu", strings.NewReader(
		`[{"name":"a1","value":"abc","domain":".xiaohongshu.com"},{"name":"third","value":"x","domain":".evil.com"}]`))
	if err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(p)
	if !strings.Contains(string(data), "a1=abc") || strings.Contains(string(data), "third=") {
		t.Fatalf("%s", data)
	}
}

// TestImportXiaohongshuAcceptsRednote 验证小红书导入同时接受国内版
// xiaohongshu.com 与国际版 rednote.com 的 cookie，第三方域仍然丢弃。
func TestImportXiaohongshuAcceptsRednote(t *testing.T) {
	dir := t.TempDir()
	raw := `[
		{"name":"a1","value":"abc","domain":".xiaohongshu.com"},
		{"name":"web_session","value":"ws","domain":".rednote.com"},
		{"name":"third","value":"x","domain":".evil.com"}
	]`
	path, err := Import(dir, "xiaohongshu", strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, "a1=abc") || !strings.Contains(content, "web_session=ws") {
		t.Fatalf("missing accepted cookies: %s", content)
	}
	if strings.Contains(content, "third=") {
		t.Fatalf("third-party cookie kept: %s", content)
	}
}

func TestDetectFormats(t *testing.T) {
	cases := []struct {
		name string
		text string
		want Format
	}{
		{"json object", `{"a1":"abc","web_session":"def"}`, JSONCookies},
		{"empty json object", `{}`, JSONCookies},
		{"invalid json", `{"a1":`, Header},
		{"json array", `[{"name":"a1","value":"abc"}]`, JSONCookies},
		{"wrapped json array", `{"cookies":[{"name":"a1","value":"abc"}]}`, JSONCookies},
		{"header", "a1=abc; web_session=def", Header},
		{"header with cookie label", "Cookie: a1=abc", Header},
		{"netscape", "# comment\n.douyin.com\tTRUE\t/\tTRUE\t1\tUIFID\tu1\n", Netscape},
		{"netscape needs seven tab fields", "a\tb\tc\td\te\tf", Header},
		{"empty", "", Header},
	}
	for _, tc := range cases {
		if got := Detect(tc.text); got != tc.want {
			t.Errorf("%s: Detect = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestParseMapJSON(t *testing.T) {
	m, format, err := ParseMap(`{"a1":"abc"}`)
	if err != nil || format != JSONCookies || m["a1"] != "abc" {
		t.Fatalf("format=%v map=%v err=%v", format, m, err)
	}

	// 数组内同名首个生效；空值保留为空串（由 Check 判定为缺口）。
	raw := `[{"name":"a1","value":"first"},{"name":"a1","value":"second"},{"name":"empty","value":""},{"name":"","value":"skip"}]`
	m, format, err = ParseMap(raw)
	if err != nil || format != JSONCookies {
		t.Fatalf("format=%v err=%v", format, err)
	}
	if m["a1"] != "first" {
		t.Fatalf("first occurrence should win: %v", m)
	}
	if value, ok := m["empty"]; !ok || value != "" {
		t.Fatalf("empty value must be preserved: %v", m)
	}
	if _, ok := m[""]; ok || len(m) != 2 {
		t.Fatalf("map=%v", m)
	}
}

func TestParseMapHeader(t *testing.T) {
	// 浏览器复制的 Cookie 头常带行首标签，标签不是 cookie 名。
	m, format, err := ParseMap("Cookie: a1=abc; web_session=def")
	if err != nil || format != Header {
		t.Fatalf("format=%v err=%v", format, err)
	}
	if m["a1"] != "abc" || m["web_session"] != "def" {
		t.Fatalf("map=%v", m)
	}
	if _, ok := m["Cookie: a1"]; ok {
		t.Fatalf("Cookie 标签被当成了 cookie 名: %v", m)
	}

	// 同名首个出现生效，畸形片段与空名忽略。
	m, _, err = ParseMap("a=1; broken; =empty; a=2; b=3")
	if err != nil {
		t.Fatal(err)
	}
	if m["a"] != "1" || m["b"] != "3" || len(m) != 2 {
		t.Fatalf("map=%v", m)
	}
}

func TestParseMapNetscape(t *testing.T) {
	text := strings.Join([]string{
		"# Netscape HTTP Cookie File",
		"",
		".douyin.com\tTRUE\t/\tTRUE\t4070908800\tUIFID\tu1",
		".douyin.com\tTRUE\t/\tTRUE\t4070908800\tUIFID\tu2",
		"short\tline",
		".douyin.com\tTRUE\t/\tTRUE\t4070908800\t\tskip-empty-name",
	}, "\n")
	m, format, err := ParseMap(text)
	if err != nil || format != Netscape {
		t.Fatalf("format=%v err=%v", format, err)
	}
	if m["UIFID"] != "u1" {
		t.Fatalf("first occurrence should win: %v", m)
	}
	if _, ok := m[""]; ok || len(m) != 1 {
		t.Fatalf("map=%v", m)
	}
}

func TestParseMapEmpty(t *testing.T) {
	for _, text := range []string{"", "   ", "\n\t\n"} {
		m, format, err := ParseMap(text)
		if err != nil {
			t.Fatalf("%q: %v", text, err)
		}
		if format != Header || len(m) != 0 {
			t.Fatalf("%q: format=%v map=%v", text, format, m)
		}
	}
}

func TestToHeader(t *testing.T) {
	netscape := "# Netscape HTTP Cookie File\n" +
		".douyin.com\tTRUE\t/\tTRUE\t4070908800\tUIFID\tu1\n" +
		".douyin.com\tTRUE\t/\tTRUE\t4070908800\tttwid\tt1\n"
	if got := ToHeader(netscape); got != "UIFID=u1; ttwid=t1" {
		t.Fatalf("ToHeader = %q", got)
	}

	// 非 Netscape 行原样拼接；Cookie: 标签属于传输语法，不属于 cookie 内容。
	if got := ToHeader("Cookie: a1=abc; web_session=def"); got != "a1=abc; web_session=def" {
		t.Fatalf("ToHeader = %q", got)
	}
	if got := ToHeader(""); got != "" {
		t.Fatalf("ToHeader(empty) = %q", got)
	}
}

func TestToNetscape(t *testing.T) {
	got := ToNetscape("a1=abc; web_session=def", ".xiaohongshu.com")
	want := "# Netscape HTTP Cookie File\n" +
		"# This is generated by downloader-bot\n" +
		".xiaohongshu.com\tTRUE\t/\tTRUE\t4070908800\ta1\tabc\n" +
		".xiaohongshu.com\tTRUE\t/\tTRUE\t4070908800\tweb_session\tdef\n"
	if got != want {
		t.Fatalf("ToNetscape =\n%q\nwant\n%q", got, want)
	}

	// 已有 Netscape 内容重新转换：不重复带头、不丢 cookie。
	if got := ToNetscape("UIFID=u1", ".douyin.com"); strings.Count(got, "# Netscape HTTP Cookie File") != 1 {
		t.Fatalf("ToNetscape = %q", got)
	}
}

func TestImport(t *testing.T) {
	dir := t.TempDir()

	// 未知站点保留请求头原文。
	path, err := Import(dir, "example", strings.NewReader("UIFID=u; ttwid=t"))
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(dir, "cookies", "example.txt")
	if path != wantPath {
		t.Fatalf("path = %q, want %q", path, wantPath)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "UIFID=u; ttwid=t\n" {
		t.Fatalf("content = %q", data)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("perm = %o, want 600", perm)
	}

	// 未知站点的 JSON 对象按键名排序转成 Cookie 头字符串。
	if _, err := Import(dir, "example", strings.NewReader(`{"web_session":"def","a1":"abc"}`)); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(data)); got != "a1=abc; web_session=def" {
		t.Fatalf("json content = %q", got)
	}

	// 需要 Netscape 的站点收到 Netscape 输入时原样保存。
	netscape := "# Netscape HTTP Cookie File\n.douyin.com\tTRUE\t/\tTRUE\t4070908800\tUIFID\tu1"
	if _, err := Import(dir, "douyin", strings.NewReader(netscape)); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(filepath.Join(dir, "cookies", "douyin.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != netscape+"\n" {
		t.Fatalf("netscape content = %q", data)
	}

	if _, err := Import(dir, "douyin", strings.NewReader("  \n")); err == nil {
		t.Fatal("empty input should fail")
	}
}

func TestCheckXiaohongshu(t *testing.T) {
	a1 := strings.Repeat("a", xhsA1MinLen)
	if issues := Check("a1="+a1+"; webId=w", "xiaohongshu"); len(issues) != 0 {
		t.Fatalf("complete cookie reported: %+v", issues)
	}

	issues := Check("webId=w", "xiaohongshu")
	if len(issues) != 1 || issues[0].Key != "a1" || !issues[0].Missing || issues[0].Message == "" {
		t.Fatalf("issues = %+v", issues)
	}

	// a1 存在但过短同样算缺口。
	issues = Check("a1=short; webId=x", "xiaohongshu")
	if len(issues) != 1 || issues[0].Key != "a1" {
		t.Fatalf("short a1 issues = %+v", issues)
	}

	// 空值 webId 不能顶替 web_session。
	issues = Check("a1="+a1+"; webId=", "xiaohongshu")
	if len(issues) != 1 || issues[0].Key != "webId/web_session" {
		t.Fatalf("empty webId issues = %+v", issues)
	}

	issues = Check("a1="+a1, "xiaohongshu")
	if len(issues) != 1 || issues[0].Key != "webId/web_session" || !issues[0].Missing {
		t.Fatalf("issues = %+v", issues)
	}

	if issues := Check("", "xiaohongshu"); len(issues) != 2 {
		t.Fatalf("empty cookie should report both gaps: %+v", issues)
	}

	// 长度恰好 30 位视为足量；29 位算缺口。
	if issues := Check("a1="+a1[:xhsA1MinLen-1]+"; web_session=s", "xiaohongshu"); len(issues) != 1 || issues[0].Key != "a1" {
		t.Fatalf("29-char a1 issues = %+v", issues)
	}
}

// TestHTTPOnlyNetscapeRowsAreData 验证浏览器/yt-dlp 导出的
// `#HttpOnly_` 行被当作数据而不是注释：丢掉的往往是登录态本身。
func TestHTTPOnlyNetscapeRowsAreData(t *testing.T) {
	text := strings.Join([]string{
		"# Netscape HTTP Cookie File",
		"#HttpOnly_.douyin.com\tTRUE\t/\tTRUE\t4070908800\tttwid\tt1",
		".douyin.com\tTRUE\t/\tTRUE\t4070908800\tUIFID\tu1",
	}, "\n")

	if got := Detect(text); got != Netscape {
		t.Fatalf("Detect = %v, want Netscape", got)
	}

	m, format, err := ParseMap(text)
	if err != nil || format != Netscape {
		t.Fatalf("format=%v err=%v", format, err)
	}
	if m["ttwid"] != "t1" || m["UIFID"] != "u1" {
		t.Fatalf("map=%v", m)
	}

	if got := ToHeader(text); got != "ttwid=t1; UIFID=u1" {
		t.Fatalf("ToHeader = %q", got)
	}

	if issues := Check(text, "douyin"); len(issues) != 0 {
		t.Fatalf("HttpOnly ttwid should satisfy the gate: %+v", issues)
	}
}

func TestCheckOtherSite(t *testing.T) {
	for _, site := range []string{"youtube", "bilibili", "twitter", "unknown"} {
		if issues := Check("", site); len(issues) != 0 {
			t.Fatalf("%s reported issues: %+v", site, issues)
		}
	}
}

func TestCheckTreatsEmptyValuesAsGaps(t *testing.T) {
	issues := Check("UIFID=; ttwid=", "douyin")
	if len(issues) != 2 {
		t.Fatalf("empty values should be two gaps: %+v", issues)
	}
	if issues[0].Key != "UIFID/UIFID_TEMP" || issues[1].Key != "ttwid/sessionid" {
		t.Fatalf("unexpected keys: %+v", issues)
	}

	if issues := Check("UIFID=u; ttwid=", "douyin"); len(issues) != 1 || issues[0].Key != "ttwid/sessionid" {
		t.Fatalf("issues = %+v", issues)
	}

	// 提示只提键名，绝不回显 Cookie 值。
	for _, issue := range Check("UIFID=secret-value; ttwid=", "douyin") {
		if strings.Contains(issue.Message, "secret-value") || strings.Contains(issue.Key, "=") {
			t.Fatalf("issue leaks value: %+v", issue)
		}
	}
}
