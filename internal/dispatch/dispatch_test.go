package dispatch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/backtraxe/downloader-go/internal/httpclient"
	"github.com/backtraxe/downloader-go/internal/logging"
	"github.com/backtraxe/downloader-go/internal/result"
)

func writeCookie(t *testing.T, dataDir, site, content string) {
	t.Helper()
	dir := filepath.Join(dataDir, "cookies")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, site+".txt"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func okHandler(name string, calls *[]string) func(context.Context, string, bool) result.Result {
	return func(context.Context, string, bool) result.Result {
		*calls = append(*calls, name)
		return result.Result{Status: result.Downloaded}
	}
}

func TestRoutesAndCookieGate(t *testing.T) {
	calls := []string{}
	h := Handlers{
		XHS: func(context.Context, string, string, bool) result.Result {
			calls = append(calls, "xhs")
			return result.Result{Status: result.Downloaded}
		},
		YTDLP:   okHandler("ytdlp", &calls),
		Generic: okHandler("generic", &calls),
	}
	d := &Dispatcher{DataDir: t.TempDir(), Handlers: h}
	if got := d.Dispatch(context.Background(), "https://www.xiaohongshu.com/explore/1?xsec_token=x", false); got.Status != result.Failed {
		t.Fatalf("missing cookie should fail: %+v", got)
	}
	if got := d.Dispatch(context.Background(), "https://youtu.be/x", false); got.Status != result.Downloaded {
		t.Fatalf("%+v", got)
	}
	if len(calls) != 1 || calls[0] != "ytdlp" {
		t.Fatalf("%v", calls)
	}
}

func TestDispatchXHSLoadsCookieConvertsHeaderAndPropagatesForce(t *testing.T) {
	dir := t.TempDir()
	writeCookie(t, dir, "xiaohongshu", "a1=abc; webId=web; web_session=sess")

	var gotURL, gotCookie string
	var gotForce, hasRequestID bool
	d := &Dispatcher{DataDir: dir, Handlers: Handlers{
		XHS: func(ctx context.Context, url, cookie string, force bool) result.Result {
			gotURL, gotCookie, gotForce = url, cookie, force
			hasRequestID = logging.RequestID(ctx) != ""
			return result.Result{Status: result.Downloaded}
		},
	}}

	const raw = "https://www.xiaohongshu.com/explore/1?xsec_token=x"
	if got := d.Dispatch(context.Background(), raw, true); got.Status != result.Downloaded {
		t.Fatalf("%+v", got)
	}
	if gotURL != raw {
		t.Fatalf("url=%q", gotURL)
	}
	if gotCookie != "a1=abc; webId=web; web_session=sess" {
		t.Fatalf("cookie=%q", gotCookie)
	}
	if !gotForce {
		t.Fatal("force not propagated")
	}
	if !hasRequestID {
		t.Fatal("dispatch must open a logging request scope")
	}
}

func TestDispatchDouyinNoteAndVideo(t *testing.T) {
	dir := t.TempDir()
	writeCookie(t, dir, "douyin", "UIFID=u; ttwid=t")

	var noteURL, noteCookie string
	var noteForce bool
	var videoCalled bool
	d := &Dispatcher{DataDir: dir, Handlers: Handlers{
		DouyinNote: func(_ context.Context, url, cookie string, force bool) result.Result {
			noteURL, noteCookie, noteForce = url, cookie, force
			return result.Result{Status: result.Downloaded}
		},
		DouyinVideo: func(context.Context, string, string, bool) result.Result {
			videoCalled = true
			return result.Result{Status: result.Downloaded}
		},
	}}

	const note = "https://www.douyin.com/note/123"
	if got := d.Dispatch(context.Background(), note, true); got.Status != result.Downloaded {
		t.Fatalf("%+v", got)
	}
	if noteURL != note || noteCookie != "UIFID=u; ttwid=t" || !noteForce {
		t.Fatalf("note url=%q cookie=%q force=%v", noteURL, noteCookie, noteForce)
	}

	if got := d.Dispatch(context.Background(), "https://www.douyin.com/video/456", false); got.Status != result.Downloaded {
		t.Fatalf("%+v", got)
	}
	if !videoCalled {
		t.Fatal("video handler not called")
	}
}

func TestDispatchDouyinMissingCookieFails(t *testing.T) {
	d := &Dispatcher{DataDir: t.TempDir(), Handlers: Handlers{
		DouyinNote: func(context.Context, string, string, bool) result.Result {
			t.Fatal("handler should not run without cookie")
			return result.Result{}
		},
	}}
	got := d.Dispatch(context.Background(), "https://www.douyin.com/note/123", false)
	if got.Status != result.Failed {
		t.Fatalf("%+v", got)
	}
	if !strings.Contains(got.Message, filepath.Join("cookies", "douyin.txt")) {
		t.Fatalf("message should point at cookie path: %q", got.Message)
	}
}

func TestDispatchDouyinShortLinkResolvesBeforeRouting(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Host == "v.douyin.com" {
			http.Redirect(w, r, "http://www.douyin.com/note/123", http.StatusFound)
			return
		}
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	writeCookie(t, dir, "douyin", "UIFID=u; ttwid=t")

	var gotURL string
	d := &Dispatcher{
		HTTP:    httpclient.New(httpclient.Options{Profile: httpclient.Chrome120, Timeouts: httpclient.DefaultTimeouts(), Proxy: srv.URL}),
		DataDir: dir,
		Handlers: Handlers{
			DouyinNote: func(_ context.Context, url, _ string, _ bool) result.Result {
				gotURL = url
				return result.Result{Status: result.Downloaded}
			},
		},
	}

	if got := d.Dispatch(context.Background(), "http://v.douyin.com/abc", false); got.Status != result.Downloaded {
		t.Fatalf("%+v", got)
	}
	if want := "http://www.douyin.com/note/123"; gotURL != want {
		t.Fatalf("resolved url=%q want %q", gotURL, want)
	}
}

func TestDispatchDouyinNonNoteURLFallsThroughToYTDLP(t *testing.T) {
	calls := []string{}
	d := &Dispatcher{DataDir: t.TempDir(), Handlers: Handlers{
		YTDLP:   okHandler("ytdlp", &calls),
		Generic: okHandler("generic", &calls),
	}}
	if got := d.Dispatch(context.Background(), "https://www.douyin.com/user/abc", false); got.Status != result.Downloaded {
		t.Fatalf("%+v", got)
	}
	if len(calls) != 1 || calls[0] != "ytdlp" {
		t.Fatalf("%v", calls)
	}
}

func TestDispatchKnownAndUnknownSites(t *testing.T) {
	for _, raw := range []string{
		"https://www.bilibili.com/video/BV1",
		"https://www.youtube.com/watch?v=1",
		"https://www.instagram.com/p/1",
		"https://x.com/user/status/1",
	} {
		calls := []string{}
		d := &Dispatcher{DataDir: t.TempDir(), Handlers: Handlers{
			YTDLP:   okHandler("ytdlp", &calls),
			Generic: okHandler("generic", &calls),
		}}
		if got := d.Dispatch(context.Background(), raw, false); got.Status != result.Downloaded {
			t.Fatalf("%s: %+v", raw, got)
		}
		if len(calls) != 1 || calls[0] != "ytdlp" {
			t.Fatalf("%s: %v", raw, calls)
		}
	}

	calls := []string{}
	d := &Dispatcher{DataDir: t.TempDir(), Handlers: Handlers{
		YTDLP:   okHandler("ytdlp", &calls),
		Generic: okHandler("generic", &calls),
	}}
	if got := d.Dispatch(context.Background(), "https://example.com/page", false); got.Status != result.Downloaded {
		t.Fatalf("%+v", got)
	}
	if len(calls) != 1 || calls[0] != "generic" {
		t.Fatalf("%v", calls)
	}
}

func TestDispatchNilHandlersFailClearly(t *testing.T) {
	dir := t.TempDir()
	writeCookie(t, dir, "xiaohongshu", "a1=abc")
	writeCookie(t, dir, "douyin", "UIFID=u")

	cases := []struct {
		name string
		url  string
		want string
	}{
		{"xhs", "https://www.xiaohongshu.com/explore/1", "小红书后端尚未接入"},
		{"douyin-note", "https://www.douyin.com/note/123", "抖音图文后端尚未接入"},
		{"douyin-video", "https://www.douyin.com/video/456", "抖音视频后端尚未接入"},
		{"ytdlp", "https://youtu.be/x", "yt-dlp后端尚未接入"},
		{"generic", "https://example.com/page", "通用网页后端尚未接入"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &Dispatcher{DataDir: dir}
			got := d.Dispatch(context.Background(), tc.url, false)
			if got.Status != result.Failed || !strings.Contains(got.Message, tc.want) {
				t.Fatalf("%+v, want message containing %q", got, tc.want)
			}
		})
	}
}
