package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/backtraxe/downloader-go/internal/config"
	"github.com/backtraxe/downloader-go/internal/result"
	"github.com/backtraxe/downloader-go/internal/tgapi"
)

// sentText 记录一条 sendMessage 请求。
type sentText struct {
	chatID string
	text   string
}

// fakeTelegram 是 Bot API 的离线替身，记录文字消息、删除请求与上传内容。
type fakeTelegram struct {
	mu        sync.Mutex
	texts     []sentText
	deleted   []int64
	uploads   []string
	groups    [][]albumItem
	singles   []string
	documents []string
	nextID    int64
}

func (f *fakeTelegram) handler(w http.ResponseWriter, req *http.Request) {
	if strings.HasPrefix(req.Header.Get("Content-Type"), "multipart/form-data") {
		if err := req.ParseMultipartForm(32 << 20); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	} else if err := req.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	ok := func(value any) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": value})
	}
	recordFiles := func() {
		if req.MultipartForm == nil {
			return
		}
		for _, headers := range req.MultipartForm.File {
			for _, fh := range headers {
				f.uploads = append(f.uploads, fh.Filename)
			}
		}
	}

	switch {
	case strings.HasSuffix(req.URL.Path, "/sendMessage"):
		f.mu.Lock()
		f.texts = append(f.texts, sentText{chatID: req.FormValue("chat_id"), text: req.FormValue("text")})
		f.nextID++
		id := f.nextID
		f.mu.Unlock()
		ok(map[string]any{"message_id": id})
	case strings.HasSuffix(req.URL.Path, "/deleteMessage"):
		id, _ := strconv.ParseInt(req.FormValue("message_id"), 10, 64)
		f.mu.Lock()
		f.deleted = append(f.deleted, id)
		f.mu.Unlock()
		ok(true)
	case strings.HasSuffix(req.URL.Path, "/sendMediaGroup"):
		var items []albumItem
		if err := json.Unmarshal([]byte(req.FormValue("media")), &items); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.groups = append(f.groups, items)
		recordFiles()
		f.mu.Unlock()
		result := make([]map[string]any, len(items))
		for i := range result {
			result[i] = map[string]any{"message_id": i + 1}
		}
		ok(result)
	case strings.HasSuffix(req.URL.Path, "/sendDocument"):
		f.mu.Lock()
		f.documents = append(f.documents, req.FormValue("caption"))
		recordFiles()
		f.mu.Unlock()
		ok(map[string]any{"message_id": 90})
	case strings.HasSuffix(req.URL.Path, "/sendPhoto"), strings.HasSuffix(req.URL.Path, "/sendVideo"):
		f.mu.Lock()
		f.singles = append(f.singles, req.FormValue("caption"))
		recordFiles()
		f.mu.Unlock()
		ok(map[string]any{"message_id": 91})
	default:
		http.Error(w, "unexpected method "+req.URL.Path, http.StatusNotFound)
	}
}

func (f *fakeTelegram) replies() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.texts))
	for _, t := range f.texts {
		out = append(out, t.text)
	}
	return out
}

func (f *fakeTelegram) uploadedNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.uploads))
	copy(out, f.uploads)
	return out
}

func (f *fakeTelegram) uploadedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.groups) + len(f.singles) + len(f.documents)
}

func newFakeBot(t *testing.T, rec *fakeTelegram, cfg config.Telegram, dispatch func(context.Context, string, bool) result.Result) *Bot {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(rec.handler))
	t.Cleanup(srv.Close)
	api := &tgapi.Client{Token: "test", BaseURL: srv.URL, HTTP: srv.Client()}
	return &Bot{
		API:      api,
		Config:   cfg,
		Dispatch: dispatch,
		Uploader: &Uploader{API: api},
	}
}

func channelID(id string) json.Number { return json.Number(id) }

