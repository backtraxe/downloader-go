package logging

import (
	"bytes"
	"context"
	"log/slog"
	"regexp"
	"strings"
	"testing"
)

func TestRequestIDFormatAndPrefix(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf, slog.LevelInfo)
	ctx, id := WithRequest(context.Background())
	if !regexp.MustCompile(`^r[a-z0-9]{4}$`).MatchString(id) {
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
	pattern := regexp.MustCompile(`^r[a-z0-9]{4}$`)
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
