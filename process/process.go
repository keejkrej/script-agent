// Package process runs one caller-supplied external process with bounded
// output capture, an explicit working directory, an explicit environment,
// and process-group cancellation.
//
// Run is a low-level primitive, not a sandbox. It does not decide which
// programs may run, it does not confine filesystem access, and it does not
// promise that the child stays inside Request.Dir. The caller is responsible
// for policy: only pass an Argv the application has already authorized.
//
// # Argv
//
// Argv is passed to the operating system as a literal argument vector. Run
// never joins, quotes, splits, or interpolates it. A trusted caller may pass
// a shell program such as
//
//	[]string{"bash", "--noprofile", "--norc", "-c", `source "$0"`, scriptPath}
//
// but generated or otherwise untrusted tools must never be allowed to build a
// shell command line by string concatenation. If a caller needs to pass data
// to a shell script, pass it as a separate Argv element and read it from "$1"
// (or later) inside the script.
//
// # Dir
//
// Dir is the child's working directory. It must be an absolute path to an
// existing directory. Run does not call os.Chdir and never changes the parent
// process's working directory. Dir is not a sandbox: the child can use
// absolute paths, "..", and symlinks to leave it. Run does not resolve
// symlinks and does not promise any confinement.
//
// # Env
//
// Env is the child's complete environment. A nil Env means an empty
// environment, not the parent's environment; Run never falls back to
// os.Environ. The executable is still resolved with the host's PATH (the same
// lookup exec.Command performs), so a bare name such as "bash" is found even
// when the child environment has no PATH. Pass an explicit PATH in Env if the
// child needs one.
//
// # Output
//
// Stdout and Stderr are raw byte prefixes of the child's output. Their length
// never exceeds the configured cap. Truncation is a byte-level prefix clip;
// Run never rewrites or re-encodes bytes, so a truncated stream may end in the
// middle of a UTF-8 sequence. Callers that need text should validate UTF-8
// themselves.
//
// # Exit status
//
// A process that exits with a non-zero status is a normal result:
// Result.ExitCode points at the status and Run returns a nil error. A process
// killed by a signal that Run did not cause has a nil ExitCode and Run returns
// the *exec.ExitError. A process killed because of a timeout, output overflow,
// or parent cancellation is reported through Result.TimedOut,
// Result.OutputExceeded, or the returned context error.
//
// # Cancellation
//
// Run enforces the earlier of Request.Timeout (default DefaultTimeout) and the
// parent context's deadline. On POSIX the child is placed in its own process
// group and cancellation sends SIGKILL to the whole group, so grandchildren do
// not survive. Run always makes a best-effort attempt to terminate the process
// group before returning, including after a normal exit, so a child that
// daemonizes is not supported.
package process

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultTimeout is used when Request.Timeout is zero.
	DefaultTimeout = 30 * time.Second

	// DefaultMaxOutputBytes is used when a per-stream cap is zero.
	DefaultMaxOutputBytes = 1 << 20 // 1 MiB

	// WaitDelay is the grace period after the child exits during which Run
	// waits for output pipes to close before forcibly closing them. It bounds
	// how long a grandchild can hold the pipes open.
	WaitDelay = 2 * time.Second
)

var (
	// ErrInvalidRequest reports a Request that Run refused before starting
	// any process.
	ErrInvalidRequest = errors.New("process: invalid request")

	// errUnsupported is returned on platforms where Run cannot execute
	// processes. It is only reachable on Windows.
	errUnsupported = errors.New("process: unsupported on this platform")

	// errTimeout and errOutputExceeded are internal cancellation causes. They
	// are never returned to callers; Result.TimedOut and
	// Result.OutputExceeded carry the outcome instead.
	errTimeout        = errors.New("process: timeout")
	errOutputExceeded = errors.New("process: output exceeded")
)

// Request describes one process to run.
type Request struct {
	// Argv is the literal argument vector. Argv[0] is the executable.
	Argv []string
	// Dir is the child's absolute working directory.
	Dir string
	// Env is the child's complete environment. Nil means empty.
	Env []string
	// Timeout bounds the run. Zero means DefaultTimeout.
	Timeout time.Duration
	// MaxStdoutBytes and MaxStderrBytes cap captured output. Zero means
	// DefaultMaxOutputBytes; negative values are rejected.
	MaxStdoutBytes int
	MaxStderrBytes int
}

// Result is the outcome of a run.
type Result struct {
	Stdout []byte
	Stderr []byte

	StdoutTruncated bool
	StderrTruncated bool

	// ExitCode is set when the process exited normally. It is nil when the
	// process was killed by a signal or never started.
	ExitCode *int

	// TimedOut reports that the run was stopped by Request.Timeout or by the
	// parent context's deadline.
	TimedOut bool
	// OutputExceeded reports that a stream exceeded its cap and the process
	// group was killed.
	OutputExceeded bool
}

// Run executes req.Argv once and returns its captured output.
//
// Run returns an error only for an invalid Request, a failure to start the
// process, or parent-context cancellation. A non-zero exit status, a timeout,
// and output overflow are reported through Result with a nil error.
func Run(ctx context.Context, req Request) (Result, error) {
	if ctx == nil {
		return Result{}, fmt.Errorf("%w: nil context", ErrInvalidRequest)
	}
	if err := validate(req); err != nil {
		return Result{}, err
	}
	if !platformSupported {
		return Result{}, errUnsupported
	}
	return run(ctx, req)
}

