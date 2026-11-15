package telegram

import (
	"context"
	"encoding/json"
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

// TestSendSplitsAlbumByConvertedUploadSize 验证相册按实际转换产物的总体积
// 拆分，避免每项都合法但单个 multipart 请求超过 Telegram 的 50MiB 限制。
func TestSendSplitsAlbumByConvertedUploadSize(t *testing.T) {
	dir := t.TempDir()
	files := make([]string, 0, 6)
	for i := 0; i < 6; i++ {
		path := filepath.Join(dir, fmt.Sprintf("p%d.heic", i))
		if err := os.WriteFile(path, heicMagic(), 0o600); err != nil {
			t.Fatal(err)
		}
		files = append(files, path)
	}

	const serverLimit int64 = 50 << 20
	var mu sync.Mutex
	var albumSizes []int
	singleCount := 0
	handler := func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if req.ContentLength > serverLimit {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": false, "error_code": 413, "description": "Request Entity Too Large",
			})
			return
		}
		if err := req.ParseMultipartForm(1 << 20); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.MultipartForm != nil {
			defer req.MultipartForm.RemoveAll()
		}
		switch {
		case strings.HasSuffix(req.URL.Path, "/sendMediaGroup"):
			var items []albumItem
			if err := json.Unmarshal([]byte(req.FormValue("media")), &items); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			mu.Lock()
			albumSizes = append(albumSizes, len(items))
			mu.Unlock()
			messages := make([]map[string]any, len(items))
			for i := range messages {
				messages[i] = map[string]any{"message_id": i + 1}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": messages})
		case strings.HasSuffix(req.URL.Path, "/sendPhoto"):
			mu.Lock()
			singleCount++
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true, "result": map[string]any{"message_id": 99},
			})
		default:
			http.Error(w, "unexpected method "+req.URL.Path, http.StatusNotFound)
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(handler))
	defer srv.Close()

	u := &Uploader{
		API:     &tgapi.Client{Token: "test", BaseURL: srv.URL, HTTP: srv.Client()},
		WorkDir: t.TempDir(),
		Run: func(_ context.Context, _ string, args ...string) ([]byte, []byte, error) {
			out := args[len(args)-1]
			writeTestJPEG(t, out, 100, 100)
			if err := os.Truncate(out, 9<<20); err != nil {
				return nil, nil, err
			}
			return nil, nil, nil
		},
	}
	sent, skipped, err := u.Send(context.Background(), "123", result.Post{Files: files})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if sent != 6 || len(skipped) != 0 {
		t.Fatalf("sent=%d skipped=%v", sent, skipped)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(albumSizes) != 1 || albumSizes[0] != 5 || singleCount != 1 {
		t.Fatalf("albums=%v singles=%d want albums=[5] singles=1", albumSizes, singleCount)
	}
}
