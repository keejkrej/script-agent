package runtime_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/keejkrej/pi-go/codemode"
	codemoderuntime "github.com/keejkrej/pi-go/codemode/runtime"
	"github.com/keejkrej/pi-go/internal/jsonx"
)

// tprelCodedError is a tool error that carries a stable code.
type tprelCodedError struct {
	code string
	msg  string
}

func (e tprelCodedError) Error() string { return e.msg }
func (e tprelCodedError) Code() string  { return e.code }

func tprelNewSandbox(t *testing.T, tools []*codemode.CodemodeTool, trusted string) *codemoderuntime.CodemodeSandbox {
	t.Helper()
	sb, err := codemoderuntime.NewCodemodeSandbox(&codemode.CodemodeSandboxOptions{
		Tools:                tools,
		TimeoutMs:            tsandFloatPtr(10_000),
		TrustedPreludeSource: trusted,
	})
	if err != nil {
		t.Fatalf("NewCodemodeSandbox: %v", err)
	}
	t.Cleanup(func() { _ = sb.Close() })
	return sb
}

func TestTrustedPreludeEmptyPreservesBehavior(t *testing.T) {
	ctx := context.Background()
	plain := tsandNewSandbox(t, []*codemode.CodemodeTool{tsandEchoTool()}, 10_000)
	empty := tprelNewSandbox(t, []*codemode.CodemodeTool{tsandEchoTool()}, "")

	code := "return [await tools.echo('x'), typeof globalThis.__trusted]"
	a := tsandExecSuccess(t, plain, ctx, code, nil)
	b := tsandExecSuccess(t, empty, ctx, code, nil)
	av, _ := a.Value.Get()
	bv, _ := b.Value.Get()
	if got, want := tsandToJSONString(t, bv), tsandToJSONString(t, av); got != want {
		t.Fatalf("empty trusted prelude changed behavior: got %s, want %s", got, want)
	}
	if got := tsandToJSONString(t, bv); got != `["x","undefined"]` {
		t.Fatalf("got %s", got)
	}
}

func TestTrustedPreludeRunsAfterPreludeBeforeUserCode(t *testing.T) {
	ctx := context.Background()
	sb := tprelNewSandbox(t, []*codemode.CodemodeTool{tsandEchoTool()}, `
		var __globalVar = 1;
		const __ns = Object.freeze({ value: 7 });
		globalThis.__readNs = () => __ns.value;
		globalThis.__seen = {
			tools: typeof tools,
			allTools: Array.isArray(ALL_TOOLS),
			console: typeof console,
			text: typeof text,
			store: typeof store,
			load: typeof load,
			bridge: typeof bridge,
			globalBridge: typeof globalThis.bridge,
		};
	`)

	s := tsandExecSuccess(t, sb, ctx, `
		try { __ns.value = 9; } catch {}
		return {
			seen: __seen,
			globalVar: globalThis.__globalVar,
			ns: [__readNs(), __ns.value],
			echo: await tools.echo("ok"),
		};
	`, nil)
	val, _ := s.Value.Get()
	want := `{"seen":{"tools":"object","allTools":true,"console":"object","text":"function","store":"function","load":"function","bridge":"undefined","globalBridge":"undefined"},"globalVar":1,"ns":[7,7],"echo":"ok"}`
	if got := tsandToJSONString(t, val); got != want {
		t.Fatalf("got %s\nwant %s", got, want)
	}
}

func TestTrustedPreludeCanUsePublicToolsAndAsync(t *testing.T) {
	ctx := context.Background()
	sb := tprelNewSandbox(t, []*codemode.CodemodeTool{tsandEchoTool()}, `
		globalThis.__echoPromise = tools.echo("from-prelude");
		globalThis.__bytes = () => Promise.resolve(new Uint8Array([1, 2, 3]).length);
	`)

	s := tsandExecSuccess(t, sb, ctx, `
		return [await __echoPromise, await __bytes()];
	`, nil)
	val, _ := s.Value.Get()
	if got := tsandToJSONString(t, val); got != `["from-prelude",3]` {
		t.Fatalf("got %s", got)
	}
	if len(s.Calls) != 1 || s.Calls[0].Name != "echo" || s.Calls[0].Status != codemode.CodemodeCallStatusOk {
		t.Fatalf("calls = %+v", s.Calls)
	}
}

