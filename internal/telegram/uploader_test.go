package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/backtraxe/downloader-go/internal/result"
	"github.com/backtraxe/downloader-go/internal/tgapi"
)

type albumItem struct {
	Type    string `json:"type"`
	Caption string `json:"caption"`
}

type uploadRecorder struct {
	mu        sync.Mutex
	albums    [][]albumItem
	documents []string // captions, in order
	singles   []string // sendPhoto/sendVideo captions, in order
	paths     []string // request paths, in order
}

func (r *uploadRecorder) handler(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch {
	case strings.HasSuffix(req.URL.Path, "/sendMediaGroup"):
		if err := req.ParseMultipartForm(32 << 20); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var items []albumItem
		if err := json.Unmarshal([]byte(req.FormValue("media")), &items); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		r.mu.Lock()
		r.albums = append(r.albums, items)
		r.paths = append(r.paths, req.URL.Path)
		r.mu.Unlock()
		result := make([]map[string]any, len(items))
		for i := range result {
			result[i] = map[string]any{"message_id": i + 1}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
	case strings.HasSuffix(req.URL.Path, "/sendDocument"):
		if err := req.ParseMultipartForm(32 << 20); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		r.mu.Lock()
		r.documents = append(r.documents, req.FormValue("caption"))
		r.paths = append(r.paths, req.URL.Path)
		r.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 99}})
	case strings.HasSuffix(req.URL.Path, "/sendPhoto"), strings.HasSuffix(req.URL.Path, "/sendVideo"):
		if err := req.ParseMultipartForm(32 << 20); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		r.mu.Lock()
		r.singles = append(r.singles, req.FormValue("caption"))
		r.paths = append(r.paths, req.URL.Path)
		r.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 99}})
	default:
		http.Error(w, "unexpected method "+req.URL.Path, http.StatusNotFound)
	}
}

func TestSendGroupsAlbumsAndSkipsOversizedVideo(t *testing.T) {
	dir := t.TempDir()
	var files []string
	for i := 0; i < 12; i++ {
		p := filepath.Join(dir, fmt.Sprintf("p%02d.jpg", i))
		writeTestJPEG(t, p, 10, 10)
		files = append(files, p)
	}
	big := filepath.Join(dir, "big.mp4")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxUploadBytes + 1); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	files = append(files, big)
	for _, name := range []string{"a.pdf", "b.pdf"} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("pdf"), 0o600); err != nil {
			t.Fatal(err)
		}
		files = append(files, p)
	}

	rec := &uploadRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(rec.handler))
	defer srv.Close()

	u := &Uploader{
		API:     &tgapi.Client{Token: "test", BaseURL: srv.URL, HTTP: srv.Client()},
		WorkDir: t.TempDir(),
		Run: func(context.Context, string, ...string) ([]byte, []byte, error) {
			return nil, nil, errors.New("no ffmpeg in test")
		},
	}
	post := result.Post{Site: "douyin", Author: "甲", Title: "标题", Files: files}
	sent, skipped, err := u.Send(context.Background(), "123", post)
	if err != nil {
		t.Fatal(err)
	}
	if sent != 14 {
		t.Fatalf("sent=%d want 14", sent)
	}
	if len(skipped) != 1 || skipped[0] != big {
		t.Fatalf("skipped=%v want [%s]", skipped, big)
	}

	if len(rec.albums) != 2 {
		t.Fatalf("albums=%d want 2 (%v)", len(rec.albums), rec.albums)
	}
	if len(rec.albums[0]) != 10 || len(rec.albums[1]) != 2 {
		t.Fatalf("sizes=%d,%d", len(rec.albums[0]), len(rec.albums[1]))
	}
	wantCaption := "#甲 #douyin\n标题"
	for gi, album := range rec.albums {
		if album[0].Caption != wantCaption {
			t.Fatalf("album %d first caption=%q", gi, album[0].Caption)
		}
		if album[0].Type != "photo" {
			t.Fatalf("album %d type=%q", gi, album[0].Type)
		}
		for i := 1; i < len(album); i++ {
			if album[i].Caption != "" {
				t.Fatalf("album %d item %d caption=%q", gi, i, album[i].Caption)
			}
		}
	}
	if len(rec.documents) != 2 {
		t.Fatalf("documents=%v", rec.documents)
	}
	for _, caption := range rec.documents {
		if caption != wantCaption {
			t.Fatalf("document caption=%q", caption)
		}
	}
}

