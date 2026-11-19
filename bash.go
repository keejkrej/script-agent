package scriptagent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// execRequest is the normalized process request used by the Bash engine. The
// production adapter in bash_process.go translates it to the process package;
// tests inject a fake execFunc.
type execRequest struct {
	Argv           []string
	Dir            string
	Env            []string
	Timeout        time.Duration
	MaxStdoutBytes int
	MaxStderrBytes int
}

// execResult is the normalized process result.
type execResult struct {
	Stdout         []byte
	Stderr         []byte
	ExitCode       *int
	TimedOut       bool
	OutputExceeded bool
}

// execFunc runs one process. It is a variable so tests can substitute a fake.
type execFunc func(ctx context.Context, req execRequest) (execResult, error)

// bashEngine runs Bash as a host process with a controlled environment.
type bashEngine struct {
	cfg      Config
	exec     execFunc
	findBash func() (string, error)
}

func newBashEngine(cfg Config) *bashEngine {
	return &bashEngine{
		cfg:      cfg,
		exec:     runProcess,
		findBash: findBash,
	}
}

// findBash locates a Bash binary. It prefers the conventional absolute path
// and falls back to a host PATH lookup. The lookup is host-side only; the
// guest process never inherits the host environment.
func findBash() (string, error) {
	if info, err := os.Stat("/bin/bash"); err == nil && !info.IsDir() {
		return "/bin/bash", nil
	}
	if p, err := exec.LookPath("bash"); err == nil {
		return p, nil
	}
	return "", errors.New("bash not found")
}

func (b *bashEngine) Close(ctx context.Context) error { return nil }

func (b *bashEngine) Run(ctx context.Context, req engineRequest) engineResult {
	if !b.cfg.AllowProcess {
		return engineResult{
			Status: StatusDenied,
			Error:  &Error{Name: "Denied", Code: "process_denied", Message: "process execution is not allowed"},
		}
	}
	bash, err := b.findBash()
	if err != nil {
		return engineResult{
			Status: StatusUnsupported,
			Error:  &Error{Name: "Unsupported", Code: "bash_not_found", Message: err.Error()},
		}
	}
	env, cleanup, err := b.environment()
	if err != nil {
		return engineResult{
			Status: StatusFailed,
			Error:  &Error{Name: "ProcessError", Message: err.Error()},
		}
	}
	defer cleanup()

	// The source is one argv literal after -c. It is never interpolated into a
	// shell command line by the host.
	argv := []string{bash, "--noprofile", "--norc", "-c", req.Source, "script-agent"}
	res, err := b.exec(ctx, execRequest{
		Argv:           argv,
		Dir:            b.cfg.Workspace,
		Env:            env,
		Timeout:        req.Timeout,
		MaxStdoutBytes: req.MaxOutputBytes,
		MaxStderrBytes: req.MaxOutputBytes,
	})
	return b.mapResult(res, err)
}

// environment builds the complete, controlled process environment. The host
// environment is never inherited.
func (b *bashEngine) environment() ([]string, func(), error) {
	env := []string{
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"HOME=" + b.cfg.Workspace,
		"PWD=" + b.cfg.Workspace,
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
	}
	tmp, err := os.MkdirTemp("", "script-agent-")
	if err != nil {
		return nil, func() {}, fmt.Errorf("create temp dir: %w", err)
	}
	env = append(env, "TMPDIR="+tmp)
	return env, func() { _ = os.RemoveAll(tmp) }, nil
}

func (b *bashEngine) mapResult(res execResult, err error) engineResult {
	out := make([]Output, 0, 2)
	if len(res.Stdout) > 0 {
		out = append(out, Output{Kind: "text", Stream: "stdout", Text: string(res.Stdout)})
	}
	if len(res.Stderr) > 0 {
		out = append(out, Output{Kind: "text", Stream: "stderr", Text: string(res.Stderr)})
	}
	warnings := []string{"bash runs with the host user's full OS privileges; only run trusted scripts."}

	if res.OutputExceeded {
		return engineResult{
			Status:   StatusLimitExceeded,
			Output:   out,
			Warnings: warnings,
			ExitCode: res.ExitCode,
			Error:    &Error{Name: "LimitExceeded", Message: "bash output exceeded the limit"},
		}
	}
	if res.TimedOut || errors.Is(err, context.DeadlineExceeded) {
		return engineResult{
			Status:   StatusTimedOut,
			Output:   out,
			Warnings: warnings,
			ExitCode: res.ExitCode,
			Error:    &Error{Name: "Timeout", Message: "bash timed out"},
		}
	}
	if errors.Is(err, context.Canceled) {
		return engineResult{
			Status:   StatusCancelled,
			Output:   out,
			Warnings: warnings,
			ExitCode: res.ExitCode,
			Error:    &Error{Name: "Cancelled", Message: "bash cancelled"},
		}
	}
	if err != nil {
		if isProcessUnsupported(err) {
			return engineResult{
				Status:   StatusUnsupported,
				Output:   out,
				Warnings: warnings,
				ExitCode: res.ExitCode,
				Error:    &Error{Name: "Unsupported", Code: "process_unsupported", Message: err.Error()},
			}
		}
		return engineResult{
			Status:   StatusFailed,
			Output:   out,
			Warnings: warnings,
			ExitCode: res.ExitCode,
			Error:    &Error{Name: "ProcessError", Message: err.Error()},
		}
	}
	if res.ExitCode != nil && *res.ExitCode != 0 {
		return engineResult{
			Status:   StatusFailed,
			Output:   out,
			Warnings: warnings,
			ExitCode: res.ExitCode,
			Error: &Error{
				Name:    "ProcessError",
				Code:    "exit_status",
				Message: fmt.Sprintf("bash exited with status %d", *res.ExitCode),
			},
		}
	}
	return engineResult{
		Status:   StatusCompleted,
		Output:   out,
		Warnings: warnings,
		ExitCode: res.ExitCode,
	}
}
