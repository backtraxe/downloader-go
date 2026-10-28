package telegram

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/backtraxe/downloader-go/internal/tgapi"
)

func TestUploadPathDirectory(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"001.jpg", "002.jpg", "video.mp4"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("data"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	rec := &fakeTelegram{}
	srv := httptest.NewServer(http.HandlerFunc(rec.handler))
	t.Cleanup(srv.Close)
	u := &Uploader{API: &tgapi.Client{Token: "test", BaseURL: srv.URL, HTTP: srv.Client()}}

	sent, skipped, err := UploadPath(context.Background(), u, "123", dir, "相册标题")
	if err != nil {
		t.Fatal(err)
	}
	if sent != 3 || len(skipped) != 0 {
		t.Fatalf("sent=%d skipped=%v", sent, skipped)
	}
	rec.mu.Lock()
	groups := rec.groups
	rec.mu.Unlock()
	if len(groups) != 1 || len(groups[0]) != 3 {
		t.Fatalf("groups=%v want one group of 3", groups)
	}
	if groups[0][0].Caption != "相册标题" {
		t.Fatalf("caption=%q", groups[0][0].Caption)
	}
	for i := 1; i < len(groups[0]); i++ {
		if groups[0][i].Caption != "" {
			t.Fatalf("extra caption at %d: %q", i, groups[0][i].Caption)
		}
	}
	types := []string{groups[0][0].Type, groups[0][1].Type, groups[0][2].Type}
	if strings.Join(types, ",") != "photo,photo,video" {
		t.Fatalf("types=%v", types)
	}
}

func TestUploadPathSingleFileDerivesTitle(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "我的视频.mp4")
	if err := os.WriteFile(src, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}

	rec := &fakeTelegram{}
	srv := httptest.NewServer(http.HandlerFunc(rec.handler))
	t.Cleanup(srv.Close)
	u := &Uploader{API: &tgapi.Client{Token: "test", BaseURL: srv.URL, HTTP: srv.Client()}}

	sent, skipped, err := UploadPath(context.Background(), u, "123", src, "")
	if err != nil {
		t.Fatal(err)
	}
	if sent != 1 || len(skipped) != 0 {
		t.Fatalf("sent=%d skipped=%v", sent, skipped)
	}
	rec.mu.Lock()
	singles := append([]string(nil), rec.singles...)
	rec.mu.Unlock()
	if len(singles) != 1 || singles[0] != "我的视频" {
		t.Fatalf("singles=%v", singles)
	}
}

func TestUploadPathMissingFile(t *testing.T) {
	u := &Uploader{API: &tgapi.Client{Token: "test"}}
	if _, _, err := UploadPath(context.Background(), u, "123", filepath.Join(t.TempDir(), "nope"), ""); err == nil {
		t.Fatal("expected error for missing path")
	}
}
