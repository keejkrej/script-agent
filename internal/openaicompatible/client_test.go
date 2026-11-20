package openaicompatible

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/keejkrej/script-agent"
)

const testAPIKey = "sk-test-secret-value"

const validResponse = `{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"test-model","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`

func newTestClient(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewTLSServer(handler)
	t.Cleanup(srv.Close)
	caPath := writeServerCert(t, srv)
	t.Setenv("OPENAI_COMPATIBLE_TEST_KEY", testAPIKey)
	c, err := New(Config{
		Endpoint:  srv.URL + "/v1/chat/completions",
		APIKeyEnv: "OPENAI_COMPATIBLE_TEST_KEY",
		CAFile:    caPath,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c, srv
}

func writeServerCert(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	cert := srv.Certificate()
	if cert == nil {
		t.Fatal("server certificate unavailable")
	}
	path := filepath.Join(t.TempDir(), "ca.pem")
	data := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write CA file: %v", err)
	}
	return path
}

func baseRequest() scriptagent.ModelRequest {
	return scriptagent.ModelRequest{
		Model:     "test-model",
		Messages:  []scriptagent.Message{{Role: "user", Content: "hello"}},
		MaxTokens: 123,
	}
}

func toolsFromJSON(t *testing.T, raw string) []scriptagent.ModelTool {
	t.Helper()
	var tools []scriptagent.ModelTool
	if err := json.Unmarshal([]byte(raw), &tools); err != nil {
		t.Fatalf("decode tools: %v", err)
	}
	return tools
}

func TestCompleteSendsExactRequest(t *testing.T) {
	var got map[string]json.RawMessage
	var auth, contentType, accept string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		contentType = r.Header.Get("Content-Type")
		accept = r.Header.Get("Accept")
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		if strings.Contains(r.URL.String(), testAPIKey) {
			t.Errorf("API key appeared in the request URL")
		}
		if bytes.Contains(body, []byte(testAPIKey)) {
			t.Errorf("API key appeared in the request body")
		}
		if err := json.Unmarshal(body, &got); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, validResponse)
	})

	req := baseRequest()
	req.Tools = toolsFromJSON(t, `[{"type":"function","function":{"name":"python_repl","description":"run python","parameters":{"type":"object","properties":{"code":{"type":"string"}},"required":["code"],"additionalProperties":false}}}]`)

	resp, err := c.Complete(context.Background(), req)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Message.Content != "ok" {
		t.Fatalf("content = %q, want ok", resp.Message.Content)
	}
	if auth != "Bearer "+testAPIKey {
		t.Fatalf("Authorization = %q", auth)
	}
	if contentType != "application/json" {
		t.Fatalf("Content-Type = %q", contentType)
	}
	if accept != "application/json" {
		t.Fatalf("Accept = %q", accept)
	}

	wantKeys := []string{"model", "messages", "tools", "max_tokens", "stream"}
	if len(got) != len(wantKeys) {
		t.Fatalf("request keys = %v, want exactly %v", mapKeys(got), wantKeys)
	}
	for _, key := range wantKeys {
		if _, ok := got[key]; !ok {
			t.Fatalf("request is missing %q", key)
		}
	}

	var model string
	if err := json.Unmarshal(got["model"], &model); err != nil || model != "test-model" {
		t.Fatalf("model = %q, err = %v", model, err)
	}
	var maxTokens int
	if err := json.Unmarshal(got["max_tokens"], &maxTokens); err != nil || maxTokens != 123 {
		t.Fatalf("max_tokens = %d, err = %v", maxTokens, err)
	}
	var stream bool
	if err := json.Unmarshal(got["stream"], &stream); err != nil || stream {
		t.Fatalf("stream = %v, err = %v", stream, err)
	}
	var messages []scriptagent.Message
	if err := json.Unmarshal(got["messages"], &messages); err != nil {
		t.Fatalf("messages: %v", err)
	}
	if len(messages) != 1 || messages[0].Role != "user" || messages[0].Content != "hello" {
		t.Fatalf("messages = %+v", messages)
	}
	var tools []scriptagent.ModelTool
	if err := json.Unmarshal(got["tools"], &tools); err != nil {
		t.Fatalf("tools: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("tools = %+v", tools)
	}
}

