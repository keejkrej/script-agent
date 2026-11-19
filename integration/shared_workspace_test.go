//go:build scriptagent_integration

package integration

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	scriptagent "github.com/keejkrej/script-agent"
)

// TestSharedWorkspaceJSThenPythonThenJS proves the three engines share one
// real workspace through files, not through in-memory variables: JavaScript
// writes UTF-8 JSON and a binary subarray, Python reads and updates the JSON
// and reads the binary, and JavaScript reads both back.
func TestSharedWorkspaceJSThenPythonThenJS(t *testing.T) {
	ws := t.TempDir()
	a := newAgent(t, scriptagent.Config{
		Workspace:   ws,
		AllowWrites: true,
		Timeout:     20 * time.Second,
	})

	jsWrite := `
const doc = {n: 1, msg: "hello", list: [1, 2, 3]};
await fs.writeFile("data.json", JSON.stringify(doc));
const backing = new Uint8Array([9, 0, 127, 128, 255, 9]);
await fs.writeFile("blob.bin", backing.subarray(1, 5));
text("js-wrote");
`
	res := runAgent(t, a, scriptagent.Request{Language: scriptagent.LanguageJavaScript, Source: jsWrite})
	requireStatus(t, res, scriptagent.StatusCompleted)
	if got := outputText(res); got != "js-wrote" {
		t.Fatalf("js write output = %q", got)
	}

	pyUpdate := `
import quickpy, json, binascii
async def main():
    doc = json.loads(await quickpy.fs.read_text("data.json"))
    doc["n"] = doc["n"] + 41
    doc["fromPython"] = True
    await quickpy.fs.write_text("data.json", json.dumps(doc))
    blob = await quickpy.fs.read_bytes("blob.bin")
    print("py-blob", str(binascii.hexlify(blob), "ascii"))
quickpy.run(main())
`
	res = runAgent(t, a, scriptagent.Request{Language: scriptagent.LanguagePython, Source: pyUpdate})
	requireStatus(t, res, scriptagent.StatusCompleted)
	if got := outputText(res); got != "py-blob 007f80ff\n" {
		t.Fatalf("python output = %q", got)
	}

	jsRead := `
const doc = JSON.parse(await fs.readFile("data.json", "utf8"));
const blob = await fs.readFile("blob.bin");
text(JSON.stringify({n: doc.n, msg: doc.msg, fromPython: doc.fromPython, blob: Array.from(blob)}));
return doc;
`
	res = runAgent(t, a, scriptagent.Request{Language: scriptagent.LanguageJavaScript, Source: jsRead})
	requireStatus(t, res, scriptagent.StatusCompleted)
	want := `{"n":42,"msg":"hello","fromPython":true,"blob":[0,127,128,255]}`
	if got := outputText(res); got != want {
		t.Fatalf("js read output = %q, want %q", got, want)
	}
	value, ok := res.Value.(map[string]any)
	if !ok {
		// The JavaScript engine returns a jsonx object; normalize it through
		// its JSON form so the assertion does not depend on an internal type.
		rawValue, err := json.Marshal(res.Value)
		if err != nil {
			t.Fatalf("marshal value: %v", err)
		}
		if err := json.Unmarshal(rawValue, &value); err != nil {
			t.Fatalf("unmarshal value: %v", err)
		}
	}
	if value["n"] != float64(42) || value["msg"] != "hello" || value["fromPython"] != true {
		t.Fatalf("value = %#v", value)
	}

	// The host sees the same bytes the guests wrote.
	raw, err := os.ReadFile(filepath.Join(ws, "data.json"))
	if err != nil {
		t.Fatalf("read data.json: %v", err)
	}
	var hostDoc map[string]any
	if err := json.Unmarshal(raw, &hostDoc); err != nil {
		t.Fatalf("host data.json is not JSON: %v", err)
	}
	if hostDoc["n"] != float64(42) || hostDoc["fromPython"] != true {
		t.Fatalf("host data.json = %#v", hostDoc)
	}
	blob, err := os.ReadFile(filepath.Join(ws, "blob.bin"))
	if err != nil {
		t.Fatalf("read blob.bin: %v", err)
	}
	if !bytes.Equal(blob, []byte{0, 127, 128, 255}) {
		t.Fatalf("host blob.bin = %v", blob)
	}
}

