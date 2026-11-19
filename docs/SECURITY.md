# Security

This document describes the security posture of `script-agent` as implemented
today. It is not a claim that the project is a general-purpose sandbox.

`script-agent` has two different execution models:

- **Python and JavaScript** run inside WASM guests with a narrow capability
  boundary. They have no ambient filesystem, process, network, or environment
  access. The host application grants filesystem operations through one
  workspace adapter.
- **Bash** runs as a host process with the host user's OS privileges. It is a
  controlled launcher, not a sandbox. `AllowWrites` does not confine Bash.

The host application is responsible for policy: which workspace to open, which
grants to enable, which source to run, and which programs Bash may execute.

## Privilege matrix

| Capability | Default | Opt-in | Notes |
| --- | --- | --- | --- |
| Workspace read | Allowed | — | Bounded by `MaxBytes` (script-agent passes `MaxOutputBytes`). |
| Workspace write | Denied | `Config.AllowWrites: true` or CLI `--write` | Read-only is the zero value. |
| Process execution | Denied | `Config.AllowProcess` **and** `Request.AllowProcess`; CLI `--allow-process` sets both | Bash only. Python/JavaScript reject `Request.AllowProcess`. |
| Network | None for Python/JavaScript | — | Bash can use the host network. |
| Host environment | None for Python/JavaScript | — | Bash gets a controlled environment, not the host environment. |
| Host clock/random | None for Python | — | JavaScript has a WASI clock/random shim; no ambient OS service. |
| Filesystem outside workspace | Denied for Python/JavaScript | — | Bash can leave the workspace. |
| Working directory | Workspace | — | Bash `Dir` is not a sandbox. |
| `--file` source | Explicit user path | — | The CLI reads a host file the user names; it may be outside the workspace. |

## Workspace filesystem

The shared capability adapter is `github.com/keejkrej/quickpy/capability`. It
opens the workspace once with `os.Root` and binds the capability to that
directory. Later working-directory changes do not move it.

- `ReadOnly: !AllowWrites`. The zero value is read-only. `fs_write_text`,
  `fs_write_bytes`, `fs_mkdir`, and `fs_remove` are denied with
  `permission_denied` before any mutation.
- `MaxBytes` bounds text and binary reads and writes. `script-agent` passes
  `MaxOutputBytes` as `MaxBytes`, so the output cap also bounds workspace file
  reads and writes. `MaxEntries` bounds `fs_listdir` (default 10000).
- Paths are POSIX relative paths inside the root. Absolute paths, UNC paths,
  backslashes, colons, NUL, and escaping `..` are rejected. Non-escaping `.`
  and `..` are cleaned. A trailing slash is accepted. `~` is a literal
  filename.
- Symlinks are followed only when they resolve inside the root. Absolute or
  escaping symlinks are `invalid_path`; symlink loops are `invalid_path`.
  `fs_listdir` and `fs_exists` use `lstat` semantics and never follow a symlink
  for metadata.
- Writes use a unique temporary file in the target directory and rename over
  the target. There is no `fsync`; a crash may leave a `.capability-tmp-*`
  file. Writes replace the directory entry, so writing to a final-component
  symlink or hardlink replaces the link, not the target.
- There is no recursive remove, append, copy, rename, or mode control. Files
  are created with mode `0600`; directories with `0755`.
- Operation errors are `*capability.Error` with static text and
  caller-relative paths. The host root is not included. `NewWorkspace`
  constructor errors are host-facing and may name the root.

### Known filesystem limitations

- **Hardlinks.** `os.Root` cannot distinguish a hardlink inside the root from a
  file created outside it. A hardlink to an outside inode is readable through
  the capability. Atomic writes replace the directory entry, so they do not
  modify the outside inode, but reads can still expose outside content. Do not
  let untrusted code create hardlinks into a capability root.
- **Special files.** Non-regular files are rejected after open with
  `io_error`. Opening a FIFO may block on some platforms before that check. Do
  not place FIFOs, devices, or sockets in a capability root.
- **Filesystem boundaries.** `os.Root` does not prohibit bind mounts, `/proc`
  special files, or device files. Choose a root that does not contain them.
