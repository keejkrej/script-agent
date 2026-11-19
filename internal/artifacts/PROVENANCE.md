# QuickPy guest artifact — provenance

`quickpy.wasm` is the compiled QuickPy guest embedded by `internal/artifacts`.
It is committed so a fresh checkout builds and tests without a source
generation step or a network download.

## Artifact

- File: `internal/artifacts/quickpy.wasm`
- Size: 256486 bytes
- SHA-256: `5fdd351a70e0f65a4b812ad21c34a0885f87e839aee7721164768907ad61f50a`
- Guest ABI: 4
- Engine: MicroPython 1.29.0

## Source

- Repository: <https://github.com/keejkrej/quickpy>
- Commit: `9ef902956e91cec53dd5c6c30b1d5b7023dfdf69`
  (`legal: license original QuickPy code under MIT (#5)`). The guest source is
  unchanged from the earlier `3897a87` build; that commit only added the root
  `LICENSE` and licensing docs, so the artifact bytes and SHA-256 are identical.
- Build entry point: `guest/build.sh`
- MicroPython pin: `v1.29.0`, commit
  `0fd6c573ea815774668bbb16b8e197c8822368b2`

## Toolchain

The shipped bytes were produced with:

- `zig` 0.17.1
- `wasm-opt` (binaryen) 133
- `make`, `python3`, `git`

`scripts/rebuild-quickpy.sh` enforces these exact versions and refuses to
overwrite the artifact unless the rebuilt size and SHA-256 match the values
above.

## Rebuild

From the repository root:

```sh
./scripts/rebuild-quickpy.sh
```

The script clones the pinned QuickPy commit into a throwaway directory, builds
a clean guest, verifies the resulting hash, and only then copies it over
`internal/artifacts/quickpy.wasm`.

## Verify

```sh
shasum -a 256 internal/artifacts/quickpy.wasm
# 5fdd351a70e0f65a4b812ad21c34a0885f87e839aee7721164768907ad61f50a

go run ./cmd/script-agent-artifacts verify
```

## License

Original QuickPy code is MIT licensed under the QuickPy project root
`LICENSE`. The pinned commit predates that file, but the owner's MIT grant
applies to the original source. The guest is built on MicroPython 1.29.0, which
is distributed under the MIT license by the MicroPython project. This note does
not relicense third-party components.