func mapKeys(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func TestCompleteParsesToolCalls(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"chatcmpl-2","object":"chat.completion","created":2,"model":"test-model",
			"choices":[{"index":0,"message":{"role":"assistant","content":null,"reasoning_content":"ignored","tool_calls":[
				{"id":"call_1","type":"function","function":{"name":"python_repl","arguments":"{\"code\":\"print(1)\"}"}},
				{"id":"call_2","type":"function","function":{"name":"bash","arguments":""}}
			]},"finish_reason":"tool_calls"}],
			"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15},
			"system_fingerprint":"fp_1","service_tier":"default"
		}`)
	})

	resp, err := c.Complete(context.Background(), baseRequest())
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Message.Role != "assistant" {
		t.Fatalf("role = %q", resp.Message.Role)
	}
	if resp.Message.Content != "" {
		t.Fatalf("content = %q, want empty", resp.Message.Content)
	}
	if len(resp.Message.ToolCalls) != 2 {
		t.Fatalf("tool calls = %+v", resp.Message.ToolCalls)
	}
	first := resp.Message.ToolCalls[0]
	if first.ID != "call_1" || first.Type != "function" || first.Function.Name != "python_repl" {
		t.Fatalf("first call = %+v", first)
	}
	if first.Function.Arguments != `{"code":"print(1)"}` {
		t.Fatalf("arguments = %q", first.Function.Arguments)
	}
	if resp.Message.ToolCalls[1].Function.Arguments != "" {
		t.Fatalf("empty arguments = %q", resp.Message.ToolCalls[1].Function.Arguments)
	}
	if resp.FinishReason != "tool_calls" {
		t.Fatalf("finish reason = %q", resp.FinishReason)
	}
	if resp.Usage.InputTokens != 10 || resp.Usage.OutputTokens != 5 || resp.Usage.TotalTokens != 15 {
		t.Fatalf("usage = %+v", resp.Usage)
	}
}

func TestCompleteRejectsInvalidChoices(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"error":{"message":"model_error `+testAPIKey+`"}}`)
	})

	_, err := c.Complete(context.Background(), baseRequest())
	if err == nil {
		t.Fatal("expected an error")
	}
	var e *Error
	if !errors.As(err, &e) || e.Kind != ErrorProtocol {
		t.Fatalf("error = %v, want protocol error", err)
	}
	if strings.Contains(fmt.Sprintf("%v", err), testAPIKey) {
		t.Fatal("error leaked the API key")
	}
}

func TestCompleteRejectsEventStream(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {}\n\n")
	})

	_, err := c.Complete(context.Background(), baseRequest())
	var e *Error
	if !errors.As(err, &e) || e.Kind != ErrorProtocol {
		t.Fatalf("error = %v, want protocol error", err)
	}
}

