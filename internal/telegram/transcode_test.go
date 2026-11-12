package telegram

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/jpeg"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/backtraxe/downloader-go/internal/logging"
)

// recordingRunner 记录调用并按约定造出 ffmpeg 的输出文件。
func recordingRunner(t *testing.T, calls *[][]string) Runner {
	t.Helper()
	return func(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
		*calls = append(*calls, append([]string{name}, args...))
		if name == "ffprobe" {
			return []byte("10.0\n"), nil, nil
		}
		out := args[len(args)-1]
		if err := os.WriteFile(out, []byte("out"), 0o600); err != nil {
			return nil, nil, err
		}
		return nil, nil, nil
	}
}

func joinCall(call []string) string { return strings.Join(call, " ") }

// writeTestJPEG 写出一个可被 image.DecodeConfig 识别的真实 JPEG，
// 避免测试数据因文件头无法识别而被当成需要转码的图片。
func writeTestJPEG(t *testing.T, path string, width, height int) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := jpeg.Encode(f, image.NewRGBA(image.Rect(0, 0, width, height)), nil); err != nil {
		t.Fatal(err)
	}
}

// heicMagic 是足以被文件头识别为 HEIC 的最小前缀。
func heicMagic() []byte {
	return append([]byte{0x00, 0x00, 0x00, 0x18}, []byte("ftypheic")...)
}

func TestPreparePhotoHEICUsesHEIFConvert(t *testing.T) {
	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(logging.New(&logs, slog.LevelInfo))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	dir := t.TempDir()
	src := filepath.Join(dir, "a.heic")
	if err := os.WriteFile(src, heicMagic(), 0o600); err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	u := &Uploader{FFmpeg: "ffmpeg", WorkDir: t.TempDir(), Run: recordingRunner(t, &calls)}
	got, asDoc, err := u.PreparePhoto(context.Background(), src)
	if err != nil || asDoc || !strings.HasSuffix(got, ".jpg") {
		t.Fatalf("%s %v %v", got, asDoc, err)
	}
	if len(calls) != 1 || calls[0][0] != "heif-convert" || !strings.Contains(joinCall(calls[0]), "-q 90") {
		t.Fatalf("HEIF must be assembled by libheif before JPEG preparation: %v", calls)
	}
	if strings.Contains(joinCall(calls[0]), "-frames:v") {
		t.Fatalf("single-tile ffmpeg decoding must not be used: %v", calls)
	}
	if st, err := os.Stat(got); err != nil || st.Size() == 0 {
		t.Fatalf("output missing: %v", err)
	}
	if !strings.Contains(logs.String(), "使用 libheif 转换图片") || !strings.Contains(logs.String(), "format=heic") {
		t.Fatalf("conversion diagnostics missing: %q", logs.String())
	}
}

func TestPreparePhotoHEICRechecksConvertedDimensions(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "wide.heic")
	if err := os.WriteFile(src, heicMagic(), 0o600); err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	call := 0
	u := &Uploader{
		FFmpeg:  "ffmpeg",
		WorkDir: t.TempDir(),
		Run: func(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
			calls = append(calls, append([]string{name}, args...))
			// 首次转换产出宽高和超限但宽高比正常的 JPEG；缩放阶梯再产出小图。
			w, h := 10000, 1000
			if call > 0 {
				w, h = 100, 100
			}
			call++
			out := args[len(args)-1]
			f, err := os.Create(out)
			if err != nil {
				return nil, nil, err
			}
			defer f.Close()
			return nil, nil, jpeg.Encode(f, image.NewRGBA(image.Rect(0, 0, w, h)), nil)
		},
	}
	got, asDoc, err := u.PreparePhoto(context.Background(), src)
	if err != nil || asDoc || !strings.HasSuffix(got, ".jpg") {
		t.Fatalf("%s %v %v", got, asDoc, err)
	}
	if len(calls) < 2 {
		t.Fatalf("converted JPEG dimensions were not re-checked: %v", calls)
	}
	scaling := joinCall(calls[1])
	if !strings.Contains(scaling, "-filter_complex [0:v]scale=") || !strings.Contains(scaling, "-map [out]") {
		t.Fatalf("scaling must use a single complex filtergraph: %v", calls)
	}
	if strings.Contains(scaling, " -vf ") {
		t.Fatalf("simple -vf conflicts with automatic rotation: %v", calls)
	}
	// 已完成 HEIF→JPEG 后，再压缩或缩放必须以完整 JPEG 为输入，不能重新
	// 解码原始 HEIC 并让局部图块覆盖第一份正确产物。
	inputIndex := -1
	for i, arg := range calls[1] {
		if arg == "-i" {
			inputIndex = i + 1
			break
		}
	}
	if inputIndex < 0 || calls[1][inputIndex] != got {
		t.Fatalf("later ffmpeg stage must read the converted JPEG %q: %v", got, calls[1])
	}
}

