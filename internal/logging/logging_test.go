package logging

import (
	"bytes"
	"context"
	"log/slog"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestRequestIDFormatAndPrefix(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf, slog.LevelInfo)
	ctx, id := WithRequest(context.Background())
	if !regexp.MustCompile(`^r[A-Za-z0-9]{7}$`).MatchString(id) {
		t.Fatalf("id=%q", id)
	}
	logger.InfoContext(ctx, "hello")
	if !bytes.Contains(buf.Bytes(), []byte("["+id+"] hello")) {
		t.Fatalf("log=%q", buf.String())
	}
}

func TestNestedScopeReusesID(t *testing.T) {
	ctx, first := WithRequest(context.Background())
	ctx2, second := WithRequest(ctx)
	if first != second {
		t.Fatalf("%s != %s", first, second)
	}
	if RequestID(ctx2) != first {
		t.Fatalf("lost id")
	}
}

func TestRequestIDWithoutScope(t *testing.T) {
	if got := RequestID(context.Background()); got != "" {
		t.Fatalf("id=%q want empty", got)
	}
}

func TestNoPrefixWithoutRequest(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf, slog.LevelInfo)
	logger.InfoContext(context.Background(), "plain")
	if !bytes.Contains(buf.Bytes(), []byte("plain")) {
		t.Fatalf("log=%q", buf.String())
	}
	if strings.Contains(buf.String(), "[r") {
		t.Fatalf("unexpected request prefix: %q", buf.String())
	}
}

func TestNewIDUniqueAndFormatted(t *testing.T) {
	pattern := regexp.MustCompile(`^r[A-Za-z0-9]{7}$`)
	seen := make(map[string]struct{}, 500)
	for range 500 {
		id := NewID()
		if !pattern.MatchString(id) {
			t.Fatalf("id=%q", id)
		}
		if _, ok := seen[id]; ok {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = struct{}{}
	}
}

func TestWithAttrsKeepsPrefix(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf, slog.LevelDebug).With("k", "v")
	ctx, id := WithRequest(context.Background())
	logger.DebugContext(ctx, "attrs")
	out := buf.String()
	if !strings.Contains(out, "["+id+"] attrs") || !strings.Contains(out, "k=v") {
		t.Fatalf("log=%q", out)
	}
}

func TestShortLogFormat(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf, slog.LevelInfo)
	logger.Info("收到链接", "link", "https://example.com/a", "note", "two words")
	line := strings.TrimSuffix(buf.String(), "\n")
	want := `^\d{8} \d{6}[+-]\d{2} INFO 收到链接 link=https://example.com/a note="two words"$`
	if !regexp.MustCompile(want).MatchString(line) {
		t.Fatalf("log=%q does not match %q", line, want)
	}
}

func TestLogTimeUsesConfiguredZone(t *testing.T) {
	var buf bytes.Buffer
	handler := newShortHandler(&buf, slog.LevelInfo)
	record := slog.NewRecord(
		time.Date(2026, 11, 13, 1, 55, 16, 0, time.FixedZone("CST", 8*3600)),
		slog.LevelInfo, "测试", 0,
	)
	if err := handler.Handle(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if got, want := buf.String(), "20261113 015516+08 INFO 测试\n"; got != want {
		t.Fatalf("log=%q want %q", got, want)
	}
}

func TestWithGroupKeepsStructuredField(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf, slog.LevelInfo).WithGroup("download")
	logger.Info("完成", "site", "xiaohongshu")
	if !strings.Contains(buf.String(), " download.site=xiaohongshu\n") {
		t.Fatalf("log=%q", buf.String())
	}
}

func TestLevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf, slog.LevelWarn)
	logger.Info("hidden")
	if buf.Len() != 0 {
		t.Fatalf("unexpected output: %q", buf.String())
	}
	logger.Warn("shown")
	if !strings.Contains(buf.String(), "shown") {
		t.Fatalf("log=%q", buf.String())
	}
}