func message(chatID, chatType string, userID int64, text string) tgapi.Message {
	return tgapi.Message{
		MessageID: 10,
		Chat:      tgapi.Chat{ID: channelID(chatID), Type: chatType},
		From:      tgapi.User{ID: userID},
		Text:      text,
	}
}

func intPtr(v int) *int { return &v }

func TestHandleMessageDeniesUnauthorizedUser(t *testing.T) {
	rec := &fakeTelegram{}
	calls := 0
	bot := newFakeBot(t, rec, config.Telegram{AllowedUsers: []int64{1}}, func(context.Context, string, bool) result.Result {
		calls++
		return result.Result{Status: result.Skipped}
	})

	if err := bot.HandleMessage(context.Background(), message("10", "private", 2, "https://example.com/x")); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("dispatch calls=%d want 0", calls)
	}
	replies := rec.replies()
	if len(replies) != 1 || !strings.Contains(replies[0], "未授权") || !strings.Contains(replies[0], "2") {
		t.Fatalf("replies=%v", replies)
	}
}

func TestHandleMessageChannelBypassesAllowlist(t *testing.T) {
	rec := &fakeTelegram{}
	calls := 0
	bot := newFakeBot(t, rec, config.Telegram{AllowedUsers: []int64{1}}, func(_ context.Context, url string, _ bool) result.Result {
		calls++
		return result.Result{Status: result.Skipped}
	})

	if err := bot.HandleMessage(context.Background(), message("-100", "channel", 0, "https://example.com/x")); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("dispatch calls=%d want 1", calls)
	}
	replies := strings.Join(rec.replies(), "\n")
	if strings.Contains(replies, "未授权") {
		t.Fatalf("channel was denied: %q", replies)
	}
	if !strings.Contains(replies, "开始下载：https://example.com/x") || !strings.Contains(replies, "已下载过") {
		t.Fatalf("replies=%q", replies)
	}
}

func TestHandleMessageStartHelpAndNoURL(t *testing.T) {
	for _, text := range []string{"/start", "/help"} {
		t.Run(text, func(t *testing.T) {
			rec := &fakeTelegram{}
			calls := 0
			bot := newFakeBot(t, rec, config.Telegram{AllowedUsers: []int64{1}}, func(context.Context, string, bool) result.Result {
				calls++
				return result.Result{}
			})
			if err := bot.HandleMessage(context.Background(), message("10", "private", 1, text)); err != nil {
				t.Fatal(err)
			}
			if calls != 0 {
				t.Fatalf("dispatch calls=%d", calls)
			}
			replies := rec.replies()
			if len(replies) != 1 || !strings.Contains(replies[0], "发链接即可") {
				t.Fatalf("replies=%v", replies)
			}
		})
	}

	rec := &fakeTelegram{}
	bot := newFakeBot(t, rec, config.Telegram{AllowedUsers: []int64{1}}, func(context.Context, string, bool) result.Result {
		t.Fatal("dispatch should not be called without a URL")
		return result.Result{}
	})
	if err := bot.HandleMessage(context.Background(), message("10", "private", 1, "随便聊聊")); err != nil {
		t.Fatal(err)
	}
	replies := rec.replies()
	if len(replies) != 1 || !strings.Contains(replies[0], "没识别到链接") {
		t.Fatalf("replies=%v", replies)
	}
}

