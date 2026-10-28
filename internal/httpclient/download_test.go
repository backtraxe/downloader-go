package httpclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestDownloadSkipsExisting(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Write([]byte("DATA"))
	}))
	defer srv.Close()
	p := filepath.Join(t.TempDir(), "001.jpg")
	os.WriteFile(p, []byte("OLD"), 0o600)
	c := New(Options{Profile: Chrome120, Timeouts: DefaultTimeouts()})
	got, err := c.DownloadFile(context.Background(), srv.URL, p, DownloadOptions{})
	if err != nil || got != p {
		t.Fatalf("%s %v", got, err)
	}
	if atomic.LoadInt32(&hits) != 0 {
		t.Fatal("downloaded existing file")
	}
}

func TestDownloadRetriesTransient(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) == 1 {
			http.Error(w, "boom", http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte("DATA"))
	}))
	defer srv.Close()
	p := filepath.Join(t.TempDir(), "001.jpg")
	c := New(Options{Profile: Chrome120, Timeouts: DefaultTimeouts()})
	_, err := c.DownloadFile(context.Background(), srv.URL, p, DownloadOptions{MaxRetries: 3, Sleep: func(time.Duration) {}})
	if err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&hits) != 2 {
		t.Fatalf("hits=%d", hits)
	}
	data, _ := os.ReadFile(p)
	if string(data) != "DATA" {
		t.Fatalf("%q", data)
	}
}

func TestDownloadForceUsesPartThenRename(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("NEW")) }))
	defer srv.Close()
	p := filepath.Join(t.TempDir(), "001.jpg")
	os.WriteFile(p, []byte("OLD"), 0o600)
	c := New(Options{Profile: Chrome120, Timeouts: DefaultTimeouts()})
	if _, err := c.DownloadFile(context.Background(), srv.URL, p, DownloadOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(p)
	if string(data) != "NEW" {
		t.Fatalf("%q", data)
	}
	if _, err := os.Stat(p + ".part"); !os.IsNotExist(err) {
		t.Fatal("part left")
	}
}

func TestDownloadCorrectsExtension(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/webp")
		w.Write([]byte("WEBP"))
	}))
	defer srv.Close()
	p := filepath.Join(t.TempDir(), "001.jpg")
	c := New(Options{Profile: Chrome120, Timeouts: DefaultTimeouts()})
	got, err := c.DownloadFile(context.Background(), srv.URL+"/media", p, DownloadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(filepath.Dir(p), "001.webp")
	if got != want {
		t.Fatalf("got=%s want=%s", got, want)
	}
	data, _ := os.ReadFile(got)
	if string(data) != "WEBP" {
		t.Fatalf("%q", data)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("original extension file should not exist")
	}
}

func TestDownloadStatusErrorIsNotRetried(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		http.Error(w, "gone", http.StatusNotFound)
	}))
	defer srv.Close()
	p := filepath.Join(t.TempDir(), "001.jpg")
	c := New(Options{Profile: Chrome120, Timeouts: DefaultTimeouts()})
	_, err := c.DownloadFile(context.Background(), srv.URL, p, DownloadOptions{
		MaxRetries: 3,
		Sleep:      func(time.Duration) {},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("hits=%d", hits)
	}
	if !strings.Contains(err.Error(), "HTTP 404") || !strings.Contains(err.Error(), srv.URL) {
		t.Fatalf("err=%v", err)
	}
	if _, statErr := os.Stat(p); !os.IsNotExist(statErr) {
		t.Fatal("file should not be created")
	}
}

func TestDownloadStatusErrorDoesNotRetryOnURLKeywords(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		http.Error(w, "gone", http.StatusNotFound)
	}))
	defer srv.Close()
	p := filepath.Join(t.TempDir(), "001.jpg")
	c := New(Options{Profile: Chrome120, Timeouts: DefaultTimeouts()})
	// URL 含 503/eof/timeout，旧实现会因错误文本匹配 IsTransient 而误重试。
	rawURL := srv.URL + "/503/eof/timeout"
	_, err := c.DownloadFile(context.Background(), rawURL, p, DownloadOptions{
		MaxRetries: 3,
		Sleep:      func(time.Duration) {},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("hits=%d, status errors must not retry on URL keywords", hits)
	}
}

func TestDownloadFilterAbortsWithoutRetry(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte("<html>blocked</html>"))
	}))
	defer srv.Close()
	p := filepath.Join(t.TempDir(), "001.jpg")
	c := New(Options{Profile: Chrome120, Timeouts: DefaultTimeouts()})
	blocked := errors.New("blocked: html")
	_, err := c.DownloadFile(context.Background(), srv.URL, p, DownloadOptions{
		MaxRetries: 3,
		Sleep:      func(time.Duration) {},
		Filter: func(contentType string) error {
			if !strings.Contains(contentType, "text/html") {
				t.Errorf("contentType=%q", contentType)
			}
			return blocked
		},
	})
	if !errors.Is(err, blocked) {
		t.Fatalf("err=%v", err)
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("hits=%d", hits)
	}
	if _, statErr := os.Stat(p); !os.IsNotExist(statErr) {
		t.Fatal("file should not be created")
	}
}

