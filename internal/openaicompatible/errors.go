package openaicompatible

import (
	"fmt"
	"strings"
)

// ErrorKind classifies a client failure without exposing request or response
// payloads.
type ErrorKind string

const (
	// ErrorTransport means the HTTP request could not be completed. The
	// underlying cause is available through errors.Unwrap.
	ErrorTransport ErrorKind = "transport"
	// ErrorStatus means the server returned a non-2xx status.
	ErrorStatus ErrorKind = "status"
	// ErrorProtocol means the server response did not match the Chat
	// Completions contract, or the caller supplied an invalid request.
	ErrorProtocol ErrorKind = "protocol"
)

// Error is a bounded, classified client error.
//
// Error deliberately does not include the endpoint, request body, response
// body, or API key. The underlying transport error is retained for
// errors.Is/errors.As but is not rendered by Error.
type Error struct {
	Kind       ErrorKind
	StatusCode int
	Message    string
	Err        error
}

// Error implements error.
func (e *Error) Error() string {
	if e == nil {
		return "openaicompatible: <nil>"
	}
	var b strings.Builder
	b.WriteString("openaicompatible: ")
	if e.Kind != "" {
		b.WriteString(string(e.Kind))
		b.WriteString(": ")
	}
	if e.StatusCode != 0 {
		fmt.Fprintf(&b, "status %d: ", e.StatusCode)
	}
	b.WriteString(e.Message)
	return b.String()
}

// Unwrap returns the underlying transport error, if any.
func (e *Error) Unwrap() error { return e.Err }

// ConfigError reports an invalid Config value. It never includes the value of
// a secret environment variable.
type ConfigError struct {
	Field   string
	Message string
}

// Error implements error.
func (e *ConfigError) Error() string {
	if e == nil {
		return "openaicompatible: invalid configuration"
	}
	return "openaicompatible: invalid " + e.Field + ": " + e.Message
}

func protocolError(format string, args ...any) *Error {
	return &Error{Kind: ErrorProtocol, Message: fmt.Sprintf(format, args...)}
}

func statusError(code int) *Error {
	return &Error{
		Kind:       ErrorStatus,
		StatusCode: code,
		Message:    fmt.Sprintf("unexpected HTTP status %d", code),
	}
}
