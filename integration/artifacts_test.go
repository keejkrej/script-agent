//go:build scriptagent_integration

package integration

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestArtifactsListVerify runs the real artifact command against the real
// committed assets and checks the recorded hashes.
func TestArtifactsListVerify(t *testing.T) {
	root := repoRoot(t)
	bin := artifactsBinary(t)

	list := exec.Command(bin, "list")
	list.Dir = root
	listOut, err := list.CombinedOutput()
	if err != nil {
		t.Fatalf("artifacts list: %v\n%s", err, listOut)
	}
	listText := string(listOut)
	for _, want := range []string{
		"quickjs",
		"sha256=d4c9375f2b1ca4dc95f72c8aa2982a7a9951ac8011490d79c6582df732b4bbd9",
		"quickpy",
		"sha256=5fdd351a70e0f65a4b812ad21c34a0885f87e839aee7721164768907ad61f50a",
		"abi=4",
	} {
		if !strings.Contains(listText, want) {
			t.Fatalf("artifacts list missing %q:\n%s", want, listText)
		}
	}

	verify := exec.Command(bin, "verify")
	verify.Dir = root
	verifyOut, err := verify.CombinedOutput()
	if err != nil {
		t.Fatalf("artifacts verify: %v\n%s", err, verifyOut)
	}
	verifyText := string(verifyOut)
	if strings.Count(verifyText, "ok:") != 2 {
		t.Fatalf("artifacts verify did not confirm both assets:\n%s", verifyText)
	}
}

// TestNativeBinaryMetrics records the stripped native binary size and proves
// the Go symbol table is gone.
func TestNativeBinaryMetrics(t *testing.T) {
	bin := cliBinary(t)
	info, err := os.Stat(bin)
	if err != nil {
		t.Fatalf("stat binary: %v", err)
	}
	size := info.Size()
	t.Logf("native binary: %d bytes (%.2f MiB)", size, float64(size)/(1<<20))
	if size < 4<<20 || size > 64<<20 {
		t.Fatalf("native binary size = %d bytes, outside the expected 4..64 MiB range", size)
	}

	nm := exec.Command("go", "tool", "nm", bin)
	nmOut, _ := nm.CombinedOutput()
	for _, sym := range []string{"main.main", "runtime.main", "runtime.goexit"} {
		if bytes.Contains(nmOut, []byte(sym)) {
			t.Fatalf("binary is not stripped: found symbol %q", sym)
		}
	}
}

// TestCrossCompile proves the same source builds for Linux and Windows. The
// Windows binary cannot run here; the process package reports unsupported on
// Windows by design (see process/README.md and TestRunUnsupportedOnWindows).
func TestCrossCompile(t *testing.T) {
	root := repoRoot(t)
	out := t.TempDir()

	linux := filepath.Join(out, "script-agent-linux")
	build := exec.Command("go", "build", "-trimpath", "-ldflags", "-s -w", "-o", linux, "./cmd/script-agent")
	build.Dir = root
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64")
	if b, err := build.CombinedOutput(); err != nil {
		t.Fatalf("linux build: %v\n%s", err, b)
	}
	linuxHead := readHead(t, linux, 4)
	if !bytes.Equal(linuxHead, []byte{0x7f, 'E', 'L', 'F'}) {
		t.Fatalf("linux binary magic = %x, want ELF", linuxHead)
	}

	windows := filepath.Join(out, "script-agent.exe")
	build = exec.Command("go", "build", "-trimpath", "-ldflags", "-s -w", "-o", windows, "./cmd/script-agent")
	build.Dir = root
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=windows", "GOARCH=amd64")
	if b, err := build.CombinedOutput(); err != nil {
		t.Fatalf("windows build: %v\n%s", err, b)
	}
	winHead := readHead(t, windows, 2)
	if !bytes.Equal(winHead, []byte{'M', 'Z'}) {
		t.Fatalf("windows binary magic = %x, want MZ", winHead)
	}
}

func readHead(t *testing.T, path string, n int) []byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	buf := make([]byte, n)
	if _, err := f.Read(buf); err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return buf
}
