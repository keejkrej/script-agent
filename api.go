// Package scriptagent is a shared, polyglot script execution coordinator.
//
// It is an execution library, not an LLM agent loop. One Agent owns one
// explicit workspace root and dispatches a Request to one of three language
// engines:
//
//   - Python: the QuickPy WASM guest (MicroPython), with the workspace exposed
//     through the runtime-neutral capability bridge.
//   - JavaScript: the pi-go codemode QuickJS sandbox, with the same capability
//     bridge and a frozen `fs` facade.
//   - Bash: a host process launched with a controlled environment.
//
// Every execution is isolated: Python and JavaScript get a fresh VM and fresh
// globals, and Bash gets a fresh process. The Agent never inherits the host
// environment, never reads an ambient working directory, and never grants a
// capability the caller did not ask for.
//
// Run always returns a Result. A script failure, timeout, cancellation, limit,
// or denial is a Result status, not a Go error. New returns an error only for
// an invalid configuration.
package scriptagent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Language is a canonical script language name.
//
// The canonical values are "python", "javascript", and "bash". ParseLanguage
// accepts the common aliases ("py", "js", ...) and returns the canonical value.
type Language string

const (
	LanguagePython     Language = "python"
	LanguageJavaScript Language = "javascript"
	LanguageBash       Language = "bash"
)

// ParseLanguage maps a user-supplied language name to a canonical Language.
// It accepts the canonical names and the aliases py, python3, js, node,
// nodejs, sh, and shell. Matching is case-insensitive and ignores surrounding
// whitespace.
func ParseLanguage(raw string) (Language, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "python", "py", "python3", "py3":
		return LanguagePython, nil
	case "javascript", "js", "node", "nodejs":
		return LanguageJavaScript, nil
	case "bash", "sh", "shell":
		return LanguageBash, nil
	default:
		return "", fmt.Errorf("scriptagent: unsupported language %q (want python, javascript, or bash)", raw)
	}
}

// Valid reports whether l is one of the canonical languages.
func (l Language) Valid() bool {
	switch l {
	case LanguagePython, LanguageJavaScript, LanguageBash:
		return true
	default:
		return false
	}
}

// String returns the canonical language name.
func (l Language) String() string { return string(l) }

// Status is the terminal outcome of one execution.
type Status string

const (
	// StatusCompleted means the script or process finished successfully.
	StatusCompleted Status = "completed"
	// StatusFailed means the script or process failed.
	StatusFailed Status = "failed"
	// StatusTimedOut means the execution deadline expired.
	StatusTimedOut Status = "timed_out"
	// StatusCancelled means the caller's context was cancelled.
	StatusCancelled Status = "cancelled"
	// StatusLimitExceeded means an output, source, memory, or capacity limit
	// was reached.
	StatusLimitExceeded Status = "limit_exceeded"
	// StatusStalled means the guest stopped making progress without settling.
	StatusStalled Status = "stalled"
	// StatusDenied means the request asked for a capability the Agent does not
	// grant.
	StatusDenied Status = "denied"
	// StatusUnsupported means the requested language or host facility is not
	// available in this build.
	StatusUnsupported Status = "unsupported"
	// StatusInvalidRequest means the request itself was malformed.
	StatusInvalidRequest Status = "invalid_request"
)

// Output is one ordered piece of script output.
//
// Kind is "text" or "image". Stream is "stdout" or "stderr". Level is set for
// console diagnostics (for example "warn"). Text is set for text output. MIME
// and Data are set for image output; Data is raw bytes and is base64-encoded
// by encoding/json.
type Output struct {
	Kind   string `json:"kind"`
	Stream string `json:"stream"`
	Level  string `json:"level,omitempty"`
	Text   string `json:"text,omitempty"`
	MIME   string `json:"mime,omitempty"`
	Data   []byte `json:"data,omitempty"`
}

