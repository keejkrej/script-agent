# Architecture

This document describes the repository boundaries, execution flow, engines,
capability adapter, artifacts, and design limits of `script-agent`. It is
written against the current code, not a future design.

## Purpose and boundaries

`script-agent` is a coordinator. It owns:

- one explicit workspace root,
- one runtime-neutral capability adapter,
- one engine per language,
- one concurrency semaphore and lifecycle.

It does not own a model provider, a chat loop, a persistent environment, or a
daemon. A caller constructs an `Agent`, submits a `Request`, and receives a
`Result`.

The public surface is in [api.go](../api.go) and [agent.go](../agent.go). The
engine boundary is in [engine.go](../engine.go). The CLI is in
[cmd/script-agent/main.go](../cmd/script-agent/main.go).

## Repository layout

| Path | Role |
| --- | --- |
| `api.go`, `agent.go`, `engine.go` | Public types, coordinator, engine boundary. |
| `python.go` | QuickPy/MicroPython engine. |
| `javascript.go` | pi-go codemode QuickJS engine. |
| `bash.go`, `bash_process.go` | Bash engine and process adapter. |
| `adapter/quickjs/` | Capability tools and the frozen `fs` prelude. |
| `process/` | Low-level process primitive. |
| `internal/artifacts/` | Embedded QuickPy wasm and metadata. |
| `cmd/script-agent/` | CLI. |
| `cmd/script-agent-artifacts/` | Artifact list/verify helper. |
| `integration/` | Opt-in end-to-end tests for the native binary and public API. |
| `scripts/rebuild-quickpy.sh` | Reproducible QuickPy rebuild. |
| `third_party/pi-go/` | Minimal source-vendored codemode runtime. |

## Module and dependency graph

The root module is `github.com/keejkrej/script-agent`. Its
[go.mod](../go.mod) declares:

- `go 1.27.3`
- `toolchain go1.27.4`
- direct `github.com/keejkrej/pi-go v0.0.0`
- direct `github.com/keejkrej/quickpy v0.0.0-20261118233109-3897a879f468`
- indirect `github.com/tetratelabs/wazero v1.12.0`
- indirect `golang.org/x/sys v0.48.0`
- indirect `golang.org/x/text v0.43.0`

The only `replace` is:

```go
replace github.com/keejkrej/pi-go => ./third_party/pi-go
```

There is no `go.work` and no machine-local replace. `go.work` is ignored by
`.gitignore`.

Go ignores `replace` directives in dependencies. A module outside this
repository that imports `github.com/keejkrej/script-agent` must clone this
repository and add an explicit replace pointing at the actual clone path, for
example:

```go
replace github.com/keejkrej/pi-go => /path/to/script-agent/third_party/pi-go
```

The vendored content is not downloaded transitively, and generic
`go get`/`go install` is not a supported portable SDK path for v0.1. The CLI
and tests in this repository work without an extra replace.

`third_party/pi-go` is a **minimal, source-vendored copy** of the generic
codemode runtime, not a fork of the whole upstream repository. Its module path
is kept as `github.com/keejkrej/pi-go` so the vendored `internal/...` imports
stay legal. The upstream commit is
`69d43f6d9a4b83333d4582ca8c755457702d6576`. See
[third_party/pi-go/PROVENANCE.md](../third_party/pi-go/PROVENANCE.md).

`github.com/keejkrej/quickpy` is a direct module dependency, not vendored. The
pinned pseudo-version resolves to commit
`3897a879f468c47cb5c70235e3cf9872ab7fc8a8`. See
[internal/artifacts/PROVENANCE.md](../internal/artifacts/PROVENANCE.md).

The vendored module's [go.mod](../third_party/pi-go/go.mod) requires wazero
v1.12.0 and x/text v0.43.0, with x/sys v0.48.0 indirect. QuickPy's own `go.mod`
lists x/sys v0.44.0 indirect, but the root module graph selects x/sys v0.48.0.
The build uses the root's selected version. This is a module graph note, not a
local replace.

## Execution flow

1. `New(Config)` resolves `Workspace` to an absolute path, stats it, and opens
   `capability.NewWorkspace` with:
   - `Root: abs`
   - `ReadOnly: !AllowWrites`
   - `MaxBytes: int64(MaxOutputBytes)`
