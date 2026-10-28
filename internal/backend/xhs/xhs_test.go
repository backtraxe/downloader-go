package xhs

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/backtraxe/downloader-go/internal/httpclient"
	"github.com/backtraxe/downloader-go/internal/result"
)

func TestHasXsecToken(t *testing.T) {
	cases := []struct {
		url  string
		want bool
	}{
		{"https://www.xiaohongshu.com/explore/1?xsec_token=abc", true},
		{"https://www.xiaohongshu.com/explore/1?xsec_token=abc&xsec_source=pc_feed", true},
		{"https://www.xiaohongshu.com/explore/1", false},
		{"https://www.xiaohongshu.com/explore/1?xsec_token=", false},
		{"https://xhslink.com/a/abc", false},
	}
	for _, tc := range cases {
		if got := HasXsecToken(tc.url); got != tc.want {
			t.Fatalf("HasXsecToken(%q)=%v want %v", tc.url, got, tc.want)
		}
	}
}

func TestNormalizeUndefined(t *testing.T) {
	in := `{"a":undefined,"b":["x",undefined],"c":"undefined"}`
	out := NormalizeUndefined(in)
	if out != `{"a":null,"b":["x",null],"c":"undefined"}` {
		t.Fatalf("%s", out)
	}

	// 值语境带空白也要替换；正文/URL 中的 undefined 保持不变。
	in = `{"a": undefined, "desc":"this_is_undefined", "url":"https://x/undefined"}`
	out = NormalizeUndefined(in)
	if strings.Contains(out, `"a": undefined`) {
		t.Fatalf("value-position undefined not replaced: %s", out)
	}
	if !strings.Contains(out, `this_is_undefined`) || !strings.Contains(out, `https://x/undefined`) {
		t.Fatalf("string content was modified: %s", out)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("normalized JSON invalid: %v (%s)", err, out)
	}
}

func TestNormalizeJSLiterals(t *testing.T) {
	in := `{"when":new Date(1793201339151),"map":new Map([["a",1]]),` +
		`"set":new Set([1,2]),"url":new URL("https://x"),` +
		`"text":"a new thing","nan":NaN,"inf":Infinity,"ninf":-Infinity}`
	out := NormalizeUndefined(in)
	var decoded map[string]any
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("normalized JSON invalid: %v (%s)", err, out)
	}
	if decoded["text"] != "a new thing" {
		t.Fatalf("string content changed: %v", decoded["text"])
	}
}

func TestParseInitialStateTrailingScript(t *testing.T) {
	html := `<script>window.__INITIAL_STATE__ = {"note":{"noteDetailMap":{"3":{"note":{` +
		`"noteId":"3","title":"t","user":{"nickname":"u"},"imageList":[]}}}}};` +
		`window.__OTHER__ = {"ignored":true}</script>`
	note, err := ParseInitialState(html)
	if err != nil {
		t.Fatalf("ParseInitialState: %v", err)
	}
	if note.ID != "3" {
		t.Fatalf("note=%+v", note)
	}
}

func TestParseInitialState(t *testing.T) {
	html := `<html><script>window.__INITIAL_STATE__ = ` +
		`{"note":{"noteDetailMap":{"1":{"note":{` +
		`"noteId":"1","title":"t","desc":"d","user":{"nickname":"u"},` +
		`"imageList":[{"urlDefault":"https://x/1.jpg"},{"urlPre":"https://x/2.webp"}]` +
		`}}}}}</script></html>`

	note, err := ParseInitialState(html)
	if err != nil {
		t.Fatalf("ParseInitialState: %v", err)
	}
	if note.ID != "1" || note.Title != "t" || note.Desc != "d" || note.Author != "u" {
		t.Fatalf("unexpected note: %+v", note)
	}
	if len(note.Images) != 2 {
		t.Fatalf("images=%d want 2", len(note.Images))
	}
	if note.Images[0].URL != "https://x/1.jpg" || note.Images[1].URL != "https://x/2.webp" {
		t.Fatalf("unexpected image urls: %+v", note.Images)
	}
	if note.VideoURL != "" || len(note.LivePairs) != 0 {
		t.Fatalf("unexpected video/live: %+v", note)
	}
}