func TestHandleMessageSkippedAndFailed(t *testing.T) {
	t.Run("skipped", func(t *testing.T) {
		rec := &fakeTelegram{}
		bot := newFakeBot(t, rec, config.Telegram{AllowedUsers: []int64{1}}, func(context.Context, string, bool) result.Result {
			return result.Result{Status: result.Skipped}
		})
		if err := bot.HandleMessage(context.Background(), message("10", "private", 1, "https://example.com/x")); err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(rec.replies(), "\n"); !strings.Contains(got, "已下载过") {
			t.Fatalf("replies=%q", got)
		}
		if rec.uploadedCount() != 0 {
			t.Fatalf("unexpected uploads: %d", rec.uploadedCount())
		}
	})

	t.Run("failed", func(t *testing.T) {
		rec := &fakeTelegram{}
		bot := newFakeBot(t, rec, config.Telegram{AllowedUsers: []int64{1}}, func(context.Context, string, bool) result.Result {
			return result.Result{Status: result.Failed, Message: "网络错误"}
		})
		if err := bot.HandleMessage(context.Background(), message("10", "private", 1, "https://example.com/x")); err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(rec.replies(), "\n"); !strings.Contains(got, "下载失败：网络错误") {
			t.Fatalf("replies=%q", got)
		}
		if rec.uploadedCount() != 0 {
			t.Fatalf("unexpected uploads: %d", rec.uploadedCount())
		}
	})

	t.Run("no_media", func(t *testing.T) {
		rec := &fakeTelegram{}
		bot := newFakeBot(t, rec, config.Telegram{AllowedUsers: []int64{1}}, func(context.Context, string, bool) result.Result {
			return result.Result{Status: result.NoMedia, Message: "页面无媒体"}
		})
		if err := bot.HandleMessage(context.Background(), message("10", "private", 1, "https://example.com/x")); err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(rec.replies(), "\n"); !strings.Contains(got, "没有找到可下载的媒体") {
			t.Fatalf("replies=%q", got)
		}
		if rec.uploadedCount() != 0 {
			t.Fatalf("unexpected uploads: %d", rec.uploadedCount())
		}
	})
}

func TestHandleMessageUploadsOnlyNewFiles(t *testing.T) {
	dir := t.TempDir()
	new1 := filepath.Join(dir, "new1.jpg")
	new2 := filepath.Join(dir, "new2.jpg")
	old := filepath.Join(dir, "old.jpg")
	for _, path := range []string{new1, new2, old} {
		writeTestJPEG(t, path, 10, 10)
	}

	rec := &fakeTelegram{}
	bot := newFakeBot(t, rec, config.Telegram{AllowedUsers: []int64{1}}, func(context.Context, string, bool) result.Result {
		return result.Result{
			Status: result.Downloaded,
			Posts: []result.Post{
				{Title: "第一", Files: []string{new1}},
				{Title: "第二", Files: []string{new2}},
			},
		}
	})
	if err := bot.HandleMessage(context.Background(), message("10", "private", 1, "https://example.com/x")); err != nil {
		t.Fatal(err)
	}

	names := strings.Join(rec.uploadedNames(), ",")
	if !strings.Contains(names, "new1.jpg") || !strings.Contains(names, "new2.jpg") {
		t.Fatalf("uploads=%v", rec.uploadedNames())
	}
	if strings.Contains(names, "old.jpg") {
		t.Fatalf("stale file uploaded: %v", rec.uploadedNames())
	}
	if len(rec.singles) != 2 {
		t.Fatalf("singles=%v", rec.singles)
	}
	replies := strings.Join(rec.replies(), "\n")
	if !strings.Contains(replies, "完成：2 项已回传") {
		t.Fatalf("replies=%q", replies)
	}
}

func TestHandleMessagePartialAppendsDetail(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.jpg")
	writeTestJPEG(t, file, 10, 10)

	rec := &fakeTelegram{}
	bot := newFakeBot(t, rec, config.Telegram{AllowedUsers: []int64{1}}, func(context.Context, string, bool) result.Result {
		return result.Result{
			Status:  result.Partial,
			Message: "失败 1 项",
			Posts:   []result.Post{{Title: "部分", Files: []string{file}}},
		}
	})
	if err := bot.HandleMessage(context.Background(), message("10", "private", 1, "https://example.com/x")); err != nil {
		t.Fatal(err)
	}
	replies := strings.Join(rec.replies(), "\n")
	if !strings.Contains(replies, "完成：1 项已回传") || !strings.Contains(replies, "失败 1 项") {
		t.Fatalf("replies=%q", replies)
	}
}

