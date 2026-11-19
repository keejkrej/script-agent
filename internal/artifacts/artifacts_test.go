package artifacts

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestQuickPyArtifactMatchesMetadata(t *testing.T) {
	data := QuickPyWASM()
	if len(data) == 0 {
		t.Fatal("embedded quickpy.wasm is empty")
	}
	if len(data) != QuickPySize {
		t.Fatalf("size = %d, want %d", len(data), QuickPySize)
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != QuickPySHA256 {
		t.Fatalf("sha256 = %s, want %s", got, QuickPySHA256)
	}
	if err := VerifyQuickPy(); err != nil {
		t.Fatalf("VerifyQuickPy: %v", err)
	}
}

func TestQuickPyDigestMatchesMetadata(t *testing.T) {
	size, digest := QuickPyDigest()
	if size != QuickPySize {
		t.Fatalf("size = %d, want %d", size, QuickPySize)
	}
	if digest != QuickPySHA256 {
		t.Fatalf("digest = %s, want %s", digest, QuickPySHA256)
	}
}

func TestQuickPyMetadataIsImmutable(t *testing.T) {
	first := QuickPyMetadata()
	first.Name = "mutated"
	first.Size = -1
	second := QuickPyMetadata()
	if second.Name != QuickPyName || second.Size != QuickPySize {
		t.Fatalf("metadata mutated: %+v", second)
	}
	if second.ABI != 4 {
		t.Fatalf("ABI = %d, want 4", second.ABI)
	}
	if second.Engine != "MicroPython 1.29.0" {
		t.Fatalf("engine = %q", second.Engine)
	}
}

func TestQuickPyWASMReturnsClone(t *testing.T) {
	first := QuickPyWASM()
	if len(first) == 0 {
		t.Fatal("empty artifact")
	}
	first[0] ^= 0xff
	second := QuickPyWASM()
	if second[0] == first[0] {
		t.Fatal("QuickPyWASM returned a shared backing slice")
	}
	if err := VerifyQuickPy(); err != nil {
		t.Fatalf("VerifyQuickPy after caller mutation: %v", err)
	}
}
