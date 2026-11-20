package scriptagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Conversation tool names. They are the only tools a Conversation exposes to
// the model, and each maps to one executor language.
const (
	ToolPython     = "python_repl"
	ToolJavaScript = "javascript_repl"
	ToolBash       = "bash_repl"
)

// Conversation limits. A zero ConversationConfig field selects the default;
// a value above the limit is rejected by NewConversation.
const (
	DefaultMaxTurns           = 8
	MaxTurnsLimit             = 32
	DefaultMaxTokens          = 4096
	MaxTokensLimit            = 32768
	DefaultMaxToolCalls       = 16
	MaxToolCallsLimit         = 64
	DefaultMaxToolResultBytes = 64 << 10 // 64 KiB
	MaxToolResultBytesLimit   = 1 << 20  // 1 MiB
	DefaultMaxHistoryBytes    = 4 << 20  // 4 MiB
	MaxHistoryBytesLimit      = 64 << 20 // 64 MiB
)

// maxUsageTokens bounds a single provider usage report. A provider that
// reports a larger count is treated as protocol-invalid rather than allowed to
// poison the aggregate.
const maxUsageTokens = int64(1) << 40

// maxToolCallIDBytes bounds a provider tool-call id so a malformed response
// cannot inflate the tool message envelope.
const maxToolCallIDBytes = 1024

// maxToolNameBytes bounds the tool name echoed in a tool result envelope.
const maxToolNameBytes = 255

// Model is the provider boundary for a Conversation. Complete performs one
// completion. Implementations must honor ctx and must not retain or mutate the
// request.
type Model interface {
	Complete(context.Context, ModelRequest) (ModelResponse, error)
}

// ModelRequest is one completion request. It uses the structured OpenAI shape:
// a model id, the full message transcript, the tool definitions, and a
// completion-token cap.
type ModelRequest struct {
	Model     string      `json:"model"`
	Messages  []Message   `json:"messages"`
	Tools     []ModelTool `json:"tools,omitempty"`
	MaxTokens int         `json:"max_tokens"`
}

// ModelResponse is one completion response. A provider returns exactly one
// assistant message.
type ModelResponse struct {
	Message      Message    `json:"message"`
	FinishReason string     `json:"finish_reason"`
	Usage        ModelUsage `json:"usage"`
}

