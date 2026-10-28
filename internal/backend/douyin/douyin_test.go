package douyin

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/backtraxe/downloader-go/internal/httpclient"
	"github.com/backtraxe/downloader-go/internal/result"
)

// fixedRNG 让 a_bogus 的噪声与伪造 msToken 可复现。
type fixedRNG struct{ value float64 }

func (r fixedRNG) Float64() float64 { return r.value }

func newTestClient() *httpclient.Client {
	return httpclient.New(httpclient.Options{Profile: httpclient.Chrome133})
}

func testDownloader(t *testing.T) *Downloader {
	t.Helper()
	return &Downloader{
		HTTP:    newTestClient(),
		DataDir: t.TempDir(),
		Now:     func() time.Time { return time.Unix(1791914678, 0) },
		RNG:     fixedRNG{value: 0.5},
		Sleep:   func(time.Duration) {},
	}
}

// makeCamelDetail 对应 Python tests/_make_camel_detail：新版 camelCase 结构。
func makeCamelDetail() map[string]any {
	return map[string]any{
		"awemeId":    "7666381907030529443",
		"desc":       "没关系 一个人也能走很远的路。#一个人拍照 #清冷感",
		"authorInfo": map[string]any{"nickname": "偏等落叶", "uniqueId": "photo_x"},
		"images": []any{
			map[string]any{
				"downloadUrlList": []any{"https://p3-sign.douyinpic.com/img1.jpg"},
				"urlList":         []any{"https://p3-sign.douyinpic.com/img1_small.jpg"},
			},
			map[string]any{"urlList": []any{"https://p3-sign.douyinpic.com/img2.jpg"}},
			map[string]any{
				"urlList": []any{"https://p3-sign.douyinpic.com/img3.jpg"},
				"video":   map[string]any{"playAddr": map[string]any{"urlList": []any{"https://v.example.com/live3.mp4"}}},
			},
		},
		"video": map[string]any{"playAddr": map[string]any{"urlList": []any{"https://v.example.com/note.mp4"}}},
	}
}

// makeSnakeDetail 对应 Python tests/_make_snake_detail：旧版 snake_case 结构。
func makeSnakeDetail() map[string]any {
	return map[string]any{
		"aweme_id": "7666381907030529443",
		"desc":     "旧结构笔记",
		"author":   map[string]any{"nickname": "旧作者", "unique_id": "old"},
		"images": []any{
			map[string]any{
				"download_url_list": []any{"http://p3.example.com/old1.jpg"},
				"url_list":          []any{"http://p3.example.com/old1_small.jpg"},
			},
		},
		"video": map[string]any{"play_addr": map[string]any{"url_list": []any{"http://v.example.com/old.mp4"}}},
	}
}

