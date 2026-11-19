# Vendored pi-go runtime — provenance

This directory is a **minimal, source-vendored copy** of the generic codemode
runtime from `github.com/keejkrej/pi-go`. It is not a fork of the whole
repository and it is not a Go module proxy download. The module path is kept as
`github.com/keejkrej/pi-go` so the vendored `internal/...` imports stay legal.

## Upstream

- Repository: <https://github.com/keejkrej/pi-go>
- Commit: `69d43f6d9a4b83333d4582ca8c755457702d6576`
  (`Merge pull request #5 from keejkrej/chore/repo-cleanup-and-docs`)
- Vendored on: 2026-11-19

## What is vendored

Production closure only, plus the tests that pin the archived generic runtime
behavior:

| Path | Contents |
| --- | --- |
| `codemode/` | `declarations.go`, `doc.go`, `identifier.go`, `source.go`, `types.go`, `wasm.go` and the small unit tests `declarations_test.go`, `source_test.go`, `types_test.go` |
| `codemode/runtime/` | `doc.go`, `host.go`, `prelude_source.go`, `protocol.go`, `worker.go` and the runtime tests `sandbox_test.go`, `trusted_prelude_test.go`, `output_limit_test.go` |
| `internal/js/` | production files only (`date.go`, `decoder.go`, `doc.go`, `errors.go`, `number.go`, `platform.go`, `regexp.go`, `sleep.go`, `strings.go`, `unicode.go`, `unicode17.go`, `uri.go`) |
| `internal/jsonx/` | production files only |
| `internal/omap/` | production files only |
| `internal/quickjs/` | production files only plus the embedded `quickjs.wasm` |

## What is excluded

- `codingagent/`, `gui/`, `tui/`, `server/`, `client/`, `agent/`, `ai/`,
  `mcp/`, `protocol/`, `telemetry/`, `durable/`, `evals/`, `cmd/`, `pi/`,
  `chord/`, `docs/` and every other top-level package.
- Provider, frontend and GUI code.
- The large internal test fixtures: `internal/js/testdata/` (~1.1 MB),
  `internal/jsonx/testdata/` (~0.9 MB), `internal/quickjs/testdata/` (~128 KB),
  and the internal package test files that consume them. The vendored runtime
  tests do not need those fixtures.
- The upstream `go.mod`/`go.sum` dependency set. This directory has its own
  small `go.mod` with only the modules the vendored closure imports.

## Archived prototype

The trusted-prelude and coded-error changes were restored from the archived
prototype at:

- Patch: `quickjs-prototype.patch`
- Patch SHA-256: `4cb12167e702eb673f731664b5c36ef9d9953ee3f36996bdd459ee06e6bf16fc`
- Base commit: `69d43f6d9a4b83333d4582ca8c755457702d6576`

The archived `go.mod` `replace github.com/keejkrej/quickpy => ../quickpy` was
**not** imported. This module has no local-workspace replace.

### Deviations from upstream `69d43f6`

1. **`TrustedPreludeSource`** (`codemode/types.go`,
   `codemode/runtime/{host,protocol,worker,prelude_source}.go`): a trusted
   prelude is evaluated in every fresh VM after the codemode prelude installs
   tools/globals/output helpers and before the user script. It runs in global
   script mode with the filename `codemode-trusted-prelude.js`. A prelude that
   fails to compile or evaluate fails every execution with kind `sandbox` and a
   message that includes the source filename. The prelude never receives the
   private host bridge.
2. **`CodemodeErrorCoder` / `CodemodeErrorCode`** (`codemode/types.go`,
   `codemode/runtime/host.go`, `codemode/runtime/prelude_source.go`,
   `codemode/runtime/protocol.go`, `codemode/runtime/worker.go`): a tool error
   that implements `Code() string` attaches a stable `.code` to the guest's
   rejected `Error`. Codes are limited to 64 characters from
   `[A-Za-z0-9_.:-]`; anything else is dropped and the error keeps its normal
   message-only form. Ordinary Go errors are unchanged.
3. **`CodemodeSandboxOptions.MaxOutputBytes`** (local additive change, not in
   the archived patch): an optional retained-output budget. Text and console
   output count UTF-8 bytes; image output counts decoded image bytes, never the
   base64 characters. When the next item would exceed the budget it is clipped
   to the remaining bytes (text at a UTF-8 boundary, image to a raw byte prefix
   and re-encoded) and the execution fails with the new kind `limit`. Output
   produced before the limit is retained. `nil` or a non-positive value keeps
   the original unlimited behavior, so the library API is not broken when the
   option is unset.

## Embedded QuickJS artifact

- File: `internal/quickjs/quickjs.wasm`
- Size: 637405 bytes
- SHA-256: `d4c9375f2b1ca4dc95f72c8aa2982a7a9951ac8011490d79c6582df732b4bbd9`
- Engine: QuickJS-WASI 3.6.2 (QuickJS-NG 0.17.0)
- Source: copied byte-for-byte from the upstream `internal/quickjs/quickjs.wasm`
  at the commit above. It is embedded by `internal/quickjs/quickjs.go` via
  `//go:embed quickjs.wasm`; the application does not carry a second copy.

## Source check

From the repository root:

```sh
# The embedded QuickJS artifact must match the recorded hash.
shasum -a 256 third_party/pi-go/internal/quickjs/quickjs.wasm
# d4c9375f2b1ca4dc95f72c8aa2982a7a9951ac8011490d79c6582df732b4bbd9

# The vendored module must build and its runtime tests must pass.
(cd third_party/pi-go && go test ./codemode/...)
```

To compare a vendored file against upstream without cloning the whole
repository, fetch the single file at the pinned commit:

```sh
curl -fsSL \
  https://raw.githubusercontent.com/keejkrej/pi-go/69d43f6d9a4b83333d4582ca8c755457702d6576/codemode/runtime/host.go \
  -o /tmp/upstream-host.go
```

The three deviations above are the only expected differences.

## License

The upstream `pi-go` repository owner has granted MIT for original `pi-go`
code. A copy of that license is kept at
[`LICENSE`](LICENSE) in this vendored directory. The pinned commit
`69d43f6d9a4b83333d4582ca8c755457702d6576` predates the addition of the
upstream root `LICENSE` file, but the owner's MIT grant applies to the original
source vendored here. The embedded `quickjs.wasm` is the upstream QuickJS-WASI
build and keeps its own upstream terms.