- **Concurrent mutation.** `os.Root` prevents escape, not logical races.
  Concurrent mutation of the same path is not transactional.
- **No `fsync`.** Writes are atomic for readers but not crash-durable.
- **Windows.** The path policy rejects backslashes and colons. Some host errno
  distinctions may surface as `io_error` rather than a more specific code.

## Process execution

The `process` package is a low-level primitive, not a sandbox. See
[process/README.md](../process/README.md) and
[process/process.go](../process/process.go).

- `Argv` is a literal argument vector. It is never joined, quoted, split, or
  interpolated. A trusted caller may pass a shell such as
  `[]string{"bash", "--noprofile", "--norc", "-c", `source "$0"`, scriptPath}`;
  untrusted tools must never build a shell command line by concatenation.
- `Dir` must be an absolute path to an existing directory. `Run` never calls
  `os.Chdir`; the parent's working directory is unchanged. `Dir` is not a
  sandbox: the child can use absolute paths, `..`, and symlinks to leave it.
- `Env` is the child's complete environment. `nil` means an empty environment,
  never `os.Environ`. The executable is still resolved with the host `PATH`
  (the same lookup `exec.Command` performs), so a bare name such as `bash` is
  found even when the child environment has no `PATH`. Pass an explicit `PATH`
  in `Env` if the child needs one.
- `Timeout == 0` means `DefaultTimeout` (30s). A negative value is rejected.
- `MaxStdoutBytes`/`MaxStderrBytes == 0` mean `DefaultMaxOutputBytes` (1 MiB).
  Negative values are rejected.
- Output is raw bytes. Truncation is a byte-prefix clip; a truncated stream may
  end mid-UTF-8. `StdoutTruncated`/`StderrTruncated` are true only when bytes
  were dropped.
- A non-zero exit is a normal result: `ExitCode` is set and the error is nil. A
  signal death not caused by `Run` has a nil `ExitCode` and returns the
  `*exec.ExitError`.
- `TimedOut` covers both `Request.Timeout` and an earlier parent deadline.
  `OutputExceeded` is independent of `TimedOut`.
- Parent `context.Canceled` is returned as an error; partial output is still
  available in the `Result`.

### Cancellation and process groups

On POSIX (`darwin`, `linux`), the child is placed in its own process group
(`Setpgid`). Cancellation sends `SIGKILL` to the whole group, so grandchildren
do not survive. `Run` always makes a best-effort group kill before returning,
including after a normal exit. A child that daemonizes is not supported.
`cmd.WaitDelay` is 2 seconds so a grandchild holding the output pipes cannot
block `Run` indefinitely.

These are best-effort guarantees:

- PID reuse can make a late group signal ambiguous.
- A process that escapes the group or daemonizes is not guaranteed to be
  killed.
- The 2-second wait window bounds pipe-holding, not arbitrary process
  lifetime.

### Windows

`process.Run` returns an `unsupported` error without starting a process on
Windows. The package still compiles so cross-platform callers build, but there
is no process-tree guarantee. Bash is unavailable on Windows.

## Environment and ambient authority

### Python

- QuickPy/MicroPython 1.29.0, ABI 4. Fresh guest instance and fresh globals per
  execution; the runtime and tool registry persist at the engine level.
- No ambient filesystem, process, network, environment, clock, or random
  source. `time.time` is unsupported. `open()` is unsupported. `os`,
  `subprocess`, `pathlib`, and `tempfile` are not importable.
- `quickpy.fs` is the only filesystem surface. It is inert unless the host
  installs the tools. In `script-agent`, the tools are installed against the
  workspace adapter.
- `quickpy.fs` maps `not_found` to `OSError(2)`, `permission_denied` to
  `OSError(13)`, and `invalid_path` to `ValueError`. Other capability codes
  propagate as tool errors. There are no CPython `FileNotFoundError` or
  `PermissionError` subclasses.
- `quickpy.run(coro)` is submit-and-drive-to-quiescence. It must be the last
  top-level statement. Statements after it run before pending tool calls
  finish. A second `quickpy.run` raises.
