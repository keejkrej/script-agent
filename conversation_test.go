package scriptagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

// fakeModel is a sequenced Model. It records every request and returns the
// configured response or error for each call.
type fakeModel struct {
	mu        sync.Mutex
	responses []ModelResponse
	errs      []error
	calls     []ModelRequest
	idx       int
	complete  func(context.Context, ModelRequest) (ModelResponse, error)
}

func (f *fakeModel) Complete(ctx context.Context, req ModelRequest) (ModelResponse, error) {
	f.mu.Lock()
	f.calls = append(f.calls, req)
	idx := f.idx
	f.idx++
	f.mu.Unlock()
	if f.complete != nil {
		return f.complete(ctx, req)
	}
	if idx < len(f.errs) && f.errs[idx] != nil {
		return ModelResponse{}, f.errs[idx]
	}
	if idx < len(f.responses) {
		return f.responses[idx], nil
	}
	return ModelResponse{}, errors.New("fake model: no response")
}

func (f *fakeModel) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeModel) call(i int) ModelRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[i]
}

// fakeExecutor is a programmable ScriptExecutor. Its default behavior mirrors
// the root Agent's Bash double-grant check.
type fakeExecutor struct {
	cfg  Config
	mu   sync.Mutex
	reqs []Request
	run  func(context.Context, Request) Result
}

func (f *fakeExecutor) Run(ctx context.Context, req Request) Result {
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	f.mu.Unlock()
	if f.run != nil {
		return f.run(ctx, req)
	}
	if req.Language == LanguageBash && (!f.cfg.AllowProcess || !req.AllowProcess) {
		return deniedResult(LanguageBash, "process_denied",
			"process execution is not allowed; set Config.AllowProcess and Request.AllowProcess")
	}
	return Result{Language: req.Language, Status: StatusCompleted}
}

func (f *fakeExecutor) Config() Config { return f.cfg }

func (f *fakeExecutor) requestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reqs)
}

func (f *fakeExecutor) request(i int) Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reqs[i]
}

func testExecConfig() Config {
	return Config{
		Workspace:      "/tmp/ws",
		MaxOutputBytes: 1 << 20,
		MaxSourceBytes: 256 << 10,
		Timeout:        30 * time.Second,
	}
}

func newTestConversation(t *testing.T, model Model, exec *fakeExecutor, mutate func(*ConversationConfig)) *Conversation {
	t.Helper()
	cfg := ConversationConfig{
		Executor:     exec,
		Model:        model,
		ModelID:      "test-model",
		SystemPrompt: "system",
	}
	if mutate != nil {
		mutate(&cfg)
	}
	conv, err := NewConversation(cfg)
	if err != nil {
		t.Fatalf("NewConversation: %v", err)
	}
	return conv
}

func toolCallResponse(id, name, args string) ModelResponse {
	return ModelResponse{
		Message: Message{
			Role: "assistant",
			ToolCalls: []ToolCall{{
				ID:       id,
				Type:     "function",
				Function: ToolFunction{Name: name, Arguments: args},
			}},
		},
		FinishReason: "tool_calls",
	}
}

func finalResponse(text string) ModelResponse {
	return ModelResponse{
		Message:      Message{Role: "assistant", Content: text},
		FinishReason: "stop",
	}
}

func TestConversationDirectAnswer(t *testing.T) {
	model := &fakeModel{responses: []ModelResponse{{
		Message:      Message{Role: "assistant", Content: "hello"},
		FinishReason: "stop",
		Usage:        ModelUsage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5},
	}}}
	exec := &fakeExecutor{cfg: testExecConfig()}
	conv := newTestConversation(t, model, exec, func(c *ConversationConfig) {
		c.IncludeTranscript = true
	})

	res := conv.Run(context.Background(), ConversationRequest{Prompt: "hi"})
	if res.Status != StatusCompleted {
		t.Fatalf("status = %q, want completed", res.Status)
	}
	if res.Response != "hello" {
		t.Fatalf("response = %q", res.Response)
	}
	if res.Turns != 1 {
		t.Fatalf("turns = %d", res.Turns)
	}
	if len(res.ToolResults) != 0 {
		t.Fatalf("tool results = %d", len(res.ToolResults))
	}
	if res.Usage != (ModelUsage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5}) {
		t.Fatalf("usage = %+v", res.Usage)
	}
	if len(res.Messages) != 3 {
		t.Fatalf("messages = %d, want 3", len(res.Messages))
	}
	if res.Messages[0].Role != "system" || res.Messages[0].Content != "system" {
		t.Fatalf("messages[0] = %+v", res.Messages[0])
	}
	if res.Messages[1].Role != "user" || res.Messages[1].Content != "hi" {
		t.Fatalf("messages[1] = %+v", res.Messages[1])
	}
	if res.Messages[2].Role != "assistant" || res.Messages[2].Content != "hello" {
		t.Fatalf("messages[2] = %+v", res.Messages[2])
	}
	if exec.requestCount() != 0 {
		t.Fatalf("executor calls = %d", exec.requestCount())
	}
	if model.callCount() != 1 {
		t.Fatalf("model calls = %d", model.callCount())
	}
	req := model.call(0)
	if req.Model != "test-model" || req.MaxTokens != DefaultMaxTokens {
		t.Fatalf("request = %+v", req)
	}
	if len(req.Tools) != 3 {
		t.Fatalf("tools = %d", len(req.Tools))
	}
}

