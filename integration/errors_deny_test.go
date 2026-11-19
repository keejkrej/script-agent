//go:build scriptagent_integration

package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	scriptagent "github.com/keejkrej/script-agent"
)

// TestReadOnlyMutationsDeniedPython proves the default read-only posture
// denies a Python write with the guest's permission error and leaves no file.
func TestReadOnlyMutationsDeniedPython(t *testing.T) {
	ws := t.TempDir()
	a := newAgent(t, scriptagent.Config{Workspace: ws, Timeout: 20 * time.Second})
	src := `import quickpy
async def main():
    try:
        await quickpy.fs.write_text("denied.txt", "hi")
        print("WROTE")
    except OSError as e:
        print("DENIED", e.errno)
quickpy.run(main())
`
	res := runAgent(t, a, scriptagent.Request{Language: scriptagent.LanguagePython, Source: src})
	requireStatus(t, res, scriptagent.StatusCompleted)
	if got := outputText(res); got != "DENIED 13\n" {
		t.Fatalf("output = %q, want a permission denial", got)
	}
	if _, err := os.Stat(filepath.Join(ws, "denied.txt")); !os.IsNotExist(err) {
		t.Fatalf("denied.txt exists after a read-only write: %v", err)
	}
}

// TestReadOnlyMutationsDeniedJavaScript proves the same for the QuickJS fs
// facade, both caught (EACCES) and uncaught (failed result).
func TestReadOnlyMutationsDeniedJavaScript(t *testing.T) {
	ws := t.TempDir()
	a := newAgent(t, scriptagent.Config{Workspace: ws, Timeout: 20 * time.Second})

	caught := runAgent(t, a, scriptagent.Request{
		Language: scriptagent.LanguageJavaScript,
		Source: `try { await fs.writeFile("denied.txt", "hi"); text("WROTE"); }
catch (e) { text("DENIED " + e.code + " " + e.capabilityCode); }`,
	})
	requireStatus(t, caught, scriptagent.StatusCompleted)
	if got := outputText(caught); got != "DENIED EACCES permission_denied" {
		t.Fatalf("output = %q", got)
	}

	uncaught := runAgent(t, a, scriptagent.Request{
		Language: scriptagent.LanguageJavaScript,
		Source:   `await fs.writeFile("denied.txt", "hi");`,
	})
	requireStatus(t, uncaught, scriptagent.StatusFailed)
	if uncaught.Error == nil || !strings.Contains(uncaught.Error.Message, "read-only") {
		t.Fatalf("uncaught error = %+v", uncaught.Error)
	}
	if _, err := os.Stat(filepath.Join(ws, "denied.txt")); !os.IsNotExist(err) {
		t.Fatalf("denied.txt exists after a read-only write: %v", err)
	}
}

// TestBashDeniedByDefault proves Bash is denied before any process starts when
// the Agent-level process grant is off.
func TestBashDeniedByDefault(t *testing.T) {
	ws := t.TempDir()
	a := newAgent(t, scriptagent.Config{Workspace: ws, Timeout: 20 * time.Second})
	res := runAgent(t, a, scriptagent.Request{
		Language:     scriptagent.LanguageBash,
		Source:       "touch denied.txt",
		AllowProcess: true,
	})
	requireStatus(t, res, scriptagent.StatusDenied)
	if res.Error == nil || res.Error.Code != "process_denied" {
		t.Fatalf("error = %+v", res.Error)
	}
	if _, err := os.Stat(filepath.Join(ws, "denied.txt")); !os.IsNotExist(err) {
		t.Fatalf("denied.txt exists; a denied Bash request started a process: %v", err)
	}
}

// TestCLIRequiresWorkspace proves the CLI never falls back to an implicit
// working directory.
func TestCLIRequiresWorkspace(t *testing.T) {
	run := runCLI(t, t.TempDir(), nil, "--lang", "python", "--code", `print(1)`)
	if run.ExitCode != 2 {
		t.Fatalf("exit = %d, want 2", run.ExitCode)
	}
	res := decodeSingleJSON(t, run.Stdout)
	requireStatus(t, res, scriptagent.StatusInvalidRequest)
	if res.Error == nil || res.Error.Code != "workspace" {
		t.Fatalf("error = %+v", res.Error)
	}
}

