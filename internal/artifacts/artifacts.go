// Package artifacts embeds the runtime artifacts that script-agent ships in
// its native binary. It is internal: callers get a defensive copy of the bytes
// and an immutable metadata value, never a mutable slice or a download path.
package artifacts

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
)

// quickpyWASM is the compiled QuickPy guest. It is embedded at build time; the
// build never downloads or generates it.
//
//go:embed quickpy.wasm
var quickpyWASM []byte

const (
	// QuickPyName is the artifact file name.
	QuickPyName = "quickpy.wasm"
	// QuickPySize is the expected artifact size in bytes.
	QuickPySize = 256486
	// QuickPySHA256 is the expected lowercase hex SHA-256 of the artifact.
	QuickPySHA256 = "5fdd351a70e0f65a4b812ad21c34a0885f87e839aee7721164768907ad61f50a"
	// QuickPyABI is the guest ABI version the artifact implements.
	QuickPyABI = 4
	// QuickPyEngine identifies the guest engine and version.
	QuickPyEngine = "MicroPython 1.29.0"
)

// Metadata describes one embedded artifact. It is a value type, so callers
// cannot mutate the package's copy.
type Metadata struct {
	Name   string
	Size   int
	SHA256 string
	ABI    int
	Engine string
}

var quickPyMetadata = Metadata{
	Name:   QuickPyName,
	Size:   QuickPySize,
	SHA256: QuickPySHA256,
	ABI:    QuickPyABI,
	Engine: QuickPyEngine,
}

// QuickPyMetadata returns the immutable QuickPy artifact metadata.
func QuickPyMetadata() Metadata {
	return quickPyMetadata
}

// QuickPyWASM returns a defensive copy of the embedded QuickPy artifact. The
// caller may mutate the returned slice without affecting later calls.
func QuickPyWASM() []byte {
	out := make([]byte, len(quickpyWASM))
	copy(out, quickpyWASM)
	return out
}

// QuickPyDigest returns the size and lowercase hex SHA-256 of the embedded
// bytes as they are compiled into the binary.
func QuickPyDigest() (int, string) {
	sum := sha256.Sum256(quickpyWASM)
	return len(quickpyWASM), hex.EncodeToString(sum[:])
}

// VerifyQuickPy checks that the embedded artifact is present and matches the
// recorded metadata. It never falls back to a download or an external file.
func VerifyQuickPy() error {
	if len(quickpyWASM) == 0 {
		return fmt.Errorf("%s: embedded artifact is empty", QuickPyName)
	}
	size, digest := QuickPyDigest()
	if size != QuickPySize {
		return fmt.Errorf("%s: size = %d, want %d", QuickPyName, size, QuickPySize)
	}
	if digest != QuickPySHA256 {
		return fmt.Errorf("%s: sha256 = %s, want %s", QuickPyName, digest, QuickPySHA256)
	}
	return nil
}
