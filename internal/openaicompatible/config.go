package openaicompatible

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	// DefaultHTTPTimeout bounds one complete HTTP request, including reading
	// the response body.
	DefaultHTTPTimeout = 30 * time.Second

	// MaxCAFileBytes is the largest accepted CA file.
	MaxCAFileBytes = 1 << 20

	// MaxResponseBytes is the largest accepted response body.
	MaxResponseBytes = 4 << 20
)

// Config configures a Client.
//
// Endpoint is the full Chat Completions URL, for example
// "https://api.example.com/v1/chat/completions". It must be absolute HTTPS
// with an explicit host and path. The client never appends a path.
//
// APIKeyEnv names exactly one environment variable. The client reads that
// variable once in New and never consults OPENAI_API_KEY, OPENAI_BASE_URL, or
// any other ambient provider configuration.
//
// CAFile optionally names a PEM file with additional trust roots. The roots
// are added to the system pool; TLS verification is never disabled.
//
// HTTPTimeout bounds one complete request. Zero selects DefaultHTTPTimeout.
type Config struct {
	Endpoint    string
	APIKeyEnv   string
	CAFile      string
	HTTPTimeout time.Duration
}

// Client is an immutable, concurrency-safe Chat Completions client.
type Client struct {
	endpoint   *url.URL
	apiKey     string
	httpClient *http.Client
}

// New validates cfg and returns a Client. It performs no network I/O.
func New(cfg Config) (*Client, error) {
	endpoint, err := validateEndpoint(cfg.Endpoint)
	if err != nil {
		return nil, err
	}

	timeout := cfg.HTTPTimeout
	if timeout < 0 {
		return nil, &ConfigError{Field: "HTTPTimeout", Message: "must not be negative"}
	}
	if timeout == 0 {
		timeout = DefaultHTTPTimeout
	}

	apiKey, err := readAPIKey(cfg.APIKeyEnv)
	if err != nil {
		return nil, err
	}

	tlsConfig, err := tlsConfigFor(cfg.CAFile)
	if err != nil {
		return nil, err
	}

	transport := &http.Transport{
		// A nil Proxy disables HTTP_PROXY, HTTPS_PROXY, and NO_PROXY lookup.
		Proxy: nil,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:      true,
		TLSClientConfig:        tlsConfig,
		TLSHandshakeTimeout:    10 * time.Second,
		ExpectContinueTimeout:  time.Second,
		MaxIdleConns:           4,
		MaxIdleConnsPerHost:    2,
		IdleConnTimeout:        30 * time.Second,
		MaxResponseHeaderBytes: 1 << 20,
	}

	httpClient := &http.Client{
		Transport: transport,
		Timeout:   timeout,
		// Never follow a redirect. A 3xx response is returned to Complete,
		// which rejects it as a non-2xx status. This prevents the
		// Authorization header from being replayed to another host.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	return &Client{
		endpoint:   endpoint,
		apiKey:     apiKey,
		httpClient: httpClient,
	}, nil
}

func validateEndpoint(raw string) (*url.URL, error) {
	if raw == "" {
		return nil, &ConfigError{Field: "Endpoint", Message: "must not be empty"}
	}
	if strings.TrimSpace(raw) != raw {
		return nil, &ConfigError{Field: "Endpoint", Message: "must not have leading or trailing whitespace"}
	}
	for _, r := range raw {
		if r < 0x20 || r == 0x7f {
			return nil, &ConfigError{Field: "Endpoint", Message: "must not contain control characters"}
		}
	}
	if strings.Contains(raw, "#") {
		return nil, &ConfigError{Field: "Endpoint", Message: "must not include a fragment"}
	}

	u, err := url.Parse(raw)
	if err != nil {
		return nil, &ConfigError{Field: "Endpoint", Message: "must be a valid absolute URL"}
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return nil, &ConfigError{Field: "Endpoint", Message: "must use https"}
	}
	if u.Opaque != "" {
		return nil, &ConfigError{Field: "Endpoint", Message: "must be a hierarchical URL"}
	}
	if u.Host == "" || u.Hostname() == "" {
		return nil, &ConfigError{Field: "Endpoint", Message: "must include a host"}
	}
	if u.User != nil {
		return nil, &ConfigError{Field: "Endpoint", Message: "must not include user information"}
	}
	if u.Fragment != "" || u.RawFragment != "" {
		return nil, &ConfigError{Field: "Endpoint", Message: "must not include a fragment"}
	}
	if u.Path == "" || u.Path == "/" {
		return nil, &ConfigError{Field: "Endpoint", Message: "must include a chat completions path"}
	}
	if !strings.HasPrefix(u.Path, "/") {
		return nil, &ConfigError{Field: "Endpoint", Message: "path must be absolute"}
	}
	return u, nil
}