// TestParseInitialStateUndefined 验证解析前会先归一化 undefined。
func TestParseInitialStateUndefined(t *testing.T) {
	html := `<script>window.__INITIAL_STATE__ = {"note":{"noteDetailMap":{"7":{"note":{` +
		`"noteId":"7","title":undefined,"desc":undefined,"video":undefined,` +
		`"user":undefined,"imageList":undefined` +
		`}}}}}</script>`
	note, err := ParseInitialState(html)
	if err != nil {
		t.Fatalf("ParseInitialState: %v", err)
	}
	if note.ID != "7" || note.Title != "" || len(note.Images) != 0 {
		t.Fatalf("unexpected note: %+v", note)
	}
}

// TestParseInitialStateLiveAndVideo 验证实况图配对与 h264 最高画质选择。
func TestParseInitialStateLiveAndVideo(t *testing.T) {
	html := `<script>window.__INITIAL_STATE__ = {"note":{"noteDetailMap":{"9":{"note":{` +
		`"noteId":"9","title":"live",` +
		`"imageList":[{"urlDefault":"https://x/1.heic","livePhoto":true,"stream":{` +
		`"EF4":[{"masterUrl":"https://x/1_sd.mp4","qualityType":"SD"},` +
		`{"masterUrl":"https://x/1_hd.mp4","qualityType":"HD"}]}},` +
		`{"urlDefault":"https://x/2.jpg"}],` +
		`"video":{"media":{"stream":{"h264":[` +
		`{"masterUrl":"https://x/v_sd.mp4","qualityType":"SD","videoBitrate":100},` +
		`{"masterUrl":"https://x/v_hd.mp4","qualityType":"HD","videoBitrate":200}]}}}` +
		`}}}}}</script>`

	note, err := ParseInitialState(html)
	if err != nil {
		t.Fatalf("ParseInitialState: %v", err)
	}
	if len(note.LivePairs) != 1 {
		t.Fatalf("live pairs=%d want 1", len(note.LivePairs))
	}
	if note.LivePairs[0].ImageURL != "https://x/1.heic" ||
		note.LivePairs[0].VideoURL != "https://x/1_hd.mp4" {
		t.Fatalf("unexpected live pair: %+v", note.LivePairs[0])
	}
	if note.VideoURL != "https://x/v_hd.mp4" {
		t.Fatalf("video=%q want highest quality h264", note.VideoURL)
	}
}

// TestParseInitialStateVideoFallbacks 覆盖新笔记只给 h265/av1，以及
// h264 档只有 backupUrls 的情况：都必须能取到视频直链。
func TestParseInitialStateVideoFallbacks(t *testing.T) {
	cases := []struct {
		name    string
		streams string
		want    string
	}{
		{
			name: "h264 无直链时回退 h265",
			streams: `"h264":[{"masterUrl":"","qualityType":"HD"}],` +
				`"h265":[{"masterUrl":"https://x/v_h265.mp4","qualityType":"HD"}]`,
			want: "https://x/v_h265.mp4",
		},
		{
			name:    "只有 av1",
			streams: `"av1":[{"masterUrl":"https://x/v_av1.mp4","qualityType":"HD"}]`,
			want:    "https://x/v_av1.mp4",
		},
		{
			name: "masterUrl 为空时用 backupUrls",
			streams: `"h264":[{"masterUrl":"","backupUrls":["https://x/v_backup.mp4"],` +
				`"qualityType":"HD"}]`,
			want: "https://x/v_backup.mp4",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			html := `<script>window.__INITIAL_STATE__ = {"note":{"noteDetailMap":{"v1":{"note":{` +
				`"noteId":"v1","imageList":[{"urlDefault":"https://x/cover.jpg"}],` +
				`"video":{"media":{"stream":{` + tc.streams + `}}}}}}}}}</script>`
			note, err := ParseInitialState(html)
			if err != nil {
				t.Fatalf("ParseInitialState: %v", err)
			}
			if note.VideoURL != tc.want {
				t.Fatalf("video=%q want %q", note.VideoURL, tc.want)
			}
		})
	}
}

func TestParseInitialStateNoNoteData(t *testing.T) {
	cases := []string{
		`<script>window.__INITIAL_STATE__ = {"note":{"noteDetailMap":{}}}</script>`,
		`<script>window.__INITIAL_STATE__ = {"note":{}}</script>`,
	}
	for _, html := range cases {
		if _, err := ParseInitialState(html); err != ErrNoNoteData {
			t.Fatalf("err=%v want ErrNoNoteData", err)
		}
	}
}

