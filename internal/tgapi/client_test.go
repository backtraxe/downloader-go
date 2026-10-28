package tgapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCallUnwrapsResultAndErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/botTOKEN/sendMessage" {
			t.Errorf("path=%s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("method=%s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
			t.Errorf("content-type=%s", ct)
		}
		if r.FormValue("chat_id") != "42" {
			t.Errorf("chat_id=%s", r.FormValue("chat_id"))
		}
		if r.FormValue("text") != "hi" {
			t.Errorf("text=%s", r.FormValue("text"))
		}
		w.Write([]byte(`{"ok":true,"result":{"message_id":9,"chat":{"id":42,"type":"private"}}}`))
	}))
	defer srv.Close()
	c := New("TOKEN")
	c.BaseURL = srv.URL
	msg, err := c.SendMessage(context.Background(), "42", "hi")
	if err != nil || msg.MessageID != 9 {
		t.Fatalf("%+v %v", msg, err)
	}
	if msg.Chat.ID.String() != "42" || msg.Chat.Type != "private" {
		t.Fatalf("chat=%+v", msg.Chat)
	}

	errSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":false,"description":"bad token"}`))
	}))
	defer errSrv.Close()
	c.BaseURL = errSrv.URL
	if _, err := c.SendMessage(context.Background(), "42", "hi"); err == nil || !strings.Contains(err.Error(), "bad token") {
		t.Fatalf("err=%v", err)
	}
}

func TestCallNon2xxIncludesStatusAndBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		io.WriteString(w, `upstream down`)
	}))
	defer srv.Close()
	c := New("T")
	c.BaseURL = srv.URL
	_, err := c.Call(context.Background(), "getMe", nil)
	if err == nil || !strings.Contains(err.Error(), "502") || !strings.Contains(err.Error(), "upstream down") {
		t.Fatalf("err=%v", err)
	}
}

func TestCallMultipartUploadsFile(t *testing.T) {
	dir := t.TempDir()
	tmpDir := t.TempDir()
	t.Setenv("TMPDIR", tmpDir)
	p := filepath.Join(dir, "a.jpg")
	if err := os.WriteFile(p, []byte("JPEG"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/botT/sendPhoto" {
			t.Errorf("path=%s", r.URL.Path)
		}
		if r.ContentLength <= 0 {
			t.Errorf("content-length=%d", r.ContentLength)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("parse multipart: %v", err)
			return
		}
		if got := r.FormValue("chat_id"); got != "1" {
			t.Errorf("chat_id=%s", got)
		}
		f, hdr, err := r.FormFile("photo")
		if err != nil {
			t.Errorf("form file: %v", err)
			return
		}
		defer f.Close()
		if hdr.Filename != "a.jpg" {
			t.Errorf("filename=%s", hdr.Filename)
		}
		got, _ := io.ReadAll(f)
		if string(got) != "JPEG" {
			t.Errorf("content=%q", got)
		}
		w.Write([]byte(`{"ok":true,"result":{"message_id":1,"chat":{"id":1,"type":"private"}}}`))
	}))
	defer srv.Close()
	c := New("T")
	c.BaseURL = srv.URL
	_, err := c.CallMultipart(context.Background(), "sendPhoto", map[string]string{"chat_id": "1"}, map[string]string{"photo": p})
	if err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(tmpDir); err != nil {
		t.Fatal(err)
	} else if len(entries) != 0 {
		t.Fatalf("temp files left behind: %v", entries)
	}
}

func TestTransportErrorRedactsToken(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	c := New("SECRET_TOKEN")
	c.BaseURL = "http://" + addr
	if _, err := c.SendMessage(context.Background(), "1", "x"); err == nil {
		t.Fatal("expected transport error")
	} else if strings.Contains(err.Error(), "SECRET_TOKEN") {
		t.Fatalf("token leaked: %v", err)
	}
}

func TestCanceledContextRedactsTokenAndPreservesCause(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := New("SECRET_TOKEN")
	c.BaseURL = srv.URL
	_, err := c.SendMessage(ctx, "1", "x")
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if strings.Contains(err.Error(), "SECRET_TOKEN") {
		t.Fatalf("token leaked: %v", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cause lost: %v", err)
	}
}

func TestErrorBodyRedactsToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		io.WriteString(w, "boom SECRET_TOKEN")
	}))
	defer srv.Close()
	c := New("SECRET_TOKEN")
	c.BaseURL = srv.URL
	_, err := c.Call(context.Background(), "getMe", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "SECRET_TOKEN") {
		t.Fatalf("token leaked: %v", err)
	}
	if !strings.Contains(err.Error(), "<redacted>") {
		t.Fatalf("token not marked redacted: %v", err)
	}
}

func TestErrorBodyIsTruncated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		io.WriteString(w, strings.Repeat("x", maxResponseBody+100))
	}))
	defer srv.Close()
	c := New("T")
	c.BaseURL = srv.URL
	_, err := c.Call(context.Background(), "getMe", nil)
	if err == nil || !strings.Contains(err.Error(), "response truncated") {
		t.Fatalf("err=%v", err)
	}
}

