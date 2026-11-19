package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runCapture(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestList(t *testing.T) {
	code, stdout, stderr := runCapture(t, "list")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	for _, want := range []string{
		quickJSPath,
		quickJSSHA256,
		quickJSEngine,
		quickPyPath,
		"5fdd351a70e0f65a4b812ad21c34a0885f87e839aee7721164768907ad61f50a",
		"MicroPython 1.29.0",
		"abi=4",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("list output missing %q:\n%s", want, stdout)
		}
	}
}

func TestVerifyRealRepo(t *testing.T) {
	root, err := findRepoRoot()
	if err != nil {
		t.Fatalf("findRepoRoot: %v", err)
	}
	code, stdout, stderr := runCapture(t, "verify", "--repo", root)
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "ok: "+quickJSPath) || !strings.Contains(stdout, "ok: "+quickPyPath) {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestVerifyDefaultRepoRoot(t *testing.T) {
	root, err := findRepoRoot()
	if err != nil {
		t.Fatalf("findRepoRoot: %v", err)
	}
	t.Chdir(filepath.Join(root, "cmd", "script-agent-artifacts"))
	code, _, stderr := runCapture(t, "verify")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
}

func TestVerifyTempRepo(t *testing.T) {
	root, err := findRepoRoot()
	if err != nil {
		t.Fatalf("findRepoRoot: %v", err)
	}
	tmp := t.TempDir()
	copyAsset(t, root, tmp, quickJSPath)
	copyAsset(t, root, tmp, quickPyPath)
	code, _, stderr := runCapture(t, "verify", "--repo", tmp)
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
}

func TestVerifyCorruptAsset(t *testing.T) {
	root, err := findRepoRoot()
	if err != nil {
		t.Fatalf("findRepoRoot: %v", err)
	}
	tmp := t.TempDir()
	copyAsset(t, root, tmp, quickJSPath)
	copyAsset(t, root, tmp, quickPyPath)
	path := filepath.Join(tmp, quickPyPath)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data[0] ^= 0xff
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runCapture(t, "verify", "--repo", tmp)
	if code != 1 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stderr, "quickpy") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestVerifyMissingAsset(t *testing.T) {
	tmp := t.TempDir()
	code, _, stderr := runCapture(t, "verify", "--repo", tmp)
	if code != 1 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stderr, "quickjs") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestUnknownCommand(t *testing.T) {
	code, _, stderr := runCapture(t, "bogus")
	if code != 2 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
}

func copyAsset(t *testing.T, root, dst, rel string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	target := filepath.Join(dst, rel)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
