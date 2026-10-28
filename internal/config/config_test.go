package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadWritesTemplateWhenMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	_, err := Load(LoadOptions{Path: path, LegacyPath: filepath.Join(dir, "telegram.json")})
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("err=%v", err)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("template missing: %v", statErr)
	}
}

func TestLoadLegacyTelegram(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "telegram.json")
	os.WriteFile(legacy, []byte(`{"bot_token":"t","chat_id":42,"allowed_users":[7]}`), 0o600)
	cfg, err := Load(LoadOptions{Path: filepath.Join(dir, "none.json"), LegacyPath: legacy})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Telegram.BotToken != "t" || cfg.Telegram.ChatID.String() != "42" {
		t.Fatalf("%+v", cfg)
	}
}

func TestChatIDBothForms(t *testing.T) {
	for _, raw := range []string{`123`, `"@channel"`} {
		var c ChatID
		if err := json.Unmarshal([]byte(raw), &c); err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if c.String() == "" {
			t.Fatalf("empty for %s", raw)
		}
	}
}

func TestTTLDefaultAndZero(t *testing.T) {
	if got := (Telegram{}).TTL(); got != 300 {
		t.Fatalf("default=%d", got)
	}
	zero := 0
	if got := (Telegram{ProcessMessageTTLSeconds: &zero}).TTL(); got != 0 {
		t.Fatalf("zero=%d", got)
	}
}

func TestChatIDUnmarshalValues(t *testing.T) {
	cases := map[string]string{
		`123`:        "123",
		`-100123456`: "-100123456",
		`"@channel"`: "@channel",
		`"123"`:      "123",
		`null`:       "",
	}
	for raw, want := range cases {
		var c ChatID
		if err := json.Unmarshal([]byte(raw), &c); err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if got := c.String(); got != want {
			t.Fatalf("%s: got %q want %q", raw, got, want)
		}
	}
}

func TestChatIDRejectsBool(t *testing.T) {
	var c ChatID
	if err := json.Unmarshal([]byte(`true`), &c); err == nil {
		t.Fatal("expected error for bool")
	}
}

func TestChatIDMarshalNumberWhenInteger(t *testing.T) {
	cases := []struct{ raw, want string }{
		{`123`, `123`},
		{`"@channel"`, `"@channel"`},
		{`"123"`, `123`},
		{`"-1001"`, `-1001`},
		{`"007"`, `7`},
		{`"+123"`, `123`},
		{`"abc"`, `"abc"`},
	}
	for _, tc := range cases {
		var c ChatID
		if err := json.Unmarshal([]byte(tc.raw), &c); err != nil {
			t.Fatalf("%s: %v", tc.raw, err)
		}
		got, err := json.Marshal(c)
		if err != nil {
			t.Fatalf("%s: %v", tc.raw, err)
		}
		if string(got) != tc.want {
			t.Fatalf("%s: got %s want %s", tc.raw, got, tc.want)
		}
	}
}

func TestLoadFromConfigPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	content := `{"telegram":{"bot_token":"tok","chat_id":"@channel","allowed_users":[1,2],"process_message_ttl_seconds":0}}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(LoadOptions{Path: path, LegacyPath: filepath.Join(dir, "telegram.json")})
	if err != nil {
		t.Fatal(err)
	}
	tg := cfg.Telegram
	if tg.BotToken != "tok" || tg.ChatID.String() != "@channel" {
		t.Fatalf("%+v", tg)
	}
	if len(tg.AllowedUsers) != 2 || tg.AllowedUsers[0] != 1 || tg.AllowedUsers[1] != 2 {
		t.Fatalf("allowed=%v", tg.AllowedUsers)
	}
	if got := tg.TTL(); got != 0 {
		t.Fatalf("ttl=%d want 0", got)
	}
}

func TestLoadMissingTTLUsesDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"telegram":{"bot_token":"tok"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(LoadOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Telegram.TTL(); got != DefaultProcessMessageTTL {
		t.Fatalf("ttl=%d want %d", got, DefaultProcessMessageTTL)
	}
}

func TestLoadInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(LoadOptions{Path: path}); err == nil {
		t.Fatal("expected error for invalid json")
	}
}

func TestWriteTemplateDoesNotOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	const existing = `{"telegram":{"bot_token":"keep"}}`
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteTemplate(path); err != nil {
		t.Fatalf("WriteTemplate: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != existing {
		t.Fatalf("overwritten: %s", data)
	}
}

// TestWriteTemplateCreatesParentDir 验证 --data-dir 指向尚不存在的目录时，
// 模板写入会一并创建父目录。
func TestWriteTemplateCreatesParentDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "newdata", "nested", "config.json")
	if err := WriteTemplate(path); err != nil {
		t.Fatalf("WriteTemplate: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("template missing: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("perm=%o want 600", perm)
	}
	if _, err := Load(LoadOptions{Path: path}); err != nil {
		t.Fatalf("template not loadable: %v", err)
	}
}

func TestTemplateIsLoadable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := WriteTemplate(path); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(LoadOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Telegram.TTL(); got != DefaultProcessMessageTTL {
		t.Fatalf("ttl=%d want %d", got, DefaultProcessMessageTTL)
	}
}
