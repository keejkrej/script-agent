# script-agent

A native Go library and CLI for running one Python, JavaScript, or Bash script
in an explicit workspace and returning a structured JSON result.

`script-agent` is an execution coordinator, not an agent loop. It owns one
workspace, one runtime-neutral capability adapter, and one engine per language.
It does not provide a model provider, a chat loop, a persistent environment, or
a daemon.

**Status:** candidate `0.1.0` native execution core. It is working and
independently reviewed, but it is not production GA, not a Prime/RLM agent, and
not a general OS sandbox.

## What works now

- **Native Go library** (`github.com/keejkrej/script-agent`) and **CLI**
  (`cmd/script-agent`).
- **Python** through the QuickPy WASM guest: MicroPython 1.29.0, guest ABI 4,
  embedded artifact.
- **JavaScript** through the pi-go codemode QuickJS sandbox: QuickJS-WASI 3.6.2
  (QuickJS-NG 0.17.0), embedded artifact, frozen `fs` namespace.
- **Bash** through a host process with a controlled environment and
  process-group cancellation on POSIX.
- **One explicit workspace**, read-only by default. `AllowWrites: true` (or
  `--write`) is the explicit write opt-in.
- **Shared capability adapter v1** for Python and JavaScript. The same eight
  filesystem operations back both languages.
- **Structured results**: status, ordered output, structured error, Bash exit
  code, warnings, and duration.
- **Stable CLI JSON contract** with documented exit codes.
- **Artifact verification** through `cmd/script-agent-artifacts`.
- **Opt-in end-to-end suite** in `integration/` that builds the native binary
  and verifies the CLI and library contract.

## What this is not

- Not a full Prime/RLM agent, provider daemon, or persistent environment.
- Not a live interpreter or serialized process context. Each execution gets
  fresh globals (Python/JavaScript) or a fresh process (Bash). Workspace files
  persist; interpreter state does not.
- Not CPython, not Node.js, and not a general OS sandbox.
- Not a Windows process runner. Bash/process execution is unsupported on
  Windows; Python and JavaScript are not a tested Windows target.
- Not a package manager or network client. Python and JavaScript have no
  ambient network access; Bash runs with the host user's authority.

## Quick start

Build the native binary:

```sh
CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' ./cmd/script-agent
mkdir -p /tmp/ws
```

`--version` needs no workspace and reports the exact language list and embedded
Python artifact metadata:

```sh
./script-agent --version
```

```json
{"languages":["python","javascript","bash"],"pythonArtifact":{"name":"quickpy.wasm","size":256486,"sha256":"5fdd351a70e0f65a4b812ad21c34a0885f87e839aee7721164768907ad61f50a","abi":4,"engine":"MicroPython 1.29.0"},"version":"0.1.0"}
```

### Python

```sh
./script-agent --lang python --workspace /tmp/ws --code '
import json
print(json.dumps({"ok": True}))
'
```

The result is one JSON document on stdout. Script output is in `output`, not
raw stdout:

```json
{"language":"python","status":"completed","output":[{"kind":"text","stream":"stdout","text":"{\"ok\": true}\n"}],"duration":74401792}
```

`duration` is an integer number of nanoseconds and varies per run.

Python filesystem access goes through the async `quickpy.fs` facade. Writes
require `--write` (or `AllowWrites: true`):

```sh
./script-agent --lang python --workspace /tmp/ws --write --code '
import quickpy

async def main():
    await quickpy.fs.write_text("hello.txt", "hello from python")
    print(await quickpy.fs.read_text("hello.txt"))

quickpy.run(main())
'
```

`quickpy.run(main())` must be the last top-level statement. Statements after it
run before pending tool calls finish. A second `quickpy.run` raises.

### JavaScript

```sh
./script-agent --lang javascript --workspace /tmp/ws --code '
const entries = await fs.readdir(".");
text(JSON.stringify(entries));
return { ok: true, n: 3 };
'
```

The source is evaluated as an async function body, so top-level `await` and
`return` work. `return` is optional. `text()` is the explicit output helper;
`console.log` also works. `fs` is a frozen namespace. A `return 4` produces
`"value":4`; a returned object produces a JSON object.

```json
{"language":"javascript","status":"completed","output":[{"kind":"text","stream":"stdout","text":"[]"}],"warnings":["JavaScript runs in a QuickJS sandbox, not Node.js: there is no module loader, npm, or network access."],"duration":157164792,"value":{"ok":true,"n":3}}
```