func TestErrorBodyTailKeepsValidUTF8(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		io.WriteString(w, strings.Repeat("界", maxErrorBodyTail))
	}))
	defer srv.Close()
	c := New("T")
	c.BaseURL = srv.URL
	_, err := c.Call(context.Background(), "getMe", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !utf8.ValidString(err.Error()) {
		t.Fatalf("invalid UTF-8: %q", err.Error())
	}
}

func TestGetUpdatesParsesMessages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/botT/getUpdates" {
			t.Errorf("path=%s", r.URL.Path)
		}
		if got := r.FormValue("offset"); got != "7" {
			t.Errorf("offset=%s", got)
		}
		if got := r.FormValue("timeout"); got != "25" {
			t.Errorf("timeout=%s", got)
		}
		w.Write([]byte(`{"ok":true,"result":[
			{"update_id":7,"message":{"message_id":3,"chat":{"id":42,"type":"private"},"from":{"id":100},"text":"/start"}},
			{"update_id":8,"channel_post":{"message_id":4,"chat":{"id":-100123,"type":"channel"},"caption":"hi"}}
		]}`))
	}))
	defer srv.Close()
	c := New("T")
	c.BaseURL = srv.URL
	updates, err := c.GetUpdates(context.Background(), 7, 25)
	if err != nil {
		t.Fatal(err)
	}
	if len(updates) != 2 {
		t.Fatalf("len=%d", len(updates))
	}
	if updates[0].UpdateID != 7 || updates[0].Message == nil || updates[0].Message.Text != "/start" {
		t.Fatalf("update0=%+v", updates[0])
	}
	if updates[0].Message.From.ID != 100 || updates[0].Message.Chat.ID.String() != "42" {
		t.Fatalf("message0=%+v", updates[0].Message)
	}
	if updates[1].ChannelPost == nil || updates[1].ChannelPost.Chat.ID.String() != "-100123" ||
		updates[1].ChannelPost.Caption != "hi" {
		t.Fatalf("update1=%+v", updates[1])
	}
}

func TestGetUpdatesEmptyResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true,"result":[]}`))
	}))
	defer srv.Close()
	c := New("T")
	c.BaseURL = srv.URL
	updates, err := c.GetUpdates(context.Background(), 0, 0)
	if err != nil || len(updates) != 0 {
		t.Fatalf("updates=%v err=%v", updates, err)
	}
}

func TestDeleteMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/botT/deleteMessage" {
			t.Errorf("path=%s", r.URL.Path)
		}
		if got := r.FormValue("chat_id"); got != "@chan" {
			t.Errorf("chat_id=%s", got)
		}
		if got := r.FormValue("message_id"); got != "55" {
			t.Errorf("message_id=%s", got)
		}
		w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer srv.Close()
	c := New("T")
	c.BaseURL = srv.URL
	if err := c.DeleteMessage(context.Background(), "@chan", 55); err != nil {
		t.Fatal(err)
	}
}

func TestSendMediaGroupShapeAndFiles(t *testing.T) {
	dir := t.TempDir()
	photo := filepath.Join(dir, "a.jpg")
	video := filepath.Join(dir, "b.mp4")
	if err := os.WriteFile(photo, []byte("PIC"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(video, []byte("VID"), 0o600); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/botT/sendMediaGroup" {
			t.Errorf("path=%s", r.URL.Path)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("parse multipart: %v", err)
			return
		}
		if got := r.FormValue("chat_id"); got != "-100" {
			t.Errorf("chat_id=%s", got)
		}
		var items []struct {
			Type    string `json:"type"`
			Media   string `json:"media"`
			Caption string `json:"caption"`
		}
		if err := json.Unmarshal([]byte(r.FormValue("media")), &items); err != nil {
			t.Errorf("media json: %v", err)
			return
		}
		if len(items) != 2 {
			t.Errorf("items=%d", len(items))
			return
		}
		if items[0].Type != "photo" || items[0].Media != "attach://file0" || items[0].Caption != "#a\ncap" {
			t.Errorf("item0=%+v", items[0])
		}
		if items[1].Type != "video" || items[1].Media != "attach://file1" || items[1].Caption != "" {
			t.Errorf("item1=%+v", items[1])
		}
		for field, want := range map[string]struct{ name, body string }{
			"file0": {"a.jpg", "PIC"},
			"file1": {"b.mp4", "VID"},
		} {
			f, hdr, err := r.FormFile(field)
			if err != nil {
				t.Errorf("form file %s: %v", field, err)
				continue
			}
			got, _ := io.ReadAll(f)
			f.Close()
			if hdr.Filename != want.name || string(got) != want.body {
				t.Errorf("%s: filename=%s body=%q", field, hdr.Filename, got)
			}
		}
		w.Write([]byte(`{"ok":true,"result":[
			{"message_id":10,"chat":{"id":-100,"type":"channel"}},
			{"message_id":11,"chat":{"id":-100,"type":"channel"}}
		]}`))
	}))
	defer srv.Close()

	c := New("T")
	c.BaseURL = srv.URL
	msgs, err := c.SendMediaGroup(context.Background(), "-100", []Media{
		{Kind: "photo", Path: photo, Caption: "#a\ncap"},
		{Kind: "video", Path: video},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || msgs[0].MessageID != 10 || msgs[1].MessageID != 11 {
		t.Fatalf("msgs=%+v", msgs)
	}
}
