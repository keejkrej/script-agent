//go:build scriptagent_integration

package integration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	scriptagent "github.com/keejkrej/script-agent"
)

// emptyCwdEnv is a minimal environment for a run in an unrelated empty
// directory with a TMPDIR that does not exist. Python and JavaScript are
// embedded WASM guests, so neither needs a host temp directory.
func emptyCwdEnv(emptyCwd string) []string {
	return []string{
		"PATH=/usr/bin:/bin",
		"HOME=" + emptyCwd,
		"TMPDIR=" + filepath.Join(emptyCwd, "missing-tmp"),
	}
}

func TestCLIEmptyCwdNoTmpdirPython(t *testing.T) {
	emptyCwd := t.TempDir()
	ws := t.TempDir()
	run := runCLI(t, emptyCwd, emptyCwdEnv(emptyCwd),
		"--lang", "python", "--code", `print("py-e2e-ok")`, "--workspace", ws)
	if run.ExitCode != 0 {
		t.Fatalf("exit = %d, stderr = %s", run.ExitCode, run.Stderr)
	}
	res := decodeSingleJSON(t, run.Stdout)
	requireStatus(t, res, scriptagent.StatusCompleted)
	if res.Language != scriptagent.LanguagePython {
		t.Fatalf("language = %q", res.Language)
	}
	if got := outputText(res); got != "py-e2e-ok\n" {
		t.Fatalf("output text = %q", got)
	}
	if len(res.Output) != 1 || res.Output[0].Kind != "text" || res.Output[0].Stream != "stdout" {
		t.Fatalf("output is not one structured stdout text event: %+v", res.Output)
	}
	// The embedded artifact is the only runtime: the workspace stays empty.
	entries, err := os.ReadDir(ws)
	if err != nil {
		t.Fatalf("read workspace: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("workspace gained %d entries; no external runtime file should be needed", len(entries))
	}
}

func TestCLIEmptyCwdNoTmpdirJavaScript(t *testing.T) {
	emptyCwd := t.TempDir()
	ws := t.TempDir()
	run := runCLI(t, emptyCwd, emptyCwdEnv(emptyCwd),
		"--lang", "javascript", "--code", `text("js-e2e-ok"); return {ok: true};`, "--workspace", ws)
	if run.ExitCode != 0 {
		t.Fatalf("exit = %d, stderr = %s", run.ExitCode, run.Stderr)
	}
	res := decodeSingleJSON(t, run.Stdout)
	requireStatus(t, res, scriptagent.StatusCompleted)
	if res.Language != scriptagent.LanguageJavaScript {
		t.Fatalf("language = %q", res.Language)
	}
	if got := outputText(res); got != "js-e2e-ok" {
		t.Fatalf("output text = %q", got)
	}
	if !res.HasValue() {
		t.Fatal("missing return value")
	}
	entries, err := os.ReadDir(ws)
	if err != nil {
		t.Fatalf("read workspace: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("workspace gained %d entries; no external runtime file should be needed", len(entries))
	}
}

func TestCLIVersionArtifactIdentity(t *testing.T) {
	run := runCLI(t, t.TempDir(), nil, "--version")
	if run.ExitCode != 0 {
		t.Fatalf("exit = %d, stderr = %s", run.ExitCode, run.Stderr)
	}
	var info struct {
		Version   string   `json:"version"`
		Languages []string `json:"languages"`
		Artifact  struct {
			Name   string `json:"name"`
			Size   int    `json:"size"`
			SHA256 string `json:"sha256"`
			ABI    int    `json:"abi"`
			Engine string `json:"engine"`
		} `json:"pythonArtifact"`
	}
	if err := json.Unmarshal(run.Stdout, &info); err != nil {
		t.Fatalf("decode version: %v\nraw: %s", err, run.Stdout)
	}
	if info.Version == "" {
		t.Fatal("version is empty")
	}
	wantLangs := map[string]bool{"python": false, "javascript": false, "bash": false}
	for _, l := range info.Languages {
		if _, ok := wantLangs[l]; ok {
			wantLangs[l] = true
		}
	}
	for l, seen := range wantLangs {
		if !seen {
			t.Fatalf("languages %v missing %q", info.Languages, l)
		}
	}
	if info.Artifact.Name != "quickpy.wasm" {
		t.Fatalf("artifact name = %q", info.Artifact.Name)
	}
	if info.Artifact.Size != 256486 {
		t.Fatalf("artifact size = %d, want 256486", info.Artifact.Size)
	}
	const wantSHA = "5fdd351a70e0f65a4b812ad21c34a0885f87e839aee7721164768907ad61f50a"
	if info.Artifact.SHA256 != wantSHA {
		t.Fatalf("artifact sha256 = %q, want %q", info.Artifact.SHA256, wantSHA)
	}
	if info.Artifact.ABI != 4 {
		t.Fatalf("artifact abi = %d, want 4", info.Artifact.ABI)
	}
	if info.Artifact.Engine == "" {
		t.Fatal("artifact engine is empty")
	}
}

// TestCLIStdoutSingleJSONObject proves script text never leaks into stdout:
// even when the script prints a JSON-looking string, stdout is exactly one
// Result object and the script text lives in output.
func TestCLIStdoutSingleJSONObject(t *testing.T) {
	ws := t.TempDir()
	run := runCLI(t, t.TempDir(), nil,
		"--lang", "python", "--code", `print('{"nested": true}')`, "--workspace", ws)
	if run.ExitCode != 0 {
		t.Fatalf("exit = %d, stderr = %s", run.ExitCode, run.Stderr)
	}
	res := decodeSingleJSON(t, run.Stdout)
	requireStatus(t, res, scriptagent.StatusCompleted)
	if got := outputText(res); got != "{\"nested\": true}\n" {
		t.Fatalf("script text = %q", got)
	}
	if strings.Count(string(run.Stdout), `"status"`) != 1 {
		t.Fatalf("stdout is not a single Result object: %s", run.Stdout)
	}
}

// TestCLINoExternalRuntime runs Python and JavaScript with a PATH that cannot
// resolve node or python. The embedded WASM guests must still work.
func TestCLINoExternalRuntime(t *testing.T) {
	emptyCwd := t.TempDir()
	ws := t.TempDir()
	env := []string{"PATH=/nonexistent", "HOME=/nonexistent"}

	py := runCLI(t, emptyCwd, env, "--lang", "python", "--code", `print("py-no-path")`, "--workspace", ws)
	if py.ExitCode != 0 {
		t.Fatalf("python exit = %d, stderr = %s", py.ExitCode, py.Stderr)
	}
	if got := outputText(decodeSingleJSON(t, py.Stdout)); got != "py-no-path\n" {
		t.Fatalf("python output = %q", got)
	}

	js := runCLI(t, emptyCwd, env, "--lang", "javascript", "--code", `text("js-no-path")`, "--workspace", ws)
	if js.ExitCode != 0 {
		t.Fatalf("javascript exit = %d, stderr = %s", js.ExitCode, js.Stderr)
	}
	if got := outputText(decodeSingleJSON(t, js.Stdout)); got != "js-no-path" {
		t.Fatalf("javascript output = %q", got)
	}
}
