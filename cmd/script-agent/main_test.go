package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keejkrej/script-agent"
)

func runCLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func decodeResult(t *testing.T, raw string) scriptagent.Result {
	t.Helper()
	var res scriptagent.Result
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		t.Fatalf("decode result %q: %v", raw, err)
	}
	return res
}

func TestVersionNeedsNoWorkspace(t *testing.T) {
	code, stdout, _ := runCLI(t, "--version")
	if code != exitOK {
		t.Fatalf("exit = %d", code)
	}
	var info map[string]any
	if err := json.Unmarshal([]byte(stdout), &info); err != nil {
		t.Fatalf("decode version: %v", err)
	}
	if info["version"] != version {
		t.Fatalf("version = %v", info["version"])
	}
	if _, ok := info["pythonArtifact"]; !ok {
		t.Fatalf("missing pythonArtifact: %v", info)
	}
}

func TestHelpNeedsNoWorkspace(t *testing.T) {
	code, _, stderr := runCLI(t, "--help")
	if code != exitOK {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(stderr, "usage:") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestInvalidArgumentsEmitJSON(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name string
		args []string
	}{
		{"no language", []string{"--code", "x", "--workspace", dir}},
		{"bad language", []string{"--lang", "ruby", "--code", "x", "--workspace", dir}},
		{"no source", []string{"--lang", "bash", "--workspace", dir}},
		{"both sources", []string{"--lang", "bash", "--code", "x", "--file", "y", "--workspace", dir}},
		{"no workspace", []string{"--lang", "bash", "--code", "x"}},
		{"write and readonly", []string{"--lang", "bash", "--code", "x", "--workspace", dir, "--write", "--readonly"}},
		{"allow process python", []string{"--lang", "python", "--code", "x", "--workspace", dir, "--allow-process"}},
		{"zero timeout", []string{"--lang", "bash", "--code", "x", "--workspace", dir, "--timeout", "0s"}},
		{"negative timeout", []string{"--lang", "bash", "--code", "x", "--workspace", dir, "--timeout", "-1s"}},
		{"zero output", []string{"--lang", "bash", "--code", "x", "--workspace", dir, "--max-output-bytes", "0"}},
		{"huge output", []string{"--lang", "bash", "--code", "x", "--workspace", dir, "--max-output-bytes", "1073741825"}},
		{"zero source", []string{"--lang", "bash", "--code", "x", "--workspace", dir, "--max-source-bytes", "0"}},
		{"huge source", []string{"--lang", "bash", "--code", "x", "--workspace", dir, "--max-source-bytes", "268435457"}},
		{"positional", []string{"--lang", "bash", "--code", "x", "--workspace", dir, "extra"}},
		{"version positional", []string{"--version", "extra"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, _ := runCLI(t, tc.args...)
			if code != exitInvalid {
				t.Fatalf("exit = %d, want %d", code, exitInvalid)
			}
			res := decodeResult(t, stdout)
			if res.Status != scriptagent.StatusInvalidRequest {
				t.Fatalf("status = %s", res.Status)
			}
			if res.Error == nil || res.Error.Name != "InvalidRequest" {
				t.Fatalf("error = %+v", res.Error)
			}
		})
	}
}

func TestFileSourceLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "script.sh")
	if err := os.WriteFile(path, []byte("echo hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, _ := runCLI(t, "--lang", "bash", "--file", path, "--workspace", dir, "--max-source-bytes", "3")
	if code != exitInvalid {
		t.Fatalf("exit = %d", code)
	}
	res := decodeResult(t, stdout)
	if res.Status != scriptagent.StatusInvalidRequest {
		t.Fatalf("status = %s", res.Status)
	}
}

func TestReadSourceFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := readSourceFile(path, 5)
	if err != nil {
		t.Fatalf("readSourceFile: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("data = %q", data)
	}
	if _, err := readSourceFile(path, 4); err == nil {
		t.Fatal("readSourceFile succeeded over the limit")
	}
	if _, err := readSourceFile(filepath.Join(dir, "missing"), 5); err == nil {
		t.Fatal("readSourceFile succeeded for a missing file")
	}
}

func TestExitCodeFor(t *testing.T) {
	cases := map[scriptagent.Status]int{
		scriptagent.StatusCompleted:      exitOK,
		scriptagent.StatusFailed:         exitFailed,
		scriptagent.StatusInvalidRequest: exitInvalid,
		scriptagent.StatusDenied:         exitDenied,
		scriptagent.StatusStalled:        exitStalled,
		scriptagent.StatusUnsupported:    exitUnsupported,
		scriptagent.StatusTimedOut:       exitTimeout,
		scriptagent.StatusCancelled:      exitCancelled,
		scriptagent.StatusLimitExceeded:  exitLimit,
	}
	for status, want := range cases {
		if got := exitCodeFor(status); got != want {
			t.Fatalf("exitCodeFor(%s) = %d, want %d", status, got, want)
		}
	}
}

const cliWriteProbe = `import quickpy
async def main():
    try:
        await quickpy.fs.write_text("probe.txt", "hi")
        print("WROTE")
    except Exception as e:
        print("DENIED", e)
quickpy.run(main())
`

func cliOutputText(out []scriptagent.Output) string {
	var b strings.Builder
	for _, o := range out {
		if o.Kind == "text" {
			b.WriteString(o.Text)
		}
	}
	return b.String()
}

func TestCLIDefaultReadOnly(t *testing.T) {
	dir := t.TempDir()
	code, stdout, _ := runCLI(t, "--lang", "python", "--code", cliWriteProbe, "--workspace", dir)
	if code != exitOK {
		t.Fatalf("exit = %d", code)
	}
	res := decodeResult(t, stdout)
	text := cliOutputText(res.Output)
	if !strings.Contains(text, "DENIED") {
		t.Fatalf("write was not denied by default: %q", text)
	}
	if strings.Contains(text, "WROTE") {
		t.Fatalf("write succeeded without --write: %q", text)
	}
	if _, err := os.Stat(filepath.Join(dir, "probe.txt")); !os.IsNotExist(err) {
		t.Fatalf("probe.txt exists after a denied write: %v", err)
	}
}

func TestCLIWriteFlag(t *testing.T) {
	dir := t.TempDir()
	code, stdout, _ := runCLI(t, "--lang", "python", "--code", cliWriteProbe, "--workspace", dir, "--write")
	if code != exitOK {
		t.Fatalf("exit = %d", code)
	}
	res := decodeResult(t, stdout)
	text := cliOutputText(res.Output)
	if !strings.Contains(text, "WROTE") {
		t.Fatalf("write was not permitted with --write: %q", text)
	}
	if _, err := os.Stat(filepath.Join(dir, "probe.txt")); err != nil {
		t.Fatalf("probe.txt missing after --write: %v", err)
	}
}