func TestHomepageLanding(t *testing.T) {
	if !IsHomepageLanding("https://www.xiaohongshu.com/", `{"note":{"noteDetailMap":{}}}`) {
		t.Fatal("homepage")
	}
	if !IsHomepageLanding("https://www.xiaohongshu.com/explore", `{}`) {
		t.Fatal("explore landing")
	}
	if IsHomepageLanding("https://www.xiaohongshu.com/explore/1", `{"note":{"noteDetailMap":{"1":{}}}}`) {
		t.Fatal("note page")
	}
	// 笔记页即使 noteDetailMap 为空，也不是首页落地（404 等情况另行提示）。
	if IsHomepageLanding("https://www.xiaohongshu.com/explore/1", `{"note":{"noteDetailMap":{}}}`) {
		t.Fatal("note path with empty map is not homepage landing")
	}
}

// newTLSServer 启动 TLS 测试服务器，并静默关闭时的握手噪声日志。
func newTLSServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	server := httptest.NewUnstartedServer(handler)
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	t.Cleanup(server.Close)
	return server
}

func writePNG(t *testing.T, w http.ResponseWriter) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	w.Header().Set("Content-Type", "image/png")
	if err := png.Encode(w, img); err != nil {
		t.Errorf("encode png: %v", err)
	}
}

// pageHTML 构造带 __INITIAL_STATE__ 的页面。
func pageHTML(t *testing.T, state map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	return `<html><body><script>window.__INITIAL_STATE__ = ` + string(raw) + `</script></body></html>`
}

func newTestDownloader(t *testing.T) *Downloader {
	t.Helper()
	return &Downloader{
		HTTP: httpclient.New(httpclient.Options{
			Profile:            httpclient.Chrome133,
			InsecureSkipVerify: true,
		}),
		DataDir: t.TempDir(),
	}
}

// newXSHSite 构造离线小红书测试站点。
//
// 测试用 127.0.0.1 代替 xhslink.com：非 xiaohongshu.com/rednote.com 的
// host 走“短链”路径（不做事前 token 预检），/short/note 模拟 App 分享
// 短链（token 只在 302 目标里），/short/expired 模拟过期短链落地首页。
func newXSHSite(t *testing.T) *httptest.Server {
	t.Helper()
	var serverURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/short/note", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, serverURL+"/explore/note1?xsec_token=fromshort", http.StatusFound)
	})
	mux.HandleFunc("/short/expired", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, serverURL+"/", http.StatusFound)
	})
	mux.HandleFunc("/explore/note1", func(w http.ResponseWriter, r *http.Request) {
		state := map[string]any{"note": map[string]any{"noteDetailMap": map[string]any{
			"note1": map[string]any{"note": map[string]any{
				"noteId": "note1",
				"title":  "测试标题",
				"desc":   "描述",
				"user":   map[string]any{"nickname": "作者"},
				"imageList": []any{
					map[string]any{"urlDefault": serverURL + "/img/1"},
					map[string]any{"urlPre": serverURL + "/img/2"},
				},
			}},
		}}}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, pageHTML(t, state))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, pageHTML(t, map[string]any{
			"note": map[string]any{"noteDetailMap": map[string]any{}},
		}))
	})
	mux.HandleFunc("/img/", func(w http.ResponseWriter, r *http.Request) {
		writePNG(t, w)
	})
	server := newTLSServer(t, mux)
	serverURL = server.URL
	return server
}

func TestDownloadWritesNumberedImages(t *testing.T) {
	server := newXSHSite(t)

	d := newTestDownloader(t)
	res := d.Download(context.Background(), server.URL+"/explore/note1?xsec_token=tok", "a1=abc", false)
	if res.Status != result.Downloaded {
		t.Fatalf("status=%s message=%s", res.Status, res.Message)
	}

	dir := filepath.Join(d.DataDir, "download", "xiaohongshu", "作者", "测试标题_note1")
	for _, name := range []string{"001.png", "002.png"} {
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("missing %s: %v", path, err)
		}
		if info.Size() == 0 {
			t.Fatalf("%s is empty", path)
		}
	}

	if len(res.Posts) != 1 {
		t.Fatalf("posts=%d want 1", len(res.Posts))
	}
	post := res.Posts[0]
	if post.Site != "xiaohongshu" || post.Author != "作者" || post.Title != "测试标题" {
		t.Fatalf("unexpected post: %+v", post)
	}
	if len(post.Files) != 2 {
		t.Fatalf("files=%v want 2", post.Files)
	}
	for _, file := range post.Files {
		if !strings.Contains(file, filepath.Join("download", "xiaohongshu")) {
			t.Fatalf("file %q not under download/xiaohongshu", file)
		}
	}
}