func validate(req Request) error {
	if len(req.Argv) == 0 {
		return fmt.Errorf("%w: empty argv", ErrInvalidRequest)
	}
	if req.Argv[0] == "" {
		return fmt.Errorf("%w: empty executable", ErrInvalidRequest)
	}
	for i, arg := range req.Argv {
		if strings.IndexByte(arg, 0) >= 0 {
			return fmt.Errorf("%w: argv[%d] contains NUL", ErrInvalidRequest, i)
		}
	}
	for i, entry := range req.Env {
		if strings.IndexByte(entry, 0) >= 0 {
			return fmt.Errorf("%w: Env[%d] contains NUL", ErrInvalidRequest, i)
		}
	}
	if req.MaxStdoutBytes < 0 {
		return fmt.Errorf("%w: negative MaxStdoutBytes", ErrInvalidRequest)
	}
	if req.MaxStderrBytes < 0 {
		return fmt.Errorf("%w: negative MaxStderrBytes", ErrInvalidRequest)
	}
	if req.Timeout < 0 {
		return fmt.Errorf("%w: negative Timeout", ErrInvalidRequest)
	}
	if req.Dir == "" {
		return fmt.Errorf("%w: Dir is required", ErrInvalidRequest)
	}
	if !filepath.IsAbs(req.Dir) {
		return fmt.Errorf("%w: Dir must be absolute", ErrInvalidRequest)
	}
	info, err := os.Stat(req.Dir)
	if err != nil {
		return fmt.Errorf("%w: Dir: %v", ErrInvalidRequest, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: Dir is not a directory", ErrInvalidRequest)
	}
	return nil
}

func run(ctx context.Context, req Request) (Result, error) {
	timeout := req.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	stdoutLimit := req.MaxStdoutBytes
	if stdoutLimit == 0 {
		stdoutLimit = DefaultMaxOutputBytes
	}
	stderrLimit := req.MaxStderrBytes
	if stderrLimit == 0 {
		stderrLimit = DefaultMaxOutputBytes
	}

	runCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	timer := time.AfterFunc(timeout, func() { cancel(errTimeout) })
	defer timer.Stop()

	stdout := newCappedWriter(stdoutLimit, func() { cancel(errOutputExceeded) })
	stderr := newCappedWriter(stderrLimit, func() { cancel(errOutputExceeded) })

	cmd := exec.CommandContext(runCtx, req.Argv[0], req.Argv[1:]...)
	cmd.Dir = req.Dir
	if req.Env == nil {
		cmd.Env = []string{}
	} else {
		cmd.Env = req.Env
	}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	configureCommand(cmd)
	cmd.Cancel = func() error { return killGroup(cmd) }
	cmd.WaitDelay = WaitDelay

	if err := cmd.Start(); err != nil {
		if errors.Is(context.Cause(runCtx), errTimeout) {
			return Result{TimedOut: true}, nil
		}
		return Result{}, err
	}

	waitErr := cmd.Wait()
	timer.Stop()
	cleanupGroup(cmd)

	outBytes, outTrunc := stdout.snapshot()
	errBytes, errTrunc := stderr.snapshot()

	res := Result{
		Stdout:          outBytes,
		Stderr:          errBytes,
		StdoutTruncated: outTrunc,
		StderrTruncated: errTrunc,
	}
	if cmd.ProcessState != nil {
		if code := cmd.ProcessState.ExitCode(); code >= 0 {
			res.ExitCode = &code
		}
	}

	cause := context.Cause(runCtx)
	switch {
	case errors.Is(cause, errOutputExceeded):
		res.OutputExceeded = true
		return res, nil
	case errors.Is(cause, errTimeout):
		// If the process exited normally despite the timer firing, prefer the
		// natural exit.
		if res.ExitCode != nil {
			return res, nil
		}
		res.TimedOut = true
		return res, nil
	case cause != nil:
		// Parent context cancellation (or a custom cause).
		if errors.Is(cause, context.DeadlineExceeded) {
			res.TimedOut = true
			return res, nil
		}
		return res, cause
	}

	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			if res.ExitCode != nil {
				return res, nil
			}
			// Killed by a signal Run did not cause.
			return res, waitErr
		}
		if errors.Is(waitErr, exec.ErrWaitDelay) {
			return res, nil
		}
		return res, waitErr
	}
	return res, nil
}

// cappedWriter retains at most limit bytes and discards the rest. It always
// reports a full write so the child is never blocked or errored by the cap.
type cappedWriter struct {
	mu        sync.Mutex
	buf       []byte
	limit     int
	truncated bool
	overflow  func()
}

func newCappedWriter(limit int, overflow func()) *cappedWriter {
	prealloc := limit
	if prealloc > 1<<20 {
		prealloc = 1 << 20
	}
	return &cappedWriter{
		buf:      make([]byte, 0, prealloc),
		limit:    limit,
		overflow: overflow,
	}
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	space := w.limit - len(w.buf)
	if space < 0 {
		space = 0
	}
	take := len(p)
	if take > space {
		take = space
	}
	if take > 0 {
		w.buf = append(w.buf, p[:take]...)
	}
	fire := false
	if take < len(p) && !w.truncated {
		w.truncated = true
		fire = true
	}
	w.mu.Unlock()
	if fire && w.overflow != nil {
		w.overflow()
	}
	return len(p), nil
}

func (w *cappedWriter) snapshot() ([]byte, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]byte, len(w.buf))
	copy(out, w.buf)
	return out, w.truncated
}