// makePaceFHTML 构造与 Python tests/_make_pace_f_html 等价的 pace_f 数据块：
// JS 字符串内 JSON 转义两层。
func makePaceFHTML(detail map[string]any) string {
	inner, err := json.Marshal(map[string]any{"statusCode": 0, "detail": detail})
	if err != nil {
		panic(err)
	}
	flight := fmt.Sprintf(`7:["$","$L9",null,{"awemeId":%q,"aweme":%s}]`, detail["awemeId"], inner)
	chunk := strings.ReplaceAll(flight, `\`, `\\`)
	chunk = strings.ReplaceAll(chunk, `"`, `\"`)
	return `<html><body><script>self.__pace_f.push([1,"` + chunk + `"])</script></body></html>`
}

func writePNG(w http.ResponseWriter) {
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	w.Header().Set("Content-Type", "image/png")
	_ = png.Encode(w, img)
}

func TestURLChecks(t *testing.T) {
	notes := []string{
		"https://www.douyin.com/note/7666381907030529443",
		"https://www.douyin.com/note/7666381907030529443?previous_page=web_code_link",
		"http://douyin.com/note/12345",
	}
	for _, raw := range notes {
		if !IsNoteURL(raw) {
			t.Fatalf("note url not recognized: %s", raw)
		}
	}
	videos := []string{
		"https://www.douyin.com/video/7666381907030529443",
		"http://douyin.com/video/12345",
	}
	for _, raw := range videos {
		if !IsVideoURL(raw) {
			t.Fatalf("video url not recognized: %s", raw)
		}
	}
	notNotes := []string{
		"https://www.douyin.com/video/7666381907030529443",
		"https://www.douyin.com/user/MS4wLjA",
		"",
	}
	for _, raw := range notNotes {
		if IsNoteURL(raw) {
			t.Fatalf("unexpected note url: %s", raw)
		}
	}
	if IsVideoURL("https://www.douyin.com/note/123") {
		t.Fatal("note url treated as video")
	}
}

func TestDetectChallenge(t *testing.T) {
	jsvm := `<html><body><script>var glb;(glb="undefined"==typeof window?global:window)._$jsvmprt=function(b,e,f){}</script></body></html>`
	if got := DetectChallenge(jsvm); got != JSVMChallenge {
		t.Fatalf("jsvm: got %v", got)
	}
	if got := DetectChallenge("<title>验证码中间页</title>"); got != CaptchaChallenge {
		t.Fatalf("captcha: got %v", got)
	}
	if got := DetectChallenge("<html><body><title>抖音图文</title></body></html>"); got != NoChallenge {
		t.Fatalf("normal: got %v", got)
	}
	if got := DetectChallenge(""); got != NoChallenge {
		t.Fatalf("empty: got %v", got)
	}
}

func TestParsePaceF(t *testing.T) {
	aweme, err := ParsePaceF(makePaceFHTML(makeCamelDetail()))
	if err != nil {
		t.Fatal(err)
	}
	if aweme.ID != "7666381907030529443" {
		t.Fatalf("id: %q", aweme.ID)
	}
	if aweme.Desc == "" || aweme.Author != "偏等落叶" {
		t.Fatalf("desc/author: %+v", aweme)
	}
	if len(aweme.Images) != 3 {
		t.Fatalf("images: %+v", aweme.Images)
	}
	if aweme.Images[0].URL != "https://p3-sign.douyinpic.com/img1.jpg" {
		t.Fatalf("image0: %+v", aweme.Images[0])
	}
	if len(aweme.LivePairs) != 1 || aweme.LivePairs[0].VideoURL != "https://v.example.com/live3.mp4" {
		t.Fatalf("live pairs: %+v", aweme.LivePairs)
	}
	if aweme.VideoURL != "https://v.example.com/note.mp4" {
		t.Fatalf("video: %q", aweme.VideoURL)
	}
}

func TestParsePaceFSkipsBrokenChunk(t *testing.T) {
	bad := `<script>self.__pace_f.push([1,"bad{unclosed"])</script>`
	aweme, err := ParsePaceF(bad + makePaceFHTML(makeCamelDetail()))
	if err != nil || aweme == nil {
		t.Fatalf("expected good chunk to parse: %v", err)
	}
}

func TestParsePaceFRenderData(t *testing.T) {
	payload, err := json.Marshal(map[string]any{"aweme": map[string]any{"detail": makeSnakeDetail()}})
	if err != nil {
		t.Fatal(err)
	}
	html := `<html><body><script id="RENDER_DATA" type="application/json">` +
		url.PathEscape(string(payload)) + `</script></body></html>`
	aweme, err := ParsePaceF(html)
	if err != nil {
		t.Fatal(err)
	}
	if aweme.ID != "7666381907030529443" || aweme.Author != "旧作者" || aweme.Desc != "旧结构笔记" {
		t.Fatalf("%+v", aweme)
	}
	if len(aweme.Images) != 1 || aweme.Images[0].URL != "http://p3.example.com/old1.jpg" {
		t.Fatalf("images: %+v", aweme.Images)
	}
}

func TestParsePaceFRouterData(t *testing.T) {
	payload, err := json.Marshal(map[string]any{"aweme": map[string]any{"detail": makeSnakeDetail()}})
	if err != nil {
		t.Fatal(err)
	}
	html := `<html><script>window._ROUTER_DATA = ` + string(payload) + `</script></html>`
	aweme, err := ParsePaceF(html)
	if err != nil {
		t.Fatal(err)
	}
	if aweme.ID != "7666381907030529443" {
		t.Fatalf("%+v", aweme)
	}
}

func TestParsePaceFReturnsErrorOnNoDetail(t *testing.T) {
	if _, err := ParsePaceF("<html><body>nothing</body></html>"); err == nil {
		t.Fatal("expected error")
	}
}

func TestParseDetailJSONCamelAndSnake(t *testing.T) {
	for name, detail := range map[string]map[string]any{
		"camel": makeCamelDetail(),
		"snake": makeSnakeDetail(),
	} {
		t.Run(name, func(t *testing.T) {
			body, err := json.Marshal(detail)
			if err != nil {
				t.Fatal(err)
			}
			aweme, err := ParseDetailJSON(body)
			if err != nil {
				t.Fatal(err)
			}
			if aweme.ID != "7666381907030529443" {
				t.Fatalf("id: %+v", aweme)
			}
			if len(aweme.Images) == 0 || aweme.Images[0].URL == "" {
				t.Fatalf("images: %+v", aweme.Images)
			}
			if aweme.VideoURL == "" {
				t.Fatalf("video: %+v", aweme)
			}
			if aweme.Author == "" || aweme.Desc == "" {
				t.Fatalf("author/desc: %+v", aweme)
			}
		})
	}
}

func TestParseDetailJSONEnvelope(t *testing.T) {
	body, err := json.Marshal(map[string]any{"status_code": 0, "aweme_detail": makeCamelDetail()})
	if err != nil {
		t.Fatal(err)
	}
	aweme, err := ParseDetailJSON(body)
	if err != nil {
		t.Fatal(err)
	}
	if aweme.ID != "7666381907030529443" || aweme.Author != "偏等落叶" {
		t.Fatalf("%+v", aweme)
	}
}

func TestImageSelectionSkipsWatermarkAndPrefersJPEG(t *testing.T) {
	detail := map[string]any{
		"awemeId": "1",
		"images": []any{map[string]any{
			"downloadUrlList": []any{"https://p3.example.com/a~tplv-dy-water.jpeg"},
			"urlList": []any{
				"https://p3.example.com/a~tplv-dy-aweme-images:q75.webp",
				"https://p3.example.com/a~tplv-dy-aweme-images:q75.jpeg",
			},
		}},
	}
	body, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	aweme, err := ParseDetailJSON(body)
	if err != nil {
		t.Fatal(err)
	}
	want := "https://p3.example.com/a~tplv-dy-aweme-images:q75.jpeg"
	if len(aweme.Images) != 1 || aweme.Images[0].URL != want {
		t.Fatalf("got %+v want %s", aweme.Images, want)
	}
}

func TestDownloadNoteWritesNumberedImages(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/note/7666381907030529443":
			if !strings.Contains(r.Header.Get("Cookie"), "UIFID=x") {
				t.Errorf("note request missing cookie: %q", r.Header.Get("Cookie"))
			}
			detail := map[string]any{
				"awemeId":    "7666381907030529443",
				"desc":       "一张图笔记",
				"authorInfo": map[string]any{"nickname": "作者甲"},
				"images": []any{
					map[string]any{"urlList": []any{srv.URL + "/img1.png"}},
				},
			}
			fmt.Fprint(w, makePaceFHTML(detail))
		case "/img1.png":
			writePNG(w)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	d := testDownloader(t)
	rawURL := srv.URL + "/note/7666381907030529443"
	res := d.DownloadNote(context.Background(), rawURL, "UIFID=x; ttwid=y", false)
	if res.Status != result.Downloaded {
		t.Fatalf("status %s msg %s", res.Status, res.Message)
	}
	if len(res.Posts) != 1 {
		t.Fatalf("posts: %+v", res.Posts)
	}
	post := res.Posts[0]
	if post.Site != "douyin" || post.Author != "作者甲" || post.Title != "一张图笔记" {
		t.Fatalf("post: %+v", post)
	}
	want := filepath.Join(d.DataDir, "download", "douyin", "作者甲", "一张图笔记_7666381907030529443", "001.png")
	if len(post.Files) != 1 || post.Files[0] != want {
		t.Fatalf("files: %+v want %s", post.Files, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatal(err)
	}

	second := d.DownloadNote(context.Background(), rawURL, "UIFID=x; ttwid=y", false)
	if second.Status != result.Skipped {
		t.Fatalf("second status %s msg %s", second.Status, second.Message)
	}
	if len(second.Posts) != 1 || len(second.Posts[0].Files) != 0 {
		t.Fatalf("skipped files: %+v", second.Posts)
	}
}

func TestDownloadNoteWritesLiveAndNoteVideo(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/note/7666381907030529443":
			detail := map[string]any{
				"awemeId":    "7666381907030529443",
				"desc":       "多图笔记",
				"authorInfo": map[string]any{"nickname": "作者乙"},
				"images": []any{
					map[string]any{"urlList": []any{srv.URL + "/img1.png"}},
					map[string]any{"urlList": []any{srv.URL + "/img2.png"}},
					map[string]any{
						"urlList": []any{srv.URL + "/img3.png"},
						"video":   map[string]any{"playAddr": map[string]any{"urlList": []any{srv.URL + "/live3.mp4"}}},
					},
				},
				"video": map[string]any{"playAddr": map[string]any{"urlList": []any{srv.URL + "/note.mp4"}}},
			}
			fmt.Fprint(w, makePaceFHTML(detail))
		case "/img1.png", "/img2.png", "/img3.png":
			writePNG(w)
		case "/live3.mp4", "/note.mp4":
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write([]byte{0, 0, 0, 24, 'f', 't', 'y', 'p'})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	d := testDownloader(t)
	res := d.DownloadNote(context.Background(), srv.URL+"/note/7666381907030529443", "UIFID=x", false)
	if res.Status != result.Downloaded {
		t.Fatalf("status %s msg %s", res.Status, res.Message)
	}
	dir := filepath.Join(d.DataDir, "download", "douyin", "作者乙", "多图笔记_7666381907030529443")
	for _, name := range []string{"001.png", "002.png", "003.png", "003.mp4", "video.mp4"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
	if len(res.Posts[0].Files) != 5 {
		t.Fatalf("files: %+v", res.Posts[0].Files)
	}
}

const jsvmPage = `<html><body><script>var glb;(glb="undefined"==typeof window?global:window)._$jsvmprt=function(){}</script></body></html>`

func serveDetailJSON(w http.ResponseWriter, detail map[string]any) {
	_ = json.NewEncoder(w).Encode(map[string]any{"status_code": 0, "aweme_detail": detail})
}

func TestDownloadNoteJSVMFails(t *testing.T) {
	oldBase := detailAPIBase
	defer func() { detailAPIBase = oldBase }()

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/note/7666381907030529443":
			fmt.Fprint(w, jsvmPage)
		case "/detail":
			// 签名兜底也被 Argus 拒绝：确定性失败，不应重试。
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, "Blocked by ArgusSecurityPlugin Uifid Not Found")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	detailAPIBase = srv.URL + "/detail"

	d := testDownloader(t)
	res := d.DownloadNote(context.Background(), srv.URL+"/note/7666381907030529443", "UIFID=x", false)
	if res.Status != result.Failed {
		t.Fatalf("status %s", res.Status)
	}
	if !strings.Contains(res.Message, "Cookie") {
		t.Fatalf("message: %s", res.Message)
	}
}

func TestDownloadNoteJSVMFallsBackToDetailAPI(t *testing.T) {
	oldBase := detailAPIBase
	defer func() { detailAPIBase = oldBase }()

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/note/7666381907030529443":
			fmt.Fprint(w, jsvmPage)
		case "/detail":
			serveDetailJSON(w, map[string]any{
				"awemeId":    "7666381907030529443",
				"desc":       "兜底笔记",
				"authorInfo": map[string]any{"nickname": "兜底作者"},
				"images": []any{
					map[string]any{"urlList": []any{srv.URL + "/img1.png"}},
				},
			})
		case "/img1.png":
			writePNG(w)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	detailAPIBase = srv.URL + "/detail"

	d := testDownloader(t)
	res := d.DownloadNote(context.Background(), srv.URL+"/note/7666381907030529443", "UIFID=x; ttwid=y", false)
	if res.Status != result.Downloaded {
		t.Fatalf("status %s msg %s", res.Status, res.Message)
	}
	if len(res.Posts) != 1 || len(res.Posts[0].Files) == 0 {
		t.Fatalf("posts: %+v", res.Posts)
	}
	want := filepath.Join(d.DataDir, "download", "douyin", "兜底作者", "兜底笔记_7666381907030529443", "001.png")
	if _, err := os.Stat(want); err != nil {
		t.Fatal(err)
	}
}

func TestDownloadNoteJSVMFailsWhenDetailAlsoFails(t *testing.T) {
	oldBase := detailAPIBase
	defer func() { detailAPIBase = oldBase }()

	var detailHits int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/note/7666381907030529443":
			fmt.Fprint(w, jsvmPage)
		case "/detail":
			atomic.AddInt32(&detailHits, 1)
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	detailAPIBase = srv.URL + "/detail"

	d := testDownloader(t)
	res := d.DownloadNote(context.Background(), srv.URL+"/note/7666381907030529443", "UIFID=x; ttwid=y", false)
	if res.Status != result.Failed {
		t.Fatalf("status %s", res.Status)
	}
	if !strings.Contains(res.Message, "Cookie") {
		t.Fatalf("message: %s", res.Message)
	}
	if got := atomic.LoadInt32(&detailHits); got != 3 {
		t.Fatalf("detail attempts: %d want 3", got)
	}
}

func TestDownloadNoteCaptchaFails(t *testing.T) {
	oldBase := detailAPIBase
	defer func() { detailAPIBase = oldBase }()

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/note/7666381907030529443":
			fmt.Fprint(w, `<html><head><title>验证码中间页</title></head><body></body></html>`)
		case "/detail":
			w.WriteHeader(http.StatusForbidden)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	detailAPIBase = srv.URL + "/detail"

	d := testDownloader(t)
	res := d.DownloadNote(context.Background(), srv.URL+"/note/7666381907030529443", "UIFID=x", false)
	if res.Status != result.Failed || !strings.Contains(res.Message, "验证码") {
		t.Fatalf("status %s msg %s", res.Status, res.Message)
	}
}

func TestDownloadVideoSignedRequest(t *testing.T) {
	oldBase := detailAPIBase
	defer func() { detailAPIBase = oldBase }()

	var seenQuery url.Values
	var seenSig, seenUIFID, seenExpire string
	var hits int32

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/detail":
			atomic.AddInt32(&hits, 1)
			seenQuery = r.URL.Query()
			seenSig = r.Header.Get("x-secsdk-web-signature")
			seenUIFID = r.Header.Get("uifid")
			seenExpire = r.Header.Get("x-secsdk-web-expire")
			detail := makeCamelDetail()
			detail["video"] = map[string]any{"play_addr": map[string]any{"url_list": []any{srv.URL + "/v.mp4"}}}
			_ = json.NewEncoder(w).Encode(map[string]any{"status_code": 0, "aweme_detail": detail})
		case "/v.mp4":
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write([]byte{0, 0, 0, 24, 'f', 't', 'y', 'p'})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	detailAPIBase = srv.URL + "/detail"

	d := testDownloader(t)
	res := d.DownloadVideo(context.Background(), "https://www.douyin.com/video/7666381907030529443", "UIFID=uid123; ttwid=t; msToken=token123", false)
	if res.Status != result.Downloaded {
		t.Fatalf("status %s msg %s", res.Status, res.Message)
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("detail hits: %d", hits)
	}

	if got := seenQuery.Get("device_platform"); got != "webapp" {
		t.Fatalf("device_platform: %q", got)
	}
	if got := seenQuery.Get("aid"); got != "6383" {
		t.Fatalf("aid: %q", got)
	}
	if got := seenQuery.Get("aweme_id"); got != "7666381907030529443" {
		t.Fatalf("aweme_id: %q", got)
	}
	if got := seenQuery.Get("msToken"); got != "token123" {
		t.Fatalf("msToken: %q", got)
	}
	if seenQuery.Get("a_bogus") == "" {
		t.Fatal("missing a_bogus")
	}
	if got := seenQuery.Get("uifid"); got != "uid123" {
		t.Fatalf("uifid query: %q", got)
	}
	if got := seenQuery.Get("timestamp"); got != "1791914678" {
		t.Fatalf("timestamp query: %q", got)
	}
	if len(seenSig) != 32 {
		t.Fatalf("signature header: %q", seenSig)
	}
	if seenUIFID != "uid123" || seenExpire != "1791914678" {
		t.Fatalf("headers uifid=%q expire=%q", seenUIFID, seenExpire)
	}

	if len(res.Posts) != 1 {
		t.Fatalf("posts: %+v", res.Posts)
	}
	post := res.Posts[0]
	if post.Site != "douyin" || post.Author != "偏等落叶" {
		t.Fatalf("post: %+v", post)
	}
	if len(post.Files) != 1 {
		t.Fatalf("files: %+v", post.Files)
	}
	wantDir := filepath.Join(d.DataDir, "download", "douyin", "偏等落叶")
	if filepath.Dir(post.Files[0]) != wantDir {
		t.Fatalf("file dir: %s want %s", filepath.Dir(post.Files[0]), wantDir)
	}
	if !strings.HasSuffix(post.Files[0], "_7666381907030529443.mp4") {
		t.Fatalf("file name: %s", post.Files[0])
	}
	if _, err := os.Stat(post.Files[0]); err != nil {
		t.Fatal(err)
	}
}

func TestDownloadVideoMissingUIFID(t *testing.T) {
	d := testDownloader(t)
	res := d.DownloadVideo(context.Background(), "https://www.douyin.com/video/123", "ttwid=t; msToken=m", false)
	if res.Status != result.Failed {
		t.Fatalf("status %s", res.Status)
	}
	if !strings.Contains(res.Message, "UIFID") {
		t.Fatalf("message: %s", res.Message)
	}
}

func TestDownloadVideoForbidden(t *testing.T) {
	oldBase := detailAPIBase
	defer func() { detailAPIBase = oldBase }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, "Blocked by ArgusSecurityPlugin Uifid Not Found")
	}))
	defer srv.Close()
	detailAPIBase = srv.URL + "/detail"

	d := testDownloader(t)
	res := d.DownloadVideo(context.Background(), "https://www.douyin.com/video/123", "UIFID=uid; ttwid=t", false)
	if res.Status != result.Failed || !strings.Contains(res.Message, "UIFID") {
		t.Fatalf("status %s msg %s", res.Status, res.Message)
	}
}