// Error is a structured execution failure.
//
// Name is a stable, presentable class such as "ScriptError", "Timeout",
// "Cancelled", "LimitExceeded", "Stalled", "Denied", "Unsupported",
// "InvalidRequest", "EngineError", or "ProcessError". Code is an optional
// machine-readable detail (a guest exception type, a capability code, or a
// process exit class). Stack is optional guest stack or traceback text.
type Error struct {
	Name    string `json:"name"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message"`
	Stack   string `json:"stack,omitempty"`
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Code != "" {
		return e.Name + " (" + e.Code + "): " + e.Message
	}
	return e.Name + ": " + e.Message
}

// Result is the terminal outcome of one execution.
//
// Value is the script's return value when the engine exposes one. It is
// omitted from JSON when the engine produced no value; a present JSON null is
// distinct from an absent value. ExitCode is set only for Bash. Duration is
// the wall-clock time spent in Run, including engine startup on first use.
type Result struct {
	Language Language      `json:"language"`
	Status   Status        `json:"status"`
	Value    any           `json:"-"`
	Output   []Output      `json:"output,omitempty"`
	Error    *Error        `json:"error,omitempty"`
	ExitCode *int          `json:"exitCode,omitempty"`
	Warnings []string      `json:"warnings,omitempty"`
	Duration time.Duration `json:"duration"`

	valueSet bool
}

// SetValue records a present script value, including JSON zero values such as
// 0, false, and "". It is used by engine adapters; callers normally do not
// need it.
func (r *Result) SetValue(v any) {
	r.Value = v
	r.valueSet = true
}

// HasValue reports whether the Result carries a present script value.
func (r Result) HasValue() bool { return r.valueSet || r.Value != nil }

// MarshalJSON implements json.Marshaler. It omits Value when the engine
// produced no value and preserves present zero values (0, false, "") that
// encoding/json's omitempty would otherwise drop. It does not escape HTML
// characters, so guest stack traces and source text stay readable.
func (r Result) MarshalJSON() ([]byte, error) {
	type resultAlias Result
	aux := struct {
		*resultAlias
		Value json.RawMessage `json:"value,omitempty"`
	}{resultAlias: (*resultAlias)(&r)}
	if r.HasValue() {
		raw, err := marshalNoEscape(r.Value)
		if err != nil {
			return nil, fmt.Errorf("scriptagent: marshal result value: %w", err)
		}
		aux.Value = raw
	}
	return marshalNoEscape(aux)
}

func marshalNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// UnmarshalJSON implements json.Unmarshaler so a Result round-trips through
// the CLI JSON contract, including a present null value.
func (r *Result) UnmarshalJSON(data []byte) error {
	type resultAlias Result
	aux := struct {
		*resultAlias
		Value json.RawMessage `json:"value"`
	}{resultAlias: (*resultAlias)(r)}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	if len(aux.Value) > 0 {
		var v any
		if err := json.Unmarshal(aux.Value, &v); err != nil {
			return err
		}
		r.Value = v
		r.valueSet = true
	}
	return nil
}

// Config configures an Agent.
//
// Workspace is required and must be an existing directory. It is resolved to
// an absolute path once, at New, and every capability is confined to it.
//
// AllowWrites is the explicit workspace-write opt-in. The zero value is
// read-only: every mutating capability operation is denied. Set it to true
// only when the caller wants the guest to create, modify, or remove files
// under Workspace.
//
// AllowWrites does not confine a privileged Bash process. Bash runs with the
// host user's OS privileges and can write outside Workspace regardless of this
// field; the Bash result carries a warning that says so.
//
// AllowProcess is the Agent-level process grant. Bash additionally requires
// Request.AllowProcess, so a caller must opt in twice.
//
// Zero numeric limits select the documented defaults. Negative values are
// rejected by New.
type Config struct {
	Workspace string

	AllowWrites  bool
	AllowProcess bool

	MaxOutputBytes int
	MaxSourceBytes int
	Timeout        time.Duration

	MaxPythonMemoryBytes int64
	MaxExecutions        int
}