2. The Agent creates one engine per language. Python and JavaScript engines are
   lazy; Bash is cheap to construct.
3. `Run(ctx, Request)` validates language, source size, timeout, output cap,
   and process grants. It acquires a semaphore slot (`MaxExecutions`), creates a
   timeout context, and dispatches to the engine.
4. The engine returns an internal `engineResult`. The coordinator maps it to the
   public `Result` and measures `Duration`.
5. `Close(ctx)` stops admission, waits for in-flight executions, closes the
   engines, and closes the capability adapter.

`Run` never returns a Go error. Invalid requests, denials, timeouts,
cancellations, limits, and script failures are `Result` statuses. `New` returns
an error only for an invalid configuration.

## Python engine

[python.go](../python.go) runs Python through QuickPy.

- The runtime is created lazily with `quickpy.New`:
  - `Artifact: quickpy.ArtifactSource{Bytes: artifacts.QuickPyWASM()}`
  - `MaxMemoryBytes: Config.MaxPythonMemoryBytes`
  - `MaxExecutions: Config.MaxExecutions`
  - `DefaultTimeout: Config.Timeout`
  - `MaxSourceBytes: Config.MaxSourceBytes`
  - `MaxOutputBytes: Config.MaxOutputBytes`
- `quickpybridge.Install(rt, adapter)` registers the eight `fs_*` tools once.
- Each `Execute` gets a fresh guest instance and fresh globals. The runtime and
  tool registry persist at the engine level.
- `exec.Wait` uses a hard cap of `req.Timeout + 5s` independent of the caller's
  context. The runtime cancels on the caller's context; the cap protects
  against a stuck engine.
- `StateWritten` becomes `Result.Value`. `set_state(None)` is a present `null`,
  distinct from no write.
- Output mapping:
  - `TextEvent` -> `kind: "text"`, `stream: "stdout"`.
  - `ConsoleEvent` -> `kind: "text"`, `stream: "stderr"`, with `level`.
  - `ImageEvent` -> `kind: "image"`, `stream: "stdout"`, `mime`, raw `data`.

The QuickPy guest is MicroPython 1.29.0, ABI 4. It has no ambient clock or
random source; `time.time` is unsupported. `open()` is unsupported, and `os`,
`subprocess`, `pathlib`, and `tempfile` are not importable. `quickpy.fs` is the
only filesystem surface, and it is inert unless the host installs the tools.

`quickpy.run(coro)` is submit-and-drive-to-quiescence, not a blocking
`asyncio.run`. It must be the last top-level statement. Statements after it run
before pending tool calls finish. A second `quickpy.run` raises.

`quickpy.fs` maps `not_found` to `OSError(2)`, `permission_denied` to
`OSError(13)`, and `invalid_path` to `ValueError`. Other capability codes
propagate as tool errors. There are no CPython `FileNotFoundError` or
`PermissionError` subclasses.

## JavaScript engine

[javascript.go](../javascript.go) runs JavaScript through the pi-go codemode
QuickJS sandbox.

- The sandbox is created lazily with:
  - `TimeoutMs` from `Config.Timeout`
  - `MemoryLimitBytes: 256 MiB`
  - `MaxOutputBytes: Config.MaxOutputBytes`
  - `TrustedPreludeSource: quickjs.FacadeSource`
- `quickjs.Install(sb, adapter)` registers the eight capability tools once.
- Each `Execute` gets a fresh VM and fresh globals. The sandbox and tool
  registry persist at the engine level.
- The source is evaluated as an async function body:
  `(async (tools, console) => { ...source... })`. Top-level `await` and
  `return` work.
- Globals: `tools`, `ALL_TOOLS`, `text`, `image`, `console`, `store`, `load`,
  `exit`, and the frozen `fs` namespace.
- `store`/`load` are per-execution helpers. They are not a persistence
  mechanism across executions.
- `fs` is a trusted prelude over the public `tools` object. It captures the
  intrinsics it needs before untrusted code runs and never references the
  private host bridge. See [adapter/quickjs/facade.go](../adapter/quickjs/facade.go)
  and [adapter/quickjs/bridge.go](../adapter/quickjs/bridge.go).

