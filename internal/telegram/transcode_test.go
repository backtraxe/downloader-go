package telegram

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func TestPreparePhotoHEICUsesFFmpeg(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.heic")
	if err := os.WriteFile(src, []byte("heic"), 0o600); err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	u := &Uploader{FFmpeg: "ffmpeg", WorkDir: t.TempDir(), Run: recordingRunner(t, &calls)}
	got, asDoc, err := u.PreparePhoto(context.Background(), src)
	if err != nil || asDoc || !strings.HasSuffix(got, ".jpg") {
		t.Fatalf("%s %v %v", got, asDoc, err)
	}
	if len(calls) != 1 || !strings.Contains(joinCall(calls[0]), "-frames:v") {
		t.Fatalf("%v", calls)
	}
	if st, err := os.Stat(got); err != nil || st.Size() == 0 {
		t.Fatalf("output missing: %v", err)
	}
}

func TestPreparePhotoNormalUnchanged(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.jpg")
	if err := os.WriteFile(src, []byte("jpeg"), 0o600); err != nil {
		t.Fatal(err)
	}
	u := &Uploader{WorkDir: t.TempDir(), Run: func(context.Context, string, ...string) ([]byte, []byte, error) {
		t.Fatal("ffmpeg should not run for a small photo")
		return nil, nil, nil
	}}
	got, asDoc, err := u.PreparePhoto(context.Background(), src)
	if err != nil || asDoc || got != src {
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
