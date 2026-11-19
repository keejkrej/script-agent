//go:build scriptagent_integration

// Package scriptagent integration tests exercise the real Python, JavaScript,
// and Bash engines. They are opt-in because they compile WASM and spawn a
// process:
//
//	go test -tags scriptagent_integration ./...
package scriptagent

import (
	"context"
	"strings"
	"testing"
	"time"
)

func newIntegrationAgent(t *testing.T, allowWrites, allowProcess bool) *Agent {
	t.Helper()
	a, err := New(Config{
		Workspace:      t.TempDir(),
		AllowWrites:    allowWrites,
		AllowProcess:   allowProcess,
		Timeout:        20 * time.Second,
		MaxExecutions:  1,
		MaxOutputBytes: 1 << 20,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := a.Close(ctx); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return a
}

func TestIntegrationPython(t *testing.T) {
	a := newIntegrationAgent(t, false, false)
	res := a.Run(context.Background(), Request{
		Language: LanguagePython,
		Source:   "import quickpy\nprint('hello')\nquickpy.set_state({'n': 1})\n",
	})
	if res.Status != StatusCompleted {
		t.Fatalf("status = %s, error = %+v", res.Status, res.Error)
	}
	if len(res.Output) != 1 || res.Output[0].Text != "hello\n" {
		t.Fatalf("output = %+v", res.Output)
	}
	if !res.HasValue() {
		t.Fatal("missing state value")
	}
}

func TestIntegrationJavaScript(t *testing.T) {
	a := newIntegrationAgent(t, false, false)
	res := a.Run(context.Background(), Request{
		Language: LanguageJavaScript,
		Source:   "text('hi'); return 1 + 1;",
	})
	if res.Status != StatusCompleted {
		t.Fatalf("status = %s, error = %+v", res.Status, res.Error)
	}
	if !res.HasValue() || res.Value != float64(2) {
		t.Fatalf("value = %v", res.Value)
	}
	if len(res.Output) != 1 || res.Output[0].Text != "hi" {
		t.Fatalf("output = %+v", res.Output)
	}
}

func TestIntegrationBash(t *testing.T) {
	a := newIntegrationAgent(t, false, true)
	res := a.Run(context.Background(), Request{
		Language:     LanguageBash,
		Source:       "echo hello; echo err >&2",
		AllowProcess: true,
	})
	if res.Status != StatusCompleted {
		t.Fatalf("status = %s, error = %+v", res.Status, res.Error)
	}
	if res.ExitCode == nil || *res.ExitCode != 0 {
		t.Fatalf("exit code = %v", res.ExitCode)
	}
	var stdout, stderr string
	for _, o := range res.Output {
		switch o.Stream {
		case "stdout":
			stdout += o.Text
		case "stderr":
			stderr += o.Text
		}
	}
	if stdout != "hello\n" || stderr != "err\n" {
		t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
	}
	if len(res.Warnings) == 0 {
		t.Fatal("missing bash warning")
	}
}

func TestIntegrationReadOnlyDenied(t *testing.T) {
	a := newIntegrationAgent(t, false, false)
	res := a.Run(context.Background(), Request{
		Language: LanguagePython,
		Source:   "import quickpy\nasync def main():\n    await quickpy.fs.write_text('x.txt', 'hi')\nquickpy.run(main())\n",
	})
	if res.Status != StatusFailed {
		t.Fatalf("status = %s", res.Status)
	}
	if res.Error == nil || !strings.Contains(res.Error.Message, "13") {
		t.Fatalf("error = %+v", res.Error)
	}
}

func TestIntegrationScriptError(t *testing.T) {
	a := newIntegrationAgent(t, false, false)
	res := a.Run(context.Background(), Request{
		Language: LanguagePython,
		Source:   "raise ValueError('bad')\n",
	})
	if res.Status != StatusFailed {
		t.Fatalf("status = %s", res.Status)
	}
	if res.Error == nil || res.Error.Name != "ScriptError" || res.Error.Code != "ValueError" {
		t.Fatalf("error = %+v", res.Error)
	}
}

func TestIntegrationWritable(t *testing.T) {
	a := newIntegrationAgent(t, true, false)
	res := a.Run(context.Background(), Request{
		Language: LanguagePython,
		Source: "import quickpy\n" +
			"async def main():\n" +
			"    await quickpy.fs.write_text('x.txt', 'hi')\n" +
			"    print(await quickpy.fs.read_text('x.txt'))\n" +
			"quickpy.run(main())\n",
	})
	if res.Status != StatusCompleted {
		t.Fatalf("status = %s, error = %+v", res.Status, res.Error)
	}
	if len(res.Output) != 1 || res.Output[0].Text != "hi\n" {
		t.Fatalf("output = %+v", res.Output)
	}
}