The JavaScript environment is not Node:

- no module loader, `require`, `Buffer`, `process`, npm, or network;
- no `*Sync` filesystem methods;
- `fs` is a small async subset: `readFile`, `writeFile`, `readdir`, `mkdir`,
  `rm`, `exists`;
- `fs.mkdir` returns a boolean, not Node's first-created path;
- `fs.exists` is a custom helper;
- `fs.rm` removes a file, symlink, or empty directory only;
- `fs.writeFile` and `fs.readdir` reject unsupported options.

Capability codes are mapped to Node-style `.code` values (`ENOENT`, `EACCES`,
`EINVAL`, `EISDIR`, `ENOTDIR`, `EEXIST`, `ENOTEMPTY`, `EFBIG`, `EIO`). The
original code is kept on `.capabilityCode`, and the relative path is kept on
`.path`. Local validation uses `ERR_INVALID_ARG_TYPE`, `ERR_UNKNOWN_ENCODING`,
and `ERR_UNSUPPORTED_OPERATION`.

The vendored QuickJS runtime uses a WASI shim for `clock_time_get` (Date.now and
Math.random seeding), `random_get` (WASI libc init), `fd_write` (internal
logging), and a few stubs. It does not provide ambient filesystem, network, or
process access. See
[third_party/pi-go/internal/quickjs/wasi_shim.go](../third_party/pi-go/internal/quickjs/wasi_shim.go).

## Bash engine

[bash.go](../bash.go) and [bash_process.go](../bash_process.go) run Bash as a
host process.

- `findBash` prefers `/bin/bash` and falls back to `exec.LookPath("bash")`.
- The environment is complete and controlled:
  - `PATH=/usr/local/bin:/usr/bin:/bin`
  - `HOME=<workspace>`
  - `PWD=<workspace>`
  - `LANG=C.UTF-8`
  - `LC_ALL=C.UTF-8`
  - `TMPDIR=<private temp dir>`
- The host environment is never inherited. The private temp dir is created
  with `os.MkdirTemp` under the host `TMPDIR` and removed after the run. A
  nonexistent host `TMPDIR` makes Bash fail with a process error; this is a
  current limitation, not a build blocker. Python and JavaScript use embedded
  direct bytes and do not need a writable `TMPDIR`.
- The argv is `[bash, --noprofile, --norc, -c, source, script-agent]`. The
  source is one literal argv element; the host does not interpolate it.
- `process.Run` receives `Dir=<workspace>`, the timeout, and
  `MaxStdoutBytes=MaxStderrBytes=MaxOutputBytes`.
- The process package places the child in its own process group on POSIX
  (`Setpgid`), sends `SIGKILL` to the group on cancellation, and uses a
  2-second `WaitDelay` so a grandchild holding the output pipes cannot block
  forever.
- A non-zero exit is a normal result with `ExitCode` set. A signal death not
  caused by `Run` has a nil `ExitCode` and returns the `*exec.ExitError`.
- Bash is not a sandbox. `Dir` is only a working directory; the child can use
  absolute paths, `..`, and symlinks to leave it. Bash runs with the host user's
  full OS privileges. The result carries a warning.

On Windows, `process.Run` returns an `unsupported` error without starting a
process. The package still compiles, but Bash is unavailable.

## Capability adapter v1

The shared adapter is `github.com/keejkrej/quickpy/capability`. It is a
runtime-neutral, in-process data ABI, not a network protocol.

`capability.NewWorkspace` opens the root once with `os.Root` and binds the
capability to that directory. The eight operations are:

| Operation | Arguments | Result |
| --- | --- | --- |
| `fs_read_text` | `path`; optional `encoding` (default `utf-8`) | `{"text": string}` |
| `fs_write_text` | `path`; `text` | `{"written": int64}` |
| `fs_read_bytes` | `path` | `{"base64": string}` |
| `fs_write_bytes` | `path`; `base64` | `{"written": int64}` |
| `fs_exists` | `path` | `{"exists": bool}` |
| `fs_listdir` | optional `path` (default `.`) | `{"entries": []any}` |
| `fs_mkdir` | `path`; optional `parents` (default `false`) | `{"created": bool}` |
| `fs_remove` | `path` | `{"removed": bool}` |