func readAPIKey(name string) (string, error) {
	if name == "" {
		return "", &ConfigError{Field: "APIKeyEnv", Message: "must name an environment variable"}
	}
	if !validEnvName(name) {
		return "", &ConfigError{Field: "APIKeyEnv", Message: "must be a valid environment variable name"}
	}
	value, ok := os.LookupEnv(name)
	if !ok {
		return "", &ConfigError{Field: "APIKeyEnv", Message: "environment variable is not set"}
	}
	if value == "" {
		return "", &ConfigError{Field: "APIKeyEnv", Message: "environment variable is empty"}
	}
	for _, r := range value {
		if r <= 0x20 || r == 0x7f {
			return "", &ConfigError{Field: "APIKeyEnv", Message: "environment variable contains whitespace or control characters"}
		}
	}
	return value, nil
}

func validEnvName(name string) bool {
	for i, r := range name {
		switch {
		case r == '_':
		case r >= 'A' && r <= 'Z':
		case r >= 'a' && r <= 'z':
		case i > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return name != ""
}

func tlsConfigFor(caFile string) (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if caFile == "" {
		return cfg, nil
	}

	info, err := os.Stat(caFile)
	if err != nil {
		return nil, &ConfigError{Field: "CAFile", Message: "cannot stat file"}
	}
	if !info.Mode().IsRegular() {
		return nil, &ConfigError{Field: "CAFile", Message: "must be a regular file"}
	}
	if info.Size() <= 0 {
		return nil, &ConfigError{Field: "CAFile", Message: "must not be empty"}
	}
	if info.Size() > MaxCAFileBytes {
		return nil, &ConfigError{Field: "CAFile", Message: "must not exceed 1 MiB"}
	}

	f, err := os.Open(caFile)
	if err != nil {
		return nil, &ConfigError{Field: "CAFile", Message: "cannot open file"}
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, MaxCAFileBytes+1))
	if err != nil {
		return nil, &ConfigError{Field: "CAFile", Message: "cannot read file"}
	}
	if len(data) > MaxCAFileBytes {
		return nil, &ConfigError{Field: "CAFile", Message: "must not exceed 1 MiB"}
	}

	certs, err := parseCertPEM(data)
	if err != nil {
		return nil, &ConfigError{Field: "CAFile", Message: err.Error()}
	}

	pool, err := x509.SystemCertPool()
	if err != nil {
		return nil, &ConfigError{Field: "CAFile", Message: "cannot load system certificate pool: " + err.Error()}
	}
	if pool == nil {
		return nil, &ConfigError{Field: "CAFile", Message: "system certificate pool is unavailable"}
	}
	for _, cert := range certs {
		pool.AddCert(cert)
	}
	cfg.RootCAs = pool
	return cfg, nil
}

func parseCertPEM(data []byte) ([]*x509.Certificate, error) {
	var certs []*x509.Certificate
	rest := data
	for {
		idx := bytes.Index(rest, []byte("-----BEGIN"))
		if idx < 0 {
			break
		}
		if len(bytes.TrimSpace(rest[:idx])) != 0 {
			return nil, fmt.Errorf("contains data outside PEM blocks")
		}
		block, next := pem.Decode(rest[idx:])
		if block == nil {
			return nil, fmt.Errorf("contains an invalid PEM block")
		}
		if block.Type != "CERTIFICATE" {
			return nil, fmt.Errorf("must contain only certificate PEM blocks")
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("contains an invalid certificate")
		}
		certs = append(certs, cert)
		rest = next
	}
	if len(certs) == 0 {
		return nil, fmt.Errorf("must contain at least one PEM certificate")
	}
	if len(bytes.TrimSpace(rest)) != 0 {
		return nil, fmt.Errorf("contains trailing data after PEM certificates")
	}
	return certs, nil
}
