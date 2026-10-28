package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/backtraxe/downloader-go/internal/result"
)

func TestRunVersion(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{"version"}, strings.NewReader(""), &out, &errOut)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "dl-go dev") {
		t.Fatalf("unexpected version output: %q", out.String())
	}
}

func TestRunCLIHandlesQAndURLs(t *testing.T) {
	var out bytes.Buffer
	seen := []string{}
	deps := dependencies{dispatch: func(_ context.Context, u string, _ bool) result.Result {
		seen = append(seen, u)
		return result.Result{Status: result.Downloaded, Message: "ok"}
	}}
	code := runWith(nil, strings.NewReader("https://a.com/x\nq\nhttps://b.com/y\n"), &out, &out, deps)
	if code != 0 || len(seen) != 1 || seen[0] != "https://a.com/x" {
		t.Fatalf("code=%d seen=%v", code, seen)
	}
	if !strings.Contains(out.String(), "downloaded: ok") {
		t.Fatalf("unexpected output: %q", out.String())
	}
}

func TestRunCLIForceFlagAndShareText(t *testing.T) {
	var out bytes.Buffer
	var gotForce bool
	seen := []string{}
	deps := dependencies{dispatch: func(_ context.Context, u string, force bool) result.Result {
		seen = append(seen, u)
		gotForce = force
		return result.Result{Status: result.Downloaded, Message: "ok"}
	}}
	code := runWith([]string{"run", "-f"}, strings.NewReader("看这个 https://a.com/x 很好\n"), &out, &out, deps)
	if code != 0 || len(seen) != 1 || seen[0] != "https://a.com/x" || !gotForce {
		t.Fatalf("code=%d seen=%v force=%v", code, seen, gotForce)
	}
}

func TestRunCLIWithoutDispatchFails(t *testing.T) {
	var out bytes.Buffer
	if code := runWith(nil, strings.NewReader("https://a.com/x\n"), &out, &out, dependencies{}); code != 1 {
		t.Fatalf("code=%d output=%q", code, out.String())
	}
}

func TestRunUnknownCommandKeepsNotImplementedExit(t *testing.T) {
	var out bytes.Buffer
	if code := runWith([]string{"frobnicate"}, strings.NewReader(""), &out, &out, dependencies{}); code != 2 {
		t.Fatalf("code=%d", code)
	}
	if !strings.Contains(out.String(), "尚未接入") {
		t.Fatalf("output=%q", out.String())
	}
}

func TestResolveDataDir(t *testing.T) {
	t.Setenv("DOWNLOADER_HOME", "/env/data")
	if got := resolveDataDir("/flag/data"); got != "/flag/data" {
		t.Fatalf("flag: %q", got)
	}
	if got := resolveDataDir(""); got != "/env/data" {
		t.Fatalf("env: %q", got)
	}
	t.Setenv("DOWNLOADER_HOME", "")
	if got := resolveDataDir(""); got != "." {
		t.Fatalf("cwd fallback: %q", got)
	}
}

func TestParseUploadArgs(t *testing.T) {
	tests := []struct {
		args    []string
		path    string
		caption string
		wantErr bool
	}{
		{args: []string{"a.jpg"}, path: "a.jpg"},
		{args: []string{"a.jpg", "--caption", "标题"}, path: "a.jpg", caption: "标题"},
		{args: []string{"--caption=标题", "a.jpg"}, path: "a.jpg", caption: "标题"},
		{args: nil, wantErr: true},
		{args: []string{"a", "b"}, wantErr: true},
		{args: []string{"a", "--caption"}, wantErr: true},
		{args: []string{"--nope", "a"}, wantErr: true},
	}
	for _, tt := range tests {
		path, caption, err := parseUploadArgs(tt.args)
		if tt.wantErr {
			if err == nil {
				t.Fatalf("args=%v: expected error", tt.args)
			}
			continue
		}
		if err != nil || path != tt.path || caption != tt.caption {
			t.Fatalf("args=%v: path=%q caption=%q err=%v", tt.args, path, caption, err)
		}
	}
}