// TestHandleMessageReportsSkippedPaths 验证超限跳过的本地路径逐条回显，
// 而不仅是一个计数。
func TestHandleMessageReportsSkippedPaths(t *testing.T) {
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

	rec := &fakeTelegram{}
	bot := newFakeBot(t, rec, config.Telegram{AllowedUsers: []int64{1}}, func(context.Context, string, bool) result.Result {
		return result.Result{
			Status: result.Downloaded,
			Posts:  []result.Post{{Title: "超限", Files: []string{big}}},
		}
	})
	if err := bot.HandleMessage(context.Background(), message("10", "private", 1, "https://example.com/x")); err != nil {
		t.Fatal(err)
	}
	replies := strings.Join(rec.replies(), "\n")
	if !strings.Contains(replies, "1 项超限跳过") {
		t.Fatalf("missing skip count: %q", replies)
	}
	if !strings.Contains(replies, big) {
		t.Fatalf("missing skipped path: %q", replies)
	}
	if rec.uploadedCount() != 0 {
		t.Fatalf("oversized file must not be uploaded: %d", rec.uploadedCount())
	}
}

func TestHandleMessageSchedulesDeletionForTextOnly(t *testing.T) {
	rec := &fakeTelegram{}
	timers := &timerRecorder{}
	bot := newFakeBot(t, rec, config.Telegram{
		AllowedUsers:             []int64{1},
		ProcessMessageTTLSeconds: intPtr(7),
	}, func(context.Context, string, bool) result.Result {
		return result.Result{
			Status: result.Downloaded,
			Posts:  []result.Post{{Title: "标题", Files: []string{writeTempJPEG(t)}}},
		}
	})
	bot.AfterFunc = timers.after

	if err := bot.HandleMessage(context.Background(), message("10", "private", 1, "/start")); err != nil {
		t.Fatal(err)
	}
	if got := timers.recorded(); len(got) != 1 || got[0] != 7*time.Second {
		t.Fatalf("timers=%v want [7s]", got)
	}

	if err := bot.HandleMessage(context.Background(), message("10", "private", 1, "https://example.com/x")); err != nil {
		t.Fatal(err)
	}
	// 新增两次文字回复（开始下载、完成），媒体上传本身不调度删除。
	if got := timers.recorded(); len(got) != 3 {
		t.Fatalf("timers=%v want 3 entries", got)
	}

	// 回调删除对应文字消息；媒体消息不在删除列表中。
	timers.run(0)
	deleted := rec.deletedIDs()
	if len(deleted) != 1 || deleted[0] != 1 {
		t.Fatalf("deleted=%v want [1]", deleted)
	}
}

func TestHandleMessageDoesNotScheduleWhenTTLDisabled(t *testing.T) {
	rec := &fakeTelegram{}
	timers := &timerRecorder{}
	bot := newFakeBot(t, rec, config.Telegram{
		AllowedUsers:             []int64{1},
		ProcessMessageTTLSeconds: intPtr(0),
	}, func(context.Context, string, bool) result.Result {
		return result.Result{}
	})
	bot.AfterFunc = timers.after
	if err := bot.HandleMessage(context.Background(), message("10", "private", 1, "/start")); err != nil {
		t.Fatal(err)
	}
	if got := timers.recorded(); len(got) != 0 {
		t.Fatalf("timers=%v want none", got)
	}
}

func writeTempJPEG(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "p.jpg")
	writeTestJPEG(t, path, 10, 10)
	return path
}

type timerRecorder struct {
	mu     sync.Mutex
	delays []time.Duration
	funcs  []func()
}

func (r *timerRecorder) after(d time.Duration, f func()) *time.Timer {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.delays = append(r.delays, d)
	r.funcs = append(r.funcs, f)
	return nil
}

func (r *timerRecorder) recorded() []time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]time.Duration, len(r.delays))
	copy(out, r.delays)
	return out
}

func (r *timerRecorder) run(i int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.funcs[i]()
}

func (f *fakeTelegram) deletedIDs() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]int64, len(f.deleted))
	copy(out, f.deleted)
	return out
}

