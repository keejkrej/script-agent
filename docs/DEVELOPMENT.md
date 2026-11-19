# Development

This document covers building, testing, verifying artifacts, cross-building,
and the CI workflow. It is written against the current repository state.

## Prerequisites

- **Go 1.27.3 or newer** per [go.mod](../go.mod). The module declares
  `toolchain go1.27.4`; the development environment used for this repository
  has Go 1.27.4.
- **No Node, CPython, Zig, or wasm-opt is needed for a normal build or test.**
  The QuickPy and QuickJS WASM artifacts are committed and embedded.
- **Zig 0.17.1**, **wasm-opt (binaryen) 133**, `git`, `make`, `python3`, and
  `shasum` are needed only to rebuild the QuickPy artifact with
  [scripts/rebuild-quickpy.sh](../scripts/rebuild-quickpy.sh).
- Network access on the first build to download public Go modules. There are no
  private modules. The repository is not fully offline without a populated
  module cache.

## Build

```sh
CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' ./cmd/script-agent ./cmd/script-agent-artifacts
```

This produces native binaries with the WASM artifacts embedded. There is no
external Node or CPython runtime dependency.

A plain `go build ./...` also works. The first build may run `go mod download`
for public modules.

## Test

```sh
go test ./...
go test -race ./...
go vet ./...
```

- Root `./...` includes the root package, `adapter/quickjs`, `process`,
  `internal/artifacts`, `integration` (no test files without the tag), and both
  `cmd` packages.
- `go test -race ./...` is slower; CI runs it.
- `go test ./...` does **not** include `third_party/pi-go`, because that is a
  separate Go module.

## Integration tests

There are two opt-in integration layers. Both use the
`scriptagent_integration` build tag and compile the real WASM artifacts.

### Root integration tests

```sh
go test -tags scriptagent_integration -run TestIntegration -count=1 .
```

These exercise the Python, JavaScript, and Bash engines, read-only denial,
script errors, and writable workspace behavior through the public library API.

### End-to-end integration suite

```sh
go test -tags scriptagent_integration -count=1 ./integration
go test -race -count=1 -tags scriptagent_integration ./integration
go vet -tags scriptagent_integration ./...
```

The `integration/` package builds the real `CGO_ENABLED=0` native binary and
verifies the observable contract from the outside: CLI JSON framing, empty-cwd
and missing-`TMPDIR` behavior for Python/JavaScript, shared workspace
reads/writes, denial and error codes, limits, Bash process behavior, artifact
list/verify, cross-compiles, and a clean-copy build with `-mod=readonly`. Bash
itself needs a valid host `TMPDIR` because its private temp directory is created
with `os.MkdirTemp`; a nonexistent `TMPDIR` is a documented Bash process
limitation, not a build blocker. See
[integration/README.md](../integration/README.md) for the current file map,
recorded results, and binary metrics.

- `-tags scriptagent_integration` enables the build tag.
- `-run TestIntegration` selects the root integration tests.
- `-count=1` avoids the test cache.
- On Windows, the Bash integration path is unsupported. Use Linux or macOS.

## Vendor module tests

`third_party/pi-go` is a separate module. Run its tests explicitly:

```sh
(cd third_party/pi-go && go test ./codemode/...)
(cd third_party/pi-go && go vet ./codemode/...)
```

The vendored tests pin the archived generic runtime behavior, including the
trusted prelude, coded errors, and the retained-output budget. They are not
part of root `go test ./...`.

## Artifact verification

```sh
go run ./cmd/script-agent-artifacts list
go run ./cmd/script-agent-artifacts verify
go run ./cmd/script-agent-artifacts verify --repo .
```

- `list` prints the QuickJS and QuickPy paths, sizes, SHA-256 values, and engine
  strings.
- `verify` checks the on-disk copies and the embedded QuickPy bytes.
- `--repo` defaults to the nearest parent of the working directory whose
  `go.mod` declares this module.

The recorded metadata is in
[internal/artifacts/PROVENANCE.md](../internal/artifacts/PROVENANCE.md) and
[third_party/pi-go/PROVENANCE.md](../third_party/pi-go/PROVENANCE.md).

## Rebuilding the QuickPy artifact

```sh
./scripts/rebuild-quickpy.sh
```

The script:

- requires Zig 0.17.1 and wasm-opt 133;
- clones the pinned QuickPy commit
  `9ef902956e91cec53dd5c6c30b1d5b7023dfdf69` into a throwaway directory;
- builds a clean guest;
- verifies the exact size (256486 bytes) and SHA-256
  (`5fdd351a70e0f65a4b812ad21c34a0885f87e839aee7721164768907ad61f50a`);
- refuses to overwrite the committed artifact on any mismatch.

It does not use a sibling checkout and does not import a local replace. The
QuickJS artifact is copied from upstream pi-go; there is no separate QuickJS
rebuild script in this repository.

