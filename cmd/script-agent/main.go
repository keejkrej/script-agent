// Command script-agent runs one script in one language and prints a single
// structured JSON Result to stdout.
//
// Usage:
//
//	script-agent --lang <python|javascript|bash> (--code <source> | --file <path>) \
//	    --workspace <dir> [--write|--readonly] [--allow-process] \
//	    [--timeout 30s] [--max-output-bytes N] [--max-source-bytes N]
//
// The command never mixes script output into stdout: script stdout, stderr,
// console diagnostics, and images are all fields of the JSON Result. A short
// human-readable message for a non-completed result goes to stderr.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/keejkrej/script-agent"
)

const version = "0.1.0"

// Exit codes. They are stable for the CLI contract.
const (
	exitOK          = 0
	exitFailed      = 1
	exitInvalid     = 2
	exitDenied      = 3
	exitStalled     = 4
	exitUnsupported = 5
	exitTimeout     = 124
	exitCancelled   = 130
	exitLimit       = 134
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	// The `agent` subcommand is the only subcommand. Every other invocation
	// keeps the original single-script flag contract.
	if len(args) > 0 && args[0] == "agent" {
		return runAgent(args[1:], stdout, stderr)
	}

	fs := flag.NewFlagSet("script-agent", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: script-agent --lang <python|javascript|bash> (--code <source> | --file <path>) --workspace <dir> [flags]")
		fmt.Fprintln(stderr, "flags:")
		fs.PrintDefaults()
	}

	var (
		lang         = fs.String("lang", "", "language: python|py|javascript|js|bash")
		code         = fs.String("code", "", "script source")
		file         = fs.String("file", "", "script source file")
		workspace    = fs.String("workspace", "", "workspace directory (required)")
		write        = fs.Bool("write", false, "allow workspace writes")
		allowProcess = fs.Bool("allow-process", false, "allow process execution (bash only)")
		timeout      = fs.Duration("timeout", scriptagent.DefaultTimeout, "execution timeout")
		maxOutput    = fs.Int("max-output-bytes", scriptagent.DefaultMaxOutputBytes, "maximum output bytes")
		maxSource    = fs.Int("max-source-bytes", scriptagent.DefaultMaxSourceBytes, "maximum source bytes")
		showVersion  = fs.Bool("version", false, "print version and artifact info as JSON")
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
		return emitInvalid(stdout, stderr, "", "positional_args", "unexpected positional arguments")
	}
	if *showVersion {
		return printVersion(stdout)
	}

	parsedLang, err := scriptagent.ParseLanguage(*lang)
	if err != nil {
		return emitInvalid(stdout, stderr, "", "language", err.Error())
	}

	codeSet := flagWasSet(fs, "code")
	fileSet := flagWasSet(fs, "file")
	if codeSet == fileSet {
		return emitInvalid(stdout, stderr, parsedLang, "source", "exactly one of --code or --file is required")
	}

	if *workspace == "" {
		return emitInvalid(stdout, stderr, parsedLang, "workspace", "--workspace is required")
	}

	writeSet := flagWasSet(fs, "write")
	readonlySet := flagWasSet(fs, "readonly")
	if writeSet && readonlySet {
		return emitInvalid(stdout, stderr, parsedLang, "readonly", "--write and --readonly are mutually exclusive")
	}
	allowWrites := false
	if writeSet && *write {
		allowWrites = true
	}
	if readonlySet {
		allowWrites = false
	}

	if *allowProcess && parsedLang != scriptagent.LanguageBash {
		return emitInvalid(stdout, stderr, parsedLang, "allow_process", "--allow-process is only valid with --lang bash")
	}
	if *timeout <= 0 {
		return emitInvalid(stdout, stderr, parsedLang, "timeout", "--timeout must be > 0")
	}
	if *maxOutput <= 0 || *maxOutput > scriptagent.MaxOutputBytesLimit {
		return emitInvalid(stdout, stderr, parsedLang, "max_output_bytes",
			fmt.Sprintf("--max-output-bytes must be in 1..%d", scriptagent.MaxOutputBytesLimit))
	}
	if *maxSource <= 0 || *maxSource > scriptagent.MaxSourceBytesLimit {
		return emitInvalid(stdout, stderr, parsedLang, "max_source_bytes",
			fmt.Sprintf("--max-source-bytes must be in 1..%d", scriptagent.MaxSourceBytesLimit))
	}

	var source string
	filename := ""
	if codeSet {
		source = *code
	} else {
		data, err := readSourceFile(*file, *maxSource)
		if err != nil {
			return emitInvalid(stdout, stderr, parsedLang, "file", err.Error())
		}
		source = string(data)
		filename = filepath.Base(*file)
	}
	if len(source) > *maxSource {
		return emitInvalid(stdout, stderr, parsedLang, "source_too_large",
			fmt.Sprintf("source is %d bytes, limit is %d", len(source), *maxSource))
	}

	cfg := scriptagent.Config{
		Workspace:      *workspace,
		AllowWrites:    allowWrites,
		AllowProcess:   *allowProcess,
		MaxOutputBytes: *maxOutput,
		MaxSourceBytes: *maxSource,
		Timeout:        *timeout,
		MaxExecutions:  1,
	}
	agent, err := scriptagent.New(cfg)
	if err != nil {
		return emitInvalid(stdout, stderr, parsedLang, "config", err.Error())
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = agent.Close(ctx)
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	res := agent.Run(ctx, scriptagent.Request{
		Language:       parsedLang,
		Source:         source,
		Filename:       filename,
		Timeout:        *timeout,
		MaxOutputBytes: *maxOutput,
		AllowProcess:   *allowProcess,
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

func flagWasSet(fs *flag.FlagSet, name string) bool {
	set := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
}

// readSourceFile reads at most max+1 bytes so an oversized file is detected
// without allocating the whole file.
func readSourceFile(path string, max int) ([]byte, error) {
	if path == "" {
		return nil, errors.New("--file is empty")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, int64(max)+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if len(data) > max {
		return nil, fmt.Errorf("%s exceeds the %d-byte source limit", path, max)
	}
	return data, nil
}

func printVersion(stdout io.Writer) int {
	info := map[string]any{
		"version":        version,
		"languages":      []string{"python", "javascript", "bash"},
		"pythonArtifact": scriptagent.PythonArtifactInfo(),
	}
	if err := scriptagent.VerifyPythonArtifact(); err != nil {
		info["pythonArtifactError"] = err.Error()
	}
	if err := writeJSON(stdout, info); err != nil {
		return exitFailed
	}
	return exitOK
}

func emitInvalid(stdout, stderr io.Writer, lang scriptagent.Language, code, message string) int {
	res := scriptagent.Result{
		Language: lang,
		Status:   scriptagent.StatusInvalidRequest,
		Error:    &scriptagent.Error{Name: "InvalidRequest", Code: code, Message: message},
	}
	if err := writeJSON(stdout, res); err != nil {
		fmt.Fprintln(stderr, "script-agent: encode result:", err)
		return exitFailed
	}
	fmt.Fprintf(stderr, "script-agent: %s\n", message)
	return exitInvalid
}

// writeJSON encodes v into a buffer and writes it in one call, so stdout never
// contains a partially written JSON document.
func writeJSON(w io.Writer, v any) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return err
	}
	_, err := w.Write(buf.Bytes())
	return err
}

func exitCodeFor(status scriptagent.Status) int {
	switch status {
	case scriptagent.StatusCompleted:
		return exitOK
	case scriptagent.StatusFailed:
		return exitFailed
	case scriptagent.StatusInvalidRequest:
		return exitInvalid
	case scriptagent.StatusDenied:
		return exitDenied
	case scriptagent.StatusStalled:
		return exitStalled
	case scriptagent.StatusUnsupported:
		return exitUnsupported
	case scriptagent.StatusTimedOut:
		return exitTimeout
	case scriptagent.StatusCancelled:
		return exitCancelled
	case scriptagent.StatusLimitExceeded:
		return exitLimit
	default:
		return exitFailed
	}
}
