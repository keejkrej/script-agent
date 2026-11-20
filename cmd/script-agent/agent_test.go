package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/keejkrej/script-agent"
	"github.com/keejkrej/script-agent/internal/openaicompatible"
)

// fakeModel is a scripted scriptagent.Model. It records every request so a
// test can assert the exact model id, token cap, and transcript.
type fakeModel struct {
	responses []scriptagent.ModelResponse
	requests  []scriptagent.ModelRequest
}

func (m *fakeModel) Complete(_ context.Context, req scriptagent.ModelRequest) (scriptagent.ModelResponse, error) {
	m.requests = append(m.requests, req)
	if len(m.requests) > len(m.responses) {
		return scriptagent.ModelResponse{}, errors.New("fake model: no more responses")
	}
	return m.responses[len(m.requests)-1], nil
}

// withFakeModel replaces the provider factory for one test.
func withFakeModel(t *testing.T, m scriptagent.Model) {
	t.Helper()
	old := newAgentModel
	newAgentModel = func(openaicompatible.Config) (scriptagent.Model, error) { return m, nil }
	t.Cleanup(func() { newAgentModel = old })
}

func decodeConversationResult(t *testing.T, raw string) scriptagent.ConversationResult {
	t.Helper()
	var res scriptagent.ConversationResult
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		t.Fatalf("decode conversation result %q: %v", raw, err)
	}
	return res
}

// agentArgs returns a complete valid argument list. Callers append overrides.
func agentArgs(ws string) []string {
	return []string{
		"agent",
		"--prompt", "inspect the workspace",
		"--workspace", ws,
		"--provider", "openai-compatible",
		"--endpoint", "https://example.invalid/v1/chat/completions",
		"--model", "test-model",
		"--api-key-env", "SCRIPT_AGENT_TEST_KEY",
	}
}

func TestAgentRequiresFlags(t *testing.T) {
	ws := t.TempDir()
	cases := []struct {
		name string
		args []string
	}{
		{"no prompt", []string{"agent", "--workspace", ws, "--provider", "openai-compatible", "--endpoint", "https://example.invalid/v1/chat/completions", "--model", "m", "--api-key-env", "K"}},
		{"blank prompt", []string{"agent", "--prompt", "   ", "--workspace", ws, "--provider", "openai-compatible", "--endpoint", "https://example.invalid/v1/chat/completions", "--model", "m", "--api-key-env", "K"}},
		{"no workspace", []string{"agent", "--prompt", "p", "--provider", "openai-compatible", "--endpoint", "https://example.invalid/v1/chat/completions", "--model", "m", "--api-key-env", "K"}},
		{"no provider", []string{"agent", "--prompt", "p", "--workspace", ws, "--endpoint", "https://example.invalid/v1/chat/completions", "--model", "m", "--api-key-env", "K"}},
		{"wrong provider", []string{"agent", "--prompt", "p", "--workspace", ws, "--provider", "mock", "--endpoint", "https://example.invalid/v1/chat/completions", "--model", "m", "--api-key-env", "K"}},
		{"no endpoint", []string{"agent", "--prompt", "p", "--workspace", ws, "--provider", "openai-compatible", "--model", "m", "--api-key-env", "K"}},
		{"no model", []string{"agent", "--prompt", "p", "--workspace", ws, "--provider", "openai-compatible", "--endpoint", "https://example.invalid/v1/chat/completions", "--api-key-env", "K"}},
		{"no api key env", []string{"agent", "--prompt", "p", "--workspace", ws, "--provider", "openai-compatible", "--endpoint", "https://example.invalid/v1/chat/completions", "--model", "m"}},
		{"positional", append(agentArgs(ws), "extra")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, _ := runCLI(t, tc.args...)
			if code != exitInvalid {
				t.Fatalf("exit = %d, want %d", code, exitInvalid)
			}
			res := decodeConversationResult(t, stdout)
			if res.Status != scriptagent.StatusInvalidRequest {
				t.Fatalf("status = %q, want invalid_request", res.Status)
			}
			if res.Error == nil || res.Error.Name != "InvalidRequest" {
				t.Fatalf("error = %+v", res.Error)
			}
		})
	}
}

func TestAgentWriteReadonlyExclusive(t *testing.T) {
	ws := t.TempDir()
	args := append(agentArgs(ws), "--write", "--readonly")
	code, stdout, _ := runCLI(t, args...)
	if code != exitInvalid {
		t.Fatalf("exit = %d, want %d", code, exitInvalid)
	}
	res := decodeConversationResult(t, stdout)
	if res.Error == nil || res.Error.Code != "readonly" {
		t.Fatalf("error = %+v", res.Error)
	}
}