### Bash

Bash requires the process grant. The CLI flag sets both the Agent-level and
Request-level grants:

```sh
./script-agent --lang bash --workspace /tmp/ws --allow-process --code 'printf "hello from bash\n"'
```

```json
{"language":"bash","status":"completed","output":[{"kind":"text","stream":"stdout","text":"hello from bash\n"}],"exitCode":0,"warnings":["bash runs with the host user's full OS privileges; only run trusted scripts."],"duration":3551041}
```

Bash is not sandboxed. It runs with the host user's OS privileges and can read
or write outside the workspace. Only run trusted scripts.

### Cross-language workspace

Each invocation is a separate process. Workspace files persist; interpreter
state does not.

```sh
# JavaScript writes a file.
./script-agent --lang javascript --workspace /tmp/ws --write --code '
await fs.writeFile("shared.txt", "from js\n");
text("wrote");
'

# Python reads it. Python async filesystem calls need quickpy.run(main()) last.
./script-agent --lang python --workspace /tmp/ws --code '
import quickpy

async def main():
    print(await quickpy.fs.read_text("shared.txt"))

quickpy.run(main())
'

# JavaScript reads it back as UTF-8 text.
./script-agent --lang javascript --workspace /tmp/ws --code '
const value = await fs.readFile("shared.txt", "utf8");
text(value.trim());
'
```

The three results contain `wrote`, `from js\n\n`, and `from js` respectively.
The extra newline in the Python result is the file's trailing newline plus
`print`'s newline.

## CLI reference

```text
script-agent --lang <python|javascript|bash> (--code <source> | --file <path>) \
    --workspace <dir> [--write|--readonly] [--allow-process] \
    [--timeout 30s] [--max-output-bytes N] [--max-source-bytes N]
```

| Flag | Meaning | Default / constraint |
| --- | --- | --- |
| `--lang` | Language name. Aliases: `py`, `python3`, `py3`, `js`, `node`, `nodejs`, `sh`, `shell`. | Required. Canonical result value is `python`, `javascript`, or `bash`. |
| `--code` | Inline source. | Exactly one of `--code` or `--file`. |
| `--file` | Source file path. | Exactly one of `--code` or `--file`. The path is read from the host; it may be outside the workspace. |
| `--workspace` | Existing workspace directory. | Required. Resolved to an absolute path once. |
| `--write` | Allow workspace writes. | Off by default. Mutually exclusive with `--readonly`. |
| `--readonly` | Explicit read-only marker. | Default posture. Mutually exclusive with `--write`. |
| `--allow-process` | Allow Bash process execution. | Off by default. Bash only; rejected for Python and JavaScript. |
| `--timeout` | Execution timeout. | `30s`; must be greater than zero. |
| `--max-output-bytes` | Output cap. | `1 MiB`; must be in `1..1 GiB`. |
| `--max-source-bytes` | Source cap. | `256 KiB`; must be in `1..256 MiB`. |
| `--version` | Print version and artifact JSON. | No workspace needed. |

`--code` and `--file` are mutually exclusive and exactly one is required.
`--workspace` is always required for execution. `--write` and `--readonly` are
mutually exclusive. `--allow-process` is only valid with `--lang bash`.

### Exit codes

| Code | Status | Meaning |
| --- | --- | --- |
| `0` | `completed` | Success. |
| `1` | `failed` | Script or process failure. |
| `2` | `invalid_request` | Bad flags, source, workspace, or request. |
| `3` | `denied` | Capability or process grant denied. |
| `4` | `stalled` | The guest stopped making progress without settling. |
| `5` | `unsupported` | Language or host facility is unavailable in this build. |
| `124` | `timed_out` | The execution deadline expired. |
| `130` | `cancelled` | The caller cancelled the execution. |
| `134` | `limit_exceeded` | Output, source, memory, or capacity limit reached. |

Process denial is exit code `3` (`denied`), not `5` (`unsupported`). A missing
Bash binary or an unsupported platform is `5`.

### JSON contract

- **stdout is exactly one JSON document.** Script stdout, stderr, console
  diagnostics, and images are fields of that document. Do not parse script
  output from the process stdout stream.
