//go:build darwin || linux

package process

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func requireBash(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	return path
}

// bashArgv builds [bash --noprofile --norc -c script bash args...], so the
// script sees $0="bash" and $1=args[0].
func bashArgv(bash, script string, args ...string) []string {
	argv := []string{bash, "--noprofile", "--norc", "-c", script, "bash"}
	return append(argv, args...)
}

func TestRunExactOutput(t *testing.T) {
	bash := requireBash(t)
	res, err := Run(context.Background(), Request{
		Argv: bashArgv(bash, `printf 'hello'; printf 'world' >&2`),
		Dir:  t.TempDir(),
		Env:  []string{"PATH=/usr/bin:/bin"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if string(res.Stdout) != "hello" {
		t.Fatalf("stdout = %q", res.Stdout)
	}
	if string(res.Stderr) != "world" {
		t.Fatalf("stderr = %q", res.Stderr)
	}
	if res.StdoutTruncated || res.StderrTruncated {
		t.Fatal("unexpected truncation")
	}
	if res.ExitCode == nil || *res.ExitCode != 0 {
		t.Fatalf("ExitCode = %v, want 0", res.ExitCode)
	}
	if res.TimedOut || res.OutputExceeded {
		t.Fatalf("TimedOut=%v OutputExceeded=%v", res.TimedOut, res.OutputExceeded)
	}
}

func TestRunExactCapNoTruncation(t *testing.T) {
	bash := requireBash(t)
	const n = 1024
	res, err := Run(context.Background(), Request{
		Argv:           bashArgv(bash, `printf 'a%.0s' {1..1024}`),
		Dir:            t.TempDir(),
		Env:            []string{"PATH=/usr/bin:/bin"},
		MaxStdoutBytes: n,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Stdout) != n {
		t.Fatalf("len(stdout) = %d, want %d", len(res.Stdout), n)
	}
	if res.StdoutTruncated || res.OutputExceeded {
		t.Fatalf("StdoutTruncated=%v OutputExceeded=%v", res.StdoutTruncated, res.OutputExceeded)
	}
}

func TestRunExceedsCap(t *testing.T) {
	bash := requireBash(t)
	const limit = 1024
	res, err := Run(context.Background(), Request{
		Argv:           bashArgv(bash, `printf 'a%.0s' {1..4096}`),
		Dir:            t.TempDir(),
		Env:            []string{"PATH=/usr/bin:/bin"},
		MaxStdoutBytes: limit,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.OutputExceeded {
		t.Fatal("OutputExceeded = false, want true")
	}
	if !res.StdoutTruncated {
		t.Fatal("StdoutTruncated = false, want true")
	}
	if len(res.Stdout) != limit {
		t.Fatalf("len(stdout) = %d, want %d", len(res.Stdout), limit)
	}
	if string(res.Stdout) != strings.Repeat("a", limit) {
		t.Fatal("stdout is not the expected prefix")
	}
	if res.TimedOut {
		t.Fatal("TimedOut = true, want false")
	}
}

func TestRunStderrExceeds(t *testing.T) {
	bash := requireBash(t)
	const limit = 512
	res, err := Run(context.Background(), Request{
		Argv:           bashArgv(bash, `printf 'b%.0s' {1..2048} >&2`),
		Dir:            t.TempDir(),
		Env:            []string{"PATH=/usr/bin:/bin"},
		MaxStderrBytes: limit,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.OutputExceeded || !res.StderrTruncated {
		t.Fatalf("OutputExceeded=%v StderrTruncated=%v", res.OutputExceeded, res.StderrTruncated)
	}
	if len(res.Stderr) != limit || string(res.Stderr) != strings.Repeat("b", limit) {
		t.Fatalf("stderr = %d bytes", len(res.Stderr))
	}
}

func TestRunNonzeroExit(t *testing.T) {
	bash := requireBash(t)
	res, err := Run(context.Background(), Request{
		Argv: bashArgv(bash, `printf 'out'; printf 'err' >&2; exit 23`),
		Dir:  t.TempDir(),
		Env:  []string{"PATH=/usr/bin:/bin"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode == nil || *res.ExitCode != 23 {
		t.Fatalf("ExitCode = %v, want 23", res.ExitCode)
	}
	if string(res.Stdout) != "out" || string(res.Stderr) != "err" {
		t.Fatalf("stdout=%q stderr=%q", res.Stdout, res.Stderr)
	}
	if res.TimedOut {
		t.Fatal("TimedOut = true")
	}
}

func TestRunEnvIsolation(t *testing.T) {
	bash := requireBash(t)
	const secret = "process-test-secret-value"
	t.Setenv("PROCESS_TEST_SECRET", secret)

	// Nil Env means an empty environment: the parent secret must not leak.
	res, err := Run(context.Background(), Request{
		Argv: bashArgv(bash, `printf '%s' "$PROCESS_TEST_SECRET"`),
		Dir:  t.TempDir(),
		Env:  nil,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(string(res.Stdout), secret) {
		t.Fatal("child inherited the parent secret with nil Env")
	}

	// An explicit Env is passed through verbatim, still without the secret.
	res, err = Run(context.Background(), Request{
		Argv: bashArgv(bash, `printf '%s|%s' "$FOO" "$PROCESS_TEST_SECRET"`),
		Dir:  t.TempDir(),
		Env:  []string{"FOO=bar"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if string(res.Stdout) != "bar|" {
		t.Fatalf("stdout = %q, want %q", res.Stdout, "bar|")
	}
}

func TestRunDir(t *testing.T) {
	bash := requireBash(t)
	dir := t.TempDir()
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), Request{
		Argv: bashArgv(bash, `pwd -P`),
		Dir:  dir,
		Env:  []string{"PATH=/usr/bin:/bin"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := strings.TrimSpace(string(res.Stdout)); got != want {
		t.Fatalf("pwd = %q, want %q", got, want)
	}
	after, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if after != parent {
		t.Fatalf("parent cwd changed: %q -> %q", parent, after)
	}
}

func TestRunLiteralArgv(t *testing.T) {
	bash := requireBash(t)
	const payload = `a b;$(echo hi)&|*?`
	res, err := Run(context.Background(), Request{
		Argv: bashArgv(bash, `printf '%s' "$1"`, payload),
		Dir:  t.TempDir(),
		Env:  []string{"PATH=/usr/bin:/bin"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if string(res.Stdout) != payload {
		t.Fatalf("stdout = %q, want %q", res.Stdout, payload)
	}
}

func TestRunTimeout(t *testing.T) {
	bash := requireBash(t)
	start := time.Now()
	res, err := Run(context.Background(), Request{
		Argv:    bashArgv(bash, `trap '' TERM; sleep 60 & wait`),
		Dir:     t.TempDir(),
		Env:     []string{"PATH=/usr/bin:/bin"},
		Timeout: 200 * time.Millisecond,
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.TimedOut {
		t.Fatal("TimedOut = false, want true")
	}
	if res.OutputExceeded {
		t.Fatal("OutputExceeded = true")
	}
	if elapsed > 3*time.Second {
		t.Fatalf("took %v", elapsed)
	}
}

func TestRunParentCancel(t *testing.T) {
	bash := requireBash(t)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	res, err := Run(ctx, Request{
		Argv: bashArgv(bash, `sleep 60`),
		Dir:  t.TempDir(),
		Env:  []string{"PATH=/usr/bin:/bin"},
	})
	elapsed := time.Since(start)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if res.TimedOut {
		t.Fatal("TimedOut = true")
	}
	if elapsed > 3*time.Second {
		t.Fatalf("took %v", elapsed)
	}
}

func TestRunCancelKillsGroup(t *testing.T) {
	bash := requireBash(t)
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	script := `trap '' TERM; sleep 60 & echo $! > "$1"; wait`

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type outcome struct {
		res Result
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := Run(ctx, Request{
			Argv: bashArgv(bash, script, pidFile),
			Dir:  dir,
			Env:  []string{"PATH=/usr/bin:/bin"},
		})
		done <- outcome{res, err}
	}()

	var pid int
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, err := os.ReadFile(pidFile)
		if err == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
			if pid > 0 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("pid file was not written")
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	select {
	case out := <-done:
		if !errors.Is(out.err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", out.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after cancel")
	}

	deadline = time.Now().Add(2 * time.Second)
	for {
		err := syscall.Kill(pid, 0)
		if err == syscall.ESRCH {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("grandchild %d still alive (kill err = %v)", pid, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRunWaitDelayGrandchild(t *testing.T) {
	bash := requireBash(t)
	start := time.Now()
	res, err := Run(context.Background(), Request{
		Argv:    bashArgv(bash, `sleep 60 &`),
		Dir:     t.TempDir(),
		Env:     []string{"PATH=/usr/bin:/bin"},
		Timeout: 10 * time.Second,
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if elapsed > WaitDelay+3*time.Second {
		t.Fatalf("took %v, want <= %v", elapsed, WaitDelay+3*time.Second)
	}
	if res.ExitCode == nil || *res.ExitCode != 0 {
		t.Fatalf("ExitCode = %v, want 0", res.ExitCode)
	}
}

func TestRunConcurrent(t *testing.T) {
	bash := requireBash(t)
	dir := t.TempDir()
	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()

	var wg sync.WaitGroup
	wg.Add(2)
	var res1, res2 Result
	var err1, err2 error
	go func() {
		defer wg.Done()
		res1, err1 = Run(ctx1, Request{
			Argv: bashArgv(bash, `sleep 60`),
			Dir:  dir,
			Env:  []string{"PATH=/usr/bin:/bin"},
		})
	}()
	go func() {
		defer wg.Done()
		res2, err2 = Run(context.Background(), Request{
			Argv: bashArgv(bash, `printf 'second'`),
			Dir:  dir,
			Env:  []string{"PATH=/usr/bin:/bin"},
		})
	}()

	time.Sleep(200 * time.Millisecond)
	cancel1()
	wg.Wait()

	if !errors.Is(err1, context.Canceled) {
		t.Fatalf("err1 = %v, want context.Canceled", err1)
	}
	if err2 != nil {
		t.Fatalf("err2 = %v", err2)
	}
	if string(res2.Stdout) != "second" {
		t.Fatalf("res2.Stdout = %q", res2.Stdout)
	}
	_ = res1
}

func TestRunStartError(t *testing.T) {
	_, err := Run(context.Background(), Request{
		Argv: []string{"/nonexistent/definitely-not-a-binary"},
		Dir:  t.TempDir(),
	})
	if err == nil {
		t.Fatal("expected an error")
	}
}
