package openaicompatible

import (
	"bytes"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewValidatesEndpoint(t *testing.T) {
	t.Setenv("OPENAI_COMPATIBLE_TEST_KEY", testAPIKey)

	cases := []struct {
		name     string
		endpoint string
		ok       bool
	}{
		{"valid", "https://example.com/v1/chat/completions", true},
		{"valid uppercase scheme", "HTTPS://example.com/v1/chat/completions", true},
		{"valid query", "https://example.com/v1/chat/completions?api-version=1", true},
		{"http", "http://example.com/v1/chat/completions", false},
		{"relative", "/v1/chat/completions", false},
		{"no host", "https:///v1/chat/completions", false},
		{"no path", "https://example.com", false},
		{"root path", "https://example.com/", false},
		{"userinfo", "https://user:pass@example.com/v1/chat/completions", false},
		{"fragment", "https://example.com/v1/chat/completions#frag", false},
		{"empty", "", false},
		{"whitespace", " https://example.com/v1/chat/completions", false},
		{"control", "https://example.com/v1/chat\ncompletions", false},
		{"invalid port", "https://example.com:bad/v1/chat/completions", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(Config{Endpoint: tc.endpoint, APIKeyEnv: "OPENAI_COMPATIBLE_TEST_KEY"})
			if tc.ok && err != nil {
				t.Fatalf("New: %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestNewReadsOnlyNamedAPIKeyEnv(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "ambient-secret")
	t.Setenv("OPENAI_BASE_URL", "https://ambient.invalid/v1")

	_, err := New(Config{
		Endpoint:  "https://example.com/v1/chat/completions",
		APIKeyEnv: "OPENAI_COMPATIBLE_DEFINITELY_MISSING",
	})
	if err == nil {
		t.Fatal("expected an error for a missing named variable")
	}
	if strings.Contains(err.Error(), "ambient-secret") {
		t.Fatal("error leaked an ambient secret")
	}

	t.Setenv("OPENAI_COMPATIBLE_TEST_KEY", testAPIKey)
	if _, err := New(Config{
		Endpoint:  "https://example.com/v1/chat/completions",
		APIKeyEnv: "OPENAI_COMPATIBLE_TEST_KEY",
	}); err != nil {
		t.Fatalf("New with the named variable: %v", err)
	}
}

func TestNewRejectsInvalidAPIKeyEnv(t *testing.T) {
	cases := []struct {
		name    string
		envName string
		value   string
		set     bool
	}{
		{"empty name", "", "", false},
		{"invalid name", "BAD-NAME", "x", true},
		{"missing", "OPENAI_COMPATIBLE_DEFINITELY_MISSING", "", false},
		{"empty value", "OPENAI_COMPATIBLE_EMPTY_KEY", "", true},
		{"control value", "OPENAI_COMPATIBLE_CONTROL_KEY", "abc\n", true},
		{"space value", "OPENAI_COMPATIBLE_SPACE_KEY", "abc def", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.set {
				t.Setenv(tc.envName, tc.value)
			}
			_, err := New(Config{
				Endpoint:  "https://example.com/v1/chat/completions",
				APIKeyEnv: tc.envName,
			})
			if err == nil {
				t.Fatal("expected an error")
			}
			if tc.value != "" && strings.Contains(err.Error(), tc.value) {
				t.Fatal("error leaked the environment value")
			}
		})
	}
}

func TestNewCAFile(t *testing.T) {
	t.Setenv("OPENAI_COMPATIBLE_TEST_KEY", testAPIKey)

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(srv.Close)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})

	write := func(t *testing.T, data []byte) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "ca.pem")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("write CA file: %v", err)
		}
		return path
	}

	cases := []struct {
		name string
		path func(t *testing.T) string
		ok   bool
	}{
		{"valid", func(t *testing.T) string { return write(t, certPEM) }, true},
		{"multiple certificates", func(t *testing.T) string {
			return write(t, append(append([]byte{}, certPEM...), certPEM...))
		}, true},
		{"missing", func(t *testing.T) string {
			return filepath.Join(t.TempDir(), "missing.pem")
		}, false},
		{"directory", func(t *testing.T) string { return t.TempDir() }, false},
		{"empty", func(t *testing.T) string { return write(t, nil) }, false},
		{"oversized", func(t *testing.T) string {
			return write(t, bytes.Repeat([]byte("a"), MaxCAFileBytes+1))
		}, false},
		{"not pem", func(t *testing.T) string { return write(t, []byte("not a certificate")) }, false},
		{"trailing junk", func(t *testing.T) string {
			return write(t, append(append([]byte{}, certPEM...), []byte("junk")...))
		}, false},
		{"private key block", func(t *testing.T) string {
			return write(t, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("x")}))
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(Config{
				Endpoint:  "https://example.com/v1/chat/completions",
				APIKeyEnv: "OPENAI_COMPATIBLE_TEST_KEY",
				CAFile:    tc.path(t),
			})
			if tc.ok && err != nil {
				t.Fatalf("New: %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatal("expected an error")
			}
		})
	}

	t.Run("unreadable", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("running as root")
		}
		path := write(t, certPEM)
		if err := os.Chmod(path, 0o000); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		if _, err := New(Config{
			Endpoint:  "https://example.com/v1/chat/completions",
			APIKeyEnv: "OPENAI_COMPATIBLE_TEST_KEY",
			CAFile:    path,
		}); err == nil {
			t.Fatal("expected an error")
		}
	})
}

func TestNewDoesNotMakeHTTP(t *testing.T) {
	var hits int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
	}))
	t.Cleanup(srv.Close)

	t.Setenv("OPENAI_COMPATIBLE_TEST_KEY", testAPIKey)
	if _, err := New(Config{
		Endpoint:  srv.URL + "/v1/chat/completions",
		APIKeyEnv: "OPENAI_COMPATIBLE_TEST_KEY",
		CAFile:    writeServerCert(t, srv),
	}); err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Fatalf("server hits = %d, want 0", got)
	}
}

func TestNewRejectsNegativeTimeout(t *testing.T) {
	t.Setenv("OPENAI_COMPATIBLE_TEST_KEY", testAPIKey)
	_, err := New(Config{
		Endpoint:    "https://example.com/v1/chat/completions",
		APIKeyEnv:   "OPENAI_COMPATIBLE_TEST_KEY",
		HTTPTimeout: -time.Second,
	})
	if err == nil {
		t.Fatal("expected an error")
	}
}