// TestCLICodeFileMutualExclusion proves exactly one source flag is required.
func TestCLICodeFileMutualExclusion(t *testing.T) {
	ws := t.TempDir()
	file := filepath.Join(ws, "s.py")
	if err := os.WriteFile(file, []byte("print(1)"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		args []string
	}{
		{"both", []string{"--lang", "python", "--code", "print(1)", "--file", file, "--workspace", ws}},
		{"neither", []string{"--lang", "python", "--workspace", ws}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run := runCLI(t, ws, nil, tc.args...)
			if run.ExitCode != 2 {
				t.Fatalf("exit = %d, want 2", run.ExitCode)
			}
			res := decodeSingleJSON(t, run.Stdout)
			requireStatus(t, res, scriptagent.StatusInvalidRequest)
			if res.Error == nil || res.Error.Code != "source" {
				t.Fatalf("error = %+v", res.Error)
			}
		})
	}
}

// TestCLISourceCaps proves the source cap is enforced at the boundary and a
// source exactly at the cap still runs.
func TestCLISourceCaps(t *testing.T) {
	ws := t.TempDir()
	const src = `print("hello world")` // 20 bytes
	if len(src) != 20 {
		t.Fatalf("test source is %d bytes, want 20", len(src))
	}

	over := runCLI(t, ws, nil, "--lang", "python", "--code", src, "--workspace", ws, "--max-source-bytes", "5")
	if over.ExitCode != 2 {
		t.Fatalf("over-limit exit = %d, want 2", over.ExitCode)
	}
	res := decodeSingleJSON(t, over.Stdout)
	requireStatus(t, res, scriptagent.StatusInvalidRequest)
	if res.Error == nil || res.Error.Code != "source_too_large" {
		t.Fatalf("error = %+v", res.Error)
	}

	atLimit := runCLI(t, ws, nil, "--lang", "python", "--code", src, "--workspace", ws, "--max-source-bytes", "20")
	if atLimit.ExitCode != 0 {
		t.Fatalf("at-limit exit = %d, stderr = %s", atLimit.ExitCode, atLimit.Stderr)
	}
	requireStatus(t, decodeSingleJSON(t, atLimit.Stdout), scriptagent.StatusCompleted)

	// A large source is a source-cap violation, never an "unsupported" result.
	huge := strings.Repeat("# padding\n", 30000) // 300 KB
	big := runCLI(t, ws, nil, "--lang", "python", "--code", huge, "--workspace", ws)
	if big.ExitCode != 2 {
		t.Fatalf("huge exit = %d, want 2", big.ExitCode)
	}
	bigRes := decodeSingleJSON(t, big.Stdout)
	requireStatus(t, bigRes, scriptagent.StatusInvalidRequest)
	if bigRes.Error == nil || bigRes.Error.Code != "source_too_large" {
		t.Fatalf("huge error = %+v", bigRes.Error)
	}
}

// TestCLINegativeInvalidFlags proves malformed numeric flags produce a JSON
// invalid_request and exit 2, not a crash or a partial document.
func TestCLINegativeInvalidFlags(t *testing.T) {
	ws := t.TempDir()
	cases := []struct {
		name string
		args []string
	}{
		{"negative timeout", []string{"--lang", "python", "--code", "print(1)", "--workspace", ws, "--timeout", "-1s"}},
		{"zero timeout", []string{"--lang", "python", "--code", "print(1)", "--workspace", ws, "--timeout", "0s"}},
		{"zero output", []string{"--lang", "python", "--code", "print(1)", "--workspace", ws, "--max-output-bytes", "0"}},
		{"negative output", []string{"--lang", "python", "--code", "print(1)", "--workspace", ws, "--max-output-bytes", "-5"}},
		{"huge output", []string{"--lang", "python", "--code", "print(1)", "--workspace", ws, "--max-output-bytes", "1073741825"}},
		{"zero source", []string{"--lang", "python", "--code", "print(1)", "--workspace", ws, "--max-source-bytes", "0"}},
		{"huge source", []string{"--lang", "python", "--code", "print(1)", "--workspace", ws, "--max-source-bytes", "268435457"}},
		{"write and readonly", []string{"--lang", "python", "--code", "print(1)", "--workspace", ws, "--write", "--readonly"}},
		{"allow process python", []string{"--lang", "python", "--code", "print(1)", "--workspace", ws, "--allow-process"}},
		{"positional", []string{"--lang", "python", "--code", "print(1)", "--workspace", ws, "extra"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run := runCLI(t, ws, nil, tc.args...)
			if run.ExitCode != 2 {
				t.Fatalf("exit = %d, want 2 (stderr = %s)", run.ExitCode, run.Stderr)
			}
			res := decodeSingleJSON(t, run.Stdout)
			requireStatus(t, res, scriptagent.StatusInvalidRequest)
			if res.Error == nil || res.Error.Name != "InvalidRequest" {
				t.Fatalf("error = %+v", res.Error)
			}
		})
	}
}
