package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/backtraxe/downloader-go/internal/result"
)

func TestRunVersion(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{"version"}, strings.NewReader(""), &out, &errOut)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "downloader dev") {
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
	if code := runWith([]string{"cookie"}, strings.NewReader(""), &out, &out, dependencies{}); code != 2 {
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
