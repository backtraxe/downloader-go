// Package timezone makes log timestamps use UTC+8 by default.
//
// Go formats slog timestamps in time.Local. Termux usually has TZ unset, so
// logs come out in UTC (14:39Z instead of 22:39+08:00). We set a fixed +08:00
// zone at startup. DOWNLOADER_TZ overrides it (IANA name, or "Local" to keep
// the system setting).
package timezone

import (
	"os"
	"time"
)

const defaultOffset = 8 * 60 * 60

// Configure sets time.Local. It is called once at startup.
func Configure(getenv func(string) string) {
	name := getenv("DOWNLOADER_TZ")
	if name == "" {
		time.Local = time.FixedZone("CST", defaultOffset)
		return
	}
	if name == "Local" || name == "local" {
		return
	}
	if loc, err := time.LoadLocation(name); err == nil {
		time.Local = loc
		return
	}
	time.Local = time.FixedZone("CST", defaultOffset)
}

// ConfigureDefault is the production entry point.
func ConfigureDefault() {
	Configure(os.Getenv)
}
