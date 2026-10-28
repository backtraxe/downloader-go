package termuxenv

import (
	"errors"
	"os"
	"testing"
	"time"
)

type fakeInfo struct {
	dir bool
}

func (f fakeInfo) Name() string       { return "fake" }
func (f fakeInfo) Size() int64        { return 0 }
func (f fakeInfo) Mode() os.FileMode  { return 0 }
func (f fakeInfo) ModTime() time.Time { return time.Time{} }
func (f fakeInfo) IsDir() bool        { return f.dir }
func (f fakeInfo) Sys() any           { return nil }

func TestConfigureTermuxSetsCertPaths(t *testing.T) {
	vars := map[string]string{"PREFIX": "/data/data/com.termux/files/usr"}
	stat := func(path string) (os.FileInfo, error) {
		switch path {
		case "/data/data/com.termux/files/usr/etc/tls/cert.pem":
			return fakeInfo{}, nil
		case "/data/data/com.termux/files/usr/etc/tls/certs":
			return fakeInfo{dir: true}, nil
		}
		return nil, errors.New("missing")
	}
	Configure(func(k string) string { return vars[k] }, func(k, v string) error {
		vars[k] = v
		return nil
	}, stat)

	if vars["SSL_CERT_FILE"] != "/data/data/com.termux/files/usr/etc/tls/cert.pem" {
		t.Fatalf("SSL_CERT_FILE=%q", vars["SSL_CERT_FILE"])
	}
	if vars["SSL_CERT_DIR"] != "/data/data/com.termux/files/usr/etc/tls/certs" {
		t.Fatalf("SSL_CERT_DIR=%q", vars["SSL_CERT_DIR"])
	}
}

func TestConfigureNonTermuxNoop(t *testing.T) {
	vars := map[string]string{"PREFIX": "/usr"}
	Configure(func(k string) string { return vars[k] }, func(k, v string) error {
		vars[k] = v
		return nil
	}, func(string) (os.FileInfo, error) { return fakeInfo{}, nil })

	if _, ok := vars["SSL_CERT_FILE"]; ok {
		t.Fatal("non-Termux must not set SSL_CERT_FILE")
	}
}

func TestConfigureMissingFilesNoop(t *testing.T) {
	vars := map[string]string{"PREFIX": "/data/data/com.termux/files/usr"}
	Configure(func(k string) string { return vars[k] }, func(k, v string) error {
		vars[k] = v
		return nil
	}, func(string) (os.FileInfo, error) { return nil, errors.New("missing") })

	if _, ok := vars["SSL_CERT_FILE"]; ok {
		t.Fatal("missing cert file must not be set")
	}
}
