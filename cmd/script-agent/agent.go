// The `agent` subcommand runs one bounded conversation: a model is asked to
// inspect a workspace through the three script tools, and the terminal
// ConversationResult is printed as one JSON document on stdout.
//
// Usage:
//
//	script-agent agent --prompt <text> --workspace <dir> \
//	    --provider openai-compatible --endpoint <full-url> \
//	    --model <id> --api-key-env <var> [flags]
//
// The command never reads an ambient provider key, base URL, or model. The
// only credential input is the named environment variable, and it is sent only
// as an Authorization header by the provider client.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/keejkrej/script-agent"
	"github.com/keejkrej/script-agent/internal/openaicompatible"
)

// Agent command defaults. They mirror the conversation package defaults so the
// CLI and the library agree on the safe posture.
const (
	defaultAgentTimeout            = 30 * time.Second
	defaultAgentMaxSourceBytes     = scriptagent.DefaultMaxSourceBytes
	defaultAgentMaxOutputBytes     = scriptagent.DefaultMaxOutputBytes
	defaultAgentMaxTurns           = scriptagent.DefaultMaxTurns
	defaultAgentMaxTokens          = scriptagent.DefaultMaxTokens
	defaultAgentMaxToolCalls       = scriptagent.DefaultMaxToolCalls
	defaultAgentMaxToolResultBytes = scriptagent.DefaultMaxToolResultBytes
	defaultAgentMaxHistoryBytes    = scriptagent.DefaultMaxHistoryBytes
)

// agentSystemPrompt is the minimal system message. The executable facts live
// in the tool descriptions; this only sets the task and the credential
// boundary.
const agentSystemPrompt = "You are a scripting agent. Inspect the workspace and complete the user's request using the available tools. Each tool call runs in a fresh environment; workspace files persist. Do not attempt to read provider credentials or proxy model traffic through tools."

// newAgentModel builds the provider. It is a package variable so unit tests
// can substitute a scripted model without network I/O. Production always uses
// the real OpenAI-compatible client.
var newAgentModel = func(cfg openaicompatible.Config) (scriptagent.Model, error) {
	return openaicompatible.New(cfg)
}