func TestPreparePhotoNormalUnchanged(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.jpg")
	writeTestJPEG(t, src, 100, 100)
	u := &Uploader{WorkDir: t.TempDir(), Run: func(context.Context, string, ...string) ([]byte, []byte, error) {
		t.Fatal("ffmpeg should not run for a small photo")
		return nil, nil, nil
	}}
	got, asDoc, err := u.PreparePhoto(context.Background(), src)
	if err != nil || asDoc || got != src {
		t.Fatalf("%s %v %v", got, asDoc, err)
	}
}

// TestPreparePhotoConvertsWebPDisguisedAsJPEG 验证按文件头而不是扩展名
// 判断格式：小红书等站点会把 WebP 原图存成 .jpg，直接进相册会触发
// IMAGE_PROCESS_FAILED。
func TestPreparePhotoConvertsWebPDisguisedAsJPEG(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.jpg")
	webp := append([]byte("RIFF\x00\x00\x00\x00WEBP"), []byte("VP8 ")...)
	if err := os.WriteFile(src, webp, 0o600); err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	u := &Uploader{FFmpeg: "ffmpeg", WorkDir: t.TempDir(), Run: recordingRunner(t, &calls)}
	got, asDoc, err := u.PreparePhoto(context.Background(), src)
	if err != nil || asDoc || got == src {
		t.Fatalf("%s %v %v", got, asDoc, err)
	}
	if len(calls) != 1 || !strings.Contains(joinCall(calls[0]), "-frames:v") {
		t.Fatalf("webp was not converted: %v", calls)
	}
}

func TestPreparePhotoExtremeAspectRatioBecomesDocument(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "long.jpg")
	writeTestJPEG(t, src, 100, 3000)
	u := &Uploader{WorkDir: t.TempDir(), Run: func(context.Context, string, ...string) ([]byte, []byte, error) {
		t.Fatal("ffmpeg should not run when the aspect ratio cannot be fixed by scaling")
		return nil, nil, nil
	}}
	got, asDoc, err := u.PreparePhoto(context.Background(), src)
	if err != nil || !asDoc || got != src {
		t.Fatalf("%s %v %v", got, asDoc, err)
	}
}

func TestPreparePhotoOversizedFallback(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "big.jpg")
	f, err := os.Create(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxPhotoBytes + 1); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	u := &Uploader{FFmpeg: "ffmpeg", WorkDir: t.TempDir(), Run: func(context.Context, string, ...string) ([]byte, []byte, error) {
		return nil, []byte("boom"), errors.New("ffmpeg failed")
	}}
	got, asDoc, err := u.PreparePhoto(context.Background(), src)
	if err != nil || !asDoc || got != src {
		t.Fatalf("%s %v %v", got, asDoc, err)
	}
}

func TestPreparePhotoHEIFConvertFailureDoesNotUseSingleTileFallback(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.heic")
	if err := os.WriteFile(src, heicMagic(), 0o600); err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	u := &Uploader{FFmpeg: "ffmpeg", WorkDir: t.TempDir(), Run: func(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
		calls = append(calls, append([]string{name}, args...))
		return nil, []byte("unsupported tile layout"), errors.New("decoder failed")
	}}
	got, asDoc, err := u.PreparePhoto(context.Background(), src)
	if err != nil || !asDoc || got != src {
		t.Fatalf("%s %v %v", got, asDoc, err)
	}
	if len(calls) != 1 || calls[0][0] != "heif-convert" {
		t.Fatalf("must not silently fall back to a partial image: %v", calls)
	}
}