func TestTrustedPreludeCanExposeToolErrorCode(t *testing.T) {
	ctx := context.Background()
	sb := tprelNewSandbox(t, []*codemode.CodemodeTool{
		{
			Name: "fail",
			Execute: func(_ context.Context, _ any) (any, error) {
				return nil, tprelCodedError{code: "E_FROM_TRUSTED", msg: "trusted wrapper"}
			},
		},
	}, `
		globalThis.__callCode = async (name) => {
			try { await tools[name](); return null; } catch (error) { return [error.message, error.code]; }
		};
	`)

	s := tsandExecSuccess(t, sb, ctx, `return await __callCode("fail");`, nil)
	val, _ := s.Value.Get()
	if got := tsandToJSONString(t, val); got != `["trusted wrapper","E_FROM_TRUSTED"]` {
		t.Fatalf("got %s", got)
	}
	if len(s.Calls) != 1 || s.Calls[0].Name != "fail" || s.Calls[0].Status != codemode.CodemodeCallStatusError {
		t.Fatalf("calls = %+v", s.Calls)
	}
}

func TestTrustedPreludeCanUseStoreAndOutput(t *testing.T) {
	ctx := context.Background()
	sb := tprelNewSandbox(t, nil, `
		globalThis.__bump = () => { const n = load("n") || 0; store("n", n + 1); return n + 1; };
		globalThis.__emit = () => { text("from trusted"); console.log("trusted log"); };
	`)

	s := tsandExecSuccess(t, sb, ctx, `
		__emit();
		return __bump();
	`, &codemode.CodemodeExecuteOptions{Store: map[string]any{"n": 41}})
	val, _ := s.Value.Get()
	if val != float64(42) {
		t.Fatalf("val = %v", val)
	}
	if len(s.Output) != 2 {
		t.Fatalf("output = %+v", s.Output)
	}
	if s.Output[0].(*codemode.CodemodeTextOutput).Text != "from trusted" || s.Output[1].(*codemode.CodemodeTextOutput).Text != "trusted log" {
		t.Fatalf("output = %+v", s.Output)
	}
	if v, ok := s.StoreWrites.Set.Get("n"); !ok || v != float64(42) {
		t.Fatalf("storeWrites = %+v", s.StoreWrites)
	}
}

func TestTrustedPreludeFreshPerExecution(t *testing.T) {
	ctx := context.Background()
	sb := tprelNewSandbox(t, nil, `globalThis.__count = (globalThis.__count || 0) + 1;`)

	for i := 0; i < 2; i++ {
		s := tsandExecSuccess(t, sb, ctx, "return globalThis.__count", nil)
		if val, _ := s.Value.Get(); val != float64(1) {
			t.Fatalf("execution %d: count = %v, want 1", i, val)
		}
	}
}

func TestTrustedPreludeToolSnapshotUnchanged(t *testing.T) {
	ctx := context.Background()
	sb := tprelNewSandbox(t, []*codemode.CodemodeTool{tsandEchoTool()}, `
		globalThis.__toolNames = Object.keys(tools);
		globalThis.__allTools = ALL_TOOLS.map((tool) => tool.name);
	`)

	s := tsandExecSuccess(t, sb, ctx, `
		return { names: __toolNames, all: __allTools, echo: await tools.echo("x") };
	`, nil)
	val, _ := s.Value.Get()
	if got := tsandToJSONString(t, val); got != `{"names":["echo"],"all":["echo"],"echo":"x"}` {
		t.Fatalf("got %s", got)
	}
	if tools := sb.Tools(); len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("tools = %+v", tools)
	}
}

