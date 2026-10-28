package generic

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PuerkitoBio/goquery"

	"github.com/backtraxe/downloader-go/internal/httpclient"
	"github.com/backtraxe/downloader-go/internal/result"
	"github.com/backtraxe/downloader-go/internal/sites"
)

func TestExtractCandidates(t *testing.T) {
	html := `<html><head><title>正文 来自 作者甲 - 示例站</title></head><body>
      <img src="/a.jpg"><img data-src="//cdn.x/b.png"><img data-original="https://cdn.x/logo.png">
      <video src="/v.mp4"></video><a href="https://cdn.x/c.webp">x</a>
      <div style="background-image:url('/d.jpg')"></div></body></html>`
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		t.Fatal(err)
	}
	got := ExtractCandidates(doc, "https://site/p/1")
	if len(got) != 6 {
		t.Fatalf("%+v", got)
	}
	if got[1].URL != "https://cdn.x/b.png" {
		t.Fatalf("%+v", got)
	}
	if got[3].Kind != ".mp4" {
		t.Fatalf("video kind: %+v", got[3])
	}
}

func TestExtractAuthor(t *testing.T) {
	if got := ExtractAuthor("正文 来自 作者甲 - 示例站", "example.com"); got != "作者甲" {
		t.Fatal(got)
	}
	if got := ExtractAuthor("普通标题", "example.com"); got != "" {
		t.Fatal(got)
	}
	if got := ExtractAuthor("正文来自 作者乙-站点", "example.com"); got != "作者乙" {
		t.Fatal(got)
	}
}

func TestExtractTitle(t *testing.T) {
	cases := []struct {
		html string
		want string
	}{
		{`<html><head><title>  标题 - 站点  </title></head></html>`, "标题"},
		{`<html><head><title>a - b - c</title></head></html>`, "a - b"},
		{`<html><head><title>山间清晨 正文 来自 摄影师甲 - 站点</title></head></html>`, "山间清晨"},
		{`<html><head><title>正文 来自 作者甲 - 示例站</title></head></html>`, "untitled"},
		{`<html><head><title>只有标题</title></head></html>`, "只有标题"},
		{`<html><head><title>   </title></head></html>`, "untitled"},
		{`<html><head></head></html>`, "untitled"},
	}
	for _, tc := range cases {
		doc, err := goquery.NewDocumentFromReader(strings.NewReader(tc.html))
		if err != nil {
			t.Fatal(err)
		}
		if got := ExtractTitle(doc); got != tc.want {
			t.Fatalf("%s: got %q want %q", tc.html, got, tc.want)
		}
	}
}

func TestExtractCandidatesDedupesAndDropsDataURLs(t *testing.T) {
	html := `<html><body>
      <img src="https://cdn.x/a.jpg" data-src="https://cdn.x/a.jpg">
      <img src="data:image/png;base64,AAAA" srcset="https://cdn.x/b.jpg 1x, https://cdn.x/c.jpg 2x">
      <a href="/d.JPG?x=1">media</a>
      <a href="/page.jpg.html">page</a>
      <div style="background:url('/ignored.jpg'); background-image: url('//cdn.x/e.png')"></div>
    </body></html>`
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		t.Fatal(err)
	}
	got := ExtractCandidates(doc, "https://cdn.x/page")
	want := []string{
		"https://cdn.x/a.jpg",
		"https://cdn.x/b.jpg",
		"https://cdn.x/d.JPG?x=1",
		"https://cdn.x/e.png",
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i, url := range want {
		if got[i].URL != url {
			t.Fatalf("candidate %d: got %q want %q (%+v)", i, got[i].URL, url, got)
		}
	}
	if got[3].Kind != ".png" {
		t.Fatalf("style kind: %+v", got[3])
	}
}

