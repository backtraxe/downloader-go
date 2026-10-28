package ytdlp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDownloadProbesThenDownloads(t *testing.T) {
	dir := t.TempDir()
	var calls [][]string
	run := func(ctx context.Context, args ...string) ([]byte, []byte, error) {
		calls = append(calls, args)
		if contains(args, "-J") {
			return []byte(`{"title":"标题","uploader":"作者","ext":"mp4"}`), nil, nil
		}
		p := filepath.Join(dir, "download", "youtube", "作者", "标题.mp4")
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("V"), 0o600)
		return []byte(p + "\n"), nil, nil
	}
	d := &Downloader{DataDir: dir, Bin: "yt-dlp", Run: run}
	res := d.Download(context.Background(), "https://youtu.be/x", false)
	if res.Status != "downloaded" || len(res.Posts) != 1 || res.Posts[0].Author != "作者" {
		t.Fatalf("%+v", res)
	}
	if len(calls) != 2 {
		t.Fatalf("calls=%v", calls)
	}
	if !contains(calls[1], "--print") {
		t.Fatalf("missing print: %v", calls[1])
	}
}

func TestMissingBinaryFailsWithHint(t *testing.T) {
	d := &Downloader{DataDir: t.TempDir(), Bin: "yt-dlp", Run: func(context.Context, ...string) ([]byte, []byte, error) {
		return nil, nil, errors.New("exec: \"yt-dlp\": executable file not found in $PATH")
	}}
	res := d.Download(context.Background(), "https://youtu.be/x", false)
	if res.Status != "failed" || !strings.Contains(res.Message, "yt-dlp") {
		t.Fatalf("%+v", res)
	}
}