func TestConversationPythonTool(t *testing.T) {
	model := &fakeModel{responses: []ModelResponse{
		toolCallResponse("call_1", ToolPython,
			`{"code":"print(1)","filename":"main.py","timeout_ms":1000,"max_output_bytes":2048}`),
		finalResponse("done"),
	}}
	exec := &fakeExecutor{cfg: testExecConfig(), run: func(ctx context.Context, req Request) Result {
		return Result{
			Language: req.Language,
			Status:   StatusCompleted,
			Output:   []Output{{Kind: "text", Stream: "stdout", Text: "1\n"}},
		}
	}}
	conv := newTestConversation(t, model, exec, func(c *ConversationConfig) {
		c.IncludeTranscript = true
	})

	res := conv.Run(context.Background(), ConversationRequest{Prompt: "run"})
	if res.Status != StatusCompleted || res.Response != "done" {
		t.Fatalf("result = %+v", res)
	}
	if res.Turns != 2 {
		t.Fatalf("turns = %d", res.Turns)
	}
	if exec.requestCount() != 1 {
		t.Fatalf("executor calls = %d", exec.requestCount())
	}
	req := exec.request(0)
	if req.Language != LanguagePython || req.Source != "print(1)" || req.Filename != "main.py" {
		t.Fatalf("request = %+v", req)
	}
	if req.Timeout != time.Second || req.MaxOutputBytes != 2048 || req.AllowProcess {
		t.Fatalf("request limits = %+v", req)
	}
	if len(res.ToolResults) != 1 {
		t.Fatalf("tool results = %d", len(res.ToolResults))
	}
	tr := res.ToolResults[0]
	if tr.ToolCallID != "call_1" || tr.ToolName != ToolPython || tr.IsError || tr.Truncated {
		t.Fatalf("tool result = %+v", tr)
	}
	if tr.Result.Status != StatusCompleted || len(tr.Result.Output) != 1 || tr.Result.Output[0].Text != "1\n" {
		t.Fatalf("result = %+v", tr.Result)
	}
	if len(tr.Result.Warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", tr.Result.Warnings)
	}

	// The second completion must see the assistant call and the tool message.
	second := model.call(1)
	if len(second.Messages) != 4 {
		t.Fatalf("second messages = %d, want 4", len(second.Messages))
	}
	if second.Messages[2].Role != "assistant" || len(second.Messages[2].ToolCalls) != 1 {
		t.Fatalf("assistant message = %+v", second.Messages[2])
	}
	toolMsg := second.Messages[3]
	if toolMsg.Role != "tool" || toolMsg.ToolCallID != "call_1" || toolMsg.Name != ToolPython {
		t.Fatalf("tool message = %+v", toolMsg)
	}
	var decoded ConversationToolResult
	if err := json.Unmarshal([]byte(toolMsg.Content), &decoded); err != nil {
		t.Fatalf("decode tool content: %v", err)
	}
	if decoded.ToolCallID != "call_1" || decoded.Result.Status != StatusCompleted {
		t.Fatalf("decoded tool content = %+v", decoded)
	}
}

func TestConversationJavaScriptTool(t *testing.T) {
	model := &fakeModel{responses: []ModelResponse{
		toolCallResponse("call_js", ToolJavaScript, `{"code":"return 1"}`),
		finalResponse("ok"),
	}}
	exec := &fakeExecutor{cfg: testExecConfig()}
	conv := newTestConversation(t, model, exec, nil)

	res := conv.Run(context.Background(), ConversationRequest{Prompt: "run"})
	if res.Status != StatusCompleted {
		t.Fatalf("status = %q", res.Status)
	}
	req := exec.request(0)
	if req.Language != LanguageJavaScript || req.Source != "return 1" || req.AllowProcess {
		t.Fatalf("request = %+v", req)
	}
	if req.Timeout != testExecConfig().Timeout || req.MaxOutputBytes != testExecConfig().MaxOutputBytes {
		t.Fatalf("request limits = %+v", req)
	}
	if res.Messages != nil {
		t.Fatalf("messages present without IncludeTranscript: %+v", res.Messages)
	}
}

func TestConversationBashGrants(t *testing.T) {
	cases := []struct {
		name        string
		coreGrant   bool
		convoGrant  bool
		wantProcess bool
		wantStatus  Status
	}{
		{"both grants", true, true, true, StatusCompleted},
		{"missing conversation grant", true, false, false, StatusDenied},
		{"missing core grant", false, true, true, StatusDenied},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			model := &fakeModel{responses: []ModelResponse{
				toolCallResponse("call_bash", ToolBash, `{"code":"echo hi"}`),
				finalResponse("ok"),
			}}
			cfg := testExecConfig()
			cfg.AllowProcess = tc.coreGrant
			exec := &fakeExecutor{cfg: cfg}
			conv := newTestConversation(t, model, exec, nil)

			res := conv.Run(context.Background(), ConversationRequest{Prompt: "run", AllowProcess: tc.convoGrant})
			if res.Status != StatusCompleted {
				t.Fatalf("status = %q", res.Status)
			}
			req := exec.request(0)
			if req.AllowProcess != tc.wantProcess {
				t.Fatalf("AllowProcess = %v, want %v", req.AllowProcess, tc.wantProcess)
			}
			tr := res.ToolResults[0]
			if tr.Result.Status != tc.wantStatus {
				t.Fatalf("tool status = %q, want %q", tr.Result.Status, tc.wantStatus)
			}
			if !hasWarning(tr.Result.Warnings, bashPrivilegeWarning) {
				t.Fatalf("bash warning missing: %v", tr.Result.Warnings)
			}
		})
	}
}