func TestExtractCandidatesFromTestdata(t *testing.T) {
	data, err := os.ReadFile("testdata/page.html")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	got := ExtractCandidates(doc, "https://example.com/post/123")
	// 先扫描全部 img/source/video，再扫描 a[href]，最后扫描行内样式，
	// 因此 logo 图片排在 <a> 与背景图之前。
	want := []string{
		"https://cdn.example.com/two.png",
		"https://example.com/images/one.jpg",
		"https://cdn.example.com/three.webp",
		"https://example.com/media/clip.mp4",
		"https://example.com/assets/logo.png",
		"https://cdn.example.com/five.gif",
		"https://cdn.example.com/six.jpg",
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i, url := range want {
		if got[i].URL != url {
			t.Fatalf("candidate %d: got %q want %q", i, got[i].URL, url)
		}
	}
	if title := ExtractTitle(doc); title != "山间清晨" {
		t.Fatalf("title=%q", title)
	}
	rawTitle := doc.Find("title").First().Text()
	if author := ExtractAuthor(strings.TrimSpace(rawTitle), "example.com"); author != "摄影师甲" {
		t.Fatalf("author=%q", author)
	}
}

func TestDownloadFiltersDecorativeAndWritesNumbered(t *testing.T) {
	big := pngBytes(t, 400, 400)
	logo := pngBytes(t, 10, 10)

	var (
		mu       sync.Mutex
		referers []string
		agents   []string
	)
	mux := http.NewServeMux()
	mux.HandleFunc("/post/74208176", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><head><title>  测试标题 - 示例站  </title></head><body>
          <img src="/a.png"><img src="/logo.png"></body></html>`)
	})
	mux.HandleFunc("/a.png", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		referers = append(referers, r.Header.Get("Referer"))
		agents = append(agents, r.Header.Get("User-Agent"))
		mu.Unlock()
		w.Header().Set("Content-Type", "image/png")
		w.Write(big)
	})
	mux.HandleFunc("/logo.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(logo)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	pageURL := srv.URL + "/post/74208176"
	dataDir := t.TempDir()
	d := &Downloader{
		HTTP:    httpclient.New(httpclient.Options{Profile: httpclient.Chrome133}),
		DataDir: dataDir,
	}
	res := d.Download(context.Background(), pageURL, false)

	if res.Status != result.Downloaded {
		t.Fatalf("%+v", res)
	}
	if len(res.Posts) != 1 {
		t.Fatalf("%+v", res)
	}
	post := res.Posts[0]
	if post.Title != "测试标题" || post.Status != result.Downloaded {
		t.Fatalf("%+v", post)
	}
	if len(post.Files) != 1 || filepath.Base(post.Files[0]) != "001.png" {
		t.Fatalf("files=%v", post.Files)
	}
	rel, err := filepath.Rel(dataDir, post.Files[0])
	if err != nil {
		t.Fatal(err)
	}
	wantRel := filepath.Join("download", sites.Detect(pageURL), "测试标题_74208176", "001.png")
	if rel != wantRel {
		t.Fatalf("relative path=%q want %q", rel, wantRel)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(post.Files[0]), ".002.skip")); err == nil {
		t.Fatal("decorative name should not write a skip marker")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(post.Files[0]), "002.png")); err == nil {
		t.Fatal("decorative name should not be downloaded")
	}

	mu.Lock()
	gotReferers := append([]string(nil), referers...)
	gotAgents := append([]string(nil), agents...)
	mu.Unlock()
	if len(gotReferers) != 1 || gotReferers[0] != pageURL {
		t.Fatalf("referers=%v want %q", gotReferers, pageURL)
	}
	// 不覆盖 UA：客户端会补上与其 TLS profile 对齐的 UA。
	if len(gotAgents) != 1 || gotAgents[0] != httpclient.Chrome133.UserAgent {
		t.Fatalf("agents=%v want %q", gotAgents, httpclient.Chrome133.UserAgent)
	}
}

func TestSkipMarkerOnlySkipsSameURL(t *testing.T) {
	img := pngBytes(t, 400, 400)
	var requests atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/post/55555", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><head><title>标记 - 站</title></head><body>
          <img src="/new.png"></body></html>`)
	})
	mux.HandleFunc("/new.png", func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "image/png")
		w.Write(img)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	pageURL := srv.URL + "/post/55555"
	dirFor := func(dataDir string) string {
		return filepath.Join(dataDir, "download", sites.Detect(pageURL), "标记_55555")
	}

	t.Run("mismatched marker redownloads", func(t *testing.T) {
		dataDir := t.TempDir()
		dir := dirFor(dataDir)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		marker := filepath.Join(dir, ".001.skip")
		if err := os.WriteFile(marker, []byte("https://old.example/old.png\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		before := requests.Load()
		d := &Downloader{HTTP: httpclient.New(httpclient.Options{}), DataDir: dataDir}
		res := d.Download(context.Background(), pageURL, false)
		if res.Status != result.Downloaded || len(res.Posts[0].Files) != 1 {
			t.Fatalf("%+v", res)
		}
		if got := filepath.Base(res.Posts[0].Files[0]); got != "001.png" {
			t.Fatalf("file=%q", got)
		}
		if got := requests.Load(); got != before+1 {
			t.Fatalf("requests=%d want %d", got, before+1)
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("stale marker should be removed: %v", err)
		}
	})

	t.Run("matching marker skips", func(t *testing.T) {
		dataDir := t.TempDir()
		dir := dirFor(dataDir)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		marker := filepath.Join(dir, ".001.skip")
		candidateURL := srv.URL + "/new.png"
		if err := os.WriteFile(marker, []byte("  "+candidateURL+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		before := requests.Load()
		d := &Downloader{HTTP: httpclient.New(httpclient.Options{}), DataDir: dataDir}
		res := d.Download(context.Background(), pageURL, false)
		if res.Status != result.Skipped || len(res.Posts[0].Files) != 0 {
			t.Fatalf("%+v", res)
		}
		if got := requests.Load(); got != before {
			t.Fatalf("matching marker should not download: requests=%d", got-before)
		}
	})
}

func TestDecorativeNameInMiddleKeepsContiguousNumbers(t *testing.T) {
	big := pngBytes(t, 400, 400)
	logo := pngBytes(t, 10, 10)

	mux := http.NewServeMux()
	mux.HandleFunc("/post/77777", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><head><title>连续 - 站</title></head><body>
          <img src="/logo.png"><img src="/a.png"><img src="/b.png"></body></html>`)
	})
	image := func(data []byte) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "image/png")
			w.Write(data)
		}
	}
	mux.HandleFunc("/logo.png", image(logo))
	mux.HandleFunc("/a.png", image(big))
	mux.HandleFunc("/b.png", image(big))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	pageURL := srv.URL + "/post/77777"
	dataDir := t.TempDir()
	d := &Downloader{HTTP: httpclient.New(httpclient.Options{}), DataDir: dataDir}
	res := d.Download(context.Background(), pageURL, false)
	if res.Status != result.Downloaded || len(res.Posts) != 1 {
		t.Fatalf("%+v", res)
	}
	files := res.Posts[0].Files
	if len(files) != 2 {
		t.Fatalf("files=%v", files)
	}
	if filepath.Base(files[0]) != "001.png" || filepath.Base(files[1]) != "002.png" {
		t.Fatalf("files=%v", files)
	}
	dir := filepath.Dir(files[0])
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "003") {
			t.Fatalf("decorative candidate left a sequence hole: %s", entry.Name())
		}
	}
	if !strings.Contains(res.Message, "过滤 1 项") {
		t.Fatalf("message=%q", res.Message)
	}
}

func TestTitleStripsAuthorTail(t *testing.T) {
	img := pngBytes(t, 400, 400)
	mux := http.NewServeMux()
	mux.HandleFunc("/post/44444", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><head><title>正文 来自 作者甲 - 示例站</title></head><body>
          <img src="/a.png"></body></html>`)
	})
	mux.HandleFunc("/a.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(img)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	pageURL := srv.URL + "/post/44444"
	dataDir := t.TempDir()
	d := &Downloader{HTTP: httpclient.New(httpclient.Options{}), DataDir: dataDir}
	res := d.Download(context.Background(), pageURL, false)
	if res.Status != result.Downloaded || len(res.Posts) != 1 {
		t.Fatalf("%+v", res)
	}
	post := res.Posts[0]
	if strings.Contains(post.Title, "正文 来自") {
		t.Fatalf("title still contains author tail: %q", post.Title)
	}
	rel, err := filepath.Rel(dataDir, post.Files[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rel, "正文 来自") {
		t.Fatalf("path still contains author tail: %q", rel)
	}
	wantRel := filepath.Join(
		"download", sites.Detect(pageURL), "作者甲", "untitled_44444", "001.png",
	)
	if rel != wantRel {
		t.Fatalf("relative path=%q want %q", rel, wantRel)
	}
}

// TestDownloadFiltersSmallImagesAndIsIdempotent covers the post-download
// dimension filter, the .NNN.skip marker, existing-file skipping and --force.
func TestDownloadFiltersSmallImagesAndIsIdempotent(t *testing.T) {
	big := pngBytes(t, 400, 400)
	small := pngBytes(t, 200, 200)

	var requests atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/post/88888", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><head><title>小图测试 - 站</title></head><body>
          <img src="/big.png"><img src="/small.png"></body></html>`)
	})
	mux.HandleFunc("/big.png", func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "image/png")
		w.Write(big)
	})
	mux.HandleFunc("/small.png", func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "image/png")
		w.Write(small)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	pageURL := srv.URL + "/post/88888"
	dataDir := t.TempDir()
	d := &Downloader{HTTP: httpclient.New(httpclient.Options{}), DataDir: dataDir}

	res := d.Download(context.Background(), pageURL, false)
	if res.Status != result.Downloaded || len(res.Posts[0].Files) != 1 {
		t.Fatalf("%+v", res)
	}
	dir := filepath.Dir(res.Posts[0].Files[0])
	if base := filepath.Base(res.Posts[0].Files[0]); base != "001.png" {
		t.Fatalf("file=%q", base)
	}
	marker := filepath.Join(dir, ".002.skip")
	content, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("skip marker: %v", err)
	}
	if string(content) != srv.URL+"/small.png" {
		t.Fatalf("marker=%q", content)
	}
	if _, err := os.Stat(filepath.Join(dir, "002.png")); err == nil {
		t.Fatal("small image should have been removed")
	}

	// 第二次运行：已有文件与 .skip 标记都计为 skipped，且不再发请求。
	before := requests.Load()
	res = d.Download(context.Background(), pageURL, false)
	if res.Status != result.Skipped || len(res.Posts[0].Files) != 0 {
		t.Fatalf("%+v", res)
	}
	if got := requests.Load(); got != before {
		t.Fatalf("second run made %d requests", got-before)
	}

	// --force 绕过已有文件与跳过标记，重新判定装饰图。
	stale := filepath.Join(dir, ".001.skip")
	if err := os.WriteFile(stale, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	res = d.Download(context.Background(), pageURL, true)
	if res.Status != result.Downloaded || len(res.Posts[0].Files) != 1 {
		t.Fatalf("%+v", res)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("successful image should clear the stale marker: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("marker should be rewritten after force: %v", err)
	}
}

func TestDownloadUsesAuthorFromRawTitle(t *testing.T) {
	img := pngBytes(t, 400, 400)
	mux := http.NewServeMux()
	mux.HandleFunc("/post/99999", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><head><title>山间清晨 正文 来自 摄影师甲 - 示例站</title></head>
          <body><img src="/a.png"></body></html>`)
	})
	mux.HandleFunc("/a.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(img)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	pageURL := srv.URL + "/post/99999"
	dataDir := t.TempDir()
	d := &Downloader{HTTP: httpclient.New(httpclient.Options{}), DataDir: dataDir}
	res := d.Download(context.Background(), pageURL, false)
	if res.Status != result.Downloaded || len(res.Posts) != 1 {
		t.Fatalf("%+v", res)
	}
	post := res.Posts[0]
	if post.Author != "摄影师甲" || post.Title != "山间清晨" {
		t.Fatalf("%+v", post)
	}
	rel, err := filepath.Rel(dataDir, post.Files[0])
	if err != nil {
		t.Fatal(err)
	}
	wantRel := filepath.Join(
		"download", sites.Detect(pageURL), "摄影师甲", "山间清晨_99999", "001.png",
	)
	if rel != wantRel {
		t.Fatalf("relative path=%q want %q", rel, wantRel)
	}
}

func TestDownloadRejectsHTMLMedia(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/post/1", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><head><title>拦截 - 站</title></head><body><img src="/blocked.jpg"></body></html>`)
	})
	mux.HandleFunc("/blocked.jpg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<html>denied</html>`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	d := &Downloader{HTTP: httpclient.New(httpclient.Options{}), DataDir: t.TempDir()}
	res := d.Download(context.Background(), srv.URL+"/post/1", false)
	if res.Status != result.Failed {
		t.Fatalf("%+v", res)
	}
}

