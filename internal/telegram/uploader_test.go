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
		if err := os.WriteFile(p, []byte("jpeg"), 0o600); err != nil {
			t.Fatal(err)
		}
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
