//go:build scriptagent_integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	scriptagent "github.com/keejkrej/script-agent"
)

func newBashAgent(t *testing.T, cfg scriptagent.Config) *scriptagent.Agent {
	t.Helper()
	cfg.AllowProcess = true
	if cfg.Timeout == 0 {
		cfg.Timeout = 20 * time.Second
	}
	return newAgent(t, cfg)
}

// TestBashEnvIsolation proves the child never inherits the host environment.
func TestBashEnvIsolation(t *testing.T) {
	t.Setenv("SCRIPT_AGENT_SECRET", "leaked")
	a := newBashAgent(t, scriptagent.Config{Workspace: t.TempDir()})
	res := runAgent(t, a, scriptagent.Request{
		Language:     scriptagent.LanguageBash,
		Source:       `echo "SECRET=${SCRIPT_AGENT_SECRET-unset}"`,
		AllowProcess: true,
	})
	requireStatus(t, res, scriptagent.StatusCompleted)
	if got := outputText(res); got != "SECRET=unset\n" {
		t.Fatalf("host environment leaked into bash: %q", got)
	}
}

// TestBashCwdIsWorkspace proves the child runs in the workspace and $PWD
// agrees with it.
func TestBashCwdIsWorkspace(t *testing.T) {
	ws := t.TempDir()
	a := newBashAgent(t, scriptagent.Config{Workspace: ws})
	res := runAgent(t, a, scriptagent.Request{
		Language:     scriptagent.LanguageBash,
		Source:       `echo "$PWD"; pwd`,
		AllowProcess: true,
	})
	requireStatus(t, res, scriptagent.StatusCompleted)
	lines := strings.Split(strings.TrimSuffix(outputText(res), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("output = %q", outputText(res))
	}
	for _, line := range lines {
		if line != ws {
			t.Fatalf("cwd = %q, want %q", line, ws)
		}
	}
}

// TestBashArgvLiteral proves the source is one literal argv element: the host
// does not interpolate it, and bash parses pipes and control characters.
func TestBashArgvLiteral(t *testing.T) {
	a := newBashAgent(t, scriptagent.Config{Workspace: t.TempDir()})
	src := `printf 'a | b\n'; printf 'c; d\n'; printf 'e && f\n'`
	res := runAgent(t, a, scriptagent.Request{
		Language:     scriptagent.LanguageBash,
		Source:       src,
		AllowProcess: true,
	})
	requireStatus(t, res, scriptagent.StatusCompleted)
	want := "a | b\nc; d\ne && f\n"
	if got := outputText(res); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

// TestBashNonzeroExit proves a normal non-zero exit is a failed result with
// the exit code preserved.
func TestBashNonzeroExit(t *testing.T) {
	a := newBashAgent(t, scriptagent.Config{Workspace: t.TempDir()})
	res := runAgent(t, a, scriptagent.Request{
		Language:     scriptagent.LanguageBash,
		Source:       `echo out; echo err >&2; exit 3`,
		AllowProcess: true,
	})
	requireStatus(t, res, scriptagent.StatusFailed)
	if res.ExitCode == nil || *res.ExitCode != 3 {
		t.Fatalf("exit code = %v, want 3", res.ExitCode)
	}
	if res.Error == nil || res.Error.Code != "exit_status" {
		t.Fatalf("error = %+v", res.Error)
	}
	if got := streamText(res, "stdout"); got != "out\n" {
		t.Fatalf("stdout = %q", got)
	}
	if got := streamText(res, "stderr"); got != "err\n" {
		t.Fatalf("stderr = %q", got)
	}
}

// TestBashOutputCaps proves stdout and stderr are capped independently and a
// cap hit is limit_exceeded with the retained prefix.
func TestBashOutputCaps(t *testing.T) {
	a := newBashAgent(t, scriptagent.Config{Workspace: t.TempDir(), MaxOutputBytes: 10})

	stdoutOver := runAgent(t, a, scriptagent.Request{
		Language:     scriptagent.LanguageBash,
		Source:       `printf 'A%.0s' $(seq 1 100)`,
		AllowProcess: true,
	})
	requireStatus(t, stdoutOver, scriptagent.StatusLimitExceeded)
	if got := streamText(stdoutOver, "stdout"); got != "AAAAAAAAAA" {
		t.Fatalf("stdout prefix = %q", got)
	}

	stderrOver := runAgent(t, a, scriptagent.Request{
		Language:     scriptagent.LanguageBash,
		Source:       `printf 'B%.0s' $(seq 1 100) >&2`,
		AllowProcess: true,
	})
	requireStatus(t, stderrOver, scriptagent.StatusLimitExceeded)
	if got := streamText(stderrOver, "stderr"); got != "BBBBBBBBBB" {
		t.Fatalf("stderr prefix = %q", got)
	}
}

// TestBashTimeout proves a process timeout is timed_out and bounded.
func TestBashTimeout(t *testing.T) {
	a := newBashAgent(t, scriptagent.Config{Workspace: t.TempDir()})
	start := time.Now()
	res := runAgent(t, a, scriptagent.Request{
		Language:     scriptagent.LanguageBash,
		Source:       `sleep 5`,
		AllowProcess: true,
		Timeout:      300 * time.Millisecond,
	})
	elapsed := time.Since(start)
	requireStatus(t, res, scriptagent.StatusTimedOut)
	if elapsed > 8*time.Second {
		t.Fatalf("timeout took %s, want a bounded return", elapsed)
	}
}

// TestBashCancel proves caller cancellation is cancelled.
func TestBashCancel(t *testing.T) {
	a := newBashAgent(t, scriptagent.Config{Workspace: t.TempDir()})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()
	defer cancel()
	res := a.Run(ctx, scriptagent.Request{
		Language:     scriptagent.LanguageBash,
		Source:       `sleep 5`,
		AllowProcess: true,
	})
	requireStatus(t, res, scriptagent.StatusCancelled)
}

// TestBashGroupCleanup proves a timed-out process group leaves no known
// descendant behind. The marker is unique so an unrelated user process cannot
// satisfy the check.
func TestBashGroupCleanup(t *testing.T) {
	if _, err := exec.LookPath("pgrep"); err != nil {
		t.Skip("pgrep not available")
	}
	marker := fmt.Sprintf("script-agent-e2e-%d", time.Now().UnixNano())
	a := newBashAgent(t, scriptagent.Config{Workspace: t.TempDir()})
	src := fmt.Sprintf(`bash -c 'exec -a %s sleep 30' & echo spawned; wait`, marker)
	res := runAgent(t, a, scriptagent.Request{
		Language:     scriptagent.LanguageBash,
		Source:       src,
		AllowProcess: true,
		Timeout:      500 * time.Millisecond,
	})
	requireStatus(t, res, scriptagent.StatusTimedOut)

	deadline := time.Now().Add(5 * time.Second)
	for {
		out, err := exec.Command("pgrep", "-f", marker).CombinedOutput()
		if err != nil {
			// pgrep exits 1 when nothing matches.
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("descendant %q survived the process-group kill: %s", marker, out)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestBashWarnings proves the privileged-process warning is preserved.
func TestBashWarnings(t *testing.T) {
	a := newBashAgent(t, scriptagent.Config{Workspace: t.TempDir()})
	res := runAgent(t, a, scriptagent.Request{
		Language:     scriptagent.LanguageBash,
		Source:       `true`,
		AllowProcess: true,
	})
	requireStatus(t, res, scriptagent.StatusCompleted)
	if len(res.Warnings) == 0 {
		t.Fatal("bash result has no warnings")
	}
	if !strings.Contains(strings.ToLower(res.Warnings[0]), "privilege") {
		t.Fatalf("warning = %q", res.Warnings[0])
	}
}

// TestBashInvalidUTF8 proves a raw invalid-UTF-8 byte prefix is still a valid
// JSON document: encoding/json replaces the invalid bytes with U+FFFD.
func TestBashInvalidUTF8(t *testing.T) {
	a := newBashAgent(t, scriptagent.Config{Workspace: t.TempDir()})
	res := runAgent(t, a, scriptagent.Request{
		Language:     scriptagent.LanguageBash,
		Source:       `printf '\xff\xfe'`,
		AllowProcess: true,
	})
	requireStatus(t, res, scriptagent.StatusCompleted)
	raw := outputText(res)
	if utf8.ValidString(raw) {
		t.Fatalf("raw output is unexpectedly valid UTF-8: %q", raw)
	}
	data, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var decoded scriptagent.Result
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	text := outputText(decoded)
	if !utf8.ValidString(text) {
		t.Fatalf("decoded text is not valid UTF-8: %q", text)
	}
	if !strings.Contains(text, "\uFFFD") {
		t.Fatalf("decoded text = %q, want U+FFFD replacement", text)
	}
}

// TestBashUnicode proves valid multi-byte output survives unchanged.
func TestBashUnicode(t *testing.T) {
	a := newBashAgent(t, scriptagent.Config{Workspace: t.TempDir()})
	const want = "Grüße 世界 👋\n"
	res := runAgent(t, a, scriptagent.Request{
		Language:     scriptagent.LanguageBash,
		Source:       `printf 'Grüße 世界 👋\n'`,
		AllowProcess: true,
	})
	requireStatus(t, res, scriptagent.StatusCompleted)
	if got := outputText(res); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

// TestBashNULRejected proves a NUL in the source is rejected by the process
// boundary instead of being passed to the OS.
func TestBashNULRejected(t *testing.T) {
	a := newBashAgent(t, scriptagent.Config{Workspace: t.TempDir()})
	res := runAgent(t, a, scriptagent.Request{
		Language:     scriptagent.LanguageBash,
		Source:       "echo hi\x00echo bye",
		AllowProcess: true,
	})
	requireStatus(t, res, scriptagent.StatusFailed)
	if res.Error == nil || res.Error.Name != "ProcessError" {
		t.Fatalf("error = %+v", res.Error)
	}
}

// TestBashCLIExitCodes proves the CLI maps a normal non-zero exit to exit 1
// with the result exitCode preserved, a timeout to 124, and a signal to 130.
func TestBashCLIExitCodes(t *testing.T) {
	ws := t.TempDir()

	nonzero := runCLI(t, ws, nil, "--lang", "bash", "--code", `exit 3`, "--workspace", ws, "--allow-process")
	if nonzero.ExitCode != 1 {
		t.Fatalf("nonzero CLI exit = %d, want 1", nonzero.ExitCode)
	}
	res := decodeSingleJSON(t, nonzero.Stdout)
	requireStatus(t, res, scriptagent.StatusFailed)
	if res.ExitCode == nil || *res.ExitCode != 3 {
		t.Fatalf("result exitCode = %v, want 3", res.ExitCode)
	}

	timedOut := runCLI(t, ws, nil, "--lang", "bash", "--code", `sleep 5`, "--workspace", ws, "--allow-process", "--timeout", "300ms")
	if timedOut.ExitCode != 124 {
		t.Fatalf("timeout CLI exit = %d, want 124", timedOut.ExitCode)
	}
	requireStatus(t, decodeSingleJSON(t, timedOut.Stdout), scriptagent.StatusTimedOut)

	limited := runCLI(t, ws, nil, "--lang", "bash", "--code", `printf 'A%.0s' $(seq 1 100)`, "--workspace", ws, "--allow-process", "--max-output-bytes", "10")
	if limited.ExitCode != 134 {
		t.Fatalf("limit CLI exit = %d, want 134", limited.ExitCode)
	}
	limitedRes := decodeSingleJSON(t, limited.Stdout)
	requireStatus(t, limitedRes, scriptagent.StatusLimitExceeded)
	if got := streamText(limitedRes, "stdout"); got != "AAAAAAAAAA" {
		t.Fatalf("limit stdout prefix = %q", got)
	}

	cancelled := runCLICancel(t, ws, "--lang", "bash", "--code", `sleep 5`, "--workspace", ws, "--allow-process", "--timeout", "20s")
	if cancelled.ExitCode != 130 {
		t.Fatalf("cancel CLI exit = %d, want 130", cancelled.ExitCode)
	}
	requireStatus(t, decodeSingleJSON(t, cancelled.Stdout), scriptagent.StatusCancelled)
}

// runCLICancel starts the CLI, sends SIGTERM after a short delay, and returns
// the completed run.
func runCLICancel(t *testing.T, dir string, args ...string) cliRun {
	t.Helper()
	cmd := exec.Command(cliBinary(t), args...)
	cmd.Dir = dir
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start cli: %v", err)
	}
	time.Sleep(400 * time.Millisecond)
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal cli: %v", err)
	}
	err := cmd.Wait()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("wait cli: %v", err)
		}
		code = exitErr.ExitCode()
	}
	return cliRun{ExitCode: code, Stdout: []byte(stdout.String()), Stderr: []byte(stderr.String())}
}

// TestBashReadOnlyStillWarns proves the read-only workspace posture does not
// suppress the privileged-process warning.
func TestBashReadOnlyStillWarns(t *testing.T) {
	a := newBashAgent(t, scriptagent.Config{Workspace: t.TempDir(), AllowWrites: false})
	res := runAgent(t, a, scriptagent.Request{
		Language:     scriptagent.LanguageBash,
		Source:       `echo ok`,
		AllowProcess: true,
	})
	requireStatus(t, res, scriptagent.StatusCompleted)
	if len(res.Warnings) == 0 {
		t.Fatal("read-only bash result has no warnings")
	}
}

// TestBashNoParentCwdChange proves the host process working directory is not
// changed by a Bash run.
func TestBashNoParentCwdChange(t *testing.T) {
	before, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	a := newBashAgent(t, scriptagent.Config{Workspace: t.TempDir()})
	res := runAgent(t, a, scriptagent.Request{
		Language:     scriptagent.LanguageBash,
		Source:       `pwd`,
		AllowProcess: true,
	})
	requireStatus(t, res, scriptagent.StatusCompleted)
	after, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("parent cwd changed from %q to %q", before, after)
	}
}
