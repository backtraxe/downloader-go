package main

import (
	"bytes"
	"strings"
	"testing"
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