func TestRunSkipsBacklogAndAdvancesOffset(t *testing.T) {
	var mu sync.Mutex
	var offsets []int64
	polled := make(chan int64, 32)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if err := req.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		offset, _ := strconv.ParseInt(req.FormValue("offset"), 10, 64)
		mu.Lock()
		offsets = append(offsets, offset)
		mu.Unlock()
		select {
		case polled <- offset:
		default:
		}

		w.Header().Set("Content-Type", "application/json")
		switch offset {
		case 0:
			// 积压消息：最大 update_id 为 5，下次应从 6 开始。
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": []any{
				map[string]any{"update_id": 3},
				map[string]any{"update_id": 5},
			}})
		case 6:
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": []any{
				map[string]any{
					"update_id": 6,
					"message": map[string]any{
						"message_id": 1,
						"chat":       map[string]any{"id": 9, "type": "private"},
						"from":       map[string]any{"id": 1},
						"text":       "",
					},
				},
			}})
		default:
			// 之后的轮询挂起，直到测试取消 context。
			<-req.Context().Done()
		}
	}))
	defer srv.Close()

	bot := &Bot{
		API:      &tgapi.Client{Token: "test", BaseURL: srv.URL, HTTP: srv.Client()},
		Config:   config.Telegram{AllowedUsers: []int64{1}},
		Dispatch: func(context.Context, string, bool) result.Result { return result.Result{} },
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- bot.Run(ctx) }()

	deadline := time.After(5 * time.Second)
	for {
		select {
		case off := <-polled:
			if off == 7 {
				cancel()
				select {
				case err := <-done:
					if err != nil {
						t.Fatalf("Run returned %v", err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("Run did not stop after context cancel")
				}
				mu.Lock()
				defer mu.Unlock()
				if len(offsets) < 3 || offsets[0] != 0 || offsets[1] != 6 {
					t.Fatalf("offsets=%v", offsets)
				}
				return
			}
		case <-deadline:
			cancel()
			t.Fatalf("Run never polled offset 7; offsets=%v", offsets)
		}
	}
}

func TestRunHandlesChannelPost(t *testing.T) {
	rec := &fakeTelegram{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if !strings.HasSuffix(req.URL.Path, "/getUpdates") {
			rec.handler(w, req)
			return
		}
		if err := req.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		offset, _ := strconv.ParseInt(req.FormValue("offset"), 10, 64)
		timeout, _ := strconv.Atoi(req.FormValue("timeout"))

		w.Header().Set("Content-Type", "application/json")
		if timeout == 0 || offset != 0 {
			// 启动拉取返回空积压；处理完 channel_post 后挂起等待取消。
			if offset == 0 && timeout == 0 {
				_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": []any{}})
				return
			}
			<-req.Context().Done()
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": []any{
			map[string]any{
				"update_id": 1,
				"channel_post": map[string]any{
					"message_id": 1,
					"chat":       map[string]any{"id": -100, "type": "channel"},
					"text":       "https://example.com/post",
				},
			},
		}})
	}))
	defer srv.Close()

	dispatched := make(chan string, 1)
	bot := &Bot{
		API: &tgapi.Client{Token: "test", BaseURL: srv.URL, HTTP: srv.Client()},
		// 频道绕过白名单；白名单为空也不应拒绝。
		Config: config.Telegram{},
		Dispatch: func(_ context.Context, url string, _ bool) result.Result {
			dispatched <- url
			return result.Result{Status: result.Skipped}
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- bot.Run(ctx) }()

	select {
	case url := <-dispatched:
		if url != "https://example.com/post" {
			t.Fatalf("dispatched url=%q", url)
		}
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("channel_post was not dispatched")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop after context cancel")
	}

	replies := strings.Join(rec.replies(), "\n")
	if !strings.Contains(replies, "开始下载：https://example.com/post") {
		t.Fatalf("replies=%q", replies)
	}
}
