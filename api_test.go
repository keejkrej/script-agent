package scriptagent

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestParseLanguage(t *testing.T) {
	cases := map[string]Language{
		"python":     LanguagePython,
		"py":         LanguagePython,
		"PYTHON3":    LanguagePython,
		" py3 ":      LanguagePython,
		"javascript": LanguageJavaScript,
		"js":         LanguageJavaScript,
		"node":       LanguageJavaScript,
		"NodeJS":     LanguageJavaScript,
		"bash":       LanguageBash,
		"sh":         LanguageBash,
		"shell":      LanguageBash,
	}
	for raw, want := range cases {
		got, err := ParseLanguage(raw)
		if err != nil {
			t.Fatalf("ParseLanguage(%q): %v", raw, err)
		}
		if got != want {
			t.Fatalf("ParseLanguage(%q) = %q, want %q", raw, got, want)
		}
	}
	if _, err := ParseLanguage("ruby"); err == nil {
		t.Fatal("ParseLanguage(ruby) succeeded, want error")
	}
	if _, err := ParseLanguage(""); err == nil {
		t.Fatal("ParseLanguage(empty) succeeded, want error")
	}
}

func TestLanguageValid(t *testing.T) {
	for _, l := range []Language{LanguagePython, LanguageJavaScript, LanguageBash} {
		if !l.Valid() {
			t.Fatalf("%q.Valid() = false", l)
		}
	}
	if Language("ruby").Valid() {
		t.Fatal("ruby.Valid() = true")
	}
}

func TestConfigResolveDefaults(t *testing.T) {
	cfg, err := Config{Workspace: "/tmp"}.resolve()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cfg.MaxOutputBytes != DefaultMaxOutputBytes {
		t.Fatalf("MaxOutputBytes = %d", cfg.MaxOutputBytes)
	}
	if cfg.MaxSourceBytes != DefaultMaxSourceBytes {
		t.Fatalf("MaxSourceBytes = %d", cfg.MaxSourceBytes)
	}
	if cfg.Timeout != DefaultTimeout {
		t.Fatalf("Timeout = %s", cfg.Timeout)
	}
	if cfg.MaxPythonMemoryBytes != DefaultMaxPythonMemoryBytes {
		t.Fatalf("MaxPythonMemoryBytes = %d", cfg.MaxPythonMemoryBytes)
	}
	if cfg.MaxExecutions != DefaultMaxExecutions {
		t.Fatalf("MaxExecutions = %d", cfg.MaxExecutions)
	}
}

func TestConfigResolveRejects(t *testing.T) {
	cases := []Config{
		{},
		{Workspace: "  "},
		{Workspace: "/tmp", MaxOutputBytes: -1},
		{Workspace: "/tmp", MaxOutputBytes: MaxOutputBytesLimit + 1},
		{Workspace: "/tmp", MaxSourceBytes: -1},
		{Workspace: "/tmp", MaxSourceBytes: MaxSourceBytesLimit + 1},
		{Workspace: "/tmp", Timeout: -time.Second},
		{Workspace: "/tmp", MaxPythonMemoryBytes: -1},
		{Workspace: "/tmp", MaxExecutions: -1},
	}
	for i, cfg := range cases {
		if _, err := cfg.resolve(); err == nil {
			t.Fatalf("case %d: resolve succeeded, want error", i)
		}
	}
}

func TestDefaultConfigIsReadOnly(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.AllowWrites {
		t.Fatal("DefaultConfig().AllowWrites = true, want read-only default")
	}
	if cfg.MaxExecutions != DefaultMaxExecutions {
		t.Fatalf("MaxExecutions = %d", cfg.MaxExecutions)
	}
}

func TestResultJSONOmitsAbsentValue(t *testing.T) {
	res := Result{Language: LanguagePython, Status: StatusCompleted}
	data, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(data), `"value"`) {
		t.Fatalf("absent value was serialized: %s", data)
	}
}

func TestResultJSONPreservesPresentZeroValues(t *testing.T) {
	cases := []struct {
		name string
		v    any
		want string
	}{
		{"zero", 0, `"value":0`},
		{"false", false, `"value":false`},
		{"empty", "", `"value":""`},
		{"null", nil, `"value":null`},
		{"object", map[string]any{"a": 1}, `"value":{"a":1}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := Result{Language: LanguageJavaScript, Status: StatusCompleted}
			res.SetValue(tc.v)
			data, err := json.Marshal(res)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if !strings.Contains(string(data), tc.want) {
				t.Fatalf("marshal = %s, want substring %s", data, tc.want)
			}
		})
	}
}

func TestResultJSONOutputAndError(t *testing.T) {
	code := 7
	res := Result{
		Language: LanguageBash,
		Status:   StatusFailed,
		Output: []Output{
			{Kind: "text", Stream: "stdout", Text: "out"},
			{Kind: "text", Stream: "stderr", Level: "warn", Text: "err"},
			{Kind: "image", Stream: "stdout", MIME: "image/png", Data: []byte{1, 2, 3}},
		},
		Error:    &Error{Name: "ProcessError", Code: "exit_status", Message: "bad"},
		ExitCode: &code,
		Warnings: []string{"careful"},
		Duration: 5 * time.Millisecond,
	}
	data, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(data)
	for _, want := range []string{
		`"language":"bash"`,
		`"status":"failed"`,
		`"kind":"text"`,
		`"stream":"stderr"`,
		`"level":"warn"`,
		`"mime":"image/png"`,
		`"data":"AQID"`,
		`"name":"ProcessError"`,
		`"code":"exit_status"`,
		`"exitCode":7`,
		`"warnings":["careful"]`,
		`"duration":5000000`,
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("marshal = %s, want substring %s", s, want)
		}
	}
}

func TestErrorImplementsError(t *testing.T) {
	var err error = &Error{Name: "ScriptError", Code: "ValueError", Message: "bad"}
	if got := err.Error(); !strings.Contains(got, "ScriptError") || !strings.Contains(got, "ValueError") {
		t.Fatalf("Error() = %q", got)
	}
}

func TestResultJSONRoundTrip(t *testing.T) {
	res := Result{Language: LanguageJavaScript, Status: StatusCompleted}
	res.SetValue(map[string]any{"a": float64(1)})
	data, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Result
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !got.HasValue() {
		t.Fatal("round trip lost the value")
	}
	m, ok := got.Value.(map[string]any)
	if !ok || m["a"] != float64(1) {
		t.Fatalf("value = %#v", got.Value)
	}
}

func TestResultJSONRoundTripPresentNull(t *testing.T) {
	res := Result{Language: LanguageJavaScript, Status: StatusCompleted}
	res.SetValue(nil)
	data, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Result
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !got.HasValue() {
		t.Fatal("round trip lost the present null value")
	}
	if got.Value != nil {
		t.Fatalf("value = %#v", got.Value)
	}
}