func TestPreparePhotoDowngradeLogsReason(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(logging.New(&logs, slog.LevelWarn))
	t.Cleanup(func() { slog.SetDefault(previous) })

	dir := t.TempDir()
	src := filepath.Join(dir, "large.jpg")
	f, err := os.Create(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxPhotoBytes + 1); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	u := &Uploader{FFmpeg: "ffmpeg", WorkDir: t.TempDir(), Run: func(context.Context, string, ...string) ([]byte, []byte, error) {
		return nil, []byte("codec not available"), errors.New("decoder failed")
	}}
	got, asDoc, err := u.PreparePhoto(context.Background(), src)
	if err != nil || !asDoc || got != src {
		t.Fatalf("%s %v %v", got, asDoc, err)
	}
	out := logs.String()
	if !strings.Contains(out, "按文件发送") || !strings.Contains(out, "codec not available") {
		t.Fatalf("missing downgrade reason: %q", out)
	}
}

func TestCompressVideoArgs(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "v.mp4")
	if err := os.WriteFile(src, []byte("video"), 0o600); err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	u := &Uploader{FFprobe: "ffprobe", FFmpeg: "ffmpeg", WorkDir: t.TempDir(), Run: recordingRunner(t, &calls)}
	out, err := u.CompressVideo(context.Background(), src, videoTargetBytes)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(out, ".mp4") {
		t.Fatalf("out=%s", out)
	}
	if len(calls) != 2 {
		t.Fatalf("calls=%v", calls)
	}
	probe := joinCall(calls[0])
	if !strings.Contains(probe, "ffprobe") || !strings.Contains(probe, "format=duration") {
		t.Fatalf("probe args: %s", probe)
	}
	ffmpeg := joinCall(calls[1])
	for _, want := range []string{"-y", "-i", "-c:v libx264", "-b:v 41008179", "-c:a aac", "-b:a 96k"} {
		if !strings.Contains(ffmpeg, want) {
			t.Fatalf("ffmpeg args %q missing %q", ffmpeg, want)
		}
	}
}

func TestCompressVideoBitrateFloor(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "v.mp4")
	if err := os.WriteFile(src, []byte("video"), 0o600); err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	u := &Uploader{
		FFprobe: "ffprobe",
		FFmpeg:  "ffmpeg",
		WorkDir: t.TempDir(),
		Run: func(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
			calls = append(calls, append([]string{name}, args...))
			if name == "ffprobe" {
				return []byte("1000000\n"), nil, nil
			}
			return nil, nil, os.WriteFile(args[len(args)-1], []byte("out"), 0o600)
		},
	}
	if _, err := u.CompressVideo(context.Background(), src, videoTargetBytes); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(joinCall(calls[1]), "-b:v 200000") {
		t.Fatalf("calls=%v", calls)
	}
}

func TestCompressVideoBadDuration(t *testing.T) {
	u := &Uploader{FFprobe: "ffprobe", FFmpeg: "ffmpeg", Run: func(context.Context, string, ...string) ([]byte, []byte, error) {
		return []byte("not-a-number\n"), nil, nil
	}}
	if _, err := u.CompressVideo(context.Background(), "v.mp4", videoTargetBytes); err == nil {
		t.Fatal("want error")
	}
}

func TestCompressVideoMissingTools(t *testing.T) {
	u := &Uploader{FFprobe: "ffprobe", FFmpeg: "ffmpeg", Run: func(context.Context, string, ...string) ([]byte, []byte, error) {
		return nil, nil, errors.New("executable file not found")
	}}
	if _, err := u.CompressVideo(context.Background(), "v.mp4", videoTargetBytes); err == nil {
		t.Fatal("want error")
	}
}