func TestCompleteRejectsUnauthorizedWithoutLeakingKey(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"bad key `+testAPIKey+`"}}`)
	})

	_, err := c.Complete(context.Background(), baseRequest())
	var e *Error
	if !errors.As(err, &e) || e.Kind != ErrorStatus || e.StatusCode != http.StatusUnauthorized {
		t.Fatalf("error = %v, want status 401", err)
	}
	if strings.Contains(fmt.Sprintf("%v", err), testAPIKey) {
		t.Fatal("error leaked the API key")
	}
}

func TestCompleteRejectsOversizedBody(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		chunk := bytes.Repeat([]byte(" "), 64<<10)
		for written := 0; written <= MaxResponseBytes; written += len(chunk) {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	})

	_, err := c.Complete(context.Background(), baseRequest())
	var e *Error
	if !errors.As(err, &e) || e.Kind != ErrorProtocol {
		t.Fatalf("error = %v, want protocol error", err)
	}
	if !strings.Contains(err.Error(), "4 MiB") {
		t.Fatalf("error = %v, want a size bound", err)
	}
}

func TestCompleteRejectsDeclaredOversizedBody(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", strconv.Itoa(MaxResponseBytes+1))
		_, _ = io.WriteString(w, "{}")
	})

	_, err := c.Complete(context.Background(), baseRequest())
	var e *Error
	if !errors.As(err, &e) || e.Kind != ErrorProtocol {
		t.Fatalf("error = %v, want protocol error", err)
	}
}

func TestCompleteDoesNotFollowRedirects(t *testing.T) {
	var hits int32
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) == 1 {
			http.Redirect(w, r, "/v1/chat/completions?redirected=1", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, validResponse)
	})

	_, err := c.Complete(context.Background(), baseRequest())
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("server hits = %d, want 1 (redirect must not be followed)", got)
	}
	var e *Error
	if !errors.As(err, &e) || e.Kind != ErrorStatus || e.StatusCode != http.StatusFound {
		t.Fatalf("error = %v, want status 302", err)
	}
}

func TestCompleteDoesNotForwardAuthorizationToRedirectTarget(t *testing.T) {
	var targetHits int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&targetHits, 1)
		if r.Header.Get("Authorization") != "" {
			t.Errorf("redirect target received an Authorization header")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, validResponse)
	}))
	t.Cleanup(target.Close)

	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/v1/chat/completions", http.StatusFound)
	})

	_, err := c.Complete(context.Background(), baseRequest())
	if got := atomic.LoadInt32(&targetHits); got != 0 {
		t.Fatalf("redirect target hits = %d, want 0", got)
	}
	var e *Error
	if !errors.As(err, &e) || e.Kind != ErrorStatus || e.StatusCode != http.StatusFound {
		t.Fatalf("error = %v, want status 302", err)
	}
}

func TestCompleteRejectsMalformedJSON(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"not json", `not json`},
		{"trailing value", `{"choices":[]} trailing`},
		{"second document", `{"choices":[]}{}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tc.body)
			})
			_, err := c.Complete(context.Background(), baseRequest())
			var e *Error
			if !errors.As(err, &e) || e.Kind != ErrorProtocol {
				t.Fatalf("error = %v, want protocol error", err)
			}
		})
	}
}

func TestCompletePropagatesCertificateError(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, validResponse)
	}))
	t.Cleanup(srv.Close)

	t.Setenv("OPENAI_COMPATIBLE_TEST_KEY", testAPIKey)
	c, err := New(Config{
		Endpoint:  srv.URL + "/v1/chat/completions",
		APIKeyEnv: "OPENAI_COMPATIBLE_TEST_KEY",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = c.Complete(context.Background(), baseRequest())
	if err == nil {
		t.Fatal("expected a certificate error")
	}
	var certErr *tls.CertificateVerificationError
	if !errors.As(err, &certErr) {
		t.Fatalf("error = %v, want a certificate verification error", err)
	}
}

func TestCompleteIgnoresProxyEnvironment(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "://bad")
	t.Setenv("HTTP_PROXY", "://bad")
	t.Setenv("NO_PROXY", "")

	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, validResponse)
	})

	if _, err := c.Complete(context.Background(), baseRequest()); err != nil {
		t.Fatalf("Complete with poisoned proxy environment: %v", err)
	}
}

func TestCompleteHonorsContextDeadline(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, validResponse)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := c.Complete(ctx, baseRequest())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context deadline exceeded", err)
	}
}

func TestCompleteHonorsHTTPTimeout(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, validResponse)
	}))
	t.Cleanup(srv.Close)

	t.Setenv("OPENAI_COMPATIBLE_TEST_KEY", testAPIKey)
	c, err := New(Config{
		Endpoint:    srv.URL + "/v1/chat/completions",
		APIKeyEnv:   "OPENAI_COMPATIBLE_TEST_KEY",
		CAFile:      writeServerCert(t, srv),
		HTTPTimeout: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = c.Complete(context.Background(), baseRequest())
	if err == nil {
		t.Fatal("expected a timeout")
	}
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("error = %v, want a timeout", err)
	}
}

func TestCompleteValidatesBeforeHTTP(t *testing.T) {
	var hits int32
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, validResponse)
	})

	cases := []struct {
		name string
		req  scriptagent.ModelRequest
	}{
		{"empty model", scriptagent.ModelRequest{Messages: []scriptagent.Message{{Role: "user", Content: "x"}}}},
		{"negative max tokens", scriptagent.ModelRequest{Model: "m", MaxTokens: -1}},
		{"wrong tool type", scriptagent.ModelRequest{Model: "m", Tools: toolsFromJSON(t, `[{"type":"other","function":{"name":"x"}}]`)}},
		{"empty tool name", scriptagent.ModelRequest{Model: "m", Tools: toolsFromJSON(t, `[{"type":"function","function":{"name":""}}]`)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := c.Complete(context.Background(), tc.req); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Fatalf("server hits = %d, want 0", got)
	}
}

func TestCompleteAcceptsOther2xx(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, validResponse)
	})

	if _, err := c.Complete(context.Background(), baseRequest()); err != nil {
		t.Fatalf("Complete: %v", err)
	}
}