func TestShortLinkResolvesToNote(t *testing.T) {
	server := newXSHSite(t)

	// 短链本身不必带 token：token 在 302 目标的 query 里。
	d := newTestDownloader(t)
	res := d.Download(context.Background(), server.URL+"/short/note", "a1=abc", false)
	if res.Status != result.Downloaded {
		t.Fatalf("status=%s message=%s", res.Status, res.Message)
	}
	if len(res.Posts) != 1 || len(res.Posts[0].Files) != 2 {
		t.Fatalf("unexpected posts: %+v", res.Posts)
	}
}

// TestShortLinkAcrossHostsCarriesCookie 复现 Go 在跨域名重定向时丢弃
// 手动 Cookie 头的行为：短链必须先不带 Cookie 解析到最终地址，随后
// 笔记页请求再带上用户 Cookie。
func TestShortLinkAcrossHostsCarriesCookie(t *testing.T) {
	var shortCookie, noteCookie string
	var serverURL string

	mux := http.NewServeMux()
	mux.HandleFunc("/short/note", func(w http.ResponseWriter, r *http.Request) {
		shortCookie = r.Header.Get("Cookie")
		// 用另一个 host 名指向同一监听端口，制造跨域名重定向。
		target := strings.Replace(serverURL, "127.0.0.1", "localhost", 1) +
			"/explore/note1?xsec_token=fromshort"
		http.Redirect(w, r, target, http.StatusFound)
	})
	mux.HandleFunc("/explore/note1", func(w http.ResponseWriter, r *http.Request) {
		noteCookie = r.Header.Get("Cookie")
		state := map[string]any{"note": map[string]any{"noteDetailMap": map[string]any{
			"note1": map[string]any{"note": map[string]any{
				"noteId": "note1",
				"title":  "跨域",
				"user":   map[string]any{"nickname": "作者"},
				"imageList": []any{
					map[string]any{"urlDefault": serverURL + "/img/1"},
				},
			}},
		}}}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, pageHTML(t, state))
	})
	mux.HandleFunc("/img/", func(w http.ResponseWriter, r *http.Request) {
		writePNG(t, w)
	})
	server := newTLSServer(t, mux)
	serverURL = server.URL

	d := newTestDownloader(t)
	res := d.Download(context.Background(), serverURL+"/short/note", "a1=abc", false)
	if res.Status != result.Downloaded {
		t.Fatalf("status=%s message=%s", res.Status, res.Message)
	}
	if shortCookie != "" {
		t.Fatalf("short-link request must not carry cookies, got %q", shortCookie)
	}
	if !strings.Contains(noteCookie, "a1=abc") {
		t.Fatalf("final note request missing cookie: %q", noteCookie)
	}
}

func TestExpiredShortLinkMessage(t *testing.T) {
	server := newXSHSite(t)

	// 过期短链 302 到首页、noteDetailMap 为空：必须报“失效”，不能报 token。
	d := newTestDownloader(t)
	res := d.Download(context.Background(), server.URL+"/short/expired", "a1=abc", false)
	if res.Status != result.Failed {
		t.Fatalf("status=%s want failed", res.Status)
	}
	if !strings.Contains(res.Message, "失效") {
		t.Fatalf("message=%q want 失效", res.Message)
	}
	if strings.Contains(res.Message, "xsec_token") {
		t.Fatalf("message=%q must not mention xsec_token", res.Message)
	}
}

func TestDirectBareExploreNeedsToken(t *testing.T) {
	d := newTestDownloader(t)
	res := d.Download(context.Background(), "https://www.xiaohongshu.com/explore/1", "a1=abc", false)
	if res.Status != result.Failed {
		t.Fatalf("status=%s want failed", res.Status)
	}
	if !strings.Contains(res.Message, "xsec_token") {
		t.Fatalf("message=%q want xsec_token hint", res.Message)
	}
}
