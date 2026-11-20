package openaicompatible

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/keejkrej/script-agent"
)

// Client implements scriptagent.Model.
var _ scriptagent.Model = (*Client)(nil)

type wireRequest struct {
	Model     string                  `json:"model"`
	Messages  []scriptagent.Message   `json:"messages"`
	Tools     []scriptagent.ModelTool `json:"tools,omitempty"`
	MaxTokens int                     `json:"max_tokens"`
	Stream    bool                    `json:"stream"`
}

type wireResponse struct {
	ID      string       `json:"id"`
	Object  string       `json:"object"`
	Created int64        `json:"created"`
	Model   string       `json:"model"`
	Choices []wireChoice `json:"choices"`
	Usage   *wireUsage   `json:"usage"`
}

type wireChoice struct {
	Index        int         `json:"index"`
	Message      wireMessage `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

type wireMessage struct {
	Role      string          `json:"role"`
	Content   json.RawMessage `json:"content"`
	ToolCalls []wireToolCall  `json:"tool_calls"`
}

type wireToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function wireFunction `json:"function"`
}

type wireFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type wireUsage struct {
	PromptTokens     *int64 `json:"prompt_tokens"`
	CompletionTokens *int64 `json:"completion_tokens"`
	TotalTokens      *int64 `json:"total_tokens"`
}

// Complete sends one non-streaming Chat Completions request and returns the
// single assistant message.
//
// Complete validates the request before any network I/O. A non-2xx status,
// a redirect, a non-JSON content type, an oversized body, malformed JSON, or
// a response that does not contain exactly one assistant choice is an error.
func (c *Client) Complete(ctx context.Context, req scriptagent.ModelRequest) (scriptagent.ModelResponse, error) {
	if ctx == nil {
		return scriptagent.ModelResponse{}, protocolError("context must not be nil")
	}
	if err := validateRequest(req); err != nil {
		return scriptagent.ModelResponse{}, err
	}

	body, err := json.Marshal(wireRequest{
		Model:     req.Model,
		Messages:  req.Messages,
		Tools:     req.Tools,
		MaxTokens: req.MaxTokens,
		Stream:    false,
	})
	if err != nil {
		return scriptagent.ModelResponse{}, protocolError("cannot encode request")
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return scriptagent.ModelResponse{}, &Error{Kind: ErrorTransport, Message: "cannot build request", Err: err}
	}
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		return scriptagent.ModelResponse{}, transportError(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		drain(resp.Body)
		return scriptagent.ModelResponse{}, statusError(resp.StatusCode)
	}

	if err := requireJSONContentType(resp.Header.Get("Content-Type")); err != nil {
		drain(resp.Body)
		return scriptagent.ModelResponse{}, err
	}

	if resp.ContentLength > MaxResponseBytes {
		drain(resp.Body)
		return scriptagent.ModelResponse{}, protocolError("response body exceeds 4 MiB")
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes+1))
	if err != nil {
		return scriptagent.ModelResponse{}, transportError(err)
	}
	if len(data) > MaxResponseBytes {
		return scriptagent.ModelResponse{}, protocolError("response body exceeds 4 MiB")
	}

	return decodeResponse(data)
}

func validateRequest(req scriptagent.ModelRequest) error {
	if strings.TrimSpace(req.Model) == "" {
		return protocolError("request model must not be empty")
	}
	if req.MaxTokens < 0 {
		return protocolError("request max_tokens must not be negative")
	}
	for i, tool := range req.Tools {
		raw, err := json.Marshal(tool)
		if err != nil {
			return protocolError("tool %d cannot be encoded", i)
		}
		var shape struct {
			Type     string `json:"type"`
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		}
		if err := json.Unmarshal(raw, &shape); err != nil {
			return protocolError("tool %d is malformed", i)
		}
		if shape.Type != "function" {
			return protocolError("tool %d must have type function", i)
		}
		if strings.TrimSpace(shape.Function.Name) == "" {
			return protocolError("tool %d must have a function name", i)
		}
	}
	return nil
}

func decodeResponse(data []byte) (scriptagent.ModelResponse, error) {
	var wire wireResponse
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&wire); err != nil {
		return scriptagent.ModelResponse{}, protocolError("malformed JSON response")
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); err != io.EOF {
		return scriptagent.ModelResponse{}, protocolError("trailing data after JSON response")
	}

	if len(wire.Choices) != 1 {
		return scriptagent.ModelResponse{}, protocolError("expected exactly one choice, got %d", len(wire.Choices))
	}
	choice := wire.Choices[0]
	if choice.Message.Role != "assistant" {
		return scriptagent.ModelResponse{}, protocolError("choice message role must be assistant")
	}

	content, err := decodeContent(choice.Message.Content)
	if err != nil {
		return scriptagent.ModelResponse{}, err
	}
	calls, err := decodeToolCalls(choice.Message.ToolCalls)
	if err != nil {
		return scriptagent.ModelResponse{}, err
	}

	if !validFinishReason(choice.FinishReason) {
		return scriptagent.ModelResponse{}, protocolError("unsupported finish_reason")
	}
	if choice.FinishReason == "tool_calls" && len(calls) == 0 {
		return scriptagent.ModelResponse{}, protocolError("finish_reason tool_calls without tool calls")
	}

	usage, err := decodeUsage(wire.Usage)
	if err != nil {
		return scriptagent.ModelResponse{}, err
	}

	return scriptagent.ModelResponse{
		Message: scriptagent.Message{
			Role:      "assistant",
			Content:   content,
			ToolCalls: calls,
		},
		FinishReason: choice.FinishReason,
		Usage:        usage,
	}, nil
}

func decodeContent(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", protocolError("malformed message content")
	}
	if _, ok := v.([]any); ok {
		return "", protocolError("multimodal message content is not supported")
	}
	return "", protocolError("message content must be a string or null")
}

func decodeToolCalls(raw []wireToolCall) ([]scriptagent.ToolCall, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(raw))
	out := make([]scriptagent.ToolCall, 0, len(raw))
	for i, call := range raw {
		if strings.TrimSpace(call.ID) == "" {
			return nil, protocolError("tool call %d is missing an id", i)
		}
		if _, ok := seen[call.ID]; ok {
			return nil, protocolError("duplicate tool call id")
		}
		seen[call.ID] = struct{}{}
		if call.Type != "function" {
			return nil, protocolError("tool call %d must have type function", i)
		}
		if strings.TrimSpace(call.Function.Name) == "" {
			return nil, protocolError("tool call %d is missing a function name", i)
		}
		out = append(out, scriptagent.ToolCall{
			ID:   call.ID,
			Type: "function",
			Function: scriptagent.ToolFunction{
				Name:      call.Function.Name,
				Arguments: call.Function.Arguments,
			},
		})
	}
	return out, nil
}

func decodeUsage(u *wireUsage) (scriptagent.ModelUsage, error) {
	if u == nil {
		return scriptagent.ModelUsage{}, nil
	}
	prompt, err := tokenCount(u.PromptTokens, "prompt_tokens")
	if err != nil {
		return scriptagent.ModelUsage{}, err
	}
	completion, err := tokenCount(u.CompletionTokens, "completion_tokens")
	if err != nil {
		return scriptagent.ModelUsage{}, err
	}
	total, err := tokenCount(u.TotalTokens, "total_tokens")
	if err != nil {
		return scriptagent.ModelUsage{}, err
	}
	return scriptagent.ModelUsage{
		InputTokens:  prompt,
		OutputTokens: completion,
		TotalTokens:  total,
	}, nil
}

func tokenCount(v *int64, name string) (int, error) {
	if v == nil {
		return 0, nil
	}
	if *v < 0 {
		return 0, protocolError("%s must not be negative", name)
	}
	maxInt := int64(^uint(0) >> 1)
	if *v > maxInt {
		return 0, protocolError("%s is too large", name)
	}
	return int(*v), nil
}

func validFinishReason(s string) bool {
	switch s {
	case "stop", "tool_calls", "length", "content_filter":
		return true
	default:
		return false
	}
}

func requireJSONContentType(header string) error {
	if header == "" {
		return protocolError("response is missing Content-Type")
	}
	mediaType, _, err := mime.ParseMediaType(header)
	if err != nil {
		return protocolError("response has an invalid Content-Type")
	}
	if !strings.EqualFold(mediaType, "application/json") {
		return protocolError("response Content-Type must be application/json")
	}
	return nil
}

func transportError(err error) *Error {
	message := "request failed"
	switch {
	case errors.Is(err, context.Canceled):
		message = "request cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		message = "request deadline exceeded"
	}
	return &Error{Kind: ErrorTransport, Message: message, Err: err}
}

func drain(r io.Reader) {
	_, _ = io.Copy(io.Discard, io.LimitReader(r, 4096))
}

// String returns a short description without the endpoint or credentials.
func (c *Client) String() string {
	if c == nil {
		return "openaicompatible.Client(<nil>)"
	}
	return fmt.Sprintf("openaicompatible.Client(%s)", c.endpoint.Host)
}
