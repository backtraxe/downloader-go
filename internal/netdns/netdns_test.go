package netdns

import (
	"net"
	"reflect"
	"testing"
)

func env(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestServersFromEnv(t *testing.T) {
	got := ServersFromEnv(env(nil))
	if !reflect.DeepEqual(got, DefaultServers) {
		t.Fatalf("default: %v", got)
	}

	got = ServersFromEnv(env(map[string]string{"DOWNLOADER_DNS": "9.9.9.9, 1.0.0.1:5353"}))
	want := []string{"9.9.9.9:53", "1.0.0.1:5353"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("override: %v", got)
	}

	if got := ServersFromEnv(env(map[string]string{"DOWNLOADER_DNS": "off"})); got != nil {
		t.Fatalf("off: %v", got)
	}
}

func TestNewNilReturnsDefault(t *testing.T) {
	if got := New(nil); got != net.DefaultResolver {
		t.Fatal("nil server list should keep the default resolver")
	}
}

func TestInstallOnlyOnTermux(t *testing.T) {
	old := net.DefaultResolver
	defer func() { net.DefaultResolver = old }()

	net.DefaultResolver = old
	Install(env(map[string]string{"PREFIX": "/usr"}))
	if net.DefaultResolver != old {
		t.Fatal("non-Termux install must be a no-op")
	}

	Install(env(map[string]string{"PREFIX": "/data/data/com.termux/files/usr"}))
	if net.DefaultResolver == old {
		t.Fatal("Termux install should replace the resolver")
	}
	if !net.DefaultResolver.PreferGo {
		t.Fatal("resolver must prefer Go")
	}
}

func TestInstallOffKeepsDefault(t *testing.T) {
	old := net.DefaultResolver
	defer func() { net.DefaultResolver = old }()

	net.DefaultResolver = old
	Install(env(map[string]string{
		"PREFIX":         "/data/data/com.termux/files/usr",
		"DOWNLOADER_DNS": "off",
	}))
	if net.DefaultResolver != old {
		t.Fatal("DOWNLOADER_DNS=off should keep the system resolver")
	}
}