// Default limits. A zero Config field selects the corresponding default.
const (
	DefaultMaxOutputBytes       = 1 << 20 // 1 MiB
	DefaultMaxSourceBytes       = 256 << 10
	DefaultTimeout              = 30 * time.Second
	DefaultMaxPythonMemoryBytes = 64 << 20 // 64 MiB
	DefaultMaxExecutions        = 4

	// MaxOutputBytesLimit and MaxSourceBytesLimit bound caller-supplied
	// limits so a typo cannot request a multi-gigabyte allocation.
	MaxOutputBytesLimit = 1 << 30 // 1 GiB
	MaxSourceBytesLimit = 1 << 28 // 256 MiB
)

// DefaultConfig returns a Config with the safe defaults applied. The caller
// must still set Workspace. AllowWrites is false, so the default posture is
// read-only.
func DefaultConfig() Config {
	return Config{
		AllowWrites:          false,
		MaxOutputBytes:       DefaultMaxOutputBytes,
		MaxSourceBytes:       DefaultMaxSourceBytes,
		Timeout:              DefaultTimeout,
		MaxPythonMemoryBytes: DefaultMaxPythonMemoryBytes,
		MaxExecutions:        DefaultMaxExecutions,
	}
}

// ConfigError is a typed configuration error returned by New.
type ConfigError struct {
	Field   string
	Message string
}

// Error implements the error interface.
func (e *ConfigError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Field == "" {
		return "scriptagent: " + e.Message
	}
	return "scriptagent: " + e.Field + ": " + e.Message
}

// Request is one execution request.
//
// Language is required. Source is the script text. Filename is an optional
// display name; engines choose a language-appropriate default.
//
// Timeout and MaxOutputBytes may only tighten the Agent's configured limits.
// A zero value inherits the Agent default. AllowProcess must be true for Bash
// and is rejected for Python and JavaScript.
type Request struct {
	Language Language
	Source   string
	Filename string

	Timeout        time.Duration
	MaxOutputBytes int
	AllowProcess   bool
}

// resolve applies defaults and validates a Config.
func (c Config) resolve() (Config, error) {
	if strings.TrimSpace(c.Workspace) == "" {
		return c, &ConfigError{Field: "Workspace", Message: "is required"}
	}
	if c.MaxOutputBytes < 0 {
		return c, &ConfigError{Field: "MaxOutputBytes", Message: "must be >= 0"}
	}
	if c.MaxOutputBytes == 0 {
		c.MaxOutputBytes = DefaultMaxOutputBytes
	}
	if c.MaxOutputBytes > MaxOutputBytesLimit {
		return c, &ConfigError{Field: "MaxOutputBytes", Message: fmt.Sprintf("must be <= %d", MaxOutputBytesLimit)}
	}
	if c.MaxSourceBytes < 0 {
		return c, &ConfigError{Field: "MaxSourceBytes", Message: "must be >= 0"}
	}
	if c.MaxSourceBytes == 0 {
		c.MaxSourceBytes = DefaultMaxSourceBytes
	}
	if c.MaxSourceBytes > MaxSourceBytesLimit {
		return c, &ConfigError{Field: "MaxSourceBytes", Message: fmt.Sprintf("must be <= %d", MaxSourceBytesLimit)}
	}
	if c.Timeout < 0 {
		return c, &ConfigError{Field: "Timeout", Message: "must be >= 0"}
	}
	if c.Timeout == 0 {
		c.Timeout = DefaultTimeout
	}
	if c.MaxPythonMemoryBytes < 0 {
		return c, &ConfigError{Field: "MaxPythonMemoryBytes", Message: "must be >= 0"}
	}
	if c.MaxPythonMemoryBytes == 0 {
		c.MaxPythonMemoryBytes = DefaultMaxPythonMemoryBytes
	}
	if c.MaxExecutions < 0 {
		return c, &ConfigError{Field: "MaxExecutions", Message: "must be >= 0"}
	}
	if c.MaxExecutions == 0 {
		c.MaxExecutions = DefaultMaxExecutions
	}
	return c, nil
}