func TestTrustedPreludeFailureIsSandboxFailure(t *testing.T) {
	ctx := context.Background()

	t.Run("syntax error", func(t *testing.T) {
		sb := tprelNewSandbox(t, nil, "const bad = ;")
		for i := 0; i < 2; i++ {
			f := tsandExecFailure(t, sb, ctx, "return 1", nil)
			if f.Error.Kind != codemode.CodemodeErrorKindSandbox {
				t.Fatalf("execution %d: kind = %q, message = %q", i, f.Error.Kind, f.Error.Message)
			}
			if !strings.Contains(f.Error.Message, "Failed to initialize trusted prelude") {
				t.Fatalf("message = %q", f.Error.Message)
			}
			if !strings.Contains(f.Error.Message, "codemode-trusted-prelude.js") {
				t.Fatalf("message missing filename: %q", f.Error.Message)
			}
		}
	})

	t.Run("runtime throw", func(t *testing.T) {
		sb := tprelNewSandbox(t, nil, "throw new Error('trusted boom');")
		f := tsandExecFailure(t, sb, ctx, "return 1", nil)
		if f.Error.Kind != codemode.CodemodeErrorKindSandbox {
			t.Fatalf("kind = %q", f.Error.Kind)
		}
		if !strings.Contains(f.Error.Message, "trusted boom") || !strings.Contains(f.Error.Message, "codemode-trusted-prelude.js") {
			t.Fatalf("message = %q", f.Error.Message)
		}
	})

	t.Run("global collision is deterministic", func(t *testing.T) {
		sb := tprelNewSandbox(t, nil, `Object.defineProperty(globalThis, "tools", { value: 1 });`)
		f := tsandExecFailure(t, sb, ctx, "return 1", nil)
		if f.Error.Kind != codemode.CodemodeErrorKindSandbox {
			t.Fatalf("kind = %q, message = %q", f.Error.Kind, f.Error.Message)
		}
		if !strings.Contains(f.Error.Message, "codemode-trusted-prelude.js") {
			t.Fatalf("message = %q", f.Error.Message)
		}
	})
}

func TestToolErrorCode(t *testing.T) {
	ctx := context.Background()

	t.Run("coded error attaches code and keeps message", func(t *testing.T) {
		sb := tprelNewSandbox(t, []*codemode.CodemodeTool{
			{
				Name: "fail",
				Execute: func(_ context.Context, _ any) (any, error) {
					return nil, tprelCodedError{code: "E_TOOL_FAIL", msg: "tool exploded"}
				},
			},
		}, "")
		s := tsandExecSuccess(t, sb, ctx, `
			try {
				await tools.fail();
				return "no error";
			} catch (error) {
				return { isError: error instanceof Error, message: error.message, code: error.code, hasCode: "code" in error };
			}
		`, nil)
		val, _ := s.Value.Get()
		if got := tsandToJSONString(t, val); got != `{"isError":true,"message":"tool exploded","code":"E_TOOL_FAIL","hasCode":true}` {
			t.Fatalf("got %s", got)
		}
		if len(s.Calls) != 1 || s.Calls[0].Name != "fail" || s.Calls[0].Status != codemode.CodemodeCallStatusError {
			t.Fatalf("calls = %+v", s.Calls)
		}
	})

	t.Run("wrapped coded error keeps code", func(t *testing.T) {
		sb := tprelNewSandbox(t, []*codemode.CodemodeTool{
			{
				Name: "fail",
				Execute: func(_ context.Context, _ any) (any, error) {
					return nil, fmt.Errorf("wrapped: %w", tprelCodedError{code: "E_WRAPPED", msg: "inner"})
				},
			},
		}, "")
		s := tsandExecSuccess(t, sb, ctx, `
			try { await tools.fail(); } catch (error) { return [error.message, error.code]; }
		`, nil)
		val, _ := s.Value.Get()
		if got := tsandToJSONString(t, val); got != `["wrapped: inner","E_WRAPPED"]` {
			t.Fatalf("got %s", got)
		}
	})

	t.Run("plain error has no code", func(t *testing.T) {
		sb := tprelNewSandbox(t, []*codemode.CodemodeTool{
			{
				Name: "fail",
				Execute: func(_ context.Context, _ any) (any, error) {
					return nil, errors.New("plain boom")
				},
			},
		}, "")
		s := tsandExecSuccess(t, sb, ctx, `
			try { await tools.fail(); } catch (error) { return [error.message, typeof error.code, "code" in error]; }
		`, nil)
		val, _ := s.Value.Get()
		if got := tsandToJSONString(t, val); got != `["plain boom","undefined",false]` {
			t.Fatalf("got %s", got)
		}
		if len(s.Calls) != 1 || s.Calls[0].Status != codemode.CodemodeCallStatusError {
			t.Fatalf("calls = %+v", s.Calls)
		}
	})

	t.Run("invalid code is dropped", func(t *testing.T) {
		sb := tprelNewSandbox(t, []*codemode.CodemodeTool{
			{
				Name: "fail",
				Execute: func(_ context.Context, _ any) (any, error) {
					return nil, tprelCodedError{code: "bad code!", msg: "bad code"}
				},
			},
		}, "")
		s := tsandExecSuccess(t, sb, ctx, `
			try { await tools.fail(); } catch (error) { return [error.message, typeof error.code, "code" in error]; }
		`, nil)
		val, _ := s.Value.Get()
		if got := tsandToJSONString(t, val); got != `["bad code","undefined",false]` {
			t.Fatalf("got %s", got)
		}
	})

	t.Run("oversized code is dropped", func(t *testing.T) {
		sb := tprelNewSandbox(t, []*codemode.CodemodeTool{
			{
				Name: "fail",
				Execute: func(_ context.Context, _ any) (any, error) {
					return nil, tprelCodedError{code: strings.Repeat("A", 65), msg: "long code"}
				},
			},
		}, "")
		s := tsandExecSuccess(t, sb, ctx, `
			try { await tools.fail(); } catch (error) { return [error.message, typeof error.code]; }
		`, nil)
		val, _ := s.Value.Get()
		if got := tsandToJSONString(t, val); got != `["long code","undefined"]` {
			t.Fatalf("got %s", got)
		}
	})
}