Values are JSON-like Go values only: `nil`, `bool`, `int64`, `float64`,
`string`, `[]any`, `map[string]any`. There is no Python object model and no
implicit conversion.

Containment and limits:

- `os.Root` guards the actual open, create, rename, and remove. The package
  does not rely on check-then-use path validation for security.
- Paths are POSIX relative paths inside the root. Absolute paths, UNC paths,
  backslashes, colons, NUL, and escaping `..` are rejected. Non-escaping `.`
  and `..` are cleaned. A trailing slash is accepted. `~` is a literal
  filename.
- Symlinks are followed only when they resolve inside the root. Absolute or
  escaping symlinks are `invalid_path`; symlink loops are `invalid_path`.
- `fs_listdir` and `fs_exists` use `lstat` semantics and never follow a symlink
  for metadata.
- `MaxBytes` bounds text and binary reads and writes. `MaxEntries` bounds
  `fs_listdir` (default 10000). A zero value selects the default; it never
  means unbounded.
- Writes use a unique temporary file in the target directory and rename over
  the target. There is no `fsync`; a crash may leave a `.capability-tmp-*`
  file. Writes replace the directory entry, so writing to a final-component
  symlink or hardlink replaces the link, not the target.
- There is no recursive remove, append, copy, rename, or mode control. Files
  are created with mode `0600`; directories with `0755`.
- Operation errors are `*capability.Error` with static text and
  caller-relative paths. The host root is not included. `NewWorkspace`
  constructor errors are host-facing and may name the root.

Known limitations are documented in [docs/SECURITY.md](SECURITY.md): hardlinks,
FIFOs/devices, bind mounts, concurrent mutation, and no `fsync`.

Both `quickpybridge` and `adapter/quickjs` install the same eight operations.
Python and JavaScript are coupled through this adapter, not through a shared
interpreter.

## Artifacts and provenance

### QuickPy

- File: `internal/artifacts/quickpy.wasm`
- Size: 256486 bytes
- SHA-256: `5fdd351a70e0f65a4b812ad21c34a0885f87e839aee7721164768907ad61f50a`
- Guest ABI: 4
- Engine: MicroPython 1.29.0
- Source: `github.com/keejkrej/quickpy` commit
  `3897a879f468c47cb5c70235e3cf9872ab7fc8a8`
- Build toolchain: Zig 0.17.1, wasm-opt (binaryen) 133
- Embedded by [internal/artifacts/artifacts.go](../internal/artifacts/artifacts.go)
  via `//go:embed`.

The Python engine passes the embedded bytes directly to QuickPy. QuickPy
compiles the bytes and does not create a temporary artifact file. The Python
engine does not require a writable TMPDIR at runtime.

### QuickJS

- File: `third_party/pi-go/internal/quickjs/quickjs.wasm`
- Size: 637405 bytes
- SHA-256: `d4c9375f2b1ca4dc95f72c8aa2982a7a9951ac8011490d79c6582df732b4bbd9`
- Engine: QuickJS-WASI 3.6.2 (QuickJS-NG 0.17.0)
- Source: copied byte-for-byte from upstream pi-go at commit
  `69d43f6d9a4b83333d4582ca8c755457702d6576`
- Embedded by the vendored `internal/quickjs` package via `//go:embed`.

The application does not carry a second copy. The JavaScript engine does not
require a writable TMPDIR at runtime.

### Verification

```sh
go run ./cmd/script-agent-artifacts list
go run ./cmd/script-agent-artifacts verify
go run ./cmd/script-agent-artifacts verify --repo .
```

`list` prints the QuickJS and QuickPy paths, sizes, SHA-256 values, and engine
strings. `verify` checks the on-disk copies and the embedded QuickPy bytes.
`--repo` defaults to the nearest parent of the working directory whose `go.mod`
declares this module.

### Rebuild

`scripts/rebuild-quickpy.sh` is the only supported QuickPy rebuild path. It:

- requires Zig 0.17.1 and wasm-opt 133;
- clones the pinned QuickPy commit into a throwaway directory;
- builds a clean guest;
- verifies the exact size and SHA-256;
- refuses to overwrite the committed artifact on any mismatch.