// ModelUsage is the token accounting for one completion.
type ModelUsage struct {
	InputTokens  int `json:"prompt_tokens"`
	OutputTokens int `json:"completion_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

// Message is one transcript message. Role is "system", "user", "assistant", or
// "tool". Content is always a string. ToolCalls is set on an assistant message
// that requests tools. ToolCallID and Name are set on a tool message.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
}

// ToolCall is one function call requested by the model.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

// ToolFunction is the callable part of a ToolCall. Arguments is the raw
// JSON object string supplied by the model.
type ToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ModelTool is one tool definition in the OpenAI function-tool shape.
type ModelTool struct {
	Type     string        `json:"type"`
	Function ModelFunction `json:"function"`
}

// ModelFunction describes one callable tool.
type ModelFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// ScriptExecutor is the execution boundary a Conversation drives. *Agent
// satisfies it. A Conversation never closes the executor.
type ScriptExecutor interface {
	Run(ctx context.Context, req Request) Result
	Config() Config
}

// ConversationConfig configures a Conversation.
//
// Executor and Model are required. ModelID is the provider model identifier.
// SystemPrompt is the first transcript message; it may be empty.
//
// MaxTurns bounds model completions. MaxTokens is the per-completion token cap.
// MaxToolCalls bounds executed tool calls across the whole conversation.
// MaxToolResultBytes bounds the serialized tool result sent back to the model.
// MaxHistoryBytes bounds the serialized transcript before each completion.
//
// IncludeTranscript copies the final transcript into ConversationResult.
type ConversationConfig struct {
	Executor     ScriptExecutor
	Model        Model
	ModelID      string
	SystemPrompt string

	MaxTurns           int
	MaxTokens          int
	MaxToolCalls       int
	MaxToolResultBytes int
	MaxHistoryBytes    int

	IncludeTranscript bool
}

// ConversationRequest is one conversation turn request.
//
// Prompt is the user message. AllowProcess is the conversation-level Bash
// grant; the executor's Config.AllowProcess is still required, so Bash needs
// both grants.
type ConversationRequest struct {
	Prompt       string
	AllowProcess bool
}

// ConversationResult is the terminal outcome of one Conversation.Run.
//
// Status is the root Status. Response is the final assistant text for a
// completed conversation. Turns is the number of model completions.
// ToolResults is every tool result in execution order, including unknown-tool
// and invalid-argument feedback. Usage is the aggregate token usage. Messages
// is present only when IncludeTranscript is true. Error is set for a
// non-completed status. Duration is the wall-clock time spent in Run.
type ConversationResult struct {
	Status      Status                   `json:"status"`
	Response    string                   `json:"response"`
	Turns       int                      `json:"turns"`
	ToolResults []ConversationToolResult `json:"toolResults,omitempty"`
	Usage       ModelUsage               `json:"usage"`
	Messages    []Message                `json:"messages,omitempty"`
	Error       *Error                   `json:"error,omitempty"`
	Duration    time.Duration            `json:"duration"`
}

// MarshalJSON implements json.Marshaler. It uses the same no-HTML-escape
// encoder as Result so tool stacks and script output stay readable, and it
// preserves a present JSON null in a nested Result value.
func (r ConversationResult) MarshalJSON() ([]byte, error) {
	type alias ConversationResult
	return marshalNoEscape(alias(r))
}

// ConversationToolResult is one tool result. It is both the Go record in
// ConversationResult.ToolResults and the JSON object encoded into the tool
// message content sent back to the model.
type ConversationToolResult struct {
	ToolCallID string `json:"toolCallId"`
	ToolName   string `json:"toolName"`
	IsError    bool   `json:"isError"`
	Truncated  bool   `json:"truncated"`
	Result     Result `json:"result"`
}

// Conversation is a bounded, one-shot agent loop over a ScriptExecutor and a
// Model. It is immutable after NewConversation and safe for concurrent use:
// every Run owns a fresh transcript, usage total, and tool-result list.
type Conversation struct {
	cfg     ConversationConfig
	execCfg Config
	tools   []ModelTool
}

// NewConversation validates cfg and returns a Conversation. It validates the
// executor and model dependencies and the executor's Config boundaries before
// any completion or execution. It does not call the model or the executor.
func NewConversation(cfg ConversationConfig) (*Conversation, error) {
	resolved, err := resolveConversationConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &Conversation{
		cfg:     resolved,
		execCfg: resolved.Executor.Config(),
		tools:   buildConversationTools(),
	}, nil
}

// Config returns the resolved conversation configuration. It is a copy.
func (c *Conversation) Config() ConversationConfig { return c.cfg }

// Run executes one bounded conversation and returns its terminal result. It
// never returns a Go error: provider failures, protocol failures, limits,
// cancellations, and tool failures are ConversationResult statuses.
func (c *Conversation) Run(ctx context.Context, req ConversationRequest) ConversationResult {
	start := time.Now()
	res := c.run(ctx, req)
	res.Duration = time.Since(start)
	return res
}

func (c *Conversation) run(ctx context.Context, req ConversationRequest) ConversationResult {
	if ctx == nil {
		ctx = context.Background()
	}

	messages := make([]Message, 0, 8)
	messages = append(messages, Message{Role: "system", Content: c.cfg.SystemPrompt})
	messages = append(messages, Message{Role: "user", Content: req.Prompt})

	var toolResults []ConversationToolResult
	var usage ModelUsage
	turns := 0
	toolCalls := 0

	for {
		if err := ctx.Err(); err != nil {
			return c.terminate(ctxStatus(err), "", turns, toolResults, usage, messages, ctxError(err))
		}
		if turns >= c.cfg.MaxTurns {
			return c.terminate(StatusLimitExceeded, "", turns, toolResults, usage, messages,
				&Error{Name: "LimitExceeded", Code: "max_turns",
					Message: fmt.Sprintf("conversation reached the %d-turn limit", c.cfg.MaxTurns)})
		}
		if n, err := historyBytes(messages); err != nil {
			return c.terminate(StatusFailed, "", turns, toolResults, usage, messages,
				&Error{Name: "ProtocolError", Message: "could not encode conversation history"})
		} else if n > c.cfg.MaxHistoryBytes {
			return c.terminate(StatusLimitExceeded, "", turns, toolResults, usage, messages,
				&Error{Name: "LimitExceeded", Code: "max_history",
					Message: fmt.Sprintf("conversation history is %d bytes, limit is %d", n, c.cfg.MaxHistoryBytes)})
		}

		turns++
		resp, err := c.cfg.Model.Complete(ctx, ModelRequest{
			Model:     c.cfg.ModelID,
			Messages:  cloneMessages(messages),
			Tools:     cloneTools(c.tools),
			MaxTokens: c.cfg.MaxTokens,
		})
		if err != nil {
			if cerr := ctx.Err(); cerr != nil {
				return c.terminate(ctxStatus(cerr), "", turns, toolResults, usage, messages, ctxError(cerr))
			}
			// The provider error is intentionally not echoed: provider errors
			// can contain request payloads or credentials.
			return c.terminate(StatusFailed, "", turns, toolResults, usage, messages,
				&Error{Name: "ProviderError", Message: "model completion failed"})
		}
		if cerr := ctx.Err(); cerr != nil {
			return c.terminate(ctxStatus(cerr), "", turns, toolResults, usage, messages, ctxError(cerr))
		}
		if perr := validateModelResponse(resp, c.cfg.MaxTokens); perr != nil {
			return c.terminate(StatusFailed, "", turns, toolResults, usage, messages, perr)
		}
		if uerr := addUsage(&usage, resp.Usage, c.cfg.MaxTokens); uerr != nil {
			return c.terminate(StatusFailed, "", turns, toolResults, usage, messages, uerr)
		}
		messages = append(messages, resp.Message)

		if resp.FinishReason == "length" {
			return c.terminate(StatusLimitExceeded, "", turns, toolResults, usage, messages,
				&Error{Name: "LimitExceeded", Code: "length",
					Message: "model response was truncated by the token limit"})
		}
		if len(resp.Message.ToolCalls) == 0 {
			return c.terminate(StatusCompleted, resp.Message.Content, turns, toolResults, usage, messages, nil)
		}

		excess := false
		for _, call := range resp.Message.ToolCalls {
			if err := ctx.Err(); err != nil {
				return c.terminate(ctxStatus(err), "", turns, toolResults, usage, messages, ctxError(err))
			}
			var ctr ConversationToolResult
			if toolCalls >= c.cfg.MaxToolCalls {
				lang, _ := toolLanguage(call.Function.Name)
				ctr = c.errorToolResult(call, lang, "max_tool_calls", "tool call limit reached")
				excess = true
			} else {
				toolCalls++
				ctr = c.executeTool(ctx, call, req.AllowProcess)
			}
			toolResults = append(toolResults, ctr)
			msg, merr := toolMessage(ctr)
			if merr != nil {
				return c.terminate(StatusFailed, "", turns, toolResults, usage, messages,
					&Error{Name: "ProtocolError", Message: "could not encode tool result"})
			}
			messages = append(messages, msg)
		}

		if turns >= c.cfg.MaxTurns {
			return c.terminate(StatusLimitExceeded, "", turns, toolResults, usage, messages,
				&Error{Name: "LimitExceeded", Code: "max_turns",
					Message: fmt.Sprintf("conversation reached the %d-turn limit", c.cfg.MaxTurns)})
		}
		if excess {
			return c.terminate(StatusLimitExceeded, "", turns, toolResults, usage, messages,
				&Error{Name: "LimitExceeded", Code: "max_tool_calls",
					Message: fmt.Sprintf("conversation reached the %d-tool-call limit", c.cfg.MaxToolCalls)})
		}
	}
}

func (c *Conversation) terminate(status Status, response string, turns int, toolResults []ConversationToolResult, usage ModelUsage, messages []Message, err *Error) ConversationResult {
	res := ConversationResult{
		Status:      status,
		Response:    response,
		Turns:       turns,
		ToolResults: toolResults,
		Usage:       usage,
		Error:       err,
	}
	if c.cfg.IncludeTranscript {
		res.Messages = cloneMessages(messages)
	}
	return res
}

func (c *Conversation) executeTool(ctx context.Context, call ToolCall, allowProcess bool) ConversationToolResult {
	lang, ok := toolLanguage(call.Function.Name)
	if !ok {
		return c.errorToolResult(call, "", "unknown_tool", fmt.Sprintf("unknown tool %q", call.Function.Name))
	}
	args, aerr := parseToolArguments(call.Function.Arguments, c.execCfg)
	if aerr != nil {
		return c.errorToolResult(call, lang, "invalid_arguments", aerr.Message)
	}
	req := Request{
		Language:       lang,
		Source:         args.Code,
		Filename:       args.Filename,
		Timeout:        args.Timeout,
		MaxOutputBytes: args.MaxOutputBytes,
		// The model can never grant process execution. Only the conversation
		// request can, and the executor's Config.AllowProcess still applies.
		AllowProcess: lang == LanguageBash && allowProcess,
	}
	res := c.cfg.Executor.Run(ctx, req)
	normalized, truncated := normalizeToolResult(res, c.cfg.MaxToolResultBytes, lang == LanguageBash)
	return ConversationToolResult{
		ToolCallID: call.ID,
		ToolName:   clipUTF8(call.Function.Name, maxToolNameBytes),
		IsError:    normalized.Status != StatusCompleted,
		Truncated:  truncated,
		Result:     normalized,
	}
}

func (c *Conversation) errorToolResult(call ToolCall, lang Language, code, message string) ConversationToolResult {
	res := Result{
		Language: lang,
		Status:   StatusInvalidRequest,
		Error:    &Error{Name: "InvalidRequest", Code: code, Message: message},
	}
	normalized, truncated := normalizeToolResult(res, c.cfg.MaxToolResultBytes, lang == LanguageBash)
	return ConversationToolResult{
		ToolCallID: call.ID,
		ToolName:   clipUTF8(call.Function.Name, maxToolNameBytes),
		IsError:    true,
		Truncated:  truncated,
		Result:     normalized,
	}
}

func toolLanguage(name string) (Language, bool) {
	switch name {
	case ToolPython:
		return LanguagePython, true
	case ToolJavaScript:
		return LanguageJavaScript, true
	case ToolBash:
		return LanguageBash, true
	default:
		return "", false
	}
}

func toolMessage(ctr ConversationToolResult) (Message, error) {
	raw, err := marshalNoEscape(ctr)
	if err != nil {
		return Message{}, err
	}
	return Message{
		Role:       "tool",
		Content:    string(raw),
		ToolCallID: ctr.ToolCallID,
		Name:       ctr.ToolName,
	}, nil
}

func historyBytes(messages []Message) (int, error) {
	raw, err := json.Marshal(messages)
	if err != nil {
		return 0, err
	}
	return len(raw), nil
}

func cloneMessages(messages []Message) []Message {
	if len(messages) == 0 {
		return nil
	}
	out := make([]Message, len(messages))
	for i, m := range messages {
		out[i] = m
		if len(m.ToolCalls) > 0 {
			out[i].ToolCalls = append([]ToolCall(nil), m.ToolCalls...)
		}
	}
	return out
}

func cloneTools(tools []ModelTool) []ModelTool {
	if len(tools) == 0 {
		return nil
	}
	return append([]ModelTool(nil), tools...)
}

func ctxStatus(err error) Status {
	if errors.Is(err, context.DeadlineExceeded) {
		return StatusTimedOut
	}
	return StatusCancelled
}

func ctxError(err error) *Error {
	if errors.Is(err, context.DeadlineExceeded) {
		return &Error{Name: "Timeout", Message: "conversation deadline exceeded"}
	}
	return &Error{Name: "Cancelled", Message: "conversation cancelled"}
}

func validateModelResponse(resp ModelResponse, maxTokens int) *Error {
	if resp.Message.Role != "assistant" {
		return &Error{Name: "ProtocolError",
			Message: fmt.Sprintf("model response role %q, want assistant", resp.Message.Role)}
	}
	if len(resp.Message.ToolCalls) == 0 && strings.TrimSpace(resp.Message.Content) == "" {
		return &Error{Name: "ProtocolError", Message: "model response has no text and no tool calls"}
	}
	seen := make(map[string]bool, len(resp.Message.ToolCalls))
	for i, call := range resp.Message.ToolCalls {
		if call.ID == "" {
			return &Error{Name: "ProtocolError", Message: fmt.Sprintf("tool call %d has no id", i)}
		}
		if len(call.ID) > maxToolCallIDBytes {
			return &Error{Name: "ProtocolError", Message: fmt.Sprintf("tool call %d id is too long", i)}
		}
		if seen[call.ID] {
			return &Error{Name: "ProtocolError", Message: fmt.Sprintf("duplicate tool call id %q", call.ID)}
		}
		seen[call.ID] = true
		if call.Type != "function" {
			return &Error{Name: "ProtocolError",
				Message: fmt.Sprintf("tool call %q has type %q, want function", call.ID, call.Type)}
		}
		if call.Function.Name == "" {
			return &Error{Name: "ProtocolError", Message: fmt.Sprintf("tool call %q has no function name", call.ID)}
		}
	}
	return validateUsage(resp.Usage, maxTokens)
}

func validateUsage(u ModelUsage, maxTokens int) *Error {
	if u.InputTokens < 0 || u.OutputTokens < 0 || u.TotalTokens < 0 {
		return &Error{Name: "ProtocolError", Message: "model usage contains a negative token count"}
	}
	if int64(u.InputTokens) > maxUsageTokens || int64(u.OutputTokens) > maxUsageTokens || int64(u.TotalTokens) > maxUsageTokens {
		return &Error{Name: "ProtocolError", Message: "model usage token count is out of range"}
	}
	if u.OutputTokens > maxTokens {
		return &Error{Name: "ProtocolError",
			Message: fmt.Sprintf("model completion tokens %d exceed the request cap %d", u.OutputTokens, maxTokens)}
	}
	return nil
}

func addUsage(dst *ModelUsage, src ModelUsage, maxTokens int) *Error {
	if err := validateUsage(src, maxTokens); err != nil {
		return err
	}
	var ok bool
	if dst.InputTokens, ok = addInt(dst.InputTokens, src.InputTokens); !ok {
		return &Error{Name: "ProtocolError", Message: "model usage overflow"}
	}
	if dst.OutputTokens, ok = addInt(dst.OutputTokens, src.OutputTokens); !ok {
		return &Error{Name: "ProtocolError", Message: "model usage overflow"}
	}
	if dst.TotalTokens, ok = addInt(dst.TotalTokens, src.TotalTokens); !ok {
		return &Error{Name: "ProtocolError", Message: "model usage overflow"}
	}
	return nil
}

var (
	maxInt = int(^uint(0) >> 1)
	minInt = -maxInt - 1
)

func addInt(a, b int) (int, bool) {
	if b > 0 && a > maxInt-b {
		return 0, false
	}
	if b < 0 && a < minInt-b {
		return 0, false
	}
	return a + b, true
}

// resolveConversationConfig applies defaults and validates a ConversationConfig.
func resolveConversationConfig(cfg ConversationConfig) (ConversationConfig, error) {
	if cfg.Executor == nil {
		return cfg, &ConfigError{Field: "Executor", Message: "is required"}
	}
	if cfg.Model == nil {
		return cfg, &ConfigError{Field: "Model", Message: "is required"}
	}
	if strings.TrimSpace(cfg.ModelID) == "" {
		return cfg, &ConfigError{Field: "ModelID", Message: "is required"}
	}
	ec := cfg.Executor.Config()
	if ec.MaxSourceBytes <= 0 {
		return cfg, &ConfigError{Field: "Executor.Config.MaxSourceBytes", Message: "must be > 0"}
	}
	if ec.MaxOutputBytes <= 0 {
		return cfg, &ConfigError{Field: "Executor.Config.MaxOutputBytes", Message: "must be > 0"}
	}
	if ec.Timeout <= 0 {
		return cfg, &ConfigError{Field: "Executor.Config.Timeout", Message: "must be > 0"}
	}
	if cfg.MaxTurns < 0 {
		return cfg, &ConfigError{Field: "MaxTurns", Message: "must be >= 0"}
	}
	if cfg.MaxTurns == 0 {
		cfg.MaxTurns = DefaultMaxTurns
	}
	if cfg.MaxTurns > MaxTurnsLimit {
		return cfg, &ConfigError{Field: "MaxTurns", Message: fmt.Sprintf("must be <= %d", MaxTurnsLimit)}
	}
	if cfg.MaxTokens < 0 {
		return cfg, &ConfigError{Field: "MaxTokens", Message: "must be >= 0"}
	}
	if cfg.MaxTokens == 0 {
		cfg.MaxTokens = DefaultMaxTokens
	}
	if cfg.MaxTokens > MaxTokensLimit {
		return cfg, &ConfigError{Field: "MaxTokens", Message: fmt.Sprintf("must be <= %d", MaxTokensLimit)}
	}
	if cfg.MaxToolCalls < 0 {
		return cfg, &ConfigError{Field: "MaxToolCalls", Message: "must be >= 0"}
	}
	if cfg.MaxToolCalls == 0 {
		cfg.MaxToolCalls = DefaultMaxToolCalls
	}
	if cfg.MaxToolCalls > MaxToolCallsLimit {
		return cfg, &ConfigError{Field: "MaxToolCalls", Message: fmt.Sprintf("must be <= %d", MaxToolCallsLimit)}
	}
	if cfg.MaxToolResultBytes < 0 {
		return cfg, &ConfigError{Field: "MaxToolResultBytes", Message: "must be >= 0"}
	}
	if cfg.MaxToolResultBytes == 0 {
		cfg.MaxToolResultBytes = DefaultMaxToolResultBytes
	}
	if cfg.MaxToolResultBytes > MaxToolResultBytesLimit {
		return cfg, &ConfigError{Field: "MaxToolResultBytes", Message: fmt.Sprintf("must be <= %d", MaxToolResultBytesLimit)}
	}
	if cfg.MaxHistoryBytes < 0 {
		return cfg, &ConfigError{Field: "MaxHistoryBytes", Message: "must be >= 0"}
	}
	if cfg.MaxHistoryBytes == 0 {
		cfg.MaxHistoryBytes = DefaultMaxHistoryBytes
	}
	if cfg.MaxHistoryBytes > MaxHistoryBytesLimit {
		return cfg, &ConfigError{Field: "MaxHistoryBytes", Message: fmt.Sprintf("must be <= %d", MaxHistoryBytesLimit)}
	}
	return cfg, nil
}

// buildConversationTools returns the immutable tool definitions. The slice and
// its maps are never mutated after construction.
func buildConversationTools() []ModelTool {
	return []ModelTool{
		{
			Type: "function",
			Function: ModelFunction{
				Name:        ToolPython,
				Description: pythonToolDescription,
				Parameters:  toolParameters(),
			},
		},
		{
			Type: "function",
			Function: ModelFunction{
				Name:        ToolJavaScript,
				Description: javascriptToolDescription,
				Parameters:  toolParameters(),
			},
		},
		{
			Type: "function",
			Function: ModelFunction{
				Name:        ToolBash,
				Description: bashToolDescription,
				Parameters:  toolParameters(),
			},
		},
	}
}

const (
	pythonToolDescription = "Run Python in the QuickPy MicroPython guest (not CPython). " +
		"Filesystem access is the async quickpy.fs facade: read_text, write_text, read_bytes, " +
		"write_bytes, exists, listdir, mkdir, remove. Call quickpy.run(main()) as the last " +
		"top-level statement. No open, os, subprocess, pathlib, tempfile, network, or pip. " +
		"Convert with bytes(text, \"utf-8\") and str(data, \"utf-8\"); str.encode() and " +
		"bytes.decode() are unavailable. Workspace writes require the agent's AllowWrites grant."

	javascriptToolDescription = "Run JavaScript in a QuickJS sandbox (not Node.js). " +
		"Source is an async function body: top-level await and return work. Use text() for " +
		"output. fs is a frozen async namespace: readFile, writeFile, readdir, mkdir, rm, " +
		"exists. readFile returns Uint8Array, or a string with \"utf8\". No Buffer, modules, " +
		"require, network, or *Sync methods. rm removes files, symlinks, or empty directories " +
		"only; mkdir returns a created boolean. Workspace writes require the agent's " +
		"AllowWrites grant."

	bashToolDescription = "Run Bash as a host process with the host user's full OS privileges. " +
		"It can use the network and read or write outside the workspace. The host environment " +
		"and credentials are not inherited. Process execution requires both the conversation " +
		"and core process grants; if it is not permitted, do not retry."
)

func toolParameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"code": map[string]any{
				"type":        "string",
				"description": "Script source. Required.",
			},
			"filename": map[string]any{
				"type":        "string",
				"description": "Optional display filename (basename only, at most 255 bytes).",
			},
			"timeout_ms": map[string]any{
				"type":        "integer",
				"minimum":     1,
				"description": "Optional timeout in milliseconds. Must be positive and no greater than the agent timeout.",
			},
			"max_output_bytes": map[string]any{
				"type":        "integer",
				"minimum":     1,
				"description": "Optional output cap in bytes. Must be positive and no greater than the agent output cap.",
			},
		},
		"required":             []any{"code"},
		"additionalProperties": false,
	}
}

// toolArguments is the validated, resolved argument set for one tool call.
type toolArguments struct {
	Code           string
	Filename       string
	Timeout        time.Duration
	MaxOutputBytes int
}

// rawToolArguments keeps each field raw so the parser can type-check it
// instead of letting encoding/json coerce a string or float into a number.
type rawToolArguments struct {
	Code           *json.RawMessage `json:"code"`
	Filename       *json.RawMessage `json:"filename"`
	TimeoutMS      *json.RawMessage `json:"timeout_ms"`
	MaxOutputBytes *json.RawMessage `json:"max_output_bytes"`
}

func parseToolArguments(raw string, cfg Config) (toolArguments, *Error) {
	if err := validateJSONObject([]byte(raw)); err != nil {
		return toolArguments{}, invalidArguments(err.Error())
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	var args rawToolArguments
	if err := dec.Decode(&args); err != nil {
		return toolArguments{}, invalidArguments(err.Error())
	}
	if args.Code == nil {
		return toolArguments{}, invalidArguments("code is required")
	}
	code, err := decodeString(*args.Code, "code")
	if err != nil {
		return toolArguments{}, invalidArguments(err.Error())
	}
	if len(code) > cfg.MaxSourceBytes {
		return toolArguments{}, invalidArguments(fmt.Sprintf("code is %d bytes, limit is %d", len(code), cfg.MaxSourceBytes))
	}
	out := toolArguments{
		Code:           code,
		Timeout:        cfg.Timeout,
		MaxOutputBytes: cfg.MaxOutputBytes,
	}
	if args.Filename != nil {
		filename, err := decodeString(*args.Filename, "filename")
		if err != nil {
			return toolArguments{}, invalidArguments(err.Error())
		}
		if filename != "" {
			if err := validateFilename(filename); err != nil {
				return toolArguments{}, invalidArguments(err.Error())
			}
			out.Filename = filename
		}
	}
	if args.TimeoutMS != nil {
		ms, err := decodePositiveInt(*args.TimeoutMS, "timeout_ms")
		if err != nil {
			return toolArguments{}, invalidArguments(err.Error())
		}
		maxMS := int64(cfg.Timeout / time.Millisecond)
		if ms > maxMS {
			return toolArguments{}, invalidArguments(fmt.Sprintf("timeout_ms %d exceeds the agent limit %d", ms, maxMS))
		}
		if ms > maxInt64/int64(time.Millisecond) {
			return toolArguments{}, invalidArguments("timeout_ms is out of range")
		}
		out.Timeout = time.Duration(ms) * time.Millisecond
	}
	if args.MaxOutputBytes != nil {
		n, err := decodePositiveInt(*args.MaxOutputBytes, "max_output_bytes")
		if err != nil {
			return toolArguments{}, invalidArguments(err.Error())
		}
		if n > int64(cfg.MaxOutputBytes) {
			return toolArguments{}, invalidArguments(fmt.Sprintf("max_output_bytes %d exceeds the agent limit %d", n, cfg.MaxOutputBytes))
		}
		out.MaxOutputBytes = int(n)
	}
	return out, nil
}

const maxInt64 = int64(^uint64(0) >> 1)

func invalidArguments(message string) *Error {
	return &Error{Name: "InvalidRequest", Code: "invalid_arguments", Message: message}
}

func decodeString(raw json.RawMessage, field string) (string, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", fmt.Errorf("%s must be a string", field)
	}
	return s, nil
}

func decodePositiveInt(raw json.RawMessage, field string) (int64, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || (trimmed[0] != '-' && (trimmed[0] < '0' || trimmed[0] > '9')) {
		return 0, fmt.Errorf("%s must be an integer", field)
	}
	var num json.Number
	if err := json.Unmarshal(trimmed, &num); err != nil {
		return 0, fmt.Errorf("%s must be an integer", field)
	}
	s := num.String()
	if !isIntegerLiteral(s) {
		return 0, fmt.Errorf("%s must be an integer", field)
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s is out of range", field)
	}
	if v <= 0 {
		return 0, fmt.Errorf("%s must be positive", field)
	}
	return v, nil
}

func isIntegerLiteral(s string) bool {
	if s == "" {
		return false
	}
	if s[0] == '-' {
		s = s[1:]
	}
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func validateFilename(name string) error {
	if len(name) > 255 {
		return fmt.Errorf("filename is %d bytes, limit is 255", len(name))
	}
	if !utf8.ValidString(name) {
		return errors.New("filename must be valid UTF-8")
	}
	if name == "." || name == ".." {
		return fmt.Errorf("filename %q is not allowed", name)
	}
	if strings.ContainsAny(name, "/\\") {
		return errors.New("filename must be a basename without path separators")
	}
	for _, r := range name {
		if r == 0 || r < 0x20 || r == 0x7f {
			return errors.New("filename must not contain control characters")
		}
	}
	return nil
}

// validateJSONObject requires exactly one JSON object with no duplicate keys
// and no trailing data.
func validateJSONObject(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return errors.New("arguments must be a JSON object")
	}
	if err := walkObjectBody(dec); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		if err == nil {
			return errors.New("unexpected trailing JSON")
		}
		return err
	}
	return nil
}

func walkObjectBody(dec *json.Decoder) error {
	seen := make(map[string]bool)
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return err
		}
		key, ok := keyTok.(string)
		if !ok {
			return errors.New("object key is not a string")
		}
		if seen[key] {
			return fmt.Errorf("duplicate object key %q", key)
		}
		seen[key] = true
		if err := walkValue(dec); err != nil {
			return err
		}
	}
	_, err := dec.Token() // consume '}'
	return err
}

func walkValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		return walkObjectBody(dec)
	case '[':
		for dec.More() {
			if err := walkValue(dec); err != nil {
				return err
			}
		}
		_, err := dec.Token() // consume ']'
		return err
	}
	return nil
}

// Tool-result normalization limits. Each field is bounded independently so a
// single huge stack or image cannot crowd out the rest of the result.
const (
	maxToolStackBytes        = 8 << 10
	maxToolOutputEventBytes  = 8 << 10
	maxToolErrorMessageBytes = 8 << 10
	maxToolWarningBytes      = 8 << 10
)

const bashPrivilegeWarning = "bash runs with the host user's full OS privileges; only run trusted scripts."

// normalizeToolResult returns a bounded, JSON-safe copy of res. The same value
// is stored in ConversationToolResult.Result and encoded into the tool message
// content. It never mutates res, including image data.
func normalizeToolResult(res Result, maxBytes int, bash bool) (Result, bool) {
	truncated := false
	out := Result{
		Language: res.Language,
		Status:   res.Status,
	}
	if res.ExitCode != nil {
		v := *res.ExitCode
		out.ExitCode = &v
	}
	if res.Error != nil {
		e := *res.Error
		out.Error = &e
	}
	if res.HasValue() {
		if v, ok := normalizeJSONValue(res.Value); ok {
			out.SetValue(v)
		} else {
			out.SetValue("<unserializable value>")
			truncated = true
		}
	}
	if len(res.Output) > 0 {
		out.Output = make([]Output, len(res.Output))
		for i, o := range res.Output {
			out.Output[i] = o
			if o.Kind == "image" {
				out.Output[i].Data = append([]byte(nil), o.Data...)
			}
		}
	}
	if len(res.Warnings) > 0 {
		out.Warnings = append([]string(nil), res.Warnings...)
	}
	if bash {
		out.Warnings = ensureBashWarning(out.Warnings)
	}
	if out.Error != nil {
		if len(out.Error.Stack) > maxToolStackBytes {
			out.Error.Stack = clipUTF8(out.Error.Stack, maxToolStackBytes)
			truncated = true
		}
		if len(out.Error.Message) > maxToolErrorMessageBytes {
			out.Error.Message = clipUTF8(out.Error.Message, maxToolErrorMessageBytes)
			truncated = true
		}
	}
	for i, w := range out.Warnings {
		if len(w) > maxToolWarningBytes {
			out.Warnings[i] = clipUTF8(w, maxToolWarningBytes)
			truncated = true
		}
	}
	for i := range out.Output {
		o := &out.Output[i]
		if o.Kind == "image" {
			if len(o.Data) > maxToolOutputEventBytes {
				o.Data = o.Data[:maxToolOutputEventBytes]
				truncated = true
			}
		} else if len(o.Text) > maxToolOutputEventBytes {
			o.Text = clipUTF8(o.Text, maxToolOutputEventBytes)
			truncated = true
		}
	}
	if out.HasValue() {
		if raw, err := json.Marshal(out.Value); err == nil && len(raw) > maxBytes {
			if v, ok := shrinkValue(out.Value, maxBytes/2); ok {
				out.SetValue(v)
			} else {
				out.Value = nil
				out.valueSet = false
			}
			truncated = true
		}
	}
	for {
		raw, err := json.Marshal(out)
		if err != nil {
			out = Result{
				Language: out.Language,
				Status:   out.Status,
				Error:    &Error{Name: "LimitExceeded", Code: "tool_result_too_large", Message: "tool result could not be serialized"},
			}
			truncated = true
			continue
		}
		if len(raw) <= maxBytes {
			break
		}
		truncated = true
		if len(out.Output) > 0 {
			out.Output = out.Output[:len(out.Output)-1]
			continue
		}
		if out.HasValue() {
			cur, _ := json.Marshal(out.Value)
			next, ok := shrinkValue(out.Value, len(cur)/2)
			if ok {
				nb, _ := json.Marshal(next)
				if len(nb) < len(cur) {
					out.SetValue(next)
					continue
				}
			}
			out.Value = nil
			out.valueSet = false
			continue
		}
		if len(out.Warnings) > 0 {
			out.Warnings = out.Warnings[:len(out.Warnings)-1]
			continue
		}
		if out.Error != nil && out.Error.Stack != "" {
			out.Error.Stack = ""
			continue
		}
		if out.Error != nil && out.Error.Message != "" {
			out.Error.Message = ""
			continue
		}
		if out.Error != nil {
			out.Error = nil
			continue
		}
		out = Result{Language: out.Language, Status: out.Status}
		break
	}
	return out, truncated
}

func ensureBashWarning(warnings []string) []string {
	for _, w := range warnings {
		if w == bashPrivilegeWarning {
			return warnings
		}
	}
	return append(warnings, bashPrivilegeWarning)
}

// normalizeJSONValue round-trips v through JSON so the CLI record and the model
// tool message carry the same plain generic Go value.
func normalizeJSONValue(v any) (any, bool) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, false
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, false
	}
	return out, true
}

// shrinkValue returns a structurally smaller value that still marshals to
// valid JSON. It never splices a serialized document.
func shrinkValue(v any, budget int) (any, bool) {
	if budget < 0 {
		budget = 0
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, false
	}
	if len(raw) <= budget {
		return v, true
	}
	switch x := v.(type) {
	case string:
		return clipUTF8(x, budget), true
	case []any:
		out := make([]any, 0, len(x))
		for _, item := range x {
			candidate := append(out, item)
			b, err := json.Marshal(candidate)
			if err != nil || len(b) > budget {
				break
			}
			out = candidate
		}
		return out, true
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make(map[string]any, len(x))
		for _, k := range keys {
			out[k] = x[k]
			b, err := json.Marshal(out)
			if err != nil || len(b) > budget {
				delete(out, k)
				break
			}
		}
		return out, true
	default:
		return nil, false
	}
}
