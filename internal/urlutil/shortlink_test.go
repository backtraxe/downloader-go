package urlutil

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/backtraxe/downloader-go/internal/httpclient"
)

// proxyClient 返回一个经 httptest 代理发送明文请求的客户端，测试无需真实网络。
func proxyClient(proxyURL string) *httpclient.Client {
	return httpclient.New(httpclient.Options{
		Profile:  httpclient.Chrome120,
		Timeouts: httpclient.DefaultTimeouts(),
		Proxy:    proxyURL,
	})
}

func TestResolveShortLinkPassesThroughNonShortLink(t *testing.T) {
	const raw = "https://example.com/a"
	if got := ResolveShortLink(context.Background(), nil, raw); got != raw {
		t.Fatalf("got %q want %q", got, raw)
	}
}

func TestResolveShortLinkExpandsRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/abc" {
			http.Redirect(w, r, "http://b23.tv/video/BV1", http.StatusFound)
			return
		}
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	got := ResolveShortLink(context.Background(), proxyClient(srv.URL), "http://b23.tv/abc")
	if want := "http://b23.tv/video/BV1"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestResolveShortLinkReturnsOriginalOnError(t *testing.T) {
	// 非法代理让首次 Get 立即失败，避免真实网络请求。
	c := httpclient.New(httpclient.Options{Proxy: "://bad"})
	const raw = "https://b23.tv/x"
	if got := ResolveShortLink(context.Background(), c, raw); got != raw {
		t.Fatalf("got %q want %q", got, raw)
	}
}

func TestResolveShortLinkWithRetryWaitsBetweenAttempts(t *testing.T) {
	var attempts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/abc" {
			w.Write([]byte("final"))
			return
		}
		attempts++
		if attempts < 3 {
			// 模拟短链服务端偶发返回 200 页面而不重定向。
			w.Write([]byte("no redirect"))
			return
		}
		http.Redirect(w, r, "http://b23.tv/video/BV1", http.StatusFound)
	}))
	defer srv.Close()

	var sleeps []time.Duration
	got := ResolveShortLinkWithRetry(context.Background(), proxyClient(srv.URL), "http://b23.tv/abc", 3,
		func(d time.Duration) { sleeps = append(sleeps, d) })

	if want := "http://b23.tv/video/BV1"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if attempts != 3 {
		t.Fatalf("attempts=%d want 3", attempts)
	}
	if len(sleeps) != 2 {
		t.Fatalf("sleeps=%v want 2 entries", sleeps)
	}
	if sleeps[0] != 1500*time.Millisecond || sleeps[1] != 3*time.Second {
		t.Fatalf("unexpected backoff: %v", sleeps)
	}
}

func TestResolveShortLinkWithRetryNonShortDoesNotSleep(t *testing.T) {
	const raw = "https://example.com/a"
	slept := false
	got := ResolveShortLinkWithRetry(context.Background(), nil, raw, 3,
		func(time.Duration) { slept = true })
	if got != raw {
		t.Fatalf("got %q want %q", got, raw)
	}
	if slept {
		t.Fatal("non-short link should not sleep or request")
	}
}

func TestResolveShortLinkWithRetryExhaustsAttempts(t *testing.T) {
	// 所有尝试都失败（非法代理），最终原样返回。
	c := httpclient.New(httpclient.Options{Proxy: "://bad"})
	var sleeps []time.Duration
	const raw = "https://b23.tv/x"
	got := ResolveShortLinkWithRetry(context.Background(), c, raw, 3,
		func(d time.Duration) { sleeps = append(sleeps, d) })
	if got != raw {
		t.Fatalf("got %q want %q", got, raw)
	}
	if len(sleeps) != 2 {
		t.Fatalf("sleeps=%v want 2 entries", sleeps)
	}
}
