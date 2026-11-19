//go:build scriptagent_integration

package integration

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCleanCopyBuildAndTest copies the working tree (without .git) into a
// fresh temp directory and builds and tests it with -mod=readonly. This proves
// the module is portable: the only replace is the relative vendored pi-go, and
// no machine-local path or dependency edit is required.
func TestCleanCopyBuildAndTest(t *testing.T) {
	src := repoRoot(t)
	dst := filepath.Join(t.TempDir(), "copy")
	copyTree(t, src, dst)

	modPath := filepath.Join(dst, "go.mod")
	mod, err := os.ReadFile(modPath)
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	modText := string(mod)
	if !strings.Contains(modText, "replace github.com/keejkrej/pi-go => ./third_party/pi-go") {
		t.Fatalf("go.mod is missing the relative pi-go replace:\n%s", modText)
	}
	for _, bad := range []string{"/Users/", "/Volumes/", "/home/", `C:\`} {
		if strings.Contains(modText, bad) {
			t.Fatalf("go.mod contains a machine-local path %q:\n%s", bad, modText)
		}
	}
	if _, err := os.Stat(filepath.Join(dst, "go.sum")); err != nil {
		t.Fatalf("go.sum missing from the clean copy: %v", err)
	}

	beforeMod := hashFile(t, modPath)
	beforeSum := hashFile(t, filepath.Join(dst, "go.sum"))

	runGo(t, dst, "build", "-mod=readonly", "./...")
	runGo(t, dst, "test", "-mod=readonly", "./...")

	if got := hashFile(t, modPath); got != beforeMod {
		t.Fatalf("go build/test modified go.mod")
	}
	if got := hashFile(t, filepath.Join(dst, "go.sum")); got != beforeSum {
		t.Fatalf("go build/test modified go.sum")
	}
}

func hashFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