func TestDownloadVideoEmptyBody(t *testing.T) {
	oldBase := detailAPIBase
	defer func() { detailAPIBase = oldBase }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	detailAPIBase = srv.URL + "/detail"

	d := testDownloader(t)
	res := d.DownloadVideo(context.Background(), "https://www.douyin.com/video/123", "UIFID=uid; ttwid=t", false)
	if res.Status != result.Failed || !strings.Contains(res.Message, "Cookie") {
		t.Fatalf("status %s msg %s", res.Status, res.Message)
	}
}

func TestDownloadVideoNonJSON(t *testing.T) {
	oldBase := detailAPIBase
	defer func() { detailAPIBase = oldBase }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "<html>blocked</html>")
	}))
	defer srv.Close()
	detailAPIBase = srv.URL + "/detail"

	d := testDownloader(t)
	res := d.DownloadVideo(context.Background(), "https://www.douyin.com/video/123", "UIFID=uid; ttwid=t", false)
	if res.Status != result.Failed || !strings.Contains(res.Message, "JSON") {
		t.Fatalf("status %s msg %s", res.Status, res.Message)
	}
}

func TestDetailAPIRetriesEmptyResponse(t *testing.T) {
	oldBase := detailAPIBase
	defer func() { detailAPIBase = oldBase }()

	var hits int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/detail":
			if atomic.AddInt32(&hits, 1) == 1 {
				w.WriteHeader(http.StatusOK)
				return
			}
			detail := makeCamelDetail()
			detail["video"] = map[string]any{"play_addr": map[string]any{"url_list": []any{srv.URL + "/v.mp4"}}}
			serveDetailJSON(w, detail)
		case "/v.mp4":
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write([]byte{0, 0, 0, 24, 'f', 't', 'y', 'p'})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	detailAPIBase = srv.URL + "/detail"

	d := testDownloader(t)
	res := d.DownloadVideo(context.Background(), "https://www.douyin.com/video/7666381907030529443", "UIFID=uid; ttwid=t", false)
	if res.Status != result.Downloaded {
		t.Fatalf("status %s msg %s", res.Status, res.Message)
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Fatalf("detail attempts: %d want 2", got)
	}
	if len(res.Posts) != 1 || len(res.Posts[0].Files) != 1 {
		t.Fatalf("posts: %+v", res.Posts)
	}
}

func TestPageRetriesTransient(t *testing.T) {
	var hits int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/note/7666381907030529443":
			if atomic.AddInt32(&hits, 1) == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			detail := map[string]any{
				"awemeId":    "7666381907030529443",
				"desc":       "重试笔记",
				"authorInfo": map[string]any{"nickname": "重试作者"},
				"images": []any{
					map[string]any{"urlList": []any{srv.URL + "/img1.png"}},
				},
			}
			fmt.Fprint(w, makePaceFHTML(detail))
		case "/img1.png":
			writePNG(w)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	d := testDownloader(t)
	res := d.DownloadNote(context.Background(), srv.URL+"/note/7666381907030529443", "UIFID=x; ttwid=y", false)
	if res.Status != result.Downloaded {
		t.Fatalf("status %s msg %s", res.Status, res.Message)
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Fatalf("page attempts: %d want 2", got)
	}
	want := filepath.Join(d.DataDir, "download", "douyin", "重试作者", "重试笔记_7666381907030529443", "001.png")
	if _, err := os.Stat(want); err != nil {
		t.Fatal(err)
	}
}