- **stderr is a short human-readable summary** for a non-completed result. It
  is not structured and is not part of the JSON contract.
- `language`: canonical language string.
- `status`: one of `completed`, `failed`, `timed_out`, `cancelled`,
  `limit_exceeded`, `stalled`, `denied`, `unsupported`, `invalid_request`.
- `value`: present only when the engine produced a value. A present JSON `null`
  is distinct from an absent value. Python uses `quickpy.set_state`; JavaScript
  uses `return`; Bash has no value.
- `output`: ordered array. Each item has `kind` (`text` or `image`), `stream`
  (`stdout` or `stderr`), optional `level` for console diagnostics, `text` for
  text, and `mime` plus `data` for images. `data` is raw bytes encoded as
  base64 by JSON.
- `error`: structured failure with `name`, optional `code`, `message`, and
  optional `stack`.
- `exitCode`: Bash only.
- `warnings`: advisory strings. Bash includes a full-OS-privileges warning;
  JavaScript includes a sandbox warning.
- `duration`: wall-clock time in nanoseconds as an integer. It includes engine
  startup on first use and is not a benchmark.

Text output is a JSON string. Bash captures raw bytes and converts them to a
string, so non-UTF-8 bytes may be replaced when JSON-encoded. Image data is raw
bytes and is base64-encoded in JSON.

## Library API

```go
package main

import (
    "context"
    "fmt"
    "log"
    "time"

    scriptagent "github.com/keejkrej/script-agent"
)

func main() {
    agent, err := scriptagent.New(scriptagent.Config{
        Workspace:   "/abs/path/to/workspace",
        AllowWrites: true, // explicit; the zero value is read-only
    })
    if err != nil {
        log.Fatal(err)
    }
    defer func() {
        ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
        defer cancel()
        _ = agent.Close(ctx)
    }()

    res := agent.Run(context.Background(), scriptagent.Request{
        Language: scriptagent.LanguagePython,
        Source:   `print("hello")`,
    })
    fmt.Println(res.Status, res.Duration)
}
```

`New` returns an error only for an invalid configuration. `Run` always returns
a `Result`; script failures, timeouts, cancellations, limits, and denials are
statuses, not Go errors.

**Source checkout is the supported build path.** This v0.1 native application
is built from a clone of this repository. Do not assume
`go get github.com/keejkrej/script-agent` or `go install ...@...` works as a
portable external SDK: Go ignores `replace` directives in dependencies, and the
vendored `third_party/pi-go` content is not downloaded transitively. If you
compose it from another module, clone this repository and add an explicit
replace pointing at the actual clone path, for example:

```go
replace github.com/keejkrej/pi-go => /path/to/script-agent/third_party/pi-go
```

The CLI and tests in this repository work without an extra replace. This is
intended native application/source composition for the v0.1 prototype, not a
conventional transitive `go get` SDK promise.

### Config

| Field | Meaning | Default |
| --- | --- | --- |
| `Workspace` | Existing directory. Resolved absolute once. | Required. |
| `AllowWrites` | Explicit workspace-write opt-in. | `false` (read-only). |
| `AllowProcess` | Agent-level process grant. Bash also needs `Request.AllowProcess`. | `false`. |
| `MaxOutputBytes` | Output cap. Also used as the capability `MaxBytes` for workspace reads and writes. | `1 MiB`. |
| `MaxSourceBytes` | Source cap. | `256 KiB`. |
| `Timeout` | Default execution timeout. | `30s`. |
| `MaxPythonMemoryBytes` | QuickPy guest memory cap. | `64 MiB`. |
| `MaxExecutions` | Concurrent execution slots. | `4`. |

Zero numeric limits select the defaults. Negative values are rejected. The
CLI sets `MaxExecutions` to `1`.

### Request

| Field | Meaning |
| --- | --- |
| `Language` | Required. `LanguagePython`, `LanguageJavaScript`, or `LanguageBash`. |
| `Source` | Script text. |
| `Filename` | Optional display name. Engines choose a language-appropriate default. |
| `Timeout` | May only tighten the Agent timeout. Zero inherits. |
| `MaxOutputBytes` | May only tighten the Agent output cap. Zero inherits. |
| `AllowProcess` | Required for Bash; rejected for Python and JavaScript. |

### Result

