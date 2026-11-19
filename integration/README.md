# script-agent integration tests

These are real end-to-end tests, not unit tests. They build the actual
`CGO_ENABLED=0` native binary, execute real Python (QuickPy WASM), JavaScript
(QuickJS WASM), and Bash (host process) scripts, and verify the observable
contract from the outside.

The tests are opt-in because they compile WASM, build binaries, and spawn
processes. They are tagged `scriptagent_integration` and live in this
directory only; the default `go test ./...` run does not execute them.

## Files

| File | What it proves |
| --- | --- |
| `cli_e2e_test.go` | The built binary runs Python and JavaScript in an unrelated empty cwd with a nonexistent `TMPDIR`; stdout is exactly one bounded JSON object; script text is structured `output`; `--version` reports version, languages, and the embedded artifact identity; no external Node/Python runtime is needed. |
| `shared_workspace_test.go` | JavaScript writes UTF-8 JSON and a binary subarray, Python reads/updates the JSON and reads the binary, JavaScript reads both back; `fs` is frozen and returns real Promises; guest globals are fresh per call; two workspace roots cannot reach each other or an outer sentinel. |
| `errors_deny_test.go` | Read-only mutations are denied in both languages with the guest's own codes; Bash is denied before any process starts; the CLI requires `--workspace`; `--code`/`--file` are mutually exclusive; source caps and malformed numeric flags produce a JSON `invalid_request` and exit 2. |
| `limits_test.go` | Public `Agent` output/source/timeout limits for Python and QuickJS; `limit_exceeded` retains the byte prefix; valid runs have only normal fields; outer deadline and cancellation are reported as `timed_out`/`cancelled`; concurrent runs; idempotent `Close` with no in-flight leak. |
| `bash_test.go` | Real Bash process execution: host env is not inherited, `$PWD` is the workspace, the source is one literal argv element, non-zero exit is a failed result with the exit code, stdout/stderr caps are independent, timeout/cancel/group cleanup, warnings, invalid-UTF-8 and Unicode output, NUL rejection, CLI exit codes 1/124/130/134. |
| `artifacts_test.go` | `script-agent-artifacts list`/`verify` against the real committed assets; native binary size and stripped symbol table; Linux and Windows cross-compiles. |
| `clean_copy_test.go` | A `.git`-free copy of the tree builds and tests with `-mod=readonly`; the only replace is the relative vendored `third_party/pi-go`; `go.mod`/`go.sum` are not modified. |
| `helpers_test.go` | Shared build/run/decode helpers. |

## Commands

Run from the repository root:

```sh
# Default (fast) checks.
go build ./...
go vet ./...
go test ./...

# Full real tagged integration.
go vet -tags scriptagent_integration ./...
go test -tags scriptagent_integration ./...

# Race detector on the public root and process packages.
go test -race -count=1 ./...

# Race detector on the real integration suite.
go test -race -count=1 -tags scriptagent_integration ./integration
```

## Recorded results

Environment: macOS, `darwin/arm64`, Go 1.27.4, `CGO_ENABLED=0`.

| Command | Result |
| --- | --- |
| `go build ./...` | ok |
| `go vet ./...` | ok |
| `go vet -tags scriptagent_integration ./...` | ok |
| `go test ./...` | ok (all packages; `integration` reports `[no test files]` without the tag) |
| `go test -tags scriptagent_integration ./...` | ok; `integration` ~9.5-10.6s, `process` ~2.7s, others < 1s |
| `go test -race -count=1 ./...` | ok |
| `go test -race -count=1 -tags scriptagent_integration ./integration` | ok, ~18s |

Integration suite: **44 top-level tests / 56 test cases**, all passing,
~9.5s without the race detector.

## Binary and artifact metrics

| Item | Value |
| --- | --- |
| Native binary | `8,452,770` bytes (8.06 MiB), Mach-O 64-bit arm64, stripped (`-s -w`; no `main.main`/`runtime.main` symbols) |
| QuickPy artifact | `256,486` bytes, SHA-256 `5fdd351a70e0f65a4b812ad21c34a0885f87e839aee7721164768907ad61f50a`, ABI 4, MicroPython 1.29.0 |
| QuickJS artifact | `637,405` bytes, SHA-256 `d4c9375f2b1ca4dc95f72c8aa2982a7a9951ac8011490d79c6582df732b4bbd9`, QuickJS-WASI 3.6.2 (QuickJS-NG 0.17.0) |
| Linux cross-compile | `GOOS=linux GOARCH=amd64` builds a stripped static ELF |
| Windows cross-compile | `GOOS=windows GOARCH=amd64` builds a PE32+ executable |

The binary embeds both WASM guests. It runs Python and JavaScript with
`PATH=/nonexistent` and no host Node or Python installation.

## Platform notes

- Bash process execution is POSIX-only. On Windows the `process` package
  returns an `unsupported` error without starting a process (see
  `process/README.md` and `TestRunUnsupportedOnWindows`). The Windows binary
  is cross-compiled here but not executed.
- The Bash engine creates its private temp directory with `os.MkdirTemp` under
  the host `TMPDIR`. A nonexistent `TMPDIR` is therefore a Bash process
  limitation; the empty-cwd/nonexistent-`TMPDIR` tests cover Python and
  JavaScript, which use embedded direct bytes and do not need a writable
  `TMPDIR`.

## Failures requiring package owners

None. Every check in this suite passed against the current working tree.