// runAgent implements the `agent` subcommand. It returns a process exit code.
// On a valid invocation it writes exactly one ConversationResult JSON document
// to stdout. A flag-parse error keeps the legacy CLI behavior: it writes only
// to stderr and emits no JSON.
func runAgent(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("script-agent agent", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: script-agent agent --prompt <text> --workspace <dir> --provider openai-compatible --endpoint <url> --model <id> --api-key-env <var> [flags]")
		fmt.Fprintln(stderr, "flags:")
		fs.PrintDefaults()
	}

	var (
		prompt       = fs.String("prompt", "", "user prompt (required)")
		workspace    = fs.String("workspace", "", "workspace directory (required)")
		provider     = fs.String("provider", "", "model provider: openai-compatible (required)")
		endpoint     = fs.String("endpoint", "", "full Chat Completions URL (required)")
		model        = fs.String("model", "", "model identifier (required)")
		apiKeyEnv    = fs.String("api-key-env", "", "environment variable holding the API key (required)")
		caFile       = fs.String("ca-file", "", "optional PEM file with additional trust roots")
		write        = fs.Bool("write", false, "allow workspace writes")
		allowProcess = fs.Bool("allow-process", false, "allow process execution (bash)")
		timeout      = fs.Duration("timeout", defaultAgentTimeout, "conversation timeout")
		maxSource    = fs.Int("max-source-bytes", defaultAgentMaxSourceBytes, "maximum script source bytes")
		maxOutput    = fs.Int("max-output-bytes", defaultAgentMaxOutputBytes, "maximum script output bytes")
		maxTurns     = fs.Int("max-turns", defaultAgentMaxTurns, "maximum model turns")
		maxTokens    = fs.Int("max-tokens", defaultAgentMaxTokens, "maximum completion tokens per turn")
		maxToolCalls = fs.Int("max-tool-calls", defaultAgentMaxToolCalls, "maximum tool calls")
		maxToolRes   = fs.Int("max-tool-result-bytes", defaultAgentMaxToolResultBytes, "maximum tool result bytes")
		maxHistory   = fs.Int("max-history-bytes", defaultAgentMaxHistoryBytes, "maximum conversation history bytes")
		transcript   = fs.Bool("transcript", false, "include the full transcript in the result")
	)
	// --readonly is a marker for the default posture. It is registered
	// separately because its value is not used: the absence of --write is
	// already read-only.
	fs.Bool("readonly", false, "read-only workspace (default)")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitInvalid
	}
	if fs.NArg() > 0 {
		return emitAgentInvalid(stdout, stderr, "positional_args", "unexpected positional arguments")
	}

	if strings.TrimSpace(*prompt) == "" {
		return emitAgentInvalid(stdout, stderr, "prompt", "--prompt is required")
	}
	if *workspace == "" {
		return emitAgentInvalid(stdout, stderr, "workspace", "--workspace is required")
	}
	if *provider != "openai-compatible" {
		return emitAgentInvalid(stdout, stderr, "provider", "--provider must be openai-compatible")
	}
	if *endpoint == "" {
		return emitAgentInvalid(stdout, stderr, "endpoint", "--endpoint is required")
	}
	if strings.TrimSpace(*model) == "" {
		return emitAgentInvalid(stdout, stderr, "model", "--model is required")
	}
	if *apiKeyEnv == "" {
		return emitAgentInvalid(stdout, stderr, "api_key_env", "--api-key-env is required")
	}

	writeSet := flagWasSet(fs, "write")
	readonlySet := flagWasSet(fs, "readonly")
	if writeSet && readonlySet {
		return emitAgentInvalid(stdout, stderr, "readonly", "--write and --readonly are mutually exclusive")
	}
	allowWrites := false
	if writeSet && *write {
		allowWrites = true
	}
	if readonlySet {
		allowWrites = false
	}

	if *timeout <= 0 {
		return emitAgentInvalid(stdout, stderr, "timeout", "--timeout must be > 0")
	}
	if *maxSource <= 0 || *maxSource > scriptagent.MaxSourceBytesLimit {
		return emitAgentInvalid(stdout, stderr, "max_source_bytes",
			fmt.Sprintf("--max-source-bytes must be in 1..%d", scriptagent.MaxSourceBytesLimit))
	}
	if *maxOutput <= 0 || *maxOutput > scriptagent.MaxOutputBytesLimit {
		return emitAgentInvalid(stdout, stderr, "max_output_bytes",
			fmt.Sprintf("--max-output-bytes must be in 1..%d", scriptagent.MaxOutputBytesLimit))
	}
	if *maxTurns <= 0 || *maxTurns > scriptagent.MaxTurnsLimit {
		return emitAgentInvalid(stdout, stderr, "max_turns",
			fmt.Sprintf("--max-turns must be in 1..%d", scriptagent.MaxTurnsLimit))
	}
	if *maxTokens <= 0 || *maxTokens > scriptagent.MaxTokensLimit {
		return emitAgentInvalid(stdout, stderr, "max_tokens",
			fmt.Sprintf("--max-tokens must be in 1..%d", scriptagent.MaxTokensLimit))
	}
	if *maxToolCalls <= 0 || *maxToolCalls > scriptagent.MaxToolCallsLimit {
		return emitAgentInvalid(stdout, stderr, "max_tool_calls",
			fmt.Sprintf("--max-tool-calls must be in 1..%d", scriptagent.MaxToolCallsLimit))
	}
	if *maxToolRes <= 0 || *maxToolRes > scriptagent.MaxToolResultBytesLimit {
		return emitAgentInvalid(stdout, stderr, "max_tool_result_bytes",
			fmt.Sprintf("--max-tool-result-bytes must be in 1..%d", scriptagent.MaxToolResultBytesLimit))
	}
	if *maxHistory <= 0 || *maxHistory > scriptagent.MaxHistoryBytesLimit {
		return emitAgentInvalid(stdout, stderr, "max_history_bytes",
			fmt.Sprintf("--max-history-bytes must be in 1..%d", scriptagent.MaxHistoryBytesLimit))
	}

	agent, err := scriptagent.New(scriptagent.Config{
		Workspace:      *workspace,
		AllowWrites:    allowWrites,
		AllowProcess:   *allowProcess,
		MaxOutputBytes: *maxOutput,
		MaxSourceBytes: *maxSource,
		Timeout:        *timeout,
		MaxExecutions:  1,
	})
	if err != nil {
		return emitAgentInvalid(stdout, stderr, "config", err.Error())
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = agent.Close(ctx)
	}()

	providerClient, err := newAgentModel(openaicompatible.Config{
		Endpoint:    *endpoint,
		APIKeyEnv:   *apiKeyEnv,
		CAFile:      *caFile,
		HTTPTimeout: *timeout,
	})
	if err != nil {
		return emitAgentInvalid(stdout, stderr, "provider", err.Error())
	}

	conv, err := scriptagent.NewConversation(scriptagent.ConversationConfig{
		Executor:           agent,
		Model:              providerClient,
		ModelID:            *model,
		SystemPrompt:       agentSystemPrompt,
		MaxTurns:           *maxTurns,
		MaxTokens:          *maxTokens,
		MaxToolCalls:       *maxToolCalls,
		MaxToolResultBytes: *maxToolRes,
		MaxHistoryBytes:    *maxHistory,
		IncludeTranscript:  *transcript,
	})
	if err != nil {
		return emitAgentInvalid(stdout, stderr, "conversation", err.Error())
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	res := conv.Run(ctx, scriptagent.ConversationRequest{
		Prompt:       *prompt,
		AllowProcess: *allowProcess,
	})

	if err := writeJSON(stdout, res); err != nil {
		fmt.Fprintln(stderr, "script-agent: encode result:", err)
		return exitFailed
	}
	if res.Status != scriptagent.StatusCompleted {
		msg := string(res.Status)
		if res.Error != nil {
			msg = res.Error.Name + ": " + res.Error.Message
		}
		fmt.Fprintf(stderr, "script-agent: %s\n", msg)
	}
	return exitCodeFor(res.Status)
}

// emitAgentInvalid writes one invalid-request ConversationResult and returns
// the invalid-request exit code.
func emitAgentInvalid(stdout, stderr io.Writer, code, message string) int {
	res := scriptagent.ConversationResult{
		Status: scriptagent.StatusInvalidRequest,
		Error:  &scriptagent.Error{Name: "InvalidRequest", Code: code, Message: message},
	}
	if err := writeJSON(stdout, res); err != nil {
		fmt.Fprintln(stderr, "script-agent: encode result:", err)
		return exitFailed
	}
	fmt.Fprintf(stderr, "script-agent: %s\n", message)
	return exitInvalid
}