func TestSendSinglePhotoUsesSendPhoto(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "one.jpg")
	writeTestJPEG(t, src, 10, 10)

	rec := &uploadRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(rec.handler))
	defer srv.Close()

	u := &Uploader{
		API:     &tgapi.Client{Token: "test", BaseURL: srv.URL, HTTP: srv.Client()},
		WorkDir: t.TempDir(),
	}
	sent, skipped, err := u.Send(context.Background(), "123", result.Post{
		Site: "douyin", Author: "甲", Title: "标题", Files: []string{src},
	})
	if err != nil || sent != 1 || len(skipped) != 0 {
		t.Fatalf("sent=%d skipped=%v err=%v", sent, skipped, err)
	}
	if len(rec.paths) != 1 || rec.paths[0] != "/bottest/sendPhoto" {
		t.Fatalf("paths=%v want [/bottest/sendPhoto]", rec.paths)
	}
	if len(rec.singles) != 1 || rec.singles[0] != "#甲 #douyin\n标题" {
		t.Fatalf("singles=%v", rec.singles)
	}
}

// TestSendAlbumFallsBackToSinglesAndDocument 验证相册因 IMAGE_PROCESS_FAILED
// 整组失败时逐项重发，并把仍然失败的照片降级为 document。
func TestSendAlbumFallsBackToSinglesAndDocument(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.jpg")
	b := filepath.Join(dir, "b.jpg")
	writeTestJPEG(t, a, 10, 10)
	writeTestJPEG(t, b, 10, 10)

	var mu sync.Mutex
	var paths []string
	photoCalls := 0
	fail := func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":          false,
			"error_code":  400,
			"description": `Bad Request: failed to send message #2 with the error message "IMAGE_PROCESS_FAILED"`,
		})
	}
	ok := func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 7}})
	}
	handler := func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		paths = append(paths, req.URL.Path)
		mu.Unlock()
		switch {
		case strings.HasSuffix(req.URL.Path, "/sendMediaGroup"):
			fail(w)
		case strings.HasSuffix(req.URL.Path, "/sendPhoto"):
			mu.Lock()
			photoCalls++
			n := photoCalls
			mu.Unlock()
			if n == 2 {
				fail(w)
				return
			}
			ok(w)
		case strings.HasSuffix(req.URL.Path, "/sendDocument"):
			ok(w)
		default:
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(handler))
	defer srv.Close()

	u := &Uploader{API: &tgapi.Client{Token: "test", BaseURL: srv.URL, HTTP: srv.Client()}}
	sent, skipped, err := u.Send(context.Background(), "123", result.Post{
		Site: "xiaohongshu", Author: "甲", Title: "标题", Files: []string{a, b},
	})
	if err != nil || sent != 2 || len(skipped) != 0 {
		t.Fatalf("sent=%d skipped=%v err=%v", sent, skipped, err)
	}
	want := []string{
		"/bottest/sendMediaGroup",
		"/bottest/sendPhoto",
		"/bottest/sendPhoto",
		"/bottest/sendDocument",
	}
	if strings.Join(paths, ",") != strings.Join(want, ",") {
		t.Fatalf("paths=%v want %v", paths, want)
	}
}

func TestSendSkipsOversizedDocument(t *testing.T) {
	dir := t.TempDir()
	big := filepath.Join(dir, "big.bin")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxUploadBytes + 1); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	rec := &uploadRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(rec.handler))
	defer srv.Close()

	u := &Uploader{API: &tgapi.Client{Token: "test", BaseURL: srv.URL, HTTP: srv.Client()}}
	sent, skipped, err := u.Send(context.Background(), "123", result.Post{Files: []string{big}})
	if err != nil {
		t.Fatal(err)
	}
	if sent != 0 {
		t.Fatalf("sent=%d", sent)
	}
	if len(skipped) != 1 || skipped[0] != big {
		t.Fatalf("skipped=%v want [%s]", skipped, big)
	}
	if len(rec.paths) != 0 {
		t.Fatalf("unexpected API requests: %v", rec.paths)
	}
}