func TestCodemodeErrorCodeHelper(t *testing.T) {
	if got := codemode.CodemodeErrorCode(nil); got != "" {
		t.Fatalf("nil: got %q", got)
	}
	if got := codemode.CodemodeErrorCode(errors.New("plain")); got != "" {
		t.Fatalf("plain: got %q", got)
	}
	if got := codemode.CodemodeErrorCode(tprelCodedError{code: "E_X", msg: "m"}); got != "E_X" {
		t.Fatalf("coded: got %q", got)
	}
	if got := codemode.CodemodeErrorCode(fmt.Errorf("wrap: %w", tprelCodedError{code: "E_Y", msg: "m"})); got != "E_Y" {
		t.Fatalf("wrapped: got %q", got)
	}
}

func TestTrustedPreludeProtocolFields(t *testing.T) {
	h := &codemoderuntime.HostToWorkerMessage{
		Type:    "result",
		Id:      1,
		Ok:      false,
		Payload: tsandStrPtr("boom"),
		Code:    tsandStrPtr("E_X"),
	}
	encoded, err := jsonx.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(encoded), `{"type":"result","id":1,"ok":false,"payload":"boom","code":"E_X"}`; got != want {
		t.Fatalf("got %s, want %s", got, want)
	}

	noCode, err := jsonx.Marshal(&codemoderuntime.HostToWorkerMessage{Type: "result", Id: 2, Ok: true})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(noCode), `{"type":"result","id":2,"ok":true}`; got != want {
		t.Fatalf("got %s, want %s", got, want)
	}

	data, err := jsonx.Marshal(&codemoderuntime.WorkerData{Code: "return 1", TrustedPreludeSource: "var x = 1;"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"trustedPreludeSource":"var x = 1;"`) {
		t.Fatalf("missing trustedPreludeSource: %s", data)
	}

	empty, err := jsonx.Marshal(&codemoderuntime.WorkerData{Code: "return 1"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(empty), "trustedPreludeSource") {
		t.Fatalf("empty trustedPreludeSource should be omitted: %s", empty)
	}
}