// TestSharedWorkspaceFreshGlobals proves that a variable set in one execution
// is not visible in the next: only files persist, never guest globals.
func TestSharedWorkspaceFreshGlobals(t *testing.T) {
	a := newAgent(t, scriptagent.Config{Workspace: t.TempDir(), Timeout: 20 * time.Second})

	pySet := runAgent(t, a, scriptagent.Request{
		Language: scriptagent.LanguagePython,
		Source:   "x = 42\nprint('set')\n",
	})
	requireStatus(t, pySet, scriptagent.StatusCompleted)
	pyCheck := runAgent(t, a, scriptagent.Request{
		Language: scriptagent.LanguagePython,
		Source:   "print('x' in globals())\n",
	})
	requireStatus(t, pyCheck, scriptagent.StatusCompleted)
	if got := outputText(pyCheck); got != "False\n" {
		t.Fatalf("python global leaked across calls: %q", got)
	}

	jsSet := runAgent(t, a, scriptagent.Request{
		Language: scriptagent.LanguageJavaScript,
		Source:   "globalThis.leak = 99; text('set');",
	})
	requireStatus(t, jsSet, scriptagent.StatusCompleted)
	jsCheck := runAgent(t, a, scriptagent.Request{
		Language: scriptagent.LanguageJavaScript,
		Source:   "text(typeof globalThis.leak);",
	})
	requireStatus(t, jsCheck, scriptagent.StatusCompleted)
	if got := outputText(jsCheck); got != "undefined" {
		t.Fatalf("javascript global leaked across calls: %q", got)
	}
}

// TestSharedWorkspaceTwoRootsIsolation proves two Agents with different roots
// cannot reach each other or an outer sentinel, and that the sentinel is
// unchanged.
func TestSharedWorkspaceTwoRootsIsolation(t *testing.T) {
	parent := t.TempDir()
	rootA := filepath.Join(parent, "a")
	rootB := filepath.Join(parent, "b")
	for _, dir := range []string{rootA, rootB} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	sentinel := filepath.Join(parent, "sentinel.txt")
	const sentinelText = "OUTER-SENTINEL"
	if err := os.WriteFile(sentinel, []byte(sentinelText), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootB, "secret.txt"), []byte("B-SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}

	a := newAgent(t, scriptagent.Config{Workspace: rootA, Timeout: 20 * time.Second})
	b := newAgent(t, scriptagent.Config{Workspace: rootB, Timeout: 20 * time.Second})

	py := runAgent(t, a, scriptagent.Request{
		Language: scriptagent.LanguagePython,
		Source: `import quickpy
async def main():
    for path in ("../sentinel.txt", "../b/secret.txt"):
        try:
            print("READ", await quickpy.fs.read_text(path))
        except ValueError as e:
            print("PY-INVALID", path)
quickpy.run(main())
`,
	})
	requireStatus(t, py, scriptagent.StatusCompleted)
	if got := outputText(py); !strings.Contains(got, "PY-INVALID ../sentinel.txt") ||
		!strings.Contains(got, "PY-INVALID ../b/secret.txt") {
		t.Fatalf("python traversal was not rejected: %q", got)
	}

	js := runAgent(t, b, scriptagent.Request{
		Language: scriptagent.LanguageJavaScript,
		Source: `for (const path of ["../sentinel.txt", "../a"]) {
  try { text("READ " + await fs.readFile(path, "utf8")); }
  catch (e) { text("JS-INVALID " + path + " " + e.code); }
}`,
	})
	requireStatus(t, js, scriptagent.StatusCompleted)
	if got := outputText(js); !strings.Contains(got, "JS-INVALID ../sentinel.txt EINVAL") {
		t.Fatalf("javascript traversal was not rejected: %q", got)
	}

	got, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatalf("read sentinel: %v", err)
	}
	if string(got) != sentinelText {
		t.Fatalf("outer sentinel changed: %q", got)
	}
}

// TestJavaScriptFSFrozenPromiseShape verifies the public `fs` facade is frozen
// and every method returns a real Promise, including for a missing file.
func TestJavaScriptFSFrozenPromiseShape(t *testing.T) {
	a := newAgent(t, scriptagent.Config{Workspace: t.TempDir(), Timeout: 20 * time.Second})
	src := `
const p = fs.readFile("missing.txt", "utf8");
const isPromise = p instanceof Promise;
const frozen = Object.isFrozen(fs);
const keys = Object.keys(fs).sort().join(",");
text(JSON.stringify({frozen, isPromise, keys}));
try { await p; } catch (e) { text("caught " + e.code); }
`
	res := runAgent(t, a, scriptagent.Request{Language: scriptagent.LanguageJavaScript, Source: src})
	requireStatus(t, res, scriptagent.StatusCompleted)
	got := outputText(res)
	if !strings.Contains(got, `"frozen":true`) {
		t.Fatalf("fs is not frozen: %q", got)
	}
	if !strings.Contains(got, `"isPromise":true`) {
		t.Fatalf("fs.readFile did not return a Promise: %q", got)
	}
	for _, name := range []string{"exists", "mkdir", "readFile", "readdir", "rm", "writeFile"} {
		if !strings.Contains(got, name) {
			t.Fatalf("fs facade missing %q: %q", name, got)
		}
	}
	if !strings.Contains(got, "caught ENOENT") {
		t.Fatalf("missing file did not reject with ENOENT: %q", got)
	}
}
