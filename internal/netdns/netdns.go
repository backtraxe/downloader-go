// Package netdns installs a DNS resolver for Termux.
//
// Termux has no /etc/resolv.conf: Android keeps DNS configuration in netd,
// and the pure-Go resolver (CGO_ENABLED=0) falls back to 127.0.0.1:53 /
// [::1]:53 when the file is missing. Nothing listens there, so every lookup
// fails with "connection refused" even though curl works.
//
// On Termux we replace net.DefaultResolver with a resolver that dials
// well-known public DNS servers directly. DOWNLOADER_DNS overrides the list
// (comma-separated host:port); DOWNLOADER_DNS=off keeps the system resolver.
package netdns

import (
	"context"
	"net"
	"os"
	"strings"
	"time"
)

// DefaultServers is the fallback list used on Termux.
var DefaultServers = []string{
	"1.1.1.1:53",
	"223.5.5.5:53",
	"8.8.8.8:53",
}

// ServersFromEnv returns the DNS server list from DOWNLOADER_DNS, or the
// default list. `off` (case-insensitive) returns nil.
func ServersFromEnv(getenv func(string) string) []string {
	raw := strings.TrimSpace(getenv("DOWNLOADER_DNS"))
	if strings.EqualFold(raw, "off") {
		return nil
	}
	if raw == "" {
		return append([]string(nil), DefaultServers...)
	}
	var servers []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if !strings.Contains(part, ":") {
			part += ":53"
		}
		servers = append(servers, part)
	}
	if len(servers) == 0 {
		return append([]string(nil), DefaultServers...)
	}
	return servers
}

// New returns a resolver that sends all queries to the given servers in order.
func New(servers []string) *net.Resolver {
	if len(servers) == 0 {
		return net.DefaultResolver
	}
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var lastErr error
			for _, server := range servers {
				d := net.Dialer{Timeout: 3 * time.Second}
				conn, err := d.DialContext(ctx, network, server)
				if err == nil {
					return conn, nil
				}
				lastErr = err
			}
			return nil, lastErr
		},
	}
}

// Install replaces net.DefaultResolver when running on Termux. It is a no-op
// elsewhere, or when DOWNLOADER_DNS=off is set.
func Install(getenv func(string) string) {
	if !isTermux(getenv("PREFIX")) {
		return
	}
	servers := ServersFromEnv(getenv)
	if len(servers) == 0 {
		return
	}
	net.DefaultResolver = New(servers)
}

func isTermux(prefix string) bool {
	return strings.Contains(prefix, "com.termux")
}

// InstallDefault is the production entry point.
func InstallDefault() {
	Install(os.Getenv)
}