func TestDownloadKeepsSmallVideo(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/post/12345", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><head><title>视频 - 站</title></head><body>
          <video src="/clip.mp4"></video></body></html>`)
	})
	mux.HandleFunc("/clip.mp4", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		w.Write([]byte("tiny video"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	d := &Downloader{HTTP: httpclient.New(httpclient.Options{}), DataDir: t.TempDir()}
	res := d.Download(context.Background(), srv.URL+"/post/12345", false)
	if res.Status != result.Downloaded || len(res.Posts) != 1 || len(res.Posts[0].Files) != 1 {
		t.Fatalf("%+v", res)
	}
	if got := filepath.Base(res.Posts[0].Files[0]); got != "001.mp4" {
		t.Fatalf("file=%q", got)
	}
}

func TestDownloadNoCandidates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><head><title>空页面 - 站</title></head><body><p>无图</p></body></html>`)
	}))
	defer srv.Close()

	d := &Downloader{HTTP: httpclient.New(httpclient.Options{}), DataDir: t.TempDir()}
	res := d.Download(context.Background(), srv.URL+"/post/1", false)
	if res.Status != result.NoMedia || len(res.Posts) != 0 {
		t.Fatalf("%+v", res)
	}
}

func TestDownloadPageError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	d := &Downloader{HTTP: httpclient.New(httpclient.Options{}), DataDir: t.TempDir()}
	res := d.Download(context.Background(), srv.URL+"/post/1", false)
	if res.Status != result.Failed || !strings.Contains(res.Message, "500") {
		t.Fatalf("%+v", res)
	}
}

