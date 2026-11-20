//go:build scriptagent_integration

package integration

import (
	"bytes"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	scriptagent "github.com/keejkrej/script-agent"
)

// capturedChatRequest is one raw request the fixture server received.
type capturedChatRequest struct {
	Method string
	Path   string
	Header http.Header
	Body   []byte
}

// chatFixture is a local TLS Chat Completions server that records every
// request and replies with a scripted sequence. It never reaches the public
// network.
type chatFixture struct {
	server   *httptest.Server
	mu       sync.Mutex
	requests []capturedChatRequest
	replies  []func(capturedChatRequest) (int, any)
}

func newChatFixture(t *testing.T, replies ...func(capturedChatRequest) (int, any)) *chatFixture {
	t.Helper()
	f := &chatFixture{replies: replies}
	f.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		if err != nil {
			http.Error(w, "read", http.StatusBadRequest)
			return
		}
		cap := capturedChatRequest{Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone(), Body: body}
		f.mu.Lock()
		f.requests = append(f.requests, cap)
		idx := len(f.requests) - 1
		f.mu.Unlock()
		if idx >= len(f.replies) {
			http.Error(w, "unexpected request", http.StatusInternalServerError)
			return
		}
		status, resp := f.replies[idx](cap)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *chatFixture) endpoint() string { return f.server.URL + "/v1/chat/completions" }

// caFile writes the fixture's public certificate to a 0644 PEM file outside
// the workspace. The CLI passes it through --ca-file.
func (f *chatFixture) caFile(t *testing.T) string {
	t.Helper()
	cert := f.server.Certificate()
	if cert == nil {
		t.Fatal("fixture server has no certificate")
	}
	data := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	path := filepath.Join(t.TempDir(), "fixture-ca.pem")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write CA file: %v", err)
	}
	return path
}

func (f *chatFixture) captured() []capturedChatRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]capturedChatRequest(nil), f.requests...)
}

// chatCompletion builds one OpenAI Chat Completions response body.
func chatCompletion(content string, toolCalls []scriptagent.ToolCall, finish string) map[string]any {
	msg := map[string]any{"role": "assistant", "content": content}
	if len(toolCalls) > 0 {
		msg["tool_calls"] = toolCalls
	}
	return map[string]any{
		"id":      "chatcmpl-test",
		"object":  "chat.completion",
		"created": 1,
		"model":   "test-model",
		"choices": []any{map[string]any{
			"index":         0,
			"message":       msg,
			"finish_reason": finish,
		}},
		"usage": map[string]any{
			"prompt_tokens":     10,
			"completion_tokens": 5,
			"total_tokens":      15,
		},
	}
}

// chatRequest is the decoded request body the fixture received.
type chatRequest struct {
	Model     string                  `json:"model"`
	Messages  []scriptagent.Message   `json:"messages"`
	Tools     []scriptagent.ModelTool `json:"tools"`
	MaxTokens int                     `json:"max_tokens"`
	Stream    bool                    `json:"stream"`
}

func decodeChatRequest(t *testing.T, raw []byte) chatRequest {
	t.Helper()
	var req chatRequest
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&req); err != nil {
		t.Fatalf("decode chat request: %v\nraw: %s", err, raw)
	}
	return req
}