func TestDownloadForceFailureKeepsTargetAndRemovesPart(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	p := filepath.Join(t.TempDir(), "001.jpg")
	os.WriteFile(p, []byte("OLD"), 0o600)
	c := New(Options{Profile: Chrome120, Timeouts: DefaultTimeouts()})
	_, err := c.DownloadFile(context.Background(), srv.URL, p, DownloadOptions{
		Force:      true,
		MaxRetries: 2,
		Sleep:      func(time.Duration) {},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	data, _ := os.ReadFile(p)
	if string(data) != "OLD" {
		t.Fatalf("target modified: %q", data)
	}
	if _, statErr := os.Stat(p + ".part"); !os.IsNotExist(statErr) {
		t.Fatal("part left")
	}
}

func TestDownloadOnRetryCalled(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) == 1 {
			http.Error(w, "boom", http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte("DATA"))
	}))
	defer srv.Close()
	p := filepath.Join(t.TempDir(), "001.jpg")
	c := New(Options{Profile: Chrome120, Timeouts: DefaultTimeouts()})
	var attempts []int
	_, err := c.DownloadFile(context.Background(), srv.URL, p, DownloadOptions{
		MaxRetries: 3,
		Sleep:      func(time.Duration) {},
		OnRetry:    func(attempt int, err error) { attempts = append(attempts, attempt) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 || attempts[0] != 1 {
		t.Fatalf("attempts=%v", attempts)
	}
}

func TestDownloadHonorsHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Referer") != "https://example.com/" {
			t.Errorf("referer=%q", r.Header.Get("Referer"))
		}
		w.Write([]byte("DATA"))
	}))
	defer srv.Close()
	p := filepath.Join(t.TempDir(), "001.jpg")
	h := http.Header{}
	h.Set("Referer", "https://example.com/")
	c := New(Options{Profile: Chrome120, Timeouts: DefaultTimeouts()})
	if _, err := c.DownloadFile(context.Background(), srv.URL, p, DownloadOptions{Header: h}); err != nil {
		t.Fatal(err)
	}
}

func TestDownloadCleansPlaceholderOnFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 声明了长度却提前断开，模拟流式写入中途失败。
		w.Header().Set("Content-Length", "100")
		w.Write([]byte("short"))
	}))
	defer srv.Close()
	p := filepath.Join(t.TempDir(), "001.jpg")
	c := New(Options{Profile: Chrome120, Timeouts: DefaultTimeouts()})
	_, err := c.DownloadFile(context.Background(), srv.URL, p, DownloadOptions{
		MaxRetries: 1,
		Sleep:      func(time.Duration) {},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	entries, _ := os.ReadDir(filepath.Dir(p))
	if len(entries) != 0 {
		t.Fatalf("leftover files: %v", entries)
	}
}

func TestIsTransient(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{errors.New("context deadline exceeded (Client.Timeout exceeded while awaiting headers)"), true},
		{errors.New("dial tcp: lookup example.com: no address associated with hostname"), true},
		{errors.New("read tcp: connection reset by peer"), true},
		{errors.New("HTTP 503 Service Unavailable"), true},
		{errors.New("HTTP 502 Bad Gateway"), true},
		{errors.New("unexpected EOF"), true},
		{errors.New("write: broken pipe"), true},
		{errors.New("HTTP 404 Not Found"), false},
		{errors.New("permission denied"), false},
		{io.EOF, true},
		// Go 原生解析器消息（CGO_ENABLED=0 下 DNS 失败的典型文本）
		{errors.New("dial tcp: lookup x on 10.0.0.2:53: no such host"), true},
		{errors.New("dial tcp: lookup x on 10.0.0.2:53: server misbehaving"), true},
		{errors.New("read tcp 10.0.0.1:1234: connection was reset by peer"), true},
		{errors.New("http2: server sent goaway"), true},
		{errors.New("http2: client connection lost"), true},
		// errors.As / errors.Is 路径
		{&net.DNSError{Err: "server misbehaving", Name: "x", IsTemporary: true}, true},
		{fmt.Errorf("request failed: %w", syscall.ECONNRESET), true},
		{fmt.Errorf("dial failed: %w", syscall.ECONNREFUSED), true},
		{io.ErrUnexpectedEOF, true},
	}
	for _, tc := range cases {
		if got := IsTransient(tc.err); got != tc.want {
			t.Errorf("IsTransient(%q)=%v want %v", tc.err, got, tc.want)
		}
	}
	if IsTransient(nil) {
		t.Error("IsTransient(nil) should be false")
	}
}

func TestIsTransientStatus(t *testing.T) {
	cases := map[int]bool{200: false, 301: false, 400: false, 404: false, 429: true, 500: true, 502: true, 503: true, 599: true, 600: false}
	for code, want := range cases {
		if got := IsTransientStatus(code); got != want {
			t.Errorf("IsTransientStatus(%d)=%v want %v", code, got, want)
		}
	}
}