- `quickpy.set_state(value)` / `quickpy.get_state()` carry a JSON-like value.
  The last successful write wins and commits only on successful completion.
  `set_state(None)` is distinct from no write.
- `str.encode()` and `bytes.decode()` are not available at this ROM level; use
  `bytes(text, "utf-8")` and `str(data, "utf-8")`.
- `json.dumps(float("inf"))` and `json.dumps(float("nan"))` emit invalid JSON
  tokens. The public state/tool value model stays strict-finite.
- `re.split` is not a module-level function; use
  `re.compile(pattern).split(text)`.
- `math` lacks `isclose`, `factorial`, `tau`, `inf`, and `nan`.

### JavaScript

- QuickJS-WASI 3.6.2 (QuickJS-NG 0.17.0). Fresh VM and fresh globals per
  execution; the sandbox and tool registry persist at the engine level.
- No ambient filesystem, process, network, or environment. No Node module
  loader, `require`, `Buffer`, `process`, `*Sync` methods, npm, or network.
- The WASI shim provides `clock_time_get` (Date.now and Math.random seeding),
  `random_get` (WASI libc init), `fd_write` (internal logging), and a few
  stubs. This is not ambient OS access; it is the engine's selected WASI
  surface.
- `fs` is a frozen namespace over the public `tools` object. It captures the
  intrinsics it needs before untrusted code runs and never references the
  private host bridge.
- `fs` methods: `readFile`, `writeFile`, `readdir`, `mkdir`, `rm`, `exists`.
  There are no `*Sync` methods. `fs.mkdir` returns a boolean; `fs.exists` is a
  custom helper; `fs.rm` removes a file, symlink, or empty directory only.
- Capability codes are mapped to Node-style `.code` values (`ENOENT`,
  `EACCES`, `EINVAL`, `EISDIR`, `ENOTDIR`, `EEXIST`, `ENOTEMPTY`, `EFBIG`,
  `EIO`). The original code is kept on `.capabilityCode`, and the relative path
  is kept on `.path`. Local validation uses `ERR_INVALID_ARG_TYPE`,
  `ERR_UNKNOWN_ENCODING`, and `ERR_UNSUPPORTED_OPERATION`.
- `store`/`load` are per-execution helpers, not a persistence mechanism. The
  prelude limits a single stored value to 256 KiB of JSON and the total to
  1 MiB.
- `image` accepts a base64 data URI for PNG, JPEG, GIF, or WebP. Remote image
  URLs are rejected. Image output is raw bytes base64-encoded in the result.
- `exit()` ends the execution successfully. `return` and top-level `await`
  work because the source is an async function body.

### Bash

- Host process with the host user's full OS privileges. Not a sandbox.
- Requires both `Config.AllowProcess` and `Request.AllowProcess`. The CLI
  `--allow-process` flag sets both. Python and JavaScript reject
  `Request.AllowProcess`.
- Controlled environment: fixed `PATH`, `HOME` and `PWD` set to the workspace,
  `LANG`/`LC_ALL` set to `C.UTF-8`, and a private `TMPDIR` created with
  `os.MkdirTemp` under the host `TMPDIR` and removed after the run. The host
  environment is not inherited. If the host `TMPDIR` points to a nonexistent
  directory, Bash execution fails with a process error; this is a current
  limitation, not a build blocker. Python and JavaScript use embedded direct
  bytes and do not need a writable `TMPDIR`.
- The source is one literal argv element after `-c`; the host does not
  interpolate it into a shell command line.
- `Dir` is the workspace, but the child can `cd ..`, use absolute paths, and
  follow symlinks. `AllowWrites` does not confine Bash.
- Bash can use the network, signal other processes, and read or write outside
  the workspace with the host user's authority.
- The result carries a warning that Bash runs with full OS privileges. The CLI
  also writes a short summary to stderr for non-completed results.

## Tool callbacks and host trust

The `tools` object in JavaScript and `quickpy.tools` in Python are host
callbacks. They execute Go code in the host process. The guest can call them
with arbitrary JSON-like arguments; the host adapter validates arguments and
enforces the capability policy.