func hasWarning(warnings []string, want string) bool {
	for _, w := range warnings {
		if w == want {
			return true
		}
	}
	return false
}

func TestConversationUnknownTool(t *testing.T) {
	model := &fakeModel{responses: []ModelResponse{
		toolCallResponse("call_x", "ruby_repl", `{"code":"puts 1"}`),
		finalResponse("ok"),
	}}
	exec := &fakeExecutor{cfg: testExecConfig()}
	conv := newTestConversation(t, model, exec, nil)

	res := conv.Run(context.Background(), ConversationRequest{Prompt: "run"})
	if res.Status != StatusCompleted {
		t.Fatalf("status = %q", res.Status)
	}
	if exec.requestCount() != 0 {
		t.Fatalf("executor calls = %d, want 0", exec.requestCount())
	}
	tr := res.ToolResults[0]
	if !tr.IsError || tr.Result.Status != StatusInvalidRequest {
		t.Fatalf("tool result = %+v", tr)
	}
	if tr.Result.Error == nil || tr.Result.Error.Code != "unknown_tool" {
		t.Fatalf("error = %+v", tr.Result.Error)
	}
}

func TestConversationInvalidArguments(t *testing.T) {
	cases := []struct {
		name string
		args string
	}{
		{"not json", `not json`},
		{"array", `[]`},
		{"unknown field", `{"code":"x","extra":1}`},
		{"duplicate key", `{"code":"x","code":"y"}`},
		{"missing code", `{}`},
		{"code not string", `{"code":1}`},
		{"filename slash", `{"code":"x","filename":"a/b"}`},
		{"filename backslash", `{"code":"x","filename":"a\\b"}`},
		{"filename dot", `{"code":"x","filename":"."}`},
		{"filename dotdot", `{"code":"x","filename":".."}`},
		{"filename control", `{"code":"x","filename":"a\u0000b"}`},
		{"timeout zero", `{"code":"x","timeout_ms":0}`},
		{"timeout fraction", `{"code":"x","timeout_ms":1.5}`},
		{"timeout exponent", `{"code":"x","timeout_ms":1e3}`},
		{"timeout string", `{"code":"x","timeout_ms":"100"}`},
		{"timeout too large", `{"code":"x","timeout_ms":999999999}`},
		{"output zero", `{"code":"x","max_output_bytes":0}`},
		{"output too large", `{"code":"x","max_output_bytes":999999999}`},
		{"trailing json", `{"code":"x"} {}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			model := &fakeModel{responses: []ModelResponse{
				toolCallResponse("call_1", ToolPython, tc.args),
				finalResponse("ok"),
			}}
			exec := &fakeExecutor{cfg: testExecConfig()}
			conv := newTestConversation(t, model, exec, nil)

			res := conv.Run(context.Background(), ConversationRequest{Prompt: "run"})
			if res.Status != StatusCompleted {
				t.Fatalf("status = %q", res.Status)
			}
			if exec.requestCount() != 0 {
				t.Fatalf("executor calls = %d, want 0", exec.requestCount())
			}
			tr := res.ToolResults[0]
			if !tr.IsError || tr.Result.Status != StatusInvalidRequest {
				t.Fatalf("tool result = %+v", tr)
			}
			if tr.Result.Error == nil || tr.Result.Error.Code != "invalid_arguments" {
				t.Fatalf("error = %+v", tr.Result.Error)
			}
		})
	}
}

func TestConversationFilenameLimits(t *testing.T) {
	cfg := testExecConfig()
	valid := []string{"main.py", "a b.txt", "ü.py", strings.Repeat("a", 255)}
	for _, name := range valid {
		args := fmt.Sprintf(`{"code":"x","filename":%q}`, name)
		if _, err := parseToolArguments(args, cfg); err != nil {
			t.Fatalf("valid filename %q: %v", name, err)
		}
	}
	invalid := []string{
		strings.Repeat("a", 256),
		"a/b",
		"a\\b",
		".",
		"..",
		"a\x00b",
		"a\nb",
	}
	for _, name := range invalid {
		args := fmt.Sprintf(`{"code":"x","filename":%q}`, name)
		if _, err := parseToolArguments(args, cfg); err == nil {
			t.Fatalf("invalid filename %q accepted", name)
		}
	}
}

func TestConversationMaxTurns(t *testing.T) {
	var n int
	model := &fakeModel{complete: func(ctx context.Context, req ModelRequest) (ModelResponse, error) {
		n++
		return toolCallResponse(fmt.Sprintf("call_%d", n), ToolPython, `{"code":"x"}`), nil
	}}
	exec := &fakeExecutor{cfg: testExecConfig()}
	conv := newTestConversation(t, model, exec, func(c *ConversationConfig) {
		c.MaxTurns = 2
	})

	res := conv.Run(context.Background(), ConversationRequest{Prompt: "run"})
	if res.Status != StatusLimitExceeded {
		t.Fatalf("status = %q", res.Status)
	}
	if res.Error == nil || res.Error.Code != "max_turns" {
		t.Fatalf("error = %+v", res.Error)
	}
	if model.callCount() != 2 {
		t.Fatalf("model calls = %d, want 2", model.callCount())
	}
	if exec.requestCount() != 2 {
		t.Fatalf("executor calls = %d, want 2", exec.requestCount())
	}
	if res.Turns != 2 {
		t.Fatalf("turns = %d", res.Turns)
	}
}

func TestConversationMaxToolCalls(t *testing.T) {
	model := &fakeModel{responses: []ModelResponse{{
		Message: Message{
			Role: "assistant",
			ToolCalls: []ToolCall{
				{ID: "call_1", Type: "function", Function: ToolFunction{Name: ToolPython, Arguments: `{"code":"1"}`}},
				{ID: "call_2", Type: "function", Function: ToolFunction{Name: ToolPython, Arguments: `{"code":"2"}`}},
				{ID: "call_3", Type: "function", Function: ToolFunction{Name: ToolPython, Arguments: `{"code":"3"}`}},
			},
		},
		FinishReason: "tool_calls",
	}}}
	exec := &fakeExecutor{cfg: testExecConfig()}
	conv := newTestConversation(t, model, exec, func(c *ConversationConfig) {
		c.MaxToolCalls = 2
	})

	res := conv.Run(context.Background(), ConversationRequest{Prompt: "run"})
	if res.Status != StatusLimitExceeded {
		t.Fatalf("status = %q", res.Status)
	}
	if res.Error == nil || res.Error.Code != "max_tool_calls" {
		t.Fatalf("error = %+v", res.Error)
	}
	if model.callCount() != 1 {
		t.Fatalf("model calls = %d, want 1", model.callCount())
	}
	if exec.requestCount() != 2 {
		t.Fatalf("executor calls = %d, want 2", exec.requestCount())
	}
	if len(res.ToolResults) != 3 {
		t.Fatalf("tool results = %d, want 3", len(res.ToolResults))
	}
	excess := res.ToolResults[2]
	if excess.ToolCallID != "call_3" || !excess.IsError {
		t.Fatalf("excess result = %+v", excess)
	}
	if excess.Result.Error == nil || excess.Result.Error.Code != "max_tool_calls" {
		t.Fatalf("excess error = %+v", excess.Result.Error)
	}
}

func TestConversationFinishLength(t *testing.T) {
	resp := toolCallResponse("call_1", ToolPython, `{"code":"x"}`)
	resp.FinishReason = "length"
	model := &fakeModel{responses: []ModelResponse{resp}}
	exec := &fakeExecutor{cfg: testExecConfig()}
	conv := newTestConversation(t, model, exec, nil)

	res := conv.Run(context.Background(), ConversationRequest{Prompt: "run"})
	if res.Status != StatusLimitExceeded {
		t.Fatalf("status = %q", res.Status)
	}
	if res.Error == nil || res.Error.Code != "length" {
		t.Fatalf("error = %+v", res.Error)
	}
	if exec.requestCount() != 0 {
		t.Fatalf("executor calls = %d, want 0", exec.requestCount())
	}
	if model.callCount() != 1 {
		t.Fatalf("model calls = %d", model.callCount())
	}
}

func TestConversationContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	model := &fakeModel{complete: func(ctx context.Context, req ModelRequest) (ModelResponse, error) {
		cancel()
		return ModelResponse{}, ctx.Err()
	}}
	exec := &fakeExecutor{cfg: testExecConfig()}
	conv := newTestConversation(t, model, exec, nil)

	res := conv.Run(ctx, ConversationRequest{Prompt: "run"})
	if res.Status != StatusCancelled {
		t.Fatalf("status = %q", res.Status)
	}
	if res.Error == nil || res.Error.Name != "Cancelled" {
		t.Fatalf("error = %+v", res.Error)
	}
}

func TestConversationContextDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond)
	model := &fakeModel{responses: []ModelResponse{finalResponse("never")}}
	exec := &fakeExecutor{cfg: testExecConfig()}
	conv := newTestConversation(t, model, exec, nil)

	res := conv.Run(ctx, ConversationRequest{Prompt: "run"})
	if res.Status != StatusTimedOut {
		t.Fatalf("status = %q", res.Status)
	}
	if res.Error == nil || res.Error.Name != "Timeout" {
		t.Fatalf("error = %+v", res.Error)
	}
}

func TestConversationValueRepresentation(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  any
	}{
		{"null", nil, nil},
		{"false", false, false},
		{"zero", 0, float64(0)},
		{"empty string", "", ""},
		{"object", map[string]any{"a": 1}, map[string]any{"a": float64(1)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			model := &fakeModel{responses: []ModelResponse{
				toolCallResponse("call_1", ToolPython, `{"code":"x"}`),
				finalResponse("ok"),
			}}
			exec := &fakeExecutor{cfg: testExecConfig(), run: func(ctx context.Context, req Request) Result {
				res := Result{Language: req.Language, Status: StatusCompleted}
				res.SetValue(tc.value)
				return res
			}}
			conv := newTestConversation(t, model, exec, nil)

			res := conv.Run(context.Background(), ConversationRequest{Prompt: "run"})
			tr := res.ToolResults[0]
			if !tr.Result.HasValue() {
				t.Fatalf("value not present")
			}
			if !jsonEqual(tr.Result.Value, tc.want) {
				t.Fatalf("value = %#v, want %#v", tr.Result.Value, tc.want)
			}
			// The tool message must carry the same value.
			var decoded ConversationToolResult
			if err := json.Unmarshal([]byte(model.call(1).Messages[3].Content), &decoded); err != nil {
				t.Fatalf("decode tool content: %v", err)
			}
			if !decoded.Result.HasValue() || !jsonEqual(decoded.Result.Value, tc.want) {
				t.Fatalf("decoded value = %#v", decoded.Result.Value)
			}
		})
	}
}

func jsonEqual(a, b any) bool {
	ra, err := json.Marshal(a)
	if err != nil {
		return false
	}
	rb, err := json.Marshal(b)
	if err != nil {
		return false
	}
	return string(ra) == string(rb)
}

func TestConversationToolResultTruncation(t *testing.T) {
	big := strings.Repeat("a", 20<<10)
	image := make([]byte, 20<<10)
	for i := range image {
		image[i] = byte(i)
	}
	imageCopy := append([]byte(nil), image...)
	model := &fakeModel{responses: []ModelResponse{
		toolCallResponse("call_1", ToolPython, `{"code":"x"}`),
		finalResponse("ok"),
	}}
	exec := &fakeExecutor{cfg: testExecConfig(), run: func(ctx context.Context, req Request) Result {
		return Result{
			Language: req.Language,
			Status:   StatusFailed,
			Output: []Output{
				{Kind: "text", Stream: "stdout", Text: big},
				{Kind: "image", Stream: "stdout", MIME: "image/png", Data: image},
			},
			Error:    &Error{Name: "ScriptError", Message: big, Stack: big},
			Warnings: []string{big},
			Value:    big,
		}
	}}
	conv := newTestConversation(t, model, exec, func(c *ConversationConfig) {
		c.MaxToolResultBytes = 4096
	})

	res := conv.Run(context.Background(), ConversationRequest{Prompt: "run"})
	tr := res.ToolResults[0]
	if !tr.Truncated {
		t.Fatal("truncated = false")
	}
	if tr.Result.Status != StatusFailed {
		t.Fatalf("status = %q", tr.Result.Status)
	}
	if len(tr.Result.Error.Stack) > maxToolStackBytes {
		t.Fatalf("stack = %d", len(tr.Result.Error.Stack))
	}
	if len(tr.Result.Output) > 0 && len(tr.Result.Output[0].Text) > maxToolOutputEventBytes {
		t.Fatalf("text = %d", len(tr.Result.Output[0].Text))
	}
	if len(tr.Result.Output) > 1 && len(tr.Result.Output[1].Data) > maxToolOutputEventBytes {
		t.Fatalf("image = %d", len(tr.Result.Output[1].Data))
	}
	if len(tr.Result.Warnings) > 0 && len(tr.Result.Warnings[0]) > maxToolWarningBytes {
		t.Fatalf("warning = %d", len(tr.Result.Warnings[0]))
	}
	content := model.call(1).Messages[3].Content
	if len(content) > 4096 {
		t.Fatalf("tool content = %d bytes, want <= 4096", len(content))
	}
	var decoded ConversationToolResult
	if err := json.Unmarshal([]byte(content), &decoded); err != nil {
		t.Fatalf("tool content is not valid JSON: %v", err)
	}
	// The original image data must not be mutated by normalization.
	if len(image) != 20<<10 {
		t.Fatalf("original image length = %d", len(image))
	}
	for i := range imageCopy {
		if image[i] != imageCopy[i] {
			t.Fatalf("original image byte %d changed", i)
		}
	}
}

func TestConversationOutputEventCap(t *testing.T) {
	text := strings.Repeat("a", 20<<10)
	image := make([]byte, 20<<10)
	model := &fakeModel{responses: []ModelResponse{
		toolCallResponse("call_1", ToolPython, `{"code":"x"}`),
		finalResponse("ok"),
	}}
	exec := &fakeExecutor{cfg: testExecConfig(), run: func(ctx context.Context, req Request) Result {
		return Result{
			Language: req.Language,
			Status:   StatusCompleted,
			Output: []Output{
				{Kind: "text", Stream: "stdout", Text: text},
				{Kind: "image", Stream: "stdout", MIME: "image/png", Data: image},
			},
		}
	}}
	conv := newTestConversation(t, model, exec, nil)

	res := conv.Run(context.Background(), ConversationRequest{Prompt: "run"})
	tr := res.ToolResults[0]
	if !tr.Truncated {
		t.Fatal("truncated = false")
	}
	if len(tr.Result.Output) != 2 {
		t.Fatalf("output events = %d, want 2", len(tr.Result.Output))
	}
	if got := len(tr.Result.Output[0].Text); got != maxToolOutputEventBytes {
		t.Fatalf("text = %d, want %d", got, maxToolOutputEventBytes)
	}
	if got := len(tr.Result.Output[1].Data); got != maxToolOutputEventBytes {
		t.Fatalf("image = %d, want %d", got, maxToolOutputEventBytes)
	}
}

func TestConversationStackUTF8(t *testing.T) {
	stack := strings.Repeat("a", maxToolStackBytes-1) + strings.Repeat("é", 100)
	model := &fakeModel{responses: []ModelResponse{
		toolCallResponse("call_1", ToolPython, `{"code":"x"}`),
		finalResponse("ok"),
	}}
	exec := &fakeExecutor{cfg: testExecConfig(), run: func(ctx context.Context, req Request) Result {
		return Result{
			Language: req.Language,
			Status:   StatusFailed,
			Error:    &Error{Name: "ScriptError", Message: "boom", Stack: stack},
		}
	}}
	conv := newTestConversation(t, model, exec, nil)

	res := conv.Run(context.Background(), ConversationRequest{Prompt: "run"})
	got := res.ToolResults[0].Result.Error.Stack
	if len(got) > maxToolStackBytes {
		t.Fatalf("stack = %d", len(got))
	}
	if !utf8.ValidString(got) {
		t.Fatal("stack is not valid UTF-8")
	}
}

func TestConversationBashWarning(t *testing.T) {
	cases := []struct {
		name     string
		warnings []string
		want     int
	}{
		{"absent", nil, 1},
		{"present", []string{bashPrivilegeWarning}, 1},
		{"other", []string{"other"}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			model := &fakeModel{responses: []ModelResponse{
				toolCallResponse("call_1", ToolBash, `{"code":"true"}`),
				finalResponse("ok"),
			}}
			cfg := testExecConfig()
			cfg.AllowProcess = true
			exec := &fakeExecutor{cfg: cfg, run: func(ctx context.Context, req Request) Result {
				return Result{Language: req.Language, Status: StatusCompleted, Warnings: tc.warnings}
			}}
			conv := newTestConversation(t, model, exec, nil)

			res := conv.Run(context.Background(), ConversationRequest{Prompt: "run", AllowProcess: true})
			got := res.ToolResults[0].Result.Warnings
			if len(got) != tc.want {
				t.Fatalf("warnings = %v, want %d", got, tc.want)
			}
			if !hasWarning(got, bashPrivilegeWarning) {
				t.Fatalf("bash warning missing: %v", got)
			}
		})
	}
}

func TestConversationHistoryCap(t *testing.T) {
	model := &fakeModel{responses: []ModelResponse{
		toolCallResponse("call_1", ToolPython, `{"code":"x"}`),
		finalResponse("ok"),
	}}
	exec := &fakeExecutor{cfg: testExecConfig(), run: func(ctx context.Context, req Request) Result {
		return Result{
			Language: req.Language,
			Status:   StatusCompleted,
			Output:   []Output{{Kind: "text", Stream: "stdout", Text: strings.Repeat("a", 1024)}},
		}
	}}
	conv := newTestConversation(t, model, exec, func(c *ConversationConfig) {
		c.MaxHistoryBytes = 300
	})

	res := conv.Run(context.Background(), ConversationRequest{Prompt: "run"})
	if res.Status != StatusLimitExceeded {
		t.Fatalf("status = %q", res.Status)
	}
	if res.Error == nil || res.Error.Code != "max_history" {
		t.Fatalf("error = %+v", res.Error)
	}
	if model.callCount() != 1 {
		t.Fatalf("model calls = %d, want 1", model.callCount())
	}
}

func TestConversationProtocolErrors(t *testing.T) {
	cases := []struct {
		name string
		resp ModelResponse
	}{
		{"wrong role", ModelResponse{Message: Message{Role: "user", Content: "x"}}},
		{"no text no calls", ModelResponse{Message: Message{Role: "assistant"}}},
		{"missing id", ModelResponse{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{
			Type: "function", Function: ToolFunction{Name: ToolPython, Arguments: `{"code":"x"}`},
		}}}}},
		{"huge id", ModelResponse{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{
			ID: strings.Repeat("a", maxToolCallIDBytes+1), Type: "function",
			Function: ToolFunction{Name: ToolPython, Arguments: `{"code":"x"}`},
		}}}}},
		{"duplicate id", ModelResponse{Message: Message{Role: "assistant", ToolCalls: []ToolCall{
			{ID: "a", Type: "function", Function: ToolFunction{Name: ToolPython, Arguments: `{"code":"x"}`}},
			{ID: "a", Type: "function", Function: ToolFunction{Name: ToolPython, Arguments: `{"code":"y"}`}},
		}}}},
		{"wrong type", ModelResponse{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{
			ID: "a", Type: "text", Function: ToolFunction{Name: ToolPython, Arguments: `{"code":"x"}`},
		}}}}},
		{"missing name", ModelResponse{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{
			ID: "a", Type: "function", Function: ToolFunction{Arguments: `{"code":"x"}`},
		}}}}},
		{"negative usage", ModelResponse{Message: Message{Role: "assistant", Content: "x"}, Usage: ModelUsage{InputTokens: -1}}},
		{"completion over cap", ModelResponse{Message: Message{Role: "assistant", Content: "x"}, Usage: ModelUsage{OutputTokens: DefaultMaxTokens + 1}}},
		{"giant usage", ModelResponse{Message: Message{Role: "assistant", Content: "x"}, Usage: ModelUsage{TotalTokens: 1 << 50}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			model := &fakeModel{responses: []ModelResponse{tc.resp}}
			exec := &fakeExecutor{cfg: testExecConfig()}
			conv := newTestConversation(t, model, exec, nil)

			res := conv.Run(context.Background(), ConversationRequest{Prompt: "run"})
			if res.Status != StatusFailed {
				t.Fatalf("status = %q", res.Status)
			}
			if res.Error == nil || res.Error.Name != "ProtocolError" {
				t.Fatalf("error = %+v", res.Error)
			}
			if exec.requestCount() != 0 {
				t.Fatalf("executor calls = %d", exec.requestCount())
			}
		})
	}
}

func TestConversationProviderError(t *testing.T) {
	const secret = "sk-secret-provider-value"
	model := &fakeModel{errs: []error{errors.New(secret)}}
	exec := &fakeExecutor{cfg: testExecConfig()}
	conv := newTestConversation(t, model, exec, nil)

	res := conv.Run(context.Background(), ConversationRequest{Prompt: "prompt-secret-value"})
	if res.Status != StatusFailed {
		t.Fatalf("status = %q", res.Status)
	}
	if res.Error == nil || res.Error.Name != "ProviderError" {
		t.Fatalf("error = %+v", res.Error)
	}
	if strings.Contains(res.Error.Message, secret) || strings.Contains(res.Error.Message, "prompt-secret-value") {
		t.Fatalf("error leaks request or provider data: %q", res.Error.Message)
	}
}

func TestConversationTranscriptFresh(t *testing.T) {
	model := &fakeModel{responses: []ModelResponse{
		finalResponse("one"),
		finalResponse("two"),
	}}
	exec := &fakeExecutor{cfg: testExecConfig()}
	conv := newTestConversation(t, model, exec, nil)

	first := conv.Run(context.Background(), ConversationRequest{Prompt: "first"})
	if first.Status != StatusCompleted {
		t.Fatalf("first status = %q", first.Status)
	}
	second := conv.Run(context.Background(), ConversationRequest{Prompt: "second"})
	if second.Status != StatusCompleted {
		t.Fatalf("second status = %q", second.Status)
	}
	req := model.call(1)
	if len(req.Messages) != 2 {
		t.Fatalf("second run messages = %d, want 2", len(req.Messages))
	}
	if req.Messages[0].Role != "system" || req.Messages[1].Role != "user" || req.Messages[1].Content != "second" {
		t.Fatalf("second run messages = %+v", req.Messages)
	}
}

func TestConversationNewValidation(t *testing.T) {
	validExec := &fakeExecutor{cfg: testExecConfig()}
	validModel := &fakeModel{}
	badExec := func(mutate func(*Config)) *fakeExecutor {
		cfg := testExecConfig()
		mutate(&cfg)
		return &fakeExecutor{cfg: cfg}
	}
	cases := []struct {
		name  string
		cfg   ConversationConfig
		field string
	}{
		{"nil executor", ConversationConfig{Model: validModel, ModelID: "m"}, "Executor"},
		{"nil model", ConversationConfig{Executor: validExec, ModelID: "m"}, "Model"},
		{"empty model id", ConversationConfig{Executor: validExec, Model: validModel}, "ModelID"},
		{"negative turns", ConversationConfig{Executor: validExec, Model: validModel, ModelID: "m", MaxTurns: -1}, "MaxTurns"},
		{"turns over cap", ConversationConfig{Executor: validExec, Model: validModel, ModelID: "m", MaxTurns: MaxTurnsLimit + 1}, "MaxTurns"},
		{"negative tokens", ConversationConfig{Executor: validExec, Model: validModel, ModelID: "m", MaxTokens: -1}, "MaxTokens"},
		{"tokens over cap", ConversationConfig{Executor: validExec, Model: validModel, ModelID: "m", MaxTokens: MaxTokensLimit + 1}, "MaxTokens"},
		{"negative tool calls", ConversationConfig{Executor: validExec, Model: validModel, ModelID: "m", MaxToolCalls: -1}, "MaxToolCalls"},
		{"tool calls over cap", ConversationConfig{Executor: validExec, Model: validModel, ModelID: "m", MaxToolCalls: MaxToolCallsLimit + 1}, "MaxToolCalls"},
		{"negative result bytes", ConversationConfig{Executor: validExec, Model: validModel, ModelID: "m", MaxToolResultBytes: -1}, "MaxToolResultBytes"},
		{"result bytes over cap", ConversationConfig{Executor: validExec, Model: validModel, ModelID: "m", MaxToolResultBytes: MaxToolResultBytesLimit + 1}, "MaxToolResultBytes"},
		{"negative history", ConversationConfig{Executor: validExec, Model: validModel, ModelID: "m", MaxHistoryBytes: -1}, "MaxHistoryBytes"},
		{"history over cap", ConversationConfig{Executor: validExec, Model: validModel, ModelID: "m", MaxHistoryBytes: MaxHistoryBytesLimit + 1}, "MaxHistoryBytes"},
		{"exec source zero", ConversationConfig{Executor: badExec(func(c *Config) { c.MaxSourceBytes = 0 }), Model: validModel, ModelID: "m"}, "Executor.Config.MaxSourceBytes"},
		{"exec output zero", ConversationConfig{Executor: badExec(func(c *Config) { c.MaxOutputBytes = 0 }), Model: validModel, ModelID: "m"}, "Executor.Config.MaxOutputBytes"},
		{"exec timeout zero", ConversationConfig{Executor: badExec(func(c *Config) { c.Timeout = 0 }), Model: validModel, ModelID: "m"}, "Executor.Config.Timeout"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewConversation(tc.cfg)
			if err == nil {
				t.Fatal("NewConversation succeeded, want error")
			}
			var ce *ConfigError
			if !errors.As(err, &ce) {
				t.Fatalf("error = %T %v", err, err)
			}
			if ce.Field != tc.field {
				t.Fatalf("field = %q, want %q", ce.Field, tc.field)
			}
		})
	}
}

func TestConversationToolDefinitions(t *testing.T) {
	conv := newTestConversation(t, &fakeModel{}, &fakeExecutor{cfg: testExecConfig()}, nil)
	if len(conv.tools) != 3 {
		t.Fatalf("tools = %d", len(conv.tools))
	}
	want := []string{ToolPython, ToolJavaScript, ToolBash}
	for i, tool := range conv.tools {
		if tool.Type != "function" {
			t.Fatalf("tool %d type = %q", i, tool.Type)
		}
		if tool.Function.Name != want[i] {
			t.Fatalf("tool %d name = %q, want %q", i, tool.Function.Name, want[i])
		}
		if tool.Function.Description == "" {
			t.Fatalf("tool %d has no description", i)
		}
		params := tool.Function.Parameters
		if params["type"] != "object" {
			t.Fatalf("tool %d parameters type = %v", i, params["type"])
		}
		if params["additionalProperties"] != false {
			t.Fatalf("tool %d additionalProperties = %v", i, params["additionalProperties"])
		}
		required, ok := params["required"].([]any)
		if !ok || len(required) != 1 || required[0] != "code" {
			t.Fatalf("tool %d required = %v", i, params["required"])
		}
		props, ok := params["properties"].(map[string]any)
		if !ok {
			t.Fatalf("tool %d properties = %T", i, params["properties"])
		}
		for _, field := range []string{"code", "filename", "timeout_ms", "max_output_bytes"} {
			if _, ok := props[field]; !ok {
				t.Fatalf("tool %d missing property %q", i, field)
			}
		}
	}
}

func TestConversationJSONContract(t *testing.T) {
	res := ConversationResult{
		Status:   StatusCompleted,
		Response: "ok",
		Turns:    1,
		ToolResults: []ConversationToolResult{{
			ToolCallID: "call_1",
			ToolName:   ToolPython,
			IsError:    false,
			Truncated:  false,
			Result: func() Result {
				r := Result{Language: LanguagePython, Status: StatusCompleted}
				r.SetValue(nil)
				return r
			}(),
		}},
		Usage:    ModelUsage{InputTokens: 1, OutputTokens: 2, TotalTokens: 3},
		Duration: time.Millisecond,
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(raw)
	for _, want := range []string{
		`"status":"completed"`,
		`"response":"ok"`,
		`"turns":1`,
		`"toolResults":[`,
		`"toolCallId":"call_1"`,
		`"toolName":"python_repl"`,
		`"isError":false`,
		`"truncated":false`,
		`"result":{`,
		`"value":null`,
		`"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}`,
		`"duration":1000000`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("JSON missing %s: %s", want, got)
		}
	}
	if strings.Contains(got, `"messages"`) {
		t.Fatalf("messages present without IncludeTranscript: %s", got)
	}
}

func TestConversationUsageAggregation(t *testing.T) {
	model := &fakeModel{responses: []ModelResponse{
		{
			Message:      Message{Role: "assistant", Content: "one"},
			FinishReason: "stop",
			Usage:        ModelUsage{InputTokens: 10, OutputTokens: 2, TotalTokens: 12},
		},
	}}
	exec := &fakeExecutor{cfg: testExecConfig()}
	conv := newTestConversation(t, model, exec, nil)
	res := conv.Run(context.Background(), ConversationRequest{Prompt: "run"})
	if res.Usage != (ModelUsage{InputTokens: 10, OutputTokens: 2, TotalTokens: 12}) {
		t.Fatalf("usage = %+v", res.Usage)
	}
}

func TestConversationToolOrder(t *testing.T) {
	model := &fakeModel{responses: []ModelResponse{{
		Message: Message{
			Role: "assistant",
			ToolCalls: []ToolCall{
				{ID: "a", Type: "function", Function: ToolFunction{Name: ToolPython, Arguments: `{"code":"a"}`}},
				{ID: "b", Type: "function", Function: ToolFunction{Name: ToolPython, Arguments: `{"code":"b"}`}},
				{ID: "c", Type: "function", Function: ToolFunction{Name: ToolPython, Arguments: `{"code":"c"}`}},
			},
		},
		FinishReason: "tool_calls",
	}, finalResponse("ok")}}
	exec := &fakeExecutor{cfg: testExecConfig()}
	conv := newTestConversation(t, model, exec, nil)

	res := conv.Run(context.Background(), ConversationRequest{Prompt: "run"})
	if res.Status != StatusCompleted {
		t.Fatalf("status = %q", res.Status)
	}
	if exec.requestCount() != 3 {
		t.Fatalf("executor calls = %d", exec.requestCount())
	}
	for i, want := range []string{"a", "b", "c"} {
		if got := exec.request(i).Source; got != want {
			t.Fatalf("request %d source = %q, want %q", i, got, want)
		}
	}
}

func TestConversationMaxTokensRequest(t *testing.T) {
	model := &fakeModel{responses: []ModelResponse{finalResponse("ok")}}
	exec := &fakeExecutor{cfg: testExecConfig()}
	conv := newTestConversation(t, model, exec, func(c *ConversationConfig) {
		c.MaxTokens = 1234
	})
	conv.Run(context.Background(), ConversationRequest{Prompt: "run"})
	if got := model.call(0).MaxTokens; got != 1234 {
		t.Fatalf("MaxTokens = %d", got)
	}
}