func TestDownloadLimitsSameHostConcurrency(t *testing.T) {
	img := pngBytes(t, 400, 400)
	var current, max atomic.Int64
	var mu sync.Mutex

	mux := http.NewServeMux()
	mux.HandleFunc("/page", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		var b strings.Builder
		b.WriteString(`<html><head><title>并发 - 站</title></head><body>`)
		for i := 0; i < 6; i++ {
			fmt.Fprintf(&b, `<img src="/img%d.png">`, i)
		}
		b.WriteString(`</body></html>`)
		fmt.Fprint(w, b.String())
	})
	handler := func(w http.ResponseWriter, r *http.Request) {
		n := current.Add(1)
		mu.Lock()
		if n > max.Load() {
			max.Store(n)
		}
		mu.Unlock()
		time.Sleep(20 * time.Millisecond)
		w.Header().Set("Content-Type", "image/png")
		w.Write(img)
		current.Add(-1)
	}
	for i := 0; i < 6; i++ {
		mux.HandleFunc(fmt.Sprintf("/img%d.png", i), handler)
	}
	srv := httptest.NewServer(mux)
	defer srv.Close()

	d := &Downloader{HTTP: httpclient.New(httpclient.Options{}), DataDir: t.TempDir()}
	res := d.Download(context.Background(), srv.URL+"/page", false)
	if res.Status != result.Downloaded || len(res.Posts[0].Files) != 6 {
		t.Fatalf("%+v", res)
	}
	if got := max.Load(); got > 2 {
		t.Fatalf("same-host concurrency reached %d, want <= 2", got)
	}
}

func pngBytes(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