- The eight filesystem tools are implemented by the shared capability adapter.
  The adapter validates operation names, argument keys, value types, paths, and
  limits before touching the filesystem.
- The JavaScript `fs` facade is a trusted prelude over the public `tools`
  object. It captures the intrinsics it needs before untrusted code runs and
  never references the private host bridge. The guest can still call
  `tools.fs_*` directly; the adapter is the security boundary.
- The Python `quickpy.fs` facade is the same boundary in Python form. The guest
  can call `quickpy.tools.fs_*` directly; the adapter is the security boundary.
- The host must not register tools that grant more authority than intended.
  A tool implementation is host code; a bug in a tool is a host-code bug, not a
  guest sandbox escape.
- The host must treat tool arguments as untrusted. The capability adapter does
  this for the eight filesystem operations; any additional tool must do the
  same.

The high-risk surface is the host callback, not the WASM guest alone. The
QuickJS host bridge and the QuickPy tool bridge are internal, but the `tools`
callbacks are intentionally reachable from generated code.

## CLI input and `--file`

- `--workspace` is required for execution. It must be an existing directory and
  is resolved to an absolute path once.
- `--code` and `--file` are mutually exclusive and exactly one is required.
- `--file` reads a host file path supplied by the user. The file may be outside
  the workspace. This is an explicit user input, not an untrusted guest
  filesystem capability. The CLI reads at most `maxSource+1` bytes so an
  oversized file is detected without allocating the whole file.
- `--write` and `--readonly` are mutually exclusive. The default is read-only.
- `--allow-process` is only valid with `--lang bash`.
- `--timeout` must be greater than zero. `--max-output-bytes` must be in
  `1..1 GiB`. `--max-source-bytes` must be in `1..256 MiB`.
- `--version` needs no workspace and does not execute a script.
- stdout is exactly one JSON document. Script output is in `output`, not raw
  stdout. stderr is a short human-readable summary for non-completed results;
  it is not structured and is not part of the JSON contract.

## Error handling and sanitization

- Capability `Handle` errors are `*capability.Error` with static text and
  caller-relative paths. The host root is redacted. `NewWorkspace` constructor
  errors are host-facing and may name the root.
- JavaScript `fs` errors carry `.code`, `.capabilityCode`, and `.path`. The
  message comes from the capability layer and does not include the root.
- Python `quickpy.fs` errors are `OSError`, `ValueError`, or tool errors. The
  message comes from the capability layer and does not include the root.
- Bash process errors are host-facing and may include host paths, executable
  names, or `Dir` values. They are not sanitized like capability errors.
- CLI `--file` errors include the path the user supplied. This is explicit user
  input.
- `Config.Workspace` errors may name the path.
- `Result.Error.Stack` may include guest stack or traceback text and source
  filenames. It does not include the host root unless the guest saw it.
- `Result.Warnings` are advisory. They are not security guarantees.

## Platform support

| Platform | Python | JavaScript | Bash | Notes |
| --- | --- | --- | --- | --- |
| `darwin` | Yes | Yes | Yes (POSIX) | Local development target. |
| `linux` | Yes | Yes | Yes (POSIX) | CI target (amd64). |
| `windows` | Builds; not tested/supported | Builds; not tested/supported | Unsupported | `process.Run` returns `unsupported`; no process-tree guarantee. |

Do not claim Windows support. The Go code compiles for Windows, but Bash is
unavailable and Python/JavaScript are not covered by CI or tests on Windows.

## License and redistribution

License and redistribution choices are not a runtime security control. There is
no root `LICENSE` file in this repository, and no license is granted for the
original `script-agent` code. The vendored `third_party/pi-go` source and the
`github.com/keejkrej/quickpy` dependency also have no license file at their
pinned revisions. The owner must choose licenses before external
redistribution. See [THIRD_PARTY_NOTICES.md](../THIRD_PARTY_NOTICES.md).

## Reporting

No security contact or disclosure policy is defined in this repository yet.
The owner must add one before external distribution.