// TestTelegramMediaTimeout 确认共享 HTTP 客户端按媒体上传放宽超时，
// 文字调用仍由各自 60s 的 context 收紧。
func TestTelegramMediaTimeout(t *testing.T) {
	if telegramMediaTimeout <= telegramRequestTimeout {
		t.Fatalf("media timeout %s must exceed text timeout %s", telegramMediaTimeout, telegramRequestTimeout)
	}
	api := newTelegramAPI("token")
	if api.HTTP == nil || api.HTTP.Timeout != telegramMediaTimeout {
		t.Fatalf("api timeout=%v want %v", api.HTTP.Timeout, telegramMediaTimeout)
	}
	if telegramMediaTimeout != 10*time.Minute {
		t.Fatalf("media timeout=%s want 10m", telegramMediaTimeout)
	}
}

func TestRunUploadMissingConfig(t *testing.T) {
	var out, errOut bytes.Buffer
	code := runUpload(context.Background(), t.TempDir(), []string{"some-file"}, &out, &errOut)
	if code != 1 {
		t.Fatalf("exit=%d stderr=%q", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "请先在") {
		t.Fatalf("stderr=%q", errOut.String())
	}
}

func TestRunUploadUsageError(t *testing.T) {
	var out, errOut bytes.Buffer
	code := runUpload(context.Background(), t.TempDir(), nil, &out, &errOut)
	if code != 2 || !strings.Contains(errOut.String(), "用法") {
		t.Fatalf("exit=%d stderr=%q", code, errOut.String())
	}
}

func TestRunBotMissingConfig(t *testing.T) {
	var errOut bytes.Buffer
	code := runBot(context.Background(), t.TempDir(), func(context.Context, string, bool) result.Result {
		return result.Result{}
	}, &errOut)
	if code != 1 || !strings.Contains(errOut.String(), "请先在") {
		t.Fatalf("exit=%d stderr=%q", code, errOut.String())
	}
}

func TestRunWithRoutesBotAndUpload(t *testing.T) {
	var out bytes.Buffer
	var botCalled bool
	var uploadArgs []string
	deps := dependencies{
		bot: func(context.Context, io.Writer, io.Writer) int {
			botCalled = true
			return 0
		},
		upload: func(_ context.Context, args []string, _, _ io.Writer) int {
			uploadArgs = args
			return 0
		},
	}
	if code := runWith([]string{"bot"}, strings.NewReader(""), &out, &out, deps); code != 0 || !botCalled {
		t.Fatalf("bot code=%d called=%v", code, botCalled)
	}
	if code := runWith([]string{"upload", "a.jpg", "--caption", "标题"}, strings.NewReader(""), &out, &out, deps); code != 0 {
		t.Fatalf("upload code=%d", code)
	}
	if strings.Join(uploadArgs, " ") != "a.jpg --caption 标题" {
		t.Fatalf("uploadArgs=%v", uploadArgs)
	}
}

func TestRunBotPlaceholderConfig(t *testing.T) {
	dir := t.TempDir()
	template := `{"telegram":{"bot_token":"在此填入 @BotFather 给的 token","chat_id":0,"allowed_users":[],"process_message_ttl_seconds":300}}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(template), 0o600); err != nil {
		t.Fatal(err)
	}
	var errOut bytes.Buffer
	code := runBot(context.Background(), dir, func(context.Context, string, bool) result.Result {
		return result.Result{}
	}, &errOut)
	if code != 1 || !strings.Contains(errOut.String(), "请先在") {
		t.Fatalf("exit=%d stderr=%q", code, errOut.String())
	}
}

func TestRunUploadPlaceholderConfig(t *testing.T) {
	dir := t.TempDir()
	template := `{"telegram":{"bot_token":"在此填入 @BotFather 给的 token","chat_id":0,"allowed_users":[],"process_message_ttl_seconds":300}}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(template), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code := runUpload(context.Background(), dir, []string{"some-file"}, &out, &errOut)
	if code != 1 || !strings.Contains(errOut.String(), "请先在") {
		t.Fatalf("exit=%d stderr=%q", code, errOut.String())
	}
}
