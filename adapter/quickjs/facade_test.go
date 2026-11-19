package quickjs

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/keejkrej/pi-go/codemode"
	codemoderuntime "github.com/keejkrej/pi-go/codemode/runtime"
	"github.com/keejkrej/quickpy/capability"
)

func newSandbox(t *testing.T, a *capability.Adapter) *codemoderuntime.CodemodeSandbox {
	t.Helper()
	timeout := 10_000.0
	sb, err := codemoderuntime.NewCodemodeSandbox(&codemode.CodemodeSandboxOptions{
		TimeoutMs:            &timeout,
		TrustedPreludeSource: FacadeSource,
	})
	if err != nil {
		t.Fatalf("NewCodemodeSandbox: %v", err)
	}
	if err := Install(sb, a); err != nil {
		_ = sb.Close()
		t.Fatalf("Install: %v", err)
	}
	t.Cleanup(func() { _ = sb.Close() })
	return sb
}

func newSandboxWithAdapter(t *testing.T, a adapter) *codemoderuntime.CodemodeSandbox {
	t.Helper()
	timeout := 10_000.0
	sb, err := codemoderuntime.NewCodemodeSandbox(&codemode.CodemodeSandboxOptions{
		TimeoutMs:            &timeout,
		TrustedPreludeSource: FacadeSource,
	})
	if err != nil {
		t.Fatalf("NewCodemodeSandbox: %v", err)
	}
	if err := install(sb, a); err != nil {
		_ = sb.Close()
		t.Fatalf("install: %v", err)
	}
	t.Cleanup(func() { _ = sb.Close() })
	return sb
}

func newWorkspaceSandbox(t *testing.T, readOnly bool) (*capability.Adapter, *codemoderuntime.CodemodeSandbox, string) {
	t.Helper()
	dir := t.TempDir()
	a, err := capability.NewWorkspace(capability.WorkspaceOptions{Root: dir, ReadOnly: readOnly})
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return a, newSandbox(t, a), dir
}