func TestAgentNumericLimits(t *testing.T) {
	ws := t.TempDir()
	cases := []struct {
		name string
		flag string
		val  string
	}{
		{"zero timeout", "--timeout", "0s"},
		{"negative timeout", "--timeout", "-1s"},
		{"zero source", "--max-source-bytes", "0"},
		{"huge source", "--max-source-bytes", "268435457"},
		{"zero output", "--max-output-bytes", "0"},
		{"huge output", "--max-output-bytes", "1073741825"},
		{"zero turns", "--max-turns", "0"},
		{"huge turns", "--max-turns", "33"},
		{"zero tokens", "--max-tokens", "0"},
		{"huge tokens", "--max-tokens", "32769"},
		{"zero tool calls", "--max-tool-calls", "0"},
		{"huge tool calls", "--max-tool-calls", "65"},
		{"zero tool result", "--max-tool-result-bytes", "0"},
		{"huge tool result", "--max-tool-result-bytes", "1048577"},
		{"zero history", "--max-history-bytes", "0"},
		{"huge history", "--max-history-bytes", "67108865"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := append(agentArgs(ws), tc.flag, tc.val)
			code, stdout, _ := runCLI(t, args...)
			if code != exitInvalid {
				t.Fatalf("exit = %d, want %d", code, exitInvalid)
			}
			res := decodeConversationResult(t, stdout)
			if res.Status != scriptagent.StatusInvalidRequest {
				t.Fatalf("status = %q", res.Status)
			}
		})
	}
}

func TestAgentProviderConfigError(t *testing.T) {
	ws := t.TempDir()
	// Plain HTTP is rejected by the provider before any network I/O.
	args := []string{
		"agent", "--prompt", "p", "--workspace", ws,
		"--provider", "openai-compatible",
		"--endpoint", "http://example.invalid/v1/chat/completions",
		"--model", "m", "--api-key-env", "SCRIPT_AGENT_TEST_KEY",
	}
	code, stdout, _ := runCLI(t, args...)
	if code != exitInvalid {
		t.Fatalf("exit = %d, want %d", code, exitInvalid)
	}
	res := decodeConversationResult(t, stdout)
	if res.Error == nil || res.Error.Code != "provider" {
		t.Fatalf("error = %+v", res.Error)
	}
}

func TestAgentMissingAPIKeyEnv(t *testing.T) {
	ws := t.TempDir()
	args := []string{
		"agent", "--prompt", "p", "--workspace", ws,
		"--provider", "openai-compatible",
		"--endpoint", "https://example.invalid/v1/chat/completions",
		"--model", "m", "--api-key-env", "SCRIPT_AGENT_TEST_MISSING_KEY_XYZ",
	}
	code, stdout, _ := runCLI(t, args...)
	if code != exitInvalid {
		t.Fatalf("exit = %d, want %d", code, exitInvalid)
	}
	res := decodeConversationResult(t, stdout)
	if res.Error == nil || res.Error.Code != "provider" {
		t.Fatalf("error = %+v", res.Error)
	}
}

func TestAgentSuccess(t *testing.T) {
	ws := t.TempDir()
	model := &fakeModel{responses: []scriptagent.ModelResponse{{
		Message:      scriptagent.Message{Role: "assistant", Content: "done"},
		FinishReason: "stop",
	}}}
	withFakeModel(t, model)

	code, stdout, stderr := runCLI(t, agentArgs(ws)...)
	if code != exitOK {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	res := decodeConversationResult(t, stdout)
	if res.Status != scriptagent.StatusCompleted {
		t.Fatalf("status = %q, error = %+v", res.Status, res.Error)
	}
	if res.Response != "done" {
		t.Fatalf("response = %q", res.Response)
	}
	if res.Turns != 1 {
		t.Fatalf("turns = %d", res.Turns)
	}
	if len(res.Messages) != 0 {
		t.Fatalf("messages present without --transcript: %+v", res.Messages)
	}
	if len(model.requests) != 1 {
		t.Fatalf("model calls = %d", len(model.requests))
	}
	req := model.requests[0]
	if req.Model != "test-model" {
		t.Fatalf("model id = %q", req.Model)
	}
	if req.MaxTokens != scriptagent.DefaultMaxTokens {
		t.Fatalf("max tokens = %d", req.MaxTokens)
	}
	if len(req.Tools) != 3 {
		t.Fatalf("tools = %d", len(req.Tools))
	}
	if req.Messages[0].Role != "system" || req.Messages[1].Role != "user" {
		t.Fatalf("messages = %+v", req.Messages)
	}
}

func TestAgentTranscript(t *testing.T) {
	ws := t.TempDir()
	model := &fakeModel{responses: []scriptagent.ModelResponse{{
		Message:      scriptagent.Message{Role: "assistant", Content: "done"},
		FinishReason: "stop",
	}}}
	withFakeModel(t, model)

	args := append(agentArgs(ws), "--transcript")
	code, stdout, stderr := runCLI(t, args...)
	if code != exitOK {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	res := decodeConversationResult(t, stdout)
	if len(res.Messages) != 3 {
		t.Fatalf("messages = %d, want 3", len(res.Messages))
	}
	if res.Messages[0].Role != "system" || res.Messages[1].Role != "user" || res.Messages[2].Role != "assistant" {
		t.Fatalf("roles = %+v", res.Messages)
	}
	if !strings.Contains(res.Messages[0].Content, "scripting agent") {
		t.Fatalf("system prompt = %q", res.Messages[0].Content)
	}
}

func TestAgentProviderFailureIsFailed(t *testing.T) {
	ws := t.TempDir()
	model := &fakeModel{responses: nil}
	withFakeModel(t, model)

	code, stdout, _ := runCLI(t, agentArgs(ws)...)
	if code != exitFailed {
		t.Fatalf("exit = %d, want %d", code, exitFailed)
	}
	res := decodeConversationResult(t, stdout)
	if res.Status != scriptagent.StatusFailed {
		t.Fatalf("status = %q", res.Status)
	}
	if res.Error == nil || res.Error.Name != "ProviderError" {
		t.Fatalf("error = %+v", res.Error)
	}
}