func decodeConversationResult(t *testing.T, raw []byte) scriptagent.ConversationResult {
	t.Helper()
	if len(raw) == 0 {
		t.Fatal("stdout is empty, want one JSON object")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	var res scriptagent.ConversationResult
	if err := dec.Decode(&res); err != nil {
		t.Fatalf("decode conversation result: %v\nraw: %s", err, raw)
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); err != io.EOF {
		t.Fatalf("stdout has trailing data after the JSON object: err=%v extra=%s", err, extra)
	}
	return res
}

func toolResultText(res scriptagent.ConversationToolResult) string {
	var b strings.Builder
	for _, o := range res.Result.Output {
		if o.Kind == "text" {
			b.WriteString(o.Text)
		}
	}
	return b.String()
}

// TestAgentCLITLSPythonToolCall runs the real binary against a local TLS
// fixture. The model asks for one Python tool call, then returns final text.
func TestAgentCLITLSPythonToolCall(t *testing.T) {
	ws := t.TempDir()
	const toolCallID = "call_python_1"
	fixture := newChatFixture(t,
		func(capturedChatRequest) (int, any) {
			return http.StatusOK, chatCompletion("", []scriptagent.ToolCall{{
				ID:   toolCallID,
				Type: "function",
				Function: scriptagent.ToolFunction{
					Name:      "python_repl",
					Arguments: `{"code":"print(\"hello from python\")"}`,
				},
			}}, "tool_calls")
		},
		func(capturedChatRequest) (int, any) {
			return http.StatusOK, chatCompletion("done from script", nil, "stop")
		},
	)
	ca := fixture.caFile(t)
	env := append(os.Environ(), "SCRIPT_AGENT_TEST_PROVIDER_KEY=dummy-secret")

	run := runCLI(t, t.TempDir(), env,
		"agent",
		"--prompt", "inspect the workspace",
		"--workspace", ws,
		"--provider", "openai-compatible",
		"--endpoint", fixture.endpoint(),
		"--model", "test-model",
		"--api-key-env", "SCRIPT_AGENT_TEST_PROVIDER_KEY",
		"--ca-file", ca,
		"--transcript",
	)
	if run.ExitCode != 0 {
		t.Fatalf("exit = %d, stderr = %s", run.ExitCode, run.Stderr)
	}
	res := decodeConversationResult(t, run.Stdout)
	if res.Status != scriptagent.StatusCompleted {
		t.Fatalf("status = %q, error = %+v", res.Status, res.Error)
	}
	if res.Response != "done from script" {
		t.Fatalf("response = %q", res.Response)
	}
	if res.Turns != 2 {
		t.Fatalf("turns = %d, want 2", res.Turns)
	}
	if len(res.ToolResults) != 1 {
		t.Fatalf("tool results = %d, want 1", len(res.ToolResults))
	}
	tr := res.ToolResults[0]
	if tr.ToolCallID != toolCallID || tr.ToolName != "python_repl" {
		t.Fatalf("tool result = %+v", tr)
	}
	if tr.IsError {
		t.Fatalf("tool result is an error: %+v", tr.Result)
	}
	if got := toolResultText(tr); !strings.Contains(got, "hello from python") {
		t.Fatalf("tool output = %q", got)
	}
	if len(res.Messages) == 0 {
		t.Fatal("--transcript did not include messages")
	}

	reqs := fixture.captured()
	if len(reqs) != 2 {
		t.Fatalf("fixture requests = %d, want 2", len(reqs))
	}
	for i, req := range reqs {
		if req.Method != http.MethodPost {
			t.Fatalf("request %d method = %q", i, req.Method)
		}
		if req.Path != "/v1/chat/completions" {
			t.Fatalf("request %d path = %q", i, req.Path)
		}
		if got := req.Header.Get("Authorization"); got != "Bearer dummy-secret" {
			t.Fatalf("request %d authorization = %q", i, got)
		}
		if got := req.Header.Get("Content-Type"); got != "application/json" {
			t.Fatalf("request %d content type = %q", i, got)
		}
		body := decodeChatRequest(t, req.Body)
		if body.Model != "test-model" {
			t.Fatalf("request %d model = %q", i, body.Model)
		}
		if body.Stream {
			t.Fatalf("request %d stream = true", i)
		}
		if body.MaxTokens != scriptagent.DefaultMaxTokens {
			t.Fatalf("request %d max_tokens = %d", i, body.MaxTokens)
		}
		if len(body.Tools) != 3 {
			t.Fatalf("request %d tools = %d", i, len(body.Tools))
		}
		names := []string{body.Tools[0].Function.Name, body.Tools[1].Function.Name, body.Tools[2].Function.Name}
		want := []string{"python_repl", "javascript_repl", "bash_repl"}
		for j := range want {
			if names[j] != want[j] {
				t.Fatalf("request %d tool %d = %q, want %q", i, j, names[j], want[j])
			}
		}
		if len(body.Messages) < 2 || body.Messages[0].Role != "system" || body.Messages[1].Role != "user" {
			t.Fatalf("request %d messages = %+v", i, body.Messages)
		}
	}

	second := decodeChatRequest(t, reqs[1].Body)
	var toolMsg *scriptagent.Message
	for k := range second.Messages {
		if second.Messages[k].Role == "tool" {
			toolMsg = &second.Messages[k]
		}
	}
	if toolMsg == nil {
		t.Fatalf("second request has no tool message: %+v", second.Messages)
	}
	if toolMsg.ToolCallID != toolCallID {
		t.Fatalf("tool message call id = %q", toolMsg.ToolCallID)
	}
	if !strings.Contains(toolMsg.Content, "hello from python") {
		t.Fatalf("tool message content = %q", toolMsg.Content)
	}
	// The API key must never appear in the request body.
	if bytes.Contains(reqs[0].Body, []byte("dummy-secret")) || bytes.Contains(reqs[1].Body, []byte("dummy-secret")) {
		t.Fatal("request body contains the API key")
	}
}

// TestAgentCLIBashEnvIsolation proves the provider key reaches the model
// transport but not the Bash child environment.
func TestAgentCLIBashEnvIsolation(t *testing.T) {
	ws := t.TempDir()
	const toolCallID = "call_bash_1"
	args, err := json.Marshal(map[string]string{"code": `printf '%s' "$SCRIPT_AGENT_TEST_PROVIDER_KEY"`})
	if err != nil {
		t.Fatal(err)
	}
	fixture := newChatFixture(t,
		func(capturedChatRequest) (int, any) {
			return http.StatusOK, chatCompletion("", []scriptagent.ToolCall{{
				ID:       toolCallID,
				Type:     "function",
				Function: scriptagent.ToolFunction{Name: "bash_repl", Arguments: string(args)},
			}}, "tool_calls")
		},
		func(capturedChatRequest) (int, any) {
			return http.StatusOK, chatCompletion("done", nil, "stop")
		},
	)
	ca := fixture.caFile(t)
	env := append(os.Environ(), "SCRIPT_AGENT_TEST_PROVIDER_KEY=dummy-secret")

	run := runCLI(t, t.TempDir(), env,
		"agent",
		"--prompt", "inspect",
		"--workspace", ws,
		"--provider", "openai-compatible",
		"--endpoint", fixture.endpoint(),
		"--model", "test-model",
		"--api-key-env", "SCRIPT_AGENT_TEST_PROVIDER_KEY",
		"--ca-file", ca,
		"--allow-process",
		"--transcript",
	)
	if run.ExitCode != 0 {
		t.Fatalf("exit = %d, stderr = %s", run.ExitCode, run.Stderr)
	}
	res := decodeConversationResult(t, run.Stdout)
	if res.Status != scriptagent.StatusCompleted {
		t.Fatalf("status = %q, error = %+v", res.Status, res.Error)
	}
	if len(res.ToolResults) != 1 {
		t.Fatalf("tool results = %d", len(res.ToolResults))
	}
	tr := res.ToolResults[0]
	if tr.IsError {
		t.Fatalf("bash tool result is an error: %+v", tr.Result)
	}
	if got := toolResultText(tr); strings.Contains(got, "dummy-secret") {
		t.Fatalf("bash child inherited the provider key: %q", got)
	}
	if got := toolResultText(tr); got != "" {
		t.Fatalf("bash output = %q, want empty", got)
	}
	// The Bash privilege warning is always present.
	found := false
	for _, w := range tr.Result.Warnings {
		if strings.Contains(w, "full OS privileges") {
			found = true
		}
	}
	if !found {
		t.Fatalf("bash warning missing: %+v", tr.Result.Warnings)
	}
	reqs := fixture.captured()
	if len(reqs) != 2 {
		t.Fatalf("fixture requests = %d", len(reqs))
	}
	if got := reqs[0].Header.Get("Authorization"); got != "Bearer dummy-secret" {
		t.Fatalf("authorization = %q", got)
	}
}

// TestAgentCLIBashDeniedNoProcess proves a Bash call without --allow-process is
// denied before any child process starts.
func TestAgentCLIBashDeniedNoProcess(t *testing.T) {
	ws := t.TempDir()
	sentinel := filepath.Join(t.TempDir(), "sentinel.txt")
	const toolCallID = "call_bash_denied"
	args, err := json.Marshal(map[string]string{"code": "touch " + sentinel})
	if err != nil {
		t.Fatal(err)
	}
	fixture := newChatFixture(t,
		func(capturedChatRequest) (int, any) {
			return http.StatusOK, chatCompletion("", []scriptagent.ToolCall{{
				ID:       toolCallID,
				Type:     "function",
				Function: scriptagent.ToolFunction{Name: "bash_repl", Arguments: string(args)},
			}}, "tool_calls")
		},
		func(capturedChatRequest) (int, any) {
			return http.StatusOK, chatCompletion("done", nil, "stop")
		},
	)
	ca := fixture.caFile(t)
	env := append(os.Environ(), "SCRIPT_AGENT_TEST_PROVIDER_KEY=dummy-secret")

	run := runCLI(t, t.TempDir(), env,
		"agent",
		"--prompt", "inspect",
		"--workspace", ws,
		"--provider", "openai-compatible",
		"--endpoint", fixture.endpoint(),
		"--model", "test-model",
		"--api-key-env", "SCRIPT_AGENT_TEST_PROVIDER_KEY",
		"--ca-file", ca,
		"--transcript",
	)
	if run.ExitCode != 0 {
		t.Fatalf("exit = %d, stderr = %s", run.ExitCode, run.Stderr)
	}
	res := decodeConversationResult(t, run.Stdout)
	if res.Status != scriptagent.StatusCompleted {
		t.Fatalf("status = %q, error = %+v", res.Status, res.Error)
	}
	if len(res.ToolResults) != 1 {
		t.Fatalf("tool results = %d", len(res.ToolResults))
	}
	tr := res.ToolResults[0]
	if !tr.IsError {
		t.Fatalf("bash tool result is not an error: %+v", tr.Result)
	}
	if tr.Result.Status != scriptagent.StatusDenied {
		t.Fatalf("bash status = %q, want denied", tr.Result.Status)
	}
	if tr.Result.Error == nil || tr.Result.Error.Code != "process_denied" {
		t.Fatalf("bash error = %+v", tr.Result.Error)
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("sentinel exists; a denied Bash request started a process: %v", err)
	}
}
