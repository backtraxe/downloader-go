// Package termuxenv configures process environment variables that Go cannot
// discover on Termux by itself.
//
// Termux keeps its CA bundle at $PREFIX/etc/tls/cert.pem, not in the standard
// Linux paths. A CGO-free Go binary therefore gets an empty root pool and
// fails every HTTPS request with "certificate signed by unknown authority".
// Pointing SSL_CERT_FILE/SSL_CERT_DIR at the Termux locations fixes both the
// standard-library TLS stack and utls.
package termuxenv

import (
	"os"
	"path/filepath"
	"strings"
)

// Configure sets SSL_CERT_FILE/SSL_CERT_DIR when running on Termux and the
// files exist. It is a no-op elsewhere.
func Configure(getenv func(string) string, setenv func(string, string) error, stat func(string) (os.FileInfo, error)) {
	prefix := getenv("PREFIX")
	if !strings.Contains(prefix, "com.termux") {
		return
	}
	certFile := filepath.Join(prefix, "etc", "tls", "cert.pem")
	if _, err := stat(certFile); err == nil {
		_ = setenv("SSL_CERT_FILE", certFile)
	}
	certDir := filepath.Join(prefix, "etc", "tls", "certs")
	if info, err := stat(certDir); err == nil && info.IsDir() {
		_ = setenv("SSL_CERT_DIR", certDir)
	}
}

// ConfigureDefault is the production entry point.
func ConfigureDefault() {
	Configure(os.Getenv, os.Setenv, os.Stat)
}