## CLI smoke examples

```sh
go run ./cmd/script-agent --version

go run ./cmd/script-agent \
  --lang python \
  --workspace "$(mktemp -d)" \
  --code 'print("hello")'

go run ./cmd/script-agent \
  --lang javascript \
  --workspace "$(mktemp -d)" \
  --code 'text("hello"); return 1;'

go run ./cmd/script-agent \
  --lang bash \
  --workspace "$(mktemp -d)" \
  --allow-process \
  --code 'printf "hello\n"'
```

`go run` wraps the process exit code. Build the binary if you need to test the
exact CLI exit codes:

```sh
CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /tmp/script-agent ./cmd/script-agent
/tmp/script-agent --lang bash --workspace "$(mktemp -d)" --code 'echo hi'
echo $? # 3: process denied
```

`--version` needs no workspace. `--code` and `--file` are mutually exclusive
and exactly one is required. `--workspace` is required for execution.

## Cross-build

```sh
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' ./cmd/script-agent
GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' ./cmd/script-agent
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' ./cmd/script-agent
```

The Windows build compiles, but Bash/process execution is unsupported at
runtime. Python and JavaScript are WASM and not OS-process dependent, but
Windows is not a tested or supported target in this repository.

## Platform matrix

| Platform | Python | JavaScript | Bash | CI |
| --- | --- | --- | --- | --- |
| `darwin` | Yes | Yes | Yes (POSIX) | Local development. |
| `linux` | Yes | Yes | Yes (POSIX) | CI target, amd64. |
| `windows` | Builds; not tested/supported | Builds; not tested/supported | Unsupported | Not covered. |

Do not claim Windows support. `process.Run` returns `unsupported` on Windows,
so Bash is unavailable. Python and JavaScript are not covered by CI or tests on
Windows.

## CI

The workflow is [.github/workflows/test.yml](../.github/workflows/test.yml). It
uses:

- `actions/checkout@v4`
- `actions/setup-go@v5`
- Go `1.27.4`
- `ubuntu-latest` (Linux amd64)

Steps:

1. `go mod download`
2. `go mod verify`
3. `go vet ./...`
4. `go vet -tags scriptagent_integration ./...`
5. `go test ./...`
6. `go test -race -timeout 20m ./...`
7. `go test -tags scriptagent_integration -run TestIntegration -count=1 .`
8. `go test -tags scriptagent_integration -count=1 ./integration`
9. `(cd third_party/pi-go && go vet ./codemode/... && go test ./codemode/...)`
10. `go run ./cmd/script-agent-artifacts verify`
11. `go run ./cmd/script-agent-artifacts list`
12. `go run ./cmd/script-agent --version`
13. `CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' ./cmd/script-agent ./cmd/script-agent-artifacts`

The workflow runs both the default unit suite and the tagged real integration
suite. The `integration/` suite is **44 top-level tests / 56 cases**; the root
`TestIntegration` is a separate tagged check. The workflow uses no secrets, no
private modules, no external services, and no e2e environment beyond the
integration tests. All Go modules are public.

## Repository hygiene

- `go.work` is ignored. Do not commit a local workspace file.
- The only `replace` is the relative source vendor
  `github.com/keejkrej/pi-go => ./third_party/pi-go`. Do not add a
  machine-local replace. A module outside this repository that imports
  `script-agent` must clone this repository and add an explicit replace
  pointing at the actual clone path, for example
  `replace github.com/keejkrej/pi-go => /path/to/script-agent/third_party/pi-go`,
  because Go ignores `replace` directives in dependencies. The vendored content
  is not downloaded transitively; generic `go get`/`go install` is not a
  supported portable SDK path for v0.1.
- Committed WASM assets are intentional. `.gitignore` does not ignore
  `internal/artifacts/quickpy.wasm` or
  `third_party/pi-go/internal/quickjs/quickjs.wasm`; only `*.wasm.bak` is
  ignored.
- Do not commit build output such as `/script-agent`, `/bin/`, `/dist/`,
  `*.test`, or `*.out`.
- `third_party/pi-go` is a separate module. Run its tests separately.
- `scripts/rebuild-quickpy.sh` is the only supported QuickPy rebuild path. Do
  not hand-edit the wasm.
- `go.mod`/`go.sum` should stay tidy. Review any version change, especially
  because the root module graph selects x/sys v0.48.0 while QuickPy's own
  `go.mod` lists v0.44.0.

## Related documents

- [README.md](../README.md) — user-facing overview and CLI contract.
- [docs/ARCHITECTURE.md](ARCHITECTURE.md) — repository boundaries and engines.
- [docs/SECURITY.md](SECURITY.md) — privilege matrix and limitations.
- [integration/README.md](../integration/README.md) — end-to-end suite and
  recorded results.
- [THIRD_PARTY_NOTICES.md](../THIRD_PARTY_NOTICES.md) — third-party notices.