func execSuccess(t *testing.T, sb *codemoderuntime.CodemodeSandbox, code string) *codemode.CodemodeSuccess {
	t.Helper()
	res, err := sb.Execute(context.Background(), code, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	succ, ok := res.(*codemode.CodemodeSuccess)
	if !ok {
		fail := res.(*codemode.CodemodeFailure)
		t.Fatalf("expected success, got failure: %+v", fail.Error)
	}
	return succ
}

func execValue(t *testing.T, sb *codemoderuntime.CodemodeSandbox, code string) string {
	t.Helper()
	succ := execSuccess(t, sb, code)
	val, ok := succ.Value.Get()
	if !ok {
		t.Fatal("result has no value")
	}
	s, err := json.Marshal(val)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return string(s)
}

func TestFacadeReadFileText(t *testing.T) {
	_, sb, dir := newWorkspaceSandbox(t, false)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("héllo 🌍"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := execValue(t, sb, `
		const a = await fs.readFile("a.txt", "utf8");
		const b = await fs.readFile("a.txt", "utf-8");
		const c = await fs.readFile("a.txt", { encoding: "utf8" });
		return [a, b, c];
	`)
	want := `["héllo 🌍","héllo 🌍","héllo 🌍"]`
	if got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestFacadeReadFileBinary(t *testing.T) {
	_, sb, dir := newWorkspaceSandbox(t, false)
	if err := os.WriteFile(filepath.Join(dir, "b.bin"), []byte{0x00, 0xFF, 0x41}, 0o600); err != nil {
		t.Fatal(err)
	}
	got := execValue(t, sb, `
		const bytes = await fs.readFile("b.bin");
		const nullBytes = await fs.readFile("b.bin", null);
		return {
			isUint8Array: bytes instanceof Uint8Array,
			bytes: Array.from(bytes),
			nullBytes: Array.from(nullBytes),
		};
	`)
	want := `{"isUint8Array":true,"bytes":[0,255,65],"nullBytes":[0,255,65]}`
	if got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestFacadeReadFileUnsupportedEncoding(t *testing.T) {
	_, sb, dir := newWorkspaceSandbox(t, false)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := execValue(t, sb, `
		const p = fs.readFile("a.txt", "latin1");
		const isPromise = p instanceof Promise;
		try {
			await p;
			return { isPromise, rejected: false };
		} catch (e) {
			return { isPromise, rejected: true, code: e.code, name: e.name };
		}
	`)
	want := `{"isPromise":true,"rejected":true,"code":"ERR_UNKNOWN_ENCODING","name":"Error"}`
	if got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestFacadeReadFileInvalidOptions(t *testing.T) {
	_, sb, dir := newWorkspaceSandbox(t, false)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := execValue(t, sb, `
		const cases = [];
		for (const options of [123, true, [], { flag: "r" }, { encoding: 123 }]) {
			try {
				await fs.readFile("a.txt", options);
				cases.push("ok");
			} catch (e) {
				cases.push(e.code);
			}
		}
		return cases;
	`)
	want := `["ERR_INVALID_ARG_TYPE","ERR_INVALID_ARG_TYPE","ERR_INVALID_ARG_TYPE","ERR_INVALID_ARG_TYPE","ERR_INVALID_ARG_TYPE"]`
	if got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestFacadeReadFileInvalidPathType(t *testing.T) {
	_, sb, _ := newWorkspaceSandbox(t, false)
	got := execValue(t, sb, `
		const cases = [];
		for (const path of [123, null, undefined, true, {}, []]) {
			try {
				await fs.readFile(path);
				cases.push("ok");
			} catch (e) {
				cases.push(e.code);
			}
		}
		return cases;
	`)
	want := `["ERR_INVALID_ARG_TYPE","ERR_INVALID_ARG_TYPE","ERR_INVALID_ARG_TYPE","ERR_INVALID_ARG_TYPE","ERR_INVALID_ARG_TYPE","ERR_INVALID_ARG_TYPE"]`
	if got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestFacadeWriteFileText(t *testing.T) {
	_, sb, dir := newWorkspaceSandbox(t, false)
	got := execValue(t, sb, `
		const value = await fs.writeFile("c.txt", "héllo");
		return { value: value === undefined, read: await fs.readFile("c.txt", "utf8") };
	`)
	want := `{"value":true,"read":"héllo"}`
	if got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
	data, err := os.ReadFile(filepath.Join(dir, "c.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "héllo" {
		t.Fatalf("file = %q", data)
	}
}

func TestFacadeWriteFileBinarySubarray(t *testing.T) {
	_, sb, dir := newWorkspaceSandbox(t, false)
	got := execValue(t, sb, `
		const source = new Uint8Array([0x00, 0xFF, 0x41, 0x42, 0x43]);
		const view = source.subarray(1, 4);
		const value = await fs.writeFile("d.bin", view);
		const read = await fs.readFile("d.bin");
		return { value: value === undefined, read: Array.from(read) };
	`)
	want := `{"value":true,"read":[255,65,66]}`
	if got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
	data, err := os.ReadFile(filepath.Join(dir, "d.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, []byte{0xFF, 0x41, 0x42}) {
		t.Fatalf("file = %v", data)
	}
}

func TestFacadeWriteFileInvalidData(t *testing.T) {
	_, sb, _ := newWorkspaceSandbox(t, false)
	got := execValue(t, sb, `
		const cases = [];
		for (const data of [123, null, undefined, {}, [], new ArrayBuffer(1)]) {
			try {
				await fs.writeFile("x", data);
				cases.push("ok");
			} catch (e) {
				cases.push(e.code);
			}
		}
		return cases;
	`)
	want := `["ERR_INVALID_ARG_TYPE","ERR_INVALID_ARG_TYPE","ERR_INVALID_ARG_TYPE","ERR_INVALID_ARG_TYPE","ERR_INVALID_ARG_TYPE","ERR_INVALID_ARG_TYPE"]`
	if got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestFacadeWriteFileOptions(t *testing.T) {
	_, sb, _ := newWorkspaceSandbox(t, false)
	got := execValue(t, sb, `
		try {
			await fs.writeFile("x", "y", {});
			return "ok";
		} catch (e) {
			return e.code;
		}
	`)
	want := `"ERR_UNSUPPORTED_OPERATION"`
	if got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestFacadeReaddir(t *testing.T) {
	_, sb, dir := newWorkspaceSandbox(t, false)
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := execValue(t, sb, `
		const root = await fs.readdir();
		const sub = await fs.readdir("sub");
		return { root, sub };
	`)
	want := `{"root":["a.txt","b.txt","sub"],"sub":[]}`
	if got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestFacadeReaddirOptions(t *testing.T) {
	_, sb, _ := newWorkspaceSandbox(t, false)
	got := execValue(t, sb, `
		try {
			await fs.readdir(".", { withFileTypes: true });
			return "ok";
		} catch (e) {
			return e.code;
		}
	`)
	want := `"ERR_UNSUPPORTED_OPERATION"`
	if got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestFacadeMkdir(t *testing.T) {
	_, sb, _ := newWorkspaceSandbox(t, false)
	got := execValue(t, sb, `
		const first = await fs.mkdir("d");
		let second;
		try {
			await fs.mkdir("d");
			second = "ok";
		} catch (e) {
			second = e.code;
		}
		const nested = await fs.mkdir("e/f", { recursive: true });
		const again = await fs.mkdir("e/f", { recursive: true });
		return { first, second, nested, again };
	`)
	want := `{"first":true,"second":"EEXIST","nested":true,"again":false}`
	if got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestFacadeMkdirInvalidOptions(t *testing.T) {
	_, sb, _ := newWorkspaceSandbox(t, false)
	got := execValue(t, sb, `
		const cases = [];
		for (const options of [{ recursive: "yes" }, { mode: 0o777 }, 123, []]) {
			try {
				await fs.mkdir("x", options);
				cases.push("ok");
			} catch (e) {
				cases.push(e.code);
			}
		}
		return cases;
	`)
	want := `["ERR_INVALID_ARG_TYPE","ERR_INVALID_ARG_TYPE","ERR_INVALID_ARG_TYPE","ERR_INVALID_ARG_TYPE"]`
	if got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestFacadeRm(t *testing.T) {
	_, sb, dir := newWorkspaceSandbox(t, false)
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("f"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "nonempty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "nonempty", "x"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := execValue(t, sb, `
		const file = await fs.rm("f.txt");
		const missing = await fs.rm("missing");
		let nonempty;
		try {
			await fs.rm("nonempty");
			nonempty = "ok";
		} catch (e) {
			nonempty = e.code;
		}
		let recursive;
		try {
			await fs.rm("nonempty", { recursive: true });
			recursive = "ok";
		} catch (e) {
			recursive = e.code;
		}
		return { file: file === undefined, missing: missing === undefined, nonempty, recursive };
	`)
	want := `{"file":true,"missing":true,"nonempty":"ENOTEMPTY","recursive":"ERR_UNSUPPORTED_OPERATION"}`
	if got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
	if _, err := os.Stat(filepath.Join(dir, "f.txt")); !os.IsNotExist(err) {
		t.Fatalf("f.txt still exists: %v", err)
	}
}

func TestFacadeExists(t *testing.T) {
	_, sb, dir := newWorkspaceSandbox(t, false)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := execValue(t, sb, `
		return { yes: await fs.exists("a.txt"), no: await fs.exists("missing") };
	`)
	want := `{"yes":true,"no":false}`
	if got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestFacadePromiseShape(t *testing.T) {
	_, sb, dir := newWorkspaceSandbox(t, false)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := execValue(t, sb, `
		const calls = [
			fs.readFile("a.txt"),
			fs.writeFile("b.txt", "b"),
			fs.readdir(),
			fs.mkdir("d"),
			fs.rm("missing"),
			fs.exists("a.txt"),
		];
		const shapes = calls.map((p) => p instanceof Promise);
		await Promise.all(calls);
		return {
			shapes,
			readFileSync: typeof fs.readFileSync,
			writeFileSync: typeof fs.writeFileSync,
		};
	`)
	want := `{"shapes":[true,true,true,true,true,true],"readFileSync":"undefined","writeFileSync":"undefined"}`
	if got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestFacadeNamespaceFrozen(t *testing.T) {
	_, sb, _ := newWorkspaceSandbox(t, false)
	got := execValue(t, sb, `
		"use strict";
		const before = fs.readFile;
		let threw = false;
		try {
			fs.readFile = null;
		} catch (e) {
			threw = true;
		}
		const descriptor = Object.getOwnPropertyDescriptor(globalThis, "fs");
		return {
			frozen: Object.isFrozen(fs),
			writable: descriptor.writable,
			configurable: descriptor.configurable,
			threw,
			same: fs.readFile === before,
			type: typeof fs.readFile,
		};
	`)
	want := `{"frozen":true,"writable":false,"configurable":false,"threw":true,"same":true,"type":"function"}`
	if got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestFacadePrivateBridgeInaccessible(t *testing.T) {
	_, sb, _ := newWorkspaceSandbox(t, false)
	got := execValue(t, sb, `
		return {
			globalBridge: typeof globalThis.bridge,
			fsBridge: typeof fs.bridge,
			fsKeys: Object.keys(fs).sort(),
		};
	`)
	want := `{"globalBridge":"undefined","fsBridge":"undefined","fsKeys":["exists","mkdir","readFile","readdir","rm","writeFile"]}`
	if got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestFacadeNoImportOrBuffer(t *testing.T) {
	_, sb, _ := newWorkspaceSandbox(t, false)
	got := execValue(t, sb, `
		return {
			require: typeof require,
			buffer: typeof Buffer,
			process: typeof process,
			globalImport: typeof globalThis.import,
		};
	`)
	want := `{"require":"undefined","buffer":"undefined","process":"undefined","globalImport":"undefined"}`
	if got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestFacadeErrorCodeMapping(t *testing.T) {
	codes := []string{
		capability.CodeNotFound,
		capability.CodePermissionDenied,
		capability.CodeInvalidPath,
		capability.CodeInvalidArgument,
		capability.CodeToolArgument,
		capability.CodeTooLarge,
		capability.CodeIsDirectory,
		capability.CodeNotDirectory,
		capability.CodeExists,
		capability.CodeNotEmpty,
		capability.CodeIOError,
		capability.CodeInvalidUTF8,
		capability.CodeInvalidBase64,
	}
	a := &sequenceAdapter{ops: []string{capability.OpReadText}, codes: codes}
	sb := newSandboxWithAdapter(t, a)
	got := execValue(t, sb, `
		const codes = `+jsonString(t, codes)+`;
		const results = [];
		for (const code of codes) {
			try {
				await fs.readFile("x", "utf8");
				results.push("ok");
			} catch (e) {
				results.push({
					code: e.code,
					capabilityCode: e.capabilityCode,
					path: e.path,
					message: e.message,
				});
			}
		}
		return results;
	`)
	want := `[` +
		`{"code":"ENOENT","capabilityCode":"not_found","path":"x","message":"message for not_found"},` +
		`{"code":"EACCES","capabilityCode":"permission_denied","path":"x","message":"message for permission_denied"},` +
		`{"code":"EINVAL","capabilityCode":"invalid_path","path":"x","message":"message for invalid_path"},` +
		`{"code":"EINVAL","capabilityCode":"invalid_argument","path":"x","message":"message for invalid_argument"},` +
		`{"code":"EINVAL","capabilityCode":"tool_argument","path":"x","message":"message for tool_argument"},` +
		`{"code":"EFBIG","capabilityCode":"too_large","path":"x","message":"message for too_large"},` +
		`{"code":"EISDIR","capabilityCode":"is_directory","path":"x","message":"message for is_directory"},` +
		`{"code":"ENOTDIR","capabilityCode":"not_directory","path":"x","message":"message for not_directory"},` +
		`{"code":"EEXIST","capabilityCode":"exists","path":"x","message":"message for exists"},` +
		`{"code":"ENOTEMPTY","capabilityCode":"not_empty","path":"x","message":"message for not_empty"},` +
		`{"code":"EIO","capabilityCode":"io_error","path":"x","message":"message for io_error"},` +
		`{"code":"EINVAL","capabilityCode":"invalid_utf8","path":"x","message":"message for invalid_utf8"},` +
		`{"code":"EINVAL","capabilityCode":"invalid_base64","path":"x","message":"message for invalid_base64"}` +
		`]`
	if got != want {
		t.Fatalf("got %s\nwant %s", got, want)
	}
}

func TestFacadePathTraversalNoRootDisclosure(t *testing.T) {
	_, sb, dir := newWorkspaceSandbox(t, false)
	got := execValue(t, sb, `
		const results = [];
		for (const path of ["../x", "/etc/passwd", "a/../../x", "a\\b", "a:b"]) {
			try {
				await fs.readFile(path);
				results.push("ok");
			} catch (e) {
				results.push({
					code: e.code,
					capabilityCode: e.capabilityCode,
					path: e.path,
					message: e.message,
				});
			}
		}
		return results;
	`)
	var results []map[string]any
	if err := json.Unmarshal([]byte(got), &results); err != nil {
		t.Fatalf("unmarshal %s: %v", got, err)
	}
	if len(results) != 5 {
		t.Fatalf("got %d results", len(results))
	}
	for i, result := range results {
		if result["code"] != "EINVAL" {
			t.Fatalf("result %d code = %v", i, result["code"])
		}
		if result["capabilityCode"] != "invalid_path" {
			t.Fatalf("result %d capabilityCode = %v", i, result["capabilityCode"])
		}
		message, _ := result["message"].(string)
		if strings.Contains(message, dir) {
			t.Fatalf("result %d message leaks the host root: %q", i, message)
		}
	}
}

func TestFacadeReadOnly(t *testing.T) {
	_, sb, dir := newWorkspaceSandbox(t, true)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := execValue(t, sb, `
		const results = [];
		for (const call of [
			() => fs.writeFile("b.txt", "b"),
			() => fs.mkdir("d"),
			() => fs.rm("a.txt"),
		]) {
			try {
				await call();
				results.push("ok");
			} catch (e) {
				results.push({ code: e.code, capabilityCode: e.capabilityCode });
			}
		}
		return results;
	`)
	want := `[{"code":"EACCES","capabilityCode":"permission_denied"},{"code":"EACCES","capabilityCode":"permission_denied"},{"code":"EACCES","capabilityCode":"permission_denied"}]`
	if got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestFacadeFreshVMPersistsFile(t *testing.T) {
	_, sb, _ := newWorkspaceSandbox(t, false)
	execValue(t, sb, `await fs.writeFile("persist.txt", "hello"); return 1;`)
	got := execValue(t, sb, `return await fs.readFile("persist.txt", "utf8");`)
	want := `"hello"`
	if got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestFacadePromiseAll(t *testing.T) {
	_, sb, dir := newWorkspaceSandbox(t, false)
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name[:1]), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got := execValue(t, sb, `
		const values = await Promise.all([
			fs.readFile("a.txt", "utf8"),
			fs.readFile("b.txt", "utf8"),
			fs.readFile("c.txt", "utf8"),
		]);
		return values;
	`)
	want := `["a","b","c"]`
	if got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestFacadePromiseAllSettled(t *testing.T) {
	_, sb, dir := newWorkspaceSandbox(t, false)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := execValue(t, sb, `
		const results = await Promise.allSettled([
			fs.readFile("a.txt", "utf8"),
			fs.readFile("missing.txt", "utf8"),
			fs.readFile("b.txt", "utf8"),
		]);
		return results.map((r) => r.status === "fulfilled"
			? { status: r.status, value: r.value }
			: { status: r.status, code: r.reason.code, capabilityCode: r.reason.capabilityCode });
	`)
	want := `[{"status":"fulfilled","value":"a"},{"status":"rejected","code":"ENOENT","capabilityCode":"not_found"},{"status":"fulfilled","value":"b"}]`
	if got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestFacadeConcurrentToolCalls(t *testing.T) {
	const n = 3
	a := newBlockingAdapter(n)
	sb := newSandboxWithAdapter(t, a)
	got := execValue(t, sb, `
		const values = await Promise.all([
			fs.readFile("a", "utf8"),
			fs.readFile("b", "utf8"),
			fs.readFile("c", "utf8"),
		]);
		return values;
	`)
	want := `["a","b","c"]`
	if got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
	if max := a.maxConcurrent(); max != n {
		t.Fatalf("max concurrent tool calls = %d, want %d", max, n)
	}
}

func TestFacadeCollisionFailsInitialization(t *testing.T) {
	timeout := 10_000.0
	sb, err := codemoderuntime.NewCodemodeSandbox(&codemode.CodemodeSandboxOptions{
		TimeoutMs:            &timeout,
		TrustedPreludeSource: FacadeSource,
		Globals: []*codemode.CodemodeTool{{
			Name: "fs",
			Execute: func(context.Context, any) (any, error) {
				return nil, nil
			},
		}},
	})
	if err != nil {
		t.Fatalf("NewCodemodeSandbox: %v", err)
	}
	defer sb.Close()

	res, err := sb.Execute(context.Background(), "return 1", nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	fail, ok := res.(*codemode.CodemodeFailure)
	if !ok {
		t.Fatalf("expected failure, got %T", res)
	}
	if fail.Error.Kind != codemode.CodemodeErrorKindSandbox {
		t.Fatalf("kind = %q", fail.Error.Kind)
	}
	if !strings.Contains(fail.Error.Message, "Failed to initialize trusted prelude") {
		t.Fatalf("message = %q", fail.Error.Message)
	}
}

func TestDefaultSandboxHasNoFS(t *testing.T) {
	sb, err := codemoderuntime.NewCodemodeSandbox(nil)
	if err != nil {
		t.Fatalf("NewCodemodeSandbox: %v", err)
	}
	defer sb.Close()
	succ := execSuccess(t, sb, `return typeof globalThis.fs;`)
	val, _ := succ.Value.Get()
	if val != "undefined" {
		t.Fatalf("typeof fs = %v, want undefined", val)
	}
}

// sequenceAdapter returns the next capability error from a fixed list.
type sequenceAdapter struct {
	ops   []string
	mu    sync.Mutex
	codes []string
	index int
}

func (s *sequenceAdapter) Operations() []string { return s.ops }

func (s *sequenceAdapter) Handle(context.Context, string, map[string]any) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	code := s.codes[s.index]
	s.index++
	return nil, &capability.Error{Code: code, Message: "message for " + code}
}

// blockingAdapter blocks every call until n calls have arrived, proving the
// runtime dispatches tool calls concurrently.
type blockingAdapter struct {
	ops     []string
	n       int
	release chan struct{}
	once    sync.Once
	mu      sync.Mutex
	current int
	max     int
}

func newBlockingAdapter(n int) *blockingAdapter {
	return &blockingAdapter{
		ops:     []string{capability.OpReadText},
		n:       n,
		release: make(chan struct{}),
	}
}

func (b *blockingAdapter) Operations() []string { return b.ops }

func (b *blockingAdapter) Handle(ctx context.Context, _ string, args map[string]any) (any, error) {
	b.mu.Lock()
	b.current++
	if b.current > b.max {
		b.max = b.current
	}
	reached := b.current == b.n
	b.mu.Unlock()
	if reached {
		b.once.Do(func() { close(b.release) })
	}
	select {
	case <-b.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	b.mu.Lock()
	b.current--
	b.mu.Unlock()
	return map[string]any{"text": args["path"]}, nil
}

func (b *blockingAdapter) maxConcurrent() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.max
}

func jsonString(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return string(data)
}
