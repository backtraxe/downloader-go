package httpclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestGetOverHTTP2WithUTLS(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 2 {
			t.Errorf("proto=%s", r.Proto)
		}
		if r.Header.Get("X-Test") != "1" {
			t.Errorf("header missing")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()

	c := New(Options{Profile: Chrome155, Timeouts: DefaultTimeouts(), InsecureSkipVerify: true})
	h := http.Header{}
	h.Set("X-Test", "1")
	resp, err := c.Get(context.Background(), Request{URL: srv.URL, Header: h})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var out map[string]bool
	if err := resp.JSON(&out); err != nil || !out["ok"] {
		t.Fatalf("%v %v", out, err)
	}
}

func TestGetSendsCookieAndProfileUserAgent(t *testing.T) {
	var gotUA, gotCookie string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		gotCookie = r.Header.Get("Cookie")
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	c := New(Options{Profile: Chrome120, Timeouts: DefaultTimeouts()})
	h := http.Header{}
	h.Set("Cookie", "c=3")
	resp, err := c.Get(context.Background(), Request{URL: srv.URL, Header: h, Cookie: "a=1; b=2"})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Close()

	if !strings.Contains(gotUA, "Chrome/120.0.0.0") {
		t.Fatalf("ua=%q", gotUA)
	}
	for _, want := range []string{"c=3", "a=1", "b=2"} {
		if !strings.Contains(gotCookie, want) {
			t.Fatalf("cookie=%q missing %q", gotCookie, want)
		}
	}
}

func TestReadAllLimitAndRaiseForStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("not found"))
	}))
	defer srv.Close()

	c := New(Options{Profile: Chrome120, Timeouts: DefaultTimeouts()})
	resp, err := c.Get(context.Background(), Request{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Close()

	body, err := resp.ReadAll(3)
	if err != nil || string(body) != "not" {
		t.Fatalf("body=%q err=%v", body, err)
	}
	err = resp.RaiseForStatus()
	if err == nil {
		t.Fatal("expected error for 404")
	}
	if !strings.Contains(err.Error(), "404") || !strings.Contains(err.Error(), srv.URL) {
		t.Fatalf("error=%v", err)
	}
}

func TestGetForceHTTP1(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 1 {
			t.Errorf("proto=%s", r.Proto)
		}
		w.Write([]byte("ok"))
	}))
	// EnableHTTP2 的 httptest 服务端会无视 ALPN 强制说 h2，无法验证
	// HTTP/1.1 回退；这里用普通 TLS 服务端。
	srv.StartTLS()
	defer srv.Close()

	c := New(Options{
		Profile:            Chrome155,
		Timeouts:           DefaultTimeouts(),
		InsecureSkipVerify: true,
		ForceHTTP1:         true,
	})
	resp, err := c.Get(context.Background(), Request{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestGetFollowsRedirectAndKeepsCookies(t *testing.T) {
	var gotCookie string
	mux := http.NewServeMux()
	mux.HandleFunc("/a", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "sid", Value: "v"})
		http.Redirect(w, r, "/b", http.StatusFound)
	})
	mux.HandleFunc("/b", func(w http.ResponseWriter, r *http.Request) {
		gotCookie = r.Header.Get("Cookie")
		w.Write([]byte("ok"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := New(Options{Profile: Chrome120, Timeouts: DefaultTimeouts()})
	resp, err := c.Get(context.Background(), Request{URL: srv.URL + "/a"})
	if err != nil {
		t.Fatal(err)
	}
	resp.Close()
	if !strings.Contains(gotCookie, "sid=v") {
		t.Fatalf("cookie=%q", gotCookie)
	}
	u, _ := url.Parse(srv.URL + "/b")
	if resp.URL.Path != u.Path {
		t.Fatalf("final=%s", resp.URL)
	}
}
