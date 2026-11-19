#!/usr/bin/env bash
# Rebuild internal/artifacts/quickpy.wasm from the pinned QuickPy source.
#
# This is a controlled, reproducible rebuild:
#   - it clones the public QuickPy repository at the exact pinned commit into a
#     throwaway directory (never a sibling checkout of this workspace);
#   - it requires the exact toolchain versions used for the shipped artifact;
#   - it refuses to overwrite the committed artifact unless the rebuilt bytes
#     match the recorded size and SHA-256.
#
# It does not download or execute anything other than the pinned public source
# and its pinned MicroPython dependency. Run it from anywhere.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
QUICKPY_URL="https://github.com/keejkrej/quickpy.git"
QUICKPY_COMMIT="9ef902956e91cec53dd5c6c30b1d5b7023dfdf69"
EXPECTED_SIZE=256486
EXPECTED_SHA256="5fdd351a70e0f65a4b812ad21c34a0885f87e839aee7721164768907ad61f50a"
ZIG_VERSION="0.17.1"
WASM_OPT_VERSION="133"
ARTIFACT="$REPO_ROOT/internal/artifacts/quickpy.wasm"

need() {
    command -v "$1" >/dev/null 2>&1 || {
        echo "error: missing required tool: $1" >&2
        exit 1
    }
}

need git
need zig
need wasm-opt
need make
need python3
need shasum

actual_zig="$(zig version)"
if [[ "$actual_zig" != "$ZIG_VERSION" ]]; then
    echo "error: zig $ZIG_VERSION is required, found $actual_zig" >&2
    exit 1
fi

actual_wasm_opt="$(wasm-opt --version 2>/dev/null || true)"
if [[ "$actual_wasm_opt" != *"version $WASM_OPT_VERSION"* ]]; then
    echo "error: wasm-opt (binaryen) $WASM_OPT_VERSION is required, found: ${actual_wasm_opt:-<none>}" >&2
    exit 1
fi

WORK="$(mktemp -d "${TMPDIR:-/tmp}/script-agent-quickpy.XXXXXX")"
cleanup() { rm -rf "$WORK"; }
trap cleanup EXIT

SRC="$WORK/quickpy"
echo "==> cloning $QUICKPY_URL at $QUICKPY_COMMIT"
git clone --no-checkout "$QUICKPY_URL" "$SRC"
git -C "$SRC" checkout --detach "$QUICKPY_COMMIT"

actual_commit="$(git -C "$SRC" rev-parse HEAD)"
if [[ "$actual_commit" != "$QUICKPY_COMMIT" ]]; then
    echo "error: quickpy checkout is $actual_commit, expected $QUICKPY_COMMIT" >&2
    exit 1
fi

echo "==> building quickpy.wasm"
BUILD_DIR="$WORK/build" MPY_DIR="$WORK/micropython" "$SRC/guest/build.sh"

OUT="$BUILD_DIR/quickpy.wasm"
if [[ ! -f "$OUT" ]]; then
    echo "error: build did not produce $OUT" >&2
    exit 1
fi

actual_size="$(wc -c < "$OUT" | tr -d ' ')"
actual_sha="$(shasum -a 256 "$OUT" | awk '{print $1}')"
if [[ "$actual_size" != "$EXPECTED_SIZE" || "$actual_sha" != "$EXPECTED_SHA256" ]]; then
    echo "error: rebuilt artifact does not match the pinned metadata; refusing to overwrite" >&2
    echo "  size:   $actual_size (expected $EXPECTED_SIZE)" >&2
    echo "  sha256: $actual_sha (expected $EXPECTED_SHA256)" >&2
    exit 1
fi

cp "$OUT" "$ARTIFACT"
echo "==> updated $ARTIFACT"
echo "    size:   $actual_size"
echo "    sha256: $actual_sha"