| Field | Meaning |
| --- | --- |
| `Language` | Canonical language. |
| `Status` | Terminal status. |
| `Value` | Present script value, if any. Use `HasValue` to distinguish absent from present `null`. |
| `Output` | Ordered text/image output. |
| `Error` | Structured failure, if any. |
| `ExitCode` | Bash only. |
| `Warnings` | Advisory strings. |
| `Duration` | `time.Duration`; JSON integer nanoseconds. |

`Agent` is safe for concurrent use up to `MaxExecutions`. Python and JavaScript
engines are initialized lazily on first use. `Close` stops admission, waits for
in-flight executions, and then releases the engines and workspace.

## Languages

### Python

- QuickPy WASM guest, MicroPython 1.29.0, guest ABI 4.
- Fresh guest instance and fresh globals per execution. The runtime and tool
  registry persist at the engine level.
- `quickpy.run(coro)` is submit-and-drive-to-quiescence, not a blocking
  `asyncio.run`. It must be the last top-level statement.
- `quickpy.fs` is an async facade over the eight capability operations:
  `read_text`, `write_text`, `read_bytes`, `write_bytes`, `exists`, `listdir`,
  `mkdir`, `remove`.
- `quickpy.set_state(value)` / `quickpy.get_state()` carry a JSON-like value.
  The last successful write wins and commits only on successful completion.
  `set_state(None)` is distinct from no write.
- `open()` is unsupported. `os`, `subprocess`, `pathlib`, `tempfile`, and
  ambient clock/random modules are not available.
- `json`, `re`, `math`, `io.StringIO`, and `binascii` are available.
- `str.encode()` and `bytes.decode()` are not available at this ROM level; use
  `bytes(text, "utf-8")` and `str(data, "utf-8")`.
- `quickpy.fs` maps `not_found` to `OSError(2)` and `permission_denied` to
  `OSError(13)`. Other capability codes propagate as tool errors. There are no
  CPython `FileNotFoundError`/`PermissionError` subclasses.

### JavaScript

- QuickJS-WASI 3.6.2 (QuickJS-NG 0.17.0).
- Fresh VM and fresh globals per execution. The sandbox and tool registry
  persist at the engine level.
- Source is an async function body: top-level `await` and `return` work.
- Globals: `tools`, `ALL_TOOLS`, `text`, `image`, `console`, `store`, `load`,
  `exit`, and the frozen `fs` namespace.
- `store`/`load` are per-execution helpers, not a persistence mechanism.
- No Node module loader, `require`, `Buffer`, `process`, `*Sync` methods, npm,
  or network access.
- `fs` is a small Node-shaped subset, not `node:fs`:

| Call | Result |
| --- | --- |
| `fs.readFile(path[, options])` | `Uint8Array`, or `string` for `"utf8"`/`"utf-8"` or `{ encoding: "utf8"\|"utf-8" }` |
| `fs.writeFile(path, string\|Uint8Array)` | `undefined` |
| `fs.readdir([path])` | sorted entry-name array |
| `fs.mkdir(path[, { recursive }])` | `created` boolean (deviation from Node's first-created path) |
| `fs.rm(path)` | `undefined`; file, symlink, or empty directory only |
| `fs.exists(path)` | boolean (custom helper, not a Node API) |

Every `fs` function is `async` and returns a real Promise, including for
argument errors. There are no `*Sync` methods, no `Buffer`, no `require`, and no
`node:fs` import. Capability codes are mapped to Node-style `.code` values
(`ENOENT`, `EACCES`, `EINVAL`, `EISDIR`, `ENOTDIR`, `EEXIST`, `ENOTEMPTY`,
`EFBIG`, `EIO`); the original code is kept on `.capabilityCode`, and the
relative path is kept on `.path`.

### Bash

- Host process, not a sandbox.
- Requires both `Config.AllowProcess` and `Request.AllowProcess`. The CLI
  `--allow-process` flag sets both.
- Controlled environment: fixed `PATH`, `HOME` and `PWD` set to the workspace,
  `LANG`/`LC_ALL` set to `C.UTF-8`, and a private `TMPDIR` created with
  `os.MkdirTemp` under the host `TMPDIR` and removed after the run. The host
  environment is not inherited. If the host `TMPDIR` points to a nonexistent
  directory, Bash execution fails with a process error; this is a current
  limitation, not a build blocker. Python and JavaScript use embedded direct
  bytes and do not need a writable `TMPDIR`.
- The source is one literal argv element after `-c`; the host does not
  interpolate it into a shell command line.
- POSIX process group with `SIGKILL` on cancellation and a 2-second wait
  window. Daemonization is not supported.
- Bash can read and write outside the workspace, use the network, and signal
  other processes with the host user's authority. Only run trusted scripts.

## Workspace and capabilities

- One explicit workspace per `Agent`. It is resolved to an absolute path once;
  later working-directory changes do not move it.
- The capability adapter uses `os.Root` containment. Symlinks are followed only
  when they resolve inside the root. Absolute or escaping symlinks are denied.
- Read-only is the default. `AllowWrites: true` unlocks `fs_write_text`,
  `fs_write_bytes`, `fs_mkdir`, and `fs_remove`.
- `MaxOutputBytes` is also passed as the capability `MaxBytes`, so it bounds
  workspace text and binary reads and writes as well as script output.
- Writes are atomic within the target directory (temporary file plus rename),
  but there is no `fsync`. A crash may leave a `.capability-tmp-*` file.
- Hardlinks, FIFOs, devices, and bind mounts are known limitations. See
  [docs/SECURITY.md](docs/SECURITY.md).
- Capability operation errors contain static text and caller-relative paths;
  the workspace root is not included. `NewWorkspace` constructor errors may
  name the root.

## Artifacts and provenance

Both runtime artifacts are committed and embedded in the native binary. No
external Node, CPython, or Python download is needed at runtime.

| Artifact | Size | SHA-256 | Engine |
| --- | ---: | --- | --- |
| `internal/artifacts/quickpy.wasm` | 256486 | `5fdd351a70e0f65a4b812ad21c34a0885f87e839aee7721164768907ad61f50a` | MicroPython 1.29.0, ABI 4 |
| `third_party/pi-go/internal/quickjs/quickjs.wasm` | 637405 | `d4c9375f2b1ca4dc95f72c8aa2982a7a9951ac8011490d79c6582df732b4bbd9` | QuickJS-WASI 3.6.2 (QuickJS-NG 0.17.0) |

List and verify them:

```sh
go run ./cmd/script-agent-artifacts list
go run ./cmd/script-agent-artifacts verify
```

`verify` checks the on-disk copies and the embedded QuickPy bytes. `--repo`
defaults to the nearest parent of the working directory whose `go.mod` declares
this module.

The QuickPy artifact is rebuilt only through `scripts/rebuild-quickpy.sh`,
which requires Zig 0.17.1 and wasm-opt 133, clones the pinned QuickPy commit,
and refuses to overwrite the artifact unless the size and SHA-256 match. See
[internal/artifacts/PROVENANCE.md](internal/artifacts/PROVENANCE.md) and
[third_party/pi-go/PROVENANCE.md](third_party/pi-go/PROVENANCE.md).

The Go module graph is pinned in `go.mod`/`go.sum`. The first build downloads
public Go modules; the repository is not fully offline without a populated
module cache. The only `replace` is the relative source vendor
`github.com/keejkrej/pi-go => ./third_party/pi-go`. There is no `go.work` and no
machine-local replace.

## Documentation

- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) — repository boundaries, engines,
  capability adapter, artifacts, and design limits.
- [docs/SECURITY.md](docs/SECURITY.md) — privilege matrix, workspace
  containment, process behavior, and known limitations.
- [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md) — build, test, integration,
  artifact, cross-build, and CI commands.
- [integration/README.md](integration/README.md) — end-to-end suite, recorded
  results, and binary metrics.
- [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) — third-party provenance and
  required notices.
- [process/README.md](process/README.md) — low-level process primitive.
- [adapter/quickjs/facade.go](adapter/quickjs/facade.go) — the frozen `fs`
  prelude.
- [adapter/quickjs/bridge.go](adapter/quickjs/bridge.go) — capability tool
  installation.

## License and redistribution

There is no root `LICENSE` file in this repository. No license is granted for
the original `script-agent` code, and it is not licensed for external
redistribution beyond the owner's authorization. The owner must choose a
license before publishing or redistributing this repository.

The vendored `third_party/pi-go` source and the `github.com/keejkrej/quickpy`
dependency also have no license file at their pinned revisions. Their upstream
projects must make their own licensing decisions. Third-party components that
do carry licenses are recorded in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
