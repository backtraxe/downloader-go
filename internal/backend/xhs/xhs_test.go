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

func TestDownloadWritesNumberedImages(t *testing.T) {
	var serverURL string
	mux := http.NewServeMux()
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
	mux.HandleFunc("/img/", func(w http.ResponseWriter, r *http.Request) {
		writePNG(t, w)
	})
	server := newTLSServer(t, mux)
	serverURL = server.URL

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

func TestExpiredShortLinkMessage(t *testing.T) {
	server := newTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, pageHTML(t, map[string]any{
			"note": map[string]any{"noteDetailMap": map[string]any{}},
		}))
	}))

	d := newTestDownloader(t)
	res := d.Download(context.Background(), server.URL+"/?xsec_token=expired", "a1=abc", false)
	if res.Status != result.Failed {
		t.Fatalf("status=%s want failed", res.Status)
	}
	if !strings.Contains(res.Message, "失效") {
		t.Fatalf("message=%q want 失效", res.Message)
	}
}

func TestMissingTokenMessage(t *testing.T) {
	d := newTestDownloader(t)
	res := d.Download(context.Background(), "https://www.xiaohongshu.com/explore/1", "a1=abc", false)
	if res.Status != result.Failed {
		t.Fatalf("status=%s want failed", res.Status)
	}
	if !strings.Contains(res.Message, "xsec_token") {
		t.Fatalf("message=%q want xsec_token hint", res.Message)
	}
}
