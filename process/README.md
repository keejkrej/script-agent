# process

`process.Run` runs one caller-supplied external process with bounded output
capture, an explicit working directory, an explicit environment, and
process-group cancellation.

## Contract

```go
type Request struct {
    Argv           []string
    Dir            string
    Env            []string
    Timeout        time.Duration
    MaxStdoutBytes int
    MaxStderrBytes int
}

type Result struct {
    Stdout, Stderr                   []byte
    StdoutTruncated, StderrTruncated bool
    ExitCode                         *int
    TimedOut, OutputExceeded         bool
}

func Run(ctx context.Context, req Request) (Result, error)
```

- `Argv` is a literal argument vector. It is never joined, quoted, split, or
  interpolated. A trusted caller may pass a shell such as
  `[]string{"bash", "--noprofile", "--norc", "-c", `source "$0"`, scriptPath}`;
  untrusted tools must never build a shell command line by concatenation.
- `Dir` must be an absolute path to an existing directory. `Run` never calls
  `os.Chdir`; the parent's working directory is unchanged.
- `Env` is the child's complete environment. `nil` means an empty environment,
  never `os.Environ`. The executable is still resolved with the host `PATH`
  (the same lookup `exec.Command` performs).
- `Timeout == 0` means `DefaultTimeout` (30s). A negative value is rejected.
- `MaxStdoutBytes`/`MaxStderrBytes == 0` mean `DefaultMaxOutputBytes` (1 MiB).
  Negative values are rejected.
- Output is raw bytes. Truncation is a byte-prefix clip; a truncated stream may
  end mid-UTF-8. `StdoutTruncated`/`StderrTruncated` are true only when bytes
  were dropped, so output exactly at the cap is not truncated.
- A non-zero exit is a normal result: `ExitCode` is set and the error is nil.
  A signal death not caused by `Run` has a nil `ExitCode` and returns the
  `*exec.ExitError`.
- `TimedOut` covers both `Request.Timeout` and an earlier parent deadline.
  `OutputExceeded` is independent of `TimedOut`.
- Parent `context.Canceled` is returned as an error; partial output is still
  available in the `Result`.

## Cancellation

On POSIX the child is placed in its own process group (`Setpgid`). Cancellation
sends `SIGKILL` to the whole group, so grandchildren do not survive. `Run`
always makes a best-effort group kill before returning, including after a
normal exit; a child that daemonizes is not supported. `cmd.WaitDelay` is
`WaitDelay` (2s) so a grandchild holding the output pipes cannot block `Run`
indefinitely.

## Platform support

- `darwin`, `linux`: full support.
- `windows`: `Run` returns an `unsupported` error without starting a process.
  The package still compiles so cross-platform callers build, but there is no
  process-tree guarantee.

## Security notes

`Run` is not a sandbox. `Dir` is only a working directory: the child can use
absolute paths, `..`, and symlinks to leave it. `Run` does not confine
filesystem, network, or signal access, and it does not decide which programs
may run. The caller is responsible for policy and for only passing an `Argv`
the application has authorized.