The QuickJS artifact is copied from upstream pi-go. There is no separate
QuickJS rebuild script in this repository; follow
[third_party/pi-go/PROVENANCE.md](../third_party/pi-go/PROVENANCE.md) when
updating it.

## Vendored pi-go deviations

The vendored runtime is a minimal production closure, not a fork of the whole
repository. The only expected differences from upstream `69d43f6` are:

1. **`TrustedPreludeSource`** (`codemode/types.go`,
   `codemode/runtime/{host,protocol,worker,prelude_source}.go`). A trusted
   prelude is evaluated in every fresh VM after the codemode prelude installs
   tools/globals/output helpers and before the user script. It runs in global
   script mode with the filename `codemode-trusted-prelude.js`. A prelude that
   fails to compile or evaluate fails every execution with kind `sandbox` and a
   message that includes the source filename. The prelude never receives the
   private host bridge.
2. **`CodemodeErrorCoder` / `CodemodeErrorCode`** (`codemode/types.go`,
   `codemode/runtime/host.go`, `codemode/runtime/prelude_source.go`,
   `codemode/runtime/protocol.go`, `codemode/runtime/worker.go`). A tool error
   that implements `Code() string` attaches a stable `.code` to the guest's
   rejected `Error`. Codes are limited to 64 characters from
   `[A-Za-z0-9_.:-]`; anything else is dropped and the error keeps its normal
   message-only form. Ordinary Go errors are unchanged.
3. **`CodemodeSandboxOptions.MaxOutputBytes`** (local additive change, not in
   the archived patch). An optional retained-output budget. Text and console
   output count UTF-8 bytes; image output counts decoded image bytes, never the
   base64 characters. When the next item would exceed the budget it is clipped
   to the remaining bytes (text at a UTF-8 boundary, image to a raw byte prefix
   and re-encoded) and the execution fails with the new kind `limit`. Output
   produced before the limit is retained. `nil` or a non-positive value keeps
   the original unlimited behavior, so the library API is not broken when the
   option is unset.

The vendored module has its own small `go.mod` with only the modules the
vendored closure imports. It does not import the upstream `go.mod`/`go.sum`
dependency set, and it has no local-workspace replace.

## Limits and design constraints

| Limit | Default | Notes |
| --- | ---: | --- |
| `MaxOutputBytes` | 1 MiB | Script output and capability `MaxBytes`. |
| `MaxSourceBytes` | 256 KiB | Source text. |
| `Timeout` | 30s | Per execution. |
| `MaxPythonMemoryBytes` | 64 MiB | QuickPy guest memory cap. |
| `MaxExecutions` | 4 | Concurrent slots. CLI uses 1. |
| JavaScript memory | 256 MiB | Fixed in `javascript.go`. |
| `MaxOutputBytesLimit` | 1 GiB | Caller-supplied cap. |
| `MaxSourceBytesLimit` | 256 MiB | Caller-supplied cap. |

Output is retained in memory and bounded. QuickPy enforces the budget at the
WASM call boundary, not by sampling a live stream. The vendored JavaScript
runtime adds a retained-output budget: when the next item would exceed the
budget it is clipped and the execution fails with kind `limit`. Output produced
before the limit is retained. `script-agent` normalizes both to
`StatusLimitExceeded` (`limit_exceeded`).

Binary data crosses the capability/tool boundary as base64 in both the Python
and JavaScript wrappers. Both sides encode/decode and copy, so large binary
reads and writes have memory cost. `MaxBytes` bounds the payload.

All runtime dependencies are Go modules compiled into the binary. There is no
Node or CPython runtime dependency. The first build downloads public Go
modules; the repository is not fully offline without a populated module cache.

## Verification commands

```sh
go test ./...
go test -race ./...
go vet ./...
go test -tags scriptagent_integration -run TestIntegration -count=1 .
go test -tags scriptagent_integration -count=1 ./integration
(cd third_party/pi-go && go test ./codemode/...)
go run ./cmd/script-agent-artifacts list
go run ./cmd/script-agent-artifacts verify
```

See [docs/DEVELOPMENT.md](DEVELOPMENT.md) for build, cross-build, and CI
details, and [integration/README.md](../integration/README.md) for the
end-to-end suite.
