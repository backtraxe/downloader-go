package timezone

import (
	"testing"
	"time"
)

func TestConfigureDefaultsToPlusEight(t *testing.T) {
	old := time.Local
	defer func() { time.Local = old }()

	Configure(func(string) string { return "" })
	_, offset := time.Now().Zone()
	if offset != 8*60*60 {
		t.Fatalf("offset=%d", offset)
	}
	if _, err := time.LoadLocation(defaultZone); err == nil {
		if got := time.Local.String(); got != defaultZone {
			t.Fatalf("location=%q, want %q", got, defaultZone)
		}
	}
}

func TestConfigureOverrideUTC(t *testing.T) {
	old := time.Local
	defer func() { time.Local = old }()

	Configure(func(key string) string {
		if key == "DOWNLOADER_TZ" {
			return "UTC"
		}
		return ""
	})
	_, offset := time.Now().Zone()
	if offset != 0 {
		t.Fatalf("offset=%d", offset)
	}
}

func TestConfigureLocalKeepsSystem(t *testing.T) {
	old := time.Local
	defer func() { time.Local = old }()

	time.Local = old
	Configure(func(key string) string {
		if key == "DOWNLOADER_TZ" {
			return "Local"
		}
		return ""
	})
	if time.Local != old {
		t.Fatal("Local should keep the current time.Local")
	}
}