func contains(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// newTestDownloader 返回一个按 url 决定探测 JSON、下载 stdout 的 Downloader，
// 并把每次调用参数记录到 calls。
func newTestDownloader(t *testing.T, dir, probeJSON, downloadOut string) (*Downloader, *[][]string) {
	t.Helper()
	calls := &[][]string{}
	run := func(ctx context.Context, args ...string) ([]byte, []byte, error) {
		*calls = append(*calls, args)
		if contains(args, "-J") {
			return []byte(probeJSON), nil, nil
		}
		return []byte(downloadOut), nil, nil
	}
	return &Downloader{DataDir: dir, Bin: "yt-dlp", Run: run}, calls
}

func TestNewSetsDefaults(t *testing.T) {
	d := New("/data")
	if d.Bin != "yt-dlp" || d.DataDir != "/data" || d.Run == nil {
		t.Fatalf("%+v", d)
	}
}

func TestCookiePassedOnlyWhenFileNonEmpty(t *testing.T) {
	dir := t.TempDir()
	cookiePath := filepath.Join(dir, "cookies", "douyin.txt")
	if err := os.MkdirAll(filepath.Dir(cookiePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cookiePath, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	d, calls := newTestDownloader(t, dir, `{"title":"t","uploader":"u","ext":"mp4"}`, "")
	d.Download(context.Background(), "https://v.douyin.com/x", false)
	for _, args := range *calls {
		if contains(args, "--cookies") {
			t.Fatalf("empty cookie file must be ignored: %v", args)
		}
	}

	if err := os.WriteFile(cookiePath, []byte("sessionid=secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := len(*calls)
	d.Download(context.Background(), "https://v.douyin.com/x", false)
	for _, args := range (*calls)[before:] {
		if !contains(args, "--cookies") || !contains(args, cookiePath) {
			t.Fatalf("cookie path missing: %v", args)
		}
		// Cookie 内容绝不能出现在参数里。
		for _, a := range args {
			if strings.Contains(a, "secret") {
				t.Fatalf("cookie value leaked: %v", args)
			}
		}
	}
}

func TestAuthorSegmentOmittedWhenBlank(t *testing.T) {
	dir := t.TempDir()
	d, calls := newTestDownloader(t, dir, `{"title":"t","uploader":"   ","ext":"mp4"}`, "")
	res := d.Download(context.Background(), "https://youtu.be/x", false)
	if res.Status != "failed" {
		t.Fatalf("%+v", res)
	}
	args := (*calls)[1]
	outtmpl := args[indexOf(args, "-o")+1]
	want := filepath.Join(dir, "download", "youtube", "%(title).200B.%(ext)s")
	if outtmpl != want {
		t.Fatalf("outtmpl=%q want %q", outtmpl, want)
	}
}

func TestAuthorIsSanitizedAndForceAddsOverwrites(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "download", "youtube", "ab", "t.mp4")
	os.MkdirAll(filepath.Dir(out), 0o755)
	os.WriteFile(out, []byte("v"), 0o600)

	d, calls := newTestDownloader(t, dir, `{"title":"t","uploader":"a/b","ext":"mp4"}`, out+"\n")
	res := d.Download(context.Background(), "https://youtu.be/x", true)
	if res.Status != "downloaded" {
		t.Fatalf("%+v", res)
	}
	args := (*calls)[1]
	outtmpl := args[indexOf(args, "-o")+1]
	want := filepath.Join(dir, "download", "youtube", "ab", "%(title).200B.%(ext)s")
	if outtmpl != want {
		t.Fatalf("outtmpl=%q want %q", outtmpl, want)
	}
	if !contains(args, "--force-overwrites") {
		t.Fatalf("missing force: %v", args)
	}
}

func TestDownloadParsesExistingFilesOnly(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.mp4")
	b := filepath.Join(dir, "b.mp4")
	os.WriteFile(a, []byte("a"), 0o600)
	os.WriteFile(b, []byte("b"), 0o600)
	stdout := "[youtube] extracting\n" + a + "\nnot-a-file\n\n" + b + "\n"

	d, _ := newTestDownloader(t, dir, `{"title":"t","uploader":"u","ext":"mp4"}`, stdout)
	res := d.Download(context.Background(), "https://youtu.be/x", false)
	if res.Status != "downloaded" || len(res.Posts) != 1 {
		t.Fatalf("%+v", res)
	}
	post := res.Posts[0]
	if post.Status != "downloaded" || post.Site != "youtube" || post.Title != "t" || post.Author != "u" {
		t.Fatalf("%+v", post)
	}
	if len(post.Files) != 2 || post.Files[0] != a || post.Files[1] != b {
		t.Fatalf("files=%v", post.Files)
	}
}

func TestRelativeOutputResolvedAgainstCwd(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile("rel.mp4", []byte("v"), 0o600); err != nil {
		t.Fatal(err)
	}
	d, _ := newTestDownloader(t, dir, `{"title":"t","uploader":"u","ext":"mp4"}`, "rel.mp4\n")
	res := d.Download(context.Background(), "https://youtu.be/x", false)
	if res.Status != "downloaded" || len(res.Posts[0].Files) != 1 || res.Posts[0].Files[0] != filepath.Join(dir, "rel.mp4") {
		t.Fatalf("%+v", res)
	}
}

func TestNoFileOutputFailsWithStderr(t *testing.T) {
	d := &Downloader{DataDir: t.TempDir(), Bin: "yt-dlp", Run: func(ctx context.Context, args ...string) ([]byte, []byte, error) {
		if contains(args, "-J") {
			return []byte(`{"title":"t","uploader":"u","ext":"mp4"}`), nil, nil
		}
		return nil, []byte("ERROR: something exploded\n"), errors.New("exit status 1")
	}}
	res := d.Download(context.Background(), "https://youtu.be/x", false)
	if res.Status != "failed" || !strings.Contains(res.Message, "something exploded") {
		t.Fatalf("%+v", res)
	}
}

func TestFreshCookiesMapsToSignatureBlock(t *testing.T) {
	d := &Downloader{DataDir: t.TempDir(), Bin: "yt-dlp", Run: func(context.Context, ...string) ([]byte, []byte, error) {
		return nil, []byte("ERROR: Fresh cookies (not necessarily logged in) are needed\n"), errors.New("exit status 1")
	}}
	res := d.Download(context.Background(), "https://v.douyin.com/x", false)
	if res.Status != "failed" || !strings.Contains(res.Message, "抖音") || !strings.Contains(res.Message, "签名") {
		t.Fatalf("%+v", res)
	}
}

func TestLoginErrorMapsToCookieHint(t *testing.T) {
	d := &Downloader{DataDir: t.TempDir(), Bin: "yt-dlp", Run: func(context.Context, ...string) ([]byte, []byte, error) {
		return nil, []byte("ERROR: Sign in to confirm your age\n"), errors.New("exit status 1")
	}}
	res := d.Download(context.Background(), "https://youtu.be/x", false)
	if res.Status != "failed" || !strings.Contains(res.Message, "Cookie") {
		t.Fatalf("%+v", res)
	}
}

func TestLongStderrTruncated(t *testing.T) {
	d := &Downloader{DataDir: t.TempDir(), Bin: "yt-dlp", Run: func(context.Context, ...string) ([]byte, []byte, error) {
		return nil, []byte(strings.Repeat("x", 5000)), errors.New("exit status 1")
	}}
	res := d.Download(context.Background(), "https://youtu.be/x", false)
	if res.Status != "failed" || len([]rune(res.Message)) > 700 {
		t.Fatalf("message too long: %d", len([]rune(res.Message)))
	}
}

func TestProbeFailureMapsError(t *testing.T) {
	d := &Downloader{DataDir: t.TempDir(), Bin: "yt-dlp", Run: func(context.Context, ...string) ([]byte, []byte, error) {
		return nil, []byte("ERROR: Unsupported URL\n"), errors.New("exit status 1")
	}}
	res := d.Download(context.Background(), "https://example.com/x", false)
	if res.Status != "failed" || !strings.Contains(res.Message, "Unsupported URL") {
		t.Fatalf("%+v", res)
	}
}

func indexOf(args []string, want string) int {
	for i, a := range args {
		if a == want {
			return i
		}
	}
	return -1
}