func TestCompleteContentType(t *testing.T) {
	cases := []struct {
		name string
		ct   string
		ok   bool
	}{
		{"json", "application/json", true},
		{"json charset", "application/json; charset=utf-8", true},
		{"event stream", "text/event-stream", false},
		{"json patch", "application/json-patch+json", false},
		{"missing", "", false},
		{"text", "text/plain", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if tc.ct != "" {
					w.Header().Set("Content-Type", tc.ct)
				}
				_, _ = io.WriteString(w, validResponse)
			})
			_, err := c.Complete(context.Background(), baseRequest())
			if tc.ok && err != nil {
				t.Fatalf("Complete: %v", err)
			}
			if !tc.ok {
				var e *Error
				if !errors.As(err, &e) || e.Kind != ErrorProtocol {
					t.Fatalf("error = %v, want protocol error", err)
				}
			}
		})
	}
}

func TestCompleteRejectsBadResponseShapes(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"zero choices", `{"choices":[]}`},
		{"two choices", `{"choices":[{"message":{"role":"assistant","content":"a"},"finish_reason":"stop"},{"message":{"role":"assistant","content":"b"},"finish_reason":"stop"}]}`},
		{"wrong role", `{"choices":[{"message":{"role":"user","content":"a"},"finish_reason":"stop"}]}`},
		{"array content", `{"choices":[{"message":{"role":"assistant","content":[{"type":"text","text":"a"}]},"finish_reason":"stop"}]}`},
		{"number content", `{"choices":[{"message":{"role":"assistant","content":1},"finish_reason":"stop"}]}`},
		{"duplicate ids", `{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"x","type":"function","function":{"name":"a","arguments":""}},{"id":"x","type":"function","function":{"name":"b","arguments":""}}]},"finish_reason":"tool_calls"}]}`},
		{"missing id", `{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"type":"function","function":{"name":"a","arguments":""}}]},"finish_reason":"tool_calls"}]}`},
		{"wrong tool type", `{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"x","type":"other","function":{"name":"a","arguments":""}}]},"finish_reason":"tool_calls"}]}`},
		{"missing function name", `{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"x","type":"function","function":{"name":"","arguments":""}}]},"finish_reason":"tool_calls"}]}`},
		{"tool_calls without calls", `{"choices":[{"message":{"role":"assistant","content":"a"},"finish_reason":"tool_calls"}]}`},
		{"unknown finish", `{"choices":[{"message":{"role":"assistant","content":"a"},"finish_reason":"weird"}]}`},
		{"negative usage", `{"choices":[{"message":{"role":"assistant","content":"a"},"finish_reason":"stop"}],"usage":{"prompt_tokens":-1}}`},
		{"usage wrong type", `{"choices":[{"message":{"role":"assistant","content":"a"},"finish_reason":"stop"}],"usage":"x"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tc.body)
			})
			_, err := c.Complete(context.Background(), baseRequest())
			var e *Error
			if !errors.As(err, &e) || e.Kind != ErrorProtocol {
				t.Fatalf("error = %v, want protocol error", err)
			}
		})
	}
}

func TestCompleteAcceptsLengthFinish(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"partial"},"finish_reason":"length"}]}`)
	})

	resp, err := c.Complete(context.Background(), baseRequest())
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.FinishReason != "length" {
		t.Fatalf("finish reason = %q", resp.FinishReason)
	}
}

func TestCompleteAllowsMissingUsage(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	})

	resp, err := c.Complete(context.Background(), baseRequest())
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Usage != (scriptagent.ModelUsage{}) {
		t.Fatalf("usage = %+v, want zero", resp.Usage)
	}
}

func TestClientStringDoesNotLeakKey(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, validResponse)
	})
	if strings.Contains(c.String(), testAPIKey) {
		t.Fatal("String leaked the API key")
	}
}
